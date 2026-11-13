package faultinject_test

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// Slice 5c, part 3 (P4-08f): the fault rows of ADR-0017 Section 6.4. Every test runs on the
// in-memory and the PostgreSQL backend through a round_robin rig. The assignment loop is not
// started: each test runs one round at a time (rr.Pass), so every step is deterministic. Time
// passes with rr.Elapse (a fake clock in memory; stored timestamps moved back in PostgreSQL).

const (
	rrWindow = 5 * time.Second // the default accept window
	rrAge    = 30 * time.Second
)

func rrRig(t *testing.T, kind string, o sectest.RROptions) *sectest.RRRig {
	t.Helper()
	return sectest.NewRRRig(t, kind, o)
}

func other(a string, ids ...string) string {
	for _, id := range ids {
		if id != a {
			return id
		}
	}
	return ""
}

func mustAssignment(t *testing.T, a *coordinator.WorkAssignment, job, policy string) *coordinator.WorkAssignment {
	t.Helper()
	if a == nil {
		t.Fatalf("poll returned 204, want a claim of %s under %s", job, policy)
	}
	if a.JobID != job || a.Policy != policy {
		t.Fatalf("claim = job %s policy %s, want job %s policy %s", a.JobID, a.Policy, job, policy)
	}
	return a
}

func commit(t *testing.T, r *sectest.RRRig, a *coordinator.WorkAssignment, worker string) {
	t.Helper()
	if resp := r.Result(a.JobID, a.AttemptID, a.LeaseToken, worker, 0, "", ""); resp.Status != http.StatusOK {
		t.Fatalf("commit by %s = %s, want 200", worker, resp)
	}
}

// TestFault_AssignedWorkerNeverPolls is FS-14: the assigned worker is alive but never polls. The
// window lapses, the lapse is counted, the job moves to the next worker, and no attempt exists.
func TestFault_AssignedWorkerNeverPolls(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		r.Join("w1", "w2")
		job := r.Submit(3)
		r.Pass()
		a := r.Holder(job)
		if a == "" {
			t.Fatal("first pass reserved nothing")
		}
		b := other(a, "w1", "w2")

		// The silent worker's last poll is now older than the window, so it is no longer eligible;
		// the other worker keeps polling. The pass counts the lapse; the next one reserves to b.
		r.Elapse(rrWindow + time.Second)
		r.Pass()
		r.Keepalive(b)
		r.Pass()
		j := r.Job(job)
		if j.AssignmentLapses != 1 {
			t.Fatalf("lapses = %d, want 1 (counted outside grace)", j.AssignmentLapses)
		}
		if ev := r.LapseEvents(job); len(ev) != 1 || !ev[0] {
			t.Fatalf("lapse events %v, want one counted lapse", ev)
		}
		if h := r.Holder(job); h != b {
			t.Fatalf("after the lapse the job is reserved to %q, want the polling worker %s", h, b)
		}
		if n := len(r.Attempts(job)); n != 0 {
			t.Fatalf("attempts after a lapse = %d, want 0 (assignment reserves, it creates no attempt)", n)
		}
		if resp := r.PollResp(a); resp.Status != http.StatusNoContent {
			t.Fatalf("poll of the silent worker %s after the lapse = %s, want 204", a, resp)
		}
		got := mustAssignment(t, r.Poll(b), job, domain.DispatchPolicyRoundRobin)
		commit(t, r, got, b)
		if j := r.Job(job); j.State != domain.StateSucceeded || len(r.Attempts(job)) != 1 {
			t.Fatalf("job %s with %d attempts, want SUCCEEDED with 1", j.State, len(r.Attempts(job)))
		}
	})
}

