package config_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
)

func clearEnv() {
	os.Unsetenv("MINICLOUD_HTTP_ADDR")
	os.Unsetenv("MINICLOUD_HEARTBEAT_INTERVAL")
	os.Unsetenv("MINICLOUD_WORKER_TIMEOUT")
	os.Unsetenv("MINICLOUD_REAPER_INTERVAL")
	os.Unsetenv("MINICLOUD_DEFAULT_MAX_ATTEMPTS")
	os.Unsetenv("MINICLOUD_MAX_JOB_RUNTIME")
	os.Unsetenv("MINICLOUD_LOG_LEVEL")
	os.Unsetenv("MINICLOUD_STORAGE_DRIVER")
	os.Unsetenv("MINICLOUD_DATABASE_URL")
	os.Unsetenv("MINICLOUD_DB_MAX_CONNS")
	os.Unsetenv("MINICLOUD_DB_MIN_CONNS")
	os.Unsetenv("MINICLOUD_DB_QUERY_TIMEOUT")
}

func TestLoadCoordinatorConfig_Defaults(t *testing.T) {
	clearEnv()
	cfg, err := config.LoadCoordinatorConfig()
	if err != nil {
		t.Fatalf("expected valid default config, got error: %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Errorf("expected default HTTPAddr 127.0.0.1:8080, got %s", cfg.HTTPAddr)
	}
	if cfg.HeartbeatInterval != 2*time.Second {
		t.Errorf("expected default HeartbeatInterval 2s, got %v", cfg.HeartbeatInterval)
	}
	if cfg.WorkerTimeout != 10*time.Second {
		t.Errorf("expected default WorkerTimeout 10s, got %v", cfg.WorkerTimeout)
	}
	// Default reaper interval is at most half the worker timeout (5s)
	if cfg.ReaperInterval != 5*time.Second {
		t.Errorf("expected default ReaperInterval 5s (half of timeout), got %v", cfg.ReaperInterval)
	}
	if cfg.StorageDriver != "memory" {
		t.Errorf("expected default StorageDriver 'memory', got %s", cfg.StorageDriver)
	}
	if cfg.DBMaxConns != 20 {
		t.Errorf("expected default DBMaxConns 20, got %d", cfg.DBMaxConns)
	}
	if cfg.DBMinConns != 2 {
		t.Errorf("expected default DBMinConns 2, got %d", cfg.DBMinConns)
	}
	if cfg.DBQueryTimeout != 5*time.Second {
		t.Errorf("expected default DBQueryTimeout 5s, got %v", cfg.DBQueryTimeout)
	}
}

func TestLoadCoordinatorConfig_ValidationFailures(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		errContains string
	}{
		{
			name: "worker timeout less than 3x heartbeat interval",
			env: map[string]string{
				"MINICLOUD_HEARTBEAT_INTERVAL": "2s",
				"MINICLOUD_WORKER_TIMEOUT":     "5s", // < 6s
			},
			errContains: "must be at least 3x MINICLOUD_HEARTBEAT_INTERVAL",
		},
		{
			name: "reaper interval greater than worker timeout",
			env: map[string]string{
				"MINICLOUD_HEARTBEAT_INTERVAL": "1s",
				"MINICLOUD_WORKER_TIMEOUT":     "5s",
				"MINICLOUD_REAPER_INTERVAL":    "6s",
			},
			errContains: "must not exceed MINICLOUD_WORKER_TIMEOUT",
		},
		{
			name: "empty http address",
			env: map[string]string{
				"MINICLOUD_HTTP_ADDR": "",
			},
			errContains: "MINICLOUD_HTTP_ADDR cannot be empty",
		},
		{
			name: "invalid storage driver",
			env: map[string]string{
				"MINICLOUD_STORAGE_DRIVER": "redis",
			},
			errContains: "MINICLOUD_STORAGE_DRIVER must be either 'memory' or 'postgres'",
		},
		{
			name: "postgres driver without database url",
			env: map[string]string{
				"MINICLOUD_STORAGE_DRIVER": "postgres",
				"MINICLOUD_DATABASE_URL":   "",
			},
			errContains: "MINICLOUD_DATABASE_URL cannot be empty",
		},
		{
			name: "non-positive db max conns",
			env: map[string]string{
				"MINICLOUD_DB_MAX_CONNS": "0",
			},
			errContains: "MINICLOUD_DB_MAX_CONNS must be positive",
		},
		{
			name: "negative db min conns",
			env: map[string]string{
				"MINICLOUD_DB_MIN_CONNS": "-1",
			},
			errContains: "MINICLOUD_DB_MIN_CONNS cannot be negative",
		},
		{
			name: "db min conns exceeds max conns",
			env: map[string]string{
				"MINICLOUD_DB_MAX_CONNS": "5",
				"MINICLOUD_DB_MIN_CONNS": "10",
			},
			errContains: "must not exceed MINICLOUD_DB_MAX_CONNS",
		},
		{
			name: "non-positive db query timeout",
			env: map[string]string{
				"MINICLOUD_DB_QUERY_TIMEOUT": "0s",
			},
			errContains: "MINICLOUD_DB_QUERY_TIMEOUT must be positive",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv()
			for k, v := range tc.env {
				os.Setenv(k, v)
			}
			defer clearEnv()

			_, err := config.LoadCoordinatorConfig()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}
