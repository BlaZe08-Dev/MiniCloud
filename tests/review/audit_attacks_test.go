package review_test

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
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Point 4(a): Commit on a terminal job with a wrong attempt ID and with a wrong token.
// Tests what each repository does, what is mutated, and what the API returns.
func TestAudit_Point4a_CommitTerminalJob_WrongAttemptID_And_WrongToken(t *testing.T) {
	ctx := context.Background()

	runSubtest := func(t *testing.T, name string, newRepo func(t *testing.T) (storage.Repository, *pgxpool.Pool)) {
		t.Run(name, func(t *testing.T) {
			repo, pool := newRepo(t)
			svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
			if pool != nil {
				svc = coordinator.NewPostgresService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
			}
			ts := httptest.NewServer(api.NewServer("127.0.0.1:0", svc).Handler())
			defer ts.Close()

			// Register 1-slot worker
			call(t, ts, "POST", "/v1/workers/register", `{"id":"w_4a","version":"v1","total_slots":1,"supported_tasks":["echo"]}`)

			// Submit and poll Job A
			jobAID := submit(t, ts)
			_, pollBody := call(t, ts, "POST", "/v1/workers/w_4a/poll", `{}`)
			var pollResp struct {
				Assignment struct {
					AttemptID  string `json:"attempt_id"`
					LeaseToken string `json:"lease_token"`
				} `json:"assignment"`
			}
			field(t, pollBody, "assignment") // ensures valid JSON
			_ = jsonUnmarshal([]byte(pollBody), &pollResp)
			attA := pollResp.Assignment.AttemptID

			// Cancel Job A while RUNNING -> CANCEL_REQUESTED
			call(t, ts, "POST", fmt.Sprintf("/v1/jobs/%s/cancel", jobAID), "")

			// Force Job A into terminal CANCELLED state to test terminal commit path
			if pool != nil {
				_, err := pool.Exec(ctx, "UPDATE jobs SET state = $1 WHERE id = $2", string(domain.StateCancelled), jobAID)
				if err != nil {
					t.Fatalf("force job cancelled: %v", err)
				}
			} else {
				mem := repo.(*storage.InMemoryRepository)
				job, _ := mem.GetJob(ctx, jobAID)
				job.State = domain.StateCancelled
				if err := mem.UpdateJob(ctx, job); err != nil {
					t.Fatalf("update job: %v", err)
				}
			}

			// Case 1: Wrong attempt ID ("att_bogus") and wrong token ("tok_bogus") via Repository
			outcome := domain.AttemptOutcome{ExitCode: 0, Output: "done"}
			_, err := repo.CommitAttempt(ctx, jobAID, "att_bogus", "tok_bogus", "w_wrong", outcome)
			if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
				t.Fatalf("wrong attempt ID: expected ErrJobAlreadyTerminal, got %v", err)
			}

			// Verify attempt A is still RUNNING, not mutated by bogus attempt commit
			attAObj, err := repo.GetAttempt(ctx, attA)
			if err != nil {
				t.Fatalf("get attempt A: %v", err)
			}
			if attAObj.State != domain.AttemptRunning {
				t.Fatalf("attempt A should remain RUNNING after bogus attempt ID commit, got %s", attAObj.State)
			}

			// Verify no diagnostic events were recorded for bogus attempt
			events, err := repo.ListEventsByJob(ctx, jobAID)
			if err != nil {
				t.Fatalf("get job events: %v", err)
			}
			for _, ev := range events {
				if ev.EventType == domain.JobEventDiagnosticLateResult && ev.AttemptID != nil && *ev.AttemptID == "att_bogus" {
					t.Fatalf("diagnostic event must NOT be recorded for nonexistent attempt")
				}
			}

			// Case 1b: Wrong attempt ID via HTTP API
			status, body := call(t, ts, "POST", fmt.Sprintf("/v1/jobs/%s/attempts/att_bogus/result", jobAID),
				`{"lease_token":"tok_bogus","worker_id":"w_wrong","exit_code":0,"output":"done"}`)
			if status != http.StatusConflict {
				t.Fatalf("HTTP API wrong attempt ID: expected HTTP 409 Conflict, got %d %s", status, body)
			}
			t.Logf("Case 1 API response (wrong attempt ID): HTTP %d %s", status, body)

			// Case 2: Correct attempt ID (attA) with WRONG TOKEN ("tok_bogus") and WRONG WORKER ("w_wrong")
			// On a terminal job, token and worker are not verified; the RUNNING attempt is closed to prevent slot leak (SEC-022 / S1)
			_, err = repo.CommitAttempt(ctx, jobAID, attA, "tok_bogus", "w_wrong", outcome)
			if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
				t.Fatalf("wrong token on terminal job: expected ErrJobAlreadyTerminal, got %v", err)
			}

			// Verify attempt A was closed to CANCELLED and finished_at was set
			attAObj, err = repo.GetAttempt(ctx, attA)
			if err != nil {
				t.Fatalf("get attempt A after close: %v", err)
			}
			if attAObj.State != domain.AttemptCancelled {
				t.Fatalf("attempt A must be closed to CANCELLED on terminal commit, got %s", attAObj.State)
			}
			if attAObj.FinishedAt == nil {
				t.Fatalf("attempt A must have FinishedAt set")
			}

			// Verify exactly one DIAGNOSTIC_LATE_RESULT event is recorded
			events, err = repo.ListEventsByJob(ctx, jobAID)
			if err != nil {
				t.Fatalf("get job events: %v", err)
			}
			diagCount := 0
			for _, ev := range events {
				if ev.EventType == domain.JobEventDiagnosticLateResult && ev.AttemptID != nil && *ev.AttemptID == attA {
					diagCount++
				}
			}
			if diagCount != 1 {
				t.Fatalf("expected exactly 1 DIAGNOSTIC_LATE_RESULT event for attA, got %d", diagCount)
			}

			// Case 2b: Repeat commit via HTTP API with wrong token
			status, body = call(t, ts, "POST", fmt.Sprintf("/v1/jobs/%s/attempts/%s/result", jobAID, attA),
				`{"lease_token":"tok_bogus","worker_id":"w_wrong","exit_code":0,"output":"done"}`)
			if status != http.StatusConflict {
				t.Fatalf("HTTP API wrong token terminal commit: expected HTTP 409 Conflict, got %d %s", status, body)
			}
			t.Logf("Case 2 API response (wrong token, real attempt): HTTP %d %s", status, body)

			// Verify available slots restored to 1
			workers, err := repo.ListWorkers(ctx)
			if err != nil {
				t.Fatalf("list workers: %v", err)
			}
			if len(workers) != 1 || workers[0].AvailableSlots != 1 {
				t.Fatalf("worker slot must be freed (available_slots=1), got %+v", workers)
			}
		})
	}

	runSubtest(t, "memory", func(t *testing.T) (storage.Repository, *pgxpool.Pool) {
		return storage.NewInMemoryRepository(), nil
	})
	runSubtest(t, "postgres", func(t *testing.T) (storage.Repository, *pgxpool.Pool) {
		pool := setupProbePool(t)
		return postgres.NewRepository(pool), pool
	})
}

