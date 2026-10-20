# MiniCloud Project State & Handoff

## 1. Current Phase & Milestone
- **Current Branch:** `docs/phase3-design`
- **Current Milestone:** Phase 3 (Durable State) — Architecture & Design Specification — **OWNER REVIEW COMPLETED (Driver, Migrations, Option A Dispatch & Dynamic Capacity Accepted; Scheduler Participation Architecture PROPOSED)**
- **Next Milestone:** Phase 3, Slice 3a — In-Memory Repository Atomicity Refactor & Worker Re-Registration.
- **Target Release:** MiniCloud V1 (Phases 0–4).

---

## 2. Done
- `f5a4968` — `docs: add MiniCloud blueprint and instructions`
- `2fb7958` — `docs: establish Phase 0 architecture, state machine, and context layer`
- `eabcc22` / `78f2aa2` — `feat: implement Milestone 1 local core coordinator, worker protocol, and CLI` (Merged PR #1 to `main`).
- `a636ba0` — `feat: implement Phase 2 Slice 1 worker liveness, reaper loop, and failure recovery` (Merged PR #3 to `main`).
- `8763113` — `feat: implement dispatch-transport queue interface and delivery semantics` (Merged PR #4 to `main`).
- **Phase 3 — Durable State Design Specifications (`docs/phase3-design`):**
  - **ADR 0009 Accepted with Conditions:** [ADR 0009: PostgreSQL Persistence and Driver Selection](file:///home/blazeee/MiniCloud/docs/adr/0009-postgres-persistence-and-driver.md) selecting `jackc/pgx/v5` with `pgxpool` and pinned `postgres:16-alpine`. Conditions: pin `pgx` to version requiring Go <= 1.22 (`v5.7.1` uses `go 1.21`), enforce `GOTOOLCHAIN=local`, inspect all transitive dependencies in `go.mod`/`go.sum` against Go <= 1.22 during Slice 3c, and gate dependency addition to Slice 3c.
  - **ADR 0010 Accepted with Correction:** [ADR 0010: Database Migrations Policy and Runner Architecture](file:///home/blazeee/MiniCloud/docs/adr/0010-migrations-policy.md) establishing forward-only migrations and expand/contract rules. Correction: use `pg_advisory_xact_lock` inside migration transactions (or a dedicated connection) rather than session-level advisory locks on pooled connections.
  - **ADR 0011 Partially Accepted (Scheduler Choice PROPOSED):** [ADR 0011: PostgreSQL Dispatch Mechanics, Restart Recovery, and Database Invariants](file:///home/blazeee/MiniCloud/docs/adr/0011-postgres-dispatch-and-recovery.md) adopting `SELECT ... FOR UPDATE SKIP LOCKED` (Option A), dynamic worker capacity derivation in SQL (no counter drift), surviving-worker restart recovery guarantees, and SHA-256 hashed lease tokens. Re-analyzed scheduler participation against blueprint Section 4 across three candidates:
    - *Candidate A:* Scheduler pre-assigns worker with bounded accept window; concrete failure analysis for dead/slow/unhealthy workers; schema fields `assigned_worker_id`, `assigned_at`, `assignment_expires_at`.
    - *Candidate B:* Hold window at poll time (Model 4); documented severe operational friction with `SKIP LOCKED`, idle worker starvation, test determinism flakes under `go test -race`, and Redis Streams consumer groups in V2. (Rejected).
    - *Candidate C (Recommended):* Honest FIFO pull baseline (`fifo_pull`); zero fictitious round-robin claims; preserves benchmark integrity against V2 Redis Streams (ADR-0006); structured decision logging on every poll.
    - *Status:* Candidate A vs Candidate C is **PROPOSED** for owner selection prior to Slice 3d.
  - **ADR 0003 Amended:** [ADR 0003: Pull-Based Worker Protocol](file:///home/blazeee/MiniCloud/docs/adr/0003-pull-based-worker-protocol.md) updated to document Candidates A, B, and C for scheduler participation under pull-based coordination.
  - **Database Schema Updated:** [docs/schema.md](file:///home/blazeee/MiniCloud/docs/schema.md) specifying PostgreSQL tables (`jobs`, `attempts`, `workers`, `results`, `job_events`), bounded column sizes (64 KiB), database clock authority (`CURRENT_TIMESTAMP` vs `clock_timestamp()`), and dynamic worker capacity derivation (`worker_capacity` view).
  - **Testing & Provisioning Specification:** [docs/testing-postgres.md](file:///home/blazeee/MiniCloud/docs/testing-postgres.md) documenting Docker Compose local provisioning, host confinement rules, `.env.example` secret hygiene, `_test` database safety guardrails, and planned Makefile targets.

---

## 3. Approved Phase 3 Sub-Slices & Exit Criteria

### Slice 3a: Repository Atomicity Refactor, Worker Auto-Re-Registration & In-Memory Baseline
- **Scope:**
  - Reshape repository methods so the PostgreSQL implementation can run each as ONE transaction:
    - `DispatchJob(ctx, workerID, supportedTasks)`: Atomically transitions job from `QUEUED` to `RUNNING`, creates attempt record, and updates worker capacity.
    - `CommitResult(ctx, jobID, attemptID, ...)`: Atomically transitions attempt to `COMPLETED`/`FAILED`, transitions job to terminal state, commits result record, records lifecycle event, and releases worker slot.
    - `SubmitJob(ctx, job)`: Atomically inserts job and initial audit event.
    - `CancelJob(ctx, jobID)`: Atomically applies state-guarded cancellation and records audit event.
  - Implement atomic methods in `InMemoryRepository`.
  - Add worker client auto-re-registration when receiving HTTP 404 `WORKER_NOT_FOUND` on heartbeat or poll (with unit and integration tests).
  - Add coordinator-level test asserting `Queue.Release` is invoked when dispatch or attempt creation fails after a claim.
- **Exit Criteria:** Zero non-atomic mutation gaps in coordinator; worker automatically heals on 404 `WORKER_NOT_FOUND`; `Queue.Release` verified; all tests pass `-race`.

### Slice 3b: Database Provisioning, Schema & Forward-Only Migrations
- **Scope:**
  - Create `deployments/docker-compose.yml`, `init-test-db.sh`, `.env.example`, and update `.gitignore`.
  - Author initial embedded migration: `internal/storage/postgres/migrations/000001_initial_schema.sql` incorporating dynamic capacity view `worker_capacity` and timestamp semantics.
  - Implement minimal in-tree migration runner with `schema_migrations`, transaction-level advisory locks (`pg_advisory_xact_lock`), and SHA-256 checksum verification.
  - Add Makefile targets (`db-up`, `db-down`, `db-psql`, `test-db`).
- **Exit Criteria:** Migration runner successfully applies schema to `minicloud` and `minicloud_test`; concurrent runners serialized by transaction advisory lock; checksum tampering detected.

### Slice 3c: PostgreSQL Repository Implementation
- **Scope:**
  - Add `jackc/pgx/v5 v5.7.1` (`go 1.21 <= 1.22`) with `GOTOOLCHAIN=local` guardrail; verify all transitive dependencies in `go.mod`/`go.sum` against Go <= 1.22.
  - Implement `internal/storage/postgres.Repository` using `pgxpool.Pool`.
  - Enforce SQL invariants: partial unique index `idx_attempts_active_per_job`, monotonic attempt sequence, dynamic capacity derivation (no counter drift), and SHA-256 lease token hashing.
  - Add unit/integration tests running against `minicloud_test` with mandatory `_test` database name check.
- **Exit Criteria:** `internal/storage/postgres` satisfies all repository tests with race detector enabled.

### Slice 3d: PostgreSQL Dispatch Transport (`PostgresQueue`) & Scheduler Integration
- **Scope:**
  - Implement `internal/queue.Queue` backed by PostgreSQL transactional `SELECT ... FOR UPDATE SKIP LOCKED`.
  - Implement the owner-selected scheduler participation model (Candidate A pre-assignment with accept window vs Candidate C honest FIFO pull baseline with structured decision logging).
  - Verify structured decision logs (`policy`, `candidates`, `selected`, `reason`) are emitted.
- **Exit Criteria:** Zero duplicate deliveries under parallel polling; structured scheduler decisions logged; at-least-once recovery verified; benchmark baseline ready for V2 Redis comparison.

### Slice 3e: Coordinator Restart Recovery & Multi-Process Tests
- **Scope:**
  - Automated integration test verifying:
    1. Coordinator restarts with `QUEUED` jobs: jobs persist in PostgreSQL and dispatch to workers upon restart.
    2. Coordinator restarts with `RUNNING` jobs: surviving worker finishes task and restarted coordinator accepts commit.
    3. Multi-worker batch execution running against PostgreSQL storage.
- **Exit Criteria:** End-to-end restart recovery verified; all unit and integration tests pass `-race`.

---

## 4. Key Follow-Ups & Invariants

1. **Worker Re-Registration on 404:** Worker client must automatically re-register if heartbeat or poll receives `WORKER_NOT_FOUND` (scheduled for Slice 3a).
2. **Coordinator Queue Release Test:** A coordinator-level test verifying that `Queue.Release` is explicitly called when attempt creation fails after a claim (scheduled for Slice 3a).
3. **No Counter Drift:** Worker capacity is dynamically derived from active attempts in SQL rather than relying on mutable increment/decrement counters.
4. **Active Scheduler in Pull:** Owner decision pending between Candidate A (pre-assignment with accept window) and Candidate C (honest FIFO pull baseline) prior to Slice 3d. Candidate B (hold window) rejected.
5. **Phase 4 Lease Expiry & Requeue:** Unacknowledged in-flight queue items and dead workers' `RUNNING` jobs are handled in Phase 4 (leases and automated requeue).

---

## 5. Known Defects & Limitations
- Durability is currently in-memory only (PostgreSQL arrives in Phase 3 implementation).
- Jobs assigned to dead workers remain `RUNNING` until Phase 4 implements lease expiry and automatic requeue.
- Allowlisted in-process tasks only (`echo`, `sleep`, `sha256_loop`) until V3 Docker containers.

---

## 6. Last Verified Commands & Actual Results
- **Date:** 2026-10-20
```bash
$ go version
go version go1.22.2 linux/amd64

$ gofmt -s -w .
(clean exit, code 0)

$ go vet ./...
(clean exit, code 0)

$ go test -count=1 ./...
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api	0.033s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/config	0.006s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator	0.116s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/domain	0.010s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/executor	0.072s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/queue	0.006s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/scheduler	0.017s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage	0.010s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/worker	0.623s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/architecture	0.004s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	4.803s
```
