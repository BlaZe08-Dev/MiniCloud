package faultinject_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// ADR-0020 (V2-0b): fault rows FS-25 to FS-27 for a round-robin pass that reserves up to the
// worker's real slots. Every test runs on the in-memory and the PostgreSQL backend.

func registerDeclaredWorker(t *testing.T, r *sectest.RRRig, id string, slots int) {
	t.Helper()
	resp := r.Do("POST", "/v1/workers/register", map[string]any{
		"id": id, "version": "sec", "total_slots": slots,
		"supported_tasks":       []string{"echo", "sleep", "sha256_loop"},
		"executes_concurrently": true,
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("register %s: %s", id, resp)
	}
}

func heldBy(r *sectest.RRRig, worker string, jobs []string) []string {
	var out []string
	for _, j := range jobs {
		if r.Holder(j) == worker {
			out = append(out, j)
		}
	}
	return out
}

// TestFault_MultiReservationWorkerDies_AllRevoked is FS-25: a worker holding several reservations
// dies. The reaper marks it UNHEALTHY inside the accept window, and the next pass revokes every one
// of its reservations without a counted lapse; a live worker gets the jobs and completes them.
func TestFault_MultiReservationWorkerDies_AllRevoked(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{Options: sectest.Options{WorkerTimeout: 3 * time.Second}})
		registerDeclaredWorker(t, r, "w_dead", 3)
		if resp := r.PollResp("w_dead"); resp.Status != http.StatusNoContent {
			t.Fatalf("first poll of w_dead = %s, want 204", resp)
		}
		jobs := []string{r.Submit(3), r.Submit(3), r.Submit(3)}
		r.Pass()
		if got := heldBy(r, "w_dead", jobs); len(got) != 3 {
			t.Fatalf("w_dead holds %d reservations, want 3", len(got))
		}
		registerDeclaredWorker(t, r, "w_live", 3)
		if resp := r.PollResp("w_live"); resp.Status != http.StatusNoContent {
			t.Fatalf("poll of w_live = %s, want 204 (every job is reserved to w_dead)", resp)
		}

		r.Elapse(3500 * time.Millisecond) // inside the window, past the worker timeout
		r.Heartbeat("w_live")
		if resp := r.PollResp("w_live"); resp.Status != http.StatusNoContent {
			t.Fatalf("poll of w_live = %s, want 204", resp)
		}
		reaped, err := r.Svc.ReapInactiveWorkers(context.Background())
		if err != nil || len(reaped) != 1 || reaped[0] != "w_dead" {
			t.Fatalf("reaped %v err %v, want exactly w_dead", reaped, err)
		}
		r.Pass()
		for _, j := range jobs {
			if n := r.CountEvents(j, domain.JobEventAssignmentRevoked); n != 1 {
				t.Fatalf("job %s: JOB_ASSIGNMENT_REVOKED events = %d, want 1", j, n)
			}
			if ev := r.LapseEvents(j); len(ev) != 0 || r.Job(j).AssignmentLapses != 0 {
				t.Fatalf("job %s: lapse events %v, lapses %d; an UNHEALTHY revoke counts no lapse", j, ev, r.Job(j).AssignmentLapses)
			}
		}
		if got := heldBy(r, "w_live", jobs); len(got) != 3 {
			t.Fatalf("w_live holds %d of the revoked jobs, want 3", len(got))
		}
		for range jobs {
			a := r.Poll("w_live")
			if a == nil || a.Policy != domain.DispatchPolicyRoundRobin {
				t.Fatalf("w_live poll = %+v, want a round_robin claim", a)
			}
			commit(t, r, a, "w_live")
		}
	})
}

