# ADR 0016: Job Cancellation Semantics, Cooperative Worker Termination, and Race Resolution

## Status
PROPOSED (Owner Approval Pending)

## Context
MiniCloud allows users to cancel submitted jobs via `POST /v1/jobs/{job_id}/cancel`.
In Milestone 1 and Phase 3:
- If a job is in `QUEUED`, `CancelJob` transitions it immediately to `CANCELLED` (`internal/storage/inmemory.go:673-675`, `internal/storage/postgres/repository.go:1184-1186`).
- If a job is in `RUNNING`, `CancelJob` transitions it to `CANCEL_REQUESTED` (`inmemory.go:676-678`, `repository.go:1187-1189`).
- However, as identified in the code inspection, **workers currently learn nothing about cancellation**:
  1. The heartbeat response is static `{"status": "ACK"}`.
  2. Workers do not inspect or receive cancellation signals during execution.
  3. When an attempt finishes, `CommitAttempt` allows the worker's result to overwrite `CANCEL_REQUESTED` to `SUCCEEDED` or `FAILED` without confirming whether cancellation occurred.
  4. If a worker hangs, there is no timeout or lease reaper to force resolution of `CANCEL_REQUESTED`.

Blueprint Sections 3, 5, 8, and 10 require cooperative cancellation:
- Cancellation is a request for a running attempt and must be confirmed or resolved.
- The API response must distinguish immediate cancellation from requested cancellation.
- All concurrent races (cancel vs completion, cancel vs lease expiry, cancel vs retry) must resolve deterministically under explicit row-level locking.

---

## Decision

### 1. State Differentiation by Lifecycle Phase
When `POST /v1/jobs/{job_id}/cancel` is invoked:
1. **If Job is in `QUEUED`**:
   - The job has not been dispatched to any worker.
   - Transition: `QUEUED` $\rightarrow$ `CANCELLED` immediately.
   - Dispatch transport / in-memory queue: message is acknowledged and removed (`queue.Ack`).
   - API Response: `200 OK` with `state: "CANCELLED"`, `message: "job cancelled immediately"`.
2. **If Job is in `RETRYING`**:
   - The previous attempt failed, and the job is waiting out its `not_before` backoff.
   - Transition: `RETRYING` $\rightarrow$ `CANCELLED` immediately (permitted in FSM, `internal/domain/transition.go:27`).
   - The scheduled retry is permanently aborted; `not_before` is cleared.
   - API Response: `200 OK` with `state: "CANCELLED"`, `message: "pending retry cancelled immediately"`.
3. **If Job is in `RUNNING`**:
   - An attempt is actively executing on a worker under an active lease.
   - Transition: `RUNNING` $\rightarrow$ `CANCEL_REQUESTED`.
   - Audit Event: Insert `job_events` record with `event_type: 'job_cancel_requested'`.
   - API Response: `200 OK` with `state: "CANCEL_REQUESTED"`, `message: "cancellation requested; awaiting worker confirmation"`.
4. **If Job is in Terminal State (`SUCCEEDED`, `FAILED`, `CANCELLED`, `REJECTED`)**:
   - Return HTTP `409 Conflict` with error code `JOB_ALREADY_TERMINAL`.

### 2. Downstream Signal Delivery to Worker
MiniCloud employs a pull-based protocol; coordinators do not initiate TCP connections to workers. Cancellation signals flow downward through regular worker polling and renewal channels:
1. **Primary Delivery Channel: Attempt Lease Renewal (`/renew`)**:
   - While running a task, the worker issues `POST /v1/jobs/{id}/attempts/{attempt_id}/renew` every 10 seconds (ADR-0014).
   - If `job.state == 'CANCEL_REQUESTED'`, the coordinator returns:
     ```json
     {
       "status": "RENEWED",
       "lease_expires_at": "2026-10-26T20:20:00Z",
       "cancel_requested": true
     }
     ```
2. **Secondary Channel: Node Heartbeat (`/heartbeat`)**:
   - The worker heartbeat response (`POST /v1/workers/{id}/heartbeat`) expands additively to include active cancellations for that worker:
     ```json
     {
       "status": "ACK",
       "cancelled_job_ids": ["job_123"]
     }
     ```
   - An old worker ignoring `cancelled_job_ids` continues to function safely; a new worker inspects this array to preempt running attempts.

### 3. Cooperative Cancellation via Go `context.Context`
In the worker agent (`internal/worker/client.go` and `internal/executor`):
- For every active execution attempt, the worker creates a cancellable execution context:
  ```go
  taskCtx, cancelFn := context.WithCancel(ctx)
  ```
