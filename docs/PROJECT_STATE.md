# MiniCloud Project State & Handoff

## 1. Current Phase & Milestone
- **Current Branch:** `docs/security-instructions`
- **Current Milestone:** Persistent Security Instructions & Adversarial Testing Framework — **COMPLETED**
- **Next Milestone:** Phase 3, Slice 3d — PostgreSQL Dispatch Transport (`PostgresQueue`) & FIFO Pull Dispatch.
- **Target Release:** MiniCloud V1 (Phases 0–4 + Assignment-Based Round-Robin Slice).

---

## 2. Done
- **Persistent Security, Adversarial Testing & Reliability Instructions (`docs/security-instructions`):**
  - **Operating Principles:** Created [docs/security/security-testing.md](security/security-testing.md) faithfully distilling the 5 adversarial engineering roles, 8-step break-before-fix sequence, evidence labels, honesty rules, no-silent-fix, no-weakened-tests, repeatability, and V1/V2/V3 focus.
  - **Threat Model:** Created [docs/security/threat-model.md](security/threat-model.md) analyzing assets, trust boundaries, threats, mitigations, and citations across 12 components, and incorporating unconfirmed read-only code review findings.
  - **Security Findings Register:** Created [docs/security/findings.md](security/findings.md) seeded with SEC-001 through SEC-006 using full structured format for Medium+ and summary register rows for Low/Info.
  - **Adversarial & Reliability Test Plan:** Created [docs/security/test-plan.md](security/test-plan.md) mapping V1/V2/V3 tests (fuzzing, bounds, timeouts, fault injection, concurrency races, controlled load generation) with exact existing paths or "designed, not executed".
  - **ADR 0012 Accepted:** Recorded [ADR 0012: Security, Adversarial Testing, and Reliability Workflow](adr/0012-security-and-adversarial-testing-workflow.md) establishing operational policies a–h.
  - **Session Handbook & Protocol:** Updated `GEMINI.md` embedding compact security rules, relative context links, and mandatory Security Review reporting protocol step.
  - **Phase Security Gates:** Updated `docs/roadmap.md` with explicit security review gates for V1, V2, and V3 exit criteria.
