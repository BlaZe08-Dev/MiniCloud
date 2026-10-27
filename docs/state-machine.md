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
| **`CANCEL_REQUESTED`** | NO | NO | NO | — (service no-op) | NO | **YES** | **YES** | **YES** | NO |
| **`RETRYING`** | NO | NO | **YES** | NO | — | NO | **YES** | **YES** | NO |
| **`SUCCEEDED`** | NO | NO | NO | NO | NO | — | NO | NO | NO |
| **`FAILED`** | NO | NO | NO | NO | NO | NO | — | NO | NO |
| **`CANCELLED`** | NO | NO | NO | NO | NO | NO | NO | — | NO |
| **`REJECTED`** | NO | NO | NO | NO | NO | NO | NO | NO | — |

*Note: In the domain FSM (`internal/domain/transition.go:27`), the diagonal is strictly `—` (no self-transitions). A duplicate cancel on `CANCEL_REQUESTED` is handled at the coordinator service and HTTP API layer as an idempotent HTTP 200 no-op without invoking an FSM state transition. Terminal states (`SUCCEEDED`, `FAILED`, `CANCELLED`, `REJECTED`) are strictly immutable. No transition away from a terminal state is permitted under any circumstances.*

### Current Code Behavior vs Phase 4 Behavior Changes
- **Verified Current Cancellation Behavior**:
  Cancellation today behaves identically in `internal/storage/inmemory.go:658-683` and `internal/storage/postgres/repository.go:1135-1215`:
  1. Terminal job -> `domain.ErrJobAlreadyTerminal`.
  2. `QUEUED` -> `CANCELLED` (event `cancelled`).
  3. `RUNNING` -> `CANCEL_REQUESTED` (event `cancel_requested`), with the active attempt untouched.
  4. Any other non-terminal state (including `CANCEL_REQUESTED` and future `RETRYING`) -> falls through to `CANCELLED` (event `cancelled`).
  5. Workers learn nothing today (heartbeats carry static `{"status":"ACK"}`).
- **Correction of Prior Misdescription**: Prior design drafts incorrectly claimed that cancelling a `RUNNING` job moved it directly to `CANCELLED` and marked the attempt `FAILED`. That description was false. The code already sets `CANCEL_REQUESTED` on first call for `RUNNING` jobs and leaves the attempt in `RUNNING`.
- **Phase 4 Behavior Changes Introduced**:
  1. **Idempotent Duplicate Cancel on `CANCEL_REQUESTED`**: Currently, a second cancel on `CANCEL_REQUESTED` moves the job prematurely to `CANCELLED` while the worker may still be executing. Under Phase 4, duplicate cancel is an idempotent no-op returning HTTP 200 with state `CANCEL_REQUESTED` (identical response body as the first cancel).
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

### Canonical State Names, Writing Code Paths & Migration Plan
MiniCloud establishes the schema values as the canonical state names across the system:
- **Canonical Attempt States**: `PENDING`, `RUNNING`, `COMPLETED`, `FAILED`, `TIMED_OUT`, `CANCELLED`, `LOST`.
- **Spelling Alignment**: Uses double-L `CANCELLED` across both job and attempt states (`domain.StateCancelled`, `domain.AttemptCancelled`).

#### Complete Mapping Table: Domain Constants to Schema Values

| Domain Constant (`internal/domain/state.go`) | Constant Value | Schema Enum (`chk_attempts_state`) | Writing Code Path | State Transition Trigger |
|---|---|---|---|---|
| `AttemptPending` | `"PENDING"` | `PENDING` | `repo.AssignJob` (planned; post-Phase 4 Round-Robin) | Pre-assignment window before worker claims slot |
| `AttemptRunning` | `"RUNNING"` | `RUNNING` | `repo.DispatchJob` (`internal/storage/postgres/repository.go:634`) and `repo.DispatchNextJob` (`internal/storage/postgres/repository.go:873`) | Worker claims job via pull dispatch |
| `AttemptCompleted` | `"COMPLETED"` | `COMPLETED` | `repo.CommitAttempt` (`internal/storage/postgres/repository.go:946`) | Worker reports exit code 0 and empty error |
| `AttemptFailed` | `"FAILED"` | `FAILED` | `repo.CommitAttempt` (`internal/storage/postgres/repository.go:946`) | Worker reports non-zero exit code or task error |
| `AttemptTimedOut` (reserved) | `"TIMED_OUT"` | `TIMED_OUT` | Reserved state in schema; not written in V1 | Reserved; worker executor timeout reports `FAILED` with execution timeout |
| `AttemptCancelled` (canonical) / `AttemptCanceled` | `"CANCELLED"` | `CANCELLED` | `repo.CommitAttempt` / Sweeper | Worker acknowledges cancel or sweeper reaps attempt on cancelled job |
| `AttemptLost` (canonical) / `AttemptExpired` (alias) | `"LOST"` | `LOST` | Coordinator Sweeper (ADR-0014) | Lease expires without renewal (`lease_expires_at < clock_timestamp()`) |

