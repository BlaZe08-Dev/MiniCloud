# Changelog

All notable changes to MiniCloud will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Phase 3 — Durable State (Design Specifications & Owner Approvals):**
  - `docs/adr/0009-postgres-persistence-and-driver.md`: Accepted Architecture Decision Record selecting `jackc/pgx/v5` with `pgxpool` and pinned `postgres:16-alpine` container image, subject to pinned Go <= 1.22 version (`v5.7.1` uses `go 1.21`), `GOTOOLCHAIN=local`, and gated dependency introduction in Slice 3c.
  - `docs/adr/0010-migrations-policy.md`: Accepted Architecture Decision Record establishing forward-only schema migrations, expand/contract rules for destructive alterations, and an embedded in-tree runner with `pg_advisory_xact_lock` transaction advisory locking and SHA-256 checksum verification.
  - `docs/adr/0011-postgres-dispatch-and-recovery.md`: Accepted Architecture Decision Record defining PostgreSQL `SELECT ... FOR UPDATE SKIP LOCKED` pull dispatch, confirming the first-poller-wins gap, adopting Hybrid Pull with Scheduler Arbitration (Model 4) to ensure `scheduler.SelectWorker` actively governs dispatch, dynamic worker capacity derivation (no counter drift), surviving-worker restart recovery guarantees, and SHA-256 hashed lease tokens.
  - `docs/adr/0003-pull-based-worker-protocol.md`: Amended to document scheduler participation and arbitration under a pull-based coordination protocol.
  - `docs/schema.md`: PostgreSQL database schema defining tables (`jobs`, `attempts`, `workers`, `results`, `job_events`), bounded column sizes (64 KiB), database clock authority (`CURRENT_TIMESTAMP` vs `clock_timestamp()`), dynamic worker capacity view (`worker_capacity`), and SQL invariants.
  - `docs/testing-postgres.md`: Docker Compose provisioning architecture, snap Docker home directory confinement rules, `.env.example` credential hygiene, mandatory `_test` database name guardrails for integration tests, and planned Makefile targets.
- **Phase 2, Slice 2 — Dispatch-Transport Interface & 3-Worker Integration:**
  - `docs/adr/0008-dispatch-transport-queue-interface.md`: ADR defining the minimal queue interface, at-least-once delivery semantics, result-commit acknowledgement, and repository single source of truth invariant.
  - `docs/adr/0005-docker-isolation-in-v3.md`: Added "Known Gaps for V3 Container Executor" reviewing `internal/executor` against container requirements (cgroup limits, image mapping, stream handling).
  - `docs/protocol.md` & `docs/adr/0007-worker-liveness-and-recovery.md`: Documented Phase 2 correction distinguishing empty queue (HTTP 204 `NO_QUEUED_JOBS`) from unhealthy worker (HTTP 503 `WORKER_UNHEALTHY`).
  - `internal/domain`: Added `ErrWorkerUnhealthy` typed domain error.
  - `internal/api`: Mapped `ErrWorkerUnhealthy` to HTTP 503 `WORKER_UNHEALTHY`.
  - `internal/worker`: Handled HTTP 503 `WORKER_UNHEALTHY` in worker client, maintaining active heartbeats and polling without process termination.
  - `internal/queue`: Introduced `Queue` interface (`Enqueue`, `Claim`, `Release`, `Ack`, `Len`) and `Item` routing signal struct (`JobID`, `TaskType` only; payload and timeout read from repository at dispatch time).
  - `internal/queue`: Implemented thread-safe `InMemoryQueue` with FIFO ordering, capability matching, and in-flight delivery tracking.
  - `internal/coordinator`: Added `NewServiceWithQueue` constructor while keeping `NewService` signature defaulting to `InMemoryQueue`; integrated queue enqueue on submit, claim with repository state re-validation on poll (dropping non-QUEUED items and preventing duplicate attempts on redelivery), and queue ack strictly after accepted result commits.
  - `tests/architecture/boundaries_test.go`: Added boundary tests ensuring `internal/executor` does not depend on scheduler or queue, and `internal/queue` does not depend on api, coordinator, worker, or scheduler.
  - `tests/integration/multi_worker_batch_test.go`: Added 3-worker batch integration test executing 6 jobs across 3 independent worker processes, verifying parallel execution and distribution.
- **Phase 2, Slice 1 — Worker Liveness & Failure Recovery:**
  - `docs/adr/0007-worker-liveness-and-recovery.md`: Architecture Decision Record for worker liveness, atomic reaper loop, and failure recovery semantics.
  - `internal/storage`: Atomic `ReapStaleWorkers` and `RecordHeartbeat` methods in `InMemoryRepository` protecting against heartbeat/reap race conditions.
  - `internal/coordinator`: Background reaper loop `StartReaper` and `ReapInactiveWorkers` with injectable `Clock` abstraction for deterministic sub-millisecond testing.
  - `internal/coordinator`: Polling restriction excluding `UNHEALTHY` workers from claiming work; polling does not substitute for heartbeat telemetry.
  - `internal/coordinator`: Result commit acceptance for workers marked `UNHEALTHY` during execution for their valid attempt.
  - `internal/coordinator` & `internal/api`: Strict worker attempt ownership verification rejecting mismatched worker IDs with HTTP 403 `WORKER_MISMATCH`.
  - `internal/worker`: Permanent error classification via `IsNonRetryableReportError` treating HTTP 403 `WORKER_MISMATCH` and HTTP 409 conflict responses as non-retryable.
  - `internal/config`: Startup validation for configurable `MINICLOUD_REAPER_INTERVAL` (`> 0` and `<= MINICLOUD_WORKER_TIMEOUT`) and `MINICLOUD_WORKER_TIMEOUT >= 3 * MINICLOUD_HEARTBEAT_INTERVAL`.
  - `tests/integration/worker_liveness_test.go`: Automated integration test verifying dead worker reaping on SIGKILL, scheduler exclusion, and successful work routing to surviving workers.
