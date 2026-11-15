package integration_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startDefaultPolicyCoordinator builds and starts the real coordinator binary on the schema URL with
// the DEFAULT dispatch policy (MINICLOUD_DISPATCH_POLICY empty) and returns its base URL.
func startDefaultPolicyCoordinator(t *testing.T, schemaURL string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "coordinator")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/coordinator").CombinedOutput(); err != nil {
		t.Fatalf("failed to build coordinator: %v, output: %s", err, out)
	}
	addr, _ := getFreePort(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"MINICLOUD_HTTP_ADDR="+addr,
		"MINICLOUD_STORAGE_DRIVER=postgres",
		"MINICLOUD_DATABASE_URL="+schemaURL,
		"MINICLOUD_DB_QUERY_TIMEOUT=1s",
		"MINICLOUD_DISPATCH_POLICY=",
	)
	var logs safeBuffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start coordinator: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if resp, err := http.Get("http://" + addr + "/readyz"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return "http://" + addr
			}
		}
	}
	t.Fatalf("coordinator not ready; logs: %s", logs.String())
	return ""
}

// TestLeakScanPollHelper_DefaultPolicy runs the leak scan's poll step against the real coordinator
// on the default policy, without container control (the leak scan itself needs it). It guards that
// pollUntilAssignment (a) reaches the assignment under round_robin within its bound and (b) gives up
// with an error naming the bound when no assignment ever arrives.
func TestLeakScanPollHelper_DefaultPolicy(t *testing.T) {
	schemaURL, _, _ := setupPostgresTestSchema(t)
	base := startDefaultPolicyCoordinator(t, schemaURL)
	client := &http.Client{Timeout: 3 * time.Second}

	reg := `{"id":"leak-scan-worker","version":"v0.1.0","total_slots":2,"supported_tasks":["echo"]}`
	resp, err := client.Post(base+"/v1/workers/register", "application/json", strings.NewReader(reg))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	_ = resp.Body.Close()
	pollURL := base + "/v1/workers/leak-scan-worker/poll"

	resp, err = client.Post(base+"/v1/jobs", "application/json", strings.NewReader(`{"task_type":"echo","payload":{"msg":"x"}}`))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	_ = resp.Body.Close()

	got, polls, err := pollUntilAssignment(client, pollURL, leakScanPollMax, leakScanPollEvery)
	if err != nil {
		t.Fatalf("default policy: %v", err)
	}
	_ = got.Body.Close()
	t.Logf("assignment arrived on poll %d of at most %d", polls, leakScanPollMax)

	// The job is claimed and the queue is empty: every further poll is 204, so the helper must stop
	// at the bound and name it.
	_, n, err := pollUntilAssignment(client, pollURL, 3, 10*time.Millisecond)
	if err == nil || n != 3 || !strings.Contains(err.Error(), "bound of 3 polls") {
		t.Fatalf("with nothing to assign: polls=%d err=%v; want 3 polls and an error naming the bound", n, err)
	}
}
