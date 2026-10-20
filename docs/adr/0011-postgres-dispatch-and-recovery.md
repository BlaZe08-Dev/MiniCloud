# ADR 0011: PostgreSQL Dispatch Mechanics, Restart Recovery, and Database Invariants

## Status
PROPOSED (Awaiting Owner Approval)

## Context
In Milestones 1 and 2, MiniCloud's coordinator orchestrated work in-memory. In Phase 3, state durability is established in PostgreSQL. To guarantee correctness in a distributed environment:
1. We must select the dispatch mechanism for pulling jobs from durable storage.
2. We must define the coordinator restart recovery rules, explicitly documenting what Phase 3 guarantees and what is deferred to Phase 4.
3. We must secure lease tokens against database compromise by storing only cryptographic hashes.
4. We must enforce redelivery idempotency and prevent duplicate attempts using PostgreSQL database-level constraints.

---

## 1. Dispatch Options & Trade-Off Analysis

We evaluate two architectural approaches for dispatching work from PostgreSQL to polling workers:

### Option A: PostgreSQL-as-Queue using `SELECT ... FOR UPDATE SKIP LOCKED` (Recommended)
- **Mechanism:**
  When an idle worker with capacity calls `POST /v1/workers/{id}/poll`, the coordinator executes a single, atomic PostgreSQL transaction:
  ```sql
  WITH candidate AS (
      SELECT id, task_type, payload, timeout_seconds
      FROM jobs
      WHERE state = 'QUEUED'
        AND task_type = ANY($1) -- worker.SupportedTasks
      ORDER BY created_at ASC
      FOR UPDATE SKIP LOCKED
      LIMIT 1
  ),
  updated_job AS (
      UPDATE jobs
      SET state = 'RUNNING',
          current_attempt_id = $2,
          updated_at = CURRENT_TIMESTAMP
      FROM candidate
      WHERE jobs.id = candidate.id
      RETURNING jobs.id, jobs.task_type, jobs.payload, jobs.timeout_seconds
  )
  INSERT INTO attempts (
      id, job_id, attempt_no, worker_id, lease_token_hash, state, started_at
  )
  SELECT
      $2, uj.id, (SELECT COALESCE(MAX(attempt_no), 0) + 1 FROM attempts WHERE job_id = uj.id),
      $3, $4, 'RUNNING', CURRENT_TIMESTAMP
  FROM updated_job uj
  RETURNING job_id;
  ```
- **Crash Behavior:** If the coordinator crashes, all state remains fully consistent. Jobs that were not claimed remain `QUEUED`. Jobs claimed prior to the crash are already recorded as `RUNNING` with their associated attempt in PostgreSQL.
- **Duplicate Delivery:** Exactly zero duplicate claims. PostgreSQL row-level locks combined with `SKIP LOCKED` guarantee that concurrent polling workers (even across multiple coordinator instances) never receive the same queued job.
- **Complexity:** Eliminates the dual-state synchronization problem between an external in-memory queue and durable database storage.
- **Fair V1 Baseline for V2 Redis Streams Comparison (ADR-0006):**
  ADR 0006 established that V1 provides the canonical PostgreSQL pull-dispatch baseline to compare against V2 Redis Streams. Testing transactional `SKIP LOCKED` directly measures the throughput, latency, and connection scaling overhead of relational queueing vs append-only log streaming.

### Option B: In-Memory Queue Rebuilt from PostgreSQL at Startup
- **Mechanism:** The coordinator maintains `internal/queue.InMemoryQueue`. At startup, it queries `SELECT id, task_type FROM jobs WHERE state = 'QUEUED' ORDER BY created_at ASC` and repopulates the in-memory queue.
- **Crash Behavior:** Vulnerable to state divergence. If the coordinator crashes while jobs are in-flight in memory, in-flight markers vanish. If multiple coordinator instances run, their independent in-memory queues cause race conditions and duplicate claim attempts.
- **Evaluation:** Rejected. It creates a dual-source-of-truth hazard, increases coordination complexity, and does not represent an authentic PostgreSQL queue baseline.

### Dispatch Recommendation
Implement a `PostgresQueue` satisfying `internal/queue.Queue` backed by `SELECT ... FOR UPDATE SKIP LOCKED`.

---

## 2. Coordinator Restart Recovery Rules (Phase 3 Semantics)

Because Phase 4 introduces automated lease renewal and lease expiration sweeps, we must state precisely what Phase 3 guarantees and what it does not:

