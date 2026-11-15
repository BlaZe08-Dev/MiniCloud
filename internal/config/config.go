package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidGraceConfiguration is returned when a grace setting is shorter than the attempt lease
// duration (ADR-0014): grace must outlast a lease or a surviving worker's lease can lapse inside
// the grace and be reaped the moment it ends.
var ErrInvalidGraceConfiguration = errors.New("invalid grace configuration")

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

	// LeaseDuration is the attempt lease length (ADR-0014, default 30s).
	LeaseDuration time.Duration
	// SweeperInterval is the lease sweeper cadence (default 5s).
	SweeperInterval time.Duration
	// StartupGracePeriod suppresses lease reaping after coordinator start (default 30s).
	StartupGracePeriod time.Duration
	// OutageGraceWindow is the length of the reaping suppression, and of the cooldown that
	// follows it, after a database outage (default 30s).
	OutageGraceWindow time.Duration

	// DispatchPolicy selects round_robin (default, ADR-0017 Q1) or fifo_pull (ADR-0017 Section 5.2).
	DispatchPolicy string
	// Assignment settings (ADR-0017 Section 5.5). Validated in LoadCoordinatorConfig.
	AssignmentAcceptWindow time.Duration
	AssignmentInterval     time.Duration
	AssignmentMaxLapses    int
	AssignmentFallbackAge  time.Duration
	AssignmentBatchLimit   int
	AssignmentMinYield     time.Duration
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

	// The assignment variables (ADR-0017 Section 5.5) are parsed strictly: a set value that does
	// not parse is a startup error, not the default (F-5a-8). The first parse error is kept and
	// returned before validateAssignment. The older variables keep getIntEnv and getDurationEnv.
	var assignParseErr error
	strictDuration := func(key string, def time.Duration) time.Duration {
		v, err := lookupDurationEnvStrict(key, def)
		if err != nil && assignParseErr == nil {
			assignParseErr = err
		}
		return v
	}
	strictInt := func(key string, def int) int {
		v, err := lookupIntEnvStrict(key, def)
		if err != nil && assignParseErr == nil {
			assignParseErr = err
		}
		return v
	}

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

		LeaseDuration:      getDurationEnv("MINICLOUD_LEASE_DURATION", 30*time.Second),
		SweeperInterval:    getDurationEnv("MINICLOUD_SWEEPER_INTERVAL", 5*time.Second),
		StartupGracePeriod: getDurationEnv("MINICLOUD_STARTUP_GRACE_PERIOD", 30*time.Second),
		OutageGraceWindow:  getDurationEnv("MINICLOUD_OUTAGE_GRACE_WINDOW", 30*time.Second),

		DispatchPolicy:         dispatchPolicyFromEnv(),
		AssignmentAcceptWindow: strictDuration("MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW", 5*time.Second),
		AssignmentInterval:     strictDuration("MINICLOUD_ASSIGNMENT_INTERVAL", 1*time.Second),
		AssignmentMaxLapses:    strictInt("MINICLOUD_ASSIGNMENT_MAX_LAPSES", 3),
		AssignmentFallbackAge:  strictDuration("MINICLOUD_ASSIGNMENT_FALLBACK_AGE", 30*time.Second),
		AssignmentBatchLimit:   strictInt("MINICLOUD_ASSIGNMENT_BATCH_LIMIT", 50),
		AssignmentMinYield:     strictDuration("MINICLOUD_ASSIGNMENT_MIN_YIELD", 10*time.Millisecond),
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
	// The range is the one every job's max_attempts must lie in (chk_jobs_max_attempts).
	if cfg.DefaultMaxAttempts < 1 || cfg.DefaultMaxAttempts > 20 {
		return nil, fmt.Errorf("MINICLOUD_DEFAULT_MAX_ATTEMPTS must be between 1 and 20 (got %d)", cfg.DefaultMaxAttempts)
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
	if cfg.LeaseDuration <= 0 {
		return nil, fmt.Errorf("MINICLOUD_LEASE_DURATION must be positive")
	}
	if cfg.SweeperInterval <= 0 {
		return nil, fmt.Errorf("MINICLOUD_SWEEPER_INTERVAL must be positive")
	}
	if cfg.StartupGracePeriod < cfg.LeaseDuration {
		return nil, fmt.Errorf("%w: MINICLOUD_STARTUP_GRACE_PERIOD (%v) must be at least MINICLOUD_LEASE_DURATION (%v)", ErrInvalidGraceConfiguration, cfg.StartupGracePeriod, cfg.LeaseDuration)
	}
	if cfg.OutageGraceWindow < cfg.LeaseDuration {
		return nil, fmt.Errorf("%w: MINICLOUD_OUTAGE_GRACE_WINDOW (%v) must be at least MINICLOUD_LEASE_DURATION (%v)", ErrInvalidGraceConfiguration, cfg.OutageGraceWindow, cfg.LeaseDuration)
	}

	if assignParseErr != nil {
		return nil, assignParseErr
	}
	if err := validateAssignment(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validateAssignment checks the dispatch policy and the assignment variables (ADR-0017 Section 5.5),
// then the cross-variable rules of A1. Every rule is a startup error.
func validateAssignment(cfg *CoordinatorConfig) error {
	if cfg.DispatchPolicy != "fifo_pull" && cfg.DispatchPolicy != "round_robin" {
		return fmt.Errorf("MINICLOUD_DISPATCH_POLICY must be 'fifo_pull' or 'round_robin' (got %q)", cfg.DispatchPolicy)
	}
	if cfg.AssignmentAcceptWindow < 5*time.Second || cfg.AssignmentAcceptWindow > 30*time.Second {
		return fmt.Errorf("MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW must be between 5s and 30s (got %v)", cfg.AssignmentAcceptWindow)
	}
	if cfg.AssignmentInterval < 100*time.Millisecond || cfg.AssignmentInterval > 10*time.Second {
		return fmt.Errorf("MINICLOUD_ASSIGNMENT_INTERVAL must be between 100ms and 10s (got %v)", cfg.AssignmentInterval)
	}
	if cfg.AssignmentMaxLapses < 1 || cfg.AssignmentMaxLapses > 10 {
		return fmt.Errorf("MINICLOUD_ASSIGNMENT_MAX_LAPSES must be between 1 and 10 (got %d)", cfg.AssignmentMaxLapses)
	}
	lapsePathMinimum := time.Duration(cfg.AssignmentMaxLapses) * (cfg.AssignmentAcceptWindow + cfg.AssignmentInterval)
	if cfg.AssignmentFallbackAge < lapsePathMinimum || cfg.AssignmentFallbackAge > 10*time.Minute {
		return fmt.Errorf("MINICLOUD_ASSIGNMENT_FALLBACK_AGE must be at least MAX_LAPSES x (ACCEPT_WINDOW + INTERVAL) (%v) and at most 10m (got %v)", lapsePathMinimum, cfg.AssignmentFallbackAge)
	}
	if cfg.AssignmentBatchLimit < 1 || cfg.AssignmentBatchLimit > 500 {
		return fmt.Errorf("MINICLOUD_ASSIGNMENT_BATCH_LIMIT must be between 1 and 500 (got %d)", cfg.AssignmentBatchLimit)
	}
	if cfg.AssignmentMinYield < time.Millisecond || cfg.AssignmentMinYield > time.Second {
		return fmt.Errorf("MINICLOUD_ASSIGNMENT_MIN_YIELD must be between 1ms and 1s (got %v)", cfg.AssignmentMinYield)
	}
	if cfg.AssignmentMinYield > cfg.AssignmentInterval {
		return fmt.Errorf("MINICLOUD_ASSIGNMENT_MIN_YIELD (%v) must not exceed MINICLOUD_ASSIGNMENT_INTERVAL (%v)", cfg.AssignmentMinYield, cfg.AssignmentInterval)
	}

	// A1: the timing cross-rules.
	if cfg.AssignmentInterval > cfg.AssignmentAcceptWindow {
		return fmt.Errorf("MINICLOUD_ASSIGNMENT_INTERVAL (%v) must not exceed MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW (%v)", cfg.AssignmentInterval, cfg.AssignmentAcceptWindow)
	}
	if cfg.AssignmentFallbackAge <= 2*cfg.AssignmentInterval+cfg.AssignmentAcceptWindow {
		return fmt.Errorf("MINICLOUD_ASSIGNMENT_FALLBACK_AGE (%v) must be greater than 2 x MINICLOUD_ASSIGNMENT_INTERVAL plus MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW (%v)", cfg.AssignmentFallbackAge, 2*cfg.AssignmentInterval+cfg.AssignmentAcceptWindow)
	}
	return nil
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

// dispatchPolicyFromEnv returns the dispatch policy: round_robin when MINICLOUD_DISPATCH_POLICY is
// unset or empty (ADR-0017 Q1), otherwise the lower-cased value, which validateAssignment checks.
func dispatchPolicyFromEnv() string {
	v := strings.ToLower(os.Getenv("MINICLOUD_DISPATCH_POLICY"))
	if v == "" {
		return "round_robin"
	}
	return v
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

// lookupIntEnvStrict returns defaultVal when key is unset, and an error naming key when it is set
// to a value that does not parse as an integer (an empty value included).
func lookupIntEnvStrict(key string, defaultVal int) (int, error) {
	valStr, ok := os.LookupEnv(key)
	if !ok {
		return defaultVal, nil
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultVal, fmt.Errorf("%s must be an integer (got %q)", key, valStr)
	}
	return val, nil
}

// lookupDurationEnvStrict returns defaultVal when key is unset, and an error naming key when it is
// set to a value that time.ParseDuration rejects (an empty value or a number without a unit
// included).
func lookupDurationEnvStrict(key string, defaultVal time.Duration) (time.Duration, error) {
	valStr, ok := os.LookupEnv(key)
	if !ok {
		return defaultVal, nil
	}
	val, err := time.ParseDuration(valStr)
	if err != nil {
		return defaultVal, fmt.Errorf("%s must be a duration with a unit, such as 5s (got %q)", key, valStr)
	}
	return val, nil
}
