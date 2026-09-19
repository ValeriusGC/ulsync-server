// Command ulsync-server is the HTTP sync server for the ulsync protocol.
//
// Startup order: parse flags; if the config file is missing, seed it from
// exactly one of -jwks-url or -shared-secret; load YAML; open the SQLite
// store (migrations run automatically); construct the token verifier; listen
// for HTTP. -version and -healthcheck skip config entirely and never seed.
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
	"io"
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
	return runMain(os.Args[1:], os.Stdout, os.Stderr)
}

// runMain is run with injectable args and streams so tests can assert exit
// codes without occupying flag.CommandLine or the process's real stderr.
func runMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ulsync-server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "./config.yaml", "path to the YAML configuration file")
	showVersion := fs.Bool("version", false, "print version and exit")
	doHealthcheck := fs.Bool("healthcheck", false, "GET http://127.0.0.1:8080/health and exit 0 only on HTTP 200")
	jwksURL := fs.String("jwks-url", "", "HTTPS JWKS URL used to seed a missing config file")
	sharedSecret := fs.String("shared-secret", "", "HS256 shared secret used to seed a missing config file")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if *doHealthcheck {
		return probeHealth(healthClient(), healthcheckURL)
	}

	if err := ensureConfig(*configPath, *jwksURL, *sharedSecret); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
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

	if err := db.BindAuthoredOrigin(ctx, cfg.Origin); err != nil {
		logger.Error(
			"authored store origin does not match database",
			"config_origin", cfg.Origin,
			"error", err,
		)
		return 1
	}

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
