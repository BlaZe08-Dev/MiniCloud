# MiniCloud Fault Injection, Reliability & Property-Based Test Plan

## 1. Overview & Test Ideology
In accordance with Blueprint Section 10 ("Test Strategy and Failure Injection"), Blueprint Section 12, and [ADR 0012](adr/0012-security-and-adversarial-testing-workflow.md), MiniCloud validates its distributed execution semantics through reproducible, automated fault injection.

A distributed system cannot be certified by "happy path" testing alone. Every fault test in this plan states:
1. **Target Fault Scenario**: The specific distributed disruption injected.
2. **Injection Mechanism**: Exact failure mechanism (e.g., `SIGKILL`, simulated network drop, clock time advancement, connection freeze).
3. **Expected Invariant**: State machine transitions and correctness guarantees preserved.
4. **Evidence Collected**: Concrete log lines, HTTP status codes, and database rows inspected.
5. **Recovery Mode**: Whether recovery is completely **autonomous** (self-healing) or requires operator action.

*Status: All test identifiers in this document are designed, not executed (planned for Phase 4 implementation).*

---

## 2. Blueprint Required Fault Scenarios (Concrete Test Specifications)

### FS-01: Worker Crashes Before Accepting a Job
- **Planned Test Identifier**: `TestFault_WorkerCrash_BeforeAccept` (planned test)
- **Target Fault Scenario**: A registered worker pulls from the coordinator, claims a candidate job, but process terminates immediately via `SIGKILL` before writing attempt metadata or executing.
- **Injection Mechanism**: Test subprocess worker intercepts `PollWork` response and invokes `os.Exit(1)` prior to dispatch processing.
- **Expected Outcome**: In PostgreSQL mode (`repo.DispatchNextJob`), the transaction locks the job and worker atomically; if the worker dies prior to the poll, no claim occurs. If death occurs during the poll, row lock is released upon connection drop. Job remains `QUEUED` and is claimed by the next healthy polling worker.
- **Evidence Collected**: Job remains in `QUEUED`; subsequent `job_dispatched` event records second worker; final result successfully committed.
- **Recovery Mode**: Autonomous (self-healing).

### FS-02: Worker Crashes After Starting Execution
- **Planned Test Identifier**: `TestFault_WorkerCrash_DuringExecution` (planned test)
- **Target Fault Scenario**: Worker claims a job, begins task execution, and crashes (`SIGKILL`) halfway through.
- **Injection Mechanism**: Task payload executes 10 iterations of `sha256_loop`; test sends `syscall.SIGKILL` to worker PID at iteration 3. Coordinator test clock advances past `lease_expires_at` (30s).
- **Expected Outcome**: Coordinator lease sweeper scans expired running attempts, re-checks under row lock, marks attempt 1 as `LOST` (consuming 1 attempt of budget), and checks remaining retry budget (`attempt_no < max_attempts`). Coordinator transitions job from `RUNNING` to `RETRYING` with backoff `not_before`. Once `not_before` elapses, attempt 2 dispatches to an alternate worker and completes to `SUCCEEDED`.
- **Evidence Collected**: Attempt 1 state `LOST` in `attempts` table; `job_events` contains `lease_expired` and `job_retrying`; attempt 2 state `COMPLETED`; final job state `SUCCEEDED`.
- **Recovery Mode**: Autonomous (self-healing).

### FS-03: Worker Finishes but Result is Lost or Delayed
- **Planned Test Identifier**: `TestFault_ResultReport_NetworkDropOrDelay` (planned test)
- **Target Fault Scenario**: Worker completes task execution successfully, but the network drops or severely delays the `POST /v1/jobs/{id}/attempts/{attempt_id}/result` request.
- **Injection Mechanism**: Test HTTP transport proxy intercepts the result report and drops the first 3 attempts with connection reset (`ECONNRESET`).
- **Expected Outcome**: Worker client exponential backoff retries `ReportResult`. If the commit arrives while the attempt is still in `RUNNING` state (even if `lease_expires_at` passed on database time), it is accepted under the **Symmetric Accept-Until-Swept Rule**, completing the job to `SUCCEEDED` without redundant retries. If the sweeper already swept the attempt to `LOST`, commit is rejected with HTTP 409 Conflict (`LEASE_EXPIRED` / `domain.ErrLeaseExpired`) and recorded as `diagnostic_late_result` with zero resurrection.
- **Evidence Collected**: Worker client logs show transient reporting retries; single result row in `results` table; attempt state `COMPLETED` (or `LOST` if swept).
- **Recovery Mode**: Autonomous (self-healing).

