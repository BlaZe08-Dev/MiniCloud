package sectest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// ErrInjected is what a FaultRepo returns while it is told to fail.
var ErrInjected = errors.New("sectest: injected repository failure")

// FaultRepo wraps a repository and forwards the assignment surface (storage.AssignmentRepository
// and storage.AssignmentPassStaleness) so a service built on it still runs round_robin. It counts
// passes and polls and can make passes, or every assignment call, fail. It is the in-process
// stand-in for a stalled pass (FS-21) and for an outage of the assignment path (FS-20, FS-23).
type FaultRepo struct {
	storage.Repository
	ar storage.AssignmentRepository
	st storage.AssignmentPassStaleness

	mu         sync.Mutex
	failPasses bool
	failAll    bool
	passes     int
	polls      int
}

// NewFaultRepo wraps inner, which must implement the assignment interfaces.
func NewFaultRepo(t testing.TB, inner storage.Repository) *FaultRepo {
	t.Helper()
	ar, ok := inner.(storage.AssignmentRepository)
	st, ok2 := inner.(storage.AssignmentPassStaleness)
	if !ok || !ok2 {
		t.Fatalf("%T does not implement the assignment interfaces", inner)
	}
	return &FaultRepo{Repository: inner, ar: ar, st: st}
}

// FailPasses makes every AssignQueuedJobs fail while v is true; polls and claims still work.
func (f *FaultRepo) FailPasses(v bool) { f.mu.Lock(); f.failPasses = v; f.mu.Unlock() }

// FailAll makes every assignment-surface call fail while v is true.
func (f *FaultRepo) FailAll(v bool) { f.mu.Lock(); f.failAll = v; f.mu.Unlock() }

// Passes is the number of AssignQueuedJobs calls so far, failed ones included.
func (f *FaultRepo) Passes() int { f.mu.Lock(); defer f.mu.Unlock(); return f.passes }

// Polls is the number of RecordPoll calls so far.
func (f *FaultRepo) Polls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.polls }

// Now and SweepExpiredLeases fail while FailAll is set, so a sweeper tick fails in an outage as it
// does when the database is unreachable (the failed tick marks the grace pending, note 16).
func (f *FaultRepo) Now(ctx context.Context) (time.Time, error) {
	if f.down() {
		return time.Time{}, ErrInjected
	}
	return f.Repository.Now(ctx)
}

func (f *FaultRepo) SweepExpiredLeases(ctx context.Context, p domain.SweepParams) (*domain.SweepResult, error) {
	if f.down() {
		return nil, ErrInjected
	}
	return f.Repository.SweepExpiredLeases(ctx, p)
}

func (f *FaultRepo) down() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.failAll }

func (f *FaultRepo) RecordPoll(ctx context.Context, workerID string) error {
	f.mu.Lock()
	f.polls++
	fail := f.failAll
	f.mu.Unlock()
	if fail {
		return ErrInjected
	}
	return f.ar.RecordPoll(ctx, workerID)
}

func (f *FaultRepo) AssignQueuedJobs(ctx context.Context, p storage.AssignParams) (storage.AssignResult, error) {
	f.mu.Lock()
	f.passes++
	fail := f.failAll || f.failPasses
	f.mu.Unlock()
	if fail {
		return storage.AssignResult{}, ErrInjected
	}
	return f.ar.AssignQueuedJobs(ctx, p)
}

func (f *FaultRepo) ClaimForPoll(ctx context.Context, workerID string, o storage.ClaimOptions) (*storage.AssignedClaim, error) {
	if f.down() {
		return nil, ErrInjected
	}
	return f.ar.ClaimForPoll(ctx, workerID, o)
}

func (f *FaultRepo) AssignmentPassState(ctx context.Context) (time.Time, error) {
	if f.down() {
		return time.Time{}, ErrInjected
	}
	return f.ar.AssignmentPassState(ctx)
}

func (f *FaultRepo) AssignmentPassStale(ctx context.Context, threshold time.Duration) (bool, error) {
	if f.down() {
		return false, ErrInjected
	}
	return f.st.AssignmentPassStale(ctx, threshold)
}

// RROptions configures an RRRig. A zero Settings means the ADR-0017 defaults.
type RROptions struct {
	Options
	Settings *coordinator.AssignmentSettings
}

