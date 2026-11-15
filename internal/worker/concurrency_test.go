package worker_test

// Tests of the bounded attempt pool (ADR-0019). Every wait ends on an event: a channel signal from
// the instrumented task, a request reaching the fake coordinator, a hook of the poll loop or a
// fake-clock timer being requested. Deadlines only turn a hang into a failure; no sleep decides a
// verdict.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
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

const ccWait = 10 * time.Second

// probeTask is the instrumented executor: it counts concurrent executions with an atomic
// high-water mark, announces each start, and blocks until the test releases that job ID (or its
// context ends; ignoreCancel keeps it running, like an uncooperative task).
type probeTask struct {
	cur, high    atomic.Int64
	started      chan string
	ignoreCancel bool

	mu        sync.Mutex
	rel       map[string]chan struct{}
	cancelled map[string]bool
}

func newProbe() *probeTask {
	return &probeTask{started: make(chan string, 64), rel: map[string]chan struct{}{}, cancelled: map[string]bool{}}
}

func (p *probeTask) Name() string            { return "probe" }
func (p *probeTask) Validate(_ []byte) error { return nil }

func (p *probeTask) relCh(id string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch, ok := p.rel[id]
	if !ok {
		ch = make(chan struct{})
		p.rel[id] = ch
	}
	return ch
}

func (p *probeTask) release(id string) { close(p.relCh(id)) }

func (p *probeTask) wasCancelled(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancelled[id]
}

func (p *probeTask) Execute(ctx context.Context, payload []byte) (string, error) {
	var in struct {
		ID    string `json:"id"`
		Panic bool   `json:"panic"`
		Fail  bool   `json:"fail"`
	}
	_ = json.Unmarshal(payload, &in)
	n := p.cur.Add(1)
	defer p.cur.Add(-1)
	for {
		h := p.high.Load()
		if n <= h || p.high.CompareAndSwap(h, n) {
			break
		}
	}
	if in.Panic {
		panic("probe panic secret-payload-" + in.ID)
	}
	if in.Fail {
		return "", errors.New("probe failure")
	}
	p.started <- in.ID
	select {
	case <-p.relCh(in.ID):
		return "out-" + in.ID, nil
	case <-ctx.Done():
		p.mu.Lock()
		p.cancelled[in.ID] = true
		p.mu.Unlock()
		if p.ignoreCancel {
			<-p.relCh(in.ID)
			return "late-" + in.ID, nil
		}
		return "", ctx.Err()
	}
}

// ccCoord is the fake coordinator. It hands out a scripted queue of assignments (204 when empty),
// checks every renew and result request against the assignment it belongs to, and reports a poll
// that arrives while the worker holds TotalSlots assignments without a result.
type ccCoord struct {
	t     *testing.T
	total int

	mu         sync.Mutex
	queue      []coordinator.WorkAssignment
	byJob      map[string]coordinator.WorkAssignment
	handed     int
	finished   int // results received plus attempts the test declared finished without a result
	results    map[string]int
	log        []string
	heartbeats []int
	registers  []int
	errs       []string
	hb404      bool
	allow      int // when gated, how many assignments may be handed out in total
	gated      bool
	renewFn    func(job string) (int, map[string]any)
	resultFn   func(job string) int

	polls    chan int
	renews   chan string
	resultCh chan string
	fullPoll chan string
}

func newCCCoord(t *testing.T, total int, asgs ...coordinator.WorkAssignment) *ccCoord {
	c := &ccCoord{t: t, total: total, queue: asgs, byJob: map[string]coordinator.WorkAssignment{}, results: map[string]int{},
		polls: make(chan int, 4096), renews: make(chan string, 4096), resultCh: make(chan string, 4096), fullPoll: make(chan string, 64)}
	for _, a := range asgs {
		c.byJob[a.JobID] = a
	}
	return c
}

func ccAsg(id string, extra string, lease int) coordinator.WorkAssignment {
	return coordinator.WorkAssignment{JobID: id, AttemptID: "att-" + id, AttemptNo: 1, LeaseToken: "tok-" + id, TaskType: "probe",
		Payload: json.RawMessage(fmt.Sprintf(`{"id":%q%s}`, id, extra)), TimeoutSeconds: 3600, LeaseDurationSeconds: lease}
}

func (c *ccCoord) errorf(format string, args ...any) {
	c.mu.Lock()
	c.errs = append(c.errs, fmt.Sprintf(format, args...))
	c.mu.Unlock()
}

// markFinished records an attempt that ends without a result (lost, panic), so the full-poll check
// stays exact.
func (c *ccCoord) markFinished() { c.mu.Lock(); c.finished++; c.mu.Unlock() }

func (c *ccCoord) resultCount(job string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.results[job]
}

