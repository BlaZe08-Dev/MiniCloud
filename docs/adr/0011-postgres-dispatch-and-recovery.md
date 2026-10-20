# ADR 0011: PostgreSQL Dispatch Mechanics, Restart Recovery, and Database Invariants

## Status
Partially Accepted by Owner (2026-10-20)
- **Accepted:** Option A Dispatch (`SELECT ... FOR UPDATE SKIP LOCKED`), Dynamic Capacity Derivation in SQL, Coordinator Restart Guarantees, SHA-256 Hashed Lease Tokens, and Database-level Invariants.
- **PROPOSED (Under Evaluation):** Scheduler Participation Architecture in Pull Dispatch (Candidates A, B, and C).

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

### Option A: PostgreSQL-as-Queue using `SELECT ... FOR UPDATE SKIP LOCKED` (Accepted by Owner)
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

## 2. Scheduler Participation in a Pull Model: Re-Analysis Against Blueprint

### 2.1 Codebase Investigation: Confirmation of Unused Scheduler
An inspection of the active codebase confirms that **`scheduler.Scheduler` is currently unused in the production dispatch loop**:
- In [internal/coordinator/service.go:58](file:///home/blazeee/MiniCloud/internal/coordinator/service.go#L58), `scheduler scheduler.Scheduler` is stored on `Service` and injected via `NewService` / `NewServiceWithQueue` ([service.go:66-80](file:///home/blazeee/MiniCloud/internal/coordinator/service.go#L66-L80)).
- In `PollWork` ([service.go:290-386](file:///home/blazeee/MiniCloud/internal/coordinator/service.go#L290-L386)), `s.scheduler` is **never called**.
- In the active dispatch loop, when a worker polls, the coordinator claims the next FIFO item from the queue matching the worker's tasks and immediately assigns it to the caller.
- **Result:** Dispatch is currently strictly **first-poller-wins**. Integration test logs confirm `"job dispatched to worker"` appears, but `"scheduler decision"` is never logged in production flows. `scheduler.SelectWorker` is only exercised in isolated unit tests ([internal/scheduler/round_robin_test.go:32](file:///home/blazeee/MiniCloud/internal/scheduler/round_robin_test.go#L32)).

### 2.2 Blueprint Data Flow Alignment
Section 4 of the MiniCloud project blueprint specifies:
> *"A client submits a computational job, the control plane validates and tracks it, a scheduler assigns attempts to independent workers, and the platform records structured results."*
> *"Scheduler selects worker by policy (deterministic round-robin baseline)... Every decision logged (policy, candidates, selected, reason)."*

In an unconstrained pull model, workers poll independently. When a worker polls, the coordinator must decide which job to dispatch. We evaluate exactly three architectural candidates for how the scheduler participates in this data flow.

---

### 2.3 Candidate Comparison Matrix

| Evaluation Criteria | Candidate A: Scheduler Assigns, Worker Pulls (Pre-Assignment) | Candidate B: Hold Window at Poll Time (Poll Arbitration / Model 4) | Candidate C: Honest FIFO Pull Baseline (`fifo_pull`) |
|---|---|---|---|
| **Core Architecture** | Coordinator/Scheduler assigns `assigned_worker_id` when job is queued. Workers poll **only** for work assigned to them (`WHERE assigned_worker_id = $id`). Assignment is bounded by `assignment_expires_at`. | Worker $W_1$ polls. Coordinator inspects candidate job; scheduler picks $W_2$ by policy. Coordinator holds job for $W_2$ with a ~500ms window and returns HTTP 204 to $W_1$. | Honest admission that pull dispatch is first-poller-wins. Policy explicitly named `"fifo_pull"`. No round-robin claim in V1. Advanced scheduling deferred to V2. |
| **Blueprint Conformance** | **High.** Faithfully implements blueprint Section 4: scheduler actively selects worker before worker claims attempt. | **Medium.** Scheduler runs at poll time, but artificially holds jobs and rejects ready workers. | **Pragmatic.** Conforms to real distributed pull queues (e.g. SQS, Celery, Redis). Replaces theoretical round-robin with honest FIFO pull. |
| **Deterministic Testability** | **High.** Completely deterministic. No wall-clock sleeps or hold timeouts. Assignment is verified directly by worker ID query. | **Low / Flaky.** A 500ms hold window introduces wall-clock races in fast unit/integration tests (`go test -race`), risking timing flakes. | **Highest.** 100% deterministic. Standard FIFO ordering with zero timing dependencies. |
| **Correctness Under Worker Death / Slowness** | **High.** If assigned worker dies or lags, `assignment_expires_at` bounds the delay. Phase 4 lease/reaper sweep revokes stale assignment (`assigned_worker_id = NULL`) and re-schedules. | **Medium.** If target worker $W_2$ dies, job is trapped for 500ms before falling back to next poller. | **Highest.** Dead workers stop polling; only live, healthy workers with capacity pull jobs. Zero orphaned assignments. |
| **Implementation Complexity** | **Medium-High.** Adds `assigned_worker_id`, `assigned_at`, `assignment_expires_at` to `jobs`. Adds reassignment logic and sweep loop. | **High.** Complicates `SKIP LOCKED` query with hold-window filtering; requires tracking held rows in SQL or memory. | **Lowest.** Standard `SELECT ... FOR UPDATE SKIP LOCKED` without extra columns, state transitions, or hold timers. |
| **Fit with V2 Resource-Aware Scheduler** | **High.** Natural fit: V2 scheduler scores worker resources (CPU, memory) at assignment time and binds job to the optimal worker. | **Poor.** Score-based scheduling becomes unstable when evaluated ad-hoc per individual polling worker. | **High.** Provides the clean, unencumbered baseline against which V2 resource-aware policies are benchmarked. |
| **Fit with Redis Streams (ADR-0006)** | **Moderate.** Requires dedicated per-worker stream keys (e.g., `jobs:worker-1`) or partition routing. | **Very Poor.** Redis consumer groups (`XREADGROUP`) have no native hold-window primitive; requires custom Lua scripts. | **Native.** Directly maps to standard Redis Streams consumer groups (`XREADGROUP` where idle workers pull next stream message). |
| **Fairness of V1 vs V2 Benchmark** | Can introduce queue head-of-line delay if assigned workers have slight polling jitter. | Poor: introduces artificial 500ms wait penalties under load. | **Ideal Baseline.** Measures raw, unconstrained pull queue throughput and latency against which V2 scheduler overhead is measured. |
| **Decision Logging** | Logged at assignment time: `policy=round_robin, candidates=[...], selected=W_k, reason="assigned by policy"`. | Logged at poll time: `policy=round_robin, candidates=[...], selected=W_k, reason="held for target worker"`. | Logged at dispatch time: `policy=fifo_pull, candidates=[W_eligible...], selected=W_caller, reason="polled with available capacity"`. |

---

### 2.4 Concrete Fragility Analysis of Candidate A (Scheduler Pre-Assignment)
Candidate A addresses the owner's questions regarding worker failure modes concretely:
1. **When Assigned Worker Dies:**
   - Worker ceases heartbeating; background reaper marks worker `UNHEALTHY`.
   - Dispatch and reclamation query:
     ```sql
     UPDATE jobs
     SET assigned_worker_id = NULL, assigned_at = NULL, assignment_expires_at = NULL
     WHERE state = 'QUEUED'
       AND assigned_worker_id IS NOT NULL
       AND (assignment_expires_at < CURRENT_TIMESTAMP
            OR assigned_worker_id IN (SELECT id FROM workers WHERE status = 'UNHEALTHY'));
     ```
   - If `assignment_expires_at` passes or the assigned worker is reaped `UNHEALTHY`, the assignment is revoked. Phase 4 lease expiry sweeps seamlessly handle dead or unresponsive workers.
2. **When Assigned Worker is Slow:**
   - The `assignment_expires_at` window (configurable, default 5 seconds) strictly bounds latency. If the worker does not poll within 5s, the job falls back to the unassigned pool.
3. **When Assigned Worker is UNHEALTHY:**
   - Filtered out during candidate evaluation (`status = 'HEALTHY' AND available_slots > 0`). If it transitions to `UNHEALTHY` after assignment, the reaper sweep immediately invalidates the assignment.
4. **Schema / Columns Added in Candidate A:**
   - In `jobs` table:
     - `assigned_worker_id TEXT REFERENCES workers(id) ON DELETE SET NULL`
     - `assigned_at TIMESTAMPTZ`
     - `assignment_expires_at TIMESTAMPTZ`
   - State machine remains 9 states (`QUEUED` with non-null `assigned_worker_id`), avoiding modifications to `internal/domain` FSM transition rules.

---

### 2.5 Detailed Analysis of Candidate B (Model 4 Hold Window: Mechanics & Failure Modes)
Candidate B attempts to arbitrate scheduling at poll time by holding jobs for non-polling workers. A rigorous examination reveals fatal operational friction across four critical dimensions:

1. **Interaction with `SELECT ... FOR UPDATE SKIP LOCKED`:**
   - To implement a hold window in PostgreSQL, the dispatch query must inspect `held_for_worker_id` and `held_until`.
   - If worker $W_1$ polls and the scheduler determines the job should belong to $W_2$, the coordinator must mark `held_for_worker_id = 'W_2'` and `held_until = clock_timestamp() + INTERVAL '500 milliseconds'`.
   - Subsequent polling workers must execute a complex conditional query:
     ```sql
     SELECT id FROM jobs
     WHERE state = 'QUEUED'
       AND (held_for_worker_id IS NULL
            OR held_for_worker_id = $1
            OR held_until < clock_timestamp())
     ORDER BY created_at ASC
     FOR UPDATE SKIP LOCKED LIMIT 1;
     ```
   - In PostgreSQL, evaluating time-dependent conditions with `SKIP LOCKED` creates severe lock contention. Furthermore, `CURRENT_TIMESTAMP` is frozen at transaction start, necessitating `clock_timestamp()` which prevents PostgreSQL from utilizing standard index-range scans effectively.
2. **Idle Workers While Jobs Wait (Cluster Underutilization & Head-of-Line Blocking):**
   - Suppose worker $W_1$ is idle with 4 available slots and polls for work. Job $J_1$ is queued.
   - The coordinator's round-robin algorithm determines it is $W_2$'s turn. The coordinator places a 500ms hold on $J_1$ for $W_2$ and returns HTTP 204 (No Content) to $W_1$.
   - **Failure Mode:** $W_1$ sits completely idle while work is available. If $W_2$ is delayed by GC, network jitter, or a 1-second polling interval, $J_1$ sits trapped in `QUEUED` for 500ms despite an idle worker actively asking for work. This degrades cluster throughput and penalizes job latency.
3. **Interaction with Test Determinism:**
   - MiniCloud's test suite enforces `-race -count=1` and sub-second execution across unit and integration tests.
   - A 500ms wall-clock arbitration window destroys test determinism:
     - Tests must either insert artificial `time.Sleep(600 * time.Millisecond)` to test hold expiration (inflating test suite runtimes from 200ms to tens of seconds).
     - Or fast poll loops interleave unpredictably across goroutines, causing race detector flakes and intermittent CI failures.
4. **Interaction with Redis Consumer Groups in V2 (ADR-0006):**
   - In V2, MiniCloud evaluates Redis Streams dispatch via consumer groups (`XREADGROUP GROUP workers W1 COUNT 1 STREAMS jobs >`).
   - Redis Streams consumer groups maintain a Pending Entries List (PEL). There is **no native concept of a "hold window"** for another consumer. An entry read by $W_1$ is immediately bound to $W_1$'s PEL.
   - Supporting Model 4 in Redis would require multi-stage Lua scripts, secondary sorted sets for delayed visibility, or custom locks, completely abandoning native Redis Streams semantics and creating high architectural friction.

---

### 2.6 Detailed Analysis of Candidate C (Honest FIFO Pull Baseline: Architectural & Benchmark Integrity)
Candidate C resolves the tension by embracing the reality of pull-based distributed queues:

1. **Architectural Honesty:**
   - In any pull-based architecture where workers poll a central queue, work is fundamentally dispatched first-poller-wins.
   - Calling this "round-robin" is false. Naming the policy `"fifo_pull"` restores architectural honesty and engineering integrity.
2. **Repurposing or Deferring the Scheduler Interface:**
   - In V1, the scheduler interface can either:
     - **Option C.1 (Repurposed for Eligibility & Audit):** In `PollWork`, `s.scheduler.SelectWorker` evaluates all currently registered healthy workers with capacity, verifies the calling worker is an eligible candidate, and logs the decision:
       ```go
       slog.Info("scheduler decision",
           "policy", "fifo_pull",
           "candidates", eligibleWorkerIDs,
           "selected", callerWorkerID,
           "reason", "worker polled with available capacity",
       )
       ```
       This fulfills the blueprint's mandate that every assignment decision is logged with policy, candidates, selected, and reason.
     - **Option C.2 (Deferred to V2):** In V1, dispatch is handled directly by `queue.Queue` / PostgreSQL `SKIP LOCKED`, and the scheduler interface is deferred to V2 where resource-aware scoring (CPU, memory, affinity) genuinely selects among workers.
3. **Benchmark Fairness for V2 (ADR-0006):**
   - ADR-0006 mandates comparing PostgreSQL against Redis Streams.
   - A scientific benchmark requires both dispatch backends to implement identical, unencumbered FIFO pull semantics.
   - If PostgreSQL is artificially slowed down by hold windows or pre-assignment locking, the benchmark results will be skewed. Candidate C provides the pure, high-throughput baseline needed for a legitimate comparison.
4. **Zero Operational Fragility:**
   - Dead workers do not poll. Fast workers pull work faster; slow workers pull work slower.
   - No hold timeouts, no reassignment sweepers, and zero orphaned assignments.

---

### 2.7 Cost Breakdown Across Slices 3a–3e

| Sub-Slice | Candidate A (Scheduler Pre-Assignment) | Candidate B (Hold Window / Model 4) | Candidate C (Honest FIFO Pull Baseline) |
|---|---|---|---|
| **Slice 3a (In-Memory Atomicity Refactor)** | **Moderate:** Update `InMemoryRepository` to store assignment fields; update `SubmitJob` or add `ScheduleNextJobs` calling `s.scheduler.SelectWorker`; filter `PollWork` by assigned worker ID; add accept-timeout unit tests. | **High:** Add hold-window map and timer synchronization in memory; add tests for hold expiration. | **Minimal:** Integrate `s.scheduler` into `PollWork` to log `fifo_pull` decisions with eligible candidates. Zero schema or timer changes. |
| **Slice 3b (Migrations & Schema)** | **Moderate:** Add `assigned_worker_id`, `assigned_at`, `assignment_expires_at` columns, foreign keys, and index `idx_jobs_assigned` in `000001_initial_schema.sql`. | **Moderate:** Add `held_for_worker_id`, `held_until` columns and indexes in initial schema. | **Minimal:** Standard initial schema with `jobs.state = 'QUEUED'` and `idx_jobs_dispatch`. Zero extra columns. |
| **Slice 3c (Postgres Repository)** | **Moderate:** Implement assignment update query and expired assignment reclamation query in PostgreSQL. | **High:** Implement complex conditional `SKIP LOCKED` query evaluating hold windows and timestamps. | **Minimal:** Implement standard transactional `SELECT ... FOR UPDATE SKIP LOCKED` query. |
| **Slice 3d (Dispatch Transport)** | **Moderate:** Queue interface carries assigned worker routing hints or queries assigned jobs. | **High:** Complex queue claim logic with hold fallbacks; high risk of V2 Redis incompatibility. | **Minimal:** Direct `internal/queue.Queue` backed by `PostgresQueue`. Clean, high-throughput, native Redis alignment. |
| **Slice 3e (Restart Recovery Tests)** | **Moderate:** Add test verifying that unaccepted assignments recover across coordinator reboot. | **High:** Timing-sensitive integration tests with high risk of race flakes. | **Minimal:** Fast, deterministic restart recovery tests verifying queued jobs and running attempt commits. |

---

### 2.8 Recommendation & Rationale

**Recommendation:** The design team recommends **Candidate C (FIFO Pull Baseline, renamed honestly)** for MiniCloud V1, with the following rationale:

1. **Architectural Honesty:** In distributed systems, pull-based worker architectures are fundamentally queue-driven. Naming the policy `"fifo_pull"` accurately reflects the system's operational model rather than simulating push scheduling through artificial hold delays.
2. **Benchmark Integrity for V2 (ADR-0006):** In V2, we introduce resource-aware scheduling and Redis Streams. To scientifically prove the benefit of complex scheduling algorithms, one must compare against a lean, high-throughput FIFO baseline. If V1 is burdened with complex pre-assignment or hold timers, benchmark comparisons against V2 will be confounded by artificial delays.
3. **Structured Audit Conformance:** Candidate C fully satisfies the blueprint's audit logging mandate. On every poll dispatch, the coordinator evaluates all currently registered healthy workers with capacity and logs the decision.
4. **Fallback Path:** If the owner strictly insists on coordinator-driven worker selection prior to pull in V1, **Candidate A** is the only sound alternative. Candidate B (hold window) is rejected due to test flakiness, idle worker stalling, and friction with Redis Streams.

*Status: PROPOSED for Owner Decision prior to Slice 3d.*

---

## 3. Dynamic Capacity Derivation (No Counter Drift)

In distributed execution systems, storing a mutable `available_slots` integer column that is incremented on commit and decremented on dispatch is an anti-pattern. If a coordinator process crashes, a transaction rolls back, or a worker process is reaped, stored counters drift.

In MiniCloud PostgreSQL storage:
- `workers` stores `total_slots INTEGER NOT NULL CHECK (total_slots > 0)`.
- Available capacity is **derived dynamically in SQL from active running attempts**:
  ```sql
  available_slots = total_slots - (SELECT COUNT(*) FROM attempts WHERE worker_id = workers.id AND state = 'RUNNING')
  ```
- Evaluated directly in the dispatch transaction (`worker_check` CTE) and exposed via the `worker_capacity` view.
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

## Owner Decisions & Action Items

1. **Postgres-as-Queue Baseline (Option A):** **ACCEPTED**. Use `SELECT ... FOR UPDATE SKIP LOCKED` for Phase 3 dispatch.
2. **Dynamic Capacity:** **ACCEPTED**. Derive worker capacity in SQL from active attempts to prevent counter drift.
3. **Surviving Worker Restart Guarantees:** **ACCEPTED**. Surviving workers can commit completed attempts after a coordinator reboot.
4. **Hashed Tokens:** **ACCEPTED**. Store `SHA-256(lease_token)` in PostgreSQL; verify incoming raw tokens via hash comparison.
5. **Scheduler Participation Architecture:** **PROPOSED (Awaiting Final Owner Selection)** between Candidate A (Pre-Assignment with Bounded Accept Window) and Candidate C (Honest FIFO Pull Baseline with Decision Logging). Candidate B is not accepted.