### FS-04: Old Worker Returns Result After Lease Expired and Newer Attempt Started
- **Planned Test Identifier**: `TestFault_OldWorker_LateCommitAfterReassignment` (planned test)
- **Target Fault Scenario**: Worker 1 experiences a 40-second network partition or GC pause. Its lease expires at t = 30s. Coordinator sweeps attempt 1 to `LOST` and dispatches attempt 2 to Worker 2. Worker 1 wakes up and attempts to commit result for attempt 1.
- **Injection Mechanism**: Test harness freezes Worker 1 process (`SIGSTOP`), advances coordinator clock by 35s, triggers lease sweeper, dispatches attempt 2 to Worker 2, unfreezes Worker 1 (`SIGCONT`), and Worker 1 submits `ReportResult`.
- **Expected Outcome**: Coordinator rejects Worker 1's commit with HTTP 409 Conflict (`LEASE_EXPIRED` / `domain.ErrLeaseExpired`). Coordinator appends a `diagnostic_late_result` audit event to `job_events`. Worker 2's commit for attempt 2 is accepted with HTTP 200 `COMMITTED`. The final job result strictly records Worker 2's outcome. Expired attempts are never resurrected.
- **Evidence Collected**: HTTP 409 response received by Worker 1; `job_events` record with `event_type = 'diagnostic_late_result'`; `results.attempt_id` matches attempt 2; attempt 1 remains `LOST`.
- **Recovery Mode**: Autonomous (self-healing).

### FS-05: Coordinator Crashes After Submission Before Dispatch or During Execution
- **Planned Test Identifiers**:
  - `TestFault_CoordinatorCrash_AfterSubmit` (planned test)
  - `TestFault_CoordinatorCrash_ResultFinishesDuringDowntime` (planned test)
- **Target Fault Scenario**: Client successfully submits job; job is persisted in PostgreSQL in `QUEUED` state. Coordinator crashes either before any worker claims it, or while a worker is executing the task. The coordinator remains offline for an extended 45-second downtime (exceeding the 30-second lease duration). Crucially, a running task completes execution **during** the 45-second downtime (e.g., at t = 15s).
- **Injection Mechanism**:
  1. Sub-scenario A (crash before dispatch): Coordinator receives `POST /v1/jobs`, returns HTTP 201 Created, and is immediately terminated with `SIGKILL`. Replacement coordinator starts after 45s and connects to PostgreSQL.
  2. Sub-scenario B (result completes *during* downtime): Worker claims job and begins execution. Coordinator is terminated with `SIGKILL` and held down for 45s. At t = 15s, the task completes. The worker client enters its result delivery retry loop with capped jittered backoff while continuing background lease renewal attempts. A replacement coordinator starts at t = 45s on the same address.
- **Expected Outcome**:
  - For Sub-scenario A: `QUEUED` jobs remain durably in PostgreSQL and dispatch to workers upon next poll once coordinator recovers.
  - For Sub-scenario B: The worker client retries `ReportResult` with capped, jittered backoff throughout the 45s outage while continuing renewal attempts. Upon coordinator recovery at t = 45s, the sweeper's startup grace period (`MINICLOUD_STARTUP_GRACE_PERIOD`, 30s) suppresses reaping. Under the symmetric accept-until-swept rule, the worker's queued result commit succeeds with HTTP 200 `COMMITTED`, moving the job to `SUCCEEDED` and preserving Phase 3 guarantees (`TestPostgresWiring_CoordinatorRestartRecovery`) even when execution completes mid-downtime.
- **Evidence Collected**: `jobs.state` verified as `QUEUED` in DB post-crash; surviving worker commit succeeds with HTTP 200 `COMMITTED`; single result row in `results`; job completes to `SUCCEEDED`.
- **Recovery Mode**: Autonomous (self-healing).

