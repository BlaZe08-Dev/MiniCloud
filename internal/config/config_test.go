package config_test

import (
	"errors"
	"os"
	"strconv"
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
	os.Unsetenv("MINICLOUD_LEASE_DURATION")
	os.Unsetenv("MINICLOUD_SWEEPER_INTERVAL")
	os.Unsetenv("MINICLOUD_STARTUP_GRACE_PERIOD")
	os.Unsetenv("MINICLOUD_OUTAGE_GRACE_WINDOW")
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

func TestLoadCoordinatorConfig_LeaseDefaults(t *testing.T) {
	clearEnv()
	cfg, err := config.LoadCoordinatorConfig()
	if err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if cfg.LeaseDuration != 30*time.Second {
		t.Errorf("LeaseDuration = %v, want 30s", cfg.LeaseDuration)
	}
	if cfg.SweeperInterval != 5*time.Second {
		t.Errorf("SweeperInterval = %v, want 5s", cfg.SweeperInterval)
	}
	if cfg.StartupGracePeriod != 30*time.Second {
		t.Errorf("StartupGracePeriod = %v, want 30s", cfg.StartupGracePeriod)
	}
	if cfg.OutageGraceWindow != 30*time.Second {
		t.Errorf("OutageGraceWindow = %v, want 30s", cfg.OutageGraceWindow)
	}
}

func TestLoadCoordinatorConfig_LeaseSettingsFromEnv(t *testing.T) {
	clearEnv()
	t.Setenv("MINICLOUD_LEASE_DURATION", "20s")
	t.Setenv("MINICLOUD_SWEEPER_INTERVAL", "2s")
	t.Setenv("MINICLOUD_STARTUP_GRACE_PERIOD", "45s")
	t.Setenv("MINICLOUD_OUTAGE_GRACE_WINDOW", "20s")
	cfg, err := config.LoadCoordinatorConfig()
	if err != nil {
		t.Fatalf("valid lease settings rejected: %v", err)
	}
	if cfg.LeaseDuration != 20*time.Second || cfg.SweeperInterval != 2*time.Second ||
		cfg.StartupGracePeriod != 45*time.Second || cfg.OutageGraceWindow != 20*time.Second {
		t.Fatalf("lease settings not read from the environment: %+v", cfg)
	}
}

// TestConfig_Validation_GracePeriodLessThanLeaseDuration_Rejected pins ADR-0014: both grace
// settings must be at least the lease duration, otherwise a surviving worker's lease can lapse
// before grace ends and be reaped; equality is allowed.
func TestConfig_Validation_GracePeriodLessThanLeaseDuration_Rejected(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantError string // substring naming the offending setting; empty means valid
	}{
		{"startup grace below lease", map[string]string{"MINICLOUD_LEASE_DURATION": "30s", "MINICLOUD_STARTUP_GRACE_PERIOD": "29s"}, "MINICLOUD_STARTUP_GRACE_PERIOD"},
		{"outage window below lease", map[string]string{"MINICLOUD_LEASE_DURATION": "30s", "MINICLOUD_OUTAGE_GRACE_WINDOW": "29s"}, "MINICLOUD_OUTAGE_GRACE_WINDOW"},
		{"startup grace zero", map[string]string{"MINICLOUD_STARTUP_GRACE_PERIOD": "0s"}, "MINICLOUD_STARTUP_GRACE_PERIOD"},
		{"longer lease than default grace", map[string]string{"MINICLOUD_LEASE_DURATION": "60s"}, "MINICLOUD_STARTUP_GRACE_PERIOD"},
		{"startup grace equal to lease", map[string]string{"MINICLOUD_LEASE_DURATION": "30s", "MINICLOUD_STARTUP_GRACE_PERIOD": "30s", "MINICLOUD_OUTAGE_GRACE_WINDOW": "30s"}, ""},
		{"both above lease", map[string]string{"MINICLOUD_LEASE_DURATION": "10s", "MINICLOUD_STARTUP_GRACE_PERIOD": "11s", "MINICLOUD_OUTAGE_GRACE_WINDOW": "10s"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv()
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := config.LoadCoordinatorConfig()
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("valid config rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("config accepted, want rejection naming %s", tc.wantError)
			}
			if !errors.Is(err, config.ErrInvalidGraceConfiguration) {
				t.Errorf("error %v is not ErrInvalidGraceConfiguration", err)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("error %q does not name %s", err, tc.wantError)
			}
		})
	}
}

func TestConfig_Validation_NonPositiveLeaseSettings_Rejected(t *testing.T) {
	for _, name := range []string{"MINICLOUD_LEASE_DURATION", "MINICLOUD_SWEEPER_INTERVAL"} {
		for _, val := range []string{"0s", "-5s"} {
			t.Run(name+"="+val, func(t *testing.T) {
				clearEnv()
				t.Setenv(name, val)
				_, err := config.LoadCoordinatorConfig()
				if err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("error = %v, want rejection naming %s", err, name)
				}
			})
		}
	}
}

// TestConfig_DefaultMaxAttempts_ValidatedAgainstJobRange: MINICLOUD_DEFAULT_MAX_ATTEMPTS is read
// from the environment and must lie in the 1..20 range every job's max_attempts must (SQL check
// chk_jobs_max_attempts, docs/security/field-limits.md); a default no submission could use is
// refused at startup.
func TestConfig_DefaultMaxAttempts_ValidatedAgainstJobRange(t *testing.T) {
	for _, val := range []string{"1", "3", "20"} {
		t.Run("accepts_"+val, func(t *testing.T) {
			clearEnv()
			t.Setenv("MINICLOUD_DEFAULT_MAX_ATTEMPTS", val)
			cfg, err := config.LoadCoordinatorConfig()
			if err != nil {
				t.Fatalf("expected %s to be accepted, got %v", val, err)
			}
			if strconv.Itoa(cfg.DefaultMaxAttempts) != val {
				t.Fatalf("DefaultMaxAttempts = %d, want %s", cfg.DefaultMaxAttempts, val)
			}
		})
	}
	for _, val := range []string{"0", "-1", "21", "1000"} {
		t.Run("rejects_"+val, func(t *testing.T) {
			clearEnv()
			t.Setenv("MINICLOUD_DEFAULT_MAX_ATTEMPTS", val)
			_, err := config.LoadCoordinatorConfig()
			if err == nil || !strings.Contains(err.Error(), "MINICLOUD_DEFAULT_MAX_ATTEMPTS") {
				t.Fatalf("error = %v, want rejection naming MINICLOUD_DEFAULT_MAX_ATTEMPTS", err)
			}
		})
	}
}
