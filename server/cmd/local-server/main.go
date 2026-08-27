package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	if err := run(); err != nil {
		slog.Error("local server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	databasePath, fileRoot, err := localPaths()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	workbench, err := localworkbench.NewServer(ctx, localworkbench.ServerConfig{
		DatabasePath: databasePath,
		FileRoot:     fileRoot,
		Version:      version,
		Commit:       commit,
	})
	if err != nil {
		return fmt.Errorf("initialize local workbench: %w", err)
	}
	defer func() {
		if err := workbench.Close(); err != nil {
			slog.Warn("close local workbench", "error", err)
		}
	}()

	address := strings.TrimSpace(os.Getenv("MULTICA_LOCAL_ADDR"))
	if address == "" {
		address = "127.0.0.1:8080"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("MULTICA_LOCAL_ADDR must be a loopback host and port")
	}
	server := &http.Server{
		Addr:              address,
		Handler:           workbench.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown local server: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve local workbench: %w", err)
	}
}

func localPaths() (string, string, error) {
	if databasePath := strings.TrimSpace(os.Getenv("MULTICA_SQLITE_PATH")); databasePath != "" {
		fileRoot := strings.TrimSpace(os.Getenv("MULTICA_FILE_ROOT"))
		if fileRoot == "" {
			fileRoot = filepath.Join(filepath.Dir(databasePath), "files")
		}
		return databasePath, fileRoot, nil
	}
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve local config directory: %w", err)
	}
	dataRoot := filepath.Join(configRoot, "Multica")
	return filepath.Join(dataRoot, "multica.db"), filepath.Join(dataRoot, "files"), nil
}