// TestFault_AssignedWorkerDiesBeforePoll is FS-15: the reaper marks the assigned worker UNHEALTHY
// inside the window; the next pass revokes the reservation without counting a lapse and reserves
// the job to the live worker.
func TestFault_AssignedWorkerDiesBeforePoll(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		// A 3 s worker timeout is shorter than the 5 s window, so the worker times out while its
		// reservation is still live.
		r := rrRig(t, kind, sectest.RROptions{Options: sectest.Options{WorkerTimeout: 3 * time.Second}})
		r.Join("w1", "w2")
		job := r.Submit(3)
		r.Pass()
		a := r.Holder(job)
		if a == "" {
			t.Fatal("first pass reserved nothing")
		}
		b := other(a, "w1", "w2")

		r.Elapse(3500 * time.Millisecond) // inside the window, past the worker timeout
		r.Heartbeat(b)
		if resp := r.PollResp(b); resp.Status != http.StatusNoContent {
			t.Fatalf("poll of %s = %s, want 204 (the job is reserved to %s)", b, resp, a)
		}
		reaped, err := r.Svc.ReapInactiveWorkers(context.Background())
		if err != nil || len(reaped) != 1 || reaped[0] != a {
			t.Fatalf("reaped %v err %v, want exactly %s", reaped, err, a)
		}
		r.Pass()
		j := r.Job(job)
		if j.AssignmentLapses != 0 || len(r.LapseEvents(job)) != 0 {
			t.Fatalf("lapses %d, lapse events %v: an UNHEALTHY revoke must count no lapse", j.AssignmentLapses, r.LapseEvents(job))
		}
		if n := r.CountEvents(job, domain.JobEventAssignmentRevoked); n != 1 {
			t.Fatalf("JOB_ASSIGNMENT_REVOKED events = %d, want 1", n)
		}
		if h := r.Holder(job); h != b {
			t.Fatalf("reserved to %q after the revoke, want the live worker %s", h, b)
		}
		if resp := r.PollResp(a); resp.Status != http.StatusServiceUnavailable || resp.ErrCode() != "WORKER_UNHEALTHY" {
			t.Fatalf("poll of the dead worker = %s, want 503 WORKER_UNHEALTHY", resp)
		}
		got := mustAssignment(t, r.Poll(b), job, domain.DispatchPolicyRoundRobin)
		commit(t, r, got, b)
	})
}

// TestFault_PollResponseLostAfterClaim is FS-16: the claim commits and the response never reaches
// the worker. The attempt has no executor; its lease expires, the sweeper reaps it to LOST, and it
// consumes one attempt (the Q3 cost). The next attempt completes the job.
func TestFault_PollResponseLostAfterClaim(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		r.Join("w1")
		job := r.Submit(3)
		r.Pass()
		// The coordinator answers 200 and the client drops the body: the claim is committed, the
		// worker never learns the lease token.
		if resp := r.PollResp("w1"); resp.Status != http.StatusOK {
			t.Fatalf("claiming poll = %s, want 200", resp)
		}
		if atts := r.Attempts(job); len(atts) != 1 || atts[0].State != domain.AttemptRunning {
			t.Fatalf("after the lost response: %d attempts, want one RUNNING", len(atts))
		}

		r.Expire()
		r.MustSweep()
		if j := r.Job(job); j.State != domain.StateRetrying {
			t.Fatalf("after the sweep job is %s, want RETRYING", j.State)
		}
		atts := r.Attempts(job)
		if len(atts) != 1 || atts[0].State != domain.AttemptLost {
			t.Fatalf("attempt after the sweep = %+v, want one LOST attempt (one attempt consumed)", atts)
		}

		r.MakeRetryDue()
		r.Elapse(rrWindow + time.Second) // w1's last poll is older than the window: not eligible yet
		r.Pass()
		r.Keepalive("w1")
		r.Pass()
		a2 := r.Poll("w1")
		if a2 == nil || a2.AttemptNo != 2 || a2.JobID != job || a2.Policy != domain.DispatchPolicyRoundRobin {
			t.Fatalf("retry claim = %+v, want attempt 2 of %s under round_robin", a2, job)
		}
		commit(t, r, a2, "w1")
		if j := r.Job(job); j.State != domain.StateSucceeded || len(r.Attempts(job)) != 2 {
			t.Fatalf("job %s with %d attempts, want SUCCEEDED with 2 (LOST, COMPLETED)", j.State, len(r.Attempts(job)))
		}
	})
}

