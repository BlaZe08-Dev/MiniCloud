package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newMemoryRepository builds the in-memory repository with the configured attempt lease.
func newMemoryRepository(cfg *config.CoordinatorConfig) *storage.InMemoryRepository {
	return storage.NewInMemoryRepository(storage.WithLeaseDuration(cfg.LeaseDuration))
}

// configureService applies the lease and grace settings to the coordinator service. The lease
// duration given to the repository and to the service (poll metadata) must be the same value.
func configureService(svc *coordinator.Service, cfg *config.CoordinatorConfig) {
	svc.SetLeaseDuration(cfg.LeaseDuration)
	svc.SetGracePeriods(cfg.StartupGracePeriod, cfg.OutageGraceWindow)
}

// startBackground starts the worker reaper and the lease sweeper, each exactly once, and returns
// a stop function that cancels both and returns only after both loops have exited. stop is safe to
// call more than once.
func startBackground(ctx context.Context, svc *coordinator.Service, cfg *config.CoordinatorConfig) (stop func()) {
	bgCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		svc.StartReaper(bgCtx, cfg.ReaperInterval)
	}()
	go func() {
		defer wg.Done()
		svc.StartLeaseSweeper(bgCtx, cfg.SweeperInterval)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

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

	// 3. Initialize storage and core components
	var (
		repo storage.Repository
		pool *pgxpool.Pool
	)

	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	var svc *coordinator.Service

	if cfg.StorageDriver == "postgres" {
		bootCtx, bootCancel := context.WithTimeout(context.Background(), cfg.DBQueryTimeout)
		defer bootCancel()

		poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
		if err != nil {
			slog.Error("failed to parse database configuration", "err", err)
			os.Exit(1)
		}
		poolCfg.MaxConns = int32(cfg.DBMaxConns)
		poolCfg.MinConns = int32(cfg.DBMinConns)
		if cfg.DBQueryTimeout > 0 {
			if poolCfg.ConnConfig.RuntimeParams == nil {
				poolCfg.ConnConfig.RuntimeParams = make(map[string]string)
			}
			ms := strconv.Itoa(int(cfg.DBQueryTimeout.Milliseconds()))
			poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = ms
			poolCfg.ConnConfig.RuntimeParams["lock_timeout"] = ms
		}

		pool, err = pgxpool.NewWithConfig(bootCtx, poolCfg)
		if err != nil {
			slog.Error("failed to connect to postgres pool", "err", err)
			os.Exit(1)
		}

		if err := pool.Ping(bootCtx); err != nil {
			slog.Error("failed to ping postgres database", "err", err)
			os.Exit(1)
		}

		// Enforce schema version check on boot
		mig := postgres.NewMigrator()
		requiredVer, err := mig.LatestEmbeddedVersion()
		if err != nil {
			slog.Error("failed to inspect embedded migrations", "err", err)
			os.Exit(1)
		}

		currentVer, err := postgres.CurrentSchemaVersion(bootCtx, pool)
		if err != nil {
			slog.Error("failed to query database schema version", "err", err)
			os.Exit(1)
		}
		if currentVer < requiredVer {
			slog.Error("database schema is behind required version", "current", currentVer, "required", requiredVer)
			os.Exit(1)
		}

		slog.Info("connected to postgres storage", "schema_version", currentVer, "max_conns", cfg.DBMaxConns)
		repo = postgres.NewRepository(pool, postgres.WithLeaseDuration(cfg.LeaseDuration))
		svc = coordinator.NewPostgresService(repo, sched, reg, cfg.WorkerTimeout)
	} else {
		slog.Info("using in-memory storage driver")
		repo = newMemoryRepository(cfg)
		svc = coordinator.NewService(repo, sched, reg, cfg.WorkerTimeout)
	}

	configureService(svc, cfg)
	server := api.NewServer(cfg.HTTPAddr, svc)

	// Start the worker reaper and the lease sweeper (once; stopped on shutdown below).
	stopBackground := startBackground(context.Background(), svc, cfg)

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
		stopBackground()
		os.Exit(1)
	}

	// Stop the background loops and wait for them before the pool closes under them.
	stopBackground()

	if pool != nil {
		pool.Close()
	}

	slog.Info("coordinator stopped cleanly")
}
