// Package logging constructs the process-wide structured logger.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Config controls logger presentation. Production uses JSON; local dev uses
// text. Both carry the same attributes and event names.
type Config struct {
	Level     string
	Format    string
	AddSource bool
}

// New returns a configured slog logger or an error for an invalid level or
// format. Misconfigured logging should fail startup instead of silently using
// a different verbosity.
func New(out io.Writer, cfg Config) (*slog.Logger, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: level, AddSource: cfg.AddSource}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "json":
		handler = slog.NewJSONHandler(out, opts)
	case "text":
		handler = slog.NewTextHandler(out, opts)
	default:
		return nil, fmt.Errorf("LOG_FORMAT must be json or text, got %q", cfg.Format)
	}
	return slog.New(handler), nil
}

func parseLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(strings.TrimSpace(raw)))); err != nil {
		return 0, fmt.Errorf("invalid LOG_LEVEL %q: %w", raw, err)
	}
	return level, nil
}