// TestFault_CoordinatorCrash_BetweenAssignAndPoll is FS-17: the reservation is durable across a
// coordinator restart, and a reservation that expires while the coordinator is down is revoked
// without a counted lapse (the grace is pending until the new sweeper's first tick).
func TestFault_CoordinatorCrash_BetweenAssignAndPoll(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{Options: sectest.Options{StartupGrace: 2 * time.Second}})
		r.Join("w1", "w2")

		// The reservation survives the restart: its holder claims it from the new coordinator.
		j1 := r.Submit(3)
		r.Pass()
		h1 := r.Holder(j1)
		if h1 == "" {
			t.Fatal("first pass reserved nothing")
		}
		r.Restart()
		got := mustAssignment(t, r.Poll(h1), j1, domain.DispatchPolicyRoundRobin)
		commit(t, r, got, h1)

		// A reservation that expires during the downtime.
		j2 := r.Submit(3)
		r.Pass()
		h2 := r.Holder(j2)
		if h2 == "" {
			t.Fatal("pass before the crash reserved nothing")
		}
		r.Elapse(rrWindow + 10*time.Second) // the downtime outlasts the window
		r.Restart()                         // no sweeper tick yet: grace pending
		r.Pass()
		r.Keepalive("w1", "w2")
		r.Pass()
		if j := r.Job(j2); j.AssignmentLapses != 0 {
			t.Fatalf("lapses after the restart pass = %d, want 0 (grace pending counts none)", j.AssignmentLapses)
		}
		if ev := r.LapseEvents(j2); len(ev) != 1 || ev[0] {
			t.Fatalf("lapse events %v, want exactly one uncounted lapse", ev)
		}
		if r.Holder(j2) == "" {
			t.Fatal("grace must not stop the revoke and the new reservation")
		}

		// After the sweeper's first tick and the startup grace, a lapse counts again.
		r.MustSweep()
		r.Wait(2200 * time.Millisecond)  // the startup grace (2 s) ends; PostgreSQL waits for real
		r.Elapse(rrWindow + time.Second) // the new reservation expires
		r.Pass()
		if j := r.Job(j2); j.AssignmentLapses != 1 {
			t.Fatalf("lapses after grace = %d, want 1", j.AssignmentLapses)
		}
		if ev := r.LapseEvents(j2); len(ev) != 2 || ev[0] || !ev[1] {
			t.Fatalf("lapse events %v, want [uncounted counted]", ev)
		}
	})
}

// passUntilReserved runs a pass and returns the job's holder. A pass can be skipped when another
// pass holds the database-wide pass lock (ADR-0017 note 15), which is not "nothing to reserve", so
// it is retried a bounded number of times; when the bound is hit the failure says so.
func passUntilReserved(t *testing.T, r *sectest.RRRig, job string) string {
	t.Helper()
	const attempts = 5
	for i := 0; i < attempts; i++ {
		r.Pass()
		if h := r.Holder(job); h != "" {
			return h
		}
		time.Sleep(100 * time.Millisecond) // bounded: at most attempts passes
	}
	t.Fatalf("job %s still unreserved after %d passes (skipped passes are retried; a pass that never reserves is a failure)", job, attempts)
	return ""
}

