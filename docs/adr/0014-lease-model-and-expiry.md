# ADR 0014: Attempt Lease Model, Renewal Protocol, and Expiry Sweeper

## Status
PROPOSED (Owner Approval Pending)

## Context
In MiniCloud V1 Phase 3, jobs persist durably in PostgreSQL and workers pull work via atomic database transactions (`repo.DispatchNextJob`, ADR-0011 Option A, ADR-0013). However, Phase 3 explicitly deferred in-flight failure handling:
- As documented in `docs/failure-model.md` and `docs/PROJECT_STATE.md`, if a worker process crashes, loses power, or suffers a network partition while executing a task, the job remains in `RUNNING` state indefinitely.
- The worker reaper loop (`internal/coordinator/service.go:320-333`) marks inactive workers `UNHEALTHY` when `last_heartbeat_at` exceeds `worker_timeout`, but intentionally leaves active attempts untouched to avoid premature or uncoordinated state mutation.
- A partitioned or slow worker may wake up after arbitrary delays and attempt to commit results. Without lease boundaries, late commits could overwrite newer attempts or terminal states.

Blueprint Sections 4, 5, 6, and 10 mandate that:
1. Every execution attempt runs under an explicit, bounded lease on database time.
2. Workers actively renew leases while healthy and executing.
3. The coordinator sweeps expired leases and reclaims abandoned jobs.
4. Lease expiry represents a loss of coordinator trust in the worker, not physical proof of process termination.
5. Late results from expired attempts are strictly rejected for state transitions but retained as diagnostic events.

---

## Decision

### 1. Database-Authoritative Lease Timestamp (`lease_expires_at`)
- Every execution attempt gains a `lease_expires_at TIMESTAMPTZ NOT NULL` column in the `attempts` table.
- **Initial Lease at Dispatch**: When an attempt is created in `repo.DispatchNextJob` or `repo.DispatchJob`, the initial expiry is computed strictly using database time:
  ```sql
  INSERT INTO attempts (
      id, job_id, attempt_no, worker_id, lease_token_hash, state, started_at, lease_expires_at
  ) VALUES (
      $1, $2, $3, $4, $5, 'RUNNING', clock_timestamp(),
      clock_timestamp() + make_interval(secs => $6::double precision)
  );
  ```
- **Clock Skew Immunity**: Worker system clocks are completely untrusted. All comparisons, renewals, and expirations evaluate against `clock_timestamp()` (PostgreSQL) or `Clock.Now()` (`InMemoryRepository`).

### 2. Lease Timing Ratios & Durations
MiniCloud enforces explicit timing hierarchies across worker health, leases, and task execution:
- **Worker Heartbeat Interval**: Default 2 seconds (`MINICLOUD_HEARTBEAT_INTERVAL`).
- **Worker Liveness Timeout**: Default 10 seconds ($5 \times \text{heartbeat interval}$, `MINICLOUD_WORKER_TIMEOUT`).
- **Attempt Lease Duration**: Default **30 seconds** (`MINICLOUD_DEFAULT_LEASE_DURATION`).
- **Worker Renewal Interval**: Default **10 seconds** ($1/3$ of lease duration). A healthy worker attempts renewal at $t = 10\text{s}$ and $t = 20\text{s}$, tolerating up to two consecutive transient network failures before the 30-second lease lapses.
- **Relationship to `timeout_seconds`**: Task `timeout_seconds` governs maximum execution runtime (e.g., 60s, 300s, 86400s). The attempt lease is intentionally decoupled and much shorter (30s). A 1-hour job holds a series of 30-second leases continuously refreshed every 10 seconds. If the worker crashes 5 seconds into execution, the failure is detected and reclaimed within ~35 seconds, rather than waiting 1 hour.

### 3. Worker Renewal Protocol: Dedicated vs Piggyback Analysis

| Dimension | Option 1: Piggyback on Heartbeat (`/heartbeat`) | Option 2: Dedicated Renewal Endpoint (`/renew`) (Recommended) |
|---|---|---|
| **Mechanism** | Worker includes list of active attempt IDs and tokens in `POST /v1/workers/{id}/heartbeat`. | Worker issues `POST /v1/jobs/{id}/attempts/{attempt_id}/renew` for each active execution. |
| **Separation of Concerns** | Conflates node liveness telemetry with task-level attempt ownership. | Strictly decouples node health from task lease contracts. |
| **Error Granularity** | Partial failures (e.g. 1 attempt expired, 2 valid) require complex response envelopes. | Standard HTTP status codes per attempt: 200 OK, 403 Forbidden, 404 Not Found, 409 Conflict. |
| **Overhead** | Fewer HTTP requests when concurrency is high ($N$ slots). | $N$ HTTP requests every 10 seconds. In V1 ($1..4$ slots per worker), request overhead is negligible (<0.4 req/sec). |
| **Failure Isolation** | A malformed or rejected attempt could fail the entire worker heartbeat. | An error on one attempt never compromises worker liveness. |

**Decision**: MiniCloud adopts **Option 2 (Dedicated Renewal Endpoint)** as the primary authoritative protocol for attempt lease renewal, while preserving `/heartbeat` strictly for worker process liveness.

#### Endpoint Contract: `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew`
- **Request**:
  ```json
  {
    "worker_id": "worker-1",
    "lease_token": "raw-base64url-token"
  }
  ```
