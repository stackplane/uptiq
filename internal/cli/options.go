package cli

import (
	"fmt"
	"log/slog"
	"os"
)

// Options holds CLI flags.
type Options struct {
	ConfigPath string
	Listen     string
	LogLevel   string
	Watch      bool
}

// DefaultOptions returns default CLI options.
func DefaultOptions() Options {
	return Options{
		ConfigPath: "configs/example.yaml",
		LogLevel:   "info",
	}
}

// NewLogger creates a structured logger from a log level string.
func NewLogger(level string) (*slog.Logger, error) {
	var lvl slog.Level

	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("invalid log level: %q (use debug|info|warn|error)", level)
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler), nil
}