// TestFault_CancelRacesAccept is FS-18: a cancel and the poll that claims the reserved job run at
// the same time. Either the cancel wins (CANCELLED, the poll gets 204, no attempt) or the claim
// wins (the attempt exists and the cancel sees a RUNNING job: CANCEL_REQUESTED). Never both.
func TestFault_CancelRacesAccept(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		r.Join("w1", "w2", "w3")

		// The two orders, run in sequence, pin each outcome.
		jc := r.Submit(3)
		hc := passUntilReserved(t, r, jc)
		if resp := r.Cancel(jc); resp.Status != http.StatusOK {
			t.Fatalf("cancel of a reserved job = %s, want 200", resp)
		}
		if resp := r.PollResp(hc); resp.Status != http.StatusNoContent {
			t.Fatalf("poll after the cancel = %s, want 204", resp)
		}
		if j := r.Job(jc); j.State != domain.StateCancelled || len(r.Attempts(jc)) != 0 || r.Holder(jc) != "" {
			t.Fatalf("cancel first: state %s attempts %d holder %q", j.State, len(r.Attempts(jc)), r.Holder(jc))
		}
		jp := r.Submit(3)
		hp := passUntilReserved(t, r, jp)
		pa := mustAssignment(t, r.Poll(hp), jp, domain.DispatchPolicyRoundRobin)
		if resp := r.Cancel(jp); resp.Status != http.StatusOK {
			t.Fatalf("cancel of a claimed job = %s, want 200", resp)
		}
		if j := r.Job(jp); j.State != domain.StateCancelRequested || len(r.Attempts(jp)) != 1 {
			t.Fatalf("claim first: state %s attempts %d, want CANCEL_REQUESTED with 1", j.State, len(r.Attempts(jp)))
		}
		commit(t, r, pa, hp)

		// Concurrent rounds.
		var claimWins, cancelWins int
		var jobs []string
		for i := 0; i < 20; i++ {
			// Only holders poll in a round, so the idle workers refresh their freshness (a worker is
			// eligible only if it polled within the accept window; a slow run must not lose that).
			r.Keepalive("w1", "w2", "w3")
			job := r.Submit(3)
			jobs = append(jobs, job)
			h := passUntilReserved(t, r, job)
			var wg sync.WaitGroup
			var pollResp, cancelResp sectest.Resp
			start := make(chan struct{})
			wg.Add(2)
			go func() { defer wg.Done(); <-start; pollResp = r.PollResp(h) }()
			go func() { defer wg.Done(); <-start; cancelResp = r.Cancel(job) }()
			close(start)
			wg.Wait()
			if cancelResp.Status != http.StatusOK {
				t.Fatalf("round %d: cancel = %s, want 200", i, cancelResp)
			}
			j, atts := r.Job(job), r.Attempts(job)
			switch pollResp.Status {
			case http.StatusNoContent:
				cancelWins++
				if j.State != domain.StateCancelled || len(atts) != 0 {
					t.Fatalf("round %d: cancel won but job %s with %d attempts", i, j.State, len(atts))
				}
			case http.StatusOK:
				claimWins++
				if j.State != domain.StateCancelRequested || len(atts) != 1 {
					t.Fatalf("round %d: claim won but job %s with %d attempts, want CANCEL_REQUESTED with 1", i, j.State, len(atts))
				}
				id, att, tok := sectest.AssignmentOf(pollResp)
				if resp := r.Result(id, att, tok, h, 0, "", ""); resp.Status != http.StatusOK {
					t.Fatalf("round %d: commit after the claim = %s", i, resp)
				}
			default:
				t.Fatalf("round %d: poll = %s", i, pollResp)
			}
		}
		t.Logf("20 concurrent rounds on %s: claim won %d, cancel won %d", kind, claimWins, cancelWins)
		sectest.CheckInvariants(t, r.Rig, append(jobs, jc, jp))
	})
}

// TestFault_AssignmentLapseLoop_BoundedByLimit is FS-19: every chosen worker is alive and never
// claims. The job lapses exactly L times (the default 3), is then no longer reserved, and the next
// poll claims it as a round_robin_fallback with reason "lapses". The job completes.
func TestFault_AssignmentLapseLoop_BoundedByLimit(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		limit := r.Settings().MaxLapses
		r.Join("w1", "w2")
		job := r.Submit(3)

		// Every chosen worker polls, but its poll arrives after the window (the FS-22 mechanism), so
		// it never claims. Each cycle: the window passes, the pass counts the lapse, the late polls
		// get 204, and the next pass reserves again, until the limit.
		r.Pass()
		windows := 0
		for r.Job(job).AssignmentLapses < limit && windows <= limit+2 {
			if r.Holder(job) == "" {
				t.Fatalf("cycle %d: below the limit the job must be reserved", windows+1)
			}
			windows++
			r.Elapse(rrWindow + 100*time.Millisecond)
			r.Pass()
			if r.Job(job).AssignmentLapses < limit {
				r.Keepalive("w1", "w2")
				r.Pass()
			}
		}
		if windows != limit {
			t.Fatalf("job lapsed in %d windows, want exactly the limit %d", windows, limit)
		}
		j := r.Job(job)
		if j.AssignmentLapses != limit || r.Holder(job) != "" {
			t.Fatalf("after the limit: lapses %d holder %q, want %d and no reservation", j.AssignmentLapses, r.Holder(job), limit)
		}
		if ev := r.LapseEvents(job); len(ev) != limit {
			t.Fatalf("lapse events %v, want %d counted", ev, limit)
		}
		// Further passes do not reserve it again: the loop is bounded.
		for i := 0; i < 3; i++ {
			r.Elapse(rrWindow + time.Second)
			r.Pass()
			if r.Holder(job) != "" || r.Job(job).AssignmentLapses != limit {
				t.Fatalf("pass after the limit changed the job: holder %q lapses %d", r.Holder(job), r.Job(job).AssignmentLapses)
			}
		}
		got := r.Poll("w1")
		if got == nil || got.JobID != job || got.Policy != domain.DispatchPolicyRoundRobinFallback || got.FallbackReason != domain.FallbackReasonLapses {
			t.Fatalf("claim after the limit = %+v, want round_robin_fallback with reason lapses", got)
		}
		commit(t, r, got, "w1")
		if j := r.Job(job); j.State != domain.StateSucceeded || len(r.Attempts(job)) != 1 {
			t.Fatalf("job %s with %d attempts, want SUCCEEDED with 1", j.State, len(r.Attempts(job)))
		}
	})
}

