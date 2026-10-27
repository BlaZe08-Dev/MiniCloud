# ADR 0016: Job Cancellation Semantics, Cooperative Worker Termination, and Race Resolution

## Status
Accepted by Owner (2026-10-26)
- **Accepted Owner Decisions**:
  - Single cancellation delivery channel via lease renewal response (`cancel_requested: true`). Heartbeat response does not carry cancellations.
  - Worst-case cancellation delivery latency bounded by one renewal interval (10 s).
  - Idempotent duplicate cancellation on `CANCEL_REQUESTED` returning HTTP 202 `CANCEL_REQUESTED`.
  - Cancellation during `RETRYING` transitions immediately to `CANCELLED` and aborts scheduled retries.
  - Running attempts resolved exclusively via lease/renewal path: sweeper or commit path resolves `CANCEL_REQUESTED` jobs.
  - No separate `CANCEL_REQUESTED` deadline (the 30 s lease bounds it).
  - V1 relies on cooperative Go context cancellation (`context.Context`); hard process kill is deferred to V3 Docker container execution isolation (ADR-0005).
  - Worker receiving lease-lost (HTTP 409 or 403) must abort task execution and must not report a result.
- **Proposed (Implementation Details)**: Sub-slices 4a–4e implementation details remain proposed until merged.

## Context

### Verified Baseline: Existing Cancellation Behavior
MiniCloud currently implements cancellation atomically in storage via `repo.CancelJob`:
- Existing code in `internal/storage/inmemory.go:658-683` and `internal/storage/postgres/repository.go:1135-1215` behaves identically:
  1. If job is terminal (`SUCCEEDED`, `FAILED`, `CANCELLED`, `REJECTED`): returns `domain.ErrJobAlreadyTerminal` (mapped to HTTP 409).
  2. If job is in `QUEUED`: transitions job immediately to `CANCELLED` (audit event `JobEventCancelled`).
  3. If job is in `RUNNING`: transitions job to `CANCEL_REQUESTED` (audit event `JobEventCancelRequested`), leaving the active attempt unmodified in `RUNNING`.
  4. If job is in any other non-terminal state (which currently includes `CANCEL_REQUESTED` or future `RETRYING`): the `else` branch transitions the job directly to `CANCELLED` (audit event `JobEventCancelled`).
- **Correction of Prior Misdescription**: Prior design drafts incorrectly claimed that cancelling a `RUNNING` job moved it directly to `CANCELLED` and marked the attempt `FAILED`. That description was erroneous. In reality, `CancelJob` already sets `CANCEL_REQUESTED` on first call for `RUNNING` jobs and leaves the attempt in `RUNNING`.
- **The Gap Today**: Workers learn nothing about cancellation:
  1. The heartbeat response is static `{"status": "ACK"}`.
  2. Workers do not receive cancellation signals during execution.
  3. When an attempt finishes, `CommitAttempt` allows the worker's result to transition `CANCEL_REQUESTED` to `SUCCEEDED` or `FAILED`.
  4. If a worker hangs, there is no lease sweeper to force resolution of `CANCEL_REQUESTED`.

Blueprint Sections 3, 5, 8, and 10 mandate that:
- Cancellation is a cooperative request for running attempts and must be confirmed or resolved.
- Duplicate cancels on `CANCEL_REQUESTED` must be safe and idempotent.
- All concurrent races (cancel vs completion, cancel vs lease expiry, cancel vs retry) must resolve deterministically under explicit row-level locking.

---

## Decision

### 1. Behavior Changes Introduced by Phase 4 Design
The Phase 4 design introduces three concrete behavioral changes to the existing cancellation implementation:
1. **Idempotent Duplicate Cancel on `CANCEL_REQUESTED`**:
   - *Current Behavior*: A second call to `CancelJob` while `job.state == 'CANCEL_REQUESTED'` falls into the `else` block, moving the job prematurely to `CANCELLED` while the worker may still be executing the attempt.
   - *New Behavior*: A duplicate cancel on a `CANCEL_REQUESTED` job is an idempotent no-op. It returns the existing job state with HTTP 202 `CANCEL_REQUESTED` and does not insert duplicate audit events.
2. **Cancellation During `RETRYING`**:
   - When a job is in `RETRYING` awaiting backoff, `CancelJob` immediately transitions it to `CANCELLED` (terminal), clears `not_before`, and removes it from dispatch eligibility.
3. **Running Attempt Resolution via Lease / Sweeper Path**:
   - The active attempt is resolved through the lease/renewal path: either the worker receives `cancel_requested: true` on its renewal and reports cooperative cancellation, or the worker commits valid completion, or the coordinator lease sweeper reaps the expired lease and transitions `CANCEL_REQUESTED -> CANCELLED`.