// Point 4(b): Two concurrent cancels on one running job, memory and Postgres, repeated >= 50 times with -race.
func TestAudit_Point4b_ConcurrentCancels(t *testing.T) {
	ctx := context.Background()

	runSubtest := func(t *testing.T, name string, newRepo func(t *testing.T) storage.Repository) {
		t.Run(name, func(t *testing.T) {
			const iterations = 50
			for i := 0; i < iterations; i++ {
				repo := newRepo(t)
				workerID := fmt.Sprintf("w_conc_%d", i)
				jobID := fmt.Sprintf("j_conc_%d", i)

				// Register 1-slot worker
				now := time.Now()
				err := repo.UpsertWorker(ctx, &domain.Worker{
					ID:              workerID,
					TotalSlots:      1,
					AvailableSlots:  1,
					SupportedTasks:  []string{"echo"},
					Status:          domain.WorkerStatusHealthy,
					LastHeartbeatAt: now,
					RegisteredAt:    now,
				})
				if err != nil {
					t.Fatalf("iter %d: register worker: %v", i, err)
				}

				// Submit and dispatch job
				err = repo.CreateJob(ctx, &domain.Job{
					ID:             jobID,
					TaskType:       "echo",
					Payload:        []byte(`{}`),
					State:          domain.StateQueued,
					MaxAttempts:    3,
					TimeoutSeconds: 60,
					CreatedAt:      now,
					UpdatedAt:      now,
				})
				if err != nil {
					t.Fatalf("iter %d: create job: %v", i, err)
				}

				job, att, err := repo.DispatchNextJob(ctx, workerID)
				if err != nil {
					t.Fatalf("iter %d: dispatch: %v", i, err)
				}

				// Two concurrent cancels
				var wg sync.WaitGroup
				var err1, err2 error
				var job1, job2 *domain.Job
				wg.Add(2)
				go func() {
					defer wg.Done()
					job1, err1 = repo.CancelJob(ctx, job.ID)
				}()
				go func() {
					defer wg.Done()
					job2, err2 = repo.CancelJob(ctx, job.ID)
				}()
				wg.Wait()

				if err1 != nil {
					t.Fatalf("iter %d: cancel 1: %v", i, err1)
				}
				if err2 != nil {
					t.Fatalf("iter %d: cancel 2: %v", i, err2)
				}
				if job1.State != domain.StateCancelRequested || job2.State != domain.StateCancelRequested {
					t.Fatalf("iter %d: both cancels must see CANCEL_REQUESTED, got %s and %s", i, job1.State, job2.State)
				}

				// Verify attempt is still RUNNING
				attObj, err := repo.GetAttempt(ctx, att.ID)
				if err != nil {
					t.Fatalf("iter %d: get attempt: %v", i, err)
				}
				if attObj.State != domain.AttemptRunning {
					t.Fatalf("iter %d: attempt must still be RUNNING before worker commits, got %s", i, attObj.State)
				}

				// Verify worker available_slots is still 0 (attempt is running)
				workers, err := repo.ListWorkers(ctx)
				if err != nil {
					t.Fatalf("iter %d: list workers: %v", i, err)
				}
				if workers[0].AvailableSlots != 0 {
					t.Fatalf("iter %d: available_slots must be 0 while attempt is running, got %d", i, workers[0].AvailableSlots)
				}

				// Worker commits result
				_, err = repo.CommitAttempt(ctx, job.ID, att.ID, att.LeaseToken, workerID, domain.AttemptOutcome{ExitCode: 0, Output: "done"})
				if err != nil {
					t.Fatalf("iter %d: worker commit: %v", i, err)
				}

				// Verify job is SUCCEEDED and slot is 1
				jobAfter, err := repo.GetJob(ctx, job.ID)
				if err != nil {
					t.Fatalf("iter %d: get job after commit: %v", i, err)
				}
				if jobAfter.State != domain.StateSucceeded {
					t.Fatalf("iter %d: expected SUCCEEDED, got %s", i, jobAfter.State)
				}

				workers, err = repo.ListWorkers(ctx)
				if err != nil {
					t.Fatalf("iter %d: list workers after commit: %v", i, err)
				}
				if workers[0].AvailableSlots != 1 {
					t.Fatalf("iter %d: available_slots must be 1 after commit, got %d", i, workers[0].AvailableSlots)
				}
			}
		})
	}

	runSubtest(t, "memory", func(t *testing.T) storage.Repository {
		return storage.NewInMemoryRepository()
	})
	runSubtest(t, "postgres", func(t *testing.T) storage.Repository {
		pool := setupProbePool(t)
		return postgres.NewRepository(pool)
	})
}