func (c *ccCoord) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/v1/workers/register":
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		c.mu.Lock()
		c.registers = append(c.registers, int(m["available_slots"].(float64)))
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ACK"})
	case len(parts) == 4 && parts[3] == "heartbeat":
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		c.mu.Lock()
		c.heartbeats = append(c.heartbeats, int(m["available_slots"].(float64)))
		nf := c.hb404
		c.hb404 = false
		c.mu.Unlock()
		if nf {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ACK"})
	case len(parts) == 4 && parts[3] == "poll":
		c.mu.Lock()
		if c.handed-c.finished >= c.total {
			select {
			case c.fullPoll <- fmt.Sprintf("poll with %d assignments outstanding and %d slots", c.handed-c.finished, c.total):
			default:
			}
		}
		var asg *coordinator.WorkAssignment
		if len(c.queue) > 0 && (!c.gated || c.handed < c.allow) {
			a := c.queue[0]
			c.queue = c.queue[1:]
			c.handed++
			asg = &a
			c.log = append(c.log, "poll:"+a.JobID)
		} else {
			c.log = append(c.log, "poll:204")
		}
		c.mu.Unlock()
		c.polls <- 1
		if asg == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"assignment": asg})
	case len(parts) == 6 && (parts[5] == "renew" || parts[5] == "result"):
		job, att := parts[2], parts[4]
		c.mu.Lock()
		a, ok := c.byJob[job]
		c.mu.Unlock()
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if !ok || a.AttemptID != att || m["lease_token"] != a.LeaseToken || m["worker_id"] != "w-cc" {
			c.errorf("%s for job %s attempt %s carried token %v worker %v; want the assignment's own", parts[5], job, att, m["lease_token"], m["worker_id"])
		}
		if parts[5] == "renew" {
			status, resp := http.StatusOK, map[string]any{"status": "RENEWED", "cancel_requested": false}
			if c.renewFn != nil {
				status, resp = c.renewFn(job)
			}
			c.renews <- job
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		if out, _ := m["output"].(string); out != "" && out != "out-"+job && out != "late-"+job {
			c.errorf("result for job %s carried output %q", job, out)
		}
		status := http.StatusOK
		if c.resultFn != nil {
			status = c.resultFn(job)
		}
		c.mu.Lock()
		c.results[job]++
		c.finished++
		c.log = append(c.log, "result:"+job)
		c.mu.Unlock()
		c.resultCh <- job
		w.WriteHeader(status)
		if status != http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "STALE_ATTEMPT_REJECTED", "message": "x"}})
		}
	default:
		http.NotFound(w, r)
	}
}

func (c *ccCoord) check() {
	c.t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.errs {
		c.t.Error(e)
	}
}

// ccRig runs one agent against a ccCoord.
type ccRig struct {
	t        *testing.T
	coord    *ccCoord
	ts       *httptest.Server
	probe    *probeTask
	agent    *worker.Agent
	clk      *workertest.FakeClock // nil on the real clock
	slotWait chan struct{}
	done     chan struct{} // one signal per attempt that released its slot
	slotErrs chan string
	cancel   context.CancelFunc
	runDone  chan error
}

func newCCRig(t *testing.T, total int, fake bool, asgs ...coordinator.WorkAssignment) *ccRig {
	t.Helper()
	return newCCRigWith(t, total, fake, time.Hour, nil, asgs...)
}

// newCCRigWith is newCCRig with a heartbeat interval and an optional handler in front of the fake
// coordinator.
func newCCRigWith(t *testing.T, total int, fake bool, hb time.Duration, wrap func(http.Handler) http.Handler, asgs ...coordinator.WorkAssignment) *ccRig {
	t.Helper()
	r := &ccRig{t: t, coord: newCCCoord(t, total, asgs...), probe: newProbe(),
		slotWait: make(chan struct{}, 1), done: make(chan struct{}, 64), slotErrs: make(chan string, 64)}
	var h http.Handler = r.coord
	if wrap != nil {
		h = wrap(h)
	}
	r.ts = httptest.NewServer(h)
	t.Cleanup(r.ts.Close)
	reg := executor.NewRegistry()
	reg.Register(r.probe)
	r.agent = worker.NewAgent(&config.WorkerConfig{WorkerID: "w-cc", CoordinatorURL: r.ts.URL, HeartbeatInterval: hb, TotalSlots: total}, reg)
	if fake {
		r.clk = workertest.NewFakeClock(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC))
		r.agent.SetClock(r.clk)
	}
	worker.SetSlotWaitHook(r.agent, func() {
		select {
		case r.slotWait <- struct{}{}:
		default:
		}
	})
	worker.SetAttemptDoneHook(r.agent, func() {
		if n := worker.RunningForTest(r.agent); n < 0 || n > int64(total) {
			r.slotErrs <- fmt.Sprintf("running = %d after a release, outside [0, %d]", n, total)
		}
		r.done <- struct{}{}
	})
	return r
}

func (r *ccRig) start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.runDone = make(chan error, 1)
	go func() { r.runDone <- r.agent.Run(ctx) }()
}

// stop cancels Run and waits for it. On the fake clock the poll loop may sit in pollSleep, which
// does not watch ctx (unchanged V1 behaviour), so the clock is advanced until Run returns.
func (r *ccRig) stop() {
	r.t.Helper()
	r.cancel()
	deadline := time.After(ccWait)
	for {
		select {
		case err := <-r.runDone:
			if err != nil {
				r.t.Fatalf("Run: %v", err)
			}
			return
		case <-deadline:
			r.t.Fatalf("Run did not return after cancel")
		case <-time.After(5 * time.Millisecond):
			if r.clk != nil {
				r.clk.Advance(time.Second)
			}
		}
	}
}

