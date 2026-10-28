package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// sweepRecorder wraps a repository and records what the coordinator's sweeper saw, so the test can
// wait for real sweeps (no sleeps for correctness) and prove the grace was actually exercised.
type sweepRecorder struct {
	storage.Repository
	mu         sync.Mutex
	sweeps     int
	suppressed int
	swept      int
}

func (r *sweepRecorder) SweepExpiredLeases(ctx context.Context, p domain.SweepParams) (*domain.SweepResult, error) {
	res, err := r.Repository.SweepExpiredLeases(ctx, p)
	if res != nil {
		r.mu.Lock()
		r.sweeps++
		r.suppressed += res.SuppressedByGrace
		r.swept += len(res.Swept)
		r.mu.Unlock()
	}
	return res, err
}

func (r *sweepRecorder) snapshot() (sweeps, suppressed, swept int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sweeps, r.suppressed, r.swept
}

// TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit is the coordinator-level restart
// grace test of slice 4c (ADR-0014): coordinator 1 dies while a worker holds an attempt, the
// database clock runs past the lease before coordinator 2 comes up, and coordinator 2's sweeper
// (running against PostgreSQL and database time) must hold the expired attempt back for the
// startup grace so the surviving worker's commit is still accepted. The control subtest runs the
// identical scenario without startup grace and shows the same attempt reaped and the late commit
// rejected, so the grace is what saves the commit and the scenario is not vacuous.
func TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit(t *testing.T) {
	t.Run("startup grace protects the surviving worker", func(t *testing.T) {
		r := restartScenario(t, 30*time.Second)
		if r.code != http.StatusOK || !strings.Contains(r.body, `"COMMITTED"`) {
			t.Fatalf("commit after restart = %d %s, want 200 COMMITTED", r.code, r.body)
		}
		if _, suppressed, swept := r.rec.snapshot(); suppressed < 1 || swept != 0 {
			t.Fatalf("sweeper suppressed=%d swept=%d, want the expired attempt held back and none reaped", suppressed, swept)
		}
		if r.attempt.State != domain.AttemptCompleted || r.job.State != domain.StateSucceeded {
			t.Fatalf("attempt %s, job %s, want COMPLETED and SUCCEEDED", r.attempt.State, r.job.State)
		}
	})

	t.Run("control without grace reaps and rejects the late commit", func(t *testing.T) {
		r := restartScenario(t, 0)
		if r.code != http.StatusConflict {
			t.Fatalf("commit with no grace = %d %s, want 409 (attempt reaped)", r.code, r.body)
		}
		if _, _, swept := r.rec.snapshot(); swept != 1 {
			t.Fatalf("sweeper reaped %d attempts, want 1", swept)
		}
		if r.attempt.State != domain.AttemptLost {
			t.Fatalf("attempt %s, want LOST", r.attempt.State)
		}
	})
}

type restartResult struct {
	code    int
	body    string
	rec     *sweepRecorder
	attempt *domain.Attempt
	job     *domain.Job
}

// restartScenario runs coordinator 1 (no sweeper), "kills" it, lets the database clock pass the
// lease, starts coordinator 2 with the given startup grace and a fast sweeper, waits for real
// sweeps, then has the worker commit through coordinator 2. It returns the commit response, the
// sweep recorder and the final attempt and job.
func restartScenario(t *testing.T, startupGrace time.Duration) restartResult {
	t.Helper()
	_, pool, _ := setupPostgresTestSchema(t)
	ctx := context.Background()
	const lease = 30 * time.Second
	repo := postgres.NewRepository(pool, postgres.WithLeaseDuration(lease))

	newCoordinator := func(r storage.Repository) (*coordinator.Service, *httptest.Server) {
		svc := coordinator.NewService(r, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), time.Hour)
		svc.SetLeaseDuration(lease)
		srv := httptest.NewServer(api.NewServer("127.0.0.1:0", svc).Handler())
		t.Cleanup(srv.Close)
		return svc, srv
	}
	post := func(srv *httptest.Server, path, body string) (int, string) {
		t.Helper()
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	// Coordinator 1: a worker registers, a job is submitted and claimed.
	_, srv1 := newCoordinator(repo)
	if c, b := post(srv1, "/v1/workers/register", `{"id":"survivor","version":"v","total_slots":1,"supported_tasks":["echo"]}`); c != http.StatusOK {
		t.Fatalf("register: %d %s", c, b)
	}
	c, b := post(srv1, "/v1/jobs", `{"task_type":"echo","payload":{"message":"x"},"max_attempts":3}`)
	if c != http.StatusCreated {
		t.Fatalf("submit: %d %s", c, b)
	}
	var job domain.Job
	_ = json.Unmarshal([]byte(b), &job)
	c, b = post(srv1, "/v1/workers/survivor/poll", ``)
	if c != http.StatusOK {
		t.Fatalf("poll: %d %s", c, b)
	}
	var poll struct {
		Assignment coordinator.WorkAssignment `json:"assignment"`
	}
	_ = json.Unmarshal([]byte(b), &poll)
	a := poll.Assignment

	// Coordinator 1 dies. The database clock passes the lease while no coordinator is running.
	srv1.Close()
	if _, err := pool.Exec(ctx, `UPDATE attempts SET lease_expires_at = clock_timestamp() - interval '1 minute' WHERE id = $1`, a.AttemptID); err != nil {
		t.Fatalf("expiring the lease: %v", err)
	}

	// Coordinator 2 starts on the same database with a fast sweeper.
	rec := &sweepRecorder{Repository: repo}
	svc2, srv2 := newCoordinator(rec)
	svc2.SetGracePeriods(startupGrace, 30*time.Second)
	svc2.SetSweepRandom(func() float64 { return 0.5 })
	sweepCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		svc2.StartLeaseSweeper(sweepCtx, 5*time.Millisecond)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()

	// Wait until the sweeper has actually looked at the expired attempt a few times.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		sweeps, suppressed, swept := rec.snapshot()
		if sweeps >= 5 && (suppressed > 0 || swept > 0) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if sweeps, suppressed, swept := rec.snapshot(); sweeps < 5 || (suppressed == 0 && swept == 0) {
		t.Fatalf("sweeper never saw the expired attempt: sweeps=%d suppressed=%d swept=%d", sweeps, suppressed, swept)
	}

	code, body := post(srv2, "/v1/jobs/"+a.JobID+"/attempts/"+a.AttemptID+"/result",
		`{"lease_token":"`+a.LeaseToken+`","worker_id":"survivor","exit_code":0,"output":"survived"}`)
	att, err := repo.GetAttempt(ctx, a.AttemptID)
	if err != nil {
		t.Fatalf("GetAttempt: %v", err)
	}
	final, err := repo.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	return restartResult{code: code, body: body, rec: rec, attempt: att, job: final}
}
