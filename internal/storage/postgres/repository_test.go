package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/storagetest"
)

// TestPostgresRepository_Conformance runs the identical shared repository conformance
// suite against PostgreSQL durable storage in an isolated schema.
func TestPostgresRepository_Conformance(t *testing.T) {
	storagetest.RunRepositoryConformanceTests(t, func(t *testing.T) storage.Repository {
		pool, _ := setupTestPool(t)
		return postgres.NewRepository(pool)
	})
}

func newPostgresHarness(t *testing.T) storagetest.Harness {
	pool, _ := setupTestPool(t)
	return storagetest.Harness{
		Repo: postgres.NewRepository(pool),
		ForceJobState: func(t *testing.T, jobID string, state domain.JobState) {
			t.Helper()
			if _, err := pool.Exec(context.Background(), `UPDATE jobs SET state = $1 WHERE id = $2`, string(state), jobID); err != nil {
				t.Fatalf("force job %s to %s failed: %v", jobID, state, err)
			}
		},
		ForceAttemptState: func(t *testing.T, attemptID string, state domain.AttemptState) {
			t.Helper()
			if _, err := pool.Exec(context.Background(), `UPDATE attempts SET state = $1 WHERE id = $2`, string(state), attemptID); err != nil {
				t.Fatalf("force attempt %s to %s failed: %v", attemptID, state, err)
			}
		},
		LeaseDuration: domain.DefaultLeaseDuration,
		Now: func(t *testing.T) time.Time {
			t.Helper()
			var now time.Time
			if err := pool.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
				t.Fatalf("reading database time failed: %v", err)
			}
			return now
		},
		SetLeaseExpiry: func(t *testing.T, attemptID string, offset time.Duration) {
			t.Helper()
			if _, err := pool.Exec(context.Background(),
				`UPDATE attempts SET lease_expires_at = clock_timestamp() + make_interval(secs => $1::double precision) WHERE id = $2`,
				offset.Seconds(), attemptID); err != nil {
				t.Fatalf("set lease expiry of %s failed: %v", attemptID, err)
			}
		},
	}
}

func TestConformance_Dispatch_StampsLease(t *testing.T) {
	storagetest.RunDispatchLeaseConformance(t, newPostgresHarness)
}

func TestConformance_RenewLease_AcceptUntilSwept(t *testing.T) {
	storagetest.RunRenewLeaseAcceptUntilSweptConformance(t, newPostgresHarness)
}

func TestConformance_RenewLease_RejectedAfterLost(t *testing.T) {
	storagetest.RunRenewLeaseRejectedAfterLostConformance(t, newPostgresHarness)
}

func TestConformance_SweepExpiredLeases_RetryOrFail(t *testing.T) {
	storagetest.RunSweepRetryOrFailConformance(t, newPostgresHarness)
}

func TestConformance_SweepExpiredLeases_GraceInputSuppressesReaping(t *testing.T) {
	storagetest.RunSweepGraceInputConformance(t, newPostgresHarness)
}

func TestConformance_CommitAttempt_ConflictEvaluationOrder(t *testing.T) {
	storagetest.RunCommitAttemptConflictOrderConformance(t, newPostgresHarness)
}

func TestConformance_CancelJob_IdempotentCancelRequested(t *testing.T) {
	storagetest.RunCancelIdempotentCancelRequestedConformance(t, newPostgresHarness)
}

func TestConformance_CancelJob_RetryingImmediatelyCancelled(t *testing.T) {
	storagetest.RunCancelRetryingImmediatelyCancelledConformance(t, newPostgresHarness)
}

func TestConformance_CommitAttempt_TerminalJob(t *testing.T) {
	storagetest.RunCommitAttemptTerminalJobConformance(t, newPostgresHarness)
}

func TestProperty_StateMachine_ModelConformance(t *testing.T) {
	storagetest.RunStateMachineModelConformance(t, newPostgresHarness, []int64{1, 2, 3}, 120)
}

func TestConformance_CommitAttempt_NonRunningAttempt_ReturnsStaleAttempt(t *testing.T) {
	storagetest.RunCommitAttemptNonRunningAttemptConformance(t, newPostgresHarness)
}

func TestConformance_RetryingJob_DispatchAndCancelParity(t *testing.T) {
	storagetest.RunRetryingJobParityConformance(t, newPostgresHarness)
}
