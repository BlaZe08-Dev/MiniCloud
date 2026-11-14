package integration_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStartup_BadEnvStopsProcess proves SEC-062 end to end: a set but invalid value for an older
// variable stops the coordinator and the worker at startup with a non-zero exit and a message
// naming the variable and the bad value, instead of running on the default. No database is used.
func TestStartup_BadEnvStopsProcess(t *testing.T) {
	dir := t.TempDir()
	bins := map[string]string{}
	for _, name := range []string{"coordinator", "worker"} {
		bin := filepath.Join(dir, name)
		out, err := exec.Command("go", "build", "-o", bin, "../../cmd/"+name).CombinedOutput()
		if err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, out)
		}
		bins[name] = bin
	}

	cases := []struct{ proc, key, val string }{
		{"coordinator", "MINICLOUD_HEARTBEAT_INTERVAL", "5sec"},
		{"coordinator", "MINICLOUD_DB_MAX_CONNS", "20a"},
		{"coordinator", "MINICLOUD_LEASE_DURATION", "0s"},
		{"worker", "MINICLOUD_WORKER_SLOTS", "four"},
		{"worker", "MINICLOUD_HEARTBEAT_INTERVAL", "-2s"},
	}
	for _, tc := range cases {
		t.Run(tc.proc+"/"+tc.key+"="+tc.val, func(t *testing.T) {
			cmd := exec.Command(bins[tc.proc])
			cmd.Env = append(os.Environ(),
				"MINICLOUD_STORAGE_DRIVER=memory",
				"MINICLOUD_HTTP_ADDR=127.0.0.1:0",
				tc.key+"="+tc.val,
			)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				var ee *exec.ExitError
				if !errors.As(err, &ee) || ee.ExitCode() == 0 {
					t.Fatalf("want a non-zero exit, got %v\n%s", err, out.String())
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("process kept running with %s=%s\n%s", tc.key, tc.val, out.String())
			}
			if !strings.Contains(out.String(), tc.key) || !strings.Contains(out.String(), tc.val) {
				t.Fatalf("output must name %s and %q:\n%s", tc.key, tc.val, out.String())
			}
		})
	}
}
