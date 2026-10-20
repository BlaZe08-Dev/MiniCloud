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
| **V1** | **Phase 3: Durable State** | PostgreSQL schema, forward-only migrations, coordinator restart recovery, transactional dispatch. | Accepted jobs recover correctly across coordinator restarts; DB is authoritative truth; surviving workers commit results after reboot. | **In Progress** (Design Accepted by Owner) |
| **V1** | **Phase 4: Retry & Recovery** | Leases, bounded retries, stale-attempt rejection, automated fault injection suite. | Fault suite verifies invariants; expired leases revoked; no stale result wins; dead worker running jobs requeued. | Planned |
| **V2** | **Phase 6: Scheduling Research** | Resource-aware scheduling policy, repeatable workload generator, comparative metrics. | Fair comparison with round-robin; p50/p95/p99 latency, queue wait, and throughput recorded. | Planned |
| **V2** | **Transport Evaluation** | Evaluate Redis Streams dispatch against PostgreSQL pull baseline via `internal/queue`. | Benchmark report comparing throughput, recovery, and duplicate delivery; ADR records choice. | Planned |
| **V3** | **Execution Isolation** | Docker container executor with CPU, memory, PID, and hard runtime limits. | Constrained task execution; non-root isolation; no shell execution on host. | Planned |
| **V3** | **Phase 7: Multi-Machine** | Network deployment, worker authentication, mutual TLS, network fault testing. | Workers on separate physical/virtual hosts execute jobs safely over untrusted network. | Planned |
| **V3** | **Phase 8: Hardening & Release** | Prometheus metrics, operational docs, performance tuning, reproducible release. | Clean checkout setup succeeds from README; reproducible benchmark suite tagged `v1.0.0`. | Planned |

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
   - Introduce `jackc/pgx/v5 v5.7.1` (`go 1.21 <= 1.22`) with `GOTOOLCHAIN=local` guardrail.
   - Implement `internal/storage/postgres.Repository` using `pgxpool.Pool`.
   - Enforce SQL invariants (partial unique active attempt index, monotonic attempt sequence, hashed lease tokens, dynamic capacity).
4. **Slice 3d: PostgreSQL Dispatch Transport (`PostgresQueue`) & Scheduler Arbitration:**
   - Implement `internal/queue.Queue` using transactional `SELECT ... FOR UPDATE SKIP LOCKED`.
   - Integrate Hybrid Pull with Scheduler Arbitration (Model 4) so `scheduler.SelectWorker` is actively called and structured decision logs (`policy`, `candidates`, `selected`, `reason`) are emitted.
5. **Slice 3e: Coordinator Restart Recovery & Multi-Process Tests:**
   - Automated tests verifying surviving worker result commits across restarts and multi-worker execution against PostgreSQL.

---

## 4. Key Follow-Ups Across Phases
- **Worker Re-Registration:** Automatic recovery when worker ID is unknown to coordinator (Slice 3a).
- **Queue Release Verification:** Coordinator must call `Queue.Release` when attempt creation fails after a claim (Slice 3a).
- **Dispatch Atomicity & Dynamic Capacity:** Job state transition to `RUNNING` and attempt creation must be transactional; capacity derived in SQL from active attempts to prevent counter drift (Slice 3a/3b/3c).
- **Scheduler Active Participation:** Scheduler arbitration in pull loop emitting structured decision logs (Slice 3d).
- **Phase 4 Lease Expiry & In-Flight Requeue:** Handling unacked in-flight queue items and recovering jobs abandoned by dead workers will be implemented in Phase 4.

---

## 5. Scope & Version Gate Rules
1. **Incremental Complexity:** Infrastructure is introduced only when its phase is active. No PostgreSQL in Phase 2; no Redis in V1; no Docker until V3.
2. **Interface Decoupling:**
   - Scheduling policies implement `scheduler.Scheduler` without modifying job lifecycle logic.
   - Dispatch transport implements `internal/queue.Queue` so PostgreSQL pull and Redis Streams remain interchangeable.
   - Task execution implements `executor.Task` with no in-process assumptions, allowing container execution in V3.
3. **Compatibility Guarantee:** Additive-only changes within `/v1` API; forward-only database migrations.
