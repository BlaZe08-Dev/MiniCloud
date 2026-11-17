# MiniCloud Platform Roadmap: V1 to V4

## 1. Overview
MiniCloud development is structured into major releases. Each version builds directly upon the previous release on the same codebase, ensuring continuous architectural compatibility and verifiable invariants.

**V1 is released** as `v1.0.0` (tag on 166c719, 2026-11-15; code baseline `d7a397c`). Since PV-01 the plan has four versions: [ADR 0018](adr/0018-ai-assisted-workflow-dispatch.md) amends [ADR 0004](adr/0004-version-boundaries-and-compatibility.md) with a new V4 (AI workflow layer) and adds node/capability slices to V2 and node identity/Node Agent slices to V3 (owner decision D1). The merged V2/V3/V4 table is section 2a; the design behind it is in [docs/post-v1/ROADMAP.md](post-v1/ROADMAP.md) and [docs/post-v1/README.md](post-v1/README.md). Nothing after V1 is implemented.

---

## 2. Phase-to-Version Mapping & Exit Criteria

| Version | Phase | Scope | Exit Criteria | Status |
|---|---|---|---|:---:|
| **V1** | **Phase 0: Design** | Requirements, state machine, invariants, failure model, protocol. | Design docs reviewed; risks and non-goals explicit. | **DONE** |
| **V1** | **Phase 1: Local Core** | Go module, CLI/API, allowlisted task types (`echo`, `sleep`, `sha256_loop`), multi-process workers. | End-to-end jobs execute across independent processes; unit & integration tests pass `-race`. | **DONE** |
| **V1** | **Phase 2: Independent Workers** | Worker failure detection loop, heartbeat reaper, attempt IDs, capacity tracking, dispatch transport queue interface. | Worker processes communicate only through HTTP protocol; dead workers detected and evicted; 3-worker batch integration passes `-race`. | **DONE** |
| **V1** | **Phase 3: Durable State** | PostgreSQL schema, forward-only migrations, coordinator restart recovery, transactional FIFO pull dispatch (`fifo_pull`). | Accepted jobs recover correctly across coordinator restarts; DB is authoritative truth; surviving workers commit results after reboot. | **DONE** |
| **V1** | **Phase 4: Retry & Recovery** | Leases, bounded retries, stale-attempt rejection, automated fault injection suite. | Fault suite verifies invariants; expired leases revoked; no stale result wins; dead worker running jobs requeued. | **DONE** (slices 4a to 4e merged, 4e in PR #29; ADR-0014, ADR-0015, ADR-0016) |
| **V1** | **Assignment-Based Round-Robin** | Scheduler-driven pre-assignment with bounded accept window (Candidate A), fulfilling Blueprint Section 6. | Round-robin assignment enforced; unaccepted assignments expire and requeue; audit decisions logged; hard gate for V1 release. | **DONE** (ADR-0017 slices 5a to 5c merged in PRs #35, #38, #40, #41, #44, #45; `round_robin` is the default policy since P4-17, PR #47; `fifo_pull` stays selectable) |
| **V1** | **V1 Security & Resilience Gate** | Execute V1 fault injection, malformed input fuzzing, and API abuse test suites. | All V1 security test plan suites executed with results; zero unhandled panics or state corruption before tagging `v1.0.0`. | **DONE: executed**, results in [findings.md](security/findings.md) ("V1 Security Gate results"); the `v1.0.0` tag itself is not recorded here |
| **V1** | **Release** | Tag `v1.0.0`. | V1 gate executed; P4-23 archive merged (PR #54). | **RELEASED** (2026-11-15, tag on 166c719, code baseline `d7a397c`) |

The V2 and V3 rows that stood here (Phase 6 scheduling research; transport evaluation and security gate; execution isolation and security gate; Phase 7 multi-machine and auth gate; Phase 8 hardening and release) are replaced by section 2a. Their scope is kept: Phase 6 becomes V2-2 and V2-3, the transport evaluation becomes V2-4, execution isolation becomes V3-1, Phase 7 becomes V3-2 and V3-4, Phase 8 becomes V3-5.

---

## 2a. Merged V2/V3/V4 Roadmap (post-V1, planned)

Slice names are proposals for the task board (`docs/agents/TASKS.md`). Status of every row after Gate 0: planned, not started.

| Version | Slice | Scope | Exit criterion |
|---|---|---|---|
| V1.0.x | maintenance | the v1.0.1 list (minors F-5a-14/16/17, `GetWorker` not reading `last_poll_at`, in-memory F05/F06 mutation gap, `logsafe` in `cmd/migrate`, repo cleanup) | no behaviour change beyond the listed fixes |
| Gate 0 | PV-01 | documentation reconciliation and design freeze | `make test-docs` rc 0; hierarchy and links verified; no code touched |
| **V2** | V2-0 | **Worker concurrency truth.** Decide and document slot semantics: real concurrent execution with supervision, or advertise what is actually executed. ADR first. | advertised capacity equals real concurrency; race tests; V1 worker protocol still compatible |
| V2 | V2-0b | **Round-robin uses every free slot.** Remove `domain.EffectiveWorkerSlots` (rule becomes `total_slots - running - reserved`), amend ADR-0017 Section 2.4 and Q5; coordinator, storage and tests; Opus audit. Planned, not started; after V2-0, before V2-1. Also measure the `round_robin` assignment-pass overhead (V2-0 audit O-1: about 6 s slower than serial `fifo_pull`, 46.60 s against 40.41 s for 8 x 5 s jobs). | under `round_robin` a worker with free slots is reserved work up to its capacity; no reservation beyond it; ADR-0017 invariants still pass |
| V2 | V2-1 | **Node and capability model (D3).** Additive forward-only migration `000006`+ with separate tables: `nodes` (identity only), `node_hardware` (versioned static inventory), `node_telemetry` (latest dynamic sample with `sampled_at`), and nullable `workers.node_id`. `node_tools` is designed in the V2-1 ADR but created in V4. Absent data means "unknown"; legacy workers (NULL node) stay eligible. Telemetry updates must not rewrite the hardware row. | stale telemetry is never treated as current; V1 workers register unchanged; hardware rows are not rewritten by telemetry |
| V2 | V2-2 | Workload generator and benchmark harness (built-in tasks only, controlled hardware, no invented numbers). Proposed: record median-of-N speed per task type per worker for the proposed V2-3 score ([placement design](post-v1/NODE_CAPABILITIES.md)). | repeatable runs, recorded environment |
| V2 | V2-3 | **Resource-aware policy** plugged into the ADR-0017 assignment pass: hard constraints first, deterministic ranking, decision audit record. Proposed filter-and-score design: [post-v1/NODE_CAPABILITIES.md](post-v1/NODE_CAPABILITIES.md), section "Placement algorithm (proposed)". Compared with `round_robin` and `fifo_pull` on p50/p95/p99, queue wait, throughput, fairness. | placement reproducible from recorded inputs; resource limits never violated in tests |
| V2 | V2-4 | Redis Streams versus the PostgreSQL baseline (ADR-0006) with broker-restart, network-drop and duplicate-delivery tests. PostgreSQL stays the source of truth. | benchmark report with real measurements; ADR recorded |
| V2 | gate | V2 security and fault gate | per `docs/security/test-plan.md` |
| **V3** | V3-1 | Docker executor with CPU, memory, PID and runtime limits (ADR-0005) | escape and limit tests |
| V3 | V3-2 | **Authentication**: API clients, node enrollment and identity, authorization scopes, transport security (mTLS candidate), audit events. Closes SEC-004. | an unauthenticated device cannot enroll, submit, inspect or execute |
| V3 | V3-3 | **Node Agent evolution**: extend the worker runtime (no second daemon), keep the V1 worker protocol compatible | old and new workers coexist |
| V3 | V3-4 | Multi-machine networking and network fault tests. Remote execution stays disabled until V3-1 and V3-2 pass. | partitions and reconnects tested |
| V3 | V3-5 | Prometheus metrics, operational docs, reproducible release from a clean checkout | clean-checkout setup from the README |
| V3 | gate | V3 isolation and authentication gate | per ADR-0012 |
| **V4 (new)** | V4-0 | Design freeze: resolve the open decisions that block V4-1, contracts, threat model, test plan | no open decision blocks V4-1 |
| V4 | V4-1 | **Workflow model, workspaces and artifacts** (single machine), each runnable task mapped onto the existing job/attempt lifecycle | no shared-directory mutation; stale artifact never accepted |
| V4 | V4-2 | **CLI adapter contract and one provider adapter**: single machine, restricted account (D2), read-only profile first, writes with approval, argument arrays, environment allowlist, process-tree cancellation, bounded logs | adapter contract tests, no credential leakage, no shell strings |
| V4 | V4-3 | **Tool-aware placement**: create `node_tools` and add tool readiness and execution profile as hard constraints to the V2-3 policy | placement decisions explain themselves from recorded inputs |
| V4 | V4-4 | **Intelligence Dispatcher**: provider-neutral planner, schema-validated DAG, plan preview and approval, replanning as a new immutable revision | untrusted planner output cannot bypass validation, auth, scheduler constraints or approval |
| V4 | V4-5 | Interfaces: additive `/v1` workflow and node endpoints, `mc workflow` commands, Go SDK, simple chat UI, explicit external-agent ingress | all four interfaces share one backend and one authorization model |
| V4 | V4-6 | Multi-device AI execution demo (requires V3 gates), artifact transfer with checksum verification, node-loss and stale-result tests | demo survives a node loss and rejects stale results |
| V4 | gate | V4 security gate: prompt injection, malicious CLI output, workspace escape, replayed enrollment, partial side effects on retry | per [docs/post-v1/SECURITY.md](post-v1/SECURITY.md) |
| Later | HA | Coordinator failover (Raft or database leases with fencing) only after need is shown and database HA is designed with it | split-brain tests |

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
   - Dispatch uses honest FIFO pull baseline (`fifo_pull`) where polling workers claim available work matching their capacity slots (since P4-17, the default dispatch policy is `round_robin`, with `fifo_pull` selectable).
   - Dispatch logs record real facts only: `job_id`, `attempt_id`, `worker_id`, `policy="fifo_pull"`, `reason="claimed by polling worker with capacity"`.
   - `internal/scheduler` is intentionally not used in the V1 dispatch loop during Phase 3 (temporary until post-Phase 4 assignment slice).
5. **Slice 3e: Coordinator Restart Recovery & Multi-Process Tests:**
   - Automated tests verifying surviving worker result commits across restarts and multi-worker execution against PostgreSQL.

---

## 4. Proposed Phase 4 Sub-Slices (Retry & Recovery)
*Complete architectural specifications are detailed in [docs/phase4-plan.md](phase4-plan.md).*

1. **Slice 4a: Domain State Machine, Attempt Lease Primitives, In-Memory Parity & Property Test**:
   - Update `internal/domain` FSM with new transitions (`RUNNING -> RETRYING`, `CANCEL_REQUESTED -> CANCELLED`, `RETRYING -> RUNNING`), duplicate cancel idempotency, and state naming reconciliation (`AttemptCancelled`, `AttemptLost`).
   - Add `LeaseExpiresAt`, `not_before` dispatch gating, idempotent duplicate cancel and in-memory dispatch via `DispatchNextJob` in `internal/storage.InMemoryRepository`. (In-memory `RenewLease` and `SweepExpiredLeases` moved to slice 4b.)
   - Expand shared conformance suite (`internal/storage/storagetest/conformance.go`) and implement property/model-based state machine test harness.
2. **Slice 4b: Schema Migration 000004, Lease Renewal and Sweep (In-Memory and PostgreSQL) & Pure Backoff Function**:
   - Author forward-only migration `000004_leases_retries_cancellation.sql` adding `attempts.lease_expires_at`, `attempts.error_class`, `jobs.not_before`, `jobs.cancel_requested_at`, indexes, and zero-downtime constraints.
   - Implement durable atomic repository methods in `internal/storage/postgres.Repository` adhering to canonical lock order, with sweeper re-check under row lock (the 30 s startup and outage grace periods are owned by slice 4c). Also adds in-memory `RenewLease` and `SweepExpiredLeases`, the in-lock grace check driven by a grace-state input, and a pure job-retry backoff function in `internal/domain`.
3. **Slice 4c: HTTP API Protocol, Worker Lease Renewal & Coordinator Sweeper Wiring**:
   - Add `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew` endpoint as single delivery channel for cancellation (`cancel_requested: true`). Heartbeat response does not carry cancellations.
   - Implement periodic renewal loop in `internal/worker/client.go` (every 10s); implement coordinator lease sweeper loop (`StartLeaseSweeper`) ticking every 5s owning the startup grace period and the outage grace and cooldown state, and passing that state to `SweepExpiredLeases`.
4. **Slice 4d: Structured Retries, Failure Classification & Cooperative Cancellation**:
   - Implement 4-class failure classification (`PERMANENT`, `TRANSIENT`, `UNKNOWN`, `LEASE_EXPIRED`), permanent input validation failures, `LOST` attempts consuming budget, and reuse of the slice 4b backoff function for retry scheduling.
   - Implement cooperative cancellation via Go `context.Context` in worker executor and resolve concurrent cancellation races with duplicate cancel idempotency (HTTP 200).
5. **Slice 4e: End-to-End Fault-Injection Suite, Adversarial Testing & Phase 4 Security Review**:
   - Execute all 10 blueprint fault scenarios (FS-01 through FS-10) in `tests/integration/` per [docs/fault-injection-plan.md](fault-injection-plan.md).
   - Execute property-based test harness (>= 1,000 iterations) and certify Phase 4 security review gate.

---

## 5. Post-Phase 4 Hard Gate: Assignment-Based Round-Robin Slice
- **Context & Blueprint Deviation:** In V1 Phase 3, dispatch was initially FIFO pull (`fifo_pull`). Blueprint Section 6 requires scheduler-driven assignment with round-robin policy; since Slice P4-17, the default dispatch policy is `round_robin` (assignment-based round-robin), with `fifo_pull` selectable.
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
3. **Milestone V3 Isolation and Authentication Gate** (post-V1 slices V3-1 and V3-2):
   - Container isolation testing (cgroup memory/CPU limits, container escape probes, read-only rootfs barriers) must pass before claiming multi-tenant or untrusted execution support.
   - Worker mutual TLS (mTLS) and API token authentication must be fully implemented and validated with negative test vectors before any multi-machine deployment claim is recognized.

---

## 7. Scope & Version Gate Rules
1. **Incremental Complexity:** Infrastructure is introduced only when its phase is active. No PostgreSQL in Phase 2; no Redis in V1; no Docker until V3.
2. **Interface Decoupling:**
   - Scheduling policies implement `scheduler.Scheduler` without modifying job lifecycle logic.
   - Dispatch transport implements `internal/queue.Queue` so PostgreSQL pull and Redis Streams remain interchangeable. (Note, PV-01: in V1.0.0 no production package uses `internal/queue`; dispatch runs through the repository, ADR-0011 and ADR-0017. Whether the queue interface stays the seam is decided by V2-4; see the ADR-0008 implementation note.)
   - Task execution implements `executor.Task` with no in-process assumptions, allowing container execution in V3.
3. **Compatibility Guarantee:** Additive-only changes within `/v1` API; forward-only database migrations.
4. **Post-V1 additions (ADR-0018):** V4 adds the V4 security gate (section 2a). Remote execution stays disabled until V3-1 and V3-2 pass; AI CLI work on the host follows the trust model D2 in ADR-0018.