// TestFault_ReservationToFullWorker_LapsesOnce is FS-26 (ADR-0020 Section 2.3): a worker whose slots
// are all busy locally receives reservations because its attempts went LOST in the database (zombie
// attempts, invariant 6) while its last poll is still fresh. A lease shorter than the accept window
// makes that reachable. The worker does not poll (it is full), so each reservation lapses exactly
// once, the worker becomes ineligible when its poll goes stale, and another worker gets the jobs.
func TestFault_ReservationToFullWorker_LapsesOnce(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{Options: sectest.Options{Lease: 2 * time.Second}})
		registerDeclaredWorker(t, r, "w_full", 2)
		if resp := r.PollResp("w_full"); resp.Status != http.StatusNoContent {
			t.Fatalf("first poll of w_full = %s, want 204", resp)
		}
		first := []string{r.Submit(3), r.Submit(3)}
		r.Pass()
		for range first {
			if a := r.Poll("w_full"); a == nil {
				t.Fatal("w_full could not claim its reservation")
			}
		}

		// Both leases expire while w_full keeps executing (it renews nothing in this test); the
		// sweeper declares the attempts LOST. w_full's last poll (its claims) is still fresh.
		r.Elapse(2500 * time.Millisecond)
		r.MustSweep()
		late := r.Submit(3)
		all := append(append([]string{}, first...), late)
		r.Pass()
		held := heldBy(r, "w_full", all)
		if len(held) == 0 || len(held) > 2 {
			t.Fatalf("w_full holds %d reservations with 0 active attempts in the database, want 1 or 2", len(held))
		}

		// w_full never polls (locally full). After the window its reservations lapse once each and
		// its poll is stale, so the pass reserves nothing more to it.
		r.Elapse(r.Settings().AcceptWindow + time.Second)
		r.Heartbeat("w_full")
		r.Pass()
		for _, j := range held {
			if ev := r.LapseEvents(j); len(ev) != 1 || !ev[0] {
				t.Fatalf("job %s: lapse events %v, want exactly one counted lapse", j, ev)
			}
			if h := r.Holder(j); h != "" {
				t.Fatalf("job %s reserved to %q after the lapse; w_full's poll is stale and no other worker exists", j, h)
			}
		}

		registerDeclaredWorker(t, r, "w_other", 3)
		if resp := r.PollResp("w_other"); resp.Status != http.StatusNoContent {
			t.Fatalf("first poll of w_other = %s, want 204", resp)
		}
		r.Pass()
		for _, j := range held {
			if h := r.Holder(j); h != "w_other" {
				t.Fatalf("job %s reserved to %q, want w_other", j, h)
			}
			if ev := r.LapseEvents(j); len(ev) != 1 {
				t.Fatalf("job %s: lapse events %v after the reassignment, want still one", j, ev)
			}
		}
	})
}

// TestFault_UndeclaredWorker_NoExtraReservations is FS-27 (mixed versions, ADR-0020 Section 2.6): a
// worker with total_slots 4 that does not send executes_concurrently (the v1.0.0 and V2-0 workers)
// never holds more than one reservation, is reserved nothing while it runs an attempt, even when it
// polls while running (as a V2-0 worker does), and no job lapses because of an extra reservation.
func TestFault_UndeclaredWorker_NoExtraReservations(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := rrRig(t, kind, sectest.RROptions{})
		if resp := r.Register("w_old", 4); resp.Status != http.StatusOK {
			t.Fatalf("register w_old: %s", resp)
		}
		if resp := r.PollResp("w_old"); resp.Status != http.StatusNoContent {
			t.Fatalf("first poll of w_old = %s, want 204", resp)
		}
		jobs := []string{r.Submit(3), r.Submit(3), r.Submit(3), r.Submit(3)}
		for i := range jobs {
			r.Pass()
			if got := heldBy(r, "w_old", jobs); len(got) != 1 {
				t.Fatalf("round %d: w_old holds %d reservations, want 1", i, len(got))
			}
			a := r.Poll("w_old")
			if a == nil || a.Policy != domain.DispatchPolicyRoundRobin {
				t.Fatalf("round %d: w_old poll = %+v, want a round_robin claim", i, a)
			}
			r.Pass()
			if got := heldBy(r, "w_old", jobs); len(got) != 0 {
				t.Fatalf("round %d: w_old holds %d reservations while it runs an attempt, want 0", i, len(got))
			}
			if resp := r.PollResp("w_old"); resp.Status != http.StatusNoContent {
				t.Fatalf("round %d: poll while running = %s, want 204", i, resp)
			}
			commit(t, r, a, "w_old")
		}
		for _, j := range jobs {
			if ev := r.LapseEvents(j); len(ev) != 0 {
				t.Fatalf("job %s lapsed %v times; no extra reservation may be made to a one-slot worker", j, ev)
			}
			if n := r.CountEvents(j, domain.JobEventAssigned); n != 1 {
				t.Fatalf("job %s: JOB_ASSIGNED events = %d, want 1", j, n)
			}
			if r.Job(j).State != domain.StateSucceeded {
				t.Fatalf("job %s state %s, want SUCCEEDED", j, r.Job(j).State)
			}
		}
	})
}
