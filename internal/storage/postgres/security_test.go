package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// TestPostgresRepository_Security_SQLInjectionChallenge tests that hostile SQL injection
// payloads in job IDs, worker IDs, task types, payloads, and attempt IDs are treated strictly
// as verbatim text parameters and cannot manipulate SQL queries or alter schema objects.
func TestPostgresRepository_Security_SQLInjectionChallenge(t *testing.T) {
	ctx := context.Background()
	pool, schemaName := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	injectionPayloads := []string{
		"'; DROP TABLE jobs CASCADE; --",
		"' OR '1'='1",
		"admin'--",
		"'\"; DROP TABLE attempts CASCADE; --",
		"'; TRUNCATE TABLE workers CASCADE; --",
		"1; SELECT pg_sleep(5); --",
	}

	// 1. Worker ID & Task Type SQL injection
	for i, attackStr := range injectionPayloads {
		workerID := fmt.Sprintf("w_inject_%d_%s", i, attackStr)
		taskType := fmt.Sprintf("task_%s", attackStr)
		worker := &domain.Worker{
			ID:             workerID,
			Version:        "v1.0.0",
			TotalSlots:     2,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{taskType},
		}
		if err := repo.UpsertWorker(ctx, worker); err != nil {
			t.Fatalf("UpsertWorker with SQL injection string failed: %v", err)
		}

		retrieved, err := repo.GetWorker(ctx, workerID)
		if err != nil {
			t.Fatalf("GetWorker with SQL injection ID failed: %v", err)
		}
		if retrieved.ID != workerID {
			t.Fatalf("worker ID was altered or interpreted: expected %q, got %q", workerID, retrieved.ID)
		}
	}

	// 2. Job ID, Task Type & Payload SQL injection
	for i, attackStr := range injectionPayloads {
		jobID := fmt.Sprintf("job_inject_%d_%s", i, attackStr)
		job := &domain.Job{
			ID:             jobID,
			TaskType:       fmt.Sprintf("task_%s", attackStr),
			Payload:        []byte(fmt.Sprintf(`{"attack":"%s"}`, attackStr)),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, job); err != nil {
			t.Fatalf("SubmitJob with SQL injection string failed: %v", err)
		}

		retrieved, err := repo.GetJob(ctx, jobID)
		if err != nil {
			t.Fatalf("GetJob with SQL injection ID failed: %v", err)
		}
		if retrieved.ID != jobID {
			t.Fatalf("job ID was altered: expected %q, got %q", jobID, retrieved.ID)
		}
		if string(retrieved.Payload) != string(job.Payload) {
			t.Fatalf("job payload was corrupted: expected %q, got %q", string(job.Payload), string(retrieved.Payload))
		}
	}

	// Verify schema objects are 100% intact
	var tableCount int
	checkTablesQuery := fmt.Sprintf(`
		SELECT COUNT(*) 
		FROM information_schema.tables 
		WHERE table_schema = '%s' AND table_name IN ('jobs', 'attempts', 'workers', 'results', 'job_events');
	`, schemaName)
	if err := pool.QueryRow(ctx, checkTablesQuery).Scan(&tableCount); err != nil {
		t.Fatalf("failed to query table count: %v", err)
	}
	if tableCount != 5 {
		t.Fatalf("CRITICAL SECURITY VULNERABILITY: SQL injection destroyed tables! Expected 5 tables, got %d", tableCount)
	}
}

