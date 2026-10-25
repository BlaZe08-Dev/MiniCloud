package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// CoordinatorConfig defines startup configuration for the coordinator service.
type CoordinatorConfig struct {
	HTTPAddr           string
	HeartbeatInterval  time.Duration
	WorkerTimeout      time.Duration
	ReaperInterval     time.Duration
	DefaultMaxAttempts int
	MaxJobRuntime      time.Duration
	LogLevel           string
	StorageDriver      string
	DatabaseURL        string
	DBMaxConns         int
	DBMinConns         int
	DBQueryTimeout     time.Duration
}

// WorkerConfig defines startup configuration for the worker agent.
type WorkerConfig struct {
	WorkerID          string
	CoordinatorURL    string
	TotalSlots        int
	HeartbeatInterval time.Duration
	LogLevel          string
}

// LoadCoordinatorConfig loads and validates configuration from environment variables.
func LoadCoordinatorConfig() (*CoordinatorConfig, error) {
	hbInterval := getDurationEnv("MINICLOUD_HEARTBEAT_INTERVAL", 2*time.Second)
	workerTimeout := getDurationEnv("MINICLOUD_WORKER_TIMEOUT", 10*time.Second)

	// Default reaper interval is at most half the worker timeout
	defaultReaperInterval := workerTimeout / 2
	if defaultReaperInterval <= 0 {
		defaultReaperInterval = 1 * time.Second
	}
	reaperInterval := getDurationEnv("MINICLOUD_REAPER_INTERVAL", defaultReaperInterval)

	storageDriver := strings.ToLower(getEnv("MINICLOUD_STORAGE_DRIVER", "memory"))
	if storageDriver == "" {
		storageDriver = "memory"
	}
	dbURL := getEnv("MINICLOUD_DATABASE_URL", "")
	dbMaxConns := getIntEnv("MINICLOUD_DB_MAX_CONNS", 20)
	dbMinConns := getIntEnv("MINICLOUD_DB_MIN_CONNS", 2)
	dbQueryTimeout := getDurationEnv("MINICLOUD_DB_QUERY_TIMEOUT", 5*time.Second)

	cfg := &CoordinatorConfig{
		HTTPAddr:           getEnv("MINICLOUD_HTTP_ADDR", "127.0.0.1:8080"),
		HeartbeatInterval:  hbInterval,
		WorkerTimeout:      workerTimeout,
		ReaperInterval:     reaperInterval,
		DefaultMaxAttempts: getIntEnv("MINICLOUD_DEFAULT_MAX_ATTEMPTS", 3),
		MaxJobRuntime:      getDurationEnv("MINICLOUD_MAX_JOB_RUNTIME", 60*time.Second),
		LogLevel:           getEnv("MINICLOUD_LOG_LEVEL", "info"),
		StorageDriver:      storageDriver,
		DatabaseURL:        dbURL,
		DBMaxConns:         dbMaxConns,
		DBMinConns:         dbMinConns,
		DBQueryTimeout:     dbQueryTimeout,
	}

	if cfg.HTTPAddr == "" {
		return nil, fmt.Errorf("MINICLOUD_HTTP_ADDR cannot be empty")
	}
	if cfg.HeartbeatInterval <= 0 {
		return nil, fmt.Errorf("MINICLOUD_HEARTBEAT_INTERVAL must be positive")
	}
	if cfg.WorkerTimeout <= 0 {
		return nil, fmt.Errorf("MINICLOUD_WORKER_TIMEOUT must be positive")
	}
	if cfg.WorkerTimeout < 3*cfg.HeartbeatInterval {
		return nil, fmt.Errorf("MINICLOUD_WORKER_TIMEOUT (%v) must be at least 3x MINICLOUD_HEARTBEAT_INTERVAL (%v)", cfg.WorkerTimeout, cfg.HeartbeatInterval)
	}
	if cfg.ReaperInterval <= 0 {
		return nil, fmt.Errorf("MINICLOUD_REAPER_INTERVAL must be positive")
	}
	if cfg.ReaperInterval > cfg.WorkerTimeout {
		return nil, fmt.Errorf("MINICLOUD_REAPER_INTERVAL (%v) must not exceed MINICLOUD_WORKER_TIMEOUT (%v)", cfg.ReaperInterval, cfg.WorkerTimeout)
	}
	if cfg.DefaultMaxAttempts <= 0 {
		return nil, fmt.Errorf("MINICLOUD_DEFAULT_MAX_ATTEMPTS must be positive")
	}
	if cfg.MaxJobRuntime <= 0 {
		return nil, fmt.Errorf("MINICLOUD_MAX_JOB_RUNTIME must be positive")
	}
	if cfg.StorageDriver != "memory" && cfg.StorageDriver != "postgres" {
		return nil, fmt.Errorf("MINICLOUD_STORAGE_DRIVER must be either 'memory' or 'postgres' (got %q)", cfg.StorageDriver)
	}
	if cfg.StorageDriver == "postgres" && cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("MINICLOUD_DATABASE_URL cannot be empty when storage driver is 'postgres'")
	}
	if cfg.DBMaxConns <= 0 {
		return nil, fmt.Errorf("MINICLOUD_DB_MAX_CONNS must be positive")
	}
	if cfg.DBMinConns < 0 {
		return nil, fmt.Errorf("MINICLOUD_DB_MIN_CONNS cannot be negative")
	}
	if cfg.DBMinConns > cfg.DBMaxConns {
		return nil, fmt.Errorf("MINICLOUD_DB_MIN_CONNS (%d) must not exceed MINICLOUD_DB_MAX_CONNS (%d)", cfg.DBMinConns, cfg.DBMaxConns)
	}
	if cfg.DBQueryTimeout <= 0 {
		return nil, fmt.Errorf("MINICLOUD_DB_QUERY_TIMEOUT must be positive")
	}

	return cfg, nil
}

// LoadWorkerConfig loads and validates worker configuration from environment variables.
func LoadWorkerConfig() (*WorkerConfig, error) {
	cfg := &WorkerConfig{
		WorkerID:          getEnv("MINICLOUD_WORKER_ID", ""),
		CoordinatorURL:    getEnv("MINICLOUD_COORDINATOR_URL", "http://127.0.0.1:8080"),
		TotalSlots:        getIntEnv("MINICLOUD_WORKER_SLOTS", 4),
		HeartbeatInterval: getDurationEnv("MINICLOUD_HEARTBEAT_INTERVAL", 2*time.Second),
		LogLevel:          getEnv("MINICLOUD_LOG_LEVEL", "info"),
	}

	if cfg.CoordinatorURL == "" {
		return nil, fmt.Errorf("MINICLOUD_COORDINATOR_URL cannot be empty")
	}
	if cfg.TotalSlots <= 0 {
		return nil, fmt.Errorf("MINICLOUD_WORKER_SLOTS must be positive, got %d", cfg.TotalSlots)
	}
	if cfg.HeartbeatInterval <= 0 {
		return nil, fmt.Errorf("MINICLOUD_HEARTBEAT_INTERVAL must be positive")
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val, exists := os.LookupEnv(key); exists {
		return val
	}
	return defaultVal
}

func getIntEnv(key string, defaultVal int) int {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}

func getDurationEnv(key string, defaultVal time.Duration) time.Duration {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := time.ParseDuration(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}
