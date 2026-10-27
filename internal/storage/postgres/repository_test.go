package postgres_test

import (
	"context"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"testing"

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
	}
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