// TestPostgresRepository_Security_PayloadAndOutputBoundaryLimits tests exact payload and
// output boundary enforcement at 65535 (below), 65536 (exact limit), and 65537 (above limit).
func TestPostgresRepository_Security_PayloadAndOutputBoundaryLimits(t *testing.T) {
	ctx := context.Background()
	pool, _ := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	worker := &domain.Worker{
		ID:             "w_limits",
		Version:        "v1.0.0",
		TotalSlots:     5,
		Status:         domain.WorkerStatusHealthy,
		SupportedTasks: []string{"echo"},
	}
	if err := repo.UpsertWorker(ctx, worker); err != nil {
		t.Fatalf("UpsertWorker failed: %v", err)
	}

	// 1. Payload limits
	// 65535 bytes: Below limit -> Accepted
	jBelow := &domain.Job{
		ID:             "j_payload_65535",
		TaskType:       "echo",
		Payload:        make([]byte, 65535),
		State:          domain.StateQueued,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
	}
	if err := repo.SubmitJob(ctx, jBelow); err != nil {
		t.Fatalf("payload of 65535 bytes should be accepted, got: %v", err)
	}

	// 65536 bytes: Exact limit -> Accepted
	jExact := &domain.Job{
		ID:             "j_payload_65536",
		TaskType:       "echo",
		Payload:        make([]byte, 65536),
		State:          domain.StateQueued,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
	}
	if err := repo.SubmitJob(ctx, jExact); err != nil {
		t.Fatalf("payload of 65536 bytes (exact 64 KiB) should be accepted, got: %v", err)
	}

	// 65537 bytes: Above limit -> Rejected
	jAbove := &domain.Job{
		ID:             "j_payload_65537",
		TaskType:       "echo",
		Payload:        make([]byte, 65537),
		State:          domain.StateQueued,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
	}
	if err := repo.SubmitJob(ctx, jAbove); !errors.Is(err, domain.ErrPayloadTooLarge) {
		t.Fatalf("payload of 65537 bytes must be rejected with ErrPayloadTooLarge, got: %v", err)
	}

	// 2. Output limits
	att, err := repo.DispatchJob(ctx, "j_payload_65535", "w_limits")
	if err != nil {
		t.Fatalf("DispatchJob failed: %v", err)
	}

	// Output 65537 bytes: Above limit -> Rejected
	outputAbove := strings.Repeat("x", 65537)
	_, err = repo.CommitAttempt(ctx, "j_payload_65535", att.ID, att.LeaseToken, "w_limits", domain.AttemptOutcome{
		ExitCode: 0,
		Output:   outputAbove,
	})
	if !errors.Is(err, domain.ErrOutputTooLarge) {
		t.Fatalf("output of 65537 bytes must be rejected with ErrOutputTooLarge, got: %v", err)
	}

	// Output 65536 bytes: Exact limit -> Accepted
	outputExact := strings.Repeat("x", 65536)
	committedJob, err := repo.CommitAttempt(ctx, "j_payload_65535", att.ID, att.LeaseToken, "w_limits", domain.AttemptOutcome{
		ExitCode: 0,
		Output:   outputExact,
	})
	if err != nil {
		t.Fatalf("output of 65536 bytes (exact 64 KiB) should be accepted, got: %v", err)
	}
	if committedJob.Result == nil || len(committedJob.Result.Output) != 65536 {
		t.Fatalf("expected stored result output length 65536")
	}
}

// TestPostgresRepository_Security_ForgedTokenAndReplayAttacks tests that forged lease tokens,
// replayed commits, and unauthorized workers cannot breach the commit protocol.
func TestPostgresRepository_Security_ForgedTokenAndReplayAttacks(t *testing.T) {
	ctx := context.Background()
	pool, _ := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	w1 := &domain.Worker{
		ID:             "w_auth_1",
		Version:        "v1.0.0",
		TotalSlots:     1,
		Status:         domain.WorkerStatusHealthy,
		SupportedTasks: []string{"echo"},
	}
	w2 := &domain.Worker{
		ID:             "w_rogue_2",
		Version:        "v1.0.0",
		TotalSlots:     1,
		Status:         domain.WorkerStatusHealthy,
		SupportedTasks: []string{"echo"},
	}
	if err := repo.UpsertWorker(ctx, w1); err != nil {
		t.Fatalf("UpsertWorker w1 failed: %v", err)
	}
	if err := repo.UpsertWorker(ctx, w2); err != nil {
		t.Fatalf("UpsertWorker w2 failed: %v", err)
	}

	job := &domain.Job{
		ID:             "j_replay_test",
		TaskType:       "echo",
		Payload:        []byte("replay test"),
		State:          domain.StateQueued,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
	}
	if err := repo.SubmitJob(ctx, job); err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	att, err := repo.DispatchJob(ctx, "j_replay_test", "w_auth_1")
	if err != nil {
		t.Fatalf("DispatchJob failed: %v", err)
	}

	// 1. Forged lease token attack
	forgedTokens := []string{
		"forged_token_1234567890",
		att.LeaseToken + "_tampered",
		att.LeaseToken[:len(att.LeaseToken)-1] + "X",
	}
	for _, forged := range forgedTokens {
		_, err := repo.CommitAttempt(ctx, "j_replay_test", att.ID, forged, "w_auth_1", domain.AttemptOutcome{
			ExitCode: 0,
			Output:   "forged",
		})
		if !errors.Is(err, domain.ErrStaleAttempt) {
			t.Fatalf("forged token %q must be rejected with ErrStaleAttempt, got: %v", forged, err)
		}
	}

	// 2. Wrong-worker commit attack
	_, err = repo.CommitAttempt(ctx, "j_replay_test", att.ID, att.LeaseToken, "w_rogue_2", domain.AttemptOutcome{
		ExitCode: 0,
		Output:   "impersonated",
	})
	if !errors.Is(err, domain.ErrWorkerMismatch) {
		t.Fatalf("wrong worker must be rejected with ErrWorkerMismatch, got: %v", err)
	}
	var mismatchErr *domain.WorkerMismatchError
	if !errors.As(err, &mismatchErr) {
		t.Fatalf("expected error to be *domain.WorkerMismatchError, got: %T", err)
	}
	if mismatchErr.AssignedWorker != "w_auth_1" || mismatchErr.ReportingWorker != "w_rogue_2" {
		t.Fatalf("WorkerMismatchError misattributed: assigned=%s, reporting=%s", mismatchErr.AssignedWorker, mismatchErr.ReportingWorker)
	}

	// 3. Legitimate commit succeeds
	_, err = repo.CommitAttempt(ctx, "j_replay_test", att.ID, att.LeaseToken, "w_auth_1", domain.AttemptOutcome{
		ExitCode: 0,
		Output:   "authorized",
	})
	if err != nil {
		t.Fatalf("legitimate commit failed: %v", err)
	}

	// 4. Replay attack: submitting the same commit again must be rejected
	_, err = repo.CommitAttempt(ctx, "j_replay_test", att.ID, att.LeaseToken, "w_auth_1", domain.AttemptOutcome{
		ExitCode: 0,
		Output:   "replayed",
	})
	if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
		t.Fatalf("replayed commit must be rejected with ErrJobAlreadyTerminal, got: %v", err)
	}
}

