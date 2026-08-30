// Command holdconns opens N idle live SSE connections to measure server RSS per
// connection. k6 spends one VU and its own heap per stream, so measuring the
// server through k6 would describe the generator, not this process.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	// openBatch is how many connections are started before a short pause so the
	// listen backlog and file-descriptor ramp stay under control.
	openBatch = 100

	// openPause separates batches so "too many open files" reflects ulimit, not
	// an instantaneous connect storm misread as server failure.
	openPause = 10 * time.Millisecond

	// headerTimeout bounds only the response headers wait, not the full SSE hold.
	headerTimeout = 10 * time.Second
)

func main() {
	n := flag.Int("n", 1000, "number of live SSE connections to open")
	url := flag.String("url", "http://127.0.0.1:8080", "server base URL")
	tokensPath := flag.String("tokens", "load/tokens.json", "JSON array of bearer tokens")
	flag.Parse()

	if err := run(*n, *url, *tokensPath); err != nil {
		fmt.Fprintf(os.Stderr, "holdconns: %v\n", err)
		os.Exit(1)
	}
}

// run dials wanted SSE streams and blocks until SIGINT or SIGTERM.
func run(wanted int, baseURL, tokensPath string) error {
	if wanted < 1 {
		return fmt.Errorf("-n must be at least 1, got %d", wanted)
	}

	tokens, err := readTokens(tokensPath)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return fmt.Errorf("no tokens in %s", tokensPath)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: headerTimeout,
			DisableCompression:    true,
		},
	}

	var (
		mu          sync.Mutex
		established int
		bodies      []io.Closer
	)

	for i := 0; i < wanted; i++ {
		select {
		case <-ctx.Done():
			closeAll(bodies)
			return nil
		default:
		}

		token := tokens[i%len(tokens)]
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/sync/pull?since=0&live=sse", nil)
		if err != nil {
			return fmt.Errorf("build request %d: %w", i, err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "text/event-stream")

		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connection %d failed: %v\n", i, err)
		} else if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "connection %d status %d\n", i, resp.StatusCode)
			resp.Body.Close()
		} else {
			mu.Lock()
			established++
			bodies = append(bodies, resp.Body)
			count := established
			mu.Unlock()
			if count%openBatch == 0 {
				fmt.Printf("established progress=%d\n", count)
			}
			go discardSSE(ctx, resp.Body)
		}

		if (i+1)%openBatch == 0 {
			time.Sleep(openPause)
		}
	}

	fmt.Printf("established=%d wanted=%d\n", established, wanted)

	<-ctx.Done()
	closeAll(bodies)
	return nil
}

// readTokens loads the gentokens JSON array of bearer strings.
func readTokens(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tokens: %w", err)
	}
	var tokens []string
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return nil, fmt.Errorf("parse tokens: %w", err)
	}
	return tokens, nil
}

// discardSSE reads the SSE body until ctx is cancelled so TCP windows stay open.
func discardSSE(ctx context.Context, body io.ReadCloser) {
	defer body.Close()
	reader := bufio.NewReader(body)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, err := reader.ReadBytes('\n'); err != nil {
			return
		}
	}
}

// closeAll shuts down every held response body.
func closeAll(bodies []io.Closer) {
	for _, c := range bodies {
		_ = c.Close()
	}
}
