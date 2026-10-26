package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// safeBuffer is a thread-safe bytes buffer for capturing subprocess output.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestSecurity_PartD_PoolExhaustion verifies Part D (a):
// 500 concurrent API requests against a pool capped at 10 connections.
// Asserts the process stays alive, latency is bounded, every response is 2xx/4xx
// or an opaque 503 SERVICE_UNAVAILABLE (no other 5xx, no hang), and the system recovers.
func TestSecurity_PartD_PoolExhaustion(t *testing.T) {
	schemaURL, pool, schemaName := setupPostgresTestSchema(t)

	tmpDir := t.TempDir()
	coordBin := filepath.Join(tmpDir, "coordinator")
	cmdBuildCoord := exec.Command("go", "build", "-o", coordBin, "../../cmd/coordinator")
	if out, err := cmdBuildCoord.CombinedOutput(); err != nil {
		t.Fatalf("failed to build coordinator: %v, output: %s", err, string(out))
	}

	coordAddr, _ := getFreePort(t)
	coordURL := "http://" + coordAddr

	const poolSize = 10
	coordAppName := fmt.Sprintf("coord_pool_%s", schemaName)
	coordSchemaURL := fmt.Sprintf("%s&application_name=%s", schemaURL, coordAppName)

	// Start coordinator with pool capped at 10 connections
	coordCmd := exec.Command(coordBin)
	coordCmd.Env = append(os.Environ(),
		"MINICLOUD_HTTP_ADDR="+coordAddr,
		"MINICLOUD_STORAGE_DRIVER=postgres",
		"MINICLOUD_DATABASE_URL="+coordSchemaURL,
		fmt.Sprintf("MINICLOUD_DB_MAX_CONNS=%d", poolSize),
		"MINICLOUD_DB_MIN_CONNS=2",
		"MINICLOUD_DB_QUERY_TIMEOUT=2s",
	)
	var coordLogs safeBuffer
	coordCmd.Stdout = &coordLogs
	coordCmd.Stderr = &coordLogs
	if err := coordCmd.Start(); err != nil {
		t.Fatalf("failed to start coordinator: %v", err)
	}
	defer func() {
		if coordCmd.Process != nil {
			_ = coordCmd.Process.Kill()
		}
	}()

	// Wait for coordinator readiness
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
		t.Fatalf("coordinator failed to become ready within deadline")
	}

	const totalRequests = 500
	type reqResult struct {
		statusCode int
		retryAfter string
		latency    time.Duration
		body       string
		err        error
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
		},
	}

	runBatch := func() (time.Duration, time.Duration, []reqResult) {
		results := make([]reqResult, totalRequests)
		var wg sync.WaitGroup
		startBarrier := make(chan struct{})
		for i := 0; i < totalRequests; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-startBarrier
				reqStart := time.Now()

				var resp *http.Response
				var err error
				switch idx % 3 {
				case 0:
					payload := fmt.Sprintf(`{"task_type":"echo","payload":{"idx":%d}}`, idx)
					resp, err = client.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(payload))
				case 1:
					resp, err = client.Get(coordURL + "/readyz")
				case 2:
					resp, err = client.Get(fmt.Sprintf("%s/v1/jobs/job_test_%d", coordURL, idx))
				}

				lat := time.Since(reqStart)
				if err != nil {
					results[idx] = reqResult{err: err, latency: lat}
					return
				}
				defer resp.Body.Close()

				bodyBytes, _ := io.ReadAll(resp.Body)
				results[idx] = reqResult{
					statusCode: resp.StatusCode,
					retryAfter: resp.Header.Get("Retry-After"),
					latency:    lat,
					body:       string(bodyBytes),
				}
			}(i)
		}
		start := time.Now()
		close(startBarrier)
		wg.Wait()
		batchDur := time.Since(start)
		var maxLat time.Duration
		for _, r := range results {
			if r.latency > maxLat {
				maxLat = r.latency
			}
		}
		return batchDur, maxLat, results
	}

	// Secondary baseline: run 3 batches without lock held and calculate median max latency
	baselineMaxLatencies := make([]time.Duration, 3)
	for b := 0; b < 3; b++ {
		_, maxLat, res := runBatch()
		baselineMaxLatencies[b] = maxLat
		for idx, r := range res {
			if r.err != nil {
				t.Fatalf("baseline batch %d request %d transport error: %v", b, idx, r.err)
			}
			if r.statusCode >= 500 {
				t.Fatalf("baseline batch %d request %d returned 5xx: %d (body: %s)", b, idx, r.statusCode, r.body)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	sort.Slice(baselineMaxLatencies, func(i, j int) bool {
		return baselineMaxLatencies[i] < baselineMaxLatencies[j]
	})
	baselineMedian := baselineMaxLatencies[1]
	t.Logf("Baseline unlocked batches max latencies: %v (median: %v)", baselineMaxLatencies, baselineMedian)

	// Primary proof setup: sample pg_stat_activity every ~25ms during hold window
	var maxLockWaiters int
	var lockWaitersMu sync.Mutex
	stopSampling := make(chan struct{})
	samplingDone := make(chan struct{})

	go func() {
		defer close(samplingDone)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()

		sampleCtx := context.Background()
		for {
			select {
			case <-stopSampling:
				return
			case <-ticker.C:
				var count int
				err := pool.QueryRow(sampleCtx,
					"SELECT count(*) FROM pg_stat_activity WHERE application_name = $1 AND wait_event_type = 'Lock'",
					coordAppName,
				).Scan(&count)
				if err == nil {
					lockWaitersMu.Lock()
					if count > maxLockWaiters {
						maxLockWaiters = count
					}
					lockWaitersMu.Unlock()
				}
			}
		}
	}()

	// Genuinely saturate the pool of 10 connections:
	// Acquire an exclusive table lock on jobs in a separate transaction and hold it for holdDuration.
	const holdDuration = 800 * time.Millisecond
	lockAcquired := make(chan struct{})
	go func() {
		lockCtx := context.Background()
		lockConn, err := pool.Acquire(lockCtx)
		if err != nil {
			t.Errorf("failed to acquire connection for pool saturation hold: %v", err)
			close(lockAcquired)
			return
		}
		defer lockConn.Release()

		tx, err := lockConn.Begin(lockCtx)
		if err != nil {
			t.Errorf("failed to begin saturation hold transaction: %v", err)
			close(lockAcquired)
			return
		}
		defer tx.Rollback(lockCtx)

		if _, err := tx.Exec(lockCtx, fmt.Sprintf("LOCK TABLE %s.jobs IN ACCESS EXCLUSIVE MODE;", schemaName)); err != nil {
			t.Errorf("failed to lock jobs table: %v", err)
			close(lockAcquired)
			return
		}

		close(lockAcquired)
		// Hold connections in coordinator pool by sleeping inside the transaction
		time.Sleep(holdDuration)
		_ = tx.Commit(lockCtx)
	}()

	<-lockAcquired

	// Run locked batch
	batchDuration, maxLatency, results := runBatch()

	close(stopSampling)
	<-samplingDone

	t.Logf("Pool exhaustion batch completed: 500 requests across 10 conns in %v", batchDuration)

	// Invariant 1: Coordinator process stayed alive throughout the burst
	if coordCmd.ProcessState != nil && coordCmd.ProcessState.Exited() {
		t.Fatalf("PROCESS CRASH: coordinator exited during 500-request pool exhaustion: %s", coordLogs.String())
	}

	// Invariant 2: Latency is bounded (entire batch completed within reasonable window)
	if batchDuration > 20*time.Second {
		t.Fatalf("latency unbounded: batch took %v (>20s)", batchDuration)
	}

	// Invariant 3: Every response is 2xx, 4xx, or opaque 503 SERVICE_UNAVAILABLE; NO 500s or hangs
	statusCounts := make(map[int]int)
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("request %d encountered transport error: %v", i, r.err)
		}
		statusCounts[r.statusCode]++

		switch {
		case r.statusCode >= 200 && r.statusCode < 300:
			// OK
		case r.statusCode >= 400 && r.statusCode < 500:
			// OK (client errors like 404 for nonexistent job)
		case r.statusCode == http.StatusServiceUnavailable:
			// 503 SERVICE_UNAVAILABLE must include Retry-After and opaque envelope
			if r.retryAfter != "1" {
				t.Fatalf("request %d: expected Retry-After: 1 on 503, got %q", i, r.retryAfter)
			}
			var errResp api.ErrorResponse
			if err := json.Unmarshal([]byte(r.body), &errResp); err != nil {
				t.Fatalf("request %d: 503 response not valid JSON error envelope: %v", i, err)
			}
			if errResp.Error.Code != "SERVICE_UNAVAILABLE" {
				t.Fatalf("request %d: expected code SERVICE_UNAVAILABLE on 503, got %q", i, errResp.Error.Code)
			}
			if errResp.Error.Message != "service temporarily unavailable" {
				t.Fatalf("request %d: expected opaque message 'service temporarily unavailable', got %q", i, errResp.Error.Message)
			}
		default:
			t.Fatalf("UNACCEPTABLE STATUS: request %d returned status %d (body: %s)", i, r.statusCode, r.body)
		}
	}

	t.Logf("Observed status distribution: %v, max latency: %v", statusCounts, maxLatency)

	// Primary proof: assert the maximum number of connections with wait_event_type = 'Lock' observed is at least pool_size - 2
	lockWaitersMu.Lock()
	observedMaxLockWaiters := maxLockWaiters
	lockWaitersMu.Unlock()
	t.Logf("Primary proof: maximum lock-waiting connections observed: %d (threshold >= %d)", observedMaxLockWaiters, poolSize-2)
	if observedMaxLockWaiters < poolSize-2 {
		t.Fatalf("PRIMARY CONTROL FAILED: pool was not genuinely saturated by lock-waiters; expected at least %d connections with wait_event_type = 'Lock', observed maximum was %d", poolSize-2, observedMaxLockWaiters)
	}

	// Secondary proof: locked run's max latency must exceed baseline median by at least 0.25 x holdDuration
	margin := maxLatency - baselineMedian
	minRequiredMargin := time.Duration(float64(holdDuration) * 0.25)
	t.Logf("Secondary proof: baseline median=%v, locked max=%v, margin=%v (required >= %v)", baselineMedian, maxLatency, margin, minRequiredMargin)
	if margin < minRequiredMargin {
		t.Fatalf("SECONDARY CONTROL FAILED: locked max latency (%v) did not exceed baseline median (%v) by at least 0.25 x holdDuration (%v); margin was %v",
			maxLatency, baselineMedian, minRequiredMargin, margin)
	}

	// Invariant 4: System recovers immediately afterwards
	recStart := time.Now()
	recResp, err := client.Get(coordURL + "/readyz")
	if err != nil {
		t.Fatalf("recovery request failed: %v", err)
	}
	defer recResp.Body.Close()
	recLatency := time.Since(recStart)

	if recResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(recResp.Body)
		t.Fatalf("expected 200 OK on recovery, got %d (body: %s)", recResp.StatusCode, string(body))
	}
	if recLatency > 500*time.Millisecond {
		t.Fatalf("system did not recover quickly: recovery request took %v (>500ms)", recLatency)
	}
	t.Logf("System recovered cleanly: /readyz returned 200 OK in %v", recLatency)
}

