# ADR 0013: PostgreSQL Durable Dispatch, Deadlock-Free Lock Ordering, Approximate FIFO, and Coordinator Recovery

## Status
Accepted (Slice 3d)

## Context
MiniCloud Milestone 3 (Phase 3) introduces durable PostgreSQL persistence. In Phase 3b and 3c, the coordinator continued to use an in-memory queue (`internal/queue/inmemory.go`) paired with discrete `repo.DispatchJob(jobID, workerID)` calls. 

Under ADR-0011, two durable dispatch options were evaluated:
- **Option A (FIFO Pull via SQL Transaction):** Workers poll the coordinator, which queries and claims the next eligible queued job directly in PostgreSQL via `repo.DispatchNextJob(workerID)` in a single transaction with `FOR UPDATE SKIP LOCKED`.
- **Option B (Durable Queue via Separate Transport):** Introducing an external message broker (e.g. Redis Streams or RabbitMQ).

ADR-0011 selected **Option A** for V1/Phase 3 to maintain minimal operational dependencies and avoid premature distributed technology. However, wiring the coordinator directly to PostgreSQL durable dispatch required addressing six core architectural challenges:

1. **Lock Ordering & Deadlock Prevention**: Concurrent operations (`CommitAttempt`, `CancelJob`, `DispatchNextJob`) access `jobs`, `workers`, and `attempts` rows. If `DispatchNextJob` locks worker before job while `CommitAttempt` and `CancelJob` lock job before worker, opposing lock acquisition orders cause database deadlocks.
2. **Error Precedence vs Lock Order (SEC-002)**: The domain state machine mandates strict error precedence: `ErrWorkerNotFound` -> `ErrWorkerUnhealthy` -> `ErrWorkerTaskUnsupported` -> `ErrWorkerNoCapacity` -> `ErrNoQueuedJobs`. Locking the job first could obscure worker errors if no jobs are queued.
3. **Queue Split & Unbounded Growth (SEC-010)**: Storing jobs in an in-memory queue when running in PostgreSQL mode would allow an unbounded in-memory collection to grow without eviction (SEC-010) and diverge across coordinator restarts.
4. **Ordering Semantics (Approximate FIFO vs Strict Order)**: `created_at` timestamp resolution and alphanumeric ID tie-breaking introduce subtle ordering dynamics.
5. **Coordinator Crash Recovery**: A coordinator process termination mid-execution must not abandon in-flight worker attempts or lose queued work.
6. **Outage Handling & Dynamic Schema Verification**: The coordinator must verify schema compatibility on boot, expose database health via `/readyz`, and return standardized 503 responses with `Retry-After: 1` during database outages.

---

## Decision

### 1. Split Coordinator Dispatch Paths
- **Postgres Mode (`NewPostgresService`)**: The coordinator dispatches jobs strictly through `repo.DispatchNextJob(workerID)`. `SubmitJob` persists the job directly to the database in `QUEUED` state and completely bypasses the in-memory queue. This eliminates unbounded in-memory collection growth (SEC-010) and guarantees that all jobs survive coordinator process restarts.
- **Memory Mode (`NewService`)**: Preserves the in-memory queue + `repo.DispatchJob` path. Existing in-process tests pass unchanged, and the system can execute without external dependencies.
- **Shared Conformance**: The shared conformance test suite (`internal/storage/storagetest/conformance.go`) verifies identical functional behavior and error precedence between `InMemoryRepository` and PostgreSQL `Repository`.

### 2. Deadlock-Free Lock Ordering
To guarantee absolute freedom from deadlocks across all transactional operations, MiniCloud enforces the canonical lock hierarchy:
$$\text{jobs} \longrightarrow \text{workers} \longrightarrow \text{attempts}$$

In `repo.DispatchNextJob(workerID)`:
1. **Unlocked Worker Pre-Check**: The worker's row is read without locks. If the worker does not exist, is `UNHEALTHY`, or has 0 available slots, the typed domain error (`ErrWorkerNotFound`, `ErrWorkerUnhealthy`, `ErrWorkerNoCapacity`) is returned immediately. This preserves SEC-002 error precedence without acquiring locks.
2. **Lock Candidate Job (`jobs`)**: The candidate job matching worker supported tasks is locked first:
   ```sql
   SELECT id, task_type, payload, timeout_seconds, max_attempts
   FROM jobs
   WHERE state = 'QUEUED' AND task_type = ANY($1)
   ORDER BY created_at ASC, id ASC
   LIMIT 1
   FOR UPDATE SKIP LOCKED;
   ```
   If no rows match, `domain.ErrNoQueuedJobs` is returned.
3. **Lock Worker Row (`workers`)**: The worker row is locked second:
   ```sql
   SELECT total_slots, status, supported_tasks
   FROM workers
   WHERE id = $1
   FOR UPDATE;
   ```
