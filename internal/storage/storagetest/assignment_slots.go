package storagetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// Shared ADR-0020 cases: the round-robin pass and the fallback claim use the worker's real slots.
// A worker that declares executes_concurrently gets total_slots assignable slots; one that does not
// (the v1.0.0 and V2-0 workers) gets one (INV-10r).

// AssignAddDeclaredWorker registers a HEALTHY worker with a fresh heartbeat that declares
// executes_concurrently, as the V2-0b worker does.
func AssignAddDeclaredWorker(t *testing.T, h AssignmentHarness, id string, slots int, tasks ...string) {
	t.Helper()
	err := h.Repo.UpsertWorker(context.Background(), &domain.Worker{
		ID:                   id,
		Version:              "v0.1.0",
		TotalSlots:           slots,
		Status:               domain.WorkerStatusHealthy,
		SupportedTasks:       tasks,
		LastHeartbeatAt:      h.Now(t),
		ExecutesConcurrently: true,
	})
	if err != nil {
		t.Fatalf("UpsertWorker(%s): %v", id, err)
	}
}

// assignHeldBy counts the given jobs whose reservation names workerID.
func assignHeldBy(t *testing.T, h AssignmentHarness, workerID string, jobIDs []string) int {
	t.Helper()
	n := 0
	for _, id := range jobIDs {
		if w := AssignJob(t, h, id).AssignedWorkerID; w != nil && *w == workerID {
			n++
		}
	}
	return n
}

func assignSubmitN(t *testing.T, h AssignmentHarness, prefix string, n int, taskType string) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s_%02d", prefix, i)
		AssignSubmit(t, h, ids[i], taskType)
	}
	return ids
}

// assignRunning gives workerID one RUNNING attempt through the fifo_pull claim (the oldest QUEUED
// job), as an earlier claim would.
func assignRunning(t *testing.T, h AssignmentHarness, workerID string) {
	t.Helper()
	if _, _, err := h.Repo.DispatchNextJob(context.Background(), workerID); err != nil {
		t.Fatalf("DispatchNextJob(%s): %v", workerID, err)
	}
}

// RunAssignReservationsUpToDeclaredSlotsConformance: ADR-0020 Section 2.1, a declared 3-slot idle
// worker and 5 jobs; one pass reserves 3 to it, a second pass reserves none.
func RunAssignReservationsUpToDeclaredSlotsConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	AssignAddDeclaredWorker(t, h, "w_1", 3, "echo")
	jobs := assignSubmitN(t, h, "job", 5, "echo")
	AssignPoll(t, h, "w_1")

	if res := AssignRunPass(t, h); res.Reserved != 3 {
		t.Fatalf("first pass reserved %d to a declared 3-slot idle worker, want 3", res.Reserved)
	}
	if res := AssignRunPass(t, h); res.Reserved != 0 {
		t.Fatalf("second pass reserved %d more to a worker whose 3 slots are reserved", res.Reserved)
	}
	if got := assignHeldBy(t, h, "w_1", jobs); got != 3 {
		t.Fatalf("w_1 holds %d reservations, want 3", got)
	}
	// FIFO within the type: the three oldest jobs are the reserved ones.
	for i, id := range jobs {
		held := AssignJob(t, h, id).AssignedWorkerID != nil
		if held != (i < 3) {
			t.Fatalf("job %s reserved=%v; want the three oldest reserved", id, held)
		}
	}
}

