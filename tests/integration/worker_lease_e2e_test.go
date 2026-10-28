package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker/workertest"
)

// holdTask blocks until released or its context is cancelled, standing in for a long allowlisted task.
type holdTask struct {
	started   chan struct{}
	release   chan struct{}
	cancelled atomic.Bool
}

func (h *holdTask) Name() string            { return "hold" }
func (h *holdTask) Validate(_ []byte) error { return nil }
func (h *holdTask) Execute(ctx context.Context, _ []byte) (string, error) {
	h.started <- struct{}{}
	select {
	case <-h.release:
		return "finished", nil
	case <-ctx.Done():
		h.cancelled.Store(true)
		return "", ctx.Err()
	}
}

// leaseRig wires the real worker agent to the real coordinator handler over the in-memory
// repository. One FakeClock drives the repository, the coordinator and the agent's timers.
type leaseRig struct {
	t         *testing.T
	clk       *workertest.FakeClock
	repo      *storage.InMemoryRepository
	svc       *coordinator.Service
	sweeper   *coordinator.LeaseSweeper
	agent     *worker.Agent
	task      *holdTask
	dropRenew atomic.Bool // coordinator unreachable for renewals only
	dropRes   atomic.Bool // coordinator unreachable for results only
	asg       *coordinator.WorkAssignment
	jobID     string
	done      chan struct{}
}

