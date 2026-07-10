package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/worksflow/builder/backend/internal/app"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/logging"
)

func main() {
	bootstrapLogger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.Load()
	if err != nil {
		bootstrapLogger.Error("configuration is invalid", "error", err)
		os.Exit(1)
	}

	logger, err := logging.New(cfg.Log, os.Stdout)
	if err != nil {
		bootstrapLogger.Error("logger configuration is invalid", "error", err)
		os.Exit(1)
	}
	logger = logger.With("service", cfg.ServiceName, "environment", cfg.Environment)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg, logger); err != nil {
		logger.Error("application stopped with an error", "error", err)
		os.Exit(1)
	}
}