// RunAssignActiveAttemptsCountAgainstSlotsConformance: ADR-0020 Section 2.1, PENDING and RUNNING
// attempts count against the declared slots.
func RunAssignActiveAttemptsCountAgainstSlotsConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	t.Run("one running of three slots leaves two", func(t *testing.T) {
		h := newHarness(t)
		AssignAddDeclaredWorker(t, h, "w_1", 3, "echo")
		AssignSubmit(t, h, "job_run", "echo")
		AssignPoll(t, h, "w_1")
		assignRunning(t, h, "w_1")
		jobs := assignSubmitN(t, h, "job", 4, "echo")
		AssignPoll(t, h, "w_1")
		if res := AssignRunPass(t, h); res.Reserved != 2 {
			t.Fatalf("reserved %d to a 3-slot worker with 1 RUNNING attempt, want 2", res.Reserved)
		}
		if got := assignHeldBy(t, h, "w_1", jobs); got != 2 {
			t.Fatalf("w_1 holds %d reservations, want 2", got)
		}
	})
	t.Run("three running of three slots leaves none", func(t *testing.T) {
		h := newHarness(t)
		AssignAddDeclaredWorker(t, h, "w_1", 3, "echo")
		assignSubmitN(t, h, "job_run", 3, "echo")
		AssignPoll(t, h, "w_1")
		for i := 0; i < 3; i++ {
			assignRunning(t, h, "w_1")
		}
		assignSubmitN(t, h, "job", 2, "echo")
		AssignPoll(t, h, "w_1")
		if res := AssignRunPass(t, h); res.Reserved != 0 {
			t.Fatalf("reserved %d to a 3-slot worker with 3 RUNNING attempts, want 0", res.Reserved)
		}
	})
}

// RunAssignReservationCapPerWorkerConformance: ADR-0020 Section 2.5, a worker that declares 1024
// slots and polled once holds at most domain.MaxReservationsPerWorker live reservations, whatever
// the backlog.
func RunAssignReservationCapPerWorkerConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	AssignAddDeclaredWorker(t, h, "w_big", 1024, "echo")
	jobs := assignSubmitN(t, h, "job", 50, "echo")
	AssignPoll(t, h, "w_big")

	if res := AssignRunPass(t, h); res.Reserved != domain.MaxReservationsPerWorker {
		t.Fatalf("pass reserved %d to a 1024-slot worker, want the cap %d", res.Reserved, domain.MaxReservationsPerWorker)
	}
	if res := AssignRunPass(t, h); res.Reserved != 0 {
		t.Fatalf("second pass reserved %d more while %d are live", res.Reserved, domain.MaxReservationsPerWorker)
	}
	if got := assignHeldBy(t, h, "w_big", jobs); got != domain.MaxReservationsPerWorker {
		t.Fatalf("w_big holds %d reservations, want %d", got, domain.MaxReservationsPerWorker)
	}
}

// RunAssignExpiredReservationDoesNotHoldSlotConformance: ADR-0020 Section 2.1, only live
// reservations count. With batch limit 1, the revoke step clears one of two expired reservations;
// the other stays expired and unrevoked through the pass, and must not hold the worker's slot.
func RunAssignExpiredReservationDoesNotHoldSlotConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	AssignAddDeclaredWorker(t, h, "w_1", 1, "echo")
	AssignSubmit(t, h, "job_e1", "echo")
	AssignSubmit(t, h, "job_e2", "echo")
	h.SetReservation(t, "job_e1", "w_1", -AssignTestAcceptWindow/5)
	h.SetReservation(t, "job_e2", "w_1", -AssignTestAcceptWindow/5)
	AssignPoll(t, h, "w_1")

	p := AssignTestParams()
	p.BatchLimit = 1
	res, err := h.assignRepo(t).AssignQueuedJobs(context.Background(), p)
	if err != nil {
		t.Fatalf("AssignQueuedJobs: %v", err)
	}
	if res.Lapsed != 1 {
		t.Fatalf("lapsed %d with batch limit 1, want 1 (one expired reservation must stay unrevoked)", res.Lapsed)
	}
	if res.Reserved != 1 {
		t.Fatalf("reserved %d; the unrevoked expired reservation held the worker's only slot", res.Reserved)
	}
	if w := AssignJob(t, h, "job_e1").AssignedWorkerID; w == nil || *w != "w_1" {
		t.Fatalf("job_e1 reserved to %v after its lapse, want w_1", w)
	}
}

