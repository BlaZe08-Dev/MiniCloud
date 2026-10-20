# ADR 0011: PostgreSQL Dispatch Mechanics, Restart Recovery, and Database Invariants

## Status
Accepted by Owner (2026-10-20, Conditional on Scheduler Integration)

## Context
In Milestones 1 and 2, MiniCloud's coordinator orchestrated work in-memory. In Phase 3, state durability is established in PostgreSQL. To guarantee correctness in a distributed environment:
1. We must select the dispatch mechanism for pulling jobs from durable storage.
2. We must resolve how the scheduler participates in a pull-based coordination protocol (addressing the first-poller-wins gap).
3. We must define coordinator restart recovery rules, explicitly documenting what Phase 3 guarantees and what is deferred to Phase 4.
4. We must eliminate capacity counter drift by dynamically deriving worker slot availability in SQL.
5. We must secure lease tokens against database compromise by storing only cryptographic hashes.
6. We must enforce redelivery idempotency and prevent duplicate attempts using PostgreSQL database-level constraints.

---

## 1. Dispatch Options & Trade-Off Analysis

We evaluate two architectural approaches for dispatching work from PostgreSQL to polling workers:

### Option A: PostgreSQL-as-Queue using `SELECT ... FOR UPDATE SKIP LOCKED` (Selected by Owner)
- **Mechanism:**
  When an idle worker with capacity calls `POST /v1/workers/{id}/poll`, the coordinator executes a single, atomic PostgreSQL transaction:
  ```sql
  WITH worker_check AS (
      SELECT w.id
      FROM workers w
      WHERE w.id = $1
        AND w.status = 'HEALTHY'
        AND (w.total_slots - (SELECT COUNT(*) FROM attempts a WHERE a.worker_id = w.id AND a.state = 'RUNNING')) > 0
  ),
  candidate AS (
      SELECT j.id, j.task_type, j.payload, j.timeout_seconds
      FROM jobs j, worker_check wc
      WHERE j.state = 'QUEUED'
        AND j.task_type = ANY($2) -- worker.SupportedTasks
      ORDER BY j.created_at ASC
      FOR UPDATE OF j SKIP LOCKED
      LIMIT 1
  ),
  updated_job AS (
      UPDATE jobs
      SET state = 'RUNNING',
          current_attempt_id = $3,
          updated_at = CURRENT_TIMESTAMP
      FROM candidate
      WHERE jobs.id = candidate.id
      RETURNING jobs.id, jobs.task_type, jobs.payload, jobs.timeout_seconds
  )
  INSERT INTO attempts (
      id, job_id, attempt_no, worker_id, lease_token_hash, state, started_at
  )
  SELECT
      $3, uj.id, (SELECT COALESCE(MAX(attempt_no), 0) + 1 FROM attempts WHERE job_id = uj.id),
      $1, $4, 'RUNNING', CURRENT_TIMESTAMP
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

---

## 2. Scheduler Participation in a Pull Model (Investigation & Resolution)

### 2.1 Codebase Investigation: Confirmation of Unused Scheduler
An inspection of the active codebase confirms that **`scheduler.Scheduler` is currently unused in the production dispatch path**:
- In [internal/coordinator/service.go:58](file:///home/blazeee/MiniCloud/internal/coordinator/service.go#L58), `scheduler scheduler.Scheduler` is stored on the `Service` struct, and injected via `NewService` / `NewServiceWithQueue` ([service.go:66-80](file:///home/blazeee/MiniCloud/internal/coordinator/service.go#L66-L80)).
- In `PollWork` ([service.go:290-386](file:///home/blazeee/MiniCloud/internal/coordinator/service.go#L290-L386)), `s.scheduler` is **never called**.
- In the active dispatch loop, when a worker polls, the coordinator claims the next FIFO item from the queue matching the worker's tasks and immediately assigns it to the caller.
- **Result:** Dispatch is currently **first-poller-wins**. Integration test logs confirm `"job dispatched to worker"` appears, but `"scheduler decision"` is never logged in production flows. `scheduler.SelectWorker` is only exercised in isolated unit tests ([internal/scheduler/round_robin_test.go:32](file:///home/blazeee/MiniCloud/internal/scheduler/round_robin_test.go#L32)).

### 2.2 How Scheduler Participates Under a Pull Protocol
In a push model, the coordinator schedules a job across all workers. In a pull model, workers poll asynchronously. To satisfy the blueprint requirement that every scheduling decision is evaluated and logged (`policy`, `candidates`, `selected`, `reason`), we evaluate four models:

#### Model 1: Worker Match Decision at Poll Time (Scheduler-Gated Pull)
- **Concept:** When Worker $W_i$ polls, coordinator inspects the next pending job $J$. The coordinator calls `s.scheduler.SelectWorker(eligibleWorkers, J.TaskType)`.
- If the selected worker is $W_i$, the job is dispatched to $W_i$, and the decision is logged.
- If another worker $W_k$ is selected, the coordinator returns HTTP 204 `NO_QUEUED_JOBS` to $W_i$, preserving job $J$ for $W_k$.
- **Trade-off:** Strictly enforces round-robin across workers, but risks head-of-line delay if $W_k$ has a slow polling cycle or jitter.

#### Model 2: Candidate Job Ranking at Poll Time (`SelectJob`)
- **Concept:** When Worker $W_i$ polls, coordinator queries the top $N$ queued jobs compatible with $W_i$. The scheduler policy ranks and selects the *best job* for $W_i$ (e.g., shortest job first, priority, or FIFO tie-breaking).
- **Trade-off:** Fast and non-stalling, but does not balance work across multiple idle workers if one worker polls at a higher frequency.

#### Model 3: Central Pre-Assignment with Pull Pickup
- **Concept:** Coordinator runs an internal scheduling loop that assigns `target_worker_id` onto queued jobs in the database. Workers poll only for jobs matching their worker ID.
- **Trade-off:** High complexity. If an assigned worker dies or delays, the job is stuck until a re-scheduling timeout fires.

#### Model 4: Hybrid Pull with Scheduler Arbitration (Recommended)
- **Concept:**
  When Worker $W_i$ polls for work:
  1. The coordinator checks for candidate `QUEUED` jobs supporting $W_i$'s task types.
  2. For the candidate job, the coordinator evaluates currently eligible workers (healthy workers with available slots supporting the task).
  3. The coordinator invokes `s.scheduler.SelectWorker(eligibleWorkers, taskType)`.
  4. The structured scheduling decision is logged:
     `slog.Info("scheduler decision", "policy", decision.Policy, "candidates", decision.Candidates, "selected", decision.Selected, "reason", decision.Reason)`
  5. If $W_i$ is selected, dispatch proceeds immediately.
  6. If multiple eligible workers are idle and $W_k$ is chosen by round-robin, the coordinator reserves the job for $W_k$ with a short scheduling window (e.g., 500ms). If $W_k$ does not claim it within the window, the job falls back to the next poller. If $W_i$ is the only idle worker with capacity, `SelectWorker` naturally chooses $W_i$.
- **Recommendation:** Adopt Model 4 for Slice 3d. This brings `scheduler.Scheduler` into the real execution path, emits audit decision logs on every dispatch, and provides an authentic, policy-driven baseline for V2.

---

## 3. Dynamic Capacity Derivation (No Counter Drift)

In distributed execution systems, storing a mutable `available_slots` integer column that is incremented on commit and decremented on dispatch is an anti-pattern. If a coordinator process crashes, a transaction rolls back, or a worker process is reaped, stored counters drift.

In MiniCloud PostgreSQL storage:
- `workers` stores `total_slots INTEGER NOT NULL CHECK (total_slots > 0)`.
- Available capacity is **derived dynamically in SQL from active running attempts**:
  ```sql
  available_slots = total_slots - (SELECT COUNT(*) FROM attempts WHERE worker_id = workers.id AND state = 'RUNNING')
  ```
- This derivation is evaluated directly in the dispatch transaction (`worker_check` CTE) and exposed via the `worker_capacity` view.
- This guarantees zero counter drift across restarts, crashes, or ungraceful worker terminations.

---

## 4. Coordinator Restart Recovery Rules (Phase 3 Semantics)

Because Phase 4 introduces automated lease renewal and lease expiration sweeps, we state precisely what Phase 3 guarantees and what it does not:

| Entity / State | Behavior on Coordinator Restart | Phase 3 Guarantee |
|---|---|---|
| **`QUEUED` Jobs** | Remain `QUEUED` in PostgreSQL. | **Guaranteed:** Immediately available for polling workers upon coordinator reboot. Zero job loss. |
| **`RUNNING` Jobs (Worker Survives)** | Job and active attempt remain recorded in PostgreSQL. The independent worker process finishes execution and submits `POST /v1/jobs/{id}/attempts/{attempt_id}/result`. | **Guaranteed:** The restarted coordinator accepts the result! Because `attempts` and `jobs` are durable, the coordinator validates `job.CurrentAttemptID`, verifies the lease token hash and worker ID, updates the attempt to `COMPLETED`/`FAILED`, transitions the job to `SUCCEEDED`/`FAILED`, and stores the result. |
| **`RUNNING` Jobs (Worker Died)** | Worker process died before or during restart. No result will ever arrive. | **Not Guaranteed in Phase 3:** In Phase 3, the job remains `RUNNING`. Phase 4 introduces the lease expiry sweeper to detect abandoned attempts and trigger re-queueing. |
| **`RETRYING` Jobs** | Retry state transitions are deferred to Phase 4. | N/A in Phase 3. |
| **Workers** | Worker registration records persist in PostgreSQL. Surviving workers continue sending heartbeats; coordinator restores/maintains their `HEALTHY` status. | **Guaranteed:** Surviving workers do not need to re-register after coordinator reboot; heartbeats update PostgreSQL `last_heartbeat_at`. |
| **Attempts History** | Monotonic attempt records and audit timestamps persist. | **Guaranteed:** Historical attempts are never lost across restarts. |

---

## 5. Cryptographic Hashing of Lease Tokens

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

## 6. Redelivery & Duplicate-Attempt Prevention at the Database Level

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
   Enforces that **no job can ever have more than one non-terminal (`RUNNING`) attempt** at any point in time. Any race condition attempting to start a parallel attempt triggers a PostgreSQL unique constraint violation (`SQLSTATE 23505`).

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
   Guarantees that exactly one committed result can exist per job.

5. **Database-Time Invariant & Clock Semantics:**
   - All lease timeouts, heartbeat comparisons, and attempt durations must use PostgreSQL database timestamps (`CURRENT_TIMESTAMP` for transaction boundaries; `clock_timestamp()` where statement freshness matters).
   - Eliminates worker/coordinator clock skew vulnerabilities.

---

## Owner Decision & Staged Conditions

The repository owner has **APPROVED** Option A (`SELECT ... FOR UPDATE SKIP LOCKED`) subject to resolving scheduler participation in Slice 3d:
1. **Postgres-as-Queue Baseline:** Adopt `SELECT ... FOR UPDATE SKIP LOCKED` for Phase 3 dispatch.
2. **Scheduler Participation:** Implement Hybrid Pull with Scheduler Arbitration (Model 4) in Slice 3d so that `scheduler.SelectWorker` is actively invoked at dispatch time and structured decision logs are emitted.
3. **Dynamic Capacity:** Derive worker capacity in SQL from active attempts to prevent counter drift.
4. **Surviving Worker Restart Guarantees:** Guarantee that surviving workers can commit completed attempts after a coordinator reboot.
5. **Hashed Tokens:** Store `SHA-256(lease_token)` in PostgreSQL; verify incoming raw tokens via hash comparison.