// wait blocks on ch, failing on a poll while full, a slot-invariant breach, or the deadline.
func ccWaitOn[T any](r *ccRig, ch <-chan T, what string) T {
	r.t.Helper()
	select {
	case v := <-ch:
		return v
	case msg := <-r.coord.fullPoll:
		r.t.Fatalf("waiting for %s: %s", what, msg)
	case msg := <-r.slotErrs:
		r.t.Fatalf("waiting for %s: %s", what, msg)
	case <-time.After(ccWait):
		r.t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

func (r *ccRig) heartbeat() int {
	r.t.Helper()
	if err := worker.SendHeartbeatForTest(context.Background(), r.agent); err != nil {
		r.t.Fatalf("heartbeat: %v", err)
	}
	r.coord.mu.Lock()
	defer r.coord.mu.Unlock()
	return r.coord.heartbeats[len(r.coord.heartbeats)-1]
}

func quietLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestConcurrency_MaxInFlightNeverExceedsSlots: with 3 slots and 9 assignments the number of
// concurrently executing attempts reaches 3 and never exceeds it.
func TestConcurrency_MaxInFlightNeverExceedsSlots(t *testing.T) {
	quietLogs(t)
	const slots, jobs = 3, 9
	var asgs []coordinator.WorkAssignment
	for i := 1; i <= jobs; i++ {
		asgs = append(asgs, ccAsg(fmt.Sprintf("j%d", i), "", 30))
	}
	r := newCCRig(t, slots, false, asgs...)
	r.start()
	for i := 0; i < slots; i++ {
		ccWaitOn(r, r.probe.started, "first attempts to start")
	}
	ccWaitOn(r, r.slotWait, "poll loop to block on a full pool")
	if got := worker.RunningForTest(r.agent); got != slots {
		t.Fatalf("running = %d with a full pool, want %d", got, slots)
	}
	for i := 1; i <= jobs-slots; i++ {
		r.probe.release(fmt.Sprintf("j%d", i))
		ccWaitOn(r, r.probe.started, "the next attempt to start")
		ccWaitOn(r, r.slotWait, "poll loop to block again")
		if h := r.probe.high.Load(); h > slots {
			t.Fatalf("high-water mark %d exceeds %d slots", h, slots)
		}
	}
	for i := jobs - slots + 1; i <= jobs; i++ {
		r.probe.release(fmt.Sprintf("j%d", i))
	}
	for i := 0; i < jobs; i++ {
		ccWaitOn(r, r.coord.resultCh, "every result")
	}
	r.stop()
	if h := r.probe.high.Load(); h != slots {
		t.Fatalf("high-water mark = %d, want exactly %d (bounded and actually concurrent)", h, slots)
	}
	r.coord.check()
}

// TestConcurrency_HeartbeatTracksRunningAttempts: register reports TotalSlots at start and every
// heartbeat reports TotalSlots minus the attempts running at that moment.
func TestConcurrency_HeartbeatTracksRunningAttempts(t *testing.T) {
	quietLogs(t)
	const slots = 3
	r := newCCRig(t, slots, false, ccAsg("j1", "", 30), ccAsg("j2", "", 30), ccAsg("j3", "", 30))
	r.coord.gated = true // hand out one assignment at a time, so each step has an exact count
	if got := r.heartbeat(); got != slots {
		t.Fatalf("heartbeat before Run = %d, want %d", got, slots)
	}
	r.start()
	for k := 1; k <= slots; k++ {
		r.coord.mu.Lock()
		r.coord.allow = k
		r.coord.mu.Unlock()
		ccWaitOn(r, r.probe.started, "an attempt to start")
		if got := r.heartbeat(); got != slots-k {
			t.Fatalf("heartbeat with %d running = %d, want %d", k, got, slots-k)
		}
	}
	for k := 1; k <= slots; k++ {
		r.probe.release(fmt.Sprintf("j%d", k))
		ccWaitOn(r, r.done, "an attempt to release its slot")
		if got := r.heartbeat(); got != k {
			t.Fatalf("heartbeat after %d releases = %d, want %d", k, got, k)
		}
	}
	r.stop()
	r.coord.mu.Lock()
	regs := append([]int(nil), r.coord.registers...)
	r.coord.mu.Unlock()
	if len(regs) != 1 || regs[0] != slots {
		t.Fatalf("register bodies reported %v, want [%d]", regs, slots)
	}
	r.coord.check()
}

// TestConcurrency_FullWorkerDoesNotPoll: when every slot is busy the poll loop blocks on the pool
// and sends no poll.
func TestConcurrency_FullWorkerDoesNotPoll(t *testing.T) {
	quietLogs(t)
	r := newCCRig(t, 2, false, ccAsg("j1", "", 30), ccAsg("j2", "", 30))
	r.start()
	ccWaitOn(r, r.probe.started, "j1 to start")
	ccWaitOn(r, r.probe.started, "j2 to start")
	ccWaitOn(r, r.slotWait, "the poll loop to block on a full pool")
	r.coord.mu.Lock()
	polls := r.coord.handed
	r.coord.mu.Unlock()
	if polls != 2 {
		t.Fatalf("polls handed %d assignments, want 2", polls)
	}
	r.probe.release("j1")
	r.probe.release("j2")
	ccWaitOn(r, r.coord.resultCh, "a result")
	ccWaitOn(r, r.coord.resultCh, "a result")
	r.stop()
	r.coord.check()
}

// TestConcurrency_SlotReleasedOnEveryExit: each way an attempt can end gives its one slot back
// exactly once, and the worker polls again.
func TestConcurrency_SlotReleasedOnEveryExit(t *testing.T) {
	cases := []struct {
		name     string
		extra    string
		renew    func(string) (int, map[string]any)
		result   func(string) int
		advance  bool // fire the renewal timer
		release  bool // let the task finish normally
		noResult bool
	}{
		{name: "success", release: true},
		{name: "task_error", extra: `,"fail":true`},
		{name: "panic", extra: `,"panic":true`, noResult: true},
		{name: "lost_lease", advance: true, noResult: true,
			renew: func(string) (int, map[string]any) {
				return http.StatusConflict, map[string]any{"error": map[string]any{"code": "LEASE_EXPIRED", "message": "x"}}
			}},
		{name: "cancel_requested", advance: true,
			renew: func(string) (int, map[string]any) {
				return http.StatusOK, map[string]any{"status": "RENEWED", "cancel_requested": true}
			}},
		{name: "terminal_rejection", release: true, result: func(string) int { return http.StatusConflict }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := quietLogs(t)
			r := newCCRig(t, 1, true, ccAsg("j1", tc.extra, 30))
			r.coord.renewFn, r.coord.resultFn = tc.renew, tc.result
			if tc.noResult {
				r.coord.markFinished() // the attempt ends without a result; keep the full-poll check exact
			}
			r.start()
			ccWaitOn(r, r.coord.polls, "the first poll")
			if tc.extra == "" {
				ccWaitOn(r, r.probe.started, "the attempt to start")
			}
			if tc.advance {
				if !r.clk.BlockUntil(1, ccWait) {
					t.Fatal("renewal timer never armed")
				}
				r.clk.Advance(worker.DefaultRenewalInterval)
				ccWaitOn(r, r.coord.renews, "the renewal")
			}
			if tc.release {
				r.probe.release("j1")
			}
			ccWaitOn(r, r.done, "the slot release")
			ccWaitOn(r, r.coord.polls, "the poll that only a released slot allows")
			if got := worker.RunningForTest(r.agent); got != 0 {
				t.Fatalf("running = %d after the attempt ended, want 0", got)
			}
			if got := worker.AvailableSlotsForTest(r.agent); got != 1 {
				t.Fatalf("available slots = %d, want 1", got)
			}
			r.stop()
			if got := r.coord.resultCount("j1"); tc.noResult && got != 0 {
				t.Fatalf("%d results reported, want none", got)
			}
			if tc.name == "panic" {
				out := logs.String()
				if !strings.Contains(out, "attempt panicked") || !strings.Contains(out, "panic_type=string") {
					t.Fatalf("panic not logged with its type:\n%s", out)
				}
				for _, secret := range []string{"secret-payload", "tok-j1"} {
					if strings.Contains(out, secret) {
						t.Fatalf("log contains %q (payload or lease token):\n%s", secret, out)
					}
				}
			}
			select {
			case msg := <-r.slotErrs:
				t.Fatal(msg)
			default:
			}
			r.coord.check()
		})
	}
}

// TestConcurrency_ShutdownDrainsWithoutLeaks: cancelling Run with three running attempts stops
// polling, cancels the attempts through the existing path, returns, and leaves no goroutine behind.
func TestConcurrency_ShutdownDrainsWithoutLeaks(t *testing.T) {
	quietLogs(t)
	baseline := runtime.NumGoroutine()
	r := newCCRig(t, 3, false, ccAsg("j1", "", 30), ccAsg("j2", "", 30), ccAsg("j3", "", 30))
	r.start()
	for i := 0; i < 3; i++ {
		ccWaitOn(r, r.probe.started, "an attempt to start")
	}
	ccWaitOn(r, r.slotWait, "a full pool")
	r.stop()
	if got := worker.RunningForTest(r.agent); got != 0 {
		t.Fatalf("running = %d after Run returned, want 0", got)
	}
	for _, id := range []string{"j1", "j2", "j3"} {
		if !r.probe.wasCancelled(id) {
			t.Fatalf("attempt %s was not cancelled by shutdown", id)
		}
	}
	r.ts.Close()
	worker.CloseIdleConnectionsForTest(r.agent)
	deadline := time.Now().Add(ccWait)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			t.Fatalf("goroutines: %d after shutdown, baseline %d\n%s", runtime.NumGoroutine(), baseline, buf[:n])
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.coord.check()
}

// TestConcurrency_ShutdownDrainIsBounded: a task that ignores cancellation holds Run only until
// the drain bound (ResultDeliveryBudget on the agent clock).
func TestConcurrency_ShutdownDrainIsBounded(t *testing.T) {
	logs := quietLogs(t)
	r := newCCRig(t, 1, true, ccAsg("j1", "", 30))
	r.probe.ignoreCancel = true
	r.start()
	ccWaitOn(r, r.probe.started, "the attempt to start")
	ccWaitOn(r, r.slotWait, "a full pool")
	before := len(r.clk.Requested())
	r.cancel()
	deadline := time.Now().Add(ccWait)
	for {
		req := r.clk.Requested()
		found := false
		for _, d := range req[before:] {
			if d == worker.ResultDeliveryBudget {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("drain timer never requested; timers %v", req[before:])
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-r.runDone:
		t.Fatal("Run returned before the drain bound although an attempt is still running")
	default:
	}
	r.clk.Advance(worker.ResultDeliveryBudget)
	select {
	case err := <-r.runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(ccWait):
		t.Fatal("Run did not return after the drain bound")
	}
	if !strings.Contains(logs.String(), "shutdown drain bound reached") || !strings.Contains(logs.String(), "running=1") {
		t.Fatalf("drain bound not logged with the running count:\n%s", logs.String())
	}
	r.probe.release("j1")
	ccWaitOn(r, r.done, "the abandoned attempt to finish")
	if got := worker.RunningForTest(r.agent); got != 0 {
		t.Fatalf("running = %d, want 0", got)
	}
}

// TestConcurrency_ResultsNeverCrossBetweenAttempts: two concurrent attempts with different lease
// tokens; every renew and result request carries its own assignment's IDs and token, and each
// output is its own.
func TestConcurrency_ResultsNeverCrossBetweenAttempts(t *testing.T) {
	quietLogs(t)
	r := newCCRig(t, 2, true, ccAsg("j1", "", 3), ccAsg("j2", "", 3))
	r.start()
	ccWaitOn(r, r.probe.started, "an attempt to start")
	ccWaitOn(r, r.probe.started, "an attempt to start")
	ccWaitOn(r, r.slotWait, "a full pool")
	for i := 0; i < 2; i++ {
		if !r.clk.BlockUntil(2, ccWait) {
			t.Fatal("renewal timers never armed")
		}
		r.clk.Advance(time.Second)
		seen := map[string]bool{}
		seen[ccWaitOn(r, r.coord.renews, "a renewal")] = true
		seen[ccWaitOn(r, r.coord.renews, "a renewal")] = true
		if !seen["j1"] || !seen["j2"] {
			t.Fatalf("round %d renewed %v, want both attempts", i, seen)
		}
	}
	r.probe.release("j2") // reverse order of start
	if got := ccWaitOn(r, r.coord.resultCh, "j2's result"); got != "j2" {
		t.Fatalf("first result for %s, want j2", got)
	}
	r.probe.release("j1")
	if got := ccWaitOn(r, r.coord.resultCh, "j1's result"); got != "j1" {
		t.Fatalf("second result for %s, want j1", got)
	}
	r.stop()
	if r.coord.resultCount("j1") != 1 || r.coord.resultCount("j2") != 1 {
		t.Fatalf("results j1=%d j2=%d, want one each", r.coord.resultCount("j1"), r.coord.resultCount("j2"))
	}
	r.coord.check()
}

// TestConcurrency_LostLeaseIsolatedToOneAttempt: a 409 on j1's renewal cancels j1 and suppresses
// its result; j2 keeps renewing and delivers.
func TestConcurrency_LostLeaseIsolatedToOneAttempt(t *testing.T) {
	quietLogs(t)
	r := newCCRig(t, 2, true, ccAsg("j1", "", 30), ccAsg("j2", "", 30))
	r.coord.renewFn = func(job string) (int, map[string]any) {
		if job == "j1" {
			return http.StatusConflict, map[string]any{"error": map[string]any{"code": "LEASE_EXPIRED", "message": "x"}}
		}
		return http.StatusOK, map[string]any{"status": "RENEWED", "cancel_requested": false}
	}
	r.coord.markFinished() // j1 will end without a result
	r.start()
	ccWaitOn(r, r.probe.started, "an attempt to start")
	ccWaitOn(r, r.probe.started, "an attempt to start")
	if !r.clk.BlockUntil(2, ccWait) {
		t.Fatal("renewal timers never armed")
	}
	r.clk.Advance(worker.DefaultRenewalInterval)
	ccWaitOn(r, r.coord.renews, "a renewal")
	ccWaitOn(r, r.coord.renews, "a renewal")
	ccWaitOn(r, r.done, "j1 to release its slot")
	if !r.probe.wasCancelled("j1") || r.probe.wasCancelled("j2") {
		t.Fatalf("cancelled j1=%v j2=%v, want only j1", r.probe.wasCancelled("j1"), r.probe.wasCancelled("j2"))
	}
	if got := worker.RunningForTest(r.agent); got != 1 {
		t.Fatalf("running = %d, want 1 (j2)", got)
	}
	// j2 renews again on its own timer. Its re-armed timer may not exist yet, so the clock is
	// advanced until the renewal arrives (advancing further is harmless).
	deadline := time.After(ccWait)
	for j2Renewed := false; !j2Renewed; {
		select {
		case job := <-r.coord.renews:
			j2Renewed = job == "j2"
		case <-deadline:
			t.Fatal("j2 never renewed again after j1 lost its lease")
		case <-time.After(5 * time.Millisecond):
			r.clk.Advance(worker.DefaultRenewalInterval)
		}
	}
	r.probe.release("j2")
	if got := ccWaitOn(r, r.coord.resultCh, "j2's result"); got != "j2" {
		t.Fatalf("result for %s, want j2", got)
	}
	r.stop()
	if r.coord.resultCount("j1") != 0 {
		t.Fatalf("j1 reported %d results after losing its lease, want 0", r.coord.resultCount("j1"))
	}
	r.coord.check()
}

// TestConcurrency_ReRegisterReportsLiveSlots: a re-registration after a heartbeat 404 reports the
// live free slots, not TotalSlots.
func TestConcurrency_ReRegisterReportsLiveSlots(t *testing.T) {
	quietLogs(t)
	r := newCCRig(t, 3, false, ccAsg("j1", "", 30), ccAsg("j2", "", 30))
	r.start()
	ccWaitOn(r, r.probe.started, "an attempt to start")
	ccWaitOn(r, r.probe.started, "an attempt to start")
	r.coord.mu.Lock()
	r.coord.hb404 = true
	r.coord.mu.Unlock()
	if err := worker.SendHeartbeatForTest(context.Background(), r.agent); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	r.coord.mu.Lock()
	regs := append([]int(nil), r.coord.registers...)
	r.coord.mu.Unlock()
	if len(regs) != 2 || regs[0] != 3 || regs[1] != 1 {
		t.Fatalf("register bodies reported %v, want [3 1]", regs)
	}
	r.probe.release("j1")
	r.probe.release("j2")
	ccWaitOn(r, r.coord.resultCh, "a result")
	ccWaitOn(r, r.coord.resultCh, "a result")
	r.stop()
	r.coord.check()
}

// TestConcurrency_SingleSlotIsSerial is the rollback proof (ADR-0019 Section 4): with one slot
// the worker polls only when nothing executes, as the V1 loop did.
func TestConcurrency_SingleSlotIsSerial(t *testing.T) {
	quietLogs(t)
	r := newCCRig(t, 1, true, ccAsg("j1", "", 30), ccAsg("j2", "", 30), ccAsg("j3", "", 30))
	r.start()
	for _, id := range []string{"j1", "j2", "j3"} {
		if got := ccWaitOn(r, r.probe.started, id+" to start"); got != id {
			t.Fatalf("started %s, want %s", got, id)
		}
		ccWaitOn(r, r.slotWait, "the poll loop to wait for the only slot")
		if got := r.heartbeat(); got != 0 {
			t.Fatalf("heartbeat while %s runs = %d, want 0", id, got)
		}
		r.probe.release(id)
		ccWaitOn(r, r.coord.resultCh, id+"'s result")
	}
	for i := 0; i < 4; i++ { // three polls with an assignment, then the one after the last result
		ccWaitOn(r, r.coord.polls, "a poll")
	}
	r.stop()
	r.coord.mu.Lock()
	log := append([]string(nil), r.coord.log...)
	r.coord.mu.Unlock()
	want := []string{"poll:j1", "result:j1", "poll:j2", "result:j2", "poll:j3", "result:j3", "poll:204"}
	if len(log) < len(want) {
		t.Fatalf("request log %v, want it to start with %v", log, want)
	}
	for i, w := range want {
		if log[i] != w {
			t.Fatalf("request log %v, want it to start with %v", log, want)
		}
	}
	for _, e := range log[len(want):] {
		if e != "poll:204" {
			t.Fatalf("request log %v has %q after the queue drained", log, e)
		}
	}
	if h := r.probe.high.Load(); h != 1 {
		t.Fatalf("high-water mark = %d, want 1", h)
	}
	r.coord.check()
}

// TestLoadWorkerConfig_SlotsUpperBound: MINICLOUD_WORKER_SLOTS is bounded to [1, 1024] at startup
// (ADR-0019 Section 2.6), and the config copy of the bound equals the coordinator's (SEC-015).
func TestLoadWorkerConfig_SlotsUpperBound(t *testing.T) {
	if config.MaxWorkerSlots != coordinator.MaxWorkerSlots {
		t.Fatalf("config.MaxWorkerSlots = %d, coordinator.MaxWorkerSlots = %d; they must be equal", config.MaxWorkerSlots, coordinator.MaxWorkerSlots)
	}
	cases := []struct {
		val string
		ok  bool
	}{{"1", true}, {"4", true}, {"1024", true}, {"1025", false}, {"100000", false}, {"0", false}, {"-1", false}}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("MINICLOUD_WORKER_SLOTS", tc.val)
			cfg, err := config.LoadWorkerConfig()
			if tc.ok {
				if err != nil {
					t.Fatalf("rejected %s: %v", tc.val, err)
				}
				if fmt.Sprint(cfg.TotalSlots) != tc.val {
					t.Fatalf("TotalSlots = %d, want %s", cfg.TotalSlots, tc.val)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "MINICLOUD_WORKER_SLOTS") {
				t.Fatalf("value %s: err = %v, want a rejection naming MINICLOUD_WORKER_SLOTS", tc.val, err)
			}
		})
	}
}

// hookHandler records every log line as text and then calls hook with the line's message, on the
// logging goroutine. The hook may block (to hold that goroutine) or panic (to inject a fault).
type hookHandler struct {
	inner slog.Handler
	hook  func(msg string)
}

func (h *hookHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *hookHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &hookHandler{inner: h.inner.WithAttrs(a), hook: h.hook}
}
func (h *hookHandler) WithGroup(g string) slog.Handler {
	return &hookHandler{inner: h.inner.WithGroup(g), hook: h.hook}
}
func (h *hookHandler) Handle(ctx context.Context, rec slog.Record) error {
	err := h.inner.Handle(ctx, rec)
	h.hook(rec.Message)
	return err
}

func hookLogs(t *testing.T, hook func(msg string)) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(&hookHandler{inner: slog.NewTextHandler(buf, nil), hook: hook}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// faultClock is the fake clock with injected faults (the agent's Clock seam): NewTimer panics when
// newTimer says so for the requested duration, and the returned timer's C panics when timerC says
// so. The predicates run concurrently and decide "once" themselves.
type faultClock struct {
	*workertest.FakeClock
	newTimer func(time.Duration) bool
	timerC   func(time.Duration) bool
}

const injectedPanic = "injected fault secret-payload tok-j2"

func (c *faultClock) NewTimer(d time.Duration) worker.Timer {
	if c.newTimer != nil && c.newTimer(d) {
		panic(injectedPanic)
	}
	t := c.FakeClock.NewTimer(d)
	if c.timerC != nil && c.timerC(d) {
		return panicTimer{t}
	}
	return t
}

type panicTimer struct{ worker.Timer }

func (panicTimer) C() <-chan time.Time { panic(injectedPanic) }

// onceFor returns a predicate that is true the first time it sees d and false otherwise.
func onceFor(d time.Duration) func(time.Duration) bool {
	var fired atomic.Bool
	return func(got time.Duration) bool { return got == d && fired.CompareAndSwap(false, true) }
}

// assertPanicLogged checks the recovered-panic line: goroutine name, IDs, type, never the value.
func assertPanicLogged(t *testing.T, out, goroutine string, want ...string) {
	t.Helper()
	for _, w := range append([]string{"agent goroutine panicked", "goroutine=" + goroutine, "panic_type=string"}, want...) {
		if !strings.Contains(out, w) {
			t.Fatalf("log lacks %q:\n%s", w, out)
		}
	}
	for _, secret := range []string{"secret-payload", "tok-j1", "tok-j2"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log contains %q (panic value or lease token):\n%s", secret, out)
		}
	}
}

// TestConcurrency_RunWaitsForHeartbeatGoroutine: after cancel, Run does not return while the
// heartbeat goroutine is still busy (ADR-0019 Section 2.5). The heartbeat goroutine is held inside
// its "heartbeat failed" log call. The 300 ms negative window can only let a broken build pass on a
// very slow machine, never fail a correct one.
func TestConcurrency_RunWaitsForHeartbeatGoroutine(t *testing.T) {
	hbFailed, shutting, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var o1, o2, o3 sync.Once
	rel := func() { o3.Do(func() { close(release) }) }
	t.Cleanup(rel)
	hookLogs(t, func(msg string) {
		switch msg {
		case "heartbeat failed":
			o1.Do(func() { close(hbFailed) })
			<-release
		case "worker agent shutting down":
			o2.Do(func() { close(shutting) })
		}
	})
	hbIn := make(chan struct{}, 1)
	hang := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasSuffix(req.URL.Path, "/heartbeat") {
				select {
				case hbIn <- struct{}{}:
				default:
				}
				_, _ = io.Copy(io.Discard, req.Body)
				<-req.Context().Done() // answered only by the worker's cancel
				return
			}
			next.ServeHTTP(w, req)
		})
	}
	r := newCCRigWith(t, 1, false, 5*time.Millisecond, hang)
	r.start()
	ccWaitOn(r, hbIn, "a heartbeat in flight")
	r.cancel()
	ccWaitOn(r, hbFailed, "the heartbeat goroutine to log its failure")
	ccWaitOn(r, shutting, "the poll loop to shut down")
	select {
	case <-r.runDone:
		t.Fatal("Run returned while the heartbeat goroutine was still running (heartbeat WaitGroup not waited)")
	case <-time.After(300 * time.Millisecond):
	}
	rel()
	select {
	case err := <-r.runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(ccWait):
		t.Fatal("Run did not return after the heartbeat goroutine finished")
	}
}

