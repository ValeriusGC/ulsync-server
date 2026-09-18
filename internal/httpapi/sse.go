package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/live"
	"github.com/ValeriusGC/ulsync-server/internal/store"
)

// sseWriteTimeout is the per-event write deadline. Ten seconds is enough for a
// slow network and not enough for a dead TCP connection the stack has not yet
// noticed. It lives in code, not YAML: a global http.Server.WriteTimeout would
// kill the stream by closing every connection that stays quiet between pings.
const sseWriteTimeout = 10 * time.Second

// sseCursorBody is the JSON object of an SSE cursor event (SPEC §4).
// server_now_ms is stamped when the cursor event is written, not when the
// stream opened, so a long quiet connection still yields a fresh sample.
type sseCursorBody struct {
	NextCursor int64 `json:"next_cursor"`
	// ServerNowMS is the store clock sample at this cursor event (SPEC §Server clock).
	ServerNowMS int64 `json:"server_now_ms"`
}

// serveLiveSSE writes the live=sse stream. Headers go out before any body so
// proxies see text/event-stream and do not buffer. Catch-up emits stored rows
// on this connection; the wait loop then reacts to Notify or heartbeat.
func serveLiveSSE(w http.ResponseWriter, r *http.Request, db *store.Store, userID string, since int64, limit int, heartbeat time.Duration, waiter *live.Waiter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	cursor, err := streamCatchUp(r.Context(), w, db, userID, since, limit)
	if err != nil {
		return
	}
	since = cursor

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	// The bearer token is checked only when the connection opens
	// (requireBearer). Re-verifying here would drop a still-valid stream
	// at exp; reopening with a fresh token is the client's job (step 13).
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := writeSSEPing(w); err != nil {
				return
			}
		case <-waiter.C():
			next, err := streamCatchUp(r.Context(), w, db, userID, since, limit)
			if err != nil {
				return
			}
			since = next
		}
	}
}

// streamCatchUp writes envelope events for every row after since, then a cursor
// event. While the page is full (len == limit) it immediately queries again so
// a client with more than one page of backlog is caught up on this connection
// instead of hanging until the next push. A short or empty page (zero envelope
// events plus one cursor) ends the burst.
func streamCatchUp(ctx context.Context, w http.ResponseWriter, db *store.Store, userID string, since int64, limit int) (int64, error) {
	for {
		rows, cursor, err := db.Since(ctx, userID, since, limit)
		if err != nil {
			return since, err
		}
		for _, env := range rows {
			if err := writeSSEEnvelope(w, toPullEnvelope(env)); err != nil {
				return cursor, err
			}
		}
		if err := writeSSECursor(w, cursor); err != nil {
			return cursor, err
		}
		since = cursor
		if len(rows) < limit {
			return since, nil
		}
	}
}

// writeSSEFrame sets a per-write deadline, writes payload, and flushes.
// http.ErrNotSupported is ignored so httptest writers still work; any other
// deadline, write, or flush error ends the stream (defer Unsubscribe runs).
func writeSSEFrame(w http.ResponseWriter, payload []byte) error {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// writeSSEEnvelope emits one named envelope event. json.Marshal is used
// instead of json.NewEncoder: Encoder appends a newline that would break
// the SSE frame (event / data / blank line).
func writeSSEEnvelope(w http.ResponseWriter, env pullEnvelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("event: envelope\ndata: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return writeSSEFrame(w, buf.Bytes())
}

// writeSSECursor emits the cursor event that closes a burst of envelopes.
func writeSSECursor(w http.ResponseWriter, cursor int64) error {
	data, err := json.Marshal(sseCursorBody{
		NextCursor:  cursor,
		ServerNowMS: serverNowMs(),
	})
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("event: cursor\ndata: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return writeSSEFrame(w, buf.Bytes())
}

// writeSSEPing writes the heartbeat comment. Clients do not deliver SSE
// comments to application code; the bytes exist so carrier NAT does not
// drop a silent connection.
func writeSSEPing(w http.ResponseWriter) error {
	return writeSSEFrame(w, []byte(": ping\n\n"))
}
