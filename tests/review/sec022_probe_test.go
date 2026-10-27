// Package review_test holds regression tests that began as auditor probes in review
// P4-00 and now run in the normal test tree.
package review_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestReviewP400_SEC022_DoubleCancelLeaksWorkerSlot drives the unauthenticated HTTP API:
// a 1-slot worker claims a job, the job is cancelled twice, the worker's late commit is
// rejected, and a second job is polled. The assertions state the invariant the design
// requires (fault-injection-plan.md section 3.2 invariant 4: no active attempt after a
// terminal job state), so the test FAILS while SEC-022 is present. Regression test for
// SEC-022; the Postgres subtest skips when MINICLOUD_TEST_DATABASE_URL is unset.
func TestReviewP400_SEC022_DoubleCancelLeaksWorkerSlot(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		repo := storage.NewInMemoryRepository()
		svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
		runSEC022Probe(t, svc, nil)
	})

	t.Run("postgres", func(t *testing.T) {
		pool := setupProbePool(t)
		repo := postgres.NewRepository(pool)
		svc := coordinator.NewPostgresService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
		runSEC022Probe(t, svc, pool)
	})
}

func runSEC022Probe(t *testing.T, svc *coordinator.Service, pool *pgxpool.Pool) {
	ts := httptest.NewServer(api.NewServer("127.0.0.1:0", svc).Handler())
	defer ts.Close()

	status, body := call(t, ts, "POST", "/v1/workers/register",
		`{"id":"w_probe","version":"v1","total_slots":1,"supported_tasks":["echo"]}`)
	t.Logf("register worker (1 slot): HTTP %d %s", status, body)

	jobA := submit(t, ts)
	status, body = call(t, ts, "POST", "/v1/workers/w_probe/poll", `{}`)
	t.Logf("poll #1: HTTP %d %s", status, body)
	var poll struct {
		Assignment coordinator.WorkAssignment `json:"assignment"`
	}
	if err := json.Unmarshal([]byte(body), &poll); err != nil || poll.Assignment.JobID != jobA {
		t.Fatalf("setup: expected assignment for %s, got HTTP %d %s", jobA, status, body)
	}
	a := poll.Assignment

	status, body = call(t, ts, "POST", "/v1/jobs/"+jobA+"/cancel", `{}`)
	t.Logf("cancel #1: HTTP %d %s", status, body)
	status, body = call(t, ts, "POST", "/v1/jobs/"+jobA+"/cancel", `{}`)
	t.Logf("cancel #2: HTTP %d %s", status, body)

	report := fmt.Sprintf(`{"lease_token":%q,"worker_id":"w_probe","exit_code":0,"output":"done"}`, a.LeaseToken)
	status, body = call(t, ts, "POST", "/v1/jobs/"+jobA+"/attempts/"+a.AttemptID+"/result", report)
	t.Logf("late worker commit: HTTP %d %s", status, body)

	status, body = call(t, ts, "GET", "/v1/jobs/"+jobA, "")
	t.Logf("job A after commit: HTTP %d %s", status, body)
	jobState := field(t, body, "state")

	status, body = call(t, ts, "GET", "/v1/jobs/"+jobA+"/attempts", "")
	t.Logf("attempts of job A: HTTP %d %s", status, body)
	var atts struct {
		Attempts []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"attempts"`
	}
	_ = json.Unmarshal([]byte(body), &atts)

	status, body = call(t, ts, "GET", "/v1/workers", "")
	t.Logf("workers: HTTP %d %s", status, body)

	if pool != nil {
		var avail, active int
		ctx := context.Background()
		if err := pool.QueryRow(ctx, `SELECT available_slots FROM worker_capacity WHERE id = 'w_probe'`).Scan(&avail); err != nil {
			t.Fatalf("query worker_capacity: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM attempts a JOIN jobs j ON j.id = a.job_id
			WHERE j.state IN ('SUCCEEDED','FAILED','CANCELLED','REJECTED') AND a.state IN ('PENDING','RUNNING')`).Scan(&active); err != nil {
			t.Fatalf("query active attempts on terminal jobs: %v", err)
		}
		t.Logf("SQL: worker_capacity.available_slots=%d, active attempts on terminal jobs=%d", avail, active)
	}

	jobB := submit(t, ts)
	status, body = call(t, ts, "POST", "/v1/workers/w_probe/poll", `{}`)
	t.Logf("poll #2 (job %s queued): HTTP %d %s", jobB, status, body)

	if jobState == "CANCELLED" {
		for _, att := range atts.Attempts {
			if att.State == "RUNNING" || att.State == "PENDING" {
				t.Errorf("INVARIANT VIOLATION: job %s is CANCELLED but attempt %s is still %s", jobA, att.ID, att.State)
			}
		}
	}
	if status != http.StatusOK {
		t.Errorf("SLOT LEAK: idle 1-slot worker cannot claim queued job %s: HTTP %d %s", jobB, status, body)
	}
}

func submit(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	status, body := call(t, ts, "POST", "/v1/jobs", `{"task_type":"echo","payload":{"message":"probe"}}`)
	if status != http.StatusCreated {
		t.Fatalf("submit: HTTP %d %s", status, body)
	}
	return field(t, body, "id")
}

func field(t *testing.T, body, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	s, _ := m[key].(string)
	return s
}

func call(t *testing.T, ts *httptest.Server, method, path, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// setupProbePool creates an isolated, migrated schema. It refuses any database whose
// name does not end in "_test" (the guard SEC-023 reports missing elsewhere).
func setupProbePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	testURL := os.Getenv("MINICLOUD_TEST_DATABASE_URL")
	if testURL == "" {
		t.Skip("skipping postgres probe: MINICLOUD_TEST_DATABASE_URL is unset")
	}
	cfg, err := pgxpool.ParseConfig(testURL)
	if err != nil {
		t.Fatalf("parse MINICLOUD_TEST_DATABASE_URL: %v", err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatalf("SAFETY VIOLATION: refusing to run test against database %q; name must end in '_test'", cfg.ConnConfig.Database)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, testURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)

	b := make([]byte, 4)
	_, _ = rand.Read(b)
	schema := "test_review_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if conn, err := pgx.Connect(c, testURL); err == nil {
			_, _ = conn.Exec(c, "DROP SCHEMA "+schema+" CASCADE")
			_ = conn.Close(c)
		}
	})
	if _, err := admin.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	if _, err := postgres.NewMigrator().Migrate(ctx, admin); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