### 2. State Differentiation by Lifecycle Phase
When `POST /v1/jobs/{job_id}/cancel` is invoked:
1. **If Job is in `QUEUED`**:
   - Transition: `QUEUED` $\rightarrow$ `CANCELLED` immediately.
   - Dispatch transport / in-memory queue: message is dropped and acknowledged (`queue.Ack`).
   - API Response: `200 OK` with `state: "CANCELLED"`, `message: "job cancelled immediately"`.
2. **If Job is in `RETRYING`**:
   - Transition: `RETRYING` $\rightarrow$ `CANCELLED` immediately (`internal/domain/transition.go:27`).
   - Scheduled retry aborted; `not_before` cleared.
   - API Response: `200 OK` with `state: "CANCELLED"`, `message: "pending retry cancelled immediately"`.
3. **If Job is in `RUNNING`**:
   - Transition: `RUNNING` $\rightarrow$ `CANCEL_REQUESTED`.
   - Audit Event: Insert `job_events` record with `event_type: 'job_cancel_requested'`.
   - API Response: `202 Accepted` with `state: "CANCEL_REQUESTED"`, `message: "cancellation requested; awaiting worker confirmation"`.
4. **If Job is in `CANCEL_REQUESTED` (Duplicate Cancel)**:
   - State unchanged: remains in `CANCEL_REQUESTED`.
   - API Response: `202 Accepted` with `state: "CANCEL_REQUESTED"`, `message: "cancellation already requested"`. (Accepted by owner).
5. **If Job is in Terminal State (`SUCCEEDED`, `FAILED`, `CANCELLED`, `REJECTED`)**:
   - Return HTTP `409 Conflict` with error code `JOB_ALREADY_TERMINAL`.

*Note on API Contract Change:* In Milestone 1–3, the cancellation handler (`internal/api/handlers.go:267`) unconditionally returned HTTP 200 `OK` with `{"message": "cancellation processed"}` for all valid jobs, including `RUNNING` jobs. Returning HTTP 202 `Accepted` for `RUNNING` jobs (`CANCEL_REQUESTED`) and duplicate cancels on `CANCEL_REQUESTED` is a deliberate additive change to the HTTP API contract in Phase 4.

### 3. Single Downstream Delivery Channel: Lease Renewal (`/renew`)
MiniCloud adopts a single, dedicated delivery channel for cancellation signals:
- **Sole Delivery Channel**: `POST /v1/jobs/{id}/attempts/{attempt_id}/renew`.
   - While running a task, the worker issues renewal requests every 10 seconds (ADR-0014).
   - If `job.state == 'CANCEL_REQUESTED'`, the renewal response returns:
     ```json
     {
       "status": "RENEWED",
       "lease_expires_at": "2026-10-26T20:20:00Z",
       "cancel_requested": true
     }
     ```
- **Heartbeat Excluded**: Heartbeats (`POST /v1/workers/{id}/heartbeat`) remain strictly dedicated to node liveness and do not carry `cancelled_job_ids`. (Accepted by owner).
- **Latency Bound**: The worst-case delivery latency from client cancellation to worker receipt is bounded by **one renewal interval (10 seconds default)**. (Accepted by owner).

### 4. Cooperative Worker Cancellation & Lease-Lost Preemption
In the worker agent (`internal/worker/client.go` and `internal/executor`):
- For every active execution attempt, the worker creates a cancellable execution context:
  ```go
  taskCtx, cancelFn := context.WithCancel(ctx)
  ```
- **When `cancel_requested: true` is received on renewal**:
  1. The worker agent invokes `cancelFn()`.
  2. The task executor receives `taskCtx.Done()`.
  3. Predefined tasks (`sleep`, `sha256_loop`, `echo`) observe context cancellation between loop iterations or select blocks, terminate immediately, and return `context.Canceled`.
  4. The worker reports the outcome to `POST /v1/jobs/{id}/attempts/{attempt_id}/result` with `error = "task cancelled by user request"`, `exit_code = 130` (or `domain.ErrTaskCancelled`).
- **When Lease-Lost is received on renewal (HTTP 409 LEASE_EXPIRED or 403 WORKER_MISMATCH)**:
  Under the symmetric accept-until-swept rule, renewal is accepted while `attempts.state == 'RUNNING'`. HTTP 409 `LEASE_EXPIRED` indicates the coordinator lease sweeper has already swept the attempt to `LOST`.
  1. The worker agent invokes `cancelFn()`.
  2. The worker **must not report a result** to the coordinator (the attempt was swept to `LOST` by the sweeper). (Accepted by owner).

