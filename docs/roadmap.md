# MiniCloud Platform Roadmap: V1, V2, and V3

## 1. Overview
MiniCloud development is structured into three major releases. Each version builds directly upon the previous release on the same codebase, ensuring continuous architectural compatibility and verifiable invariants.

---

## 2. Phase-to-Version Mapping & Exit Criteria

| Version | Phase | Scope | Exit Criteria | Status |
|---|---|---|---|:---:|
| **V1** | **Phase 0: Design** | Requirements, state machine, invariants, failure model, protocol. | Design docs reviewed; risks and non-goals explicit. | **DONE** |
| **V1** | **Phase 1: Local Core** | Go module, CLI/API, allowlisted task types (`echo`, `sleep`, `sha256_loop`), multi-process workers. | End-to-end jobs execute across independent processes; unit & integration tests pass `-race`. | **DONE** |
| **V1** | **Phase 2: Independent Workers** | Worker failure detection loop, heartbeat reaper, attempt IDs, capacity tracking, dispatch transport queue interface. | Worker processes communicate only through HTTP protocol; dead workers detected and evicted; 3-worker batch integration passes `-race`. | **DONE** |
| **V1** | **Phase 3: Durable State** | PostgreSQL schema, forward-only migrations, coordinator restart recovery, transactional FIFO pull dispatch (`fifo_pull`). | Accepted jobs recover correctly across coordinator restarts; DB is authoritative truth; surviving workers commit results after reboot. | **DONE** |
| **V1** | **Phase 4: Retry & Recovery** | Leases, bounded retries, stale-attempt rejection, automated fault injection suite. | Fault suite verifies invariants; expired leases revoked; no stale result wins; dead worker running jobs requeued. | **In Progress** (Design Specifications Proposed; ADR-0014, ADR-0015, ADR-0016) |
| **V1** | **Assignment-Based Round-Robin** | Scheduler-driven pre-assignment with bounded accept window (Candidate A), fulfilling Blueprint Section 6. | Round-robin assignment enforced; unaccepted assignments expire and requeue; audit decisions logged; hard gate for V1 release. | Planned (Post-Phase 4) |
| **V1** | **V1 Security & Resilience Gate** | Execute V1 fault injection, malformed input fuzzing, and API abuse test suites. | All V1 security test plan suites executed with results; zero unhandled panics or state corruption before tagging `v1.0.0`. | Planned (V1 Final Gate) |
| **V2** | **Phase 6: Scheduling Research** | Resource-aware scheduling policy, repeatable workload generator, comparative metrics. | Fair comparison with round-robin; p50/p95/p99 latency, queue wait, and throughput recorded. | Planned |
| **V2** | **Transport Evaluation & Security Gate** | Evaluate Redis Streams dispatch against PostgreSQL pull baseline via `internal/queue`. | Benchmark report comparing throughput, recovery, and duplicate delivery; failure and duplicate tests executed; ADR records choice. | Planned |
| **V3** | **Execution Isolation & Security Gate** | Docker container executor with CPU, memory, PID, and hard runtime limits. | Constrained task execution; container escape and resource limits tested; non-root isolation; no shell execution on host. | Planned |
| **V3** | **Phase 7: Multi-Machine & Auth Gate** | Network deployment, worker authentication, mutual TLS, network fault testing. | Worker authentication and mTLS enforced before any multi-machine claim; network partitions and reconnects tested. | Planned |
| **V3** | **Phase 8: Hardening & Release** | Prometheus metrics, operational docs, performance tuning, reproducible release. | Clean checkout setup succeeds from README; reproducible benchmark suite tagged. | Planned |

---

## 3. Approved Phase 3 Sub-Slices (Durable State)

1. **Slice 3a: In-Memory Repository Atomicity Refactor & Worker Re-Registration:**
   - Shape repository methods so the PostgreSQL implementation can run each as ONE transaction (`DispatchJob`, `CommitResult`, `SubmitJob`, `CancelJob`).
   - Add worker client auto-re-registration when receiving HTTP 404 `WORKER_NOT_FOUND` on heartbeat or poll.
   - Add coordinator-level unit test asserting `Queue.Release` on claim/dispatch failure.
2. **Slice 3b: Database Provisioning, Schema & Migrations:**
   - Author embedded migration `000001_initial_schema.sql` incorporating dynamic worker capacity derivation (no counter drift) and database timestamp semantics (`CURRENT_TIMESTAMP` vs `clock_timestamp()`).
   - Implement minimal in-tree migration runner with `pg_advisory_xact_lock` transaction advisory locking and SHA-256 checksum verification.
   - Author `deployments/docker-compose.yml`, `init-test-db.sh`, `.env.example`, and database Makefile targets.
3. **Slice 3c: PostgreSQL Repository Implementation:**
   - Introduce `jackc/pgx/v5 v5.7.1` (`go 1.21 <= 1.22`) with `GOTOOLCHAIN=local` guardrail; verify all transitive dependencies in `go.mod`/`go.sum` against Go <= 1.22.
   - Implement `internal/storage/postgres.Repository` using `pgxpool.Pool`.
   - Enforce SQL invariants (partial unique active attempt index, monotonic attempt sequence, hashed lease tokens, dynamic capacity).
4. **Slice 3d: PostgreSQL Dispatch Transport (`PostgresQueue`) & FIFO Pull Dispatch:**
   - Implement `internal/queue.Queue` using transactional `SELECT ... FOR UPDATE SKIP LOCKED`.
   - Dispatch uses honest FIFO pull baseline (`fifo_pull`) where polling workers claim available work matching their capacity slots.
   - Dispatch logs record real facts only: `job_id`, `attempt_id`, `worker_id`, `policy="fifo_pull"`, `reason="claimed by polling worker with capacity"`.
   - `internal/scheduler` is intentionally not used in the V1 dispatch loop during Phase 3 (temporary until post-Phase 4 assignment slice).
