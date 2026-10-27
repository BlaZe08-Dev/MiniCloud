# MiniCloud State Machine & Lifecycle Specification

## 1. Overview
The job and attempt lifecycles in MiniCloud are governed by a formal finite state machine (FSM). All transitions are validated centrally by `internal/domain` rules. Any transition attempt outside the allowed transition matrix must be rejected with a typed error (`domain.ErrInvalidRequest` or `domain.NewInvalidTransitionError`).

---

## 2. Job States

| State | Classification | Meaning | Allowed Next States |
|---|---|---|---|
| `SUBMITTED` | Ephemeral / Initial | Job has passed schema validation and been recorded by the API. | `QUEUED`, `REJECTED` |
| `QUEUED` | Active / Pending | Job is waiting in the scheduling pool for an eligible worker. | `RUNNING`, `CANCELLED` |
| `RUNNING` | Active / In-Flight | An attempt has been dispatched and accepted by a worker under a lease. | `SUCCEEDED`, `RETRYING`, `FAILED`, `CANCEL_REQUESTED` |
| `CANCEL_REQUESTED` | Active / Draining | Cancellation was requested while an attempt was running; awaiting worker acknowledgement, completion, or lease expiry. | `CANCELLED`, `SUCCEEDED`, `FAILED`, `CANCEL_REQUESTED` (idempotent no-op) |
| `RETRYING` | Active / Backoff | An attempt failed or timed out, and the job is waiting out exponential backoff before becoming eligible for re-dispatch. | `RUNNING`, `FAILED`, `CANCELLED` |
| `SUCCEEDED` | **Terminal** | A valid attempt result has been committed to durable storage. | *None* |
| `FAILED` | **Terminal** | Execution failed permanently, or maximum attempt budget was exhausted. | *None* |
| `CANCELLED` | **Terminal** | Job was cancelled before execution started, or running attempt confirmed cooperative cancellation / expired. | *None* |
| `REJECTED` | **Terminal** | Job submission was rejected before acceptance (e.g., unsupported task type, invalid payload bounds). | *None* |

---

## 3. Job Transition Matrix

The table below defines whether a transition from `Current State` (rows) to `Target State` (columns) is permitted:

| From \ To | `SUB` | `QUE` | `RUN` | `CAN_REQ` | `RET` | `SUC` | `FAI` | `CAN` | `REJ` |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| **`SUBMITTED`** | — | **YES** | NO | NO | NO | NO | NO | NO | **YES** |
| **`QUEUED`** | NO | — | **YES** | NO | NO | NO | NO | **YES** | NO |
| **`RUNNING`** | NO | NO | — | **YES** | **YES** | **YES** | **YES** | NO | NO |
| **`CANCEL_REQUESTED`** | NO | NO | NO | **YES** (no-op) | NO | **YES** | **YES** | **YES** | NO |
| **`RETRYING`** | NO | NO | **YES** | NO | — | NO | **YES** | **YES** | NO |
| **`SUCCEEDED`** | NO | NO | NO | NO | NO | — | NO | NO | NO |
| **`FAILED`** | NO | NO | NO | NO | NO | NO | — | NO | NO |
| **`CANCELLED`** | NO | NO | NO | NO | NO | NO | NO | — | NO |
| **`REJECTED`** | NO | NO | NO | NO | NO | NO | NO | NO | — |

*Note: Terminal states (`SUCCEEDED`, `FAILED`, `CANCELLED`, `REJECTED`) are strictly immutable. No transition away from a terminal state is permitted under any circumstances.*

### Current Code Behavior vs Phase 4 Behavior Changes
- **Verified Current Cancellation Behavior**:
  Cancellation today behaves identically in `internal/storage/inmemory.go:658-683` and `internal/storage/postgres/repository.go:1135-1215`:
  1. Terminal job $\rightarrow$ `domain.ErrJobAlreadyTerminal`.
  2. `QUEUED` $\rightarrow$ `CANCELLED` (event `cancelled`).
  3. `RUNNING` $\rightarrow$ `CANCEL_REQUESTED` (event `cancel_requested`), with the active attempt untouched.
  4. Any other non-terminal state (including `CANCEL_REQUESTED` and future `RETRYING`) $\rightarrow$ falls through to `CANCELLED` (event `cancelled`).
  5. Workers learn nothing today (heartbeats carry static `{"status":"ACK"}`).
