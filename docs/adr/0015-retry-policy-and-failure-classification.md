# ADR 0015: Retry Policy, Failure Classification Taxonomy, and At-Least-Once Delivery

## Status
PROPOSED (Owner Approval Pending)

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

### 1. Failure Classification Taxonomy
MiniCloud introduces a structured failure classification taxonomy:

```text
                     [Execution Failure]
                              │
               ┌──────────────┴──────────────┐
               ▼                             ▼
       [Permanent Failure]           [Retryable Failure]
       • Bad input / validation      • Worker node crash / lease expiry
       • Unsupported task options    • Transient network timeout
       • Explicit task non-retryable • Memory/OOM abort
       • Max attempts exhausted      • Database lock contention
               │                             │
               ▼                             ▼
      Transition to FAILED          Evaluate max_attempts:
                                    If attempt < max: RETRYING
                                    If attempt >= max: FAILED
```

#### Classification Categories
1. **`ClassPermanent` (`"PERMANENT"`)**:
   - The failure is deterministic; repeating the execution with the same payload and environment will produce the exact same failure.
   - Examples: Payload schema invalid, unknown sub-command, application exit code explicitly designated as permanent (e.g., exit code 2), task type unsupported by all workers.
   - Action: Job transitions immediately to `FAILED` without retrying, conserving cluster capacity.
2. **`ClassRetryable` (`"RETRYABLE"`)**:
   - The failure is transient, environmental, or node-specific; repeating execution on the same or another worker has a high probability of success.
   - Examples: Lease expired (worker stopped heartbeating), worker process terminated by SIGKILL, I/O connection reset, execution timeout where task is declared idempotent.
   - Action: If `attempt_no < max_attempts`, job transitions to `RETRYING`; if `attempt_no >= max_attempts`, job transitions to `FAILED`.
3. **`ClassUnknown` (`"UNKNOWN"`)**:
   - The failure cause cannot be definitively determined from the process exit code or error string.
   - Action: Default policy treats `ClassUnknown` as **`ClassRetryable`** until `max_attempts` is exhausted. This ensures resilience against unclassified non-zero exit codes.

#### Three-Tier Classification Authority
1. **Tier 1 (Task / Executor)**: The task implementation inspects its execution outcome. Predefined tasks (`echo`, `sleep`, `sha256_loop`) emit structured exit codes and set `AttemptOutcome.ErrorClassification`.
2. **Tier 2 (Worker Agent)**: If the executor process terminates abnormally (e.g. killed by OS signal, panic recovery, context deadline exceeded), the worker agent classifies the outcome before reporting.
3. **Tier 3 (Coordinator — Final Arbiter)**: The coordinator validates the report against job policy. If the worker crashed without reporting, the lease expiry sweeper (ADR-0014) classifies the attempt as `ClassRetryable`.

### 2. Bounded Retries & Backoff with Jitter
- **Attempt Upper Bound**: `max_attempts` is configured at job submission ($1..20$, default 3; enforced by SQL check constraint `chk_jobs_max_attempts`).
- **Database Column `jobs.not_before`**:
  A new column `not_before TIMESTAMPTZ` is added to the `jobs` table (nullable).
- **Exponential Backoff Formula**:
  When an attempt fails with a retryable classification and `attempt_no < max_attempts`:
  $$\text{backoff} = \min\left(\text{max\_backoff}, \text{base\_backoff} \times 2^{\text{attempt\_no}-1}\right) \times \text{jitter}$$
  - `base_backoff`: Default 1.0 second.
  - `max_backoff`: Default 30.0 seconds.
  - `jitter`: Uniform pseudo-random multiplier between $[0.5, 1.5]$.
- **Database Timestamp Assignment**:
  `jobs.not_before = clock_timestamp() + make_interval(secs => backoff_seconds)`.

### 3. Transition from `RETRYING` to Dispatch Eligibility
To preserve formal FSM observability without adding polling latency, MiniCloud specifies:
- When a retryable failure occurs:
  1. The failed attempt transitions to `FAILED` or `LOST`.
  2. The job transitions to `RETRYING` with `not_before` set to the future timestamp.
  3. A `job_events` record is inserted with `event_type: 'job_retrying'`.
- **Eligibility at Dispatch**:
  The transactional pull dispatch query (`repo.DispatchNextJob`) evaluates both `QUEUED` and matured `RETRYING` jobs:
  ```sql
  SELECT id, task_type, payload, timeout_seconds, max_attempts
  FROM jobs
  WHERE (state = 'QUEUED' OR (state = 'RETRYING' AND not_before <= clock_timestamp()))
    AND task_type = ANY($1)
  ORDER BY created_at ASC, id ASC
  LIMIT 1
  FOR UPDATE SKIP LOCKED;
  ```
  When claimed, the job transitions directly from `RETRYING` (or `QUEUED`) to `RUNNING`.
- **Audit Consistency**:
  If the job transitions from `RETRYING` to `RUNNING`, an event `JobEventDispatched` is recorded with `from_state: 'RETRYING'`, `to_state: 'RUNNING'`.

### 4. Database Invariant Preservation Under Retries
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

### 5. At-Least-Once Delivery & Side-Effect Non-Guarantee
- **Engineering Principle**: MiniCloud guarantees **at-least-once execution** of attempts up to `max_attempts`.
- **Honesty Rule**: MiniCloud **explicitly disclaims exactly-once execution** for tasks with external side effects.
  - If a worker writes to an external database, sends an email, or uploads a file, and then crashes before committing its attempt result, the coordinator will safely time out the lease and retry the job on another worker.
  - The external side effect will occur more than once.
  - **Requirement**: Tasks with external side effects must implement application-level idempotency tokens or deduplication keys derived from `(job_id)`.

### 6. Poison-Job Protection
A "poison job" is a job whose payload causes worker crashes (e.g. fatal segfault, panic, or OOM) across every worker that attempts it.
- **Protection**: Bounded `max_attempts` guarantees that even if a poison job kills workers, it can kill at most `max_attempts` workers before permanently transitioning to `FAILED`.
- **Error Propagation**: The final failure record preserves the diagnostic error history across all attempts in `job_events`.

---

## Consequences

### Positive
- **Fault Tolerance**: Transient blips recover autonomously without operator intervention.
- **Cluster Protection**: Exponential backoff with jitter prevents thundering herd retry storms.
- **Honest Semantics**: No misleading claims of exactly-once delivery for arbitrary side effects.
- **Resource Conservation**: Permanent errors fail fast without consuming retry budgets.

### Negative / Trade-offs
- Tasks that are not idempotent may experience duplicate execution if a worker crashes post-execution but pre-commit.

### Invariants Protected
- Invariant 1: Stable Job & Attempt IDs (monotonic attempt numbering)
- Invariant 4: Bounded Execution (`max_attempts` budget)
- Invariant 5: At-Least-Once Execution Semantics under retries
