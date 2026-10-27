# MiniCloud Project State & Handoff

## 1. Current Phase & Milestone
- **Current Branch:** `docs/phase4-design`
- **Current Milestone:** Phase 4 Design Specification Revision — Leases, Bounded Retries, Stale-Attempt Rejection, Cancellation, and Automated Fault Testing (ADR-0014, ADR-0015, ADR-0016, Sub-Slices 4a–4e revised with Owner Decisions Accepted).
- **Target Release:** MiniCloud V1 (Phases 0–4 + Assignment-Based Round-Robin Slice).

---

## 2. Done
- **Two-Agent Workflow Infrastructure (P0-01, PR #16, `main` at `a9453fc`):**
  - **Single Rule File (`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`):** `AGENTS.md` is the single rule file for all coding agents; `CLAUDE.md` and `GEMINI.md` point to it.
  - **Task Board & Workflow Directories (`docs/agents/TASKS.md`, `docs/agents/handoffs/`, `docs/agents/reviews/`):** Committed agent rules and task board from owner draft with builder/auditor roles, active/done states, reserved touch points, and handoff and review folders.
  - **Per-Worktree Test Databases (`minicloud_claude_test`, `minicloud_gemini_test`):** Documented and verified per-worktree test databases in `AGENTS.md` section 9 so concurrent agent test runs avoid data collisions. Added guarded reset script refusing anything other than `minicloud_<agent>_test` and added database preflight check to session start protocol (section 7 step 0 and section 9).
  - **Citation Checker Scope (`tests/docs/citations_test.go`):** Added `AGENTS.md` to root documentation files scanned by `CollectDocFiles` in `tests/docs/citations_test.go`.
- **Phase 4 — Coordination, Leases, Retries & Cancellation Design Specification Second Revision (`docs/phase4-design`):**
  - **Symmetric Accept-Until-Swept Rule (ADR-0014, ADR-0016, `docs/protocol.md`, `docs/state-machine.md`, `docs/failure-model.md`, `docs/fault-injection-plan.md`):** Resolved renewal vs grace design contradiction. Leases grant exclusive rights until swept to `LOST`. While `attempts.state == 'RUNNING'`, both `RenewAttemptLease` and `CommitAttempt` succeed even if `lease_expires_at` has passed on database time, extending the lease from current database time (`clock_timestamp() + lease_duration`). Prevents cascading worker aborts during coordinator restarts and database outages. HTTP 409 Conflict (`LEASE_EXPIRED` / `domain.ErrLeaseExpired`) is returned only once an attempt has been swept to `LOST`. Documented trade-off (alive but delayed worker retains attempt until next sweeper pass) and added planned tests.
  - **Worker-Side Result Delivery Protocol (`docs/failure-model.md`, `docs/fault-injection-plan.md`, ADR-0014):** Designed worker retry behavior for finished but unreported tasks: capped jittered backoff (500ms base to 5.0s cap), ongoing background lease renewal during reporting, stopping on non-retryable rejection (403, 404, 409) or cumulative bound (lease + grace = 60s). Inspected existing tests pinning behavior (`internal/worker/client_test.go:16` `TestIsNonRetryableReportError`, `internal/worker/client_test.go:40` `TestAgent_ReportResult_RejectionHandling`), confirmed zero weakened assertions, submitted loop expansion in `internal/worker/client.go:324-355` as candidate for owner approval, and updated FS-05 to verify tasks completing during a 45s coordinator downtime.
  - **Sweeper SQL & Outage Grace Detection (`docs/schema.md`, ADR-0014):** Maintained canonical lock order (`jobs -> workers -> attempts`). Documented candidate scan using unlocked read and `FOR UPDATE SKIP LOCKED` on jobs and plain `FOR UPDATE` under `lock_timeout` on workers and attempts to eliminate lock contention; specified `CANCEL_REQUESTED` resolution to terminal `CANCELLED` suppressing retries; detailed lock failure fallback (skips candidate with zero mutation); and specified autonomous outage grace detection (any failed tick or database-time gap > 3x sweep interval, activating a 30s grace window capped by a 30s cooldown). Enforced config validation rule: `MINICLOUD_STARTUP_GRACE_PERIOD` and `MINICLOUD_OUTAGE_GRACE_WINDOW` must be >= lease duration.
  - **Explicit Architectural Alignments (ADR-0015, `docs/state-machine.md`):** Documented that `LOST` attempts consume `max_attempts` with `error_class = "LEASE_EXPIRED"` (alternative infinite retries rejected to avoid poison job cluster loops); cited Blueprint Section 5 justifying direct `RETRYING -> RUNNING` pull dispatch; documented in-memory repo and queue dispatch rules and shared conformance assertions; added full state mapping table with grep proof showing zero stored rows or API outputs contain old spellings; and explicitly cited cancel HTTP handler returning HTTP 200 for all valid cancellations (`internal/api/handlers.go:267`).
  - **Security Findings Register (SEC-022):** Documented finding SEC-022 (Medium, executed): duplicate `CancelJob` on `CANCEL_REQUESTED` moves job directly to `CANCELLED` via fallback `else` branch, confirmed by code reading in `internal/storage/inmemory.go:658-700` and `internal/storage/postgres/repository.go:1135-1233` and demonstrated by audit probe `TestReviewP400_SEC022_DoubleCancelLeaksWorkerSlot` (audit probe, executed on `agent/claude/P4-00-review`; regression in slice 4a), with authorized fix in Slice 4a as idempotent no-op returning HTTP 200.
    Verified by `TestConformance_CancelJob_IdempotentCancelRequested` (planned test).
- **Milestone `docs/citation-check` — Documentation Citation Verification Tooling & Rule C Hardening (`docs/citation-check`):**
  - **Automated Citation Checker (`tests/docs/citations_test.go`):** Implemented standard library static documentation citation verifier enforcing Rules A–F across `docs/` (excluding `docs/source/`), `README.md`, `GEMINI.md`, and `CHANGELOG.md`:
    - Rule A: Every cited repository path must exist on disk.
    - Rule B: Line number anchors (`path:line`, `path#Lline`) must not exceed file line counts.
    - Rule C: Test function identifiers must exist in repository test suites parsed via `go/parser` without subprocesses.
    - Rule D: Prohibits local file URI schemes (`file:` triple slash) and legacy route prefixes (`/api` preceding `/v1`).
    - Rule E: ADR citations must reference valid ADR numbers with zero gap sequence.
    - Rule F: Allowlist (`tests/docs/citation_allowlist.txt`) format, non-empty reasons (>= 15 characters), and strict non-staleness check.
  - **Rule C Exemption Tightening:** Tightened Rule C exemption evaluation to individual clauses and table cells (`[|;]|\. |\.\t|<br\s*/?>`) with identifier token stripping, ensuring separate clauses do not inherit exemptions. Verified with fixtures `TestFixtures/RuleC_SeparateClause_Fail` and `TestFixtures/RuleC_ProposedExemption_Pass`.
  - **Documentation & ADR Corrections:** Fixed `file:` URI schemes in `README.md` and migration path in `docs/adr/0010-migrations-policy.md`.
  - **Makefile Integration & Security Guidance:** Added `test-docs` target to `Makefile`, documented in `README.md`, `GEMINI.md`, and added documentation verification rules to `docs/security/security-testing.md`.
  - **Security Findings Register:** Documented `SEC-020` (Documentation Citation Drift and Nonexistent Citations) and `SEC-021` (Whole-Line Exemption Scoping in Rule C Citation Checker; verified across four executed mutation cases, with residual limitation documented for same-clause self-labeling) in `docs/security/findings.md` and `docs/security/test-plan.md`.
- **Phase 3, Slice 3d — PostgreSQL Coordinator Wiring, Durable Dispatch, Readiness Probe & Recovery (`milestone/3d-postgres-wiring`):**
  - **Durable Dispatch (`repo.DispatchNextJob`):** Single-transaction FIFO pull dispatch in PostgreSQL (`internal/storage/postgres/repository.go`) adhering to canonical deadlock-free lock order (`jobs` -> `workers` -> `attempts`). Employs unlocked worker pre-check preserving SEC-002 error precedence, candidate job selection via `SELECT ... FOR UPDATE SKIP LOCKED`, worker row locking with capacity re-check, and dynamic attempt number derivation (`COALESCE(MAX(attempt_no), 0) + 1`). Implemented in `InMemoryRepository` for shared conformance. Tested under extreme contention with `TestPostgresRepository_Concurrency_MixedOperationsStress` (`-race -count=20`) and `TestPostgresRepository_Concurrency_DispatchNextJob_StartBarrier` (`-race -count=50`).
  - **Coordinator Wiring & Queue Split:** Added `NewPostgresService` in `internal/coordinator/service.go`. In PostgreSQL mode, jobs bypass the in-memory queue entirely, preventing unbounded memory growth (SEC-010) and guaranteeing restart durability. In memory mode, the in-memory queue + `DispatchJob` path is preserved.
  - **Dynamic Boot-Time Schema Verification:** Configured `cmd/coordinator/main.go` to assert on startup that the active database schema version matches `migrator.LatestEmbeddedVersion()`, refusing to start on outdated schemas. Added `migrator.CurrentSchemaVersion`.
  - **Readiness Probe (`/readyz`) & Outage Resilience:** Updated `/readyz` endpoint in `internal/api/handlers.go` to verify repository reachability via `repo.Ping(ctx)`. Mapped connection failures and query timeouts to HTTP 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1` and opaque body. Unmapped driver errors map to opaque HTTP 500 `INTERNAL_ERROR`.
  - **Worker Client Backoff & Outage Distinction:** Updated `internal/worker/client.go` implementing exponential backoff with full jitter on 5xx responses from `/poll` and `/heartbeat`, strictly distinguishing `SERVICE_UNAVAILABLE` (transient backoff) from `WORKER_UNHEALTHY` (wait for heartbeat restoration). Tested in `internal/worker/client_test.go`.
  - **Real-Process Crash Recovery & Outage Integration Tests:** Added `tests/integration/postgres_wiring_test.go` verifying: (1) coordinator restart mid-execution accepts surviving worker attempt report with HTTP 200 COMMITTED and dispatches surviving queued jobs; (2) database container outage returns 503 with `Retry-After: 1` on `/readyz` and job submission without leaking credentials, automatically recovering when the database restarts; (3) 50-job burst across 3 workers with mid-run coordinator kill completes all 50 jobs with zero loss or duplicates.
  - **Migration `000003_worker_limits.sql` & Check Constraint Defense:** Added migration enforcing `chk_workers_total_slots_range` (1..1024), `chk_workers_version_length` (<= 64), and `chk_workers_supported_tasks_count` (<= 16). Mapped check violations and SQLSTATE `22001`/`22003` to HTTP 400 `INVALID_REQUEST`.
  - **Worker Registration Limits (SEC-015, SEC-016, SEC-017):** Bounded `total_slots` to 1..1024, `version` to <= 64 characters (empty allowed), `supported_tasks` to <= 16 unique items (duplicates deduplicated; each task name 1..64 characters of `^[a-z0-9_]{1,64}$`; unknown well-formed task names accepted for rolling fleet upgrades). Tested in `TestAPI_RegisterWorker_SlotsUpperLimit`, `TestAPI_RegisterWorker_VersionLengthLimit`, `TestAPI_RegisterWorker_SupportedTasksAllowlist`, `TestPostgresRepository_Security_WorkerCheckConstraints`, and `TestSchemaInvariants_SQLViolations`.
  - **Part D Security Test Suite (`tests/integration/part_d_security_test.go`):** Implemented and executed all Part D security tests against live PostgreSQL container:
    1. `TestSecurity_PartD_PoolExhaustion`: Genuinely saturates 10-connection pool by holding an exclusive table lock on `jobs` for 800ms (`holdDuration`) across pre-spawned barrier requests. 500 concurrent requests; process survived, all responses 2xx/4xx or opaque 503 with `Retry-After: 1`, zero 500s; primary proof verifies admin sampling of `pg_stat_activity` observed 10/10 connections in `wait_event_type = 'Lock'` (threshold >= 8); secondary proof verifies locked max latency exceeds 3-run unlocked baseline median by >= 0.25 * holdDuration (200ms); verified by adversarial mutation testing (SEC-019); immediate recovery (<2ms).
    2. `TestSecurity_PartD_SlowQuery_ContextDeadline`: Query timeout under exclusive lock contention returned HTTP 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1` and opaque body in <200ms.
    3. `TestSecurity_PartD_SQLInjection_EndToEnd`: SQL injection strings across payload, task_type, worker_id, version, supported_tasks returned 201 (stored-as-data) or 4xx; zero 5xx; zero schema objects altered.
    4. `TestSecurity_PartD_Outage_MidDispatchAndMidCommit`: Mid-run container shutdown and restore completed cleanly with worker backoff and commit; 1 attempt, 1 result, 0 duplicate commits, 0 lost jobs.
    5. `TestSecurity_PartD_LeakScan`: Scrubbed responses and logs; confirmed zero occurrences of connection strings, passwords, SQL fragments, database host/ports (`:5433`, `:5432`), or the real raw 32-byte base64url lease token issued at dispatch (`pollResult.Assignment.LeaseToken`). Verified with sub-test positive control (`PositiveControl_PlantedTokenDetected`) proving detection of planted tokens.
    6. `TestMigrator_NotSubjectToCoordinatorQueryTimeout`: Verifies that migrations executed by `cmd/migrate` / `postgres.Migrator` run on unconstrained connections (`statement_timeout = 0`, `lock_timeout = 0`) and are not cancelled by the coordinator's short query timeouts.
  - **SEC-018 Process Remediations & Reporting Protocol:** Disclosed and recorded `SEC-018` in `docs/security/findings.md` addressing reconstructed test outputs in prior reports. Mandated redirection of all test runs to `/tmp/<name>.log 2>&1` with verbatim head/tail and exact grep count verification.
  - **Documentation & Specifications:** Created `docs/security/field-limits.md` specifying all field limits and database constraints, authored `docs/adr/0013-durable-dispatch-and-coordinator-recovery.md` detailing lock ordering, approximate FIFO, pooled connection timeouts (`statement_timeout`, `lock_timeout`), and migration isolation, updated `docs/architecture.md`, and documented Phase 3 dead-worker limitation in `docs/failure-model.md`.
- **Phase 3, Slice 3d — Authorized Security Hardening (SEC-003, SEC-007, SEC-008, SEC-009, SEC-011, SEC-014) (`milestone/3d-postgres-queue`):**
  - **SEC-007 (Internal Error Sanitization & Opaque HTTP 500):** Mapped unmapped repository or driver errors to generic HTTP 500 `INTERNAL_ERROR` with opaque message (`"internal server error"`). Scrubbed connection URIs and credentials (`password=***`) in server-side logs. JSON decoding errors return fixed `"malformed JSON request body"` without leaking decoder internals or offsets. Verified by `TestAPI_UnmappedError_SimulatedDriverError_SanitizedInternalError500`, `TestAPI_OutageMapping_503RetryAfterOpaqueBody`, and `TestAPI_SubmitJob_MalformedJSON_FixedMessage` (`internal/api/api_test.go:212-321`).
  - **SEC-008 (API Boundary Identifier Validation):** Bounded worker IDs, route IDs (`{id}`, `{attempt_id}`), and commit request worker IDs to <= 128 characters matching `^[a-zA-Z0-9_-]+$`. Rejected 100 KiB IDs, control characters, newlines, and path traversal strings (`../../`) with HTTP 400 `INVALID_REQUEST`. Verified by `TestAPI_IdentifierValidation_NegativeCases` (`internal/api/api_test.go:323-412`).
  - **SEC-009 (Explicit HTTP Server ReadHeaderTimeout):** Added `DefaultReadHeaderTimeout = 2 * time.Second` to `internal/api/server.go` on `http.Server`, mitigating Slowloris resource exhaustion. Verified by `TestAPI_Slowloris_ReadHeaderTimeout` (`internal/api/api_test.go:414-473`).
  - **SEC-011 (Migration CLI Credential Exposure Deprecation):** Updated `cmd/migrate/main.go` prioritizing `MINICLOUD_DATABASE_URL` environment variable; deprecated `-database-url` CLI flag with warning regarding process table credential exposure. Verified by `TestMigrateCLI_ResolveDatabaseURL_EnvironmentVariable`, `TestMigrateCLI_ResolveDatabaseURL_DeprecatedFlagWarning`, `TestMigrateCLI_ResolveDatabaseURL_MissingURL` (`cmd/migrate/main_test.go:8-61`).
  - **SEC-014 (Numeric Parameters Boundary Validation):** Updated `SubmitJobRequest` and coordinator `SubmitJob` to validate `max_attempts` (1..20, default 3 when omitted or null) and `timeout_seconds` (1..86400, default 60 when omitted or null) with typed `domain.ErrInvalidRequest` mapped to HTTP 400. Mapped PostgreSQL check constraint violations (`chk_jobs_max_attempts` and `chk_jobs_timeout` under SQLSTATE `23514`) to `domain.ErrInvalidRequest` ensuring check constraint violations always return HTTP 400 client errors and never 500 internal errors. Verified by `TestAPI_SubmitJob_BoundaryValues`, `TestAPI_PostgresCheckConstraint_MapsToClientErrorNever500` (`internal/api/api_test.go:476-608`), and `TestPostgresRepository_Security_CheckConstraints_NumericBounds` (`internal/storage/postgres/security_test.go:809-883`).
  - **Findings Register Update:** Updated `docs/security/findings.md`, `docs/security/threat-model.md`, and `docs/security/test-plan.md` recording SEC-007, SEC-008, SEC-009, SEC-011, and SEC-014 as Fixed. Audited worker registration fields and recorded SEC-015 (unbounded worker slots), SEC-016 (unbounded worker version length), and SEC-017 (unbounded/unvalidated supported tasks) as Open.
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
  - **Security Review Test Suite:** Added `internal/storage/postgres/security_test.go` covering SQL injection, boundary limits (65535, 65536, 65537 bytes), forged tokens, replay attacks, context cancellation atomicity, and a Go AST static analysis checker (`CheckSQLSafety`). The checker inspects all `Exec`, `Query`, `QueryRow`, and `Queue` calls across all non-test `.go` files in `internal/storage/postgres` (`migrator.go`, `repository.go`), enforcing that query arguments are string literals, declared constants, or variables whose every assignment in the file is a string literal (rejecting `fmt.Sprintf`, `+` string concatenation, function calls, and function parameters; verified on positive and negative test fixtures). A single narrow allowlist is granted to `migrator.go`'s `Migrate` method executing `migration.Content`, as migration SQL is read exclusively from build-time embedded assets (`embed.FS`), verified against SHA-256 checksums, and not caller-controlled. *Limitation:* The checker operates on single-file ASTs and does not detect dynamic SQL assembled in external helper functions returned as strings.
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
- **Exit Criteria:** Zero duplicate deliveries under parallel polling; real-facts dispatch logging verified; at-least-once recovery verified; benchmark baseline ready for V2 Redis comparison. (**SATISFIED**)

### Slice 3e: Coordinator Restart Recovery & Multi-Process Tests
- **Scope:**
  - Automated integration test verifying:
    1. Coordinator restarts with `QUEUED` jobs: jobs persist in PostgreSQL and dispatch to workers upon restart.
    2. Coordinator restarts with `RUNNING` jobs: surviving worker finishes task and restarted coordinator accepts commit.
    3. Multi-worker batch execution running against PostgreSQL storage.
- **Exit Criteria:** End-to-end restart recovery verified; all unit and integration tests pass `-race`. (**SATISFIED**)

---

## 4. Proposed Phase 4 Sub-Slices & Exit Criteria (Design Specification Revision)

Detailed in [docs/phase4-plan.md](phase4-plan.md):

### Slice 4a: Grace Design, Existing-Test Impact, Domain State Machine & In-Memory Parity
- **Scope:**
  - Grace design: 30s coordinator startup grace, 30s database outage grace protecting Phase 3 surviving-worker commit recovery in `TestPostgresWiring_CoordinatorRestartRecovery` (`tests/integration/postgres_wiring_test.go:167`) and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:794`).
  - Existing-test impact: Duplicate cancel on `CANCEL_REQUESTED` becomes an idempotent no-op returning HTTP 200 with state `CANCEL_REQUESTED`. Slice 4a changes duplicate-cancel behavior (job remains in `CANCEL_REQUESTED` rather than prematurely jumping to `CANCELLED`), but changes zero HTTP status codes. Existing test `CancelJob_QueuedAndRunning` (`internal/storage/storagetest/conformance.go:299-363`, line 335) and `TestInMemoryRepository_SubmitJob_And_CancelJob_Events` (`internal/storage/inmemory_test.go:559-611`, line 607) only tested duplicate cancel on terminal `QUEUED`/`CANCELLED`. Zero existing assertions broken. Add `TestConformance_CancelJob_IdempotentCancelRequested` (planned test).
  - State naming reconciliation: canonical states `'PENDING'`, `'RUNNING'`, `'COMPLETED'`, `'FAILED'`, `'TIMED_OUT'`, `'CANCELLED'`, `'LOST'`, matching migration `000001_initial_schema.sql:97`. Map domain constants in `internal/domain/state.go:22-27` (`EXPIRED` -> `LOST`, `CANCELED` -> `CANCELLED`).
  - Domain types, transitions (`RUNNING -> RETRYING`, `CANCEL_REQUESTED -> CANCELLED`, `RETRYING -> RUNNING` via direct dispatch), typed errors (`ErrLeaseExpired`, `ErrAttemptNotRunning`, `ErrJobCancelRequested`). Success is `AttemptCompleted` (no error class); task validation errors permanent; unknown errors retryable.
  - `InMemoryRepository` parity with injectable `Clock`, `not_before` dispatch filter, leases, renewals, and sweeper.
  - Shared conformance tests in `internal/storage/storagetest` and model-based state machine property test `TestProperty_StateMachine_ModelConformance` (planned test).
- **Exit Criteria:** In-memory repository passes expanded shared conformance suite; property test generator executes >= 1,000 random event traces without invariant violations; `-race -count=1` clean. Mandatory mutation proofs for transition guards, duplicate cancel idempotency, and retry backoff.

### Slice 4b: Schema Migration 000004 & PostgreSQL Repository Parity
- **Scope:**
  - Migration `000004_leases_retries_cancellation.sql (proposed)` with forward-only schema expansion, sweeper index, retry index, check constraints.
  - PostgreSQL repository methods: `RenewLease`, `SweepExpiredLeases`, retry-aware `DispatchNextJob` with direct `RETRYING -> RUNNING` transition, atomic `CancelJob`.
  - Sweeper correctness: row-locked re-check under lock (`SELECT state, lease_expires_at FROM attempts WHERE id = $1 FOR UPDATE;` in lock order `jobs -> workers -> attempts`).
  - Late-commit Option 2: accept-until-swept while attempt is `RUNNING` in database; strictly rejected once swept to `LOST`.
  - Sweeper dual grace windows: 30s startup grace, 30s outage grace.
- **Exit Criteria:** Migration applies cleanly; PostgreSQL repository passes all shared conformance tests under `-race`; zero deadlocks under concurrent mixed operations stress test (`-race -count=20`). Mandatory mutation proofs for sweeper lock order/re-check condition, startup grace window, and late-commit acceptance.

### Slice 4c: HTTP API Protocol, Worker Lease Renewal & Coordinator Sweeper Wiring
- **Scope:**
  - Dedicated renewal endpoint `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew`.
  - Single cancellation delivery channel: renewal response returns `cancel_requested: true`. Removed `cancelled_job_ids` from heartbeat response.
  - Worker autonomous background renewal goroutine (every 10s for 30s lease). Worker receiving lease loss (403/409) aborts task via context cancellation and suppresses result reporting.
  - Coordinator background expiry sweeper ticker (every 5s, batch size 50) honoring 30s startup and outage grace.
  - Renewal flood defense: formally documented as designed, not yet implemented (R-23); planned endpoint defenses (state short-circuit, indexed lookup, statement_timeout); accepted risk under SEC-004.
  - Synchronized deployment rule: V1 coordinator and workers deploy together.
- **Exit Criteria:** Long-running jobs (>30s) maintain continuous renewals; cancellation delivered exclusively via renewal response within 10s; sweeper reclaims abandoned attempts after grace period. Mandatory mutation proofs for renewal cancellation delivery and worker abort on lease lost.

### Slice 4d: Structured Retries, Failure Classification & Cooperative Cancellation
- **Scope:**
  - 4-class failure classification (`PERMANENT`, `TRANSIENT`, `UNKNOWN`, `LEASE_EXPIRED`): success is `AttemptCompleted` (no error class); allowlisted task validation errors permanent (`ClassPermanent`); worker abnormal terminations; unknown errors retryable (`ClassTransient` / `ClassUnknown`); `LOST` attempts consume 1 attempt (`ClassLeaseExpired`).
  - Exponential backoff with full jitter ([0.5, 1.5]), base 1s, cap 30s, default 3 max attempts. `LOST` attempts consume 1 attempt.
  - Cooperative cancellation: worker receives `cancel_requested: true` via renewal, cancels execution context, preempted executor returns `context.Canceled`, worker commits with cancel status.
  - Exhaustive race resolution (cancel vs completion, cancel vs lease expiry, duplicate cancels).
- **Exit Criteria:** Transient failures retry up to `max_attempts` with backoff; permanent failures fail immediately; cancelled jobs preempt running workers within 10s; zero jobs stranded in `CANCEL_REQUESTED`. Mandatory mutation proofs for failure classification and jitter distribution.

### Slice 4e: End-to-End Fault-Injection Suite, Adversarial Testing & Phase 4 Security Review
- **Scope:**
  - Automated fault injection suite covering all 10 blueprint scenarios (FS-01 through FS-10).
  - Model-based property tests against real storage backends (>= 1,000 iterations).
  - Formal security review per ADR-0012: SEC-004 accepted risks, renewal flood boundaries, late result isolation.
- **Exit Criteria:** All 10 fault scenarios verified with concrete logs; zero invariant violations across property test transitions; full test suite passes `-race -count=1`; Phase 4 security gate certified before release tagging. Mandatory mutation proofs for stale attempt rejection and sweeper lock ordering.

---

## 5. Post-Phase 4 Hard Gate: Assignment-Based Round-Robin Slice
- **Blueprint Alignment:** Blueprint Section 6 requires scheduler-driven round-robin assignment. V1 Phase 3 implements an honest FIFO pull baseline (`fifo_pull`).
- **Hard Gate Rule:** V1 is not complete, and V2 benchmarking must not start, until scheduler-driven assignment with a deterministic round-robin policy exists.
- **Staging Justification:** Assignment expiration, dead-worker reclamation, and automatic requeue share the exact lease/requeue machinery introduced in Phase 4. Building assignment after Phase 4 avoids duplicate or disposable recovery logic. Candidate A (pre-assignment with bounded accept window) is the leading design.

---

## 6. Key Follow-Ups & Invariants

1. **Worker Re-Registration on 404:** Worker client automatically re-registers if heartbeat or poll receives `WORKER_NOT_FOUND` (**Completed in Slice 3a**).
2. **Coordinator Queue Release Test:** Coordinator verifies `Queue.Release` is explicitly called when attempt creation fails after a claim (**Completed in Slice 3a**).
3. **No Counter Drift:** Worker capacity is dynamically derived from active attempts in memory (`AvailableSlots`) and in SQL (`worker_capacity` view) rather than relying on mutable increment/decrement counters (**Completed in Slice 3a**; persisted in 3b/3c).
4. **Honest FIFO Pull & Hard Gate:** V1 Phase 3 dispatch uses honest `fifo_pull`. Dispatch logs record real facts only. See [Section 5](#5-post-phase-4-hard-gate-assignment-based-round-robin-slice) for the mandatory hard-gate rule.
5. **Phase 4 Lease Expiry & Requeue:** Unacknowledged in-flight queue items and dead workers' `RUNNING` jobs are handled in Phase 4 (leases and automated requeue).

---

## 7. Known Defects, Limitations & Open Security Findings
- **Fixed Security Findings (Slice 3d):**
  - **SEC-003 (Informational, Confirmed, Fixed):** Compose file specifies `POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}` (`deployments/docker-compose.yml:8`) with explicit `--env-file .env` passed in Makefile and test helpers, eliminating blank password startup risk.
  - **SEC-007 (Low, Fixed):** Unmapped repository and driver errors mapped to HTTP 500 `INTERNAL_ERROR` with opaque message; sanitized server logging; fixed malformed JSON message (`internal/api/handlers.go:19-75`). Tested in `TestAPI_UnmappedError_SimulatedDriverError_SanitizedInternalError500`, `TestAPI_OutageMapping_503RetryAfterOpaqueBody`, `TestAPI_SubmitJob_MalformedJSON_FixedMessage` (`internal/api/api_test.go:212-321`).
  - **SEC-008 (Low, Fixed):** Worker IDs and route parameters strictly bounded (<= 128 chars) and validated against `^[a-zA-Z0-9_-]+$` at API boundary (`internal/api/handlers.go:27-39,105-248`). Tested in `TestAPI_IdentifierValidation_NegativeCases` (`internal/api/api_test.go:323-412`).
  - **SEC-009 (Low, Fixed):** HTTP server explicit `ReadHeaderTimeout: 2s` configured (`internal/api/server.go:15,42`). Tested in `TestAPI_Slowloris_ReadHeaderTimeout` (`internal/api/api_test.go:414-473`).
  - **SEC-011 (Informational, Fixed):** Migration CLI prioritizes `MINICLOUD_DATABASE_URL` environment variable; `-database-url` flag emits deprecation warning about process table exposure (`cmd/migrate/main.go:16-33`). Tested in `TestMigrateCLI_ResolveDatabaseURL_EnvironmentVariable`, `TestMigrateCLI_ResolveDatabaseURL_DeprecatedFlagWarning`, `TestMigrateCLI_ResolveDatabaseURL_MissingURL` (`cmd/migrate/main_test.go:8-61`).
  - **SEC-014 (Medium/Low, Fixed):** `max_attempts` (1..20) and `timeout_seconds` (1..86400) validated at API/coordinator boundary; PostgreSQL check constraint violations map to HTTP 400 `INVALID_REQUEST`. Tested in `TestAPI_SubmitJob_BoundaryValues`, `TestAPI_PostgresCheckConstraint_MapsToClientErrorNever500` (`internal/api/api_test.go:476-608`), and `TestPostgresRepository_Security_CheckConstraints_NumericBounds` (`internal/storage/postgres/security_test.go:809-883`).
  - **SEC-015 (Low, Fixed):** Worker `total_slots` bounded to `[1, 1024]` at domain boundary and backed by database constraint `chk_workers_total_slots_range` (migration 000003). Tested in `TestAPI_RegisterWorker_SlotsUpperLimit` (`internal/api/api_test.go:811`) and `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:892`).
  - **SEC-016 (Low, Fixed):** Worker `version` string length capped at 64 characters at domain boundary and backed by database constraint `chk_workers_version_length` (migration 000003). Empty version allowed. Tested in `TestAPI_RegisterWorker_VersionLengthLimit` (`internal/api/api_test.go:865`) and `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:926`).
  - **SEC-017 (Low, Fixed):** Worker `supported_tasks` slice bounded to <= 16 unique items (duplicates deduplicated), individual task names bounded to 64 characters `^[a-z0-9_]{1,64}$`, backed by `chk_workers_supported_tasks_count` (migration 000003). Tested in `TestAPI_RegisterWorker_SupportedTasksAllowlist` (`internal/api/api_test.go:919`) and `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:943`).
  - **SEC-019 (Informational, Confirmed by mutation, Fixed):** Weak positive control in pool saturation test (`TestSecurity_PartD_PoolExhaustion`) replaced by dual proofs: (a) primary proof sampling `pg_stat_activity` for `wait_event_type = 'Lock'` connections >= pool_size - 2, and (b) secondary proof requiring locked max latency to exceed 3-run unlocked baseline median by >= 0.25 * holdDuration. Confirmed by adversarial mutation testing (commenting out sleep and 1ms hold both produce definitive test failures) and clean `-count=5` execution. Tested in `TestSecurity_PartD_PoolExhaustion` (`tests/integration/part_d_security_test.go:45`).
  - **SEC-020 (Low, Confirmed, Remediated by Tooling):** Prior documentation and reports cited nonexistent test names, invalid repository file paths, and obsolete API routes without automated verification. Remediated by standard-library static citation checker (`TestCitations`, `TestFixtures`) and strict allowlist (`tests/docs/citation_allowlist.txt`). Tested in `TestCitations` (`tests/docs/citations_test.go:695`) and `TestFixtures` (`tests/docs/citations_test.go:754`).
  - **SEC-021 (Informational, Confirmed by owner mutation; remediated for unrelated names; residual limitation documented):** Rule C's exemption keywords initially exempted every test name on the line. Remediated by tightening exemption evaluation to individual table cells and sentence clauses with identifier token stripping. Verified across four executed mutation cases (separate sentence fails, semicolon clause fails, separate table cell fails, same clause passes). Residual limitation documented: a fabricated name carrying its own label in the same clause passes by design; exemption is a convention requiring human review. Genuine future tests in use: `TestCoordinator_IDLeakage` (future test), `TestInMemoryRepository_MemoryExhaustion_Cap` (future test), `TestWorker_ExcessiveTimeoutClamped` (future test). Tested in `TestFixtures/RuleC_SeparateClause_Fail`, `TestFixtures/RuleC_ProposedExemption_Pass` (`tests/docs/citations_test.go:857-888`).
- **Open Security Findings (Not Authorized Yet / In Backlog):**
  - **SEC-004 (Medium, Accepted Risk until V3):** All HTTP API and worker coordination endpoints are unauthenticated; mitigated by default localhost binding (`127.0.0.1:8080`). Phase 4 renewal flood mitigation is designed (token auth, per-attempt state short-circuit, indexed lookup on primary key, statement_timeout) but unauthenticated endpoints and missing application-level rate limiting are accepted risks under SEC-004 for V1.
  - **SEC-006 (Informational, Open):** Test helper builds `CREATE SCHEMA` / `DROP SCHEMA` with `fmt.Sprintf` from in-process generated hex string (`internal/storage/postgres/testhelper_test.go:52-61`).
  - **SEC-010 (Low, Open):** Unbounded in-memory collections in `InMemoryRepository` (maps/events) and `InMemoryQueue` (slice) (`internal/storage/inmemory.go:19-25`, `internal/queue/inmemory.go:18-24`). Bypassed in production PostgreSQL mode.
  - **SEC-012 (Informational, Open):** Worker execution timeout is not clamped above from coordinator `TimeoutSeconds` (`internal/worker/client.go:267-270`).
  - **SEC-013 (Informational, Open):** Job and attempt ID format exposes sequential counter (`internal/coordinator/service.go:26`).
- **Distributed & Architectural Limitations:**
  - PostgreSQL repository and coordinator durable dispatch are fully wired (`fifo_pull`).
  - Jobs assigned to dead workers remain `RUNNING` until Phase 4 implements lease expiry and automatic requeue.
  - Allowlisted in-process tasks only (`echo`, `sleep`, `sha256_loop`) until V3 Docker containers.

---

## 8. Last Verified Commands & Actual Results
- **Date:** 2026-10-26
```bash
$ go version
go version go1.22.2 linux/amd64

$ gofmt -l .
(clean exit, code 0, empty output)

$ go vet ./...
(clean exit, code 0, empty output)

$ make test-docs
go test ./tests/docs
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	0.354s

$ go test -race -count=1 ./...
ok  	github.com/BlaZe08-Dev/Minicloud/cmd/migrate	1.055s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api	3.932s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/config	1.042s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator	1.147s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/domain	1.268s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/executor	1.091s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/queue	1.033s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/scheduler	1.033s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage	1.103s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres	1.154s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/worker	2.544s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/architecture	1.044s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	5.937s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	8.611s

$ go test -v -count=1 ./...
PASS: 77, SKIP: 26, FAIL: 0 (with MINICLOUD_TEST_DATABASE_URL unset)
```
*(Note: Database tests in `internal/storage/postgres` skip gracefully when `MINICLOUD_TEST_DATABASE_URL` is unset).*
