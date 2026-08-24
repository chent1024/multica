package logger

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/lmittmann/tint"
)

// isTerminal reports whether the given file descriptor is connected to a
// terminal. Used to suppress ANSI color escapes when stderr is redirected
// to a file (e.g. daemon.log), so log files stay clean.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Init initializes the global slog logger. Colors are enabled when stderr
// is a terminal and disabled otherwise. Reads LOG_LEVEL env var (debug,
// info, warn, error). Default: debug.
func Init() {
	level := parseLevel(os.Getenv("LOG_LEVEL"))
	handler := tint.NewHandler(os.Stderr, &tint.Options{
		Level:      level,
		TimeFormat: "15:04:05.000",
		NoColor:    !isTerminal(os.Stderr),
	})
	slog.SetDefault(slog.New(handler))
}

// NewLogger creates a named slog logger. Colors follow the same
// TTY-detection rule as Init. Useful for standalone processes (daemon,
// migrate) that want a component prefix.
func NewLogger(component string) *slog.Logger {
	level := parseLevel(os.Getenv("LOG_LEVEL"))
	handler := tint.NewHandler(os.Stderr, &tint.Options{
		Level:      level,
		TimeFormat: "15:04:05.000",
		NoColor:    !isTerminal(os.Stderr),
	})
	return slog.New(handler).With("component", component)
}

// StderrIsTerminal reports whether this process's stderr is attached to a
// terminal. The daemon uses it to distinguish a user running
// `daemon start --foreground` in a shell (log live to the terminal) from a
// detached/background child whose stderr is redirected to a file (rotate into
// daemon.log instead).
func StderrIsTerminal() bool {
	return isTerminal(os.Stderr)
}

// NewWriterLoggerDefault builds a named slog logger that writes structured,
// color-free output to w, and installs the same handler as the global slog
// default so bare slog.Info/Warn/... calls (e.g. from LoadConfig) land in the
// same sink. Intended for standalone processes that log to a file or rotating
// writer instead of a terminal (the daemon), where ANSI color is never wanted
// and every log line — injected-logger and package-global alike — must end up
// in the one managed file. Reads LOG_LEVEL like NewLogger/Init.
func NewWriterLoggerDefault(component string, w io.Writer) *slog.Logger {
	level := parseLevel(os.Getenv("LOG_LEVEL"))
	handler := tint.NewHandler(w, &tint.Options{
		Level:      level,
		TimeFormat: "15:04:05.000",
		NoColor:    true,
	})
	base := slog.New(handler)
	slog.SetDefault(base)
	return base.With("component", component)
}

// RequestAttrs extracts request and client metadata without depending on the
// retired cloud middleware package. It remains available to legacy source
// files, but the local server does not require identity or tenant context.
func RequestAttrs(r *http.Request) []any {
	attrs := make([]any, 0, 10)
	if rid := chimw.GetReqID(r.Context()); rid != "" {
		attrs = append(attrs, "request_id", rid)
	}
	if uid := r.Header.Get("X-User-ID"); uid != "" {
		attrs = append(attrs, "user_id", uid)
	}
	platform := r.Header.Get("X-Client-Platform")
	version := r.Header.Get("X-Client-Version")
	os := r.Header.Get("X-Client-OS")
	if platform != "" {
		attrs = append(attrs, "client_platform", platform)
	}
	if version != "" {
		attrs = append(attrs, "client_version", version)
	}
	if os != "" {
		attrs = append(attrs, "client_os", os)
	}
	return attrs
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}
