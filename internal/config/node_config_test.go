package config_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
)

func TestConfig_NodeLocalOnly_Table(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"127.0.0.2:1", true},
		{"[::1]:8080", true},
		{"0.0.0.0:8080", false},
		{":8080", false},
		{"[::]:8080", false},
		{"localhost:8080", false},
		{"192.168.1.5:8080", false},
		{"example.com:8080", false},
		{"not an address", false},
		{"", false},
		{"127.0.0.1", false}, // no port: unparsable
	}
	for _, c := range cases {
		if got := config.IsLocalOnlyAddr(c.addr); got != c.want {
			t.Errorf("IsLocalOnlyAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestConfig_NodeLocalOnly_ComputedOnceFromHTTPAddr(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:8080": true, "0.0.0.0:8080": false} {
		clearEnv()
		t.Setenv("MINICLOUD_HTTP_ADDR", addr)
		cfg, err := config.LoadCoordinatorConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.NodeLocalOnly != want {
			t.Errorf("addr %s: NodeLocalOnly = %v, want %v", addr, cfg.NodeLocalOnly, want)
		}
	}
}

func TestConfig_NodeIntervals_Validation(t *testing.T) {
	const key = "MINICLOUD_NODE_HARDWARE_MIN_INTERVAL"
	clearEnv()
	cfg, err := config.LoadCoordinatorConfig()
	if err != nil || cfg.NodeHardwareMinInterval != 60*time.Second {
		t.Fatalf("default = %v (err=%v), want 60s", cfg.NodeHardwareMinInterval, err)
	}
	t.Setenv(key, "90s")
	if cfg, err := config.LoadCoordinatorConfig(); err != nil || cfg.NodeHardwareMinInterval != 90*time.Second {
		t.Fatalf("90s = %v (err=%v)", cfg.NodeHardwareMinInterval, err)
	}
	for _, v := range []string{"0s", "-5s", "30", "garbage"} {
		t.Setenv(key, v)
		_, err := config.LoadCoordinatorConfig()
		if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), v) {
			t.Errorf("value %q: err = %v, want an error naming the key and the value", v, err)
		}
	}
	// Worker side: MINICLOUD_NODE_TELEMETRY_INTERVAL, examined only when reporting is on.
	const tkey = "MINICLOUD_NODE_TELEMETRY_INTERVAL"
	for _, v := range []string{"0s", "-5s", "5", "garbage"} {
		unsetNodeWorkerEnv(t)
		t.Setenv("MINICLOUD_NODE_REPORTING_ENABLED", "true")
		t.Setenv(tkey, v)
		if _, err := config.LoadWorkerConfig(); err == nil || !strings.Contains(err.Error(), tkey) {
			t.Errorf("interval %q: err = %v, want an error naming the key", v, err)
		}
		t.Setenv("MINICLOUD_NODE_REPORTING_ENABLED", "false")
		if _, err := config.LoadWorkerConfig(); err != nil {
			t.Errorf("interval %q with reporting off: %v, want it ignored", v, err)
		}
	}
	for _, set := range []bool{false, true} {
		unsetNodeWorkerEnv(t)
		t.Setenv("MINICLOUD_NODE_REPORTING_ENABLED", "true")
		if set {
			t.Setenv(tkey, "")
		}
		if cfg, err := config.LoadWorkerConfig(); err != nil || cfg.NodeTelemetryInterval != 0 {
			t.Errorf("unset/empty (set=%v): %v, %v, want 0", set, cfg, err)
		}
	}
	unsetNodeWorkerEnv(t)
	t.Setenv("MINICLOUD_NODE_REPORTING_ENABLED", "true")
	t.Setenv(tkey, "45s")
	if cfg, err := config.LoadWorkerConfig(); err != nil || cfg.NodeTelemetryInterval != 45*time.Second {
		t.Errorf("45s: %v, %v", cfg, err)
	}
}

func unsetNodeWorkerEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MINICLOUD_NODE_REPORTING_ENABLED", "MINICLOUD_NODE_STATE_DIR", "MINICLOUD_NODE_TELEMETRY_INTERVAL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestConfig_NodeReportingEnabled_StrictParse(t *testing.T) {
	const key = "MINICLOUD_NODE_REPORTING_ENABLED"
	unsetNodeWorkerEnv(t)
	if cfg, err := config.LoadWorkerConfig(); err != nil || cfg.NodeReportingEnabled {
		t.Fatalf("unset: %v, %v, want false", cfg, err)
	}
	for v, want := range map[string]bool{"": false, "true": true, "TRUE": true, "True": true, "false": false, "FALSE": false} {
		t.Setenv(key, v)
		if cfg, err := config.LoadWorkerConfig(); err != nil || cfg.NodeReportingEnabled != want {
			t.Errorf("%q: %v, %v, want %v", v, cfg, err, want)
		}
	}
	for _, v := range []string{"1", "yes", "on", " true", "ture", "true "} {
		t.Setenv(key, v)
		_, err := config.LoadWorkerConfig()
		if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), `"`+v+`"`) {
			t.Errorf("%q: err = %v, want an error naming the key and the value", v, err)
		}
	}
}

func TestConfig_NodeStateDir_MustBeAbsolute(t *testing.T) {
	const key = "MINICLOUD_NODE_STATE_DIR"
	unsetNodeWorkerEnv(t)
	t.Setenv("MINICLOUD_NODE_REPORTING_ENABLED", "true")
	t.Setenv(key, "relative/dir")
	if _, err := config.LoadWorkerConfig(); err == nil || !strings.Contains(err.Error(), key) {
		t.Errorf("relative with reporting on: err = %v, want an error naming the key", err)
	}
	t.Setenv(key, "/abs/dir")
	if cfg, err := config.LoadWorkerConfig(); err != nil || cfg.NodeStateDir != "/abs/dir" {
		t.Errorf("absolute: %v, %v", cfg, err)
	}
	t.Setenv("MINICLOUD_NODE_REPORTING_ENABLED", "false")
	t.Setenv(key, "relative/dir")
	if cfg, err := config.LoadWorkerConfig(); err != nil || cfg.NodeStateDir != "relative/dir" {
		t.Errorf("relative with reporting off: %v, %v, want it stored unexamined", cfg, err)
	}
}

func TestConfig_NodeMaxCount_Validation(t *testing.T) {
	const key = "MINICLOUD_NODE_MAX_COUNT"
	clearEnv()
	cfg, err := config.LoadCoordinatorConfig()
	if err != nil || cfg.NodeMaxCount != 10000 {
		t.Fatalf("default = %d (err=%v), want 10000", cfg.NodeMaxCount, err)
	}
	t.Setenv(key, "25")
	if cfg, err := config.LoadCoordinatorConfig(); err != nil || cfg.NodeMaxCount != 25 {
		t.Fatalf("25 = %d (err=%v)", cfg.NodeMaxCount, err)
	}
	for _, v := range []string{"0", "-1", "1.5", "ten", "99999999999999999999"} {
		t.Setenv(key, v)
		_, err := config.LoadCoordinatorConfig()
		if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), v) {
			t.Errorf("value %q: err = %v, want an error naming the key and the value", v, err)
		}
	}
}
