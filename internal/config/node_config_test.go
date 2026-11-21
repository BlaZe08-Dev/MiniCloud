package config_test

import (
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