### FS-06: Coordinator Crashes After Receiving Result Before/After Committing It
- **Planned Test Identifier**: `TestFault_CoordinatorCrash_MidResultCommit` (planned test)
- **Target Fault Scenario**: Worker submits execution outcome. Coordinator crashes either: (a) mid-transaction before commit, or (b) immediately after database transaction commits but before HTTP 200 response is transmitted.
- **Injection Mechanism**: Injected database failpoint triggers coordinator termination immediately before or after SQL `COMMIT`.
- **Expected Outcome**:
  - Sub-case A (crash before commit): Database transaction rolls back cleanly. Worker retries `ReportResult` against restarted coordinator; commit succeeds.
  - Sub-case B (crash after commit): Database transaction committed. Worker retries `ReportResult` against restarted coordinator; coordinator detects attempt is already `COMPLETED` and job is `SUCCEEDED`, returning idempotent HTTP 200 `COMMITTED` without duplicate result insertion.
- **Evidence Collected**: Exactly 1 row in `results` table; 0 duplicate rows; job state `SUCCEEDED`.
- **Recovery Mode**: Autonomous (self-healing).

### FS-07: PostgreSQL Temporarily Unavailable
- **Planned Test Identifier**: `TestFault_PostgresOutage_DuringLeaseExecution` (planned test)
- **Target Fault Scenario**: PostgreSQL container becomes unreachable for 5 seconds while workers are actively heartbeating, renewing leases, and polling for work.
- **Injection Mechanism**: Docker pause/unpause on PostgreSQL container or proxy port blockage for 5000ms.
- **Expected Outcome**: Coordinator `/readyz` probe and API endpoints return HTTP 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1`. Worker client detects 503, applies exponential backoff with full jitter, and pauses requests. When PostgreSQL resumes, `/readyz` returns 200, workers reconnect, outage grace window (`MINICLOUD_OUTAGE_GRACE_WINDOW`, 30s) prevents mass expiration of attempts, and surviving workers renew or commit cleanly without credential leakage (`TestSecurity_PartD_Outage_MidDispatchAndMidCommit`).
- **Evidence Collected**: 503 responses with `Retry-After: 1` logged; backoff intervals logged; zero unhandled panics; full execution resumes after restore.
- **Recovery Mode**: Autonomous (self-healing).

### FS-08: Heartbeat Delayed or Duplicated
- **Planned Test Identifier**: `TestFault_Heartbeat_DelayedOrDuplicated` (planned test)
- **Target Fault Scenario**: Worker heartbeat packets are duplicated by network proxies or delayed past `worker_timeout`.
- **Injection Mechanism**: Test sends 20 duplicate concurrent `POST /v1/workers/{id}/heartbeat` requests, followed by a simulated 15s delay.
- **Expected Outcome**: Duplicate heartbeats update `last_heartbeat_at` idempotently. Delayed heartbeats cause the worker reaper loop to transition worker status to `UNHEALTHY`. When the next heartbeat arrives, worker status is automatically restored to `HEALTHY`. Heartbeats do not deliver cancellations.
- **Evidence Collected**: Worker transitions `HEALTHY` -> `UNHEALTHY` -> `HEALTHY`; zero duplicate worker rows in database.
- **Recovery Mode**: Autonomous (self-healing).

### FS-09: Task Times Out, Emits Excessive Output, or Repeatedly Fails
- **Planned Test Identifier**: `TestFault_Task_TimeoutAndPoisonRepeat` (planned test)
- **Target Fault Scenario**: A task exceeds its configured runtime timeout, attempts to write 10 MiB of stdout/stderr, and repeatedly fails across retries.
- **Injection Mechanism**: Custom task runs infinite loop writing large buffers with `timeout_seconds = 2` and `max_attempts = 3`.
- **Expected Outcome**: In-process executor aborts task execution via `context.WithTimeout` at 2s; output buffer is truncated to 64 KiB (enforced by `CHECK (octet_length(output) <= 65536)`); worker reports non-zero exit code. Input validation failures fail immediately as `PERMANENT`. Transient failures retry up to `max_attempts = 3`. After attempt 3 fails, job permanently transitions to `FAILED`.
- **Evidence Collected**: Output size strictly <= 65536 bytes; exactly 3 attempts recorded in `attempts`; final job state `FAILED`; no worker crash.
- **Recovery Mode**: Autonomous (self-healing).

### FS-10: Cancellation Races with Job Completion or Duplicate Cancel
- **Planned Test Identifier**: `TestFault_Cancellation_RaceWithCompletion` (planned test)
- **Target Fault Scenario**: Client issues cancellation request (`POST /v1/jobs/{id}/cancel`) at the exact millisecond the worker issues result commit (`POST /result`), or issues duplicate cancels concurrently.
- **Injection Mechanism**: 50 concurrent goroutine pairs execute simultaneous cancel, duplicate cancel, and commit requests synchronized with a `sync.WaitGroup` start barrier.
- **Expected Outcome**: PostgreSQL row locks on `jobs` serialize execution order.
  - If commit locks first: job transitions `RUNNING` -> `SUCCEEDED`; subsequent cancel receives HTTP 409 `JOB_ALREADY_TERMINAL`.
  - If cancel locks first: job transitions `RUNNING` -> `CANCEL_REQUESTED` (HTTP 200); worker commit evaluates `CANCEL_REQUESTED`, accepts completed work, and transitions job to `SUCCEEDED` (or confirms cancel to `CANCELLED`).
  - If cancel is duplicated on `CANCEL_REQUESTED`: returns idempotent HTTP 200 `CANCEL_REQUESTED` with zero duplicate audit events.
  - Cancellation delivered exclusively via renewal response (`cancel_requested: true`).
  - Zero deadlocks, zero unhandled errors, exactly one consistent terminal state.
- **Evidence Collected**: Terminal state is either `SUCCEEDED` or `CANCELLED`; `job_events` contains sequential audit entries; race detector clean (`-race`).
- **Recovery Mode**: Autonomous (self-healing).

---

## 3. Model-Based & Property-Based Test Plan

To discover unexpected corner cases, race conditions, and sequence anomalies across arbitrary failure combinations, MiniCloud introduces a model-based testing harness (`TestProperty_StateMachine_ModelConformance` (planned test)).

### 3.1 Architecture of the Property Harness
1. **Pure Reference Model (`SimpleJobModel`)**:
   - In-memory pure state machine tracking abstract jobs and attempts with zero network or database dependencies.
   - Enforces transition validity via pure Go functions.
2. **Random Event Sequence Generator**:
   - Generates random valid and invalid operation sequences of length K = 50..500:
     - `Submit(job_id, task_type, max_attempts)`
     - `Dispatch(worker_id)`
     - `Renew(attempt_id, token)`
     - `AdvanceTime(duration)`
     - `SweepExpired()`
     - `Cancel(job_id)`
     - `ReportResult(attempt_id, exit_code)`
     - `CrashWorker(worker_id)`
     - `RestartCoordinator()`
3. **Dual Execution & Invariant Assertion**:
   - Every generated action is applied simultaneously to the Reference Model and the System Under Test (`InMemoryRepository` and PostgreSQL `Repository` using an injectable clock).
   - After each action, the repository state is compared against the reference model.

### 3.2 Invariants Checked on Every Step
1. **No Stale Result Committed**: An attempt whose lease has expired and been swept to `LOST` can never write a row to `results` or transition a job to `SUCCEEDED` (no resurrection).
2. **At Most One Committed Result Per Job**: The `results` table contains at most 1 row per `job_id`.
3. **Bounded Retries**: A job's total attempt count never exceeds `jobs.max_attempts`.
4. **No Active Attempt After Terminal State**: When `jobs.state` is in `SUCCEEDED`, `FAILED`, `CANCELLED`, or `REJECTED`, zero attempts exist with `state IN ('PENDING', 'RUNNING')`.
5. **Monotonic Attempt Sequence**: For any job, `attempt_no` starts at 1 and increases by exactly +1 per attempt with zero gaps or duplicates.
6. **Lease Expiry Never Resurrects Terminal Job**: Sweeping an expired lease for a job that already completed or was cancelled never transitions the job back to `RETRYING` or `RUNNING`.