- When `cancel_requested: true` is received in a renewal or heartbeat response:
  1. The worker agent invokes `cancelFn()`.
  2. The registered task executor receives `taskCtx.Done()`.
  3. Predefined tasks (`sleep`, `sha256_loop`, `echo`) observe context cancellation between loop iterations or select blocks, terminate immediately, and return `context.Canceled`.
  4. The worker reports the outcome to `POST /v1/jobs/{id}/attempts/{attempt_id}/result` with `error = "task cancelled by user request"`, `exit_code = 130` (or `domain.ErrTaskCancelled`).

### 4. Resolution of Distributed Races

#### Race 1: Cancellation vs Completion
- **Scenario**: Client submits cancellation while the worker has already finished computation and is calling `CommitAttempt`.
- **Resolution**:
  - The transaction in `CommitAttempt` locks the `jobs` row `FOR UPDATE`.
  - In MiniCloud's FSM (`internal/domain/transition.go:19-23`), `CANCEL_REQUESTED` explicitly permits transitions to `SUCCEEDED`, `FAILED`, and `CANCELLED`.
  - **Rule**: If the worker successfully produced a valid result before receiving the cancel signal, **valid completed work is accepted**:
    - Job transitions: `CANCEL_REQUESTED` $\rightarrow$ `SUCCEEDED`.
    - Result is stored in `results`.
    - Event: `JobEventCompleted` with detail `{"overrode_cancel_request": true}`.
  - If the worker confirms cancellation (reports `context.Canceled` or cancel exit code):
    - Job transitions: `CANCEL_REQUESTED` $\rightarrow$ `CANCELLED`.
    - Attempt transitions: `AttemptRunning` $\rightarrow$ `AttemptCanceled`.
    - Event: `JobEventCancelled`.

#### Race 2: Cancellation vs Lease Expiry (Zombie / Unresponsive Worker)
- **Scenario**: Job is in `CANCEL_REQUESTED`. The worker hangs in a non-cooperative task, loses network connectivity, or dies before confirming cancellation.
- **Resolution**:
  - The coordinator never allows a job to remain in `CANCEL_REQUESTED` indefinitely.
  - The attempt lease will expire within at most `lease_expires_at` (default 30s).
  - The coordinator lease expiry sweeper (ADR-0014) locks the job and attempt rows.
  - Seeing `job.state == 'CANCEL_REQUESTED'` and `lease_expires_at < clock_timestamp()`:
    1. Sweeper transitions the attempt to `LOST` (or `CANCELLED`).
    2. Sweeper transitions the job: `CANCEL_REQUESTED` $\rightarrow$ `CANCELLED` (terminal).
    3. Sweeper **suppresses retries**: even if `attempt_no < max_attempts`, a cancelled job is never retried.
    4. Event: `JobEventCancelled` with detail `{"resolved_by": "lease_sweeper", "reason": "worker unresponsive"}`.

#### Race 3: Duplicate Cancellation Requests
- **Scenario**: Client or automated script issues multiple concurrent `POST /v1/jobs/{id}/cancel` calls.
- **Resolution**:
  - Protected by row lock on `jobs`.
  - First request transitions job to `CANCELLED` or `CANCEL_REQUESTED`.
  - Subsequent requests:
    - If job is in `CANCEL_REQUESTED`: Idempotently return HTTP `200 OK` with `state: "CANCEL_REQUESTED"`, `message: "cancellation already requested"`.
    - If job is already in terminal `CANCELLED` (or `SUCCEEDED`/`FAILED`): Return HTTP `409 Conflict` with `code: "JOB_ALREADY_TERMINAL"`.

---

## Consequences

### Positive
- **Deterministic Resolution**: Bounded lease sweeps ensure `CANCEL_REQUESTED` always resolves to a terminal state within 30 seconds, even if workers vanish.
- **Cooperative Cleanliness**: Cooperative contexts allow tasks to flush logs or clean temporary files before exiting.
- **Race Free**: Atomic row locks guarantee serialized handling of concurrent completion, expiry, and cancellation.

### Negative / Trade-offs
- In-process tasks that do not check `ctx.Done()` cannot be forcibly preempted in V1 without terminating the worker OS process. (V3 Docker container isolation adds hard SIGKILL enforcement).

### Invariants Protected
- Invariant 2: Central State Validation (FSM transitions validated centrally)
- Invariant 4: Bounded Execution (cancellation cannot remain pending forever)
