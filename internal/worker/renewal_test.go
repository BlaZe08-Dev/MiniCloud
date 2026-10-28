package worker_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker/workertest"
)

var t0 = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

// blockTask blocks until released or until its context is cancelled, like a long allowlisted task.
type blockTask struct {
	started   chan struct{}
	release   chan struct{}
	cancelled atomic.Bool
	stubborn  bool // keeps running after its context is cancelled (an uncooperative task)
}

func newBlockTask() *blockTask {
	return &blockTask{started: make(chan struct{}, 1), release: make(chan struct{})}
}
func (b *blockTask) Name() string            { return "block" }
func (b *blockTask) Validate(_ []byte) error { return nil }
func (b *blockTask) Execute(ctx context.Context, _ []byte) (string, error) {
	b.started <- struct{}{}
	select {
	case <-b.release:
		return "done", nil
	case <-ctx.Done():
		b.cancelled.Store(true)
		if b.stubborn {
			<-b.release
			return "late", nil
		}
		return "", ctx.Err()
	}
}

// fakeCoordinator serves the renew and result endpoints with per-test behaviour.
type fakeCoordinator struct {
	mu          sync.Mutex
	renewCalls  int
	resultCalls int
	renewBodies []map[string]string
	renew       func(n int, w http.ResponseWriter, r *http.Request)
	result      func(n int, w http.ResponseWriter, r *http.Request)
}

func (f *fakeCoordinator) renews() int  { f.mu.Lock(); defer f.mu.Unlock(); return f.renewCalls }
func (f *fakeCoordinator) results() int { f.mu.Lock(); defer f.mu.Unlock(); return f.resultCalls }

func (f *fakeCoordinator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch {
	case len(r.URL.Path) > 6 && r.URL.Path[len(r.URL.Path)-6:] == "/renew":
		var m map[string]string
		_ = json.Unmarshal(body, &m)
		f.mu.Lock()
		f.renewCalls++
		n := f.renewCalls
		f.renewBodies = append(f.renewBodies, m)
		h := f.renew
		f.mu.Unlock()
		h(n, w, r)
	case len(r.URL.Path) > 7 && r.URL.Path[len(r.URL.Path)-7:] == "/result":
		f.mu.Lock()
		f.resultCalls++
		n := f.resultCalls
		h := f.result
		f.mu.Unlock()
		h(n, w, r)
	default:
		http.NotFound(w, r)
	}
}

func renewed(cancel bool) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "RENEWED", "lease_expires_at": t0.Add(30 * time.Second), "cancel_requested": cancel})
	}
}

func rejectWith(status int, code string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if code != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": "x"}})
		}
	}
}

func okResult(_ int, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

type rig struct {
	t      *testing.T
	coord  *fakeCoordinator
	clk    *workertest.FakeClock
	task   *blockTask
	agent  *worker.Agent
	asg    *coordinator.WorkAssignment
	done   chan struct{}
	cancel context.CancelFunc
}

func newRig(t *testing.T, renew, result func(int, http.ResponseWriter, *http.Request)) *rig {
	t.Helper()
	fc := &fakeCoordinator{renew: renew, result: result}
	ts := httptest.NewServer(fc)
	t.Cleanup(ts.Close)
	clk := workertest.NewFakeClock(t0)
	task := newBlockTask()
	reg := executor.NewRegistry()
	reg.Register(task)
	agent := worker.NewAgent(&config.WorkerConfig{WorkerID: "worker-1", CoordinatorURL: ts.URL, HeartbeatInterval: time.Second, TotalSlots: 1}, reg)
	agent.SetClock(clk)
	return &rig{t: t, coord: fc, clk: clk, task: task, agent: agent, done: make(chan struct{}),
		asg: &coordinator.WorkAssignment{JobID: "job-1", AttemptID: "att-1", LeaseToken: "tok-1", TaskType: "block",
			Payload: json.RawMessage(`{}`), TimeoutSeconds: 3600, LeaseDurationSeconds: 30}}
}

// start runs the assignment in the background and waits until the task is executing and the
// renewal loop has armed its first timer.
func (r *rig) start() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.t.Cleanup(cancel)
	go func() {
		defer close(r.done)
		r.agent.ExecuteAndReport(ctx, r.asg)
	}()
	select {
	case <-r.task.started:
	case <-time.After(5 * time.Second):
		r.t.Fatal("task never started")
	}
	if !r.clk.BlockUntil(1, 5*time.Second) {
		r.t.Fatal("renewal timer never armed")
	}
}