// RunAssignUndeclaredFourSlotWorkerKeepsOneSlotConformance: ADR-0020 Section 2.6 (mixed versions),
// a worker with total_slots 4 that does not declare executes_concurrently gets one reservation and
// none while it has a RUNNING attempt.
func RunAssignUndeclaredFourSlotWorkerKeepsOneSlotConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddWorker(t, h, "w_old", 4, "echo")
	jobs := assignSubmitN(t, h, "job", 3, "echo")
	AssignPoll(t, h, "w_old")

	if res := AssignRunPass(t, h); res.Reserved != 1 {
		t.Fatalf("first pass reserved %d to an undeclared 4-slot worker, want 1", res.Reserved)
	}
	if res := AssignRunPass(t, h); res.Reserved != 0 {
		t.Fatalf("second pass reserved %d more to an undeclared worker holding one", res.Reserved)
	}
	if _, err := h.assignRepo(t).ClaimForPoll(ctx, "w_old", AssignTestClaimOpts(false)); err != nil {
		t.Fatalf("claim of its own reservation: %v", err)
	}
	AssignPoll(t, h, "w_old")
	if res := AssignRunPass(t, h); res.Reserved != 0 {
		t.Fatalf("reserved %d to an undeclared worker with a RUNNING attempt", res.Reserved)
	}
	if got := assignHeldBy(t, h, "w_old", jobs); got != 0 {
		t.Fatalf("w_old holds %d reservations while running, want 0", got)
	}
}

// RunAssignRotationWithMultiSlotWorkersConformance: ADR-0020 Section 2.2, the second example. w_a
// declares 4 slots, w_b does not declare (one slot), both idle; six jobs. Decisions: w_a, w_b, w_a,
// w_a, w_a, then none. The JOB_ASSIGNED cursor_before values show one turn per decision.
func RunAssignRotationWithMultiSlotWorkersConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	AssignAddDeclaredWorker(t, h, "w_a", 4, "echo")
	AssignAddWorker(t, h, "w_b", 4, "echo")
	jobs := assignSubmitN(t, h, "job", 6, "echo")
	AssignPoll(t, h, "w_a")
	AssignPoll(t, h, "w_b")

	if res := AssignRunPass(t, h); res.Reserved != 5 {
		t.Fatalf("pass reserved %d, want 5 (4 to w_a, 1 to w_b)", res.Reserved)
	}
	want := []struct{ holder, cursorBefore string }{
		{"w_a", ""}, {"w_b", "w_a"}, {"w_a", "w_b"}, {"w_a", "w_a"}, {"w_a", "w_a"},
	}
	for i, w := range want {
		got := AssignJob(t, h, jobs[i]).AssignedWorkerID
		if got == nil || *got != w.holder {
			t.Fatalf("decision %d (%s): reserved to %v, want %s", i+1, jobs[i], got, w.holder)
		}
		ev := assignedEvent(t, h, jobs[i])
		if cb, _ := ev.Details["cursor_before"].(string); cb != w.cursorBefore {
			t.Fatalf("decision %d (%s): cursor_before %q, want %q", i+1, jobs[i], cb, w.cursorBefore)
		}
	}
	if got := AssignJob(t, h, jobs[5]).AssignedWorkerID; got != nil {
		t.Fatalf("job %s reserved to %s; both workers are full", jobs[5], *got)
	}
}

// RunClaimFallbackRespectsReservedSlotsConformance: ADR-0020 Section 2.4, a fallback claim never
// makes active + live reservations exceed the assignable slots. A declared 2-slot worker with one
// RUNNING attempt holds a live reservation it cannot take by its own arm (it re-registered without
// that task type); a lapse-arm claim must return ErrWorkerNoCapacity. Control: without that
// reservation the same claim succeeds.
func RunClaimFallbackRespectsReservedSlotsConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	for _, withReservation := range []bool{true, false} {
		name := "control: no reservation, fallback claims"
		if withReservation {
			name = "live reservation fills the second slot"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			AssignAddDeclaredWorker(t, h, "w_1", 2, "echo", "sleep")
			AssignSubmit(t, h, "job_run", "echo")
			AssignPoll(t, h, "w_1")
			assignRunning(t, h, "w_1")
			if withReservation {
				AssignSubmit(t, h, "job_res", "sleep")
				h.SetReservation(t, "job_res", "w_1", AssignTestAcceptWindow)
			}
			AssignAddDeclaredWorker(t, h, "w_1", 2, "echo")
			AssignSubmit(t, h, "job_fb", "echo")
			h.SetLapses(t, "job_fb", AssignTestMaxLapses)
			AssignPoll(t, h, "w_1")

			c, err := h.assignRepo(t).ClaimForPoll(ctx, "w_1", AssignTestClaimOpts(false))
			if withReservation {
				if !errors.Is(err, domain.ErrWorkerNoCapacity) {
					t.Fatalf("fallback claim with active 1 + reserved 1 of 2 slots: claim=%v err=%v, want ErrWorkerNoCapacity", c, err)
				}
				return
			}
			if err != nil || c.Job.ID != "job_fb" || c.Policy != domain.DispatchPolicyRoundRobinFallback {
				t.Fatalf("control: claim=%v err=%v, want job_fb by %s", c, err, domain.DispatchPolicyRoundRobinFallback)
			}
		})
	}
}