#### Code & API Mapping Plan
In Slice 4a, `internal/domain/state.go` will be updated to declare:
```go
const (
    AttemptPending   AttemptState = "PENDING"
    AttemptRunning   AttemptState = "RUNNING"
    AttemptCompleted AttemptState = "COMPLETED"
    AttemptFailed    AttemptState = "FAILED"
    AttemptTimedOut  AttemptState = "TIMED_OUT"
    AttemptCancelled AttemptState = "CANCELLED"
    AttemptLost      AttemptState = "LOST"
)

// Aliases preserved during Phase 4 transition for backwards compatibility:
const (
    AttemptCanceled = AttemptCancelled
    AttemptExpired  = AttemptLost
)
```
In-memory attempt state validation: In Slice 4a, the in-memory repository will validate attempt states against canonical attempt states (preventing typos like `"CANCELED"` from being stored in memory tests).

#### Migration Impact
Database migration `000001_initial_schema.sql:97` already defined `chk_attempts_state` with `CHECK (state IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'TIMED_OUT', 'CANCELLED', 'LOST'))`. Therefore, zero database schema migrations or data backfills are required.

#### Verification Proof: No Stored Rows or API Output Contain Old Spellings
A codebase-wide inspection confirms that no stored rows or API outputs ever used `"EXPIRED"` or single-L `"CANCELED"`, as verified by the following grep queries:
```text
$ grep -rn '"EXPIRED"' internal/
internal/domain/state.go:26:	AttemptExpired   AttemptState = "EXPIRED"

$ grep -rn '"CANCELED"' internal/
internal/domain/state.go:27:	AttemptCanceled  AttemptState = "CANCELED"

$ grep -rn 'AttemptExpired' internal/
internal/domain/state.go:26:	AttemptExpired   AttemptState = "EXPIRED"

$ grep -rn 'AttemptCanceled' internal/
internal/domain/state.go:27:	AttemptCanceled  AttemptState = "CANCELED"
```
Neither constant was ever referenced in repository queries, database inserts, or API serialization routines. All persistence layers write canonical strings exclusively.

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
2. **Commit Authority:** Only the attempt matching the job's current `attempt_id` while still in `RUNNING` state (not yet swept to `LOST`) and presenting the matching `lease_token` can transition the job to `SUCCEEDED` or `FAILED`.
3. **Stale Result Rejection:** If an old worker returns a result after its lease expired and was swept to `LOST`, the coordinator rejects the commit with `ErrStaleAttempt` (HTTP 409 Conflict). Expired attempts are never resurrected.
4. **Single Terminal Commit:** Exactly one terminal outcome is recorded per job in `results`. Once a job reaches `SUCCEEDED`, `FAILED`, `CANCELLED`, or `REJECTED`, all subsequent outcome reports are ignored or logged as diagnostics.
5. **Bounded Execution:** Maximum attempts are bounded by `max_attempts` (1..20, default 3). Exceeding `max_attempts` transitions the job permanently to `FAILED`.
6. **Lease Expiry Semantics:** A lease expiration proves that the coordinator stopped trusting the worker; it does not prove the worker process halted. Hence, subsequent result reporting by that worker is safely blocked by lease token and attempt state mismatch.
7. **Database Clock Authority:** All lease expiration evaluations, worker heartbeats, and retry `not_before` times evaluate strictly on database time (`clock_timestamp()`).