func (r *rig) waitDone() {
	r.t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.t.Fatal("ExecuteAndReport did not return")
	}
}

func (r *rig) isDone() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// tick advances the fake clock by d and waits until the renewal request it triggered has arrived.
func (r *rig) tick(d time.Duration, wantRenews int) {
	r.t.Helper()
	r.clk.Advance(d)
	waitFor(r.t, "renewal request", func() bool { return r.coord.renews() >= wantRenews })
}

// step advances the fake clock by one second during result delivery and waits until the agent is
// quiescent again (the renewal timer, the delivery budget and the next backoff are all armed) or
// has returned, so no timer is missed because a goroutine was still busy with an HTTP call.
func (r *rig) step() {
	r.t.Helper()
	r.clk.Advance(time.Second)
	waitFor(r.t, "agent to settle", func() bool { return r.isDone() || r.clk.Pending() >= 3 })
}

// stepUntilDone advances the fake clock in 1 s steps until the agent returns, with a hard guard
// so an unbounded delivery loop fails the test instead of hanging it.
func (r *rig) stepUntilDone(maxSteps int) time.Duration {
	r.t.Helper()
	start := r.clk.Now()
	for i := 0; i < maxSteps; i++ {
		if r.isDone() {
			return r.clk.Now().Sub(start)
		}
		r.step()
	}
	if !r.isDone() {
		r.t.Fatalf("agent still running after %d simulated seconds", maxSteps)
	}
	return r.clk.Now().Sub(start)
}

func TestAgent_PeriodicLeaseRenewal_Success(t *testing.T) {
	r := newRig(t, renewed(false), okResult)
	r.start()
	for i := 1; i <= 4; i++ {
		r.tick(10*time.Second, i)
		if !r.clk.BlockUntil(1, 5*time.Second) {
			t.Fatalf("renewal %d did not re-arm the timer", i)
		}
	}
	if r.task.cancelled.Load() {
		t.Fatal("task was cancelled by successful renewals")
	}
	for i, b := range r.coord.renewBodies {
		if b["worker_id"] != "worker-1" || b["lease_token"] != "tok-1" {
			t.Fatalf("renewal %d body = %v, want worker_id worker-1 and lease_token tok-1", i, b)
		}
	}
	close(r.task.release)
	r.waitDone()
	if r.coord.results() != 1 {
		t.Fatalf("results = %d, want 1", r.coord.results())
	}
	if r.clk.Pending() != 0 {
		t.Fatalf("%d timers pending after the attempt ended, the renewal loop must stop", r.clk.Pending())
	}
	before := r.coord.renews()
	r.clk.Advance(time.Minute)
	time.Sleep(20 * time.Millisecond)
	if r.coord.renews() != before {
		t.Fatalf("renewals continued after the attempt ended")
	}
}

func TestWorker_Renewal_IntervalDerivedFromAssignment(t *testing.T) {
	cases := []struct {
		name    string
		seconds int
		want    time.Duration
	}{
		{"30 s lease renews every 10 s", 30, 10 * time.Second},
		{"9 s lease renews every 3 s", 9, 3 * time.Second},
		{"missing duration falls back to 10 s", 0, 10 * time.Second},
		{"tiny lease is floored at 1 s", 1, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, renewed(false), okResult)
			r.asg.LeaseDurationSeconds = tc.seconds
			r.start()
			if got := r.clk.Requested()[0]; got != tc.want {
				t.Fatalf("first renewal timer = %v, want %v", got, tc.want)
			}
			close(r.task.release)
			r.waitDone()
		})
	}
}

func TestAgent_LeaseRenewal_ReceivesCancelRequested(t *testing.T) {
	r := newRig(t, renewed(true), okResult)
	r.start()
	r.tick(10*time.Second, 1)
	// Delivered within one renewal interval: the task is cancelled without any further clock movement.
	r.waitDone()
	if !r.task.cancelled.Load() {
		t.Fatal("task context was not cancelled by cancel_requested")
	}
	if r.coord.results() != 1 {
		t.Fatalf("results = %d, want 1: the outcome of a cancelled task is still reported", r.coord.results())
	}
	if r.coord.renews() != 1 {
		t.Fatalf("renewals = %d, want 1: renewing stops after cancel_requested", r.coord.renews())
	}
}

