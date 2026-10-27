# ADR 0016: Job Cancellation Semantics, Cooperative Worker Termination, and Race Resolution

## Status
Accepted by Owner (2026-10-26)
- **Accepted Owner Decisions**:
  - Single cancellation delivery channel via lease renewal response (`cancel_requested: true`). Heartbeat response does not carry cancellations.
  - Worst-case cancellation delivery latency bounded by one renewal interval (10 s).
  - Idempotent duplicate cancellation on `CANCEL_REQUESTED` returning HTTP 200 with state `CANCEL_REQUESTED` (no status code change).
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

Blueprint Section 5 ("Job Lifecycle and Consistency Model", pages 5–6), Section 8 ("API, Data Model, and Configuration", page 8; "Initial HTTP API (suggested)" is subheading), and Section 10 ("Test Strategy and Failure Injection", pages 9–10) mandate that:
- Cancellation is a cooperative request for running attempts and the response distinguishes requested from completed (Blueprint Section 8).
- Cancellation races with success completion deterministically (Blueprint Section 10).
- Duplicate cancels on `CANCEL_REQUESTED` must be safe and idempotent (an owner decision; the blueprint is silent on duplicate cancels).
- All concurrent races (cancel vs completion, cancel vs lease expiry, cancel vs retry) must resolve deterministically under explicit row-level locking.

---

## Decision

### 1. Behavior Changes Introduced by Phase 4 Design
The Phase 4 design introduces three concrete behavioral changes to the existing cancellation implementation:
1. **Idempotent Duplicate Cancel on `CANCEL_REQUESTED`**:
   - *Current Behavior*: A second call to `CancelJob` while `job.state == 'CANCEL_REQUESTED'` falls into the `else` block, moving the job prematurely to `CANCELLED` while the worker may still be executing the attempt. When the worker later commits, the commit is rejected as terminal (`JOB_ALREADY_TERMINAL`) without closing the attempt, permanently leaking worker capacity (SEC-022).
   - *New Behavior*: A duplicate cancel on a `CANCEL_REQUESTED` job is an idempotent no-op performed directly in the repository `CancelJob` method under the job row lock (in PostgreSQL) or mutex (in-memory). It returns the existing job state with HTTP 200 `CANCEL_REQUESTED` (with the same body as the first cancellation request) and does not insert duplicate audit events or invoke FSM transitions. In Slice 4a, `CommitAttempt` on a terminal job closes the attempt and frees the worker capacity slot in the same transaction as the rejection, writing a diagnostic event and returning 409 `JOB_ALREADY_TERMINAL`.
2. **Cancellation During `RETRYING`**:
   - When a job is in `RETRYING` awaiting backoff, `CancelJob` immediately transitions it to `CANCELLED` (terminal), clears `not_before`, and removes it from dispatch eligibility.
3. **Running Attempt Resolution via Lease / Sweeper Path**:
   - The active attempt is resolved through the lease/renewal path: either the worker receives `cancel_requested: true` on its renewal and reports cooperative cancellation, or the worker commits valid completion, or the coordinator lease sweeper reaps the expired lease and transitions `CANCEL_REQUESTED -> CANCELLED`.

### 2. State Differentiation by Lifecycle Phase
When `POST /v1/jobs/{job_id}/cancel` is invoked:
1. **If Job is in `QUEUED`**:
   - Transition: `QUEUED` -> `CANCELLED` immediately.
   - Dispatch eligibility: Job is marked terminal in repository and skipped by subsequent `DispatchNextJob` pulls.
   - API Response: `200 OK` with `state: "CANCELLED"`, `message: "cancellation processed"`.
2. **If Job is in `RETRYING`**:
   - Transition: `RETRYING` -> `CANCELLED` immediately (`internal/domain/transition.go:27`).
   - Scheduled retry aborted; `not_before` cleared.
   - API Response: `200 OK` with `state: "CANCELLED"`, `message: "cancellation processed"`.
3. **If Job is in `RUNNING`**:
   - Transition: `RUNNING` -> `CANCEL_REQUESTED`.
   - Audit Event: Insert `job_events` record with `event_type: 'JOB_CANCEL_REQUESTED'`.
   - API Response: `200 OK` with `state: "CANCEL_REQUESTED"`, `message: "cancellation processed"`.
4. **If Job is in `CANCEL_REQUESTED` (Duplicate Cancel)**:
   - State unchanged: remains in `CANCEL_REQUESTED` (handled in repository `CancelJob` under row lock as idempotent no-op).
   - API Response: `200 OK` with `state: "CANCEL_REQUESTED"`, `message: "cancellation processed"`. (Accepted by owner).
   - Duplicate cancellation returns HTTP 200 with the identical response body as the initial request, changes nothing, and adds no duplicate audit events. There is no `duplicate` response field.
5. **If Job is in Terminal State (`SUCCEEDED`, `FAILED`, `CANCELLED`, `REJECTED`)**:
   - Return HTTP `409 Conflict` with error code `JOB_ALREADY_TERMINAL`.

