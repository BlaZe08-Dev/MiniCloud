package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// assignmentEnv sets a valid coordinator environment with the round-robin variables overridden
// by the given values. The base variables are the ones LoadCoordinatorConfig already requires.
func assignmentEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	base := map[string]string{
		"MINICLOUD_STORAGE_DRIVER": "memory",
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
	// t.Setenv restores the previous value at cleanup; Unsetenv makes the default apply, since
	// getEnv treats a set but empty variable as a value.
	for _, k := range []string{
		"MINICLOUD_DISPATCH_POLICY", "MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW", "MINICLOUD_ASSIGNMENT_INTERVAL",
		"MINICLOUD_ASSIGNMENT_MAX_LAPSES", "MINICLOUD_ASSIGNMENT_FALLBACK_AGE", "MINICLOUD_ASSIGNMENT_BATCH_LIMIT",
		"MINICLOUD_ASSIGNMENT_MIN_YIELD",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	for k, v := range overrides {
		t.Setenv(k, v)
	}
}

func TestLoadCoordinatorConfig_AssignmentDefaults(t *testing.T) {
	assignmentEnv(t, nil)
	cfg, err := LoadCoordinatorConfig()
	if err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	if cfg.DispatchPolicy != "fifo_pull" {
		t.Errorf("policy = %q, want fifo_pull", cfg.DispatchPolicy)
	}
	if cfg.AssignmentAcceptWindow != 5*time.Second || cfg.AssignmentInterval != time.Second ||
		cfg.AssignmentMaxLapses != 3 || cfg.AssignmentFallbackAge != 30*time.Second ||
		cfg.AssignmentBatchLimit != 50 || cfg.AssignmentMinYield != 10*time.Millisecond {
		t.Errorf("defaults differ from ADR-0017 Section 5.5: %+v", cfg)
	}
}

// Per-variable rules of ADR-0017 Section 5.5. Each row is one startup error.
func TestLoadCoordinatorConfig_AssignmentRejections(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"policy not one of the two", map[string]string{"MINICLOUD_DISPATCH_POLICY": "random"}},
		{"accept window below 5s", map[string]string{"MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW": "4s"}},
		{"accept window above 30s", map[string]string{"MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW": "31s"}},
		{"interval below 100ms", map[string]string{"MINICLOUD_ASSIGNMENT_INTERVAL": "99ms"}},
		{"interval above 10s", map[string]string{"MINICLOUD_ASSIGNMENT_INTERVAL": "10001ms"}},
		{"max lapses zero", map[string]string{"MINICLOUD_ASSIGNMENT_MAX_LAPSES": "0"}},
		{"max lapses above 10", map[string]string{"MINICLOUD_ASSIGNMENT_MAX_LAPSES": "11"}},
		{"fallback age below the lapse path minimum", map[string]string{"MINICLOUD_ASSIGNMENT_FALLBACK_AGE": "17s"}},
		{"fallback age above 10 min", map[string]string{"MINICLOUD_ASSIGNMENT_FALLBACK_AGE": "601s"}},
		{"batch limit zero", map[string]string{"MINICLOUD_ASSIGNMENT_BATCH_LIMIT": "0"}},
		{"batch limit above 500", map[string]string{"MINICLOUD_ASSIGNMENT_BATCH_LIMIT": "501"}},
		{"min yield below 1ms", map[string]string{"MINICLOUD_ASSIGNMENT_MIN_YIELD": "0s"}},
		{"min yield above 1s", map[string]string{"MINICLOUD_ASSIGNMENT_MIN_YIELD": "1001ms"}},
		{"min yield above interval", map[string]string{"MINICLOUD_ASSIGNMENT_INTERVAL": "100ms", "MINICLOUD_ASSIGNMENT_MIN_YIELD": "200ms"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assignmentEnv(t, c.env)
			if _, err := LoadCoordinatorConfig(); err == nil {
				t.Fatalf("accepted an invalid configuration")
			}
		})
	}
}

// ADR-0017 A1: interval must not exceed accept window, and fallback age must be greater than
// 2 x interval plus the accept window. Each case below breaks only the cross rule.
func TestLoadCoordinatorConfig_AssignmentCrossValidation(t *testing.T) {
	t.Run("interval above accept window is rejected", func(t *testing.T) {
		assignmentEnv(t, map[string]string{
			"MINICLOUD_ASSIGNMENT_INTERVAL":      "6s",
			"MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW": "5s",
			// 40 s clears the lapse-path minimum (3 x 11 s) and the cross rule, so only A1 fails.
			"MINICLOUD_ASSIGNMENT_FALLBACK_AGE": "40s",
		})
		_, err := LoadCoordinatorConfig()
		if err == nil || !strings.Contains(err.Error(), "MINICLOUD_ASSIGNMENT_INTERVAL") || !strings.Contains(err.Error(), "MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW") {
			t.Fatalf("expected an error naming both variables, got %v", err)
		}
	})
	t.Run("fallback age equal to 2 x interval plus window is rejected", func(t *testing.T) {
		// With MAX_LAPSES=1 the lapse-path minimum is 1 x (5 s + 1 s) = 6 s, so only the cross rule
		// (2 x 1 s + 5 s = 7 s) fails at 7 s.
		assignmentEnv(t, map[string]string{
			"MINICLOUD_ASSIGNMENT_MAX_LAPSES":   "1",
			"MINICLOUD_ASSIGNMENT_FALLBACK_AGE": "7s",
		})
		if _, err := LoadCoordinatorConfig(); err == nil {
			t.Fatalf("fallback age equal to 2 x interval + window was accepted")
		}
	})
	t.Run("fallback age just above 2 x interval plus window is accepted", func(t *testing.T) {
		assignmentEnv(t, map[string]string{
			"MINICLOUD_ASSIGNMENT_MAX_LAPSES":   "1",
			"MINICLOUD_ASSIGNMENT_FALLBACK_AGE": "8s",
		})
		if _, err := LoadCoordinatorConfig(); err != nil {
			t.Fatalf("valid configuration rejected: %v", err)
		}
	})
	t.Run("valid round_robin configuration is accepted", func(t *testing.T) {
		assignmentEnv(t, map[string]string{"MINICLOUD_DISPATCH_POLICY": "round_robin"})
		cfg, err := LoadCoordinatorConfig()
		if err != nil || cfg.DispatchPolicy != "round_robin" {
			t.Fatalf("round_robin rejected: policy=%v err=%v", cfg, err)
		}
	})
}
