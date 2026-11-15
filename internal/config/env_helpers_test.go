package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

const envHelperKey = "MINICLOUD_TEST_ENV_HELPER"

// setHelperEnv sets the helper key to val, or leaves it unset when set is false.
func setHelperEnv(t *testing.T, set bool, val string) {
	t.Helper()
	t.Setenv(envHelperKey, "placeholder") // registers the cleanup that restores the original
	if set {
		t.Setenv(envHelperKey, val)
		return
	}
	os.Unsetenv(envHelperKey)
}

type envCase struct {
	name    string
	set     bool
	val     string
	wantErr bool
}

// intCases are the SEC-062 inputs; the want column differs only for zero and negative between
// the plain and the positive helper.
func TestGetIntEnv_Table(t *testing.T) {
	const def = 7
	cases := []struct {
		envCase
		want int
	}{
		{envCase{name: "unset"}, def},
		{envCase{name: "empty", set: true, val: ""}, def},
		{envCase{name: "valid", set: true, val: "12"}, 12},
		{envCase{name: "unparsable", set: true, val: "8080a", wantErr: true}, def},
		{envCase{name: "zero", set: true, val: "0"}, 0},
		{envCase{name: "negative", set: true, val: "-3"}, -3},
		{envCase{name: "overflow", set: true, val: "99999999999999999999", wantErr: true}, def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setHelperEnv(t, tc.set, tc.val)
			got, err := getIntEnv(envHelperKey, def)
			checkEnvResult(t, err, tc.wantErr, tc.val, got == tc.want, got)
		})
	}
}

func TestGetPositiveIntEnv_Table(t *testing.T) {
	const def = 7
	cases := []struct {
		envCase
		want int
	}{
		{envCase{name: "unset"}, def},
		{envCase{name: "empty", set: true, val: ""}, def},
		{envCase{name: "valid", set: true, val: "12"}, 12},
		{envCase{name: "unparsable", set: true, val: "8080a", wantErr: true}, def},
		{envCase{name: "zero", set: true, val: "0", wantErr: true}, def},
		{envCase{name: "negative", set: true, val: "-3", wantErr: true}, def},
		{envCase{name: "overflow", set: true, val: "99999999999999999999", wantErr: true}, def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setHelperEnv(t, tc.set, tc.val)
			got, err := getPositiveIntEnv(envHelperKey, def)
			checkEnvResult(t, err, tc.wantErr, tc.val, got == tc.want, got)
		})
	}
}

func TestGetDurationEnv_Table(t *testing.T) {
	const def = 7 * time.Second
	cases := []struct {
		envCase
		want time.Duration
	}{
		{envCase{name: "unset"}, def},
		{envCase{name: "empty", set: true, val: ""}, def},
		{envCase{name: "valid", set: true, val: "250ms"}, 250 * time.Millisecond},
		{envCase{name: "unparsable", set: true, val: "5sec", wantErr: true}, def},
		{envCase{name: "no_unit", set: true, val: "5", wantErr: true}, def},
		{envCase{name: "zero", set: true, val: "0s"}, 0},
		{envCase{name: "negative", set: true, val: "-5s"}, -5 * time.Second},
		{envCase{name: "overflow", set: true, val: "99999999999h", wantErr: true}, def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setHelperEnv(t, tc.set, tc.val)
			got, err := getDurationEnv(envHelperKey, def)
			checkEnvResult(t, err, tc.wantErr, tc.val, got == tc.want, got)
		})
	}
}

func TestGetPositiveDurationEnv_Table(t *testing.T) {
	const def = 7 * time.Second
	cases := []struct {
		envCase
		want time.Duration
	}{
		{envCase{name: "unset"}, def},
		{envCase{name: "empty", set: true, val: ""}, def},
		{envCase{name: "valid", set: true, val: "250ms"}, 250 * time.Millisecond},
		{envCase{name: "unparsable", set: true, val: "5sec", wantErr: true}, def},
		{envCase{name: "zero", set: true, val: "0s", wantErr: true}, def},
		{envCase{name: "negative", set: true, val: "-5s", wantErr: true}, def},
		{envCase{name: "overflow", set: true, val: "99999999999h", wantErr: true}, def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setHelperEnv(t, tc.set, tc.val)
			got, err := getPositiveDurationEnv(envHelperKey, def)
			checkEnvResult(t, err, tc.wantErr, tc.val, got == tc.want, got)
		})
	}
}