// Point 4(c): Commit racing Cancel.
// One goroutine commits, one goroutine cancels. Repeated 50 times with -race.
func TestAudit_Point4c_CommitRacingCancel(t *testing.T) {
	ctx := context.Background()

	runSubtest := func(t *testing.T, name string, newRepo func(t *testing.T) storage.Repository) {
		t.Run(name, func(t *testing.T) {
			const iterations = 50
			for i := 0; i < iterations; i++ {
				repo := newRepo(t)
				workerID := fmt.Sprintf("w_race_%d", i)
				jobID := fmt.Sprintf("j_race_%d", i)

				now := time.Now()
				err := repo.UpsertWorker(ctx, &domain.Worker{
					ID:              workerID,
					TotalSlots:      1,
					AvailableSlots:  1,
					SupportedTasks:  []string{"echo"},
					Status:          domain.WorkerStatusHealthy,
					LastHeartbeatAt: now,
					RegisteredAt:    now,
				})
				if err != nil {
					t.Fatalf("iter %d: register: %v", i, err)
				}

				err = repo.CreateJob(ctx, &domain.Job{
					ID:             jobID,
					TaskType:       "echo",
					Payload:        []byte(`{}`),
					State:          domain.StateQueued,
					MaxAttempts:    3,
					TimeoutSeconds: 60,
					CreatedAt:      now,
					UpdatedAt:      now,
				})
				if err != nil {
					t.Fatalf("iter %d: create job: %v", i, err)
				}

				job, att, err := repo.DispatchNextJob(ctx, workerID)
				if err != nil {
					t.Fatalf("iter %d: dispatch: %v", i, err)
				}

				var wg sync.WaitGroup
				var commitErr, cancelErr error
				wg.Add(2)
				go func() {
					defer wg.Done()
					_, commitErr = repo.CommitAttempt(ctx, job.ID, att.ID, att.LeaseToken, workerID, domain.AttemptOutcome{ExitCode: 0, Output: "ok"})
				}()
				go func() {
					defer wg.Done()
					_, cancelErr = repo.CancelJob(ctx, job.ID)
				}()
				wg.Wait()

				// In either interleaving, the system must reach a consistent valid state:
				// If commit won: commitErr == nil, cancelErr == ErrJobAlreadyTerminal
				// If cancel won: cancelErr == nil, commitErr == nil (transitions CANCEL_REQUESTED -> SUCCEEDED)
				// In all cases, commit must succeed or return ErrJobAlreadyTerminal, and cancel must succeed or return ErrJobAlreadyTerminal.
				if commitErr != nil && !errors.Is(commitErr, domain.ErrJobAlreadyTerminal) {
					t.Fatalf("iter %d: unexpected commitErr: %v", i, commitErr)
				}
				if cancelErr != nil && !errors.Is(cancelErr, domain.ErrJobAlreadyTerminal) {
					t.Fatalf("iter %d: unexpected cancelErr: %v", i, cancelErr)
				}

				// Verify job is in terminal state
				finalJob, err := repo.GetJob(ctx, job.ID)
				if err != nil {
					t.Fatalf("iter %d: get job: %v", i, err)
				}
				if !finalJob.State.IsTerminal() {
					t.Fatalf("iter %d: job must be in terminal state, got %s", i, finalJob.State)
				}

				// Verify attempt is NOT running
				finalAtt, err := repo.GetAttempt(ctx, att.ID)
				if err != nil {
					t.Fatalf("iter %d: get attempt: %v", i, err)
				}
				if finalAtt.State == domain.AttemptRunning {
					t.Fatalf("iter %d: attempt must NOT be running after terminal job, got %s", i, finalAtt.State)
				}

				// Verify worker available_slots is restored to 1
				workers, err := repo.ListWorkers(ctx)
				if err != nil {
					t.Fatalf("iter %d: list workers: %v", i, err)
				}
				if workers[0].AvailableSlots != 1 {
					t.Fatalf("iter %d: slot leak detected: available_slots=%d, want 1", i, workers[0].AvailableSlots)
				}
			}
		})
	}

	runSubtest(t, "memory", func(t *testing.T) storage.Repository {
		return storage.NewInMemoryRepository()
	})
	runSubtest(t, "postgres", func(t *testing.T) storage.Repository {
		pool := setupProbePool(t)
		return postgres.NewRepository(pool)
	})
}

