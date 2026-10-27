# ADR 0015: Retry Policy, Failure Classification Taxonomy, and At-Least-Once Delivery

## Status
Accepted by Owner (2026-10-26)
- **Accepted Owner Decisions**:
  - Exponential backoff with base 1 s, cap 30 s, full jitter ($[0.5, 1.5]$), default `max_attempts` 3 (range $1..20$).
  - 3-tier failure classification taxonomy with unknown errors defaulting to retryable (`ClassRetryable`).
  - Validation-style failures of allowlisted tasks are permanent (`ClassPermanent`).
  - Success (exit code 0) is `AttemptCompleted` and not a failure classification.
  - `LOST` attempts consume 1 attempt from the `max_attempts` budget.
  - No separate attempt-level execution timeout in V1.
  - Cooperative Go context cancellation in V1 (hard kill deferred to V3 Docker isolation).
  - Direct atomic transition from `RETRYING` to `RUNNING` via pull dispatch query (justified departure from blueprint's conceptual `RETRYING -> QUEUED`).
  - In-memory repository retry parity with injectable `Clock`.
- **Proposed (Implementation Details)**: Sub-slices 4a–4e implementation details remain proposed until merged.

## Context
MiniCloud jobs are finite computational tasks that may encounter transient errors (network timeouts, node preemption, database connection blips) or permanent errors (bad input syntax, unhandled application exceptions, missing parameters).

Blueprint Section 5 and Section 13 mandate that:
1. Retries must be bounded by an explicit `max_attempts` budget.
2. The platform must distinguish retryable vs. permanent errors.
3. The platform provides **at-least-once execution semantics** under retries. It must explicitly disclaim exactly-once execution for arbitrary external side effects.
4. Retry attempts must maintain monotonic attempt numbering and respect the single-active-attempt database index.
5. Backoff must incorporate randomized jitter to prevent thundering herds and retry storms against the coordinator and database.

---

## Decision

### 1. Failure Classification Taxonomy & Success Distinction

MiniCloud introduces a structured failure classification taxonomy:

```text
                     [Execution Outcome]
                              │
               ┌──────────────┴──────────────┐
               ▼                             ▼
       [Exit Code 0: Success]      [Non-Zero / Fault: Failure]
       • Attempt: COMPLETED                  │
       • Job: SUCCEEDED        ┌─────────────┴─────────────┐
                               ▼                           ▼
                      [Permanent Failure]         [Retryable Failure]
                      • Bad input / validation    • Worker crash / lease expiry
                      • Invalid payload schema    • Transient network timeout
                      • Unsupported task options  • Database lock contention
                              │                           │
                              ▼                           ▼
                     Transition to FAILED        Evaluate max_attempts:
                                                 If attempt < max: RETRYING
                                                 If attempt >= max: FAILED
```

#### Success is Not a Failure Class
- **Clarification**: Exit code 0 represents a successfully completed attempt (`domain.AttemptCompleted`), transitioning the job to `domain.StateSucceeded`. Success is **not** a failure class; `ErrorClassification` is strictly empty or omitted when an attempt succeeds. (Accepted by owner).

#### Classification Categories for Failed Attempts
1. **`ClassPermanent` (`"PERMANENT"`)**:
   - The failure is deterministic; repeating execution with the same payload and environment will reproduce the exact same failure.
   - **Validation-Style Failures**: All allowlisted in-process tasks (`echo`, `sleep`, `sha256_loop`) classify input validation failures (e.g., negative duration for `sleep`, malformed or missing JSON payload, unparseable round count for `sha256_loop`) as `ClassPermanent`. (Accepted by owner).
   - Action: Job transitions immediately to `FAILED` without retrying, conserving cluster capacity.
2. **`ClassRetryable` (`"RETRYABLE"`)**:
   - The failure is transient, environmental, or node-specific; repeating execution on the same or another worker has a high probability of success.
   - Examples: Lease expired (`LOST`), worker process crash/SIGKILL, transient connection reset, database query timeout.
   - Action: If `attempt_no < max_attempts`, job transitions to `RETRYING`; if `attempt_no >= max_attempts`, job transitions to `FAILED`.
3. **`ClassUnknown` (`"UNKNOWN"`)**:
   - The failure cause cannot be definitively determined from the process exit code or error string.
   - Action: Default policy treats `ClassUnknown` as **`ClassRetryable`** until `max_attempts` is exhausted. (Accepted by owner).

#### Three-Tier Classification Authority
1. **Tier 1 (Task / Executor)**: The task implementation inspects execution. Predefined tasks emit structured exit codes and set `AttemptOutcome.ErrorClassification`.
2. **Tier 2 (Worker Agent)**: If the executor process terminates abnormally (killed by OS signal, panic recovery, context deadline exceeded), the worker agent classifies the outcome before reporting.
3. **Tier 3 (Coordinator — Final Arbiter)**: The coordinator validates the report against job policy. If the worker crashed without reporting, the lease expiry sweeper (ADR-0014) classifies the attempt as `ClassRetryable`.

### 2. Bounded Retries & Backoff with Jitter
- **Attempt Upper Bound**: `max_attempts` is configured at job submission ($1..20$, default 3; enforced by SQL check constraint `chk_jobs_max_attempts`).
- **`LOST` Attempts Consume `max_attempts`**:
  - Every attempt marked `LOST` by the lease sweeper (due to worker crash, network partition, or lease expiration) **consumes 1 attempt** from the job's `max_attempts` budget. (Accepted by owner).
  - **Consequence for Prolonged Worker Outages**: If a worker or fleet suffers a persistent outage exceeding $\text{max\_attempts} \times \text{backoff}$, repeated attempt losses consume the entire `max_attempts` budget, causing the job to transition to `FAILED` due to infrastructure loss rather than task computation errors.
  - **`error_class` Label**: When an attempt is swept to `LOST`, `attempts.error_class` is stamped with `"INFRASTRUCTURE_FAULT"` (or `"LEASE_EXPIRED"`). If a job exhausts `max_attempts` due to sweeper sweeps, the resulting job failure summary and audit event explicitly record `error_class = "INFRASTRUCTURE_FAULT"`. This lets operators and monitoring alerts immediately distinguish infrastructure loss (worker crash, node eviction, host partition) from application task bugs (`"PERMANENT"` or `"TRANSIENT"` task failures).
  - **Alternative Considered & Why Rejected**:
    - *Alternative*: Granting infinite or non-budgeted retries for `LOST` attempts (not decrementing `max_attempts` when workers disappear).
    - *Why Rejected*: A "poison job" whose payload triggers host memory exhaustion, kernel panics, or SIGKILL/OOM of the worker process would loop infinitely across the cluster, systematically crashing every worker in the fleet one by one. Bounding all executions to `max_attempts` regardless of failure root cause guarantees cluster survivability and enforces bounded execution.
- **Database Column `jobs.not_before`**:
  A new column `not_before TIMESTAMPTZ` is added to the `jobs` table (nullable).
- **Exponential Backoff Formula**:
  When an attempt fails with a retryable classification and `attempt_no < max_attempts`:
  $$\text{backoff} = \min\left(\text{max\_backoff}, \text{base\_backoff} \times 2^{\text{attempt\_no}-1}\right) \times \text{jitter}$$
  - `base_backoff`: Default 1.0 second. (Accepted by owner).
  - `max_backoff`: Default 30.0 seconds. (Accepted by owner).
  - `jitter`: Uniform pseudo-random multiplier between $[0.5, 1.5]$ (full jitter). (Accepted by owner).
- **Database Timestamp Assignment**:
  `jobs.not_before = clock_timestamp() + make_interval(secs => backoff_seconds)`.

### 3. Transition from `RETRYING` to Dispatch Eligibility: Direct `RETRYING -> RUNNING`

#### Blueprint Citation & Justification for Departure from Conceptual Wording
- **Blueprint Conceptual Wording**: Blueprint Section 5 ("Job Lifecycle and State Machine", p. 11-12) and Section 13 ("Failure Handling & Edge Cases", p. 28-29) conceptually illustrate that when a retryable attempt fails, the job transitions to `RETRYING`, and when the backoff delay elapses, the job transitions back to `QUEUED` so the scheduler can pick it up again.
- **Architectural Reality & Justification**: In PostgreSQL, implementing an intermediate `RETRYING -> QUEUED` transition would require a background poller or sweeper running constantly just to execute `UPDATE jobs SET state = 'QUEUED' WHERE state = 'RETRYING' AND not_before <= clock_timestamp()`. This introduces unnecessary write amplification, database lock contention, and race conditions for a purely administrative intermediate state flip.
- **Direct Dispatch Transition (Accepted)**: Instead, `jobs.not_before` acts as a deferred availability gate directly in the single-transaction pull dispatch query (`repo.DispatchNextJob`):
  ```sql
  SELECT id, task_type, payload, timeout_seconds, max_attempts
  FROM jobs
  WHERE (state = 'QUEUED' OR (state = 'RETRYING' AND not_before <= clock_timestamp()))
    AND task_type = ANY($1)
  ORDER BY created_at ASC, id ASC
  LIMIT 1
  FOR UPDATE SKIP LOCKED;
  ```
  When claimed, the job transitions atomically: `RETRYING -> RUNNING`. (Accepted by owner).
- **Job State While Waiting for `not_before`**:
  The job state remains explicitly `RETRYING`. In API responses and client queries, the job presents as `RETRYING`, clearly signaling that an attempt failed and a backoff timer is currently active, until a worker claims it at or after `not_before`.
- **Audit Consistency**:
  When a job transitions from `RETRYING` to `RUNNING`, an audit event `JobEventDispatched` is recorded in `job_events` with `from_state: 'RETRYING'`, `to_state: 'RUNNING'`. This maintains identical scheduling semantics, eliminates intermediate database writes, and preserves complete state auditability.

### 4. In-Memory Repository, Queue, and Shared Conformance Suite

To guarantee seamless testing and environment parity between in-memory and PostgreSQL modes:
1. **In-Memory Repository and Queue Dispatch Rule**:
   - In PostgreSQL mode, `not_before` backoff is integrated directly into the database pull dispatch query (`repo.DispatchNextJob`), allowing both `QUEUED` and backoff-eligible `RETRYING` jobs to be claimed without an external queue re-enqueue.
   - In in-memory mode, `InMemoryRepository` implements domain FSM state transitions and stores `job.State = StateRetrying` and `job.NotBefore`. For dispatch, delayed backoff dispatch is Postgres-mode-only; in-memory mode is explicitly scoped to state-machine semantics testing (validating that `RUNNING -> RETRYING` succeeds when `attempt_no < max_attempts`, sets `not_before`, rejects transitions if `attempt_no >= max_attempts`, and permits immediate `RETRYING -> CANCELLED` on cancel).
2. **Shared Conformance Suite Assertions**:
   The shared conformance test suite (`internal/storage/storagetest`) asserts identical state machine behavior across both in-memory and PostgreSQL repositories:
   - Transitioning a retryable failure to `RETRYING` persists `not_before` in the future.
   - Transitioning an attempt failure when `attempt_no >= max_attempts` moves the job to `FAILED` with error `"max attempts exhausted"`.
   - Calling `CancelJob` on a job in `RETRYING` state transitions the job directly to `CANCELLED`.
   - PostgreSQL-specific repository tests (`internal/storage/postgres/repository_test.go`) additionally assert database dispatch timing: `DispatchNextJob` returns `ErrNoJobsAvailable` when `not_before > clock_timestamp()`, and claims the job directly to `RUNNING` once `clock_timestamp() >= not_before`.

### 5. Database Invariant Preservation Under Retries
1. **Monotonic Attempt Numbering (`uq_attempts_job_attempt_no`)**:
   - Each attempt for a job derives its attempt number atomically under row lock:
     ```sql
     SELECT COALESCE(MAX(attempt_no), 0) + 1 FROM attempts WHERE job_id = $1;
     ```
   - Attempt 1 fails $\rightarrow$ attempt 2 is created with `attempt_no = 2`. Unique constraint `(job_id, attempt_no)` is guaranteed.
2. **At Most One Active Attempt (`idx_attempts_active_per_job`)**:
   - Partial unique index: `WHERE state IN ('PENDING', 'RUNNING')`.
   - When attempt 1 fails or expires, its state is updated to `FAILED`, `LOST`, or `TIMED_OUT` **in the same transaction** before the job enters `RETRYING`.
   - Zero active attempts exist while the job is in `RETRYING`.
   - When attempt 2 is dispatched, exactly one active attempt exists. The partial unique index is never violated.

### 6. At-Least-Once Delivery & Side-Effect Non-Guarantee
- **Engineering Principle**: MiniCloud guarantees **at-least-once execution** of attempts up to `max_attempts`.
- **Honesty Rule**: MiniCloud **explicitly disclaims exactly-once execution** for tasks with external side effects.
  - If a worker writes to an external database, sends an email, or uploads a file, and then crashes before committing its attempt result, the coordinator will safely time out the lease and retry the job on another worker.
  - The external side effect will occur more than once.
  - **Requirement**: Tasks with external side effects must implement application-level idempotency tokens or deduplication keys derived from `(job_id)`.

### 7. Poison-Job Protection
A "poison job" is a job whose payload causes worker crashes (e.g. fatal segfault, panic, or OOM) across every worker that attempts it.
- **Protection**: Bounded `max_attempts` guarantees that even if a poison job kills workers, it can kill at most `max_attempts` workers before permanently transitioning to `FAILED`.
- **Error Propagation**: The final failure record preserves the diagnostic error history across all attempts in `job_events`.

---

## Consequences

### Positive
- **Fault Tolerance**: Transient blips recover autonomously without operator intervention.
- **Cluster Protection**: Exponential backoff with full jitter prevents thundering herd retry storms.
- **Honest Semantics**: No misleading claims of exactly-once delivery for arbitrary side effects.
- **Resource Conservation**: Permanent errors and input validation failures fail fast without consuming retry budgets.
- **Efficiency**: Direct `RETRYING -> RUNNING` pull dispatch eliminates intermediate database state sweeps.

### Negative / Trade-offs
- Tasks that are not idempotent may experience duplicate execution if a worker crashes post-execution but pre-commit.
- Extended worker outages cause active jobs to exhaust `max_attempts` and fail after $3 \times 30\text{s}$.

### Invariants Protected
- Invariant 1: Stable Job & Attempt IDs (monotonic attempt numbering)
- Invariant 4: Bounded Execution (`max_attempts` budget)
- Invariant 5: At-Least-Once Execution Semantics under retries