- **Validation**:
  1. Job must exist and be in `RUNNING` or `CANCEL_REQUESTED`.
  2. Attempt must exist, match `job.current_attempt_id`, and be in `RUNNING` state.
  3. SHA-256 hash of `lease_token` must match `attempts.lease_token_hash` in constant time (`subtle.ConstantTimeCompare`).
  4. Reporting `worker_id` must match `attempts.worker_id`.
  5. Current database time must be strictly before `lease_expires_at` (`clock_timestamp() < lease_expires_at`).
- **Response (200 OK)**:
  ```json
  {
    "status": "RENEWED",
    "lease_expires_at": "2026-10-26T20:15:30Z",
    "cancel_requested": false
  }
  ```
- **Cooperative Cancellation Signal**: If `job.state == 'CANCEL_REQUESTED'`, the renewal succeeds (`200 OK`) but returns `"cancel_requested": true`. The worker agent inspects this flag and cancels the task's execution context.
- **Terminal Rejections**:
  - `403 Forbidden` (`WORKER_MISMATCH`): Permanent; worker immediately aborts execution.
  - `409 Conflict` (`STALE_ATTEMPT_REJECTED`): Lease has already expired or attempt was superseded; worker aborts execution immediately.

### 4. Deadlock-Free Coordinator Lease Sweeper
The coordinator runs a background lease expiry sweeper (`StartLeaseSweeper`) analogous to the worker reaper loop.

#### Locking Hierarchy
To prevent deadlocks with concurrent `DispatchNextJob`, `CommitAttempt`, `CancelJob`, and `RenewLease`, the sweeper strictly maintains the canonical lock order defined in ADR-0013:
$$\text{jobs} \longrightarrow \text{workers} \longrightarrow \text{attempts} \longrightarrow \text{results} \longrightarrow \text{job\_events}$$

#### Sweeper Algorithm
1. **Candidate Scan (Unlocked Read)**:
   ```sql
   SELECT a.id, a.job_id, a.worker_id
   FROM attempts a
   WHERE a.state = 'RUNNING'
     AND a.lease_expires_at < clock_timestamp()
   ORDER BY a.lease_expires_at ASC
   LIMIT 50;
   ```
2. **Per-Candidate Atomic Transaction**: For each candidate attempt:
   - Acquire row lock on `jobs` first:
     ```sql
     SELECT id, state, max_attempts FROM jobs WHERE id = $1 FOR UPDATE;
     ```
   - Acquire row lock on `workers` second:
     ```sql
     SELECT id FROM workers WHERE id = $1 FOR UPDATE;
     ```
   - Acquire row lock on `attempts` third:
     ```sql
     SELECT id, state, lease_expires_at FROM attempts WHERE id = $1 FOR UPDATE;
     ```
   - **Re-verify Under Lock**:
     - If attempt is no longer `RUNNING` or `lease_expires_at >= clock_timestamp()`, the attempt was concurrently committed or renewed; roll back cleanly.
   - **Transition Attempt**:
     - Update attempt state to `LOST` (or `TIMED_OUT` if cumulative execution exceeded `job.timeout_seconds`).
     - Set `finished_at = clock_timestamp()`, `error = 'lease expired: worker failed to renew'`.
   - **Transition Job**:
     - If `job.state == 'CANCEL_REQUESTED'`, transition job to `CANCELLED`.
     - Else if `attempt_no < job.max_attempts`, transition job to `RETRYING`, compute exponential backoff with jitter, set `jobs.not_before = clock_timestamp() + backoff`, and requeue.
     - Else (`attempt_no >= job.max_attempts`), transition job to `FAILED` with error `"max attempts exhausted"`.
   - Record `job_events` audit record (`event_type: 'lease_expired'`).

### 5. Stale Attempt Rejection & Diagnostic Auditing
- **Core Invariant**: A worker resuming after lease expiration is treated as a zombie process.
- When an expired worker calls `POST /v1/jobs/{id}/attempts/{attempt_id}/result`:
  1. The transaction locks the job and attempt.
  2. The attempt state is already `LOST` (or `TIMED_OUT`), not `RUNNING`.
  3. The commit is rejected with HTTP 409 `STALE_ATTEMPT_REJECTED`.
  4. The coordinator appends an audit event to `job_events`:
     ```json
     {
       "event_type": "diagnostic_late_result",
       "details": {
         "reporting_worker": "worker-1",
         "attempt_id": "att_01",
         "reason": "result reported after lease expired"
       }
     }
     ```
  5. The result is **never** committed to `results` and never alters the job's terminal state.

---

## Consequences

### Positive
- **Guaranteed Reclamation**: Dead or partitioned workers no longer strand jobs in `RUNNING`.
- **Mathematical Deadlock Freedom**: Adherence to the strict canonical locking order prevents database deadlocks.
- **Clock Drift Immunity**: Relies 100% on database time (`clock_timestamp()`).
- **Audit Traceability**: Late results are preserved as diagnostics without corrupting system truth.

### Negative / Trade-offs
- Active workers must maintain a background renewal goroutine while executing tasks.
- Network partitions between worker and coordinator cause lease expiration even if the worker process is still healthy.

### Invariants Protected
- Invariant 2: Central State Validation
- Invariant 3: Stale Attempt Rejection
- Invariant 6: Lease Expiry Meaning (coordinator loss of trust, not worker death)