func TestAgent_LeaseRenewal_CancelRequested_StopsRenewingEvenIfTaskIgnoresCancel(t *testing.T) {
	r := newRig(t, renewed(true), okResult)
	r.task.stubborn = true
	r.start()
	r.tick(10*time.Second, 1)
	waitFor(t, "task to see the cancellation", r.task.cancelled.Load)
	// The lease is not extended on a cancel-requested renewal, so the worker must not keep
	// renewing: the lease has to lapse and the sweeper resolve the job (ADR-0016 race 2).
	for i := 0; i < 6; i++ {
		r.clk.Advance(10 * time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	if r.coord.renews() != 1 {
		t.Fatalf("renewals = %d, want 1: renewing must stop at cancel_requested", r.coord.renews())
	}
	if r.isDone() {
		t.Fatal("agent returned while the uncooperative task was still running")
	}
	close(r.task.release)
	r.waitDone()
	if r.coord.results() != 1 {
		t.Fatalf("results = %d, want 1", r.coord.results())
	}
}

func TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
	}{
		{"409 JOB_ALREADY_TERMINAL", 409, "JOB_ALREADY_TERMINAL"},
		{"409 LEASE_EXPIRED", 409, "LEASE_EXPIRED"},
		{"409 STALE_ATTEMPT_REJECTED", 409, "STALE_ATTEMPT_REJECTED"},
		{"403 WORKER_MISMATCH", 403, "WORKER_MISMATCH"},
		{"404 JOB_NOT_FOUND", 404, "JOB_NOT_FOUND"},
		{"404 ATTEMPT_NOT_FOUND", 404, "ATTEMPT_NOT_FOUND"},
		{"404 WORKER_NOT_FOUND", 404, "WORKER_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rejectWith(tc.status, tc.code), okResult)
			r.start()
			r.tick(10*time.Second, 1)
			r.waitDone()
			if !r.task.cancelled.Load() {
				t.Fatal("task was not aborted on lease loss")
			}
			if r.coord.results() != 0 {
				t.Fatalf("results = %d, want 0: a result for a lost attempt must not be reported", r.coord.results())
			}
			if r.coord.renews() != 1 {
				t.Fatalf("renewals = %d, want 1: renewing stops on terminal rejection", r.coord.renews())
			}
		})
	}
}

func TestWorker_Client_RenewOldCoordinatorGracefulDegradation(t *testing.T) {
	cases := []struct {
		name  string
		renew func(int, http.ResponseWriter, *http.Request)
	}{
		{"plain text 404 from a coordinator without the route", func(_ int, w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "404 page not found", http.StatusNotFound)
		}},
		{"404 with an unrecognised error code", rejectWith(404, "SOMETHING_ELSE")},
		{"404 with an empty body", rejectWith(404, "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.renew, okResult)
			r.start()
			r.tick(10*time.Second, 1)
			time.Sleep(20 * time.Millisecond)
			r.clk.Advance(time.Minute)
			time.Sleep(20 * time.Millisecond)
			if r.task.cancelled.Load() || r.isDone() {
				t.Fatal("task must keep running when the coordinator has no renew endpoint")
			}
			if r.coord.renews() != 1 {
				t.Fatalf("renewals = %d, want 1: renewals are disabled for this attempt", r.coord.renews())
			}
			close(r.task.release)
			r.waitDone()
			if r.coord.results() != 1 {
				t.Fatalf("results = %d, want 1: the result is still reported", r.coord.results())
			}
		})
	}
}

func TestWorker_Client_Renew400KeepsRunning(t *testing.T) {
	for _, code := range []string{"INVALID_REQUEST", "WORKER_TASK_UNSUPPORTED"} {
		t.Run(code, func(t *testing.T) {
			r := newRig(t, rejectWith(400, code), okResult)
			r.start()
			r.tick(10*time.Second, 1)
			time.Sleep(20 * time.Millisecond)
			r.clk.Advance(time.Minute)
			time.Sleep(20 * time.Millisecond)
			if r.task.cancelled.Load() || r.isDone() {
				t.Fatal("a 400 on renewal must not fail the executing task")
			}
			if r.coord.renews() != 1 {
				t.Fatalf("renewals = %d, want 1: renewals stop after a 400", r.coord.renews())
			}
			close(r.task.release)
			r.waitDone()
			if r.coord.results() != 1 {
				t.Fatalf("results = %d, want 1", r.coord.results())
			}
		})
	}
}

