package logging

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/worksflow/builder/backend/internal/config"
)

func New(cfg config.LogConfig, output io.Writer) (*slog.Logger, error) {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("unsupported log level %q", cfg.Level)
	}

	options := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(output, options)
	case "text":
		handler = slog.NewTextHandler(output, options)
	default:
		return nil, fmt.Errorf("unsupported log format %q", cfg.Format)
	}
	return slog.New(handler), nil
}
