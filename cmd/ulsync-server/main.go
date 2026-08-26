// Command ulsync-server is the HTTP sync server for the ulsync protocol.
//
// Startup order: load YAML configuration, open the SQLite store (migrations
// run automatically), construct the token verifier, then listen for HTTP.
// Shutdown on SIGINT/SIGTERM drains in-flight HTTP requests before closing
// the database pools.
//
// Build with an injected version string:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" \
//	  -o ulsync-server ./cmd/ulsync-server
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/auth"
	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/httpapi"
	"github.com/ValeriusGC/ulsync-server/internal/store"
)

// version is set at link time via -ldflags "-X main.version=...".
// When unset, /health and -version report "dev".
var version = "dev"

func main() {
	os.Exit(run())
}

// run is the real entry point so main can exit with a status code without
// calling os.Exit from deferred cleanup paths.
func run() int {
	configPath := flag.String("config", "./config.yaml", "path to the YAML configuration file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return 0
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load configuration", "path", *configPath, "error", err)
		return 1
	}

	ctx := context.Background()
	db, err := store.Open(ctx, cfg.Storage)
	if err != nil {
		logger.Error("open storage", "error", err)
		return 1
	}
	defer db.Close()

	verifier, err := auth.NewVerifier(cfg.Auth, nil, logger)
	if err != nil {
		logger.Error("init token verifier", "error", err)
		return 1
	}

	startedAt := time.Now().UTC()
	srv := httpapi.New(cfg, db, verifier, version, startedAt)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Listen in a goroutine so the main goroutine can wait on signals or errors.
	errCh := make(chan error, 1)
	go func() {
		logger.Info("server listening", "bind", cfg.Server.Bind)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", err)
			return 1
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		// Allow in-flight requests up to 10s; after that Shutdown returns anyway.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			return 1
		}
		logger.Info("server stopped")
	}

	return 0
}