5. **Slice 3e: Coordinator Restart Recovery & Multi-Process Tests:**
   - Automated tests verifying surviving worker result commits across restarts and multi-worker execution against PostgreSQL.

---

## 4. Proposed Phase 4 Sub-Slices (Retry & Recovery)
*Complete architectural specifications are detailed in [docs/phase4-plan.md](phase4-plan.md).*

1. **Slice 4a: Domain State Machine, Attempt Lease Primitives, In-Memory Parity & Property Test**:
   - Update `internal/domain` FSM with new transitions (`RUNNING -> RETRYING`, `CANCEL_REQUESTED -> CANCELLED`, `RETRYING -> RUNNING`), duplicate cancel idempotency, and state naming reconciliation (`AttemptCancelled`, `AttemptLost`).
   - Implement `LeaseExpiresAt`, `RenewLease`, `SweepExpiredLeases`, and `not_before` backoff in `internal/storage.InMemoryRepository`.
   - Expand shared conformance suite (`internal/storage/storagetest/conformance.go`) and implement property/model-based state machine test harness.
2. **Slice 4b: Schema Migration 000004 & PostgreSQL Repository Durable Implementation**:
   - Author forward-only migration `000004_leases_retries_cancellation.sql` adding `attempts.lease_expires_at`, `attempts.error_class`, `jobs.not_before`, `jobs.cancel_requested_at`, indexes, and zero-downtime constraints.
   - Implement durable atomic repository methods in `internal/storage/postgres.Repository` adhering to canonical lock order, with sweeper re-check under row lock and startup/outage grace periods (30s).
3. **Slice 4c: HTTP API Protocol, Worker Lease Renewal & Coordinator Sweeper Wiring**:
   - Add `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew` endpoint as single delivery channel for cancellation (`cancel_requested: true`). Heartbeat response does not carry cancellations.
   - Implement periodic renewal loop in `internal/worker/client.go` (every 10s); implement coordinator lease sweeper loop (`StartLeaseSweeper`) ticking every 5s with startup grace suppression (30s).
4. **Slice 4d: Structured Retries, Failure Classification & Cooperative Cancellation**:
   - Implement 3-tier failure classification (`PERMANENT` vs `RETRYABLE`), permanent input validation failures, `LOST` attempts consuming budget, and exponential backoff with full jitter.
   - Implement cooperative cancellation via Go `context.Context` in worker executor and resolve concurrent cancellation races with duplicate cancel idempotency (HTTP 202).
5. **Slice 4e: End-to-End Fault-Injection Suite, Adversarial Testing & Phase 4 Security Review**:
   - Execute all 10 blueprint fault scenarios (FS-01 through FS-10) in `tests/integration/` per [docs/fault-injection-plan.md](fault-injection-plan.md).
   - Execute property-based test harness ($\ge 1,000$ iterations) and certify Phase 4 security review gate.

---

## 5. Post-Phase 4 Hard Gate: Assignment-Based Round-Robin Slice
- **Context & Blueprint Deviation:** In V1 Phase 3, dispatch is FIFO pull (`fifo_pull`). Blueprint Section 6 requires scheduler-driven assignment with round-robin policy.
- **Hard Gate Rule:** V1 is not complete, and V2 benchmarking must not start, until scheduler-driven assignment with a deterministic round-robin policy exists.
- **Staging Justification:** Assignment expiration, dead-worker reclamation, and automatic requeue share the exact lease/requeue machinery introduced in Phase 4. Building assignment after Phase 4 avoids duplicate or disposable recovery logic. Candidate A (pre-assignment with bounded accept window) is the leading design.

---

## 6. Security and Adversarial Review Gates

As defined in [ADR 0012](adr/0012-security-and-adversarial-testing-workflow.md) and [security-testing.md](security/security-testing.md), every major milestone enforces explicit security and reliability review gates:

1. **Milestone V1 Security Gate**:
   - Before tagging the final V1 release (`v1.0.0`), the complete V1 fault-injection, API abuse, header timeout, and malformed input test suite (specified in `docs/security/test-plan.md`) must be executed.
   - Observed results must be documented in `docs/security/findings.md` with zero unhandled panics, zero data corruption, and all invariants verified.
2. **Milestone V2 Dispatch Comparison Gate**:
   - The comparative evaluation between Redis Streams and the PostgreSQL baseline must include adversarial tests: broker restarts, transient network drops, and simulated duplicate message delivery under high queue contention.
   - Performance benchmarks must cite real measurements from controlled local hardware baselines without invented figures.
3. **Milestone V3 Isolation and Authentication Gate**:
   - Container isolation testing (cgroup memory/CPU limits, container escape probes, read-only rootfs barriers) must pass before claiming multi-tenant or untrusted execution support.
   - Worker mutual TLS (mTLS) and API token authentication must be fully implemented and validated with negative test vectors before any multi-machine deployment claim is recognized.

---

## 6. Scope & Version Gate Rules
1. **Incremental Complexity:** Infrastructure is introduced only when its phase is active. No PostgreSQL in Phase 2; no Redis in V1; no Docker until V3.
2. **Interface Decoupling:**
   - Scheduling policies implement `scheduler.Scheduler` without modifying job lifecycle logic.
   - Dispatch transport implements `internal/queue.Queue` so PostgreSQL pull and Redis Streams remain interchangeable.
   - Task execution implements `executor.Task` with no in-process assumptions, allowing container execution in V3.
3. **Compatibility Guarantee:** Additive-only changes within `/v1` API; forward-only database migrations.