// TestSecurity_PartD_SlowQuery_ContextDeadline verifies Part D (b):
// Forces a slow statement under a short context deadline and asserts HTTP 503
// SERVICE_UNAVAILABLE with an opaque body and Retry-After: 1, not a hang and not a 500.
func TestSecurity_PartD_SlowQuery_ContextDeadline(t *testing.T) {
	schemaURL, pool, schemaName := setupPostgresTestSchema(t)

	tmpDir := t.TempDir()
	coordBin := filepath.Join(tmpDir, "coordinator")
	cmdBuildCoord := exec.Command("go", "build", "-o", coordBin, "../../cmd/coordinator")
	if out, err := cmdBuildCoord.CombinedOutput(); err != nil {
		t.Fatalf("failed to build coordinator: %v, output: %s", err, string(out))
	}

	coordAddr, _ := getFreePort(t)
	coordURL := "http://" + coordAddr

	// Start coordinator with short DB query timeout of 200ms
	coordCmd := exec.Command(coordBin)
	coordCmd.Env = append(os.Environ(),
		"MINICLOUD_HTTP_ADDR="+coordAddr,
		"MINICLOUD_STORAGE_DRIVER=postgres",
		"MINICLOUD_DATABASE_URL="+schemaURL,
		"MINICLOUD_DB_QUERY_TIMEOUT=200ms",
	)
	var coordLogs safeBuffer
	coordCmd.Stdout = &coordLogs
	coordCmd.Stderr = &coordLogs
	if err := coordCmd.Start(); err != nil {
		t.Fatalf("failed to start coordinator: %v", err)
	}
	defer func() {
		if coordCmd.Process != nil {
			_ = coordCmd.Process.Kill()
		}
	}()

	// Wait for coordinator ready
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
		t.Fatalf("coordinator failed to become ready within deadline")
	}

	// Force lock contention in PostgreSQL: acquire an exclusive table lock on jobs in a separate transaction
	// and hold it for 1 second. Any coordinator query against jobs will block and exceed the 200ms timeout.
	txCtx, txCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer txCancel()

	tx, err := pool.Begin(txCtx)
	if err != nil {
		t.Fatalf("failed to begin lock transaction: %v", err)
	}

	lockStmt := fmt.Sprintf("LOCK TABLE %s.jobs IN ACCESS EXCLUSIVE MODE;", schemaName)
	if _, err := tx.Exec(txCtx, lockStmt); err != nil {
		_ = tx.Rollback(txCtx)
		t.Fatalf("failed to acquire exclusive lock on jobs: %v", err)
	}

	// While the exclusive lock is held, submit a job through the coordinator HTTP API.
	// The query will block on the lock and exceed the 200ms MINICLOUD_DB_QUERY_TIMEOUT.
	client := &http.Client{Timeout: 5 * time.Second}
	reqPayload := `{"task_type":"echo","payload":{"message":"timeout_test"}}`

	startReq := time.Now()
	resp, err := client.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(reqPayload))
	reqDuration := time.Since(startReq)

	// Release the exclusive lock so test cleanup can proceed
	_ = tx.Rollback(txCtx)

	if err != nil {
		t.Fatalf("request failed with transport error: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	// Assertions:
	// 1. Not a hang: completed near the 200ms timeout
	if reqDuration > 3*time.Second {
		t.Fatalf("query hung: took %v", reqDuration)
	}

	// 2. Status must be 503 SERVICE_UNAVAILABLE (NOT 500)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 SERVICE_UNAVAILABLE under query deadline exceeded, got %d (body: %s)", resp.StatusCode, bodyStr)
	}

	// 3. Retry-After header must be present
	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter != "1" {
		t.Fatalf("expected Retry-After: 1 header on 503 response, got %q", retryAfter)
	}

	// 4. Body must be standard opaque envelope
	var errResp api.ErrorResponse
	if err := json.Unmarshal(bodyBytes, &errResp); err != nil {
		t.Fatalf("failed to decode JSON error envelope: %v (raw: %s)", err, bodyStr)
	}
	if errResp.Error.Code != "SERVICE_UNAVAILABLE" {
		t.Fatalf("expected code SERVICE_UNAVAILABLE, got %q", errResp.Error.Code)
	}
	if errResp.Error.Message != "service temporarily unavailable" {
		t.Fatalf("expected opaque message 'service temporarily unavailable', got %q", errResp.Error.Message)
	}

	// 5. System recovers immediately after lock release
	recResp, err := client.Get(coordURL + "/readyz")
	if err != nil {
		t.Fatalf("recovery check failed: %v", err)
	}
	defer recResp.Body.Close()
	if recResp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK after lock release, got %d", recResp.StatusCode)
	}
}