### 5. Resolution of Distributed Races

#### Race 1: Cancellation vs Completion
- **Scenario**: Client submits cancellation while worker has finished computation and is calling `CommitAttempt`.
- **Resolution**:
  - `CommitAttempt` locks the `jobs` row `FOR UPDATE`.
  - In MiniCloud's FSM (`internal/domain/transition.go:19-23`), `CANCEL_REQUESTED` explicitly permits transitions to `SUCCEEDED`, `FAILED`, and `CANCELLED`.
  - **Rule**: If the worker successfully produced a valid result before receiving the cancel signal, **valid completed work is accepted**:
    - Job transitions: `CANCEL_REQUESTED` $\rightarrow$ `SUCCEEDED`.
    - Result is stored in `results`.
    - Event: `JobEventCompleted` with detail `{"overrode_cancel_request": true}`.
  - If the worker confirms cancellation (reports `context.Canceled` or cancel exit code):
    - Job transitions: `CANCEL_REQUESTED` $\rightarrow$ `CANCELLED`.
    - Attempt transitions: `AttemptRunning` $\rightarrow$ `AttemptCancelled`.
    - Event: `JobEventCancelled`.

#### Race 2: Cancellation vs Lease Expiry (Zombie / Unresponsive Worker)
- **Scenario**: Job is in `CANCEL_REQUESTED`. Worker hangs in non-cooperative task, loses network connectivity, or dies before confirming cancellation.
- **Resolution**:
  - MiniCloud enforces **no separate `CANCEL_REQUESTED` deadline**; the 30-second lease bounds it. (Accepted by owner).
  - The coordinator lease sweeper (ADR-0014) locks `jobs -> workers -> attempts`.
  - Seeing `job.state == 'CANCEL_REQUESTED'` and `lease_expires_at < clock_timestamp()`:
    1. Sweeper transitions attempt to `LOST` (or `CANCELLED`).
    2. Sweeper transitions job: `CANCEL_REQUESTED` $\rightarrow$ `CANCELLED` (terminal).
    3. Sweeper **suppresses retries**: even if `attempt_no < max_attempts`, a cancelled job is never retried.
    4. Event: `JobEventCancelled` with detail `{"resolved_by": "lease_sweeper", "reason": "worker unresponsive"}`.

#### Race 3: Duplicate Cancellation Requests
- **Scenario**: Client issues multiple concurrent or sequential `POST /v1/jobs/{id}/cancel` calls.
- **Resolution**:
  - First request transitions job to `CANCELLED` (from `QUEUED` / `RETRYING`) or `CANCEL_REQUESTED` (from `RUNNING`).
  - Subsequent requests:
    - If job is in `CANCEL_REQUESTED`: Idempotently return HTTP `202 Accepted` with `state: "CANCEL_REQUESTED"`, `message: "cancellation already requested"`.
    - If job is in terminal `CANCELLED` (or `SUCCEEDED`/`FAILED`): Return HTTP `409 Conflict` with `code: "JOB_ALREADY_TERMINAL"`.
- **Existing Test Impact**:
  - Existing tests `CancelJob_QueuedAndRunning` in `internal/storage/storagetest/conformance.go:299-363` and `TestInMemoryRepository_SubmitJob_And_CancelJob_Events` in `internal/storage/inmemory_test.go:559` test duplicate cancel on terminal `QUEUED -> CANCELLED`, asserting `ErrJobAlreadyTerminal`. Neither test asserts duplicate cancel on `CANCEL_REQUESTED`.
  - Conformance plan adds `TestConformance_CancelJob_IdempotentCancelRequested` (planned test) to verify in-memory and PostgreSQL parity for duplicate cancel on `CANCEL_REQUESTED`.

---

## Consequences

### Positive
- **Deterministic Bounded Resolution**: Bounded 30s lease sweeps ensure `CANCEL_REQUESTED` always resolves to a terminal state within 30 seconds without requiring a separate reaper timer.
- **Protocol Simplicity**: Eliminating heartbeat cancellation leaves heartbeats focused solely on node health and reduces protocol surface area.
- **Idempotency**: Clients retrying cancellation requests on network blips receive safe 202 responses without corrupting attempt state.

### Negative / Trade-offs
- Maximum cancellation delivery latency is 10 seconds (one renewal period).
- In-process tasks that do not check `ctx.Done()` cannot be forcibly killed in V1 without OS process termination. Hard process isolation is deferred to V3 Docker containers.

### Invariants Protected
- Invariant 2: Central State Validation (FSM transitions validated centrally)
- Invariant 4: Bounded Execution (cancellation cannot remain pending forever)
