package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// CheckSQLSafety analyzes a Go source file AST for SQL injection and dynamic string formatting vulnerabilities.
// It inspects all calls to Exec, Query, QueryRow, and Queue, verifying that the query argument is:
// 1. A string literal (*ast.BasicLit with token.STRING), OR
// 2. A declared const with a string literal value, OR
// 3. A variable whose every assignment in the file is a string literal.
//
// Any call where the query is constructed from fmt.Sprintf, string concatenation (+),
// a function call, or passed through as a function parameter is flagged as a security violation.
//
// Documented Allowlist:
// The single allowlisted call site is in migrator.go within (*Migrator).Migrate executing migration.Content.
// Rationale: Migration SQL is read exclusively from build-time embedded assets (embed.FS defaultMigrationFS),
// verified against SHA-256 checksums in schema_migrations, and is never constructed from or influenced by
// user or network input.
func CheckSQLSafety(filename string, src any) []string {
	fset := token.NewFileSet()
	var (
		node *ast.File
		err  error
	)
	if src == nil {
		node, err = parser.ParseFile(fset, filename, nil, parser.ParseComments)
	} else {
		node, err = parser.ParseFile(fset, filename, src, parser.ParseComments)
	}
	if err != nil {
		return []string{fmt.Sprintf("failed to parse %s: %v", filename, err)}
	}

	// Step 1: Collect declarations and assignments across the file
	safeConsts := make(map[string]bool)
	fileParams := make(map[string]bool)
	safeVarAssignments := make(map[string]int)
	unsafeVarAssignments := make(map[string]int)

	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.GenDecl:
			if x.Tok == token.CONST {
				for _, spec := range x.Specs {
					vspec, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vspec.Names {
						if len(vspec.Values) > i {
							if lit, isLit := vspec.Values[i].(*ast.BasicLit); isLit && lit.Kind == token.STRING {
								safeConsts[name.Name] = true
							}
						}
					}
				}
			} else if x.Tok == token.VAR {
				for _, spec := range x.Specs {
					vspec, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vspec.Names {
						if len(vspec.Values) > i {
							if lit, isLit := vspec.Values[i].(*ast.BasicLit); isLit && lit.Kind == token.STRING {
								safeVarAssignments[name.Name]++
							} else {
								unsafeVarAssignments[name.Name]++
							}
						}
					}
				}
			}
		case *ast.FuncDecl:
			if x.Type != nil && x.Type.Params != nil {
				for _, field := range x.Type.Params.List {
					for _, name := range field.Names {
						fileParams[name.Name] = true
					}
				}
			}
		case *ast.FuncLit:
			if x.Type != nil && x.Type.Params != nil {
				for _, field := range x.Type.Params.List {
					for _, name := range field.Names {
						fileParams[name.Name] = true
					}
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if len(x.Rhs) > i {
					rhs := x.Rhs[i]
					if lit, isLit := rhs.(*ast.BasicLit); isLit && lit.Kind == token.STRING {
						safeVarAssignments[ident.Name]++
					} else {
						unsafeVarAssignments[ident.Name]++
					}
				} else {
					unsafeVarAssignments[ident.Name]++
				}
			}
		}
		return true
	})

	// Step 2: Inspect all SQL method invocations
	sqlMethods := map[string]bool{
		"Exec":     true,
		"Query":    true,
		"QueryRow": true,
		"Queue":    true,
	}

	var violations []string
	var currentFunc string

	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			currentFunc = x.Name.Name
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			methodName := sel.Sel.Name
			if !sqlMethods[methodName] {
				return true
			}

			// Determine query argument
			var queryArg ast.Expr
			if methodName == "Queue" {
				if len(x.Args) < 1 {
					pos := fset.Position(x.Pos())
					violations = append(violations, fmt.Sprintf("%s:%d: Queue call has no arguments", filename, pos.Line))
					return true
				}
				queryArg = x.Args[0]
			} else {
				if len(x.Args) < 2 {
					if len(x.Args) == 1 {
						queryArg = x.Args[0]
					} else {
						pos := fset.Position(x.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d: %s call has no arguments", filename, pos.Line, methodName))
						return true
					}
				} else {
					queryArg = x.Args[1]
				}
			}

			pos := fset.Position(queryArg.Pos())

			// Check narrow allowlist: migrator.go in (*Migrator).Migrate executing migration.Content
			if filepath.Base(filename) == "migrator.go" && currentFunc == "Migrate" {
				if selExpr, isSel := queryArg.(*ast.SelectorExpr); isSel {
					if xIdent, isIdent := selExpr.X.(*ast.Ident); isIdent && xIdent.Name == "migration" && selExpr.Sel.Name == "Content" {
						// Documented Allowlist:
						// migration.Content is read from build-time embedded files (embed.FS defaultMigrationFS),
						// verified against SHA-256 checksums in schema_migrations, and never constructed from caller data.
						return true
					}
				}
			}

			// Validate query argument safety
			switch arg := queryArg.(type) {
			case *ast.BasicLit:
				if arg.Kind == token.STRING {
					// Static string literal: SAFE
					return true
				}
				violations = append(violations, fmt.Sprintf("%s:%d: %s query argument is non-string basic literal (%v)",
					filename, pos.Line, methodName, arg.Kind))
			case *ast.Ident:
				name := arg.Name
				if safeConsts[name] {
					// Declared const with string literal: SAFE
					return true
				}
				if fileParams[name] {
					violations = append(violations, fmt.Sprintf("%s:%d: %s query argument %q is a function parameter",
						filename, pos.Line, methodName, name))
					return true
				}
				if unsafeVarAssignments[name] > 0 {
					violations = append(violations, fmt.Sprintf("%s:%d: %s query argument %q has unsafe assignment(s)",
						filename, pos.Line, methodName, name))
					return true
				}
				if safeVarAssignments[name] == 0 {
					violations = append(violations, fmt.Sprintf("%s:%d: %s query argument %q is neither a string literal, const, nor literal-assigned variable",
						filename, pos.Line, methodName, name))
					return true
				}
				// Variable whose every assignment in the file is a string literal: SAFE
				return true
			case *ast.CallExpr:
				violations = append(violations, fmt.Sprintf("%s:%d: %s query argument is constructed via dynamic function call",
					filename, pos.Line, methodName))
			case *ast.BinaryExpr:
				if arg.Op == token.ADD {
					violations = append(violations, fmt.Sprintf("%s:%d: %s query argument uses string concatenation (+)",
						filename, pos.Line, methodName))
				} else {
					violations = append(violations, fmt.Sprintf("%s:%d: %s query argument uses binary expression (%s)",
						filename, pos.Line, methodName, arg.Op))
				}
			default:
				violations = append(violations, fmt.Sprintf("%s:%d: %s query argument expression of type %T is unsafe",
					filename, pos.Line, methodName, queryArg))
			}
		}
		return true
	})

	return violations
}

// TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation runs CheckSQLSafety
// against test fixtures (negative and positive) and every non-test .go file in internal/storage/postgres.
func TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation(t *testing.T) {
	// ==================== Positive Fixtures (Must Pass with 0 Violations) ====================
	positiveFixtures := map[string]string{
		"good_literal": `package testpkg
import (
	"context"
	"github.com/jackc/pgx/v5"
)
func GoodLiteral(ctx context.Context, conn *pgx.Conn, id string) {
	conn.Exec(ctx, "SELECT * FROM jobs WHERE id = $1", id)
}
`,
		"good_const": `package testpkg
import (
	"context"
	"github.com/jackc/pgx/v5"
)
const selectJobs = "SELECT * FROM jobs WHERE id = $1"
func GoodConst(ctx context.Context, conn *pgx.Conn, id string) {
	conn.Exec(ctx, selectJobs, id)
}
`,
		"good_var": `package testpkg
import (
	"context"
	"github.com/jackc/pgx/v5"
)
func GoodVar(ctx context.Context, conn *pgx.Conn, id string) {
	query := "SELECT * FROM jobs WHERE id = $1"
	conn.Exec(ctx, query, id)
}
`,
		"good_batch_queue": `package testpkg
import (
	"github.com/jackc/pgx/v5"
)
func GoodBatch(b *pgx.Batch, id string) {
	b.Queue("INSERT INTO jobs (id) VALUES ($1)", id)
}
`,
		"good_migrator_allowlist": `package postgres
import (
	"context"
	"github.com/jackc/pgx/v5"
)
type Migration struct {
	Content string
}
type Migrator struct{}
func (m *Migrator) Migrate(ctx context.Context, tx pgx.Tx) error {
	var migration Migration
	_, err := tx.Exec(ctx, migration.Content)
	return err
}
`,
	}

	for name, src := range positiveFixtures {
		filename := "fixture.go"
		if name == "good_migrator_allowlist" {
			filename = "migrator.go"
		}
		violations := CheckSQLSafety(filename, src)
		if len(violations) > 0 {
			t.Fatalf("positive fixture %q failed with unexpected violations: %v", name, violations)
		}
		t.Logf("PASS: positive fixture %q verified (0 violations)", name)
	}

	// ==================== Negative Fixtures (Must Flag at Least 1 Violation) ====================
	negativeFixtures := map[string]struct {
		filename    string
		src         string
		expectedSub string
	}{
		"bad_sprintf_variable": {
			filename: "fixture.go",
			src: `package testpkg
import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)
func BadSprintfVar(ctx context.Context, conn *pgx.Conn, id string) {
	query := fmt.Sprintf("SELECT * FROM jobs WHERE id = '%s'", id)
	conn.Exec(ctx, query)
}
`,
			expectedSub: `has unsafe assignment(s)`,
		},
		"bad_direct_sprintf": {
			filename: "fixture.go",
			src: `package testpkg
import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)
func BadDirectSprintf(ctx context.Context, conn *pgx.Conn, id string) {
	conn.Exec(ctx, fmt.Sprintf("SELECT * FROM jobs WHERE id = '%s'", id))
}
`,
			expectedSub: `constructed via dynamic function call`,
		},
		"bad_concat_direct": {
			filename: "fixture.go",
			src: `package testpkg
import (
	"context"
	"github.com/jackc/pgx/v5"
)
func BadConcatDirect(ctx context.Context, conn *pgx.Conn, id string) {
	conn.Exec(ctx, "SELECT * FROM jobs WHERE id = " + id)
}
`,
			expectedSub: `uses string concatenation (+)`,
		},
		"bad_concat_var": {
			filename: "fixture.go",
			src: `package testpkg
import (
	"context"
	"github.com/jackc/pgx/v5"
)
func BadConcatVar(ctx context.Context, conn *pgx.Conn, id string) {
	query := "SELECT * FROM jobs WHERE id = " + id
	conn.Exec(ctx, query)
}
`,
			expectedSub: `has unsafe assignment(s)`,
		},
		"bad_param_passthrough": {
			filename: "fixture.go",
			src: `package testpkg
import (
	"context"
	"github.com/jackc/pgx/v5"
)
func BadParamThrough(ctx context.Context, conn *pgx.Conn, query string) {
	conn.Exec(ctx, query)
}
`,
			expectedSub: `is a function parameter`,
		},
		"bad_batch_queue_sprintf": {
			filename: "fixture.go",
			src: `package testpkg
import (
	"fmt"
	"github.com/jackc/pgx/v5"
)
func BadBatch(b *pgx.Batch, id string) {
	b.Queue(fmt.Sprintf("DELETE FROM jobs WHERE id = '%s'", id))
}
`,
			expectedSub: `constructed via dynamic function call`,
		},
		"bad_migrator_call_site": {
			filename: "migrator.go",
			src: `package postgres
import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)
type Migrator struct{}
func (m *Migrator) Migrate(ctx context.Context, tx pgx.Tx, userSQL string) error {
	_, err := tx.Exec(ctx, fmt.Sprintf("CREATE TABLE %s;", userSQL))
	return err
}
`,
			expectedSub: `constructed via dynamic function call`,
		},
		"bad_repository_using_allowlist_outside_migrator": {
			filename: "repository.go",
			src: `package postgres
import (
	"context"
	"github.com/jackc/pgx/v5"
)
type Migration struct {
	Content string
}
func Sneaky(ctx context.Context, tx pgx.Tx, m Migration) error {
	_, err := tx.Exec(ctx, m.Content)
	return err
}
`,
			expectedSub: `expression of type *ast.SelectorExpr is unsafe`,
		},
	}

	for name, tc := range negativeFixtures {
		violations := CheckSQLSafety(tc.filename, tc.src)
		if len(violations) == 0 {
			t.Fatalf("CRITICAL SECURITY FAILURE: negative fixture %q was NOT flagged by checker", name)
		}
		foundExpected := false
		for _, v := range violations {
			if strings.Contains(v, tc.expectedSub) {
				foundExpected = true
				break
			}
		}
		if !foundExpected {
			t.Fatalf("negative fixture %q was flagged, but did not match expected description %q: %v",
				name, tc.expectedSub, violations)
		}
		t.Logf("PASS: negative fixture %q successfully flagged: %s", name, violations[0])
	}

	// ==================== Production Files in internal/storage/postgres ====================
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read internal/storage/postgres directory: %v", err)
	}

	checkedCount := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		violations := CheckSQLSafety(entry.Name(), nil)
		if len(violations) > 0 {
			t.Errorf("SECURITY VIOLATION: unsafe SQL construction detected in %s:\n%s",
				entry.Name(), strings.Join(violations, "\n"))
		} else {
			t.Logf("PASS: verified SQL safety for production file: %s (0 violations)", entry.Name())
		}
		checkedCount++
	}

	if checkedCount == 0 {
		t.Fatalf("sanity check failed: no production .go files were found in internal/storage/postgres")
	}
}

