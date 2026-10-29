package sectest

import (
	"context"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// CheckInvariants reads the stored state of the given jobs and fails the test on any violation of
// the documented guarantees: attempt numbers 1..n without gaps, bounded attempts, at most one
// active attempt per job, no active attempt on a terminal job, a running job has exactly its
// current attempt active, at most one result row, a SUCCEEDED job has one, and no worker holds
// more RUNNING attempts than it has slots.
func CheckInvariants(t testing.TB, r *Rig, jobIDs []string) {
	t.Helper()
	ctx := context.Background()
	running := map[string]int{}
	for _, id := range jobIDs {
		j, err := r.Repo.GetJob(ctx, id)
		if err != nil {
			t.Errorf("invariant: get job %s: %v", id, err)
			continue
		}
		atts, err := r.Repo.ListAttemptsByJob(ctx, id)
		if err != nil {
			t.Errorf("invariant: attempts of %s: %v", id, err)
			continue
		}
		active := 0
		for i, a := range atts {
			if a.AttemptNo != i+1 {
				t.Errorf("invariant: job %s attempt #%d has attempt_no %d (want %d, no gaps or duplicates)", id, i, a.AttemptNo, i+1)
			}
			if a.State == domain.AttemptRunning || a.State == domain.AttemptPending {
				active++
				running[a.WorkerID]++
			}
		}
		if len(atts) > j.MaxAttempts {
			t.Errorf("invariant: job %s has %d attempts, max_attempts %d", id, len(atts), j.MaxAttempts)
		}
		if active > 1 {
			t.Errorf("invariant: job %s has %d active attempts", id, active)
		}
		if j.State.IsTerminal() && active != 0 {
			t.Errorf("invariant: terminal job %s (%s) has %d active attempts", id, j.State, active)
		}
		if (j.State == domain.StateRunning || j.State == domain.StateCancelRequested) && active != 1 {
			t.Errorf("invariant: %s job %s has %d active attempts, want 1", j.State, id, active)
		}
		n := r.ResultRows(id)
		if n > 1 {
			t.Errorf("invariant: job %s has %d result rows", id, n)
		}
		if j.State == domain.StateSucceeded && n != 1 {
			t.Errorf("invariant: SUCCEEDED job %s has %d result rows, want 1", id, n)
		}
	}
	ws, err := r.Repo.ListWorkers(ctx)
	if err != nil {
		t.Errorf("invariant: list workers: %v", err)
		return
	}
	for _, w := range ws {
		if running[w.ID] > w.TotalSlots {
			t.Errorf("invariant: worker %s holds %d active attempts with %d slots", w.ID, running[w.ID], w.TotalSlots)
		}
	}
	if r.PG != nil {
		if n := r.PG.Count(t, "SELECT count(*) FROM (SELECT job_id FROM attempts WHERE state IN ('RUNNING','PENDING') GROUP BY job_id HAVING count(*) > 1) x"); n != 0 {
			t.Errorf("invariant (database): %d jobs with more than one active attempt", n)
		}
		if n := r.PG.Count(t, "SELECT count(*) FROM (SELECT job_id FROM results GROUP BY job_id HAVING count(*) > 1) x"); n != 0 {
			t.Errorf("invariant (database): %d jobs with more than one result row", n)
		}
	}
}