// TestFault_DatabaseOutage_DuringPass is FS-20: the assignment path and the sweeper fail for longer
// than the window. The pass fails and changes nothing, the reservation expires on the repository
// clock, and after recovery the outage grace revokes it without a counted lapse.
func TestFault_DatabaseOutage_DuringPass(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		r.Join("w1", "w2")
		job := r.Submit(3)
		r.Pass()
		h := r.Holder(job)
		if h == "" {
			t.Fatal("first pass reserved nothing")
		}

		r.Fault.FailAll(true)
		before := r.Fault.Passes()
		r.Pass()
		if r.Fault.Passes() != before+1 {
			t.Fatalf("passes during the outage: %d -> %d, want one failed pass and no repeat", before, r.Fault.Passes())
		}
		if err := r.Sweep(); err == nil {
			t.Fatal("sweeper tick during the outage succeeded")
		}
		r.Elapse(rrWindow + 10*time.Second) // the reservation expires while the pass cannot run
		r.Pass()
		r.Fault.FailAll(false)
		if j := r.Job(job); r.Holder(job) != h || j.AssignmentLapses != 0 {
			t.Fatalf("a failed pass changed the job: holder %q lapses %d", r.Holder(job), j.AssignmentLapses)
		}

		r.MustSweep() // the first successful tick after a failure opens the outage window
		r.Pass()
		r.Keepalive("w1", "w2")
		r.Pass()
		if j := r.Job(job); j.AssignmentLapses != 0 {
			t.Fatalf("lapses after the outage = %d, want 0 (outage grace counts none)", j.AssignmentLapses)
		}
		if ev := r.LapseEvents(job); len(ev) != 1 || ev[0] {
			t.Fatalf("lapse events %v, want exactly one uncounted lapse", ev)
		}
		nh := r.Holder(job)
		if nh == "" {
			t.Fatal("the pass after the outage did not reserve the job again")
		}
		got := mustAssignment(t, r.Poll(nh), job, domain.DispatchPolicyRoundRobin)
		commit(t, r, got, nh)
	})
}

// TestFault_PollRoundTripBeyondWindow is FS-22: the assigned worker's poll arrives after the window.
// It gets 204 and no attempt; the next pass counts the lapse and reserves the job to the other
// worker, which completes it.
func TestFault_PollRoundTripBeyondWindow(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		r.Join("w1", "w2")
		job := r.Submit(3)
		r.Pass()
		a := r.Holder(job)
		b := other(a, "w1", "w2")

		// The pass runs at the expiry (the loop runs every interval, so the pass heartbeat is fresh
		// and the late poll starts no refresh); the poll arrives just after it.
		r.Elapse(rrWindow + 100*time.Millisecond)
		r.Pass()
		if resp := r.PollResp(a); resp.Status != http.StatusNoContent {
			t.Fatalf("late poll = %s, want 204", resp)
		}
		if n := len(r.Attempts(job)); n != 0 {
			t.Fatalf("a late poll created %d attempts", n)
		}
		r.Keepalive(b)
		r.Pass()
		if j := r.Job(job); j.AssignmentLapses != 1 {
			t.Fatalf("lapses = %d, want 1 (counted outside grace)", j.AssignmentLapses)
		}
		if h := r.Holder(job); h != b {
			t.Fatalf("reserved to %q, want the next worker %s", h, b)
		}
		got := mustAssignment(t, r.Poll(b), job, domain.DispatchPolicyRoundRobin)
		commit(t, r, got, b)
		if j := r.Job(job); j.State != domain.StateSucceeded {
			t.Fatalf("job %s, want SUCCEEDED", j.State)
		}
	})
}

// agedJobs submits n jobs and lets them age past FALLBACK_AGE with no pass having run, so the pass
// heartbeat is stale and the age arm would be open to a poll whose refresh failed.
func agedJobs(r *sectest.RRRig, n int) []string {
	var jobs []string
	for i := 0; i < n; i++ {
		jobs = append(jobs, r.Submit(3))
	}
	r.Elapse(rrAge + time.Second)
	return jobs
}