// RunAssignReRegisterLowerSlotsNoNewReservationsConformance: ADR-0020 FS-28, a worker that
// re-registers with fewer slots or without the declaration while it holds live reservations gets
// no new reservation while active + reserved >= its new assignable slots. Its own reservations
// stay claimable.
func RunAssignReRegisterLowerSlotsNoNewReservationsConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddDeclaredWorker(t, h, "w_1", 3, "echo")
	assignSubmitN(t, h, "job", 5, "echo")
	AssignPoll(t, h, "w_1")
	if res := AssignRunPass(t, h); res.Reserved != 3 {
		t.Fatalf("first pass reserved %d, want 3", res.Reserved)
	}

	AssignAddDeclaredWorker(t, h, "w_1", 2, "echo")
	AssignPoll(t, h, "w_1")
	if res := AssignRunPass(t, h); res.Reserved != 0 {
		t.Fatalf("reserved %d after re-registering with 2 slots while holding 3", res.Reserved)
	}

	AssignAddWorker(t, h, "w_1", 3, "echo") // the declaration dropped: one assignable slot
	AssignPoll(t, h, "w_1")
	if res := AssignRunPass(t, h); res.Reserved != 0 {
		t.Fatalf("reserved %d after re-registering without the declaration", res.Reserved)
	}
	if _, err := h.assignRepo(t).ClaimForPoll(ctx, "w_1", AssignTestClaimOpts(false)); err != nil {
		t.Fatalf("own reservation not claimable after re-registration: %v", err)
	}
	AssignPoll(t, h, "w_1")
	if res := AssignRunPass(t, h); res.Reserved != 0 {
		t.Fatalf("reserved %d with active 1 + reserved 2 of 1 assignable slot", res.Reserved)
	}
}

// RunWorkerExecutesConcurrentlyRoundTripConformance: ADR-0020 Section 2.6, the declaration is
// stored, read back by GetWorker and ListWorkers, and reset to false by a re-registration without
// it (the latest binary under an ID wins).
func RunWorkerExecutesConcurrentlyRoundTripConformance(t *testing.T, repo storage.Repository) {
	ctx := context.Background()
	w := &domain.Worker{ID: "w_rt", Version: "v0.1.0", TotalSlots: 4, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, ExecutesConcurrently: true}
	if err := repo.UpsertWorker(ctx, w); err != nil {
		t.Fatalf("UpsertWorker: %v", err)
	}
	check := func(step string, want bool) {
		t.Helper()
		got, err := repo.GetWorker(ctx, "w_rt")
		if err != nil {
			t.Fatalf("%s: GetWorker: %v", step, err)
		}
		if got.ExecutesConcurrently != want {
			t.Fatalf("%s: GetWorker executes_concurrently=%v, want %v", step, got.ExecutesConcurrently, want)
		}
		all, err := repo.ListWorkers(ctx)
		if err != nil {
			t.Fatalf("%s: ListWorkers: %v", step, err)
		}
		found := false
		for _, lw := range all {
			if lw.ID == "w_rt" {
				found = true
				if lw.ExecutesConcurrently != want {
					t.Fatalf("%s: ListWorkers executes_concurrently=%v, want %v", step, lw.ExecutesConcurrently, want)
				}
			}
		}
		if !found {
			t.Fatalf("%s: ListWorkers does not list w_rt", step)
		}
	}
	check("declared", true)
	w.ExecutesConcurrently = false
	if err := repo.UpsertWorker(ctx, w); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	check("re-registered without the declaration", false)
}
