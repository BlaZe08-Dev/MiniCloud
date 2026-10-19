package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// CoordinatorConfig defines startup configuration for the coordinator service.
type CoordinatorConfig struct {
	HTTPAddr           string
	HeartbeatInterval  time.Duration
	WorkerTimeout      time.Duration
	DefaultMaxAttempts int
	MaxJobRuntime      time.Duration
	LogLevel           string
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
	cfg := &CoordinatorConfig{
		HTTPAddr:           getEnv("MINICLOUD_HTTP_ADDR", "127.0.0.1:8080"),
		HeartbeatInterval:  getDurationEnv("MINICLOUD_HEARTBEAT_INTERVAL", 2*time.Second),
		WorkerTimeout:      getDurationEnv("MINICLOUD_WORKER_TIMEOUT", 10*time.Second),
		DefaultMaxAttempts: getIntEnv("MINICLOUD_DEFAULT_MAX_ATTEMPTS", 3),
		MaxJobRuntime:      getDurationEnv("MINICLOUD_MAX_JOB_RUNTIME", 60*time.Second),
		LogLevel:           getEnv("MINICLOUD_LOG_LEVEL", "info"),
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
	if cfg.DefaultMaxAttempts <= 0 {
		return nil, fmt.Errorf("MINICLOUD_DEFAULT_MAX_ATTEMPTS must be positive")
	}
	if cfg.MaxJobRuntime <= 0 {
		return nil, fmt.Errorf("MINICLOUD_MAX_JOB_RUNTIME must be positive")
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
	if val := os.Getenv(key); val != "" {
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