// checkEnvResult asserts the error expectation, that a failure names the variable and the bad
// value, and that the returned value matches.
func checkEnvResult(t *testing.T, err error, wantErr bool, val string, valueOK bool, got any) {
	t.Helper()
	if wantErr {
		if err == nil {
			t.Fatalf("value %q: want an error, got none (value %v)", val, got)
		}
		if !strings.Contains(err.Error(), envHelperKey) || !strings.Contains(err.Error(), val) {
			t.Fatalf("error %q must name %s and the value %q", err, envHelperKey, val)
		}
		return
	}
	if err != nil {
		t.Fatalf("value %q: unexpected error %v", val, err)
	}
	if !valueOK {
		t.Fatalf("value %q: got %v", val, got)
	}
}

// TestLoadCoordinatorConfig_OldVariablesRejectBadValue: every older coordinator variable that
// used to fall back silently now fails Load and names the variable and the bad value (SEC-062).
func TestLoadCoordinatorConfig_OldVariablesRejectBadValue(t *testing.T) {
	vars := []string{
		"MINICLOUD_HEARTBEAT_INTERVAL", "MINICLOUD_WORKER_TIMEOUT", "MINICLOUD_REAPER_INTERVAL",
		"MINICLOUD_MAX_JOB_RUNTIME", "MINICLOUD_DB_QUERY_TIMEOUT", "MINICLOUD_LEASE_DURATION",
		"MINICLOUD_SWEEPER_INTERVAL", "MINICLOUD_STARTUP_GRACE_PERIOD", "MINICLOUD_OUTAGE_GRACE_WINDOW",
		"MINICLOUD_DB_MAX_CONNS", "MINICLOUD_DB_MIN_CONNS", "MINICLOUD_DEFAULT_MAX_ATTEMPTS",
	}
	for _, name := range vars {
		bad := "12x"
		t.Run(name, func(t *testing.T) {
			assignmentEnv(t, nil)
			t.Setenv(name, bad)
			_, err := LoadCoordinatorConfig()
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), bad) {
				t.Fatalf("error = %v, want one naming %s and %q", err, name, bad)
			}
		})
		t.Run(name+"=empty_is_default", func(t *testing.T) {
			assignmentEnv(t, nil)
			t.Setenv(name, "")
			if _, err := LoadCoordinatorConfig(); err != nil {
				t.Fatalf("empty %s must give the default, got %v", name, err)
			}
		})
	}
}

func TestLoadWorkerConfig_OldVariablesRejectBadValue(t *testing.T) {
	for _, name := range []string{"MINICLOUD_WORKER_SLOTS", "MINICLOUD_HEARTBEAT_INTERVAL"} {
		for _, bad := range []string{"12x", "0", "-1"} {
			t.Run(name+"="+bad, func(t *testing.T) {
				t.Setenv("MINICLOUD_WORKER_SLOTS", "")
				t.Setenv("MINICLOUD_HEARTBEAT_INTERVAL", "")
				t.Setenv(name, bad)
				_, err := LoadWorkerConfig()
				if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), bad) {
					t.Fatalf("error = %v, want one naming %s and %q", err, name, bad)
				}
			})
		}
	}
	t.Run("empty_is_default", func(t *testing.T) {
		t.Setenv("MINICLOUD_WORKER_SLOTS", "")
		t.Setenv("MINICLOUD_HEARTBEAT_INTERVAL", "")
		cfg, err := LoadWorkerConfig()
		if err != nil || cfg.TotalSlots != 4 || cfg.HeartbeatInterval != 2*time.Second {
			t.Fatalf("got %+v, %v; want the defaults", cfg, err)
		}
	})
}