// RRRig is a Rig that runs the round_robin policy. The assignment loop is not started: tests run
// one round at a time with Pass, so every step is deterministic.
type RRRig struct {
	*Rig
	Fault *FaultRepo
	Loop  *coordinator.AssignmentLoop
	opts  RROptions
}

// NewRRRig builds a round_robin rig and runs one successful sweeper tick, which ends the startup
// grace-pending state of ADR-0017 note 16 (a coordinator that has been up for a while).
func NewRRRig(t *testing.T, kind string, o RROptions) *RRRig {
	t.Helper()
	rr := &RRRig{opts: o}
	o.WrapRepo = func(inner storage.Repository) storage.Repository {
		rr.Fault = NewFaultRepo(t, inner)
		return rr.Fault
	}
	rr.Rig = NewRig(t, kind, o.Options)
	rr.configure()
	rr.MustSweep()
	return rr
}

func (r *RRRig) configure() {
	r.T.Helper()
	settings := coordinator.DefaultAssignmentSettings()
	if r.opts.Settings != nil {
		settings = *r.opts.Settings
	}
	if err := r.Svc.SetAssignmentSettings(settings); err != nil {
		r.T.Fatalf("SetAssignmentSettings: %v", err)
	}
	if err := r.Svc.SetDispatchPolicy(domain.DispatchPolicyRoundRobin); err != nil {
		r.T.Fatalf("SetDispatchPolicy: %v", err)
	}
	r.Loop = r.Svc.NewAssignmentLoop()
}

// Pass runs one assignment round (a pass and its repeats) and returns the number of passes.
func (r *RRRig) Pass() int { return r.Loop.Round(context.Background()) }

// Settings returns the assignment settings in force.
func (r *RRRig) Settings() coordinator.AssignmentSettings { return r.Svc.AssignmentSettings() }

// Restart replaces the coordinator process: a new service, sweeper, loop and HTTP server over the
// same stored state. A PostgreSQL rig also gets a new repository on a new pool, as a real restart
// does. The new sweeper has not ticked, so the pass is grace-pending until MustSweep succeeds.
func (r *RRRig) Restart() {
	r.T.Helper()
	r.Srv.Close()
	inner := r.Fault.Repository
	if r.Kind == KindPostgres {
		pool, err := r.PG.OpenPool(context.Background(), r.PG.SchemaURL, 25)
		if err != nil {
			r.T.Fatalf("reopen pool: %v", err)
		}
		r.T.Cleanup(pool.Close)
		inner = postgres.NewRepository(pool, postgres.WithLeaseDuration(r.Lease))
	}
	r.Fault = NewFaultRepo(r.T, inner)
	r.Repo = r.Fault
	if r.Kind == KindPostgres {
		r.Svc = coordinator.NewPostgresService(r.Repo, scheduler.NewRoundRobinScheduler(), r.Reg, r.opts.workerTimeout())
	} else {
		r.Svc = coordinator.NewService(r.Repo, scheduler.NewRoundRobinScheduler(), r.Reg, r.opts.workerTimeout())
		r.Svc.SetClock(r.Clock)
	}
	r.Svc.SetLeaseDuration(r.Lease)
	r.Svc.SetGracePeriods(r.opts.StartupGrace, r.opts.outageWindow(r.Lease))
	r.Svc.SetSweepRandom(func() float64 { return 0.5 })
	r.Sweeper = r.Svc.NewLeaseSweeper(r.Lease)
	r.Srv = httptest.NewServer(api.NewServer("127.0.0.1:0", r.Svc).Handler())
	r.T.Cleanup(r.Srv.Close)
	r.configure()
}

func (o Options) workerTimeout() time.Duration {
	if o.WorkerTimeout == 0 {
		return time.Hour
	}
	return o.WorkerTimeout
}

func (o Options) outageWindow(lease time.Duration) time.Duration {
	if o.OutageWindow == 0 {
		return lease
	}
	return o.OutageWindow
}