// TestConcurrency_RenewalPanicTreatsLeaseAsLost: a panic in j2's renewal goroutine (injected through
// the Clock seam: NewTimer panics for j2's 2 s renewal interval; j1 renews every 10 s) is recovered.
// j2 is treated as lost: its task is cancelled, no result is sent, and its slot comes back. j1 keeps
// running, renewing and delivers; the slot count stays exact.
func TestConcurrency_RenewalPanicTreatsLeaseAsLost(t *testing.T) {
	logs := quietLogs(t)
	r := newCCRig(t, 2, true, ccAsg("j1", "", 30), ccAsg("j2", "", 6))
	r.agent.SetClock(&faultClock{FakeClock: r.clk, newTimer: onceFor(2 * time.Second)})
	r.coord.markFinished() // j2 ends without a result
	r.start()
	ccWaitOn(r, r.probe.started, "an attempt to start")
	ccWaitOn(r, r.probe.started, "an attempt to start")
	ccWaitOn(r, r.done, "j2's slot release after its renewal goroutine panicked")
	if got := worker.RunningForTest(r.agent); got != 1 {
		t.Fatalf("running = %d after j2 ended, want 1 (j1)", got)
	}
	if !r.probe.wasCancelled("j2") || r.probe.wasCancelled("j1") {
		t.Fatalf("cancelled j1=%v j2=%v, want only j2", r.probe.wasCancelled("j1"), r.probe.wasCancelled("j2"))
	}
	if !r.clk.BlockUntil(1, ccWait) {
		t.Fatal("j1's renewal timer never armed")
	}
	r.clk.Advance(worker.DefaultRenewalInterval)
	if got := ccWaitOn(r, r.coord.renews, "j1's renewal"); got != "j1" {
		t.Fatalf("renewal for %s, want j1", got)
	}
	r.probe.release("j1")
	if got := ccWaitOn(r, r.coord.resultCh, "j1's result"); got != "j1" {
		t.Fatalf("result for %s, want j1", got)
	}
	ccWaitOn(r, r.done, "j1's slot release")
	if got := worker.RunningForTest(r.agent); got != 0 {
		t.Fatalf("running = %d, want 0", got)
	}
	r.stop()
	if r.coord.resultCount("j1") != 1 || r.coord.resultCount("j2") != 0 {
		t.Fatalf("results j1=%d j2=%d, want 1 and 0", r.coord.resultCount("j1"), r.coord.resultCount("j2"))
	}
	assertPanicLogged(t, logs.String(), "renewal", "job_id=j2", "attempt_id=att-j2")
	r.coord.check()
}

