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
| `CANCEL_REQUESTED` | Active / Draining | Cancellation was requested while an attempt was running; awaiting worker acknowledgement, completion, or lease expiry. | `CANCELLED`, `SUCCEEDED`, `FAILED` |
| `RETRYING` | Active / Backoff | An attempt failed or timed out, and the job is waiting out exponential backoff before becoming eligible for re-dispatch. | `QUEUED`, `RUNNING`, `FAILED`, `CANCELLED` |
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
| **`CANCEL_REQUESTED`** | NO | NO | NO | — | NO | **YES** | **YES** | **YES** | NO |
| **`RETRYING`** | NO | **YES** | **YES** | NO | — | NO | **YES** | **YES** | NO |
| **`SUCCEEDED`** | NO | NO | NO | NO | NO | — | NO | NO | NO |
| **`FAILED`** | NO | NO | NO | NO | NO | NO | — | NO | NO |
| **`CANCELLED`** | NO | NO | NO | NO | NO | NO | NO | — | NO |
| **`REJECTED`** | NO | NO | NO | NO | NO | NO | NO | NO | — |

*Note: Terminal states (`SUCCEEDED`, `FAILED`, `CANCELLED`, `REJECTED`) are strictly immutable. No transition away from a terminal state is permitted under any circumstances.*

### Mapping to Existing Code and New Phase 4 Transitions
- `SUBMITTED -> QUEUED`: Existing in `internal/coordinator/service.go:184`.
- `QUEUED -> RUNNING`: Existing in `internal/storage/inmemory.go:402` and `internal/storage/postgres/repository.go:889`.
- `QUEUED -> CANCELLED`: Existing in `internal/storage/inmemory.go:674` and `internal/storage/postgres/repository.go:1185`.
- `RUNNING -> CANCEL_REQUESTED`: Existing in `internal/storage/inmemory.go:677` and `internal/storage/postgres/repository.go:1188`.
- `RUNNING -> SUCCEEDED`: Existing in `internal/storage/inmemory.go:618` and `internal/storage/postgres/repository.go:1048`.
- `RUNNING -> FAILED`: Existing in `internal/storage/inmemory.go:622` and `internal/storage/postgres/repository.go:1052` (when non-retryable or retries exhausted).
- **NEW Phase 4 Transitions**:
  - `RUNNING -> RETRYING`: Triggered when an attempt fails with a retryable error or when the lease expiry sweeper detects lease lapse and `attempt_no < max_attempts` (ADR-0014, ADR-0015).
  - `CANCEL_REQUESTED -> CANCELLED`: Triggered when worker confirms cancellation via result report with `context.Canceled`, or when the lease sweeper reaps the expired attempt (ADR-0016).
  - `CANCEL_REQUESTED -> SUCCEEDED`: Triggered when a worker successfully completes and commits before receiving the cancel signal (ADR-0016).
  - `CANCEL_REQUESTED -> FAILED`: Triggered when a worker fails with non-retryable error before observing cancel (ADR-0016).
  - `RETRYING -> RUNNING` (or `RETRYING -> QUEUED`): Triggered when `not_before <= clock_timestamp()` and an eligible worker pulls the job (ADR-0015).
  - `RETRYING -> CANCELLED`: Triggered when `CancelJob` is called on a job awaiting retry backoff (ADR-0016).
  - `RETRYING -> FAILED`: Triggered if a manual administrative abort or policy exhaustion occurs.

---

## 4. Attempt Lifecycle and State Machine

Each job execution generates a separate, explicitly tracked `Attempt`:
- `attempt_no`: Monotonically increasing 1-based integer (`1, 2, 3...`).
- `attempt_id`: Unique identifier formatted as `att_{job_id}_{attempt_no}`.
- `lease_token_hash`: SHA-256 digest of the 32-byte cryptographically secure URL-safe lease token.
- `lease_expires_at`: Database-authoritative expiration timestamp (`TIMESTAMPTZ`).
- `state`: Attempt state (`PENDING`, `RUNNING`, `COMPLETED`, `FAILED`, `TIMED_OUT`, `CANCELLED`, `LOST`).

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

### Attempt States & Schema Alignment
The PostgreSQL schema check constraint `chk_attempts_state` (`000001_initial_schema.sql:97`) defines:
`CHECK (state IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'TIMED_OUT', 'CANCELLED', 'LOST'))`.
Phase 4 formally activates and transitions all previously unused states:
- `PENDING`: Staged for post-Phase 4 Assignment-Based Round-Robin slice (Candidate A pre-assignment window).
- `RUNNING`: Initial state upon pull dispatch. Holds an active lease (`lease_expires_at`).
- `COMPLETED`: Terminal attempt success (exit code 0, empty error).
- `FAILED`: Terminal attempt execution failure (non-zero exit code or application error).
- `TIMED_OUT`: Attempt exceeded cumulative `timeout_seconds`.
- `CANCELLED`: Worker cooperatively halted task in response to `cancel_requested: true`.
- `LOST`: Worker lease expired without renewal (worker crashed, partitioned, or dead).

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
3. **Stale Result Rejection:** If an old worker returns a result after its lease expired or after a newer attempt has started, the coordinator rejects the commit with `ErrStaleAttempt` (HTTP 409 Conflict).
4. **Single Terminal Commit:** Exactly one terminal outcome is recorded per job in `results`. Once a job reaches `SUCCEEDED`, `FAILED`, `CANCELLED`, or `REJECTED`, all subsequent outcome reports are ignored or logged as diagnostics.
5. **Bounded Execution:** Maximum attempts are bounded by `max_attempts` ($1..20$, default 3). Exceeding `max_attempts` transitions the job permanently to `FAILED`.
6. **Lease Expiry Semantics:** A lease expiration proves that the coordinator stopped trusting the worker; it does not prove the worker process halted. Hence, subsequent result reporting by that worker is safely blocked by lease token and attempt state mismatch.
7. **Database Clock Authority:** All lease expiration evaluations, worker heartbeats, and retry `not_before` times evaluate strictly on database time (`clock_timestamp()`).