// TestSecurity_PartD_SQLInjection_EndToEnd verifies Part D (c):
// SQL special characters and injection strings through the HTTP API end to end
// against the real Postgres repository across: job payload, task_type, worker id,
// version, and supported_tasks. Asserts 4xx or safely stored-as-data, never 5xx,
// and that no schema object changed (table list and column count identical before and after).
func TestSecurity_PartD_SQLInjection_EndToEnd(t *testing.T) {
	schemaURL, pool, schemaName := setupPostgresTestSchema(t)

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
	var coordLogs safeBuffer
	coordCmd.Stdout = &coordLogs
	coordCmd.Stderr = &coordLogs
	if err := coordCmd.Start(); err != nil {
		t.Fatalf("failed to start coordinator: %v", err)
	}
	defer func() {
		if coordCmd.Process != nil {
			_ = coordCmd.Process.Kill()
		}
	}()

	// Wait for coordinator ready
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
		t.Fatalf("coordinator failed to become ready within deadline")
	}

	ctx := context.Background()

	// Snapshot schema catalog before attacks
	getTableList := func() []string {
		rows, err := pool.Query(ctx, `
			SELECT table_name 
			FROM information_schema.tables 
			WHERE table_schema = $1 
			ORDER BY table_name;`, schemaName)
		if err != nil {
			t.Fatalf("failed to query tables: %v", err)
		}
		defer rows.Close()
		var tables []string
		for rows.Next() {
			var tbl string
			if err := rows.Scan(&tbl); err != nil {
				t.Fatalf("failed to scan table: %v", err)
			}
			tables = append(tables, tbl)
		}
		return tables
	}

	getColumnCount := func() int {
		var count int
		err := pool.QueryRow(ctx, `
			SELECT COUNT(*) 
			FROM information_schema.columns 
			WHERE table_schema = $1;`, schemaName).Scan(&count)
		if err != nil {
			t.Fatalf("failed to query column count: %v", err)
		}
		return count
	}

	tablesBefore := getTableList()
	columnsBefore := getColumnCount()
	t.Logf("Catalog baseline: tables=%v, columns=%d", tablesBefore, columnsBefore)

	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Attack Vector: Job Payload Injections (POST /v1/jobs)
	// Must be safely stored-as-data (201 Created) without executing SQL.
	payloadInjections := []struct {
		name    string
		payload string
	}{
		{"boolean_tautology", `{"query":"' OR '1'='1"}`},
		{"drop_table_attempt", `{"sql":"'; DROP TABLE jobs CASCADE; --"}`},
		{"delete_workers_attempt", `{"attack":"'; DELETE FROM workers WHERE '1'='1'; --"}`},
		{"update_attempt_state", `{"nested":{"cmd":"'; UPDATE attempts SET state='SUCCEEDED'; --"}}`},
		{"pg_sleep_injection", `{"payload":"'; SELECT pg_sleep(5); --"}`},
	}

	for _, tc := range payloadInjections {
		t.Run("Payload_"+tc.name, func(t *testing.T) {
			reqBody := fmt.Sprintf(`{"task_type":"echo","payload":%s}`, tc.payload)
			resp, err := client.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(reqBody))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 500 {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("SERVER ERROR (5xx) on payload injection: %d, body: %s", resp.StatusCode, string(body))
			}
			if resp.StatusCode != http.StatusCreated {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("expected 201 Created for valid JSON payload stored-as-data, got %d (body: %s)", resp.StatusCode, string(body))
			}

			var jobResp struct {
				ID string `json:"id"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&jobResp)

			// Verify in PostgreSQL that the payload was stored verbatim as literal JSON data
			var storedPayload []byte
			query := fmt.Sprintf("SELECT payload FROM %s.jobs WHERE id = $1;", schemaName)
			if err := pool.QueryRow(ctx, query, jobResp.ID).Scan(&storedPayload); err != nil {
				t.Fatalf("failed to query stored payload: %v", err)
			}
			// Verify it parses back to the exact same JSON structure
			var expectedObj, actualObj any
			_ = json.Unmarshal([]byte(tc.payload), &expectedObj)
			_ = json.Unmarshal(storedPayload, &actualObj)
			expBytes, _ := json.Marshal(expectedObj)
			actBytes, _ := json.Marshal(actualObj)
			if string(expBytes) != string(actBytes) {
				t.Fatalf("stored payload mismatch: expected %s, got %s", string(expBytes), string(actBytes))
			}
		})
	}

	// 2. Attack Vector: task_type Injections (POST /v1/jobs)
	// Must be rejected with 4xx client error, never 5xx.
	taskTypeInjections := []string{
		`echo'; DROP TABLE jobs CASCADE; --`,
		`' OR '1'='1`,
		`sleep; DELETE FROM attempts; --`,
		`" UNION SELECT * FROM jobs --`,
		`task\x00injection`,
	}
	for i, attack := range taskTypeInjections {
		t.Run(fmt.Sprintf("TaskType_%d", i), func(t *testing.T) {
			reqBody, _ := json.Marshal(map[string]any{
				"task_type": attack,
				"payload":   map[string]string{"msg": "test"},
			})
			resp, err := client.Post(coordURL+"/v1/jobs", "application/json", bytes.NewReader(reqBody))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 500 {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("SERVER ERROR (5xx) on task_type injection: %d, body: %s", resp.StatusCode, string(body))
			}
			if resp.StatusCode < 400 || resp.StatusCode >= 500 {
				t.Fatalf("expected 4xx client error on invalid task_type, got %d", resp.StatusCode)
			}
		})
	}

	// 3. Attack Vector: worker ID Injections (POST /v1/workers/register and routes)
	// Must be rejected with 400 INVALID_REQUEST at API boundary, never 5xx.
	workerIDInjections := []string{
		`worker'; DROP TABLE workers CASCADE; --`,
		`' OR '1'='1`,
		`admin'--`,
		`worker-1; DELETE FROM results; --`,
		"worker\nDROP TABLE jobs",
		"../../etc/passwd",
	}
	for i, attack := range workerIDInjections {
		t.Run(fmt.Sprintf("WorkerID_%d", i), func(t *testing.T) {
			reqBody, _ := json.Marshal(map[string]any{
				"id":              attack,
				"total_slots":     2,
				"supported_tasks": []string{"echo"},
			})
			resp, err := client.Post(coordURL+"/v1/workers/register", "application/json", bytes.NewReader(reqBody))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 500 {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("SERVER ERROR (5xx) on worker_id injection: %d, body: %s", resp.StatusCode, string(body))
			}
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request on invalid worker ID, got %d", resp.StatusCode)
			}
		})
	}

	// 4. Attack Vector: version Injections (POST /v1/workers/register)
	// Valid charset versions are safely stored; SQL injections with special chars rejected with 4xx or stored-as-data.
	versionInjections := []string{
		`v1'; DROP TABLE attempts; --`,
		`'; SELECT pg_sleep(5); --`,
		`' OR '1'='1`,
	}
	for i, attack := range versionInjections {
		t.Run(fmt.Sprintf("Version_%d", i), func(t *testing.T) {
			wID := fmt.Sprintf("ver-worker-%d", i)
			reqBody, _ := json.Marshal(map[string]any{
				"id":              wID,
				"version":         attack,
				"total_slots":     2,
				"supported_tasks": []string{"echo"},
			})
			resp, err := client.Post(coordURL+"/v1/workers/register", "application/json", bytes.NewReader(reqBody))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 500 {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("SERVER ERROR (5xx) on version injection: %d, body: %s", resp.StatusCode, string(body))
			}
			// If stored, verify stored verbatim as literal text
			if resp.StatusCode == http.StatusOK {
				var storedVer string
				query := fmt.Sprintf("SELECT version FROM %s.workers WHERE id = $1;", schemaName)
				if err := pool.QueryRow(ctx, query, wID).Scan(&storedVer); err != nil {
					t.Fatalf("failed to query stored version: %v", err)
				}
				if storedVer != attack {
					t.Fatalf("stored version mismatch: expected %q, got %q", attack, storedVer)
				}
			}
		})
	}

	// 5. Attack Vector: supported_tasks Injections (POST /v1/workers/register)
	// Must be rejected by task name charset validation ^[a-z0-9_]{1,64}$, never 5xx.
	tasksInjections := [][]string{
		{`echo'; DROP TABLE jobs; --`},
		{`'; DELETE FROM workers; --`},
		{`echo`, `' OR '1'='1`},
	}
	for i, attackTasks := range tasksInjections {
		t.Run(fmt.Sprintf("SupportedTasks_%d", i), func(t *testing.T) {
			wID := fmt.Sprintf("tasks-worker-%d", i)
			reqBody, _ := json.Marshal(map[string]any{
				"id":              wID,
				"total_slots":     2,
				"supported_tasks": attackTasks,
			})
			resp, err := client.Post(coordURL+"/v1/workers/register", "application/json", bytes.NewReader(reqBody))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 500 {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("SERVER ERROR (5xx) on supported_tasks injection: %d, body: %s", resp.StatusCode, string(body))
			}
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request on SQL injection in supported_tasks, got %d", resp.StatusCode)
			}
		})
	}

	// Verify schema objects after all attacks
	tablesAfter := getTableList()
	columnsAfter := getColumnCount()

	if len(tablesBefore) != len(tablesAfter) {
		t.Fatalf("SCHEMA CORRUPTION: table count changed from %d to %d (before: %v, after: %v)",
			len(tablesBefore), len(tablesAfter), tablesBefore, tablesAfter)
	}
	for i := range tablesBefore {
		if tablesBefore[i] != tablesAfter[i] {
			t.Fatalf("SCHEMA CORRUPTION: table %q replaced by %q", tablesBefore[i], tablesAfter[i])
		}
	}
	if columnsBefore != columnsAfter {
		t.Fatalf("SCHEMA CORRUPTION: column count changed from %d to %d", columnsBefore, columnsAfter)
	}
	t.Logf("PASS: Schema completely unmodified after all SQL injection attacks")
}

