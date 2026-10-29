package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// setupPostgresTestSchema creates an isolated schema in minicloud_test, runs all migrations,
// and returns the configured schema URL and pool. Hard safety guards enforce that the target
// database name must end in '_test' and credentials are never printed.
func setupPostgresTestSchema(t *testing.T) (string, *pgxpool.Pool, string) {
	t.Helper()

	testURL := os.Getenv("MINICLOUD_TEST_DATABASE_URL")
	if testURL == "" {
		t.Skip("skipping postgres integration test: MINICLOUD_TEST_DATABASE_URL is unset")
	}

	cfg, err := pgxpool.ParseConfig(testURL)
	if err != nil {
		t.Fatalf("failed to parse test database url: %v", err)
	}

	// Hard safety guard: refuse execution unless database name ends in "_test"
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatalf("SAFETY VIOLATION: refusing to run test against database %q; name must end in '_test'", cfg.ConnConfig.Database)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	adminConn, err := pgx.Connect(ctx, testURL)
	if err != nil {
		t.Fatalf("failed to connect admin conn: %v", err)
	}
	defer adminConn.Close(ctx)

	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("failed to generate random schema name: %v", err)
	}
	schemaName := fmt.Sprintf("test_wire_%s", hex.EncodeToString(b))

	if _, err := adminConn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s;", schemaName)); err != nil {
		t.Fatalf("failed to create schema %s: %v", schemaName, err)
	}

	if _, err := adminConn.Exec(ctx, fmt.Sprintf("SET search_path TO %s;", schemaName)); err != nil {
		_, _ = adminConn.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE;", schemaName))
		t.Fatalf("failed to set search_path: %v", err)
	}

	m := postgres.NewMigrator()
	if _, err := m.Migrate(ctx, adminConn); err != nil {
		_, _ = adminConn.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE;", schemaName))
		t.Fatalf("failed to run migrations in test schema: %v", err)
	}

	// Configure search_path in the connection URL for the test coordinator
	separator := "?"
	if strings.Contains(testURL, "?") {
		separator = "&"
	}
	schemaURL := fmt.Sprintf("%s%ssearch_path=%s", testURL, separator, schemaName)

	poolCfg, err := pgxpool.ParseConfig(schemaURL)
	if err != nil {
		t.Fatalf("failed to parse schema pool config: %v", err)
	}
	poolCfg.MaxConns = 25
	poolCfg.MinConns = 2

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("failed to create pool for schema %s: %v", schemaName, err)
	}

	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanConn, err := pgx.Connect(cleanupCtx, testURL)
		if err == nil {
			_, _ = cleanConn.Exec(cleanupCtx, fmt.Sprintf("DROP SCHEMA %s CASCADE;", schemaName))
			_ = cleanConn.Close(cleanupCtx)
		}
	})

	return schemaURL, pool, schemaName
}

func stopPostgresContainer(t *testing.T) {
	t.Helper()
	refuseWithoutContainerControl(t)
	cmd := exec.Command("docker", "compose", "--env-file", "../../.env", "-f", "../../deployments/docker-compose.yml", "stop", "postgres")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to stop postgres container: %v, output: %s", err, string(out))
	}
}

func startPostgresContainer(t *testing.T) {
	t.Helper()
	refuseWithoutContainerControl(t)
	cmd := exec.Command("docker", "compose", "--env-file", "../../.env", "-f", "../../deployments/docker-compose.yml", "start", "postgres")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to start postgres container: %v, output: %s", err, string(out))
	}

	testURL := os.Getenv("MINICLOUD_TEST_DATABASE_URL")
	if testURL == "" {
		return
	}

	// Poll until database accepts connections
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := pgx.Connect(ctx, testURL)
		if err == nil {
			_ = conn.Close(ctx)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for postgres container to become healthy after start")
}

