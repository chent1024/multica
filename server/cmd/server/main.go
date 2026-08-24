package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/multica-ai/multica/server/internal/localworkbench"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	databasePath, err := localworkbench.DefaultDatabasePath()
	if err != nil {
		logger.Error("resolve local database path", "error", err)
		os.Exit(1)
	}
	app, err := localworkbench.New(context.Background(), databasePath, logger)
	if err != nil {
		logger.Error("open local workbench", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := app.Close(); err != nil {
			logger.Warn("close local workbench", "error", err)
		}
	}()
	if os.Getenv("MULTICA_INIT_ONLY") == "1" {
		logger.Info("local SQLite workbench initialized", "database", databasePath)
		return
	}

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	server := &http.Server{
		Addr:              "127.0.0.1:" + port,
		Handler:           app.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Warn("shutdown local server", "error", err)
		}
	}()

	logger.Info("local SQLite workbench listening", "addr", server.Addr, "database", databasePath, "version", version, "commit", commit)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("local server stopped", "error", err)
		os.Exit(1)
	}
}
