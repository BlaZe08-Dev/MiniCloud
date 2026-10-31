package storage_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// propertyIterations is the iteration count the ADR requires for INV-4, INV-4b, INV-15 and
// INV-16 (Section 6.4, "at least 1,000 iterations").
const propertyIterations = 1000

// INV-15: per pass, at most B candidates are read per type, and no job of a type is passed over
// for a newer one while a worker of that type is eligible. Within one type the candidate read is
// in FIFO order, and a candidate is passed over only when no worker is eligible at its decision.
// The eligible set only shrinks during a pass. So a dispatchable job of type t left unreserved
// after the pass, with a worker of type t still eligible, is allowed only when the read for t was
// full: the batch limit, not another type, is the reason it was not reached.
func TestProperty_BatchHeadNeverStarved(t *testing.T) {
	types := []string{"echo", "sleep"}
	reservations := 0
	defer func() {
		if reservations == 0 {
			t.Errorf("no reservation was made; the property is vacuous")
		}
	}()
	for seed := int64(1); seed <= propertyIterations; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		repo, clk := newAssignRepo(t)
		ctx := context.Background()
		batch := 1 + rnd.Intn(4)

		var workerIDs []string
		for i := 0; i < 1+rnd.Intn(4); i++ {
			id := fmt.Sprintf("w_%02d", i)
			var tasks []string
			for _, ty := range types {
				if rnd.Intn(2) == 0 {
					tasks = append(tasks, ty)
				}
			}
			if len(tasks) == 0 {
				tasks = []string{types[rnd.Intn(len(types))]}
			}
			addAssignWorker(t, repo, clk, id, 1, tasks...)
			workerIDs = append(workerIDs, id)
		}

		var jobIDs []string
		var jobTypes []string
		for i := 0; i < 1+rnd.Intn(12); i++ {
			ty := types[rnd.Intn(len(types))]
			id := fmt.Sprintf("job_%02d", i)
			submitAssign(t, repo, id, ty)
			jobIDs = append(jobIDs, id)
			jobTypes = append(jobTypes, ty)
		}

		for step := 0; step < 4; step++ {
			clk.Advance(time.Duration(rnd.Intn(3)) * time.Second)
			for _, w := range workerIDs {
				if rnd.Intn(3) != 0 {
					pollAssign(t, repo, clk, w)
				}
			}
			res, err := repo.AssignQueuedJobs(ctx, storage.AssignParams{AcceptWindow: testAcceptWindow, WorkerTimeout: testWorkerTimeout, BatchLimit: batch, MaxLapses: testMaxLapses})
			if err != nil {
				t.Fatalf("seed %d: pass: %v", seed, err)
			}
			reservations += res.Reserved
			for ty, n := range res.CandidatesRead {
				if n > batch {
					t.Fatalf("seed %d: pass read %d candidates of %s, batch is %d", seed, n, ty, batch)
				}
			}
			for i, id := range jobIDs {
				ty := jobTypes[i]
				if mustJob(t, repo, id).AssignedWorkerID != nil {
					continue
				}
				for _, w := range workerIDs {
					if eligibleForType(t, repo, clk, w, ty) && res.CandidatesRead[ty] < batch {
						t.Fatalf("seed %d step %d: job %s of type %s unreserved while %s is eligible and only %d of %d candidates were read", seed, step, id, ty, w, res.CandidatesRead[ty], batch)
					}
				}
			}
		}
	}
}

// eligibleForType reports whether a worker is eligible for a reservation of the type now, using
// the ADR-0017 Section 2.2 predicates available in this test: HEALTHY, fresh heartbeat, fresh
// poll, supports the type, and no live reservation. No attempts exist in this test.
func eligibleForType(t *testing.T, repo *storage.InMemoryRepository, clk *assignClock, id, ty string) bool {
	t.Helper()
	w, err := repo.GetWorker(context.Background(), id)
	if err != nil {
		t.Fatalf("GetWorker(%s): %v", id, err)
	}
	now := clk.Now()
	if w.Status != domain.WorkerStatusHealthy || !w.SupportsTask(ty) {
		return false
	}
	if !w.LastHeartbeatAt.After(now.Add(-testWorkerTimeout)) {
		return false
	}
	if w.LastPollAt == nil || !w.LastPollAt.After(now.Add(-testAcceptWindow)) {
		return false
	}
	for _, j := range listAllJobs(t, repo) {
		if j.AssignedWorkerID != nil && *j.AssignedWorkerID == id && j.AssignmentExpiresAt != nil && j.AssignmentExpiresAt.After(now) {
			return false
		}
	}
	return true
}

func listAllJobs(t *testing.T, repo *storage.InMemoryRepository) []*domain.Job {
	t.Helper()
	jobs, err := repo.ListQueuedJobs(context.Background())
	if err != nil {
		t.Fatalf("ListQueuedJobs: %v", err)
	}
	return jobs
}

// INV-16: a pass that is live (silence shorter than the fallback age) never produces an age
// fallback claim, even when the poll's age arm is open and an unreserved job is older than the
// fallback age. Here the pollers are workers that keep polling but never heartbeat, so the pass
// never makes them eligible for a reservation. Every step they could take an aged job through
// the age arm, and only the pass-silence condition stops them.
func TestProperty_SaturatedPass_NoAgeFallback(t *testing.T) {
	exposures := 0
	defer func() {
		if exposures == 0 {
			t.Errorf("no poll saw an aged unreserved job; the property is vacuous")
		}
	}()
	for seed := int64(1); seed <= propertyIterations; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		repo, clk := newAssignRepo(t)
		ctx := context.Background()
		var eligible, stale []string
		for i := 0; i < rnd.Intn(3); i++ {
			id := fmt.Sprintf("w_e%02d", i)
			addAssignWorker(t, repo, clk, id, 1, "echo")
			eligible = append(eligible, id)
		}
		for i := 0; i < 1+rnd.Intn(2); i++ {
			id := fmt.Sprintf("w_s%02d", i)
			addAssignWorker(t, repo, clk, id, 1, "echo")
			stale = append(stale, id)
		}
		for i := 0; i < 2+rnd.Intn(9); i++ {
			submitAssign(t, repo, fmt.Sprintf("job_%02d", i), "echo")
		}
		for step := 0; step < 40; step++ {
			clk.Advance(time.Second)
			for _, w := range eligible {
				pollAssign(t, repo, clk, w)
			}
			for _, w := range stale {
				if err := repo.RecordPoll(ctx, w); err != nil {
					t.Fatalf("seed %d: RecordPoll(%s): %v", seed, w, err)
				}
			}
			runPass(t, repo)

			poller := stale[rnd.Intn(len(stale))]
			now := clk.Now()
			for _, j := range listAllJobs(t, repo) {
				if j.AssignedWorkerID == nil && !j.CreatedAt.After(now.Add(-testFallbackAge)) {
					exposures++
					break
				}
			}
			claim, err := repo.ClaimForPoll(ctx, poller, claimOpts(true))
			if err == nil && claim.FallbackReason == domain.FallbackReasonAge {
				t.Fatalf("seed %d step %d: age fallback by %s while the pass is live (job %s)", seed, step, poller, claim.Job.ID)
			}
			if err == nil {
				// Finish the attempt so the worker has capacity again on a later poll.
				claim.Attempt.State = domain.AttemptCompleted
				if err := repo.UpdateAttempt(ctx, claim.Attempt); err != nil {
					t.Fatalf("seed %d: UpdateAttempt: %v", seed, err)
				}
			}
		}
	}
}