// Point 4(d): Repeated terminal-job commits: how many DIAGNOSTIC_LATE_RESULT events accumulate?
func TestAudit_Point4d_RepeatedTerminalJobCommits_DiagnosticEventCount(t *testing.T) {
	ctx := context.Background()

	runSubtest := func(t *testing.T, name string, newRepo func(t *testing.T) (storage.Repository, *pgxpool.Pool)) {
		t.Run(name, func(t *testing.T) {
			repo, pool := newRepo(t)
			workerID := "w_repeat"
			jobID := "j_repeat"

			now := time.Now()
			err := repo.UpsertWorker(ctx, &domain.Worker{
				ID:              workerID,
				TotalSlots:      1,
				AvailableSlots:  1,
				SupportedTasks:  []string{"echo"},
				Status:          domain.WorkerStatusHealthy,
				LastHeartbeatAt: now,
				RegisteredAt:    now,
			})
			if err != nil {
				t.Fatalf("register: %v", err)
			}

			err = repo.CreateJob(ctx, &domain.Job{
				ID:             jobID,
				TaskType:       "echo",
				Payload:        []byte(`{}`),
				State:          domain.StateQueued,
				MaxAttempts:    3,
				TimeoutSeconds: 60,
				CreatedAt:      now,
				UpdatedAt:      now,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			job, att, err := repo.DispatchNextJob(ctx, workerID)
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}

			// Force job to CANCELLED
			if pool != nil {
				_, err = pool.Exec(ctx, "UPDATE jobs SET state = $1 WHERE id = $2", string(domain.StateCancelled), job.ID)
				if err != nil {
					t.Fatalf("force cancelled: %v", err)
				}
			} else {
				mem := repo.(*storage.InMemoryRepository)
				j, _ := mem.GetJob(ctx, job.ID)
				j.State = domain.StateCancelled
				if err := mem.UpdateJob(ctx, j); err != nil {
					t.Fatalf("update job: %v", err)
				}
			}

			// Issue 10 consecutive late commits
			for k := 0; k < 10; k++ {
				_, err := repo.CommitAttempt(ctx, job.ID, att.ID, att.LeaseToken, workerID, domain.AttemptOutcome{ExitCode: 0, Output: "late"})
				if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
					t.Fatalf("commit %d: expected ErrJobAlreadyTerminal, got %v", k, err)
				}
			}

			// Verify event count
			events, err := repo.ListEventsByJob(ctx, job.ID)
			if err != nil {
				t.Fatalf("get events: %v", err)
			}
			diagCount := 0
			for _, ev := range events {
				if ev.EventType == domain.JobEventDiagnosticLateResult {
					diagCount++
				}
			}
			if diagCount != 1 {
				t.Fatalf("expected exactly 1 DIAGNOSTIC_LATE_RESULT event after 10 commits, got %d", diagCount)
			}
			t.Logf("DIAGNOSTIC_LATE_RESULT events after 10 terminal commits: %d", diagCount)
		})
	}

	runSubtest(t, "memory", func(t *testing.T) (storage.Repository, *pgxpool.Pool) {
		return storage.NewInMemoryRepository(), nil
	})
	runSubtest(t, "postgres", func(t *testing.T) (storage.Repository, *pgxpool.Pool) {
		pool := setupProbePool(t)
		return postgres.NewRepository(pool), pool
	})
}

func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