// pollAll polls each worker once, in order, and returns the claims by worker and the number of
// polls that did not claim.
func pollAll(r *sectest.RRRig, workers ...string) (claims map[string]*coordinator.WorkAssignment, others int) {
	claims = map[string]*coordinator.WorkAssignment{}
	for _, w := range workers {
		if a, _ := r.PollAssignment(w); a != nil {
			claims[w] = a
		} else {
			others++
		}
	}
	return claims, others
}

// TestFault_RestartThenPoll_NoAgeClaim is FS-23 and INV-19: the coordinator restarts with a stale
// pass heartbeat and an aged backlog. The first poll runs the refresh, the job goes to its
// round-robin holder, and nothing is claimed by age.
func TestFault_RestartThenPoll_NoAgeClaim(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		r.Join("w1", "w2")
		jobs := agedJobs(r, 1)
		r.Restart()
		claims, _ := pollAll(r, "w1", "w2")
		if len(claims) != 1 {
			t.Fatalf("%d polls claimed after the restart, want exactly 1", len(claims))
		}
		for w, c := range claims {
			if c.JobID != jobs[0] || c.Policy != domain.DispatchPolicyRoundRobin {
				t.Fatalf("%s claimed %s under %q, want %s under round_robin (never round_robin_fallback)", w, c.JobID, c.Policy, jobs[0])
			}
		}
	})
}

// TestFault_OutageRecovery_NoAgeClaim is FS-23 and INV-19: the database is down for longer than
// FALLBACK_AGE; polls fail meanwhile. The first poll after recovery refreshes, and no job is
// claimed by age.
func TestFault_OutageRecovery_NoAgeClaim(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		r.Join("w1", "w2")
		jobs := agedJobs(r, 1)
		_ = jobs
		r.Fault.FailAll(true)
		for _, w := range []string{"w1", "w2"} {
			if resp := r.PollResp(w); resp.Status == http.StatusOK || resp.Status == http.StatusNoContent {
				t.Fatalf("poll of %s during the outage = %s, want an error", w, resp)
			}
		}
		r.Fault.FailAll(false)
		claims, _ := pollAll(r, "w1", "w2")
		if len(claims) != 1 {
			t.Fatalf("%d polls claimed after recovery, want exactly 1", len(claims))
		}
		for w, c := range claims {
			if c.JobID != jobs[0] || c.Policy != domain.DispatchPolicyRoundRobin {
				t.Fatalf("%s claimed %s under %q, want round_robin", w, c.JobID, c.Policy)
			}
		}
	})
}

// TestFault_DeepBacklog_PollsNotStarved is FS-24 and INV-20, INV-21: a backlog far deeper than the
// batch. The round repeats while it makes progress and stops at the first pass that reserves
// nothing (one reservation per worker, so workers+1 passes at most); polls run between the passes
// while the round is in progress, because the mutex is released during the yield.
func TestFault_DeepBacklog_PollsNotStarved(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		s := coordinator.DefaultAssignmentSettings()
		s.BatchLimit = 2
		s.MinYield = 200 * time.Millisecond
		r := rrRig(t, kind, sectest.RROptions{Settings: &s})
		r.Join("w1", "w2", "w3", "w4")
		var jobs []string
		for i := 0; i < 12; i++ {
			jobs = append(jobs, r.Submit(3))
		}

		var done atomic.Bool
		var during atomic.Int64
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !done.Load() {
				// An unknown worker: answered by the service under s.mu, never reserved or claiming.
				if resp := r.PollResp("ghost"); resp.Status == http.StatusNotFound && !done.Load() {
					during.Add(1)
				}
			}
		}()
		time.Sleep(20 * time.Millisecond) // let the poller start before the round
		// A round that never stops (a repeat without progress) is cut after 5 s and fails below.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		roundStart := time.Now()
		passes := r.Loop.Round(ctx)
		elapsed := time.Since(roundStart)
		cancel()
		done.Store(true)
		wg.Wait()

		reserved := 0
		for _, id := range jobs {
			if r.Holder(id) != "" {
				reserved++
			}
		}
		if reserved != 4 {
			t.Fatalf("reservations after the round = %d, want 4 (one per worker)", reserved)
		}
		// Batch 2: pass 1 reserves 2, pass 2 reserves 2 (both full batches with progress, so they
		// repeat), pass 3 reads a full batch and reserves nothing, so it does not repeat.
		if passes != 3 {
			t.Fatalf("round ran %d passes, want 3 (repeat with progress, stop without)", passes)
		}
		// The yield is a real wait: two of them take at least 2 x MinYield (a repeat with no wait
		// between passes would finish in milliseconds).
		if elapsed < 2*s.MinYield {
			t.Fatalf("round took %v, less than two yields of %v", elapsed, s.MinYield)
		}
		// Two yields of 200 ms with the mutex released: a poll loop completes many calls in them.
		if during.Load() < 20 {
			t.Fatalf("only %d polls completed during the round: polls are starved between passes", during.Load())
		}
	})
}