| Entity / State | Behavior on Coordinator Restart | Phase 3 Guarantee |
|---|---|---|
| **`QUEUED` Jobs** | Remain `QUEUED` in PostgreSQL. | **Guaranteed:** Immediately available for polling workers upon coordinator reboot. Zero job loss. |
| **`RUNNING` Jobs (Worker Survives)** | Job and active attempt remain recorded in PostgreSQL. The independent worker process finishes execution and submits `POST /v1/jobs/{id}/attempts/{attempt_id}/result`. | **Guaranteed:** The restarted coordinator accepts the result! Because `attempts` and `jobs` are durable, the coordinator validates `job.CurrentAttemptID`, verifies the lease token hash and worker ID, updates the attempt to `COMPLETED`/`FAILED`, transitions the job to `SUCCEEDED`/`FAILED`, and stores the result. |
| **`RUNNING` Jobs (Worker Died)** | Worker process died before or during restart. No result will ever arrive. | **Not Guaranteed in Phase 3:** In Phase 3, the job remains `RUNNING`. Phase 4 introduces the lease expiry sweeper to detect abandoned attempts and trigger re-queueing. |
| **`RETRYING` Jobs** | Retry state transitions are deferred to Phase 4. | N/A in Phase 3. |
| **Workers** | Worker registration records persist in PostgreSQL. Surviving workers continue sending heartbeats; coordinator restores/maintains their `HEALTHY` status. | **Guaranteed:** Surviving workers do not need to re-register after coordinator reboot; heartbeats update PostgreSQL `last_heartbeat_at`. |
| **Attempts History** | Monotonic attempt records and audit timestamps persist. | **Guaranteed:** Historical attempts are never lost across restarts. |

---

## 3. Cryptographic Hashing of Lease Tokens

To adhere to the principle of least privilege and protect worker execution credentials against passive database inspection or SQL snapshot exposure:
1. **Token Generation:** When dispatching an attempt, the coordinator generates a cryptographically random 256-bit hexadecimal string (`lease_token`).
2. **Hashed Persistence:** The raw token is **never stored in the database**. The coordinator computes `SHA-256(lease_token)` and stores the 64-character hex digest in `attempts.lease_token_hash`.
3. **Dispatch Delivery:** The raw `lease_token` is transmitted once over HTTP to the assigned worker in `WorkAssignment.LeaseToken`.
4. **Result Verification:** When the worker commits its result via `POST /v1/jobs/{id}/attempts/{attempt_id}/result`, it provides the raw `lease_token`. The coordinator hashes the incoming token (`SHA-256(req.LeaseToken)`) and performs a constant-time comparison against `attempts.lease_token_hash` in PostgreSQL:
   ```sql
   SELECT id FROM attempts
   WHERE id = $1 AND lease_token_hash = $2;
   ```
   If the hash does not match, the commit is rejected with HTTP 409 `STALE_ATTEMPT_REJECTED`.

---

## 4. Redelivery & Duplicate-Attempt Prevention at the Database Level

To guarantee at-least-once execution without corrupted state under network partitions or concurrent worker delivery:

1. **State-Guarded Transition:**
   Job state transitions require an explicit expected source state in the `WHERE` clause:
   ```sql
   UPDATE jobs
   SET state = 'RUNNING', current_attempt_id = $2, updated_at = CURRENT_TIMESTAMP
   WHERE id = $1 AND state = 'QUEUED';
   ```
   If a duplicate delivery signal arrives for a job that is already running, cancelled, or terminal, the update matches 0 rows and fails fast.

2. **Database-Level Mutual Exclusion (Partial Unique Index):**
   ```sql
   CREATE UNIQUE INDEX idx_attempts_active_per_job
   ON attempts (job_id)
   WHERE state = 'RUNNING';
   ```
   This index enforces that **no job can ever have more than one non-terminal (`RUNNING`) attempt** at any point in time. Any race condition attempting to start a parallel attempt triggers a PostgreSQL unique constraint violation (`SQLSTATE 23505`).

3. **Monotonic Sequence Integrity:**
   ```sql
   CONSTRAINT uq_attempts_job_attempt_no UNIQUE (job_id, attempt_no);
   ```
   Guarantees distinct, non-overlapping attempt numbers per job.

4. **Single Terminal Result Constraint:**
   ```sql
   CREATE TABLE results (
       job_id TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE RESTRICT,
       attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
       exit_code INTEGER NOT NULL,
       output TEXT NOT NULL,
       error TEXT NOT NULL,
       committed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
   );
   ```
   The primary key on `job_id` guarantees that exactly one committed result can exist per job.

5. **Database-Time Invariant:**
   All lease timeouts, heartbeat comparisons, and attempt durations must use PostgreSQL database timestamps (`CURRENT_TIMESTAMP` / `now()`), eliminating clock drift between distributed worker hosts.

---

## Decision Proposal

1. **Adopt `SELECT ... FOR UPDATE SKIP LOCKED`** as the dispatch mechanism for PostgreSQL in Phase 3.
2. **Guarantee Surviving-Worker Commits across Restarts:** Formally guarantee that surviving workers can commit completed attempts after a coordinator restart.
3. **Store Lease Tokens Hashed with SHA-256.**
4. **Enforce Invariants in SQL:** Use partial unique indexes, foreign key constraints, and state-guarded `UPDATE` statements to prevent duplicate attempts and invalid transitions at the storage engine level.