func newLeaseRig(t *testing.T) *leaseRig {
	t.Helper()
	const lease = 30 * time.Second
	clk := workertest.NewFakeClock(time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC))
	task := &holdTask{started: make(chan struct{}, 1), release: make(chan struct{})}
	reg := executor.NewRegistry()
	reg.Register(task)
	repo := storage.NewInMemoryRepository(storage.WithClock(clk), storage.WithLeaseDuration(lease))
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), reg, time.Hour)
	svc.SetClock(clk)
	svc.SetLeaseDuration(lease)
	svc.SetGracePeriods(0, lease)
	svc.SetSweepRandom(func() float64 { return 0.5 })

	r := &leaseRig{t: t, clk: clk, repo: repo, svc: svc, task: task, done: make(chan struct{})}
	inner := api.NewServer("127.0.0.1:0", svc).Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if (r.dropRenew.Load() && strings.HasSuffix(req.URL.Path, "/renew")) || (r.dropRes.Load() && strings.HasSuffix(req.URL.Path, "/result")) {
			http.Error(w, `{"error":{"code":"SERVICE_UNAVAILABLE","message":"down"}}`, http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)

	ctx := context.Background()
	if err := svc.RegisterWorker(ctx, &domain.Worker{ID: "w1", TotalSlots: 1, SupportedTasks: []string{"hold"}}); err != nil {
		t.Fatal(err)
	}
	r.agent = worker.NewAgent(&config.WorkerConfig{WorkerID: "w1", CoordinatorURL: ts.URL, HeartbeatInterval: time.Second, TotalSlots: 1}, reg)
	r.agent.SetClock(clk)
	r.sweeper = svc.NewLeaseSweeper(5 * time.Second)

	job, err := svc.SubmitJob(ctx, "hold", []byte(`{}`), 3600, 3)
	if err != nil {
		t.Fatal(err)
	}
	r.jobID = job.ID
	if r.asg, err = svc.PollWork(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if r.asg.LeaseDurationSeconds != 30 {
		t.Fatalf("assignment lease_duration_seconds = %d, want 30", r.asg.LeaseDurationSeconds)
	}
	if err := r.sweeper.Tick(ctx); err != nil { // anchors the sweeper state at t0
		t.Fatal(err)
	}
	go func() {
		defer close(r.done)
		r.agent.ExecuteAndReport(ctx, r.asg)
	}()
	select {
	case <-task.started:
	case <-time.After(5 * time.Second):
		t.Fatal("task never started")
	}
	if !clk.BlockUntil(1, 5*time.Second) {
		t.Fatal("renewal loop never armed")
	}
	return r
}

// step advances the shared clock by d (the sweeper ticks every 5 s of it, as in production) and
// lets the agent settle on its next timer.
func (r *leaseRig) step(d time.Duration) {
	r.t.Helper()
	r.clk.Advance(d)
	if err := r.sweeper.Tick(context.Background()); err != nil {
		r.t.Fatalf("sweeper tick: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !r.isDone() && r.clk.Pending() < 1 {
		if time.Now().After(deadline) {
			r.t.Fatal("agent neither finished nor armed a timer")
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *leaseRig) isDone() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (r *leaseRig) waitDone() {
	r.t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.t.Fatal("agent did not finish")
	}
}

func (r *leaseRig) state() (domain.JobState, domain.AttemptState) {
	r.t.Helper()
	j, err := r.repo.GetJob(context.Background(), r.jobID)
	if err != nil {
		r.t.Fatal(err)
	}
	a, err := r.repo.GetAttempt(context.Background(), r.asg.AttemptID)
	if err != nil {
		r.t.Fatal(err)
	}
	return j.State, a.State
}

func TestWorkerLeaseE2E_RenewalsKeepLongJobAlivePastOneLease(t *testing.T) {
	r := newLeaseRig(t)
	for elapsed := 5 * time.Second; elapsed <= 70*time.Second; elapsed += 5 * time.Second {
		r.step(5 * time.Second)
		if js, as := r.state(); js != domain.StateRunning || as != domain.AttemptRunning {
			t.Fatalf("at t+%v job %s attempt %s, want RUNNING/RUNNING: renewals must outlive the 30 s lease", elapsed, js, as)
		}
	}
	close(r.task.release)
	r.waitDone()
	if js, as := r.state(); js != domain.StateSucceeded || as != domain.AttemptCompleted {
		t.Fatalf("after commit job %s attempt %s, want SUCCEEDED/COMPLETED", js, as)
	}
	if r.task.cancelled.Load() {
		t.Fatal("task was cancelled although the lease never lapsed")
	}
}

func TestWorkerLeaseE2E_WorkerWhoseRenewalsStopIsDeclaredLostAndAborts(t *testing.T) {
	r := newLeaseRig(t)
	r.step(5 * time.Second)
	r.step(5 * time.Second) // renewal at t+10 s extends the lease to t+40 s
	r.dropRenew.Store(true)
	for elapsed := 15 * time.Second; elapsed <= 45*time.Second; elapsed += 5 * time.Second {
		r.step(5 * time.Second)
	}
	if js, as := r.state(); js != domain.StateRetrying || as != domain.AttemptLost {
		t.Fatalf("job %s attempt %s, want RETRYING/LOST: the sweeper must declare the silent worker lost", js, as)
	}
	if r.task.cancelled.Load() {
		t.Fatal("the worker must not abort on transient renewal failures alone")
	}
	// The coordinator becomes reachable again: the zombie learns it was lost and aborts.
	r.dropRenew.Store(false)
	for i := 0; i < 4 && !r.isDone(); i++ { // the retry backoff is at most one renewal interval
		r.step(5 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	r.waitDone()
	if !r.task.cancelled.Load() {
		t.Fatal("the zombie worker kept running after the coordinator declared the attempt lost")
	}
	if js, as := r.state(); js != domain.StateRetrying || as != domain.AttemptLost {
		t.Fatalf("job %s attempt %s: the zombie's abort must not change coordinator state", js, as)
	}
}

func TestWorkerLeaseE2E_CancelDeliveredThroughRenewalStopsTask(t *testing.T) {
	r := newLeaseRig(t)
	r.step(5 * time.Second)
	if _, err := r.svc.CancelJob(context.Background(), r.jobID); err != nil {
		t.Fatal(err)
	}
	if r.task.cancelled.Load() {
		t.Fatal("cancel must not reach the worker before its next renewal")
	}
	r.step(5 * time.Second) // renewal at t+10 s carries cancel_requested
	r.waitDone()
	if !r.task.cancelled.Load() {
		t.Fatal("cancel_requested on the renewal response did not stop the task")
	}
	js, as := r.state()
	if as == domain.AttemptRunning || js == domain.StateRunning || js == domain.StateCancelRequested {
		t.Fatalf("job %s attempt %s: the cancelled attempt's outcome must have been reported and closed", js, as)
	}
}

func TestWorkerLeaseE2E_DeliveryBudgetExhaustedThenLeaseExpiryReclaimsJob(t *testing.T) {
	r := newLeaseRig(t)
	r.dropRes.Store(true) // results cannot be delivered; renewals still work
	close(r.task.release)
	waitFor := func(cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	waitFor(func() bool { return r.clk.Pending() >= 3 })
	for i := 0; i < 59; i++ {
		r.clk.Advance(time.Second)
		if i%5 == 4 {
			if err := r.sweeper.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		waitFor(func() bool { return r.clk.Pending() >= 3 })
		if js, as := r.state(); js != domain.StateRunning || as != domain.AttemptRunning {
			t.Fatalf("at +%ds job %s attempt %s: renewals must keep the lease alive while the result is retried", i+1, js, as)
		}
	}
	if r.isDone() {
		t.Fatal("agent gave up before the 60 s delivery budget")
	}
	r.clk.Advance(2 * time.Second)
	r.waitDone() // budget exhausted: the agent returns and renewals stop with it
	// No more renewals: the lease lapses and the sweeper reclaims the job.
	for i := 0; i < 8; i++ {
		r.clk.Advance(5 * time.Second)
		if err := r.sweeper.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if js, as := r.state(); js != domain.StateRetrying || as != domain.AttemptLost {
		t.Fatalf("job %s attempt %s, want RETRYING/LOST after the worker gave up and the lease lapsed", js, as)
	}
}