// TestConcurrency_DeliveryBudgetPanicExpiresDelivery: a panic in j2's delivery-budget goroutine
// (injected through the Clock seam: the budget timer's C panics) is recovered by expiring delivery
// at once. j2's results fail with 500 and the fake clock is never advanced, so only that expiry can
// end j2's delivery and free its slot. j1 keeps running and delivers; the slot count stays exact.
func TestConcurrency_DeliveryBudgetPanicExpiresDelivery(t *testing.T) {
	logs := quietLogs(t)
	r := newCCRig(t, 2, true, ccAsg("j1", "", 30), ccAsg("j2", "", 30))
	r.coord.total = 1 << 30 // j2's failed results may or may not reach the coordinator; slots are checked directly
	r.coord.resultFn = func(job string) int {
		if job == "j2" {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	}
	r.agent.SetClock(&faultClock{FakeClock: r.clk, timerC: onceFor(worker.ResultDeliveryBudget)})
	r.start()
	ccWaitOn(r, r.probe.started, "an attempt to start")
	ccWaitOn(r, r.probe.started, "an attempt to start")
	ccWaitOn(r, r.slotWait, "a full pool")
	r.probe.release("j2")
	ccWaitOn(r, r.done, "j2's slot release after its delivery-budget goroutine panicked")
	if got := worker.RunningForTest(r.agent); got != 1 {
		t.Fatalf("running = %d after j2 ended, want 1 (j1)", got)
	}
	if r.probe.wasCancelled("j1") {
		t.Fatal("j1 was cancelled by j2's delivery-budget panic")
	}
	r.probe.release("j1")
	for got := ""; got != "j1"; {
		got = ccWaitOn(r, r.coord.resultCh, "j1's result")
	}
	ccWaitOn(r, r.done, "j1's slot release")
	if got := worker.RunningForTest(r.agent); got != 0 {
		t.Fatalf("running = %d, want 0", got)
	}
	r.stop()
	if r.coord.resultCount("j1") != 1 {
		t.Fatalf("j1 results = %d, want 1", r.coord.resultCount("j1"))
	}
	out := logs.String()
	assertPanicLogged(t, out, "delivery_budget", "job_id=j2", "attempt_id=att-j2")
	if !strings.Contains(out, "gave up delivering result after the delivery budget") {
		t.Fatalf("j2's delivery did not end as expired:\n%s", out)
	}
	select {
	case msg := <-r.slotErrs:
		t.Fatal(msg)
	default:
	}
	r.coord.check()
}

// TestConcurrency_HeartbeatPanicIsRecovered: a panic in the heartbeat goroutine (injected in its
// re-registration log call after a heartbeat 404) is recovered; the heartbeat loop goes on, the two
// running attempts are untouched, every later heartbeat reports 0 free slots, and both deliver.
func TestConcurrency_HeartbeatPanicIsRecovered(t *testing.T) {
	var armed atomic.Bool
	panicked := make(chan struct{}, 1)
	logs := hookLogs(t, func(msg string) {
		switch msg {
		case "worker not found on coordinator during heartbeat (404); re-registering":
			if armed.CompareAndSwap(true, false) {
				panic("injected heartbeat fault secret-payload tok-j1")
			}
		case "agent goroutine panicked; recovered":
			select {
			case panicked <- struct{}{}:
			default:
			}
		}
	})
	r := newCCRigWith(t, 2, false, 5*time.Millisecond, nil, ccAsg("j1", "", 30), ccAsg("j2", "", 30))
	r.start()
	ccWaitOn(r, r.probe.started, "an attempt to start")
	ccWaitOn(r, r.probe.started, "an attempt to start")
	ccWaitOn(r, r.slotWait, "a full pool")
	armed.Store(true)
	r.coord.mu.Lock()
	r.coord.hb404 = true
	r.coord.mu.Unlock()
	ccWaitOn(r, panicked, "the heartbeat panic to be recovered")
	r.coord.mu.Lock()
	n := len(r.coord.heartbeats)
	r.coord.mu.Unlock()
	deadline := time.Now().Add(ccWait)
	for {
		r.coord.mu.Lock()
		later := append([]int(nil), r.coord.heartbeats[n:]...)
		r.coord.mu.Unlock()
		if len(later) >= 2 {
			for _, v := range later {
				if v != 0 {
					t.Fatalf("heartbeats after the panic reported %v, want 0 free slots with both running", later)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat loop stopped after the panic: %d heartbeats since", len(later))
		}
		time.Sleep(time.Millisecond)
	}
	if got := worker.RunningForTest(r.agent); got != 2 {
		t.Fatalf("running = %d, want 2", got)
	}
	if r.probe.wasCancelled("j1") || r.probe.wasCancelled("j2") {
		t.Fatal("an attempt was cancelled by the heartbeat panic")
	}
	r.probe.release("j1")
	r.probe.release("j2")
	ccWaitOn(r, r.coord.resultCh, "a result")
	ccWaitOn(r, r.coord.resultCh, "a result")
	r.stop()
	assertPanicLogged(t, logs.String(), "heartbeat")
	r.coord.check()
}