// Elapse lets d pass. Memory: the fake clock advances. PostgreSQL: every stored timestamp moves
// back by d, which is the same as moving the database clock forward (the clock itself cannot move).
func (r *RRRig) Elapse(d time.Duration) {
	r.T.Helper()
	if r.Clock != nil {
		r.Clock.Advance(d)
		return
	}
	for _, q := range []string{
		`UPDATE jobs SET created_at = created_at - $1::interval, updated_at = updated_at - $1::interval,
		        not_before = not_before - $1::interval, cancel_requested_at = cancel_requested_at - $1::interval,
		        assigned_at = assigned_at - $1::interval, assignment_expires_at = assignment_expires_at - $1::interval`,
		`UPDATE workers SET registered_at = registered_at - $1::interval, last_heartbeat_at = last_heartbeat_at - $1::interval,
		        last_poll_at = last_poll_at - $1::interval`,
		`UPDATE attempts SET started_at = started_at - $1::interval, finished_at = finished_at - $1::interval,
		        lease_expires_at = lease_expires_at - $1::interval`,
		`UPDATE results SET committed_at = committed_at - $1::interval`,
		`UPDATE job_events SET created_at = created_at - $1::interval`,
		`UPDATE assignment_pass_state SET last_completed_at = last_completed_at - $1::interval`,
		`UPDATE assignment_cursor SET updated_at = updated_at - $1::interval`,
	} {
		r.PG.Exec(r.T, q, fmt.Sprintf("%f seconds", d.Seconds()))
	}
}

// PollResp posts a poll and returns the raw response.
func (r *RRRig) PollResp(workerID string) Resp {
	r.T.Helper()
	return r.Do("POST", "/v1/workers/"+workerID+"/poll", "")
}

// Heartbeat sends a worker heartbeat and fails the test on a non-200.
func (r *RRRig) Heartbeat(workerID string) {
	r.T.Helper()
	if resp := r.Do("POST", "/v1/workers/"+workerID+"/heartbeat", map[string]int{"available_slots": 1}); resp.Status != http.StatusOK {
		r.T.Fatalf("heartbeat %s: %s", workerID, resp)
	}
}

// Join registers a worker and polls once with nothing queued, so the worker is eligible: HEALTHY,
// fresh heartbeat and fresh poll.
func (r *RRRig) Join(ids ...string) {
	r.T.Helper()
	for _, id := range ids {
		if resp := r.Register(id, 1); resp.Status != http.StatusOK {
			r.T.Fatalf("register %s: %s", id, resp)
		}
		if resp := r.PollResp(id); resp.Status != http.StatusNoContent {
			r.T.Fatalf("first poll of %s = %s, want 204", id, resp)
		}
	}
}

// Holder returns the worker the job is reserved to, or "".
func (r *RRRig) Holder(jobID string) string {
	j := r.Job(jobID)
	if j.AssignedWorkerID == nil {
		return ""
	}
	return *j.AssignedWorkerID
}

// LapseEvents returns the job's JOB_ASSIGNMENT_LAPSED events as their `counted` flags, in order.
func (r *RRRig) LapseEvents(jobID string) []bool {
	r.T.Helper()
	var out []bool
	for _, e := range r.Events(jobID) {
		if e.EventType == domain.JobEventAssignmentLapsed {
			c, _ := e.Details["counted"].(bool)
			out = append(out, c)
		}
	}
	return out
}

// Wait lets d pass for the coordinator's process clock, which the poll-time refresh rate limit
// reads (N6): the fake clock advances in memory; a PostgreSQL rig sleeps for real, and the database
// clock moves with it.
func (r *RRRig) Wait(d time.Duration) {
	r.T.Helper()
	if r.Clock != nil {
		r.Clock.Advance(d)
		return
	}
	time.Sleep(d) // bounded: the rate window is one interval (1 s by default)
}

// PollAssignment posts a poll and returns the claim, or nil on any other status, with the raw
// response.
func (r *RRRig) PollAssignment(workerID string) (*coordinator.WorkAssignment, Resp) {
	r.T.Helper()
	resp := r.PollResp(workerID)
	if resp.Status != http.StatusOK {
		return nil, resp
	}
	var env struct {
		Assignment coordinator.WorkAssignment `json:"assignment"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		r.T.Fatalf("poll decode: %v", err)
	}
	return &env.Assignment, resp
}

// Keepalive polls once for each worker and requires 204: the worker is idle and has no reservation,
// so the poll only records its freshness. An eligible worker must have polled within the accept
// window, so tests call it after time has passed and before the pass that should reserve to it.
func (r *RRRig) Keepalive(ids ...string) {
	r.T.Helper()
	for _, id := range ids {
		if resp := r.PollResp(id); resp.Status != http.StatusNoContent {
			r.T.Fatalf("keepalive poll of %s = %s, want 204", id, resp)
		}
	}
}