- `f5a4968` — `docs: add MiniCloud blueprint and instructions`
- `2fb7958` — `docs: establish Phase 0 architecture, state machine, and context layer`
- `eabcc22` / `78f2aa2` — `feat: implement Milestone 1 local core coordinator, worker protocol, and CLI` (Merged PR #1 to `main`).
- `a636ba0` — `feat: implement Phase 2 Slice 1 worker liveness, reaper loop, and failure recovery` (Merged PR #3 to `main`).
- `8763113` — `feat: implement dispatch-transport queue interface and delivery semantics` (Merged PR #4 to `main`).
- **Phase 3 — Durable State Design Specifications (`docs/phase3-design`):**
  - **ADR 0009 Accepted with Conditions:** [ADR 0009: PostgreSQL Persistence and Driver Selection](adr/0009-postgres-persistence-and-driver.md) selecting `jackc/pgx/v5` with `pgxpool` and pinned `postgres:16-alpine`. Conditions: pin `pgx` to version requiring Go <= 1.22 (`v5.7.1` uses `go 1.21`), enforce `GOTOOLCHAIN=local`, inspect all transitive dependencies in `go.mod`/`go.sum` against Go <= 1.22 during Slice 3c, and gate dependency addition to Slice 3c.
  - **ADR 0010 Accepted with Correction:** [ADR 0010: Database Migrations Policy and Runner Architecture](adr/0010-migrations-policy.md) establishing forward-only migrations and expand/contract rules. Correction: use `pg_advisory_xact_lock` inside migration transactions (or a dedicated connection) rather than session-level advisory locks on pooled connections.
  - **ADR 0011 Accepted by Owner:** [ADR 0011: PostgreSQL Dispatch Mechanics, Restart Recovery, and Database Invariants](adr/0011-postgres-dispatch-and-recovery.md) adopting `SELECT ... FOR UPDATE SKIP LOCKED` (Option A), dynamic worker capacity derivation in SQL (no counter drift), surviving-worker restart recovery guarantees, SHA-256 hashed lease tokens, and **Candidate C (Honest FIFO Pull Baseline, policy name `"fifo_pull"`)** for Phase 3 dispatch:
    - *Candidate C Accepted:* Dispatch is honest FIFO pull; dispatch logs record real facts only (`job_id`, `attempt_id`, `worker_id`, `policy="fifo_pull"`, `reason="claimed by polling worker with capacity"`). `internal/scheduler` is intentionally unused in the V1 dispatch loop during Phase 3 (temporary).
    - *Candidate B Rejected:* Hold window at poll time rejected due to test race flakiness, idle worker delay, and Redis Streams incompatibility.
    - *Candidate A Staged Post-Phase 4:* Pre-assignment with bounded accept window is held as the leading design for the post-Phase 4 "Assignment-Based Round-Robin" slice.
    - *Hard Gate Recorded:* See [Section 4](#4-post-phase-4-hard-gate-assignment-based-round-robin-slice) for the mandatory hard gate rule.
  - **ADR 0003 Amended:** [ADR 0003: Pull-Based Worker Protocol](adr/0003-pull-based-worker-protocol.md) updated to document owner acceptance of Candidate C (`fifo_pull`), honest dispatch logging, intentional non-use of `internal/scheduler` in V1 dispatch, and the post-Phase 4 hard gate.
  - **Database Schema Updated:** [docs/schema.md](schema.md) specifying PostgreSQL tables (`jobs`, `attempts`, `workers`, `results`, `job_events`), bounded column sizes (64 KiB), database clock authority (`CURRENT_TIMESTAMP` vs `clock_timestamp()`), and dynamic worker capacity derivation (`worker_capacity` view).
  - **Testing & Provisioning Specification:** [docs/testing-postgres.md](testing-postgres.md) documenting Docker Compose local provisioning, host confinement rules, `.env.example` secret hygiene, `_test` database safety guardrails, and planned Makefile targets.
- **Phase 3, Slice 3a — Repository Atomicity Refactor & Worker Re-Registration (`milestone/3a-repository-atomicity`):**
  - **Atomic Repository Mutations:** Introduced single-transaction-mapped methods to `storage.Repository` and `InMemoryRepository`: `SubmitJob`, `DispatchJob`, `CommitAttempt`, and `CancelJob`.
  - **Attempt Outcome Struct:** Refactored `CommitAttempt` to take `domain.AttemptOutcome` (ExitCode, Output, ErrorSummary, ErrorClassification) instead of loose parameters.
  - **Hashed Lease Tokens:** Stored `SHA-256(lease_token)` in attempts; verified incoming tokens via `crypto/subtle.ConstantTimeCompare`; raw tokens returned strictly once on dispatch.
  - **Dispatch Preconditions & Typed Errors:** Enforced all dispatch invariants inside `DispatchJob` (`ErrJobNotQueued`, `ErrWorkerNotFound`, `ErrWorkerNotHealthy`, `ErrTaskNotSupported`, `ErrWorkerNoCapacity`) with table-driven tests.
  - **Dynamic Capacity Derivation:** Derived `AvailableSlots` dynamically from active `RUNNING` attempts with zero mutable counter writes.
  - **Coordinator Claim Outcome Handling:** Handled claim outcomes deterministically ((a) not QUEUED / not found -> drop and `Ack`; (b) transient failure -> `Release`; (c) worker lacks capacity/unhealthy -> `Release` without creating an attempt).
  - **Honest FIFO Pull Logging:** Implemented honest `policy="fifo_pull"` and `reason="claimed by polling worker with capacity"` logging per ADR-0011 without calling `scheduler.SelectWorker`.
  - **Worker Auto-Re-Registration:** Added automatic re-registration in worker client upon receiving HTTP 404 `WORKER_NOT_FOUND` during heartbeat and poll.
  - **Non-Retryable Report Classification:** Classified 404 attempt report rejections as non-retryable in `IsNonRetryableReportError` to prevent infinite loops after coordinator restart without state.
  - **Coordinator Restart Integration Test:** Added `tests/integration/coordinator_restart_test.go` verifying that surviving workers auto-re-register, complete in-flight tasks without looping, and continue polling.

- **Phase 3, Slice 3b — Database Provisioning, Schema & Forward-Only Migrations (`milestone/3b-migrations-and-schema`):**
  - **Docker Compose Provisioning & Secrets Hygiene:** Added `deployments/docker-compose.yml` (`postgres:16-alpine`, port strictly on `127.0.0.1:5433`), `deployments/init-test-db.sh` for `minicloud_test`, `.env.example`, gitignored `.env`, and @-prefixed Makefile recipes (`db-up`, `db-down`, `db-psql`, `db-reset-test`, `test-db`). Guaranteed zero volume destruction (`db-down` preserves volumes; `db-reset-test` safely drops only `minicloud_test`).
  - **Secure Lease Token Generation:** Added `domain.NewLeaseToken` (32 bytes entropy via `crypto/rand`, URL-safe base64) and `domain.HashLeaseToken` (SHA-256 hex); updated in-memory storage; tested 10,000 token uniqueness and Shannon entropy >= 32 bytes; hardened concurrent in-memory dispatch/commit tests with barrier synchronization (`-race -count=200` clean).
  - **Forward-Only Migration Runner (`internal/storage/postgres`):** Implemented in-tree migration runner executing embedded `.sql` migrations; acquires `SELECT pg_advisory_xact_lock` FIRST inside the transaction to eliminate catalog races; creates `schema_migrations`; validates SHA-256 checksums to detect file tampering; executes unapplied migrations in version order within caller's `search_path`.
  - **Initial Schema (`000001_initial_schema.sql`):** Defined tables `jobs` (`BYTEA` payload with `octet_length <= 65536`), `workers`, `attempts` (`TEXT` output with `octet_length <= 65536`, `TEXT` error with `octet_length <= 10240`), `results`, and `job_events` (`JSONB` details); enforced `idx_attempts_active_per_job` partial unique index (at most one RUNNING attempt per job); created `worker_capacity` view dynamically deriving available slots.
  - **Migration CLI (`cmd/migrate`):** Added binary for running forward-only migrations from shell/scripts using `MINICLOUD_DATABASE_URL` or `-database-url`.
  - **Schema-Isolated Testing:** Tests execute in isolated `test_<random_hex>` schemas cleaned up via `t.Cleanup`; hard safety guard refuses execution against non-`_test` databases; tests verify fresh migration, idempotent re-run, checksum tampering detection (`ErrChecksumMismatch`), serialized concurrent runners (exactly one applies), and all SQL schema invariants.
  - **Approved PGX v5.7.1 Staging & Boundary Invariants:** Integrated `github.com/jackc/pgx/v5 v5.7.1` with `GOTOOLCHAIN=local`; verified `go 1.22.2` unchanged with zero toolchain directives; extended `tests/architecture/boundaries_test.go` prohibiting `pgx` in `internal/domain` and `internal/coordinator`, and prohibiting `internal/storage/postgres` in `internal/coordinator`.

- **Phase 3, Slice 3c — PostgreSQL Repository Implementation & Review Hardening (`milestone/3c-postgres-repository`):**
  - **`internal/storage/postgres.Repository`:** Implemented durable repository backed by `*pgxpool.Pool` fully satisfying `storage.Repository`.
  - **Deadlock-Free Row Lock Ordering:** Documented and enforced strict row locking order (`jobs` -> `workers` -> `attempts` -> `results` -> `job_events`) across all multi-table transactions. Empirically validated with `TestPostgresRepository_Concurrency_MixedOperationsStress` (60 goroutines, start barrier, overlapping jobs/workers, `-race -count=20`).
  - **Single-Transaction Atomic Operations:** Implemented `SubmitJob`, `DispatchJob`, `CommitAttempt`, and `CancelJob` mapping PostgreSQL SQLSTATE codes (`23505`, `23503`, `23514`) directly to typed domain errors.
  - **Constant-Time Token Verification:** Computed SHA-256 hash in Go and compared stored hash in constant time (`crypto/subtle.ConstantTimeCompare`), preserving fine-grained typed errors (`ErrAttemptNotFound`, `ErrWorkerMismatch`, `ErrStaleAttempt`, `ErrJobAlreadyTerminal`).
  - **Worker Mismatch Log Attribution:** Introduced `domain.WorkerMismatchError` carrying true `AssignedWorker` and `ReportingWorker`. Logged real assigned worker on the coordinator side while ensuring API response obscures assigned worker identity.
  - **Decoupled Conformance Helper:** Moved test helper suite to `internal/storage/storagetest` so production storage packages do not import `"testing"`.
  - **Schema Hardening Migration (`000002_schema_hardening.sql`):** Forward-only migration updating `idx_attempts_active_per_job` to cover non-terminal attempt states (`PENDING`, `RUNNING`) and replacing `worker_capacity` view to derive free slots from all active attempts. Tested both second-RUNNING and second-PENDING invariant rejections.
  - **Time & Clock Authority:** Enforced database time via `make_interval(secs => $1::double precision)` in `ReapStaleWorkers` and `CURRENT_TIMESTAMP` in `RecordHeartbeat`. Added injectable `Clock` constructor option (`WithClock`) to `InMemoryRepository` for deterministic test time advancement.
  - **Security Review Test Suite:** Added `internal/storage/postgres/security_test.go` covering SQL injection, boundary limits ($65535, 65536, 65537$ bytes), forged tokens, replay attacks, context cancellation atomicity, and a Go AST static analysis checker (`CheckSQLSafety`). The checker inspects all `Exec`, `Query`, `QueryRow`, and `Queue` calls across all non-test `.go` files in `internal/storage/postgres` (`migrator.go`, `repository.go`), enforcing that query arguments are string literals, declared constants, or variables whose every assignment in the file is a string literal (rejecting `fmt.Sprintf`, `+` string concatenation, function calls, and function parameters; verified on positive and negative test fixtures). A single narrow allowlist is granted to `migrator.go`'s `Migrate` method executing `migration.Content`, as migration SQL is read exclusively from build-time embedded assets (`embed.FS`), verified against SHA-256 checksums, and not caller-controlled. *Limitation:* The checker operates on single-file ASTs and does not detect dynamic SQL assembled in external helper functions returned as strings.
  - **Postgres Concurrency Tests:** Added `internal/storage/postgres/concurrency_test.go` with 50-goroutine start-barrier tests for same-job dispatch races, 1-slot worker capacity races, and duplicate commit races (`-race -count=50` clean), plus 60-goroutine mixed operations stress test (`-race -count=20` clean).
  - **Architecture Boundary Rules:** Updated `tests/architecture/boundaries_test.go` asserting that `internal/domain`, `internal/coordinator`, and `internal/queue` never import `pgx` or `internal/storage/postgres`; verified failure on deliberate violation.
  - **Dependency Verification:** Verified `pgxpool` dependency (`puddle/v2 v2.2.2`, `x/sync v0.8.0`) under `GOTOOLCHAIN=local go mod tidy` preserving `go 1.22.2` with zero toolchain directives; documented evidence in `docs/adr/0009-postgres-persistence-and-driver.md`.

---

## 3. Approved Phase 3 Sub-Slices & Exit Criteria

### Slice 3a: Repository Atomicity Refactor, Worker Auto-Re-Registration & In-Memory Baseline
- **Scope:**
  - Reshape repository methods so the PostgreSQL implementation can run each as ONE transaction (`DispatchJob`, `CommitAttempt`, `SubmitJob`, `CancelJob`).
  - Implement atomic methods in `InMemoryRepository`.
  - Add worker client auto-re-registration on HTTP 404 `WORKER_NOT_FOUND`.
  - Add coordinator-level test asserting `Queue.Release` is invoked on claim failure.
- **Exit Criteria:** Zero non-atomic mutation gaps in coordinator; worker automatically heals on 404 `WORKER_NOT_FOUND`; `Queue.Release` verified; all tests pass `-race`. (**SATISFIED**)

### Slice 3b: Database Provisioning, Schema & Forward-Only Migrations
- **Scope:**
  - Create `deployments/docker-compose.yml`, `init-test-db.sh`, `.env.example`, and update `.gitignore`.
  - Author initial embedded migration: `internal/storage/postgres/migrations/000001_initial_schema.sql` incorporating dynamic capacity view `worker_capacity` and timestamp semantics.
  - Implement minimal in-tree migration runner with `schema_migrations`, transaction-level advisory locks (`pg_advisory_xact_lock`), and SHA-256 checksum verification.
  - Add Makefile targets (`db-up`, `db-down`, `db-psql`, `db-reset-test`, `test-db`).
- **Exit Criteria:** Migration runner successfully applies schema to `minicloud` and `minicloud_test`; concurrent runners serialized by transaction advisory lock; checksum tampering detected; schema invariants verified; boundaries protected. (**SATISFIED**)

### Slice 3c: PostgreSQL Repository Implementation
- **Scope:**
  - Add `jackc/pgx/v5 v5.7.1` (`go 1.21 <= 1.22`) with `GOTOOLCHAIN=local` guardrail; verify all transitive dependencies in `go.mod`/`go.sum` against Go <= 1.22.
  - Implement `internal/storage/postgres.Repository` using `pgxpool.Pool`.
  - Enforce SQL invariants: partial unique index `idx_attempts_active_per_job`, monotonic attempt sequence, dynamic capacity derivation (no counter drift), and SHA-256 lease token hashing.
  - Add unit/integration tests running against `minicloud_test` with mandatory `_test` database name check.
- **Exit Criteria:** `internal/storage/postgres` satisfies all repository tests with race detector enabled. (**SATISFIED**)

### Slice 3d: PostgreSQL Dispatch Transport (`PostgresQueue`) & FIFO Pull Dispatch
- **Scope:**
  - Implement `internal/queue.Queue` backed by PostgreSQL transactional `SELECT ... FOR UPDATE SKIP LOCKED`.
  - Polling workers claim available queued work matching their capability slots using honest FIFO pull (`fifo_pull`).
  - Dispatch logs record real facts only: `job_id`, `attempt_id`, `worker_id`, `policy="fifo_pull"`, `reason="claimed by polling worker with capacity"`.
  - `internal/scheduler` is intentionally not called in the V1 dispatch loop during Phase 3 (temporary until assignment slice).
- **Exit Criteria:** Zero duplicate deliveries under parallel polling; real-facts dispatch logging verified; at-least-once recovery verified; benchmark baseline ready for V2 Redis comparison.

### Slice 3e: Coordinator Restart Recovery & Multi-Process Tests
- **Scope:**
  - Automated integration test verifying:
    1. Coordinator restarts with `QUEUED` jobs: jobs persist in PostgreSQL and dispatch to workers upon restart.
    2. Coordinator restarts with `RUNNING` jobs: surviving worker finishes task and restarted coordinator accepts commit.
    3. Multi-worker batch execution running against PostgreSQL storage.
- **Exit Criteria:** End-to-end restart recovery verified; all unit and integration tests pass `-race`.

---

## 4. Post-Phase 4 Hard Gate: Assignment-Based Round-Robin Slice
- **Blueprint Alignment:** Blueprint Section 6 requires scheduler-driven round-robin assignment. V1 Phase 3 implements an honest FIFO pull baseline (`fifo_pull`).
- **Hard Gate Rule:** V1 is not complete, and V2 benchmarking must not start, until scheduler-driven assignment with a deterministic round-robin policy exists.
- **Staging Justification:** Assignment expiration, dead-worker reclamation, and automatic requeue share the exact lease/requeue machinery introduced in Phase 4. Building assignment after Phase 4 avoids duplicate or disposable recovery logic. Candidate A (pre-assignment with bounded accept window) is the leading design.

---

## 5. Key Follow-Ups & Invariants

1. **Worker Re-Registration on 404:** Worker client automatically re-registers if heartbeat or poll receives `WORKER_NOT_FOUND` (**Completed in Slice 3a**).
2. **Coordinator Queue Release Test:** Coordinator verifies `Queue.Release` is explicitly called when attempt creation fails after a claim (**Completed in Slice 3a**).
3. **No Counter Drift:** Worker capacity is dynamically derived from active attempts in memory (`AvailableSlots`) and in SQL (`worker_capacity` view) rather than relying on mutable increment/decrement counters (**Completed in Slice 3a**; persisted in 3b/3c).
4. **Honest FIFO Pull & Hard Gate:** V1 Phase 3 dispatch uses honest `fifo_pull`. Dispatch logs record real facts only. See [Section 4](#4-post-phase-4-hard-gate-assignment-based-round-robin-slice) for the mandatory hard-gate rule.
5. **Phase 4 Lease Expiry & Requeue:** Unacknowledged in-flight queue items and dead workers' `RUNNING` jobs are handled in Phase 4 (leases and automated requeue).

---

## 6. Known Defects, Limitations & Open Security Findings
- **Open Security Findings:**
  - **SEC-003 (Informational, Open):** Compose file specifies `POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}` without fail-fast form `${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}` (`deployments/docker-compose.yml:8`). Note: `docs/testing-postgres.md` previously showed a stale snippet with fallback credentials, since corrected.
  - **SEC-004 (Medium, Accepted Risk until V3):** All HTTP API and worker coordination endpoints are unauthenticated; mitigated by default localhost binding (`127.0.0.1:8080`).
  - **SEC-006 (Informational, Open):** Test helper builds `CREATE SCHEMA` / `DROP SCHEMA` with `fmt.Sprintf` from in-process generated hex string (`internal/storage/postgres/testhelper_test.go:52-61`).
  - **SEC-007 (Low, Open):** Unmapped errors fall through to HTTP 400 with `err.Error()` in API error mapper (`internal/api/handlers.go:68,104`).
  - **SEC-008 (Low, Open):** Worker IDs and route parameters lack length and character set validation (`internal/coordinator/service.go:198`, `internal/api/handlers.go:182`).
  - **SEC-009 (Low, Open):** HTTP server `ReadHeaderTimeout` unset (`internal/api/server.go:42`).
  - **SEC-010 (Low, Open):** Unbounded in-memory collections in `InMemoryRepository` (maps/events) and `InMemoryQueue` (slice) (`internal/storage/inmemory.go:19-25`, `internal/queue/inmemory.go:18-24`).
  - **SEC-011 (Informational, Open):** Migration CLI accepts `-database-url` as command-line flag visible in process table (`cmd/migrate/main.go:18-27`).
  - **SEC-012 (Informational, Open):** Worker execution timeout is not clamped above from coordinator `TimeoutSeconds` (`internal/worker/client.go:267-270`).
  - **SEC-013 (Informational, Open):** Job and attempt ID format exposes sequential counter (`internal/coordinator/service.go:26`).
- **Distributed & Architectural Limitations:**
  - PostgreSQL repository is implemented (Slice 3c), but coordinator queue wiring to PostgreSQL (`PostgresQueue`) occurs in Slice 3d.
  - Jobs assigned to dead workers remain `RUNNING` until Phase 4 implements lease expiry and automatic requeue.
  - Allowlisted in-process tasks only (`echo`, `sleep`, `sha256_loop`) until V3 Docker containers.

---

## 7. Last Verified Commands & Actual Results
- **Date:** 2026-10-25
```bash
$ go version
go version go1.22.2 linux/amd64

$ gofmt -s -w .
(clean exit, code 0)

$ go vet ./...
(clean exit, code 0)

$ go build ./...
(clean exit, code 0)

$ go test -count=1 ./...
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api	0.016s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/config	0.007s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator	0.012s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/domain	0.007s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/executor	0.012s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/queue	0.013s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/scheduler	0.007s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage	0.012s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres	0.015s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/worker	0.008s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/architecture	0.009s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	7.057s
```
*(Note: Database tests in `internal/storage/postgres` skip gracefully when `MINICLOUD_TEST_DATABASE_URL` is unset, as designed for docs-only sessions).*
