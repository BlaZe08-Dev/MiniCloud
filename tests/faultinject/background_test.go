package faultinject_test

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
	"net/http/httptest"
)

// countingRepo counts completed sweeper calls and can make the commit path report a deadline error
// after the commit was applied (an ambiguous commit).
type countingRepo struct {
	storage.Repository
	sweeps       atomic.Int64
	reaps        atomic.Int64
	ambiguousOne atomic.Bool // next CommitAttempt applies, then returns context.DeadlineExceeded
}

func (c *countingRepo) SweepExpiredLeases(ctx context.Context, p domain.SweepParams) (*domain.SweepResult, error) {
	res, err := c.Repository.SweepExpiredLeases(ctx, p)
	if err == nil {
		c.sweeps.Add(1)
	}
	return res, err
}

func (c *countingRepo) ReapStaleWorkers(ctx context.Context, d time.Duration) ([]string, error) {
	ids, err := c.Repository.ReapStaleWorkers(ctx, d)
	if err == nil {
		c.reaps.Add(1)
	}
	return ids, err
}

func (c *countingRepo) CommitAttempt(ctx context.Context, jobID, attemptID, token, workerID string, o domain.AttemptOutcome, b domain.BackoffFunc) (*domain.Job, error) {
	j, err := c.Repository.CommitAttempt(ctx, jobID, attemptID, token, workerID, o, b)
	if err == nil && c.ambiguousOne.CompareAndSwap(true, false) {
		return nil, context.DeadlineExceeded // the deadline fires after the commit applied
	}
	return j, err
}

// TestFault_Sweeper_ResumesAfterHalfOpenDatabase: the background sweeper loop has no request
// deadline. A database that goes half-open must not stop it for good: after the partition heals
// it must complete sweeps again within a bound.
func TestFault_Sweeper_ResumesAfterHalfOpenDatabase(t *testing.T) {
	pg := sectest.NewPGSchema(t)
	px := sectest.NewFaultProxy(t, pg.UpstreamAddr())
	pool, err := pg.OpenPool(context.Background(), pg.URLVia(px.Addr()), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	t.Cleanup(px.Close) // runs before pool.Close: severs half-open connections
	repo := &countingRepo{Repository: postgres.NewRepository(pool, postgres.WithLeaseDuration(30*time.Second))}
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), time.Hour)
	svc.SetGracePeriods(0, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); svc.StartLeaseSweeper(ctx, 200*time.Millisecond) }()
	wg.Add(1)
	go func() { defer wg.Done(); svc.StartReaper(ctx, 200*time.Millisecond) }()
	t.Cleanup(func() { cancel(); wg.Wait() })

	if !sectest.WaitFor(10*time.Second, func() bool { return repo.sweeps.Load() >= 3 }) {
		t.Fatal("sweeper never ran against a healthy database")
	}
	px.SetMode(sectest.ModeBlackhole)
	// Let the loop run into the half-open connection (several tick periods).
	time.Sleep(2 * time.Second)
	frozen, frozenReaps := repo.sweeps.Load(), repo.reaps.Load()
	px.SetMode(sectest.ModeForward)
	healed := time.Now()
	if !sectest.WaitFor(60*time.Second, func() bool { return repo.sweeps.Load() >= frozen+3 }) {
		t.Fatalf("sweeper did not resume within 60 s of the partition healing (sweeps stuck at %d)", repo.sweeps.Load())
	}
	if !sectest.WaitFor(60*time.Second, func() bool { return repo.reaps.Load() >= frozenReaps+3 }) {
		t.Fatalf("worker reaper did not resume within 60 s of the partition healing (reaps stuck at %d)", repo.reaps.Load())
	}
	t.Logf("sweeper completed 3 new sweeps %v after the partition healed", time.Since(healed).Round(100*time.Millisecond))
}

// TestFault_AmbiguousCommit_ReplayIsSafe: the request deadline fires after the commit was applied,
// so the worker sees a 503, retries with the same token and gets the documented 409
// JOB_ALREADY_TERMINAL (protocol 6.3, D-R3-02). The stored outcome must be the first one: one result
// row, job SUCCEEDED, attempt COMPLETED, nothing duplicated. A failure commit is checked the same
// way. (Returning 200 with the stored result on replay is a documented possible future change and
// would be a protocol change, so it is not asserted.)
func TestFault_AmbiguousCommit_ReplayIsSafe(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		base := sectest.NewRig(t, kind)
		repo := &countingRepo{Repository: base.Repo}
		svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), time.Hour)
		svc.SetLeaseDuration(base.Lease)
		if base.Clock != nil {
			svc.SetClock(base.Clock)
		}
		srv := httptest.NewServer(api.NewServer("127.0.0.1:0", svc).Handler())
		t.Cleanup(srv.Close)
		r := *base // same repository and clock; requests go through the wrapping service
		r.Srv = srv
		r.Svc = svc
		r.Register("w1", 2)

		for _, failure := range []bool{false, true} {
			job := r.Submit(3)
			a := r.Poll("w1")
			exit, errStr, class := 0, "", ""
			if failure {
				exit, errStr, class = 1, "boom", "PERMANENT"
			}
			repo.ambiguousOne.Store(true)
			first := r.Result(job, a.AttemptID, a.LeaseToken, "w1", exit, errStr, class)
			if first.Status != http.StatusServiceUnavailable {
				t.Fatalf("ambiguous commit answered %s, want 503 (deadline after apply)", first)
			}
			j := r.Job(job)
			want := domain.StateSucceeded
			if failure {
				want = domain.StateFailed
			}
			if j.State != want {
				t.Fatalf("after the ambiguous commit job is %s, want %s (the commit was applied)", j.State, want)
			}
			for i := 0; i < 3; i++ {
				replay := r.Result(job, a.AttemptID, a.LeaseToken, "w1", exit, errStr, class)
				if replay.Status != http.StatusConflict || replay.ErrCode() != "JOB_ALREADY_TERMINAL" {
					t.Fatalf("replay #%d = %s, want 409 JOB_ALREADY_TERMINAL", i, replay)
				}
			}
			if n := len(r.Attempts(job)); n != 1 {
				t.Fatalf("attempts = %d after replays, want 1", n)
			}
			wantRows := 1
			if r.ResultRows(job) > wantRows {
				t.Fatalf("result rows = %d, want at most 1", r.ResultRows(job))
			}
			if !failure && (r.ResultRows(job) != 1 || r.Attempts(job)[0].State != domain.AttemptCompleted) {
				t.Fatalf("success replay changed stored state: rows %d attempt %s", r.ResultRows(job), r.Attempts(job)[0].State)
			}
			sectest.CheckInvariants(t, &r, []string{job})
		}
	})
}
