# MiniCloud Project State & Handoff

## 1. Current Phase & Milestone
- **Current Branch:** `docs/phase3-design`
- **Current Milestone:** Phase 3 (Durable State) — Architecture & Design Specification — **COMPLETED**
- **Next Milestone:** Phase 3, Slice 3a — Repository Atomicity Refactor (Awaiting Owner Approval).
- **Target Release:** MiniCloud V1 (Phases 0–4).

---

## 2. Done
- `f5a4968` — `docs: add MiniCloud blueprint and instructions`
- `2fb7958` — `docs: establish Phase 0 architecture, state machine, and context layer`
- `eabcc22` / `78f2aa2` — `feat: implement Milestone 1 local core coordinator, worker protocol, and CLI` (Merged PR #1 to `main`).
- `a636ba0` — `feat: implement Phase 2 Slice 1 worker liveness, reaper loop, and failure recovery` (Merged PR #3 to `main`).
- `8763113` — `feat: implement dispatch-transport queue interface and delivery semantics` (Merged PR #4 to `main`).
- **Phase 3 — Durable State Design Specifications (`docs/phase3-design`):**
  - Authored [ADR 0009: PostgreSQL Persistence and Driver Selection](file:///home/blazeee/MiniCloud/docs/adr/0009-postgres-persistence-and-driver.md) proposing `jackc/pgx/v5` with `pgxpool` and pinned `postgres:16-alpine`.
  - Authored [ADR 0010: Database Migrations Policy and Runner Architecture](file:///home/blazeee/MiniCloud/docs/adr/0010-migrations-policy.md) establishing forward-only migrations, expand/contract rules, and an embedded SQL runner with advisory locks.
  - Authored [ADR 0011: PostgreSQL Dispatch Mechanics, Restart Recovery, and Database Invariants](file:///home/blazeee/MiniCloud/docs/adr/0011-postgres-dispatch-and-recovery.md) defining `SELECT ... FOR UPDATE SKIP LOCKED` dispatch, coordinator restart recovery rules, SHA-256 hashed lease tokens, and SQL-level duplicate attempt prevention.
  - Authored [docs/schema.md](file:///home/blazeee/MiniCloud/docs/schema.md) specifying PostgreSQL tables (`jobs`, `attempts`, `workers`, `results`, `job_events`), bounded column sizes (64 KiB), database clock authority (`CURRENT_TIMESTAMP`), and SQL invariants.
  - Authored [docs/testing-postgres.md](file:///home/blazeee/MiniCloud/docs/testing-postgres.md) documenting Docker Compose local provisioning, host confinement rules, `.env.example` secret hygiene, `_test` database safety guardrails, and planned Makefile targets.

---

## 3. Proposed Phase 3 Sub-Slices & Exit Criteria

To ensure reviewable diffs and maintain working tests at every step, Phase 3 is divided into five incremental sub-slices:

### Slice 3a: Repository Atomicity & Interface Refactoring (In-Memory Baseline)
- **Scope:**
  - Refactor `storage.Repository` interface to unify multi-step repository calls into atomic transactional operations:
    - `DispatchJob(ctx, workerID, supportedTasks)`: Atomically transitions job from `QUEUED` to `RUNNING`, creates attempt record, and updates worker capacity.
    - `CommitResult(ctx, jobID, attemptID, ...)`: Atomically transitions attempt to `COMPLETED`/`FAILED`, transitions job to terminal state, commits result record, records lifecycle event, and releases worker slot.
    - `SubmitJob(ctx, job)`: Atomically inserts job and initial audit event.
  - Implement atomic methods in `InMemoryRepository`.
  - Add coordinator unit test asserting `Queue.Release` is invoked when dispatch or attempt creation fails after a claim.
- **Exit Criteria:** Zero non-atomic mutation gaps in coordinator; all existing tests pass `-race`.

### Slice 3b: Database Provisioning, Schema & Forward-Only Migrations
- **Scope:**
  - Create `deployments/docker-compose.yml`, `init-test-db.sh`, `.env.example`, and update `.gitignore`.
  - Author initial embedded migration: `internal/storage/postgres/migrations/000001_initial_schema.sql`.
  - Implement minimal in-tree migration runner with `schema_migrations`, advisory lock (`pg_advisory_lock`), and SHA-256 checksum verification.
  - Add Makefile targets (`db-up`, `db-down`, `db-psql`, `test-db`).
- **Exit Criteria:** Migration runner successfully applies schema to `minicloud` and `minicloud_test`; concurrent runners serialized by advisory lock; checksum tampering detected.

### Slice 3c: PostgreSQL Repository Implementation
- **Scope:**
  - Add `jackc/pgx/v5` dependency upon owner approval.
  - Implement `internal/storage/postgres.Repository` using `pgxpool.Pool`.
  - Enforce SQL invariants: partial unique index `idx_attempts_active_per_job`, monotonic attempt sequence, and SHA-256 lease token hashing.
  - Add unit/integration tests running against `minicloud_test` with mandatory `_test` database name check.
- **Exit Criteria:** `internal/storage/postgres` satisfies all repository tests with race detector enabled.

### Slice 3d: PostgreSQL Dispatch Transport (`PostgresQueue`)
- **Scope:**
  - Implement `internal/queue.Queue` backed by PostgreSQL transactional `SELECT ... FOR UPDATE SKIP LOCKED`.
  - Integrate with coordinator's `PollWork` loop.
- **Exit Criteria:** Zero duplicate deliveries under parallel polling; at-least-once recovery verified; benchmark baseline ready for V2 Redis comparison.

### Slice 3e: Coordinator Restart Recovery & Multi-Process Tests
- **Scope:**
  - Automated integration test verifying:
    1. Coordinator restarts with `QUEUED` jobs: jobs persist in PostgreSQL and dispatch to workers upon restart.
    2. Coordinator restarts with `RUNNING` jobs: surviving worker finishes task and restarted coordinator accepts commit.
    3. Multi-worker batch execution running against PostgreSQL storage.
- **Exit Criteria:** End-to-end restart recovery verified; all unit and integration tests pass `-race`.

---

## 4. Key Follow-Ups & Invariants

1. **Coordinator Queue Release Test:** A coordinator-level test verifying that `Queue.Release` is explicitly called when attempt creation fails after a claim (identified in Phase 2 Slice 2; scheduled for Slice 3a).
2. **Phase 4 Lease Expiry & Requeue:** Unacknowledged in-flight queue items and dead workers' `RUNNING` jobs are handled in Phase 4 (leases and automated requeue). The Phase 4 implementation plan must explicitly address both.
3. **Dispatch Atomicity:** In-memory dispatch currently executes as two separate repository calls (`CreateAttempt` then `UpdateJob`); Phase 3 must make this single-transaction atomic in PostgreSQL (`DispatchJob`).

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
