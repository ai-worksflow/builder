package logging

import (
	"log"
	"log/slog"
	"strings"
)

type slogWriter struct {
	logger *slog.Logger
}

func (w slogWriter) Write(value []byte) (int, error) {
	message := strings.TrimSpace(string(value))
	if message != "" {
		w.logger.Error("http server error", "message", message)
	}
	return len(value), nil
}

func StandardLogger(logger *slog.Logger) *log.Logger {
	return log.New(slogWriter{logger: logger}, "", 0)
}