// TestFault_PersistentPassFailure_AgeClaimRate is FS-21 and INV-23, INV-24: the pass fails on every
// attempt while polls work. After FALLBACK_AGE, in each interval exactly one poll attempts the
// refresh (and fails), that poll alone claims one aged job by age, and every other poll gets 204.
func TestFault_PersistentPassFailure_AgeClaimRate(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		workers := []string{"w1", "w2", "w3"}
		r.Join(workers...)
		r.Fault.FailPasses(true)
		jobs := agedJobs(r, 3)
		silence := time.Duration(0)
		poll := r.Settings().Interval
		claimed := map[string]bool{}

		for k := 0; k < 3; k++ {
			passesBefore := r.Fault.Passes()
			var order []string
			for _, w := range workers[k:] {
				order = append(order, w) // unclaimed workers only, the attempting one first
			}
			claims, others := pollAll(r, order...)
			if n := r.Fault.Passes() - passesBefore; n != 1 {
				t.Fatalf("interval %d: %d refresh attempts, want exactly 1", k, n)
			}
			if len(claims) != 1 || others != len(order)-1 {
				t.Fatalf("interval %d: %d claims and %d 204s among %d polls, want 1 and %d", k, len(claims), others, len(order), len(order)-1)
			}
			c := claims[order[0]]
			if c == nil {
				t.Fatalf("interval %d: the attempting poll %s did not claim", k, order[0])
			}
			if c.Policy != domain.DispatchPolicyRoundRobinFallback {
				t.Fatalf("interval %d: claim policy %q, want round_robin_fallback", k, c.Policy)
			}
			if claimed[c.JobID] || !contains(jobs, c.JobID) {
				t.Fatalf("interval %d: claim of %s repeats or is unknown", k, c.JobID)
			}
			claimed[c.JobID] = true
			if bound := rrAge + time.Duration(k+1)*poll + 3*time.Second; rrAge+silence > bound {
				t.Fatalf("interval %d: claimed %v after the silence began, bound %v", k, rrAge+silence, bound)
			}
			r.Wait(poll + 20*time.Millisecond)
			silence += poll + 20*time.Millisecond
		}
		if len(claimed) != 3 {
			t.Fatalf("claimed %d of 3 aged jobs, one per interval", len(claimed))
		}
	})
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// TestFault_OutageRecovery_RateWindow is FS-23 and INV-24: a failed refresh attempt just before
// recovery. Polls inside the rate window get 204 although a job is aged; the first poll after the
// window attempts again, succeeds, and its worker claims by reservation, not by age.
func TestFault_OutageRecovery_RateWindow(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		r.Join("w1", "w2")
		jobs := agedJobs(r, 2)
		_ = jobs
		r.Fault.FailPasses(true)
		first := r.Poll("w1") // the attempt fails: its own age arm opens and it claims one job
		if first == nil || first.Policy != domain.DispatchPolicyRoundRobinFallback || first.FallbackReason != domain.FallbackReasonAge {
			t.Fatalf("attempting poll = %+v, want an age claim", first)
		}
		r.Fault.FailPasses(false) // recovery

		before := r.Fault.Passes()
		if resp := r.PollResp("w2"); resp.Status != http.StatusNoContent {
			t.Fatalf("poll inside the rate window = %s, want 204", resp)
		}
		if r.Fault.Passes() != before {
			t.Fatalf("a poll inside the rate window started a refresh attempt")
		}
		r.Wait(r.Settings().Interval + 100*time.Millisecond)
		got := r.Poll("w2")
		if got == nil || got.Policy != domain.DispatchPolicyRoundRobin || got.FallbackReason != "" {
			t.Fatalf("first poll after the window = %+v, want a round_robin claim (the attempt succeeded and closed the age arm)", got)
		}
		if r.Fault.Passes() != before+1 {
			t.Fatalf("passes %d -> %d, want exactly one attempt after the window", before, r.Fault.Passes())
		}
	})
}
