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

	"github.com/ValeriusGC/ulsync-server/internal/admin"
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
	syncSrv := httpapi.New(cfg, db, verifier, version, startedAt)
	adminSrv := admin.New(cfg, db, verifier, version, startedAt, syncSrv.Metrics(), syncSrv.Registry(), admin.Options{})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Both listeners share one shutdown path so SIGTERM stops sync and admin together.
	errCh := make(chan error, 2)
	go func() {
		logger.Info("server listening", "bind", cfg.Server.Bind)
		errCh <- syncSrv.ListenAndServe()
	}()
	go func() {
		logger.Info("admin listening", "bind", cfg.Admin.Bind)
		errCh <- adminSrv.ListenAndServe()
	}()

	shutdownBoth := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := syncSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("sync graceful shutdown failed", "error", err)
		}
		if err := adminSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("admin graceful shutdown failed", "error", err)
		}
		logger.Info("server stopped")
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listener stopped unexpectedly", "error", err)
			shutdownBoth()
			return 1
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		shutdownBoth()
	}

	return 0
}