*Note on Cancel Status Codes (Unchanged from Today):* The cancellation handler (`internal/api/handlers.go:267`) returns HTTP 200 `OK` for all valid cancellation requests (whether the job was `QUEUED`, `RETRYING`, `RUNNING`, or already `CANCEL_REQUESTED`). Cancel status codes are completely unchanged from today. There is no HTTP 202 Accepted, no ADR-0004 compatibility exception, and no `duplicate` response field. Other HTTP status codes returned by `/cancel` remain unchanged: 400 (`INVALID_REQUEST`), 404 (`JOB_NOT_FOUND`), 409 (`JOB_ALREADY_TERMINAL`), and 503 (`SERVICE_UNAVAILABLE`).

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
   - **No Extension on Cancel**: On a job in `CANCEL_REQUESTED`, the renewal response returns `"cancel_requested": true` and does **not** extend `lease_expires_at` (it returns the current unextended expiry). (Accepted by owner).
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
  4. The worker stops renewing the lease, and reports its result outcome to `POST /v1/jobs/{id}/attempts/{attempt_id}/result`. The coordinator requires no dedicated cancel acknowledgement signal.
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
  - **Success Outcome**: If the worker successfully produced a valid completion (`outcome.ExitCode == 0 && outcome.Error == ""`):
    - Attempt transitions: `AttemptCompleted` (`"COMPLETED"`).
    - Job transitions: `CANCEL_REQUESTED` -> `SUCCEEDED`.
    - Result is stored in `results`.
    - Event: `JobEventCompleted` (`"JOB_COMPLETED"`).
    - This preserves today's verified behavior in `internal/storage/inmemory.go:610-622` and `internal/storage/postgres/repository.go:1037-1044`.
  - **Failure / Cancel Outcome**: Any commit on a job in `CANCEL_REQUESTED` with a failure (non-zero exit code or error string, including retryable failures):
    - Closes the attempt with the outcome reported by the worker (attempt `state = 'FAILED'`).
    - Job transitions: `CANCEL_REQUESTED` -> `CANCELLED` (terminal; cancel wins, never `RETRYING` from `CANCEL_REQUESTED`).
    - Event: `JobEventCancelled` (`"JOB_CANCELLED"`).

#### Race 2: Cancellation vs Lease Expiry (Zombie / Unresponsive Worker)
- **Scenario**: Job is in `CANCEL_REQUESTED`. Worker hangs in non-cooperative task (ignoring context cancellation), loses network connectivity, or dies before reporting outcome.
- **Resolution**:
  - The renewal response on `CANCEL_REQUESTED` did not extend the lease, and the worker ceased renewing. Therefore, the lease expires within at most one lease duration (30 seconds) from the last renewal.
  - The coordinator lease sweeper (ADR-0014) locks `jobs -> workers -> attempts`.
  - Seeing `job.state == 'CANCEL_REQUESTED'` and `lease_expires_at < clock_timestamp()`:
    1. Sweeper transitions attempt to `CANCELLED`.
    2. Sweeper transitions job: `CANCEL_REQUESTED` -> `CANCELLED` (terminal).
    3. Sweeper **suppresses retries**: even if `attempt_no < max_attempts`, a cancelled job is never retried (never `RETRYING`).
    4. Event: `JobEventCancelled` with detail `{"resolved_by": "lease_sweeper", "reason": "worker unresponsive"}`.
  - **Sweeper Rule for Already-Terminal Jobs**:
    If the sweeper encounters an attempt whose job is already in a terminal state (`CANCELLED`, `SUCCEEDED`, or `FAILED`), it closes the attempt: to `CANCELLED` if the job is `CANCELLED`, or to `LOST` otherwise. The job state remains unchanged, the worker slot is released, and a diagnostic event is recorded.

#### Race 3: Duplicate Cancellation Requests
- **Scenario**: Client issues multiple concurrent or sequential `POST /v1/jobs/{id}/cancel` calls.
- **Resolution**:
  - Handled directly in the repository `CancelJob` method under the row lock.
  - First request transitions job to `CANCELLED` (from `QUEUED` / `RETRYING`) or `CANCEL_REQUESTED` (from `RUNNING`).
  - Subsequent requests:
    - If job is in `CANCEL_REQUESTED`: Idempotently return HTTP 200 `OK` with `state: "CANCEL_REQUESTED"`, `message: "cancellation processed"` (same body as initial request, no state change, no duplicate event).
    - If job is in terminal `CANCELLED` (or `SUCCEEDED`/`FAILED`): Return HTTP `409 Conflict` with `code: "JOB_ALREADY_TERMINAL"`.
- **Existing Test Impact**:
  - Existing tests `CancelJob_QueuedAndRunning` in `internal/storage/storagetest/conformance.go:299-363` (specifically line 335) and `TestInMemoryRepository_SubmitJob_And_CancelJob_Events` in `internal/storage/inmemory_test.go:559` (specifically line 607) test duplicate cancel on terminal `QUEUED -> CANCELLED`, asserting `ErrJobAlreadyTerminal`. Neither test asserts duplicate cancel on `CANCEL_REQUESTED`.
  - Conformance plan adds `TestConformance_CancelJob_IdempotentCancelRequested` (planned test) to verify in-memory and PostgreSQL parity for duplicate cancel on `CANCEL_REQUESTED`.

---

## Consequences

### Positive
- **Deterministic Bounded Resolution**: Bounded 30s lease sweeps ensure `CANCEL_REQUESTED` always resolves to a terminal state within at most one lease duration (30 seconds) plus sweeper cadence, even if an uncooperative task ignores context cancellation, without requiring a separate reaper timer.
- **Protocol Simplicity**: Eliminating heartbeat cancellation leaves heartbeats focused solely on node health and reduces protocol surface area.
- **Idempotency**: Clients retrying cancellation requests on network blips receive safe HTTP 200 responses without corrupting attempt state.

### Negative / Trade-offs
- Maximum cancellation delivery latency is 10 seconds (one renewal period).
- In-process tasks that do not check `ctx.Done()` cannot be forcibly killed in V1 without OS process termination. Hard process isolation is deferred to V3 Docker containers.

### Invariants Protected
- Invariant 2: Central State Validation (FSM transitions validated centrally)
- Invariant 4: Bounded Execution (cancellation cannot remain pending forever)
