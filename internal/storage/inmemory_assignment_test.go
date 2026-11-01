package storage_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/storagetest"
)

// The assignment conformance cases live in the shared storagetest runner (ADR-0017 implementation
// note 6; moved there in slice 5b), and this file runs them against the in-memory repository. The
// helpers below are also used by the in-memory property tests, which stay memory-only.

// Assignment parameters: the same values as the shared runner.
const (
	testAcceptWindow  = storagetest.AssignTestAcceptWindow
	testWorkerTimeout = storagetest.AssignTestWorkerTimeout
	testBatchLimit    = storagetest.AssignTestBatchLimit
	testFallbackAge   = storagetest.AssignTestFallbackAge
	testMaxLapses     = storagetest.AssignTestMaxLapses
)

// assignClock is an injectable clock for the in-memory repository.
type assignClock struct {
	mu  sync.Mutex
	now time.Time
}

func newAssignClock() *assignClock {
	return &assignClock{now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
}

func (c *assignClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *assignClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newAssignRepo(t *testing.T) (*storage.InMemoryRepository, *assignClock) {
	t.Helper()
	clk := newAssignClock()
	return storage.NewInMemoryRepository(storage.WithClock(clk)), clk
}

func addAssignWorker(t *testing.T, repo *storage.InMemoryRepository, clk *assignClock, id string, slots int, tasks ...string) {
	t.Helper()
	err := repo.UpsertWorker(context.Background(), &domain.Worker{
		ID:              id,
		Version:         "v1.0.0",
		TotalSlots:      slots,
		Status:          domain.WorkerStatusHealthy,
		SupportedTasks:  tasks,
		LastHeartbeatAt: clk.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertWorker(%s): %v", id, err)
	}
}

// pollAssign records a poll and a heartbeat for a worker, so it is eligible for a reservation.
func pollAssign(t *testing.T, repo *storage.InMemoryRepository, clk *assignClock, id string) {
	t.Helper()
	ctx := context.Background()
	if err := repo.RecordHeartbeat(ctx, id, 0); err != nil {
		t.Fatalf("RecordHeartbeat(%s): %v", id, err)
	}
	if err := repo.RecordPoll(ctx, id); err != nil {
		t.Fatalf("RecordPoll(%s): %v", id, err)
	}
}

func submitAssign(t *testing.T, repo *storage.InMemoryRepository, id, taskType string) {
	t.Helper()
	err := repo.SubmitJob(context.Background(), &domain.Job{
		ID:             id,
		TaskType:       taskType,
		Payload:        []byte("{}"),
		State:          domain.StateQueued,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob(%s): %v", id, err)
	}
}

func assignParams() storage.AssignParams {
	return storage.AssignParams{AcceptWindow: testAcceptWindow, WorkerTimeout: testWorkerTimeout, BatchLimit: testBatchLimit, MaxLapses: testMaxLapses}
}

func runPass(t *testing.T, repo *storage.InMemoryRepository) storage.AssignResult {
	t.Helper()
	res, err := repo.AssignQueuedJobs(context.Background(), assignParams())
	if err != nil {
		t.Fatalf("AssignQueuedJobs: %v", err)
	}
	return res
}

func claimOpts(ageArm bool) storage.ClaimOptions {
	return storage.ClaimOptions{MaxLapses: testMaxLapses, FallbackAge: testFallbackAge, AgeArmOpen: ageArm}
}

func mustJob(t *testing.T, repo *storage.InMemoryRepository, id string) *domain.Job {
	t.Helper()
	job, err := repo.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJob(%s): %v", id, err)
	}
	return job
}

// newInMemoryAssignmentHarness builds the shared assignment harness over a fresh in-memory
// repository with an injected clock.
func newInMemoryAssignmentHarness(t *testing.T) storagetest.AssignmentHarness {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	return storagetest.AssignmentHarness{
		Repo:    repo,
		Now:     func(t *testing.T) time.Time { return clk.Now() },
		Advance: func(t *testing.T, d time.Duration) { clk.Advance(d) },
		SetReservation: func(t *testing.T, jobID, workerID string, expiresIn time.Duration) {
			t.Helper()
			job := mustJob(t, repo, jobID)
			w := workerID
			expires := clk.Now().Add(expiresIn)
			at := expires.Add(-testAcceptWindow)
			job.AssignedWorkerID = &w
			job.AssignedAt = &at
			job.AssignmentExpiresAt = &expires
			if err := repo.UpdateJob(ctx, job); err != nil {
				t.Fatalf("UpdateJob(%s): %v", jobID, err)
			}
		},
		SetLapses: func(t *testing.T, jobID string, lapses int) {
			t.Helper()
			job := mustJob(t, repo, jobID)
			job.AssignmentLapses = lapses
			if err := repo.UpdateJob(ctx, job); err != nil {
				t.Fatalf("UpdateJob(%s): %v", jobID, err)
			}
		},
		SetRetrying: func(t *testing.T, jobID string, notBeforeIn time.Duration) {
			t.Helper()
			job := mustJob(t, repo, jobID)
			job.State = domain.StateRetrying
			nb := clk.Now().Add(notBeforeIn)
			job.NotBefore = &nb
			if err := repo.UpdateJob(ctx, job); err != nil {
				t.Fatalf("UpdateJob(%s): %v", jobID, err)
			}
		},
		HoldPassLock: func(t *testing.T) func() { return repo.HoldPassLockForTest() },
		LastPollAt: func(t *testing.T, workerID string) *time.Time {
			t.Helper()
			w, err := repo.GetWorker(ctx, workerID)
			if err != nil {
				t.Fatalf("GetWorker(%s): %v", workerID, err)
			}
			return w.LastPollAt
		},
	}
}

// INV-2.
func TestConformance_Assign_CreatesNoAttempt(t *testing.T) {
	storagetest.RunAssignCreatesNoAttemptConformance(t, newInMemoryAssignmentHarness)
}

// INV-3.
func TestConformance_ClaimAssigned_OnlyAssignedWithinWindow(t *testing.T) {
	storagetest.RunClaimAssignedOnlyAssignedWithinWindowConformance(t, newInMemoryAssignmentHarness)
}

// INV-7a.
func TestConformance_AssignmentLapse_ConsumesNoAttempt(t *testing.T) {
	storagetest.RunAssignmentLapseConsumesNoAttemptConformance(t, newInMemoryAssignmentHarness)
}

// INV-7b.
func TestConformance_AssignmentLapse_FallbackAfterLimit(t *testing.T) {
	storagetest.RunAssignmentLapseFallbackAfterLimitConformance(t, newInMemoryAssignmentHarness)
}

// INV-8.
func TestConformance_CancelAssigned_NeverClaimed(t *testing.T) {
	storagetest.RunCancelAssignedNeverClaimedConformance(t, newInMemoryAssignmentHarness)
}

// INV-10 (a).
func TestConformance_Assign_OneReservationPerWorker(t *testing.T) {
	storagetest.RunAssignOneReservationPerWorkerConformance(t, newInMemoryAssignmentHarness)
}

// INV-10 (b).
func TestConformance_Assign_SkipsWorkerWithRunningAttempt(t *testing.T) {
	storagetest.RunAssignSkipsWorkerWithRunningAttemptConformance(t, newInMemoryAssignmentHarness)
}

// INV-11.
func TestConformance_Assign_SkipsStaleWorkers(t *testing.T) {
	storagetest.RunAssignSkipsStaleWorkersConformance(t, newInMemoryAssignmentHarness)
}

// INV-12.
func TestConformance_FifoPull_IgnoresAssignmentColumns(t *testing.T) {
	storagetest.RunFifoPullIgnoresAssignmentColumnsConformance(t, newInMemoryAssignmentHarness)
}

// INV-18.
func TestConformance_AssignEvent_CapsCandidates(t *testing.T) {
	storagetest.RunAssignEventCapsCandidatesConformance(t, newInMemoryAssignmentHarness)
}

// INV-1, in-memory variant (ADR-0017 implementation note 5).
func TestConformance_ClaimAssigned_ConcurrentPollsOneWinner(t *testing.T) {
	storagetest.RunClaimAssignedConcurrentPollsOneWinnerConformance(t, newInMemoryAssignmentHarness)
}

// F-5a-2.
func TestConformance_Claim_RespectsWorkerCapacity(t *testing.T) {
	storagetest.RunClaimRespectsWorkerCapacityConformance(t, newInMemoryAssignmentHarness)
}

// F-5a-3.
func TestConformance_AssignPass_ExcludesJobAtLapseLimit(t *testing.T) {
	storagetest.RunAssignPassExcludesJobAtLapseLimitConformance(t, newInMemoryAssignmentHarness)
}

// F-5a-3.
func TestConformance_ClaimLapseArm_ClosedBelowLimit(t *testing.T) {
	storagetest.RunClaimLapseArmClosedBelowLimitConformance(t, newInMemoryAssignmentHarness)
}

// F-5a-4.
func TestConformance_AssignPass_UnhealthyRevokeCountsNoLapse(t *testing.T) {
	storagetest.RunAssignPassUnhealthyRevokeCountsNoLapseConformance(t, newInMemoryAssignmentHarness)
}

// F-5a-5.
func TestConformance_ClaimOwnArm_ClosedAtExactExpiry(t *testing.T) {
	storagetest.RunClaimOwnArmClosedAtExactExpiryConformance(t, newInMemoryAssignmentHarness)
}

// F-5a-6.
func TestConformance_ClaimAgeArm_UsesNotBefore(t *testing.T) {
	storagetest.RunClaimAgeArmUsesNotBeforeConformance(t, newInMemoryAssignmentHarness)
}

// F-5a-7: 20 rounds, as in slice 5a.
func TestConformance_AssignEvent_CandidatesInIDOrder(t *testing.T) {
	storagetest.RunAssignEventCandidatesInIDOrderConformance(t, newInMemoryAssignmentHarness, 20)
}

// F-5a-9, INV-22 memory twin.
func TestConformance_AssignPass_SkipsWhenPassLockHeld(t *testing.T) {
	storagetest.RunAssignPassSkipsWhenPassLockHeldConformance(t, newInMemoryAssignmentHarness)
}

// Section 2.1 event shape: cursor_before follows the in-pass cursor (P4-08c review F-R5).
func TestConformance_AssignEvent_CursorBeforeAdvancesWithinPass(t *testing.T) {
	storagetest.RunAssignEventCursorBeforeAdvancesWithinPassConformance(t, newInMemoryAssignmentHarness)
}

// Sections 3.2 and 3.5: an expired, unrevoked reservation is not live for the fallback arm (P4-08c
// review F-R6).
func TestConformance_ClaimFallback_ExpiredUnrevokedReservation(t *testing.T) {
	storagetest.RunClaimFallbackExpiredUnrevokedReservationConformance(t, newInMemoryAssignmentHarness)
}

// INV-9 at the repository (slice 5c): grace revokes without counting the lapse.
func TestConformance_AssignPass_GraceRevokesWithoutCounting(t *testing.T) {
	storagetest.RunAssignPassGraceRevokesWithoutCountingConformance(t, newInMemoryAssignmentHarness)
}

// Poll freshness throttle (ADR-0017 Section 2.2, implementation note 9).
func TestConformance_RecordPoll_ThrottlesFreshnessWrites(t *testing.T) {
	storagetest.RunRecordPollThrottlesFreshnessWritesConformance(t, newInMemoryAssignmentHarness)
}

// ADR-0017 Sections 3.2 and 5.1: the policy and fallback reason per claim arm (slice 5c, part 2).
func TestConformance_ClaimPolicy_PerArm(t *testing.T) {
	storagetest.RunClaimPolicyPerArmConformance(t, newInMemoryAssignmentHarness)
}

// ADR-0017 Section 5.1: the four assignment fields of the job view (slice 5c, part 2).
func TestConformance_JobView_AssignmentFields(t *testing.T) {
	storagetest.RunJobViewAssignmentFieldsConformance(t, newInMemoryAssignmentHarness)
}

// ADR-0017 note 25: a pending grace state suppresses a lapse only until GracePendingUntil on the
// repository clock. One nanosecond before the end it holds the lapse back; at the end it counts. A
// pending state with no known end stays pending.
func TestLapseCounts_PendingEndsAtUntil(t *testing.T) {
	end := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	p := storage.AssignParams{GracePending: true, GracePendingUntil: end}
	for _, c := range []struct {
		name string
		p    storage.AssignParams
		now  time.Time
		want bool
	}{
		{"1 ns before the end", p, end.Add(-time.Nanosecond), false},
		{"at the end", p, end, true},
		{"after the end", p, end.Add(time.Hour), true},
		{"no end known", storage.AssignParams{GracePending: true}, end.Add(100 * time.Hour), false},
		{"not pending", storage.AssignParams{}, end, true},
	} {
		if got := storage.LapseCounts(c.p, c.now); got != c.want {
			t.Fatalf("%s: LapseCounts=%v, want %v", c.name, got, c.want)
		}
	}
}
