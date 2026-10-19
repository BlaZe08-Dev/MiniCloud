package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
)

func main() {
	// 1. Structured logging setup
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// 2. Load and validate configuration
	cfg, err := config.LoadWorkerConfig()
	if err != nil {
		slog.Error("worker configuration validation failed", "err", err)
		os.Exit(1)
	}

	if cfg.WorkerID == "" {
		hostname, _ := os.Hostname()
		cfg.WorkerID = fmt.Sprintf("worker_%s_%d", hostname, os.Getpid())
	}

	slog.Info("starting MiniCloud worker process...", "worker_id", cfg.WorkerID)

	// 3. Initialize allowlisted task registry
	registry := executor.NewRegistry()

	// 4. Create and run worker agent with signal cancellation
	agent := worker.NewAgent(cfg, registry)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("received termination signal, stopping worker agent", "signal", sig.String())
		cancel()
	}()

	if err := agent.Run(ctx); err != nil {
		slog.Error("worker agent terminated with error", "err", err)
		os.Exit(1)
	}

	slog.Info("worker process exited cleanly")
}