// TestPostgresRepository_Security_CheckConstraints_NumericBounds verifies SEC-014:
// bypassing coordinator validation and inserting out-of-bounds max_attempts (< 1 or > 20)
// or timeout_seconds (< 1 or > 86400) directly into PostgreSQL triggers check constraint violations (SQLSTATE 23514)
// that map directly to typed domain.ErrInvalidRequest errors rather than unmapped 500 driver errors.
func TestPostgresRepository_Security_CheckConstraints_NumericBounds(t *testing.T) {
	ctx := context.Background()
	pool, _ := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	testCases := []struct {
		name        string
		maxAttempts int
		timeoutSec  int
		expectedMsg string
	}{
		{
			name:        "max_attempts_zero",
			maxAttempts: 0,
			timeoutSec:  60,
			expectedMsg: "max_attempts must be between 1 and 20",
		},
		{
			name:        "max_attempts_negative",
			maxAttempts: -5,
			timeoutSec:  60,
			expectedMsg: "max_attempts must be between 1 and 20",
		},
		{
			name:        "max_attempts_exceeds_20",
			maxAttempts: 21,
			timeoutSec:  60,
			expectedMsg: "max_attempts must be between 1 and 20",
		},
		{
			name:        "timeout_seconds_zero",
			maxAttempts: 3,
			timeoutSec:  0,
			expectedMsg: "timeout_seconds must be between 1 and 86400",
		},
		{
			name:        "timeout_seconds_negative",
			maxAttempts: 3,
			timeoutSec:  -10,
			expectedMsg: "timeout_seconds must be between 1 and 86400",
		},
		{
			name:        "timeout_seconds_exceeds_86400",
			maxAttempts: 3,
			timeoutSec:  86401,
			expectedMsg: "timeout_seconds must be between 1 and 86400",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jobID := fmt.Sprintf("job_chk_%d", time.Now().UnixNano())
			job := &domain.Job{
				ID:             jobID,
				TaskType:       "echo",
				Payload:        []byte(`{}`),
				State:          domain.StateSubmitted,
				MaxAttempts:    tc.maxAttempts,
				TimeoutSeconds: tc.timeoutSec,
			}
			err := repo.SubmitJob(ctx, job)
			if err == nil {
				t.Fatalf("expected check violation error for %s, got nil", tc.name)
			}
			if !errors.Is(err, domain.ErrInvalidRequest) {
				t.Fatalf("expected domain.ErrInvalidRequest, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.expectedMsg) {
				t.Fatalf("expected error message to contain %q, got: %v", tc.expectedMsg, err)
			}
		})
	}
}

