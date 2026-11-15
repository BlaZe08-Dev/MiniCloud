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
	if cfg.DispatchPolicy != "round_robin" {
		t.Errorf("policy = %q, want round_robin (ADR-0017 Q1: the default flipped in P4-17)", cfg.DispatchPolicy)
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

// P4-17 (ADR-0017 Q1): unset and set-but-empty both select round_robin; fifo_pull is selected only
// by setting it, in any letter case; an invalid value is a startup error (also covered by the
// AssignmentRejections and AssignmentMalformedValuesRejected tables).
func TestLoadCoordinatorConfig_DispatchPolicySelection(t *testing.T) {
	cases := []struct {
		name  string
		env   map[string]string
		empty bool // set the variable to the empty string instead of leaving it unset
		want  string
	}{
		{"unset", nil, false, "round_robin"},
		{"set but empty", nil, true, "round_robin"},
		{"explicit round_robin", map[string]string{"MINICLOUD_DISPATCH_POLICY": "round_robin"}, false, "round_robin"},
		{"explicit fifo_pull", map[string]string{"MINICLOUD_DISPATCH_POLICY": "fifo_pull"}, false, "fifo_pull"},
		{"explicit FIFO_PULL", map[string]string{"MINICLOUD_DISPATCH_POLICY": "FIFO_PULL"}, false, "fifo_pull"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assignmentEnv(t, c.env)
			if c.empty {
				t.Setenv("MINICLOUD_DISPATCH_POLICY", "")
			}
			cfg, err := LoadCoordinatorConfig()
			if err != nil || cfg.DispatchPolicy != c.want {
				t.Fatalf("policy = %v err = %v, want %q", cfg, err, c.want)
			}
		})
	}
	t.Run("invalid value fails startup", func(t *testing.T) {
		assignmentEnv(t, map[string]string{"MINICLOUD_DISPATCH_POLICY": "lottery"})
		if _, err := LoadCoordinatorConfig(); err == nil {
			t.Fatal("policy lottery accepted, want a startup error")
		}
	})
}

// ADR-0017 Section 5.5: startup fails on any rule violation. A value that does not parse is not in
// range, so each new variable fails startup on a malformed value instead of falling back to its
// default (F-5a-8). A set but empty value is malformed too.
func TestLoadCoordinatorConfig_AssignmentMalformedValuesRejected(t *testing.T) {
	cases := []struct{ key, val string }{
		{"MINICLOUD_DISPATCH_POLICY", "round-robin"},
		{"MINICLOUD_DISPATCH_POLICY", "fifo-pull"},
		{"MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW", "5"},
		{"MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW", ""},
		{"MINICLOUD_ASSIGNMENT_INTERVAL", "1 second"},
		{"MINICLOUD_ASSIGNMENT_INTERVAL", ""},
		{"MINICLOUD_ASSIGNMENT_MAX_LAPSES", "three"},
		{"MINICLOUD_ASSIGNMENT_MAX_LAPSES", ""},
		{"MINICLOUD_ASSIGNMENT_FALLBACK_AGE", "30"},
		{"MINICLOUD_ASSIGNMENT_FALLBACK_AGE", ""},
		{"MINICLOUD_ASSIGNMENT_BATCH_LIMIT", "50x"},
		{"MINICLOUD_ASSIGNMENT_BATCH_LIMIT", ""},
		{"MINICLOUD_ASSIGNMENT_MIN_YIELD", "10"},
		{"MINICLOUD_ASSIGNMENT_MIN_YIELD", ""},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.val, func(t *testing.T) {
			assignmentEnv(t, map[string]string{c.key: c.val})
			_, err := LoadCoordinatorConfig()
			if err == nil {
				t.Fatalf("%s=%q accepted (the default was used silently)", c.key, c.val)
			}
			if !strings.Contains(err.Error(), c.key) {
				t.Fatalf("error does not name %s: %v", c.key, err)
			}
		})
	}
}

// A1 rule 1 equality: INTERVAL equal to ACCEPT_WINDOW does not exceed it, so it is accepted.
// 31 s clears the lapse-path minimum (3 x 10 s) and the rule 2 bound (2 x 5 s + 5 s).
func TestLoadCoordinatorConfig_AssignmentIntervalEqualToWindowAccepted(t *testing.T) {
	assignmentEnv(t, map[string]string{
		"MINICLOUD_ASSIGNMENT_INTERVAL":      "5s",
		"MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW": "5s",
		"MINICLOUD_ASSIGNMENT_FALLBACK_AGE":  "31s",
	})
	if _, err := LoadCoordinatorConfig(); err != nil {
		t.Fatalf("INTERVAL equal to ACCEPT_WINDOW rejected: %v", err)
	}
}