// TestSecurity_PartD_Outage_MidDispatchAndMidCommit verifies Part D (d):
// Stops the database between a worker's dispatch and its commit, restarts it,
// and asserts the surviving worker's commit is accepted or cleanly rejected,
// there is no duplicate commit, no lost job, and no partial rows.
func TestSecurity_PartD_Outage_MidDispatchAndMidCommit(t *testing.T) {
	schemaURL, pool, schemaName := setupPostgresTestSchema(t)

	// Absolute safety guard: always restore database container on cleanup
	t.Cleanup(func() {
		startPostgresContainer(t)
	})

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

	coordCmd := exec.Command(coordBin)
	coordCmd.Env = append(os.Environ(),
		"MINICLOUD_HTTP_ADDR="+coordAddr,
		"MINICLOUD_STORAGE_DRIVER=postgres",
		"MINICLOUD_DATABASE_URL="+schemaURL,
		"MINICLOUD_HEARTBEAT_INTERVAL=1s",
		"MINICLOUD_WORKER_TIMEOUT=15s",
		"MINICLOUD_DB_QUERY_TIMEOUT=2s",
	)
	var coordLogs safeBuffer
	coordCmd.Stdout = &coordLogs
	coordCmd.Stderr = &coordLogs
	if err := coordCmd.Start(); err != nil {
		t.Fatalf("failed to start coordinator: %v", err)
	}
	defer func() {
		if coordCmd.Process != nil {
			_ = coordCmd.Process.Kill()
		}
	}()

	// Wait for coordinator ready
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
		t.Fatalf("coordinator failed to become ready within deadline")
	}

	// Start worker process with sleep task supported
	workerCmd := exec.Command(workerBin)
	workerCmd.Env = append(os.Environ(),
		"MINICLOUD_COORDINATOR_URL="+coordURL,
		"MINICLOUD_WORKER_ID=outage-worker-1",
		"MINICLOUD_WORKER_SLOTS=2",
		"MINICLOUD_HEARTBEAT_INTERVAL=200ms",
	)
	var workerLogs safeBuffer
	workerCmd.Stdout = &workerLogs
	workerCmd.Stderr = &workerLogs
	if err := workerCmd.Start(); err != nil {
		t.Fatalf("failed to start worker: %v", err)
	}
	defer func() {
		if workerCmd.Process != nil {
			_ = workerCmd.Process.Kill()
		}
	}()

	// Wait 200ms for worker registration
	time.Sleep(200 * time.Millisecond)

	// Submit a sleep job that runs for 2.5 seconds
	submitPayload := `{"task_type":"sleep","payload":{"duration_ms":2500}}`
	resp, err := http.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(submitPayload))
	if err != nil {
		t.Fatalf("failed to submit job: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected HTTP 201 Created, got %d", resp.StatusCode)
	}
	var jobResp struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&jobResp)
	jobID := jobResp.ID

	// Wait until the job has been dispatched and is RUNNING
	ctx := context.Background()
	dispatchDeadline := time.Now().Add(5 * time.Second)
	dispatched := false
	for time.Now().Before(dispatchDeadline) {
		var state string
		query := fmt.Sprintf("SELECT state FROM %s.jobs WHERE id = $1;", schemaName)
		if err := pool.QueryRow(ctx, query, jobID).Scan(&state); err == nil && state == "RUNNING" {
			dispatched = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !dispatched {
		t.Fatalf("job was not dispatched within deadline")
	}

	// STOP the database container between dispatch and commit
	t.Log("Stopping PostgreSQL container mid-execution...")
	stopPostgresContainer(t)

	// Hold database stopped for 1.5 seconds while worker completes task and attempts commit
	time.Sleep(1500 * time.Millisecond)

	// RESTORE the database container
	t.Log("Restarting PostgreSQL container...")
	startPostgresContainer(t)

	// Wait for coordinator to restore readiness and worker commit to be processed
	waitDeadline := time.Now().Add(20 * time.Second)
	finalState := ""
	for time.Now().Before(waitDeadline) {
		resp, err := http.Get(fmt.Sprintf("%s/v1/jobs/%s", coordURL, jobID))
		if err == nil && resp.StatusCode == http.StatusOK {
			var res struct {
				State string `json:"state"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&res)
			_ = resp.Body.Close()
			if res.State == "SUCCEEDED" || res.State == "FAILED" {
				finalState = res.State
				break
			}
		} else if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}

	if finalState != "SUCCEEDED" {
		t.Fatalf("expected job to reach SUCCEEDED after DB recovery, got %q (coordinator logs: %s, worker logs: %s)",
			finalState, coordLogs.String(), workerLogs.String())
	}

	// Database invariant assertions:
	// 1. Exactly 1 attempt in attempts table (no duplicate attempts)
	var attemptCount int
	queryAttempts := fmt.Sprintf("SELECT COUNT(*) FROM %s.attempts WHERE job_id = $1;", schemaName)
	if err := pool.QueryRow(ctx, queryAttempts, jobID).Scan(&attemptCount); err != nil {
		t.Fatalf("failed to query attempts: %v", err)
	}
	if attemptCount != 1 {
		t.Fatalf("INVARIANT VIOLATION: expected exactly 1 attempt row, got %d", attemptCount)
	}

	// 2. Exactly 1 result in results table (no duplicate commit)
	var resultCount int
	queryResults := fmt.Sprintf("SELECT COUNT(*) FROM %s.results WHERE job_id = $1;", schemaName)
	if err := pool.QueryRow(ctx, queryResults, jobID).Scan(&resultCount); err != nil {
		t.Fatalf("failed to query results: %v", err)
	}
	if resultCount != 1 {
		t.Fatalf("INVARIANT VIOLATION: expected exactly 1 result row, got %d", resultCount)
	}

	// 3. No partial rows or NULL mandatory fields in attempts or results
	var attID, workerID, attState string
	queryAttRow := fmt.Sprintf("SELECT id, worker_id, state FROM %s.attempts WHERE job_id = $1;", schemaName)
	if err := pool.QueryRow(ctx, queryAttRow, jobID).Scan(&attID, &workerID, &attState); err != nil {
		t.Fatalf("partial or corrupted attempt row: %v", err)
	}
	if attID == "" || workerID == "" || attState == "" {
		t.Fatalf("partial row in attempts table: id=%q worker_id=%q state=%q", attID, workerID, attState)
	}

	var resAttID string
	var exitCode int
	queryResRow := fmt.Sprintf("SELECT attempt_id, exit_code FROM %s.results WHERE job_id = $1;", schemaName)
	if err := pool.QueryRow(ctx, queryResRow, jobID).Scan(&resAttID, &exitCode); err != nil {
		t.Fatalf("partial or corrupted result row: %v", err)
	}
	if resAttID != attID || exitCode != 0 {
		t.Fatalf("result row inconsistent: attempt_id=%q (expected %q), exit_code=%d", resAttID, attID, exitCode)
	}
	t.Log("PASS: Outage mid-dispatch and mid-commit handled cleanly with zero data loss or duplication")
}

// TestSecurity_PartD_LeakScan verifies Part D (e):
// Captures client responses and coordinator logs during outage tests and asserts
// zero occurrences of:
// - connection string
// - database password (read from environment, never printed)
// - SQL fragments
// - hostnames/ports of the database
// - lease tokens
func TestSecurity_PartD_LeakScan(t *testing.T) {
	schemaURL, pool, schemaName := setupPostgresTestSchema(t)

	// Absolute safety guard: always restore database container on cleanup
	t.Cleanup(func() {
		startPostgresContainer(t)
	})

	// Extract sensitive tokens from environment inside test (never print them)
	testDBURL := os.Getenv("MINICLOUD_TEST_DATABASE_URL")
	parsedURL, err := url.Parse(testDBURL)
	if err != nil {
		t.Fatalf("failed to parse test db url: %v", err)
	}
	dbPassword, _ := parsedURL.User.Password()
	dbHost := parsedURL.Hostname()
	dbPort := parsedURL.Port()
	if dbPort == "" {
		dbPort = "5432"
	}

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
		"MINICLOUD_DB_QUERY_TIMEOUT=1s",
	)
	var coordLogs safeBuffer
	coordCmd.Stdout = &coordLogs
	coordCmd.Stderr = &coordLogs
	if err := coordCmd.Start(); err != nil {
		t.Fatalf("failed to start coordinator: %v", err)
	}
	defer func() {
		if coordCmd.Process != nil {
			_ = coordCmd.Process.Kill()
		}
	}()

	// Wait for coordinator ready
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
		t.Fatalf("coordinator failed to become ready within deadline")
	}

	var capturedClientData []string

	recordResponse := func(resp *http.Response) {
		if resp == nil {
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			capturedClientData = append(capturedClientData, fmt.Sprintf("%s: %s", k, strings.Join(v, ",")))
		}
		b, _ := io.ReadAll(resp.Body)
		capturedClientData = append(capturedClientData, string(b))
	}

	client := &http.Client{Timeout: 3 * time.Second}

	// 0. Register a worker and submit a job to capture a REAL lease token issued at dispatch
	wRegPayload := `{"id":"leak-scan-worker","version":"v0.1.0","total_slots":2,"supported_tasks":["echo"]}`
	respReg, err := client.Post(coordURL+"/v1/workers/register", "application/json", strings.NewReader(wRegPayload))
	if err != nil {
		t.Fatalf("failed to register leak scan worker: %v", err)
	}
	respReg.Body.Close()

	jSubPayload := `{"task_type":"echo","payload":{"msg":"leak-scan-dispatch"}}`
	respSub, err := client.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(jSubPayload))
	if err != nil {
		t.Fatalf("failed to submit leak scan job: %v", err)
	}
	respSub.Body.Close()

	// Worker polls and receives attempt containing the real raw lease token
	respPoll, err := client.Post(coordURL+"/v1/workers/leak-scan-worker/poll", "application/json", nil)
	if err != nil {
		t.Fatalf("failed to poll for leak scan job: %v", err)
	}
	defer respPoll.Body.Close()
	if respPoll.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on worker poll, got %d", respPoll.StatusCode)
	}
	var pollResult struct {
		Assignment struct {
			AttemptID  string `json:"attempt_id"`
			LeaseToken string `json:"lease_token"`
		} `json:"assignment"`
	}
	if err := json.NewDecoder(respPoll.Body).Decode(&pollResult); err != nil {
		t.Fatalf("failed to decode worker poll response: %v", err)
	}
	realLeaseToken := pollResult.Assignment.LeaseToken
	if realLeaseToken == "" || len(realLeaseToken) < 32 {
		t.Fatalf("expected valid raw lease token (>=32 chars), got %q", realLeaseToken)
	}

	// 1. Send normal baseline traffic
	resp1, _ := client.Get(coordURL + "/readyz")
	recordResponse(resp1)

	resp2, _ := client.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(`{"task_type":"echo","payload":{"msg":"normal"}}`))
	recordResponse(resp2)

	// 2. Stop database container to initiate outage
	t.Log("Stopping PostgreSQL container for leak scan...")
	stopPostgresContainer(t)

	// 3. Send requests across multiple endpoints during outage
	resp3, _ := client.Get(coordURL + "/readyz")
	recordResponse(resp3)

	resp4, _ := client.Post(coordURL+"/v1/jobs", "application/json", strings.NewReader(`{"task_type":"echo","payload":{"msg":"outage"}}`))
	recordResponse(resp4)

	resp5, _ := client.Get(coordURL + "/v1/jobs/job_outage_check")
	recordResponse(resp5)

	resp6, _ := client.Post(coordURL+"/v1/workers/register", "application/json", strings.NewReader(`{"id":"outage-w1","total_slots":2,"supported_tasks":["echo"]}`))
	recordResponse(resp6)

	// 4. Restart database container
	t.Log("Restarting PostgreSQL container for leak scan...")
	startPostgresContainer(t)

	// 5. Send recovery request
	recDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(recDeadline) {
		recResp, err := client.Get(coordURL + "/readyz")
		if err == nil && recResp.StatusCode == http.StatusOK {
			recordResponse(recResp)
			break
		}
		if recResp != nil {
			recordResponse(recResp)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Terminate coordinator cleanly
	if coordCmd.Process != nil {
		_ = coordCmd.Process.Kill()
		_ = coordCmd.Wait()
	}

	allCapturedClient := strings.Join(capturedClientData, "\n---\n")
	allLogs := coordLogs.String()

	// LEAK SCAN ASSERTIONS:
	type leakTarget struct {
		name    string
		pattern string
	}

	scanForForbidden := func(sourceName, content string, targets []leakTarget) error {
		for _, target := range targets {
			if target.pattern != "" && strings.Contains(content, target.pattern) {
				return fmt.Errorf("detected %s (%q) in %s", target.name, target.pattern, sourceName)
			}
		}
		return nil
	}

	// Targets that must NEVER appear in client responses:
	// Note: Database port (:5433 or 5432) and socket dial details must not appear in client responses.
	clientForbidden := []leakTarget{
		{"connection_url", testDBURL},
		{"connection_url_escaped", url.QueryEscape(testDBURL)},
		{"database_password", dbPassword},
		{"database_host", dbHost},
		{"database_port_colon", ":" + dbPort},
		{"database_port_5432", "5432"},
		{"database_port_5433", "5433"},
		{"sql_select", "SELECT "},
		{"sql_insert", "INSERT INTO "},
		{"sql_update", "UPDATE "},
		{"sql_delete", "DELETE FROM "},
		{"sql_drop", "DROP TABLE"},
		{"pg_catalog", "pg_"},
		{"connection_refused", "connection refused"},
		{"dial_tcp", "dial tcp"},
		{"cluster_internal", "cluster.local"},
		{"real_lease_token", realLeaseToken},
	}

	if err := scanForForbidden("client responses", allCapturedClient, clientForbidden); err != nil {
		t.Fatalf("SECURITY LEAK IN CLIENT RESPONSES: %v", err)
	}

	// Targets that must NEVER appear in coordinator logs:
	// Note on 127.0.0.1 / ports:
	// The coordinator legitimately logs its own bind address "addr=127.0.0.1:<coordPort>" at startup,
	// so generic "127.0.0.1" is not forbidden in coordinator logs.
	// But the full database connection URL, database password, database socket port (:5433/:5432),
	// SQL query fragments, and raw lease tokens are strictly forbidden.
	logsForbidden := []leakTarget{
		{"connection_url", testDBURL},
		{"connection_url_escaped", url.QueryEscape(testDBURL)},
		{"database_password", dbPassword},
		{"database_port_colon", ":" + dbPort},
		{"sql_select", "SELECT "},
		{"sql_insert", "INSERT INTO "},
		{"sql_update", "UPDATE "},
		{"sql_delete", "DELETE FROM "},
		{"sql_drop", "DROP TABLE"},
		{"real_lease_token", realLeaseToken},
	}

	if err := scanForForbidden("coordinator logs", allLogs, logsForbidden); err != nil {
		t.Fatalf("SECURITY LEAK IN COORDINATOR LOGS: %v", err)
	}

	// Positive Control: plant a known fake token in a synthetic log buffer and assert scan fails on it
	t.Run("PositiveControl_PlantedTokenDetected", func(t *testing.T) {
		fakeToken, err := domain.NewLeaseToken()
		if err != nil {
			t.Fatalf("failed to generate fake token: %v", err)
		}
		syntheticDirtyLog := fmt.Sprintf("2026/10/26 00:00:00 INFO dispatched attempt_id=att_123 token=%s", fakeToken)
		err = scanForForbidden("synthetic_log", syntheticDirtyLog, []leakTarget{
			{"planted_lease_token", fakeToken},
		})
		if err == nil {
			t.Fatalf("POSITIVE CONTROL FAILED: leak scanner failed to detect planted lease token %s", fakeToken)
		}
		t.Logf("Positive control passed: planted token successfully detected: %v", err)
	})

	t.Log("PASS: Zero leak occurrences in client responses and coordinator logs across outage cycle")

	_ = pool
	_ = schemaName
}
