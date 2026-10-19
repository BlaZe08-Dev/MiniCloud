package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

func main() {
	// 1. Structured logging setup
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("starting MiniCloud coordinator...")

	// 2. Load and validate configuration
	cfg, err := config.LoadCoordinatorConfig()
	if err != nil {
		slog.Error("configuration validation failed", "err", err)
		os.Exit(1)
	}

	// 3. Initialize core components
	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	svc := coordinator.NewService(repo, sched, reg)
	server := api.NewServer(cfg.HTTPAddr, svc)

	// 4. Start HTTP Server in background goroutine
	go func() {
		slog.Info("coordinator listening for requests", "addr", cfg.HTTPAddr)
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	// 5. Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("received termination signal, initiating graceful shutdown", "signal", sig.String())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("error during server shutdown", "err", err)
		os.Exit(1)
	}

	slog.Info("coordinator stopped cleanly")
}