4. **Re-Check Health and Capacity**: Health, supported tasks, and capacity are re-checked under the worker row lock (accounting for concurrent attempts). If capacity was exhausted by a concurrent transaction, the transaction rolls back cleanly with `ErrWorkerNoCapacity`.
5. **Derive Attempt Number & Insert Attempt (`attempts`)**: Attempt number is derived dynamically as `COALESCE(MAX(attempt_no), 0) + 1`, and the attempt is inserted.
6. **Update Job State**: Job is transitioned to `RUNNING` with `current_attempt_id` updated.

This eliminates deadlock hazards with `CommitAttempt` and `CancelJob`, verified by running `TestPostgresRepository_Concurrency_MixedOperationsStress` under `-race -count=20`.

### 3. Approximate FIFO Semantics
MiniCloud defines dispatch ordering in PostgreSQL mode as **Approximate FIFO**:
- **Mechanism**: `ORDER BY created_at ASC, id ASC` with `FOR UPDATE SKIP LOCKED`.
- **Trade-off Analysis**: A strictly sequential `IDENTITY` column was evaluated against `created_at`. An `IDENTITY` sequence introduces serialization bottlenecks, gap handling complexities, and index contention across concurrent submitters. In distributed execution platforms, approximate FIFO with `SKIP LOCKED` scales horizontally with zero contention while ensuring no job starvation.
- **Test Invariant**: Tests assert strict sequential ordering only for single-worker sequential polls. Under concurrency, tests assert exactly-once dispatch, zero duplicates, and zero lost jobs.

### 4. Crash Recovery & Failure Handling
- **Surviving Attempts**: When a coordinator process crashes (SIGKILL) mid-run, in-flight worker execution continues uninterrupted. When a replacement coordinator boots on the same port and connects to PostgreSQL, the worker's attempt completion is accepted with HTTP 200 COMMITTED and marked `SUCCEEDED`.
- **Surviving Queued Jobs**: Queued jobs remain durable in the database and are dispatched to surviving workers upon coordinator restart.
- **Phase 3 Failure Model Limitation**: As documented in `docs/failure-model.md`, a `RUNNING` job whose worker process dies stays `RUNNING` in Phase 3 until worker lease renewal expiration arrives in Phase 4.

### 5. Dynamic Schema Version Verification
Rather than hardcoding schema versions, the coordinator derives the required schema version dynamically from the embedded migration definitions (`migrator.LatestEmbeddedVersion()`). On startup, the coordinator queries `schema_migrations` and refuses to start if the database schema is unmigrated or behind the embedded version.

### 6. Health Probes & Outage Resilience
- **`/readyz` Endpoint**: Executes `repo.Ping(ctx)`. If PostgreSQL is reachable, returns 200 OK.
- **Outage Mapping**: Connection failures or query timeouts return HTTP 503 `SERVICE_UNAVAILABLE` with header `Retry-After: 1` and an opaque, credential-redacted error body `{"error":{"code":"SERVICE_UNAVAILABLE","message":"service temporarily unavailable"}}`.
- **Worker Client Backoff**: The worker client detects 5xx responses from the coordinator and applies exponential backoff with full jitter, distinguishing transient outages (`SERVICE_UNAVAILABLE`) from coordinator-enforced worker state (`WORKER_UNHEALTHY`, which returns HTTP 503 without `Retry-After`).

### 7. Migration 000003 Operational Note & Zero-Downtime Validation
Migration 000003 applies CHECK constraints (`chk_workers_total_slots_range`, `chk_workers_version_length`, `chk_workers_supported_tasks_count`) to the `workers` table using `ADD CONSTRAINT ... NOT VALID` followed by `VALIDATE CONSTRAINT`. This eliminates long-held exclusive table locks during schema migration. If pre-existing legacy rows violate the new bounds, `VALIDATE CONSTRAINT` fails loudly and rolls back the migration transaction, preventing unvalidated rows from being certified. Operators must remediate invalid worker rows before re-running migrations. Tested in `TestMigrator_PreExistingInvalidRow_ValidationFails`.

---

## Consequences

### Positive
- **Durability**: Zero state loss on coordinator restart.
- **Deadlock Free**: Canonical `jobs` -> `workers` -> `attempts` locking mathematically eliminates deadlocks.
- **Resource Safety**: Unbounded in-memory collections bypassed in production mode (SEC-010).
- **Graceful Degrade**: Worker agents automatically back off during database maintenance and resume without manual intervention.

### Negative / Trade-offs
- Lexical ID tie-breaking means jobs submitted in the exact same transaction timestamp sort textually rather than numerically (approximate FIFO).
- A dead worker process leaves a job in `RUNNING` until Phase 4 lease timeouts are introduced.