// TestPostgresRepository_Security_ContextCancellationAtomicity tests that context cancellation
// mid-transaction leaves zero partial rows (atomic rollback).
func TestPostgresRepository_Security_ContextCancellationAtomicity(t *testing.T) {
	pool, schemaName := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	worker := &domain.Worker{
		ID:             "w_cancel_atomic",
		Version:        "v1.0.0",
		TotalSlots:     2,
		Status:         domain.WorkerStatusHealthy,
		SupportedTasks: []string{"echo"},
	}
	if err := repo.UpsertWorker(context.Background(), worker); err != nil {
		t.Fatalf("UpsertWorker failed: %v", err)
	}

	job := &domain.Job{
		ID:             "j_cancel_atomic",
		TaskType:       "echo",
		Payload:        []byte("test"),
		State:          domain.StateQueued,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
	}
	if err := repo.SubmitJob(context.Background(), job); err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	// Call DispatchJob with a pre-cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := repo.DispatchJob(cancelledCtx, "j_cancel_atomic", "w_cancel_atomic")
	if err == nil {
		t.Fatalf("expected error on cancelled context, got nil")
	}

	// Verify database state: no attempts or events were created
	var attemptCount int
	err = pool.QueryRow(context.Background(), fmt.Sprintf("SELECT COUNT(*) FROM %s.attempts WHERE job_id = 'j_cancel_atomic';", schemaName)).Scan(&attemptCount)
	if err != nil {
		t.Fatalf("failed to query attempts: %v", err)
	}
	if attemptCount != 0 {
		t.Fatalf("CRITICAL ATOMICITY FAILURE: cancelled transaction leaked %d attempt rows", attemptCount)
	}

	// Verify job remains in QUEUED state
	var jobState string
	err = pool.QueryRow(context.Background(), fmt.Sprintf("SELECT state FROM %s.jobs WHERE id = 'j_cancel_atomic';", schemaName)).Scan(&jobState)
	if err != nil {
		t.Fatalf("failed to query job state: %v", err)
	}
	if jobState != string(domain.StateQueued) {
		t.Fatalf("expected job to remain QUEUED, got %s", jobState)
	}
}

// TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation analyzes the AST of
// internal/storage/postgres/repository.go to ensure all SQL executions pass static parameterized
// strings and no caller input is concatenated or formatted via fmt.Sprintf or string addition.
func TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation(t *testing.T) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "repository.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse repository.go: %v", err)
	}

	sqlMethods := map[string]bool{
		"Exec":     true,
		"Query":    true,
		"QueryRow": true,
	}

	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if !sqlMethods[sel.Sel.Name] {
			return true
		}

		// In pgx, query is the second argument (ctx is first)
		if len(call.Args) < 2 {
			return true
		}

		queryArg := call.Args[1]

		// Ensure queryArg is either a string literal OR a constant/static string
		switch arg := queryArg.(type) {
		case *ast.BasicLit:
			if arg.Kind == token.STRING {
				// Pure static SQL literal (e.g. `SELECT ... WHERE id = $1`) - SAFE!
				return true
			}
		case *ast.Ident:
			// Named query string variable or constant
			return true
		case *ast.CallExpr:
			// Calling a function (like fmt.Sprintf) to construct SQL
			if fnSel, isSel := arg.Fun.(*ast.SelectorExpr); isSel {
				pkgIdent, isIdent := fnSel.X.(*ast.Ident)
				if isIdent && pkgIdent.Name == "fmt" && (fnSel.Sel.Name == "Sprintf" || fnSel.Sel.Name == "Sprint") {
					t.Fatalf("SECURITY VIOLATION: dynamic SQL query construction detected at line %d: %s.%s",
						fset.Position(arg.Pos()).Line, pkgIdent.Name, fnSel.Sel.Name)
				}
			}
		case *ast.BinaryExpr:
			// String concatenation (e.g. "SELECT ... " + id)
			if arg.Op == token.ADD {
				t.Fatalf("SECURITY VIOLATION: SQL string concatenation (+) detected at line %d",
					fset.Position(arg.Pos()).Line)
			}
		}

		return true
	})
}