func TestWorker_Renewal_TransientErrorsNeverAbortAndAreBackedOff(t *testing.T) {
	// A 503 (database outage), a 500 and a dropped connection, then more 503s until the backoff
	// reaches its cap, then recovery.
	const failures = 8
	r := newRig(t, func(n int, w http.ResponseWriter, req *http.Request) {
		switch {
		case n == 2:
			rejectWith(500, "INTERNAL")(n, w, req)
		case n == 3:
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			_ = c.Close()
		case n <= failures:
			rejectWith(503, "SERVICE_UNAVAILABLE")(n, w, req)
		default:
			renewed(false)(n, w, req)
		}
	}, okResult)
	r.start()
	r.tick(10*time.Second, 1)
	for n := 2; n <= failures+1; n++ {
		if !r.clk.BlockUntil(1, 5*time.Second) {
			t.Fatalf("no retry timer after failure %d", n-1)
		}
		r.tick(10*time.Second, n)
	}
	if !r.clk.BlockUntil(1, 5*time.Second) {
		t.Fatal("no timer after the recovered renewal")
	}
	if r.task.cancelled.Load() || r.isDone() {
		t.Fatal("transient renewal errors must never abort the attempt")
	}
	got := r.clk.Requested()
	if len(got) != failures+2 {
		t.Fatalf("timers requested = %v, want %d", got, failures+2)
	}
	if got[0] != 10*time.Second || got[len(got)-1] != 10*time.Second {
		t.Fatalf("first and post-recovery waits = %v and %v, want the 10 s interval", got[0], got[len(got)-1])
	}
	for n := 1; n <= failures; n++ {
		hi := min(time.Second<<(n-1), 10*time.Second) // base 1 s, doubling, capped at the interval
		lo := hi / 2                                  // jitter factor in [0.5, 1]
		if d := got[n]; d < lo || d > hi {
			t.Fatalf("wait after failure %d = %v, want within [%v, %v]: bounded exponential backoff capped at the interval", n, d, lo, hi)
		}
	}
	close(r.task.release)
	r.waitDone()
}

func TestWorker_Client_ResultDeliveryLoop_BoundedRetry(t *testing.T) {
	r := newRig(t, renewed(false), rejectWith(503, "SERVICE_UNAVAILABLE"))
	r.start()
	close(r.task.release)
	elapsed := r.stepUntilDone(200)
	if elapsed < 60*time.Second || elapsed > 62*time.Second {
		t.Fatalf("delivery gave up after %v of simulated time, want the 60 s budget", elapsed)
	}
	if n := r.coord.results(); n < 5 || n > 62 {
		t.Fatalf("delivery attempts = %d, want a bounded number of backed-off retries", n)
	}
	// Delivery backoff: base 200 ms, doubling up to the 16x multiplier cap of ComputeBackoffWithJitter
	// (3.2 s, below the 5 s ceiling), jitter factor in [0.5, 1].
	var backoffs []time.Duration
	for _, d := range r.clk.Requested() {
		if d != 10*time.Second && d != ResultBudgetForTest {
			backoffs = append(backoffs, d)
		}
	}
	if len(backoffs) < 8 {
		t.Fatalf("only %d backoff waits recorded", len(backoffs))
	}
	for i, d := range backoffs {
		hi := min(200*time.Millisecond<<min(i, 4), 5*time.Second)
		if d < hi/2 || d > hi {
			t.Fatalf("delivery backoff %d = %v, want within [%v, %v]", i+1, d, hi/2, hi)
		}
	}
}

const ResultBudgetForTest = worker.ResultDeliveryBudget

func TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success(t *testing.T) {
	var clk *workertest.FakeClock
	var downUntil time.Time
	r := newRig(t, renewed(false), func(n int, w http.ResponseWriter, req *http.Request) {
		if clk.Now().Before(downUntil) {
			rejectWith(503, "SERVICE_UNAVAILABLE")(n, w, req)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	clk = r.clk
	downUntil = t0.Add(45 * time.Second)
	r.start()
	r.tick(10*time.Second, 1)
	close(r.task.release)
	r.clk.BlockUntil(1, 5*time.Second)
	elapsed := r.stepUntilDone(100)
	if r.clk.Now().Before(downUntil) {
		t.Fatalf("returned at %v, before the outage ended at %v", r.clk.Now(), downUntil)
	}
	if elapsed > 60*time.Second {
		t.Fatalf("delivery took %v of simulated time, want within the 60 s budget", elapsed)
	}
	if r.coord.results() < 2 {
		t.Fatalf("results = %d, want retries during the outage and a final success", r.coord.results())
	}
}

func TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	r := newRig(t, renewed(false), func(n int, w http.ResponseWriter, req *http.Request) {
		if failing.Load() {
			rejectWith(503, "SERVICE_UNAVAILABLE")(n, w, req)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	r.start()
	close(r.task.release)
	waitFor(t, "first delivery attempt", func() bool { return r.coord.results() >= 1 })
	for i := 0; i < 35 && !r.isDone(); i++ {
		r.step()
	}
	if r.isDone() {
		t.Fatal("agent gave up before the delivery budget")
	}
	if r.coord.renews() < 3 {
		t.Fatalf("renewals = %d during 35 s of delivery retries, want at least 3: the lease must stay alive while the result is retried", r.coord.renews())
	}
	failing.Store(false)
	r.stepUntilDone(30)
	after := r.coord.renews()
	r.clk.Advance(time.Minute)
	time.Sleep(20 * time.Millisecond)
	if r.coord.renews() != after || r.clk.Pending() != 0 {
		t.Fatal("renewals continued after delivery finished")
	}
}

func TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace(t *testing.T) {
	// A black-holed coordinator: the result request never answers.
	r := newRig(t, renewed(false), func(_ int, _ http.ResponseWriter, req *http.Request) { <-req.Context().Done() })
	r.start()
	close(r.task.release)
	waitFor(t, "result request in flight", func() bool { return r.coord.results() == 1 })
	r.clk.BlockUntil(1, 5*time.Second)
	r.clk.Advance(59 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if r.isDone() {
		t.Fatal("gave up before the 60 s budget")
	}
	r.clk.Advance(2 * time.Second)
	r.waitDone()
	if r.coord.results() != 1 {
		t.Fatalf("results = %d, want 1 in-flight request cut off at the budget", r.coord.results())
	}
}

func TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
	}{
		{"409 JOB_ALREADY_TERMINAL", 409, "JOB_ALREADY_TERMINAL"},
		{"409 LEASE_EXPIRED", 409, "LEASE_EXPIRED"},
		{"403 WORKER_MISMATCH", 403, "WORKER_MISMATCH"},
		{"404 JOB_NOT_FOUND", 404, "JOB_NOT_FOUND"},
		{"400 INVALID_REQUEST", 400, "INVALID_REQUEST"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, renewed(false), rejectWith(tc.status, tc.code))
			r.start()
			close(r.task.release)
			r.waitDone() // no clock movement at all: no backoff, no retry
			if r.coord.results() != 1 {
				t.Fatalf("delivery attempts = %d, want exactly 1", r.coord.results())
			}
		})
	}
}

func TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying(t *testing.T) {
	r := newRig(t, rejectWith(409, "LEASE_EXPIRED"), rejectWith(503, "SERVICE_UNAVAILABLE"))
	r.start()
	close(r.task.release)
	waitFor(t, "first delivery attempt", func() bool { return r.coord.results() >= 1 })
	r.clk.BlockUntil(2, 5*time.Second)
	r.tick(10*time.Second, 1)
	r.waitDone()
	n := r.coord.results()
	r.clk.Advance(time.Minute)
	time.Sleep(20 * time.Millisecond)
	if r.coord.results() != n {
		t.Fatal("kept delivering a result after the coordinator declared the lease lost")
	}
}

func TestWorker_ExecuteAndReport_Shutdown_StopsRenewalAndReturns(t *testing.T) {
	r := newRig(t, renewed(false), okResult)
	r.start()
	r.cancel()
	r.waitDone()
	if r.clk.Pending() != 0 {
		t.Fatalf("%d timers pending after shutdown", r.clk.Pending())
	}
}