- **Correction of Prior Misdescription**: Prior design drafts incorrectly claimed that cancelling a `RUNNING` job moved it directly to `CANCELLED` and marked the attempt `FAILED`. That description was false. The code already sets `CANCEL_REQUESTED` on first call for `RUNNING` jobs and leaves the attempt in `RUNNING`.
- **Phase 4 Behavior Changes Introduced**:
  1. **Idempotent Duplicate Cancel on `CANCEL_REQUESTED`**: Currently, a second cancel on `CANCEL_REQUESTED` moves the job prematurely to `CANCELLED` while the worker may still be executing. Under Phase 4, duplicate cancel is an idempotent no-op returning HTTP 202 `CANCEL_REQUESTED`.
  2. **Cancellation during `RETRYING`**: Cancelling a job in `RETRYING` stays an immediate transition to `CANCELLED`, clearing `not_before` and aborting scheduled retries.
  3. **Running Attempt Resolution via Lease Path**: The running attempt is handled via the lease/renewal path: the sweeper or commit path resolves `CANCEL_REQUESTED` jobs.
  4. **Direct `RETRYING -> RUNNING` via Pull Dispatch**: Blueprint conceptual diagrams describe `RETRYING -> QUEUED` when a retry backoff timer elapses. In MiniCloud's implementation, transitioning `RETRYING -> RUNNING` directly via `repo.DispatchNextJob` (`SELECT ... FOR UPDATE SKIP LOCKED` where `(state = 'QUEUED' OR (state = 'RETRYING' AND not_before <= clock_timestamp()))`) eliminates unnecessary intermediate database writes and polling loops while preserving full auditability via `JobEventDispatched` (`from_state: 'RETRYING'`, `to_state: 'RUNNING'`). (Accepted by owner).

---

## 4. Attempt Lifecycle, State Machine & State Naming Reconciliation

### Reconciling Domain Constants and Database Schema Values
There was an initial naming mismatch between the domain constants and PostgreSQL schema constraints:
- `internal/domain/state.go:22-27` defined:
  `AttemptPending = "PENDING"`, `AttemptRunning = "RUNNING"`, `AttemptCompleted = "COMPLETED"`, `AttemptFailed = "FAILED"`, `AttemptExpired = "EXPIRED"`, `AttemptCanceled = "CANCELED"`.
- PostgreSQL schema CHECK constraint `chk_attempts_state` (`000001_initial_schema.sql:97`) defines:
  `CHECK (state IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'TIMED_OUT', 'CANCELLED', 'LOST'))`.

### Canonical State Names & Migration Plan
MiniCloud establishes the schema values as the canonical state names across the system:
- **Canonical Attempt States**: `PENDING`, `RUNNING`, `COMPLETED`, `FAILED`, `TIMED_OUT`, `CANCELLED`, `LOST`.
- **Spelling Alignment**: Uses double-L `CANCELLED` across both job and attempt states (`domain.StateCancelled`, `domain.AttemptCancelled`).
- **Semantic Distinction**:
  - `LOST`: Worker lease expired without renewal (worker crashed, partitioned, or uncommunicative). Replaces domain `EXPIRED`.
  - `TIMED_OUT`: Attempt exceeded cumulative `timeout_seconds`.
- **Code & API Mapping Plan**:
  - In Slice 4a: Update `internal/domain/state.go` to declare:
    ```go
    AttemptPending   AttemptState = "PENDING"
    AttemptRunning   AttemptState = "RUNNING"
    AttemptCompleted AttemptState = "COMPLETED"
    AttemptFailed    AttemptState = "FAILED"
    AttemptTimedOut  AttemptState = "TIMED_OUT"
    AttemptCancelled AttemptState = "CANCELLED"
    AttemptLost      AttemptState = "LOST"
    ```
    (Preserve aliases `AttemptCanceled = AttemptCancelled` and `AttemptExpired = AttemptLost` during transition if needed for compatibility).
  - Database rows already store canonical strings; migration 000001 already permits all 7 values in `chk_attempts_state`. No schema migration required for state names.
  - API responses serialize the canonical strings directly.

### Attempt Lifecycle Diagram

```text
               [Dispatch Job: repo.DispatchNextJob]
                                │
                                ▼
                            (RUNNING) ──[Worker renews: /renew]──► (RUNNING)
                           /    │    \           │
                          /     │     \          │ (lease_expires_at extended)
                         /      │      \         ▼
      [Success Outcome] /       │       \ [Lease Expired / Worker Vanished]
                       /        │        \
                      ▼         │         ▼
                 (COMPLETED)    │       (LOST)
                                │
          ┌─────────────────────┴─────────────────────┐
          ▼                                           ▼
   [Task Failure]                             [Cancel Acknowledged]
          │                                           │
          ▼                                           ▼
       (FAILED)                                  (CANCELLED)
```