- **Architecture Boundaries, Version Gating & Owner Decisions (`docs/version-boundaries`):**
  - `docs/adr/0004-version-boundaries-and-compatibility.md`: V1/V2/V3 platform boundaries and additive-only compatibility policy (Accepted).
  - `docs/adr/0005-docker-isolation-in-v3.md`: Deferring Docker container execution to V3 with unprivileged allowlisted task execution in V1/V2 (Accepted).
  - `docs/adr/0006-redis-streams-dispatch-evaluation-in-v2.md`: Comparative evaluation of Redis Streams vs. PostgreSQL baseline in V2 with `internal/queue` transport abstraction (Accepted for evaluation).
  - `docs/roadmap.md`: Consolidated phase-to-version mapping and exit criteria across V1, V2, and V3.
  - `tests/architecture/boundaries_test.go`: Automated architecture boundary test enforcing complete independence of `internal/domain`.
  - `docs/protocol.md`: Added Section 5 defining additive compatibility guarantees, deprecation windows, and migration rules.
  - `GEMINI.md`: Integrated 10 supplemental engineering rules and version boundaries within a concise 76-line manual.
- **Phase 0 Context & Architecture:**
  - `GEMINI.md`: Repository operating manual, invariant list, and session protocol.
  - `docs/PROJECT_STATE.md`: Living handoff state and verified command log.
  - `docs/architecture.md`: System components, boundaries, data flow, and ownership rules.
  - `docs/state-machine.md`: Finite state machine, transition matrix, and attempt lifecycle.
  - `docs/failure-model.md`: Fault scenarios, expected behaviors, and testing matrix.
  - `docs/protocol.md`: Client HTTP API and worker pull-based coordination protocol.
  - Architecture Decision Records:
    - `docs/adr/0001-record-architecture-decisions.md`
    - `docs/adr/0002-go-stdlib-http-for-initial-api.md`
    - `docs/adr/0003-pull-based-worker-protocol.md`
  - `.gitignore` for Go binaries, test artifacts, and IDE configurations.
- **Milestone 1 — Local Core:**
  - Go module initialized (`github.com/BlaZe08-Dev/Minicloud`) with zero third-party dependencies (stdlib only).
  - `internal/domain`: Pure domain model with Job, Attempt, and Worker entities; central FSM validation for all 9 states and typed `InvalidStateTransitionError`.
  - `internal/storage`: Thread-safe, non-durable `InMemoryRepository` with CRUD for jobs, attempts, and workers.
  - `internal/executor`: Allowlisted task execution registry with `echo`, `sleep` (bounded duration <= 10s), and `sha256_loop` (bounded iterations <= 1,000,000), context-driven timeout enforcement, 64 KiB payload limit, and 64 KiB output truncation.
  - `internal/scheduler`: Deterministic round-robin scheduler with candidate filtering (health, slots, supported tasks), deterministic tie-breaking, and structured decision logging.
  - `internal/coordinator`: Orchestration service implementing job submission, dispatching, and atomic result commitment with attempt ID and lease token verification for stale-result rejection.
  - `internal/api`: HTTP API server implementing endpoints (`POST /v1/jobs`, `GET /v1/jobs/{id}`, `GET /v1/jobs/{id}/attempts`, `POST /v1/jobs/{id}/cancel`, `GET /v1/workers`, `GET /healthz`, `GET /readyz`, `POST /v1/workers/register`, `POST /v1/workers/{id}/heartbeat`, `POST /v1/workers/{id}/poll`, `POST /v1/jobs/{id}/attempts/{attempt_id}/result`), request size limit (1 MiB), and structured JSON error envelope.
  - `internal/worker`: Independent worker agent communicating strictly via HTTP (registration, periodic heartbeats, pull/claim loop, execution, and result reporting).
  - `internal/config`: Environment variable configuration and startup validation.
  - `cmd/coordinator`, `cmd/worker`, `cmd/mc`: CLI application and daemons with structured `log/slog` output.
  - `Makefile`: Targets `build`, `test`, `test-race`, `fmt`, `vet`, `run-coordinator`, `run-worker`, `demo`.
  - Comprehensive test suite: Full 81-pair FSM transition matrix unit test, executor bounds test, scheduler determinism test, API validation and stale-rejection test, and multi-process integration test (`TestLocalCore_MultiProcessIntegration`).
  - Automated demo script `scripts/demo.sh`.