// TestPostgresRepository_Security_WorkerCheckConstraints verifies SEC-015, SEC-016, SEC-017:
// inserting out-of-bounds worker fields directly into PostgreSQL triggers check constraint violations (SQLSTATE 23514)
// or data truncation errors (SQLSTATE 22001) that map directly to typed domain.ErrInvalidRequest errors.
func TestPostgresRepository_Security_WorkerCheckConstraints(t *testing.T) {
	ctx := context.Background()
	pool, _ := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	t.Run("total_slots_zero_rejected", func(t *testing.T) {
		w := &domain.Worker{
			ID:             fmt.Sprintf("w_chk_%d", time.Now().UnixNano()),
			Version:        "v0.1.0",
			TotalSlots:     0,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		err := repo.UpsertWorker(ctx, w)
		if err == nil {
			t.Fatalf("expected error for total_slots=0, got nil")
		}
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("expected ErrInvalidRequest, got: %v", err)
		}
	})

	t.Run("total_slots_1025_rejected", func(t *testing.T) {
		w := &domain.Worker{
			ID:             fmt.Sprintf("w_chk_%d", time.Now().UnixNano()),
			Version:        "v0.1.0",
			TotalSlots:     1025,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		err := repo.UpsertWorker(ctx, w)
		if err == nil {
			t.Fatalf("expected error for total_slots=1025, got nil")
		}
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("expected ErrInvalidRequest, got: %v", err)
		}
	})

	t.Run("version_65_chars_rejected", func(t *testing.T) {
		w := &domain.Worker{
			ID:             fmt.Sprintf("w_chk_%d", time.Now().UnixNano()),
			Version:        strings.Repeat("v", 65),
			TotalSlots:     2,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		err := repo.UpsertWorker(ctx, w)
		if err == nil {
			t.Fatalf("expected error for version length > 64, got nil")
		}
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("expected ErrInvalidRequest, got: %v", err)
		}
	})

	t.Run("supported_tasks_17_items_rejected", func(t *testing.T) {
		tasks := make([]string, 17)
		for i := range tasks {
			tasks[i] = fmt.Sprintf("t%d", i)
		}
		w := &domain.Worker{
			ID:             fmt.Sprintf("w_chk_%d", time.Now().UnixNano()),
			Version:        "v0.1.0",
			TotalSlots:     2,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: tasks,
		}
		err := repo.UpsertWorker(ctx, w)
		if err == nil {
			t.Fatalf("expected error for supported_tasks > 16, got nil")
		}
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("expected ErrInvalidRequest, got: %v", err)
		}
	})
}