- `PENDING`: Staged for post-Phase 4 Assignment-Based Round-Robin slice (Candidate A pre-assignment window).
- `RUNNING`: Initial state upon pull dispatch. Holds an active lease (`lease_expires_at`). Under the **Symmetric Accept-Until-Swept Rule**, renewals (`POST /renew`) and commits (`POST /result`) are accepted while `state == 'RUNNING'` even if `lease_expires_at` has passed on database time; renewal extends the lease from current database time (`clock_timestamp() + lease_duration`). HTTP 409 Conflict (`LEASE_EXPIRED` / `domain.ErrLeaseExpired`) is returned only after the attempt has been swept to `LOST` by the coordinator sweeper.
- `COMPLETED`: Terminal attempt success (exit code 0, empty error).
- `FAILED`: Terminal attempt execution failure (non-zero exit code or application error).
- `TIMED_OUT`: Attempt exceeded cumulative `timeout_seconds`.
- `CANCELLED`: Worker cooperatively halted task in response to `cancel_requested: true`.
- `LOST`: Worker lease expired without renewal and swept by the coordinator sweeper. Consumes 1 attempt of `max_attempts`. Any subsequent commit or renewal attempt is rejected with HTTP 409 Conflict (`LEASE_EXPIRED`) and `domain.ErrLeaseExpired`.

---

## 5. Lifecycle Audit Events (`job_events`)

Phase 4 defines the complete audit event vocabulary stored in `job_events`:

| Event Type | Actor | Trigger | Details JSON Payload |
|---|---|---|---|
| `job_submitted` | Client / API | Job accepted via `POST /v1/jobs`. | `{"task_type": "...", "max_attempts": 3}` |
| `job_queued` | Coordinator | Job placed into scheduling pool. | `{}` |
| `job_dispatched` | Worker / Poller | Job claimed via `repo.DispatchNextJob`. | `{"worker_id": "...", "attempt_no": 1, "policy": "fifo_pull"}` |
| `lease_renewed` | Worker / Agent | Worker renewed attempt lease via `/renew`. | `{"attempt_id": "...", "worker_id": "...", "new_expires_at": "..."}` |
| `lease_expired` | Sweeper | Coordinator swept expired attempt. | `{"attempt_id": "...", "worker_id": "...", "expired_at": "..."}` |
| `job_retrying` | Coordinator | Retryable failure scheduled for backoff. | `{"attempt_no": 1, "not_before": "...", "backoff_seconds": 2.5}` |
| `job_cancel_requested` | Client / API | Client requested cancellation for running job. | `{"requested_at": "..."}` |
| `job_cancelled` | Worker / Sweeper | Cancellation confirmed or resolved. | `{"resolved_by": "worker_ack|lease_sweeper"}` |
| `job_completed` | Worker | Worker committed successful result. | `{"worker_id": "...", "exit_code": 0}` |
| `job_failed` | Worker / Sweeper | Worker committed permanent error or budget exhausted. | `{"worker_id": "...", "exit_code": 1, "error": "..."}` |
| `diagnostic_late_result` | Worker | Expired worker attempted commit after lease lapse. | `{"reporting_worker": "...", "attempt_id": "...", "rejected": true}` |

---

## 6. Critical Distributed Invariants

1. **Unique Identity:** A job has a stable immutable ID across all retries. Each execution has a distinct `attempt_id` and monotonic `attempt_no`.
2. **Commit Authority:** Only the attempt matching the job's current `attempt_id` and holding a valid, unexpired `lease_token` can transition the job to `SUCCEEDED` or `FAILED`.
3. **Stale Result Rejection:** If an old worker returns a result after its lease expired and was swept to `LOST`, the coordinator rejects the commit with `ErrStaleAttempt` (HTTP 409 Conflict). Expired attempts are never resurrected.
4. **Single Terminal Commit:** Exactly one terminal outcome is recorded per job in `results`. Once a job reaches `SUCCEEDED`, `FAILED`, `CANCELLED`, or `REJECTED`, all subsequent outcome reports are ignored or logged as diagnostics.
5. **Bounded Execution:** Maximum attempts are bounded by `max_attempts` ($1..20$, default 3). Exceeding `max_attempts` transitions the job permanently to `FAILED`.
6. **Lease Expiry Semantics:** A lease expiration proves that the coordinator stopped trusting the worker; it does not prove the worker process halted. Hence, subsequent result reporting by that worker is safely blocked by lease token and attempt state mismatch.
7. **Database Clock Authority:** All lease expiration evaluations, worker heartbeats, and retry `not_before` times evaluate strictly on database time (`clock_timestamp()`).