func getFreePort(t *testing.T) (string, net.Listener) {
	t.Helper()
	l, err := listenWithReuse("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to obtain ephemeral port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close() // Release so coordinator process can bind to it
	return addr, l
}

// TestPostgresWiring_CoordinatorRestartRecovery verifies real-process durability across coordinator kill:
//  1. Coordinator 1 starts against PostgreSQL storage.
//  2. Real worker process registers and claims an in-flight job (sleep task). A second job remains QUEUED.
//  3. Coordinator 1 is killed with SIGKILL mid-execution.
//  4. Coordinator 2 starts on the exact same port and connects to the same PostgreSQL schema.
//  5. The surviving worker completes its attempt and reports the outcome to Coordinator 2:
//     Coordinator 2 accepts the result with HTTP 200 COMMITTED and marks the job SUCCEEDED.
//  6. The surviving worker claims the remaining QUEUED job from Coordinator 2 and completes it.
//
// Documented Limitation (docs/failure-model.md):
// If a worker process dies mid-execution, its RUNNING attempt stays RUNNING in Phase 3 until Phase 4 leases.
func TestPostgresWiring_CoordinatorRestartRecovery(t *testing.T) {
	schemaURL, pool, schemaName := setupPostgresTestSchema(t)

	tmpDir := t.TempDir()
	coordBin := filepath.Join(tmpDir, "coordinator")
	workerBin := filepath.Join(tmpDir, "worker")

	// Build binaries
	cmdBuildCoord := exec.Command("go", "build", "-o", coordBin, "../../cmd/coordinator")
	if out, err := cmdBuildCoord.CombinedOutput(); err != nil {
		t.Fatalf("failed to build coordinator: %v, output: %s", err, string(out))
	}
	cmdBuildWorker := exec.Command("go", "build", "-o", workerBin, "../../cmd/worker")
	if out, err := cmdBuildWorker.CombinedOutput(); err != nil {
		t.Fatalf("failed to build worker: %v, output: %s", err, string(out))
	}

	coordAddr, _ := getFreePort(t)
	coordURL := "http://" + coordAddr

	// Start Coordinator 1
	startCoordCmd := func() *exec.Cmd {
		cmd := exec.Command(coordBin)
		cmd.Env = append(os.Environ(),
			"MINICLOUD_HTTP_ADDR="+coordAddr,
			"MINICLOUD_STORAGE_DRIVER=postgres",
			"MINICLOUD_DATABASE_URL="+schemaURL,
			"MINICLOUD_HEARTBEAT_INTERVAL=1s",
			"MINICLOUD_WORKER_TIMEOUT=5s",
			"MINICLOUD_REAPER_INTERVAL=1s",
		)
		cmd.Stdout = io.Discard
		cmd.Stderr = os.Stderr
		return cmd
	}

	coordCmd1 := startCoordCmd()
	if err := coordCmd1.Start(); err != nil {
		t.Fatalf("failed to start coordinator 1: %v", err)
	}

	waitForServerReady := func(url string) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := http.Get(url + "/readyz")
			if err == nil && resp.StatusCode == http.StatusOK {
				_ = resp.Body.Close()
				return
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for coordinator at %s to be ready", url)
	}

	waitForServerReady(coordURL)

	// Start Worker Process
	workerID := "real-worker-restart-1"
	workerCmd := exec.Command(workerBin)
	workerCmd.Env = append(os.Environ(),
		"MINICLOUD_COORDINATOR_URL="+coordURL,
		"MINICLOUD_WORKER_ID="+workerID,
		"MINICLOUD_WORKER_SLOTS=2",
		"MINICLOUD_HEARTBEAT_INTERVAL=100ms",
	)
	workerCmd.Stdout = io.Discard
	workerCmd.Stderr = os.Stderr
	if err := workerCmd.Start(); err != nil {
		_ = coordCmd1.Process.Kill()
		t.Fatalf("failed to start worker process: %v", err)
	}
	defer func() {
		if workerCmd.Process != nil {
			_ = workerCmd.Process.Kill()
		}
	}()

	// Wait for worker registration
	time.Sleep(200 * time.Millisecond)

	// Submit Job 1: sleep 800ms (will be in-flight when coordinator 1 dies)
	job1Payload := `{"task_type":"sleep","payload":{"duration_ms":800}}`
	resp1, err := http.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(job1Payload))
	if err != nil {
		t.Fatalf("failed to submit job 1: %v", err)
	}
	var job1 struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp1.Body).Decode(&job1)
	resp1.Body.Close()

	// Submit Job 2: echo (will remain QUEUED while worker executes job 1)
	job2Payload := `{"task_type":"echo","payload":{"message":"hello durable"}}`
	resp2, err := http.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(job2Payload))
	if err != nil {
		t.Fatalf("failed to submit job 2: %v", err)
	}
	var job2 struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&job2)
	resp2.Body.Close()

	// Wait 200ms to ensure Worker has claimed Job 1 and it is RUNNING
	time.Sleep(200 * time.Millisecond)

	// KILL Coordinator 1 mid-execution
	if err := coordCmd1.Process.Kill(); err != nil {
		t.Fatalf("failed to kill coordinator 1: %v", err)
	}
	_ = coordCmd1.Wait()

	// Start Coordinator 2 on the same address with the same durable schema
	coordCmd2 := startCoordCmd()
	if err := coordCmd2.Start(); err != nil {
		t.Fatalf("failed to start coordinator 2: %v", err)
	}
	defer func() {
		if coordCmd2.Process != nil {
			_ = coordCmd2.Process.Kill()
		}
	}()

	waitForServerReady(coordURL)

	// Poll until both jobs are completed
	pollJobState := func(jobID string) string {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := http.Get(fmt.Sprintf("%s/v1/jobs/%s", coordURL, jobID))
			if err == nil && resp.StatusCode == http.StatusOK {
				var res struct {
					State string `json:"state"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&res)
				resp.Body.Close()
				if res.State == "SUCCEEDED" || res.State == "FAILED" {
					return res.State
				}
			}
			if resp != nil {
				resp.Body.Close()
			}
			time.Sleep(100 * time.Millisecond)
		}
		return "TIMEOUT"
	}

	state1 := pollJobState(job1.ID)
	if state1 != "SUCCEEDED" {
		t.Fatalf("expected in-flight job 1 to be accepted by restarted coordinator and SUCCEED, got %s", state1)
	}

	state2 := pollJobState(job2.ID)
	if state2 != "SUCCEEDED" {
		t.Fatalf("expected surviving queued job 2 to be claimed and SUCCEED, got %s", state2)
	}

	// Verify database authoritative state: exactly 1 result row per job
	var resCount1, resCount2 int
	ctx := context.Background()
	queryRes1 := fmt.Sprintf("SELECT COUNT(*) FROM %s.results WHERE job_id = $1;", schemaName)
	if err := pool.QueryRow(ctx, queryRes1, job1.ID).Scan(&resCount1); err != nil || resCount1 != 1 {
		t.Fatalf("expected 1 result row for job 1, got %d, err=%v", resCount1, err)
	}
	if err := pool.QueryRow(ctx, queryRes1, job2.ID).Scan(&resCount2); err != nil || resCount2 != 1 {
		t.Fatalf("expected 1 result row for job 2, got %d, err=%v", resCount2, err)
	}
}

// TestPostgresWiring_DatabaseOutageAndRecovery verifies coordinator behavior during a real database outage:
// 1. Coordinator starts against healthy PostgreSQL.
// 2. /readyz returns 200 OK {"status":"ready"}.
// 3. PostgreSQL container is stopped via docker compose stop postgres.
// 4. /readyz immediately returns 503 SERVICE_UNAVAILABLE with Retry-After: 1.
// 5. POST /v1/jobs returns 503 SERVICE_UNAVAILABLE with Retry-After: 1 and an opaque body.
// 6. PostgreSQL container is restored via docker compose start postgres.
// 7. Coordinator automatically recovers without restart; /readyz returns 200 OK.
// 8. New job submissions succeed immediately.
func TestPostgresWiring_DatabaseOutageAndRecovery(t *testing.T) {
	requireContainerControl(t)
	schemaURL, pool, _ := setupPostgresTestSchema(t)

	// Absolute safety guard: always restart container on test cleanup
	t.Cleanup(func() {
		startPostgresContainer(t)
	})

	tmpDir := t.TempDir()
	coordBin := filepath.Join(tmpDir, "coordinator")
	cmdBuildCoord := exec.Command("go", "build", "-o", coordBin, "../../cmd/coordinator")
	if out, err := cmdBuildCoord.CombinedOutput(); err != nil {
		t.Fatalf("failed to build coordinator: %v, output: %s", err, string(out))
	}

	coordAddr, _ := getFreePort(t)
	coordURL := "http://" + coordAddr

	coordCmd := exec.Command(coordBin)
	coordCmd.Env = append(os.Environ(),
		"MINICLOUD_HTTP_ADDR="+coordAddr,
		"MINICLOUD_STORAGE_DRIVER=postgres",
		"MINICLOUD_DATABASE_URL="+schemaURL,
		"MINICLOUD_DB_QUERY_TIMEOUT=2s",
	)
	coordCmd.Stdout = io.Discard
	coordCmd.Stderr = os.Stderr
	if err := coordCmd.Start(); err != nil {
		t.Fatalf("failed to start coordinator: %v", err)
	}
	defer func() {
		if coordCmd.Process != nil {
			_ = coordCmd.Process.Kill()
		}
	}()

	// 1. Verify healthy readiness initially
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(coordURL + "/readyz")
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			ready = true
			break
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("coordinator failed to become ready")
	}

	// 2. STOP PostgreSQL container (simulating outage)
	stopPostgresContainer(t)

	// Verify /readyz returns 503 SERVICE_UNAVAILABLE with Retry-After: 1
	outageDeadline := time.Now().Add(6 * time.Second)
	sawOutage503 := false
	for time.Now().Before(outageDeadline) {
		resp, err := http.Get(coordURL + "/readyz")
		if err == nil && resp.StatusCode == http.StatusServiceUnavailable {
			sawOutage503 = true
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "1" {
				t.Fatalf("expected Retry-After header '1', got %q", retryAfter)
			}
			resp.Body.Close()
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !sawOutage503 {
		t.Fatalf("expected /readyz to return HTTP 503 during database outage")
	}

	// Verify POST /v1/jobs returns 503 with Retry-After: 1 and opaque body
	jobPayload := `{"task_type":"echo","payload":{"message":"fail"}}`
	jobResp, err := http.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(jobPayload))
	if err != nil {
		t.Fatalf("job submission request failed: %v", err)
	}
	defer jobResp.Body.Close()

	if jobResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 for job submission during outage, got %d", jobResp.StatusCode)
	}
	if retryAfter := jobResp.Header.Get("Retry-After"); retryAfter != "1" {
		t.Fatalf("expected Retry-After header '1' on job submission, got %q", retryAfter)
	}

	bodyBytes, _ := io.ReadAll(jobResp.Body)
	bodyStr := string(bodyBytes)
	if strings.Contains(bodyStr, "password") || strings.Contains(bodyStr, "5433") || strings.Contains(bodyStr, "dial") {
		t.Fatalf("SECURITY VIOLATION: sensitive details leaked during outage: %s", bodyStr)
	}

	// 3. RESTORE PostgreSQL container
	startPostgresContainer(t)

	// Verify coordinator automatically recovers: /readyz returns 200 OK
	recoveryDeadline := time.Now().Add(10 * time.Second)
	recovered := false
	for time.Now().Before(recoveryDeadline) {
		resp, err := http.Get(coordURL + "/readyz")
		if err == nil && resp.StatusCode == http.StatusOK {
			recovered = true
			resp.Body.Close()
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !recovered {
		t.Fatalf("coordinator failed to recover after database was restarted")
	}

	// Verify newly submitted job succeeds
	validPayload := `{"task_type":"echo","payload":{"message":"recovered"}}`
	recResp, err := http.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(validPayload))
	if err != nil {
		t.Fatalf("failed to submit job after recovery: %v", err)
	}
	defer recResp.Body.Close()
	if recResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected HTTP 201 Created after recovery, got %d", recResp.StatusCode)
	}

	_ = pool
}

// TestPostgresWiring_BurstConcurrentJobs_WithMidRunCoordinatorRestart executes 50 concurrent jobs
// across 3 worker processes while killing and restarting the coordinator mid-run. It verifies:
// 1. All 50 jobs eventually reach SUCCEEDED.
// 2. Exactly 50 attempt rows exist in the database (zero duplicate attempts).
// 3. Exactly 50 result rows exist in the database.
func TestPostgresWiring_BurstConcurrentJobs_WithMidRunCoordinatorRestart(t *testing.T) {
	schemaURL, pool, schemaName := setupPostgresTestSchema(t)

	tmpDir := t.TempDir()
	coordBin := filepath.Join(tmpDir, "coordinator")
	workerBin := filepath.Join(tmpDir, "worker")

	cmdBuildCoord := exec.Command("go", "build", "-o", coordBin, "../../cmd/coordinator")
	if out, err := cmdBuildCoord.CombinedOutput(); err != nil {
		t.Fatalf("failed to build coordinator: %v, output: %s", err, string(out))
	}
	cmdBuildWorker := exec.Command("go", "build", "-o", workerBin, "../../cmd/worker")
	if out, err := cmdBuildWorker.CombinedOutput(); err != nil {
		t.Fatalf("failed to build worker: %v, output: %s", err, string(out))
	}

	coordAddr, _ := getFreePort(t)
	coordURL := "http://" + coordAddr

	startCoordCmd := func() *exec.Cmd {
		cmd := exec.Command(coordBin)
		cmd.Env = append(os.Environ(),
			"MINICLOUD_HTTP_ADDR="+coordAddr,
			"MINICLOUD_STORAGE_DRIVER=postgres",
			"MINICLOUD_DATABASE_URL="+schemaURL,
			"MINICLOUD_HEARTBEAT_INTERVAL=1s",
			"MINICLOUD_WORKER_TIMEOUT=5s",
			"MINICLOUD_REAPER_INTERVAL=1s",
		)
		cmd.Stdout = io.Discard
		cmd.Stderr = os.Stderr
		return cmd
	}

	coordCmd := startCoordCmd()
	if err := coordCmd.Start(); err != nil {
		t.Fatalf("failed to start coordinator: %v", err)
	}

	waitForServerReady := func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := http.Get(coordURL + "/readyz")
			if err == nil && resp.StatusCode == http.StatusOK {
				resp.Body.Close()
				return
			}
			if resp != nil {
				resp.Body.Close()
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for coordinator ready")
	}
	waitForServerReady()

	// Launch 3 Worker Processes
	const numWorkers = 3
	workerCmds := make([]*exec.Cmd, numWorkers)
	for i := 0; i < numWorkers; i++ {
		wID := fmt.Sprintf("burst-worker-%d", i)
		wCmd := exec.Command(workerBin)
		wCmd.Env = append(os.Environ(),
			"MINICLOUD_COORDINATOR_URL="+coordURL,
			"MINICLOUD_WORKER_ID="+wID,
			"MINICLOUD_WORKER_SLOTS=10",
			"MINICLOUD_HEARTBEAT_INTERVAL=100ms",
		)
		wCmd.Stdout = io.Discard
		wCmd.Stderr = os.Stderr
		if err := wCmd.Start(); err != nil {
			_ = coordCmd.Process.Kill()
			t.Fatalf("failed to start worker %d: %v", i, err)
		}
		workerCmds[i] = wCmd
	}
	defer func() {
		for _, wCmd := range workerCmds {
			if wCmd.Process != nil {
				_ = wCmd.Process.Kill()
			}
		}
		if coordCmd != nil && coordCmd.Process != nil {
			_ = coordCmd.Process.Kill()
		}
	}()

	time.Sleep(200 * time.Millisecond)

	// Submit 50 jobs
	const totalJobs = 50
	jobIDs := make([]string, totalJobs)
	var submitWg sync.WaitGroup
	for i := 0; i < totalJobs; i++ {
		submitWg.Add(1)
		go func(idx int) {
			defer submitWg.Done()
			payload := fmt.Sprintf(`{"task_type":"echo","payload":{"idx":%d}}`, idx)
			resp, err := http.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(payload))
			if err != nil {
				t.Errorf("job %d submit failed: %v", idx, err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("job %d submit returned status %d: %s", idx, resp.StatusCode, string(body))
				return
			}
			var j struct {
				ID string `json:"id"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&j)
			if j.ID == "" {
				t.Errorf("job %d submit returned empty ID", idx)
				return
			}
			jobIDs[idx] = j.ID
		}(i)
	}
	submitWg.Wait()

	// Wait 250ms then kill coordinator mid-burst
	time.Sleep(250 * time.Millisecond)
	_ = coordCmd.Process.Kill()
	_ = coordCmd.Wait()

	// Restart coordinator
	coordCmd = startCoordCmd()
	if err := coordCmd.Start(); err != nil {
		t.Fatalf("failed to restart coordinator: %v", err)
	}
	waitForServerReady()

	// Poll until all 50 jobs reach SUCCEEDED
	deadline := time.Now().Add(45 * time.Second)
	completed := make(map[string]bool)

	for time.Now().Before(deadline) && len(completed) < totalJobs {
		for _, jID := range jobIDs {
			if completed[jID] {
				continue
			}
			resp, err := http.Get(fmt.Sprintf("%s/v1/jobs/%s", coordURL, jID))
			if err == nil && resp.StatusCode == http.StatusOK {
				var res struct {
					State string `json:"state"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&res)
				resp.Body.Close()
				if res.State == "SUCCEEDED" {
					completed[jID] = true
				}
			} else if resp != nil {
				resp.Body.Close()
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if len(completed) != totalJobs {
		for _, jID := range jobIDs {
			if !completed[jID] {
				var dbState, workerID string
				_ = pool.QueryRow(context.Background(), fmt.Sprintf("SELECT state FROM %s.jobs WHERE id = $1;", schemaName), jID).Scan(&dbState)
				_ = pool.QueryRow(context.Background(), fmt.Sprintf("SELECT worker_id FROM %s.attempts WHERE job_id = $1 ORDER BY attempt_no DESC LIMIT 1;", schemaName), jID).Scan(&workerID)
				t.Logf("incomplete job %s state=%s last_worker=%s", jID, dbState, workerID)
			}
		}
		t.Fatalf("expected all %d jobs to succeed, only %d completed within deadline", totalJobs, len(completed))
	}

	// Verify invariants in PostgreSQL database:
	ctx := context.Background()

	// 1. Exactly 50 attempts in attempts table
	var attemptCount int
	queryAttempts := fmt.Sprintf("SELECT COUNT(*) FROM %s.attempts;", schemaName)
	if err := pool.QueryRow(ctx, queryAttempts).Scan(&attemptCount); err != nil {
		t.Fatalf("failed to query attempts: %v", err)
	}
	if attemptCount != totalJobs {
		t.Fatalf("expected exactly %d attempts, got %d", totalJobs, attemptCount)
	}

	// 2. Zero duplicate attempts per job
	queryDupAttempts := fmt.Sprintf(`
		SELECT job_id, COUNT(*) 
		FROM %s.attempts 
		GROUP BY job_id 
		HAVING COUNT(*) > 1;
	`, schemaName)
	rows, err := pool.Query(ctx, queryDupAttempts)
	if err != nil {
		t.Fatalf("failed to query duplicate attempts: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var jID string
		var count int
		_ = rows.Scan(&jID, &count)
		t.Fatalf("INVARIANT VIOLATION: job %s has %d duplicate attempts (>1)", jID, count)
	}

	// 3. Exactly 50 results in results table
	var resultCount int
	queryResults := fmt.Sprintf("SELECT COUNT(*) FROM %s.results;", schemaName)
	if err := pool.QueryRow(ctx, queryResults).Scan(&resultCount); err != nil {
		t.Fatalf("failed to query results: %v", err)
	}
	if resultCount != totalJobs {
		t.Fatalf("expected exactly %d results in database, got %d", totalJobs, resultCount)
	}
}
