# ADR 0014: Attempt Lease Model, Renewal Protocol, and Expiry Sweeper

## Status
Accepted by Owner (2026-10-26)
- **Accepted Owner Decisions**:
  - Lease duration 30 s, renewal interval 10 s, sweeper interval 5 s with batch size 50.
  - Dedicated renewal endpoint `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew`.
  - Single cancellation delivery channel via renewal response (`cancel_requested: true`).
  - No separate attempt-level timeout in V1 (job `timeout_seconds` bounds overall job runtime; leases bound execution intervals).
  - V1 relies on cooperative context cancellation; hard process kill is deferred to V3 Docker isolation (ADR-0005).
  - No separate `CANCEL_REQUESTED` deadline (the lease bounds it).
  - No resurrection of expired attempts (`LOST` / `TIMED_OUT` is terminal; late results strictly rejected and recorded as diagnostic audit events).
  - Sweeper re-check under row lock (`attempts` state and lease expiration re-verified under `FOR UPDATE`).
  - Coordinator startup grace period and database outage grace window (30 s grace) protecting Phase 3 surviving-worker commit guarantees.
- **Proposed (Implementation Details)**: Sub-slices 4a–4e implementation details remain proposed until merged.

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

### 2. Lease Timing Ratios, Durations, and Bounded Execution
MiniCloud enforces explicit timing hierarchies across worker health, leases, and task execution:
- **Worker Heartbeat Interval**: Default 2 seconds (`MINICLOUD_HEARTBEAT_INTERVAL`).
- **Worker Liveness Timeout**: Default 10 seconds ($5 \times \text{heartbeat interval}$, `MINICLOUD_WORKER_TIMEOUT`).
- **Attempt Lease Duration**: Default **30 seconds** (`MINICLOUD_DEFAULT_LEASE_DURATION`). (Accepted by owner).
- **Worker Renewal Interval**: Default **10 seconds** ($1/3$ of lease duration). A healthy worker attempts renewal at $t = 10\text{s}$ and $t = 20\text{s}$, tolerating up to two consecutive transient network failures before the 30-second lease lapses. (Accepted by owner).
- **Coordinator Sweeper Cadence**: Ticker interval **5 seconds**, batch size **50** attempts per sweep (`MINICLOUD_SWEEPER_INTERVAL`, `MINICLOUD_SWEEPER_BATCH_SIZE`). (Accepted by owner).
- **No Separate Attempt-Level Timeout in V1**: Task `timeout_seconds` governs maximum execution runtime across the entire job. In V1, there is no separate per-attempt execution timeout; the 30-second lease boundary governs attempt liveness. (Accepted by owner).
- **Cooperative Context Cancellation in V1**: Execution termination in V1 relies on cooperative Go context cancellation (`context.Context`). Hard process killing is deferred to V3 Docker container execution isolation (ADR-0005). (Accepted by owner).
- **Relationship to `timeout_seconds`**: Task `timeout_seconds` governs maximum execution runtime (e.g., 60s, 300s, 86400s). The attempt lease is intentionally decoupled and much shorter (30s). A 1-hour job holds a series of 30-second leases continuously refreshed every 10 seconds. If the worker crashes 5 seconds into execution, the failure is detected and reclaimed within ~35 seconds, rather than waiting 1 hour.

### 3. Worker Renewal Protocol: Dedicated Endpoint & Single Cancellation Channel

MiniCloud adopts a dedicated renewal endpoint as the sole authoritative protocol for lease extension and cooperative cancellation delivery:

| Dimension | Option 1: Piggyback on Heartbeat (`/heartbeat`) | Option 2: Dedicated Renewal Endpoint (`/renew`) (Accepted) |
|---|---|---|
| **Mechanism** | Worker includes active attempt IDs and tokens in `POST /v1/workers/{id}/heartbeat`. | Worker issues `POST /v1/jobs/{id}/attempts/{attempt_id}/renew` for each active execution. |
| **Separation of Concerns** | Conflates node liveness telemetry with task-level attempt ownership. | Strictly decouples node health from task lease contracts. |
| **Error Granularity** | Partial failures require complex batch envelopes. | Standard HTTP status codes per attempt: 200 OK, 403 Forbidden, 404 Not Found, 409 Conflict. |
| **Overhead** | Fewer HTTP requests at high concurrency. | Negligible in V1 (<0.4 req/sec per worker slot). |
| **Failure Isolation** | A rejected attempt compromises worker liveness. | An error on one attempt never compromises node liveness. |

**Decision**: MiniCloud adopts **Option 2 (Dedicated Renewal Endpoint)** (Accepted by owner). Heartbeats (`POST /v1/workers/{id}/heartbeat`) remain strictly dedicated to worker node liveness and do not carry renewal tokens or cancelled job arrays.

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
  5. Attempt state must be `RUNNING`. Under the **Symmetric Accept-Until-Swept Rule**, if `lease_expires_at` has elapsed on database time (for example during sweeper grace periods or ticker intervals) but the sweeper has not yet transitioned the attempt to `LOST`, renewal is accepted and extended from current database time (`clock_timestamp() + make_interval(secs => lease_duration)`), symmetric with commit. HTTP 409 Conflict (`LEASE_EXPIRED` / `STALE_ATTEMPT_REJECTED`) is returned only once the attempt has already been swept to `LOST`.
- **Response (200 OK)**:
  ```json
  {
    "status": "RENEWED",
    "lease_expires_at": "2026-10-26T20:15:30Z",
    "cancel_requested": false
  }
  ```
- **Single Cancellation Channel**: The renewal response is the single channel for delivering cancellation requests to workers. When `job.state == 'CANCEL_REQUESTED'`, the renewal succeeds (`200 OK`) and returns `"cancel_requested": true`. The worker agent inspects this flag and triggers cooperative task cancellation via `context.CancelFunc`. Worst-case delivery latency is bounded by exactly one renewal interval (10 seconds default). (Accepted by owner).
- **Lease-Lost Handling**: If renewal returns HTTP 409 Conflict (`LEASE_EXPIRED` / `STALE_ATTEMPT_REJECTED`) or 403 Forbidden (`WORKER_MISMATCH`), meaning the attempt was swept to `LOST` by the sweeper, the worker must immediately abort task execution via context cancellation and **must not report a result** to the coordinator. (Accepted by owner).

### 4. Deadlock-Free Coordinator Lease Sweeper, Re-Check Under Lock, and Grace Periods

The coordinator runs a background lease expiry sweeper (`StartLeaseSweeper`) analogous to the worker reaper loop.

#### Locking Hierarchy
To prevent deadlocks with concurrent `DispatchNextJob`, `CommitAttempt`, `CancelJob`, and `RenewAttemptLease`, the sweeper strictly maintains the canonical lock order defined in ADR-0013:
$$\text{jobs} \longrightarrow \text{workers} \longrightarrow \text{attempts} \longrightarrow \text{results} \longrightarrow \text{job\_events}$$

#### Sweeper Algorithm & Re-Check Under Lock
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
   - **RE-CHECK UNDER LOCK (Critical Invariant)**:
     A concurrent worker renewal (`RenewAttemptLease`) or completion commit (`CommitAttempt`) may have executed between the unlocked candidate scan and the row lock acquisition. Under the row lock, the sweeper evaluates:
     ```sql
     -- Verified under exclusive attempt row lock:
     SELECT state, lease_expires_at FROM attempts WHERE id = $1 FOR UPDATE;
     ```
     If `attempts.state != 'RUNNING'` OR `attempts.lease_expires_at >= clock_timestamp()`, the attempt was concurrently committed or renewed. The sweeper rolls back or skips the candidate cleanly with zero state mutation. (Accepted by owner).
     - Planned test: `TestPostgresRepository_Sweeper_ConcurrentRenewalRecheck` (planned test).
   - **Transition Attempt**:
     - Update attempt state to `LOST` (or `TIMED_OUT` if cumulative execution exceeded `job.timeout_seconds`).
     - Set `finished_at = clock_timestamp()`, `error = 'lease expired: worker failed to renew'`.
   - **Transition Job**:
     - If `job.state == 'CANCEL_REQUESTED'`, transition job to `CANCELLED`.
     - Else if `attempt_no < job.max_attempts`, transition job to `RETRYING`, compute exponential backoff with full jitter, set `jobs.not_before = clock_timestamp() + backoff`, and allow redelivery.
     - Else (`attempt_no >= job.max_attempts`), transition job to `FAILED` with error `"max attempts exhausted"`.
   - Record `job_events` audit record (`event_type: 'lease_expired'`).

#### Restart and Outage Grace Design
- **The Problem**: Leases expire on database time (`clock_timestamp()`). If a coordinator restarts or the database experiences an outage longer than the lease duration (30s), database time advances. Upon recovery, healthy workers whose tasks were executing normally would find their attempts expired. The sweeper would immediately reap them to `LOST`, requeue the jobs, and cause duplicate execution.
- **Conflict with Phase 3 Guarantees**: This would break the Phase 3 guarantee that a surviving worker's commit after a coordinator restart is accepted with HTTP 200 COMMITTED, pinned by:
  - `TestPostgresWiring_CoordinatorRestartRecovery` (`tests/integration/postgres_wiring_test.go:167`)
  - `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:794`)
- **Grace Mechanism Options Evaluated**:
  1. *Option A (Startup Grace Period)*: Sweeper delays reaping after coordinator boot.
  2. *Option B (Outage Grace Window)*: Sweeper suppresses reaping following storage reconnection.
  3. *Option C (Dynamic Lease Extension in DB)*: Shifting `lease_expires_at` forward by measured downtime.
- **Accepted Recommendation**: A dual grace period mechanism:
  1. **Coordinator Startup Grace Period (`MINICLOUD_STARTUP_GRACE_PERIOD`, default 30s)**: On coordinator boot or restart, the sweeper suppresses reaping passes until 30 seconds have elapsed. This gives surviving workers a full lease window to reconnect, auto-re-register, and execute at least 3 renewal attempts (renewal interval 10s) or commit completed results before any attempt can be reaped. (Accepted by owner).
  2. **Database Outage Grace Window (`MINICLOUD_OUTAGE_GRACE_WINDOW`, default 30s)**: When the coordinator detects that the database has recovered from an outage, the sweeper suppresses reaping passes for 30 seconds. (Accepted by owner).
- **Outage Grace Detection Mechanism**:
  Because the sweeper and coordinator can only observe their own interactions with PostgreSQL, an outage condition is detected autonomously when either:
  1. **Consecutive Failed Ticks**: $N \ge 2$ consecutive database operations (sweeper ticks or `/readyz` health pings) fail with database connectivity errors (`net.OpError`, connection refused, pool closed, context deadline exceeded).
  2. **Inter-Tick Wall-Clock Gap**: The wall-clock elapsed time since the last successful database round trip exceeds a configured gap: $\Delta t > 1.5 \times \text{sweeper\_interval}$ (or $> 10\text{s}$). If host sleeping, VM suspension, or container freeze pauses the process, the clock advances without successful database round trips.
  Upon the **first successful database round trip** following an outage condition, the coordinator activates the outage grace window:
  $$\text{outage\_grace\_until} = \text{clock\_timestamp}() + \text{MINICLOUD\_OUTAGE\_GRACE\_WINDOW}$$
  While `clock_timestamp() < outage_grace_until`, sweeper reaping passes are completely suppressed.
- **Configuration Names & Validation Rules**:
  - `MINICLOUD_STARTUP_GRACE_PERIOD`: Time to suppress sweeper reaping after coordinator boot (default: `30s`).
  - `MINICLOUD_OUTAGE_GRACE_WINDOW`: Time to suppress sweeper reaping after database recovery (default: `30s`).
  - **Validation Rule**: Both `MINICLOUD_STARTUP_GRACE_PERIOD` and `MINICLOUD_OUTAGE_GRACE_WINDOW` MUST be greater than or equal to the attempt lease duration (`MINICLOUD_LEASE_DURATION`, default `30s`). If either grace window is configured strictly less than the lease duration (e.g., `10s < 30s`), config validation rejects the startup with `ErrInvalidGraceConfiguration`, because a grace period shorter than the lease duration cannot protect surviving workers whose renewal interval is 10s and lease is 30s.
- **Interaction with Phase 3 Tests**:
  - In `TestPostgresWiring_CoordinatorRestartRecovery`, Coordinator 1 is killed and Coordinator 2 starts up while a worker executes an 800ms task. With startup grace (30s), Coordinator 2's sweeper does not reap the attempt; the surviving worker commits with HTTP 200 COMMITTED, exactly preserving the Phase 3 contract.
  - In `TestSecurity_PartD_Outage_MidDispatchAndMidCommit`, the database container is stopped for 1.5s. Outage grace protects the attempt while the worker retries its commit upon database restoration.
- **Worker-Side Result Delivery Pairing During Outages**:
  Coordinator-side grace periods must be paired with worker-side delivery resilience. If a task completes execution during a coordinator or database outage, the worker's result delivery loop (`internal/worker/client.go:324-355`) retries `ReportResult` with capped jittered backoff ($500\text{ms}$ base to $5.0\text{s}$ cap) and concurrently continues issuing lease renewals every 10s until the coordinator recovers. This ensures that in scenarios with extended coordinator downtime (e.g. 45s), work that finished mid-downtime is not discarded by the worker before the coordinator and its startup grace window become available.
- **Impact on Existing Tests & Owner Approval Candidates**:
  - Zero existing test assertions are weakened. Existing integration tests run with restarts/outages of 200ms–1.5s, well within the 30s grace window.
  - Candidates for Owner Approval: If future tests explicitly configure short micro-leases (e.g. 500ms lease) to test fast expiry, they must explicitly configure `MINICLOUD_STARTUP_GRACE_PERIOD=0s` or wait for the grace window to elapse. Expanding `executeAndReport` from 5 attempts to a 60-second time-bounded loop with capped jittered backoff and ongoing lease renewal is an additive durability improvement submitted for owner approval.
- **Planned Grace Verification Tests**:
  - `TestPostgresRepository_Sweeper_OutageGraceDetection_SuppressesReap` (planned test).
  - `TestPostgresRepository_Sweeper_StartupGrace_SuppressesReap` (planned test).
  - `TestPostgresRepository_DatabaseStopStart_WorkerRenewsSuccessfully` (planned test).
  - `TestConfig_Validation_GracePeriodLessThanLeaseDuration_Rejected` (planned test).

### 5. Stale Attempt Rejection & Symmetric Accept-Until-Swept Rule

#### Renewal and Commit Before Sweeper Runs: Option Analysis & Decision

| Dimension | Option 1: Strict Lease Rejection at Clock Time | Option 2: Symmetric Accept-Until-Swept (Accepted) |
|---|---|---|
| **Rule** | `CommitAttempt` and `RenewAttemptLease` reject if `clock_timestamp() > lease_expires_at`, even if `attempts.state == 'RUNNING'`. | `CommitAttempt` and `RenewAttemptLease` succeed if `attempts.state == 'RUNNING'`, even if `clock_timestamp() > lease_expires_at`, provided the sweeper has not swept the attempt to `LOST`. |
| **Costs** | Discards valid computational work completed by a slightly slow or delayed worker, and aborts surviving workers renewing during coordinator restart or outage grace periods. | A slow or lagging worker that is still alive retains its attempt until the sweeper executes its reaping pass, delaying reclamation of a stalled worker until the sweeper runs. |
| **Effect on Retries** | Sweeper subsequently reaps attempt to `LOST` and triggers an automated retry. | Avoids redundant retry; job completes directly to `SUCCEEDED` or renewal extends lease from database time. |
| **Duplicate Execution** | **Increases duplicate execution**: another worker executes the exact same job even though the first worker is alive and actively computing or renewing. | **Minimizes duplicate execution**: avoids re-running already active or completed tasks when no replacement worker has been dispatched. |

**Decision**: MiniCloud adopts **Option 2 (Symmetric Accept-Until-Swept while `RUNNING`)** across both `CommitAttempt` and `RenewAttemptLease` (Accepted by owner).
- **Symmetric Rule**: A lease represents exclusive write and execution rights until revoked by the coordinator sweeper. While `attempts.state == 'RUNNING'`:
  - `CommitAttempt` accepts the result and transitions the job to `SUCCEEDED` (or `FAILED` / `RETRYING`).
  - `RenewAttemptLease` accepts the renewal and extends `lease_expires_at` to `clock_timestamp() + make_interval(secs => lease_duration)`.
  Neither commit nor renewal is rejected merely because `lease_expires_at` has passed on the clock while the sweeper was paused for grace or between ticks.
- **Cost of Accept-Until-Swept**: A slow, delayed, or temporarily paused worker that remains alive keeps its attempt even if `lease_expires_at` has passed, until the sweeper runs its next pass. This slightly delays reclaiming stalled workers, but prevents catastrophic cascading aborts during coordinator restarts and database outages, preserving in-progress computation and upholding Phase 3 surviving-worker guarantees.
- **Code-Level Error Definition**:
  ```go
  // ErrLeaseExpired is returned when a renewal or commit is attempted on an attempt that was already swept to LOST.
  var ErrLeaseExpired = errors.New("attempt lease expired and was marked lost")
  ```
- **Hard Boundary (No Resurrection Once Swept)**: Once the sweeper acquires row locks and transitions the attempt to `LOST` (or `TIMED_OUT`), Option 2 ceases. Any subsequent commit or renewal attempt is strictly rejected with HTTP 409 Conflict (`LEASE_EXPIRED` / `STALE_ATTEMPT_REJECTED`) and `domain.ErrLeaseExpired`. The late result is recorded strictly as a `diagnostic_late_result` in `job_events` and is **never** committed to `results`. Expired attempts are never resurrected. (Accepted by owner).
- **Planned Verification Tests**:
  - `TestPostgresRepository_RenewAttemptLease_ExpiredButUnswept_RenewedFromDBTime` (planned test).
  - `TestPostgresRepository_RenewAttemptLease_AfterSweptToLost_RejectedLeaseExpired` (planned test).
  - `TestPostgresRepository_CommitAttempt_ExpiredButUnswept_Accepted` (planned test).
  - `TestPostgresRepository_CommitAttempt_AfterSweptToLost_RejectedLeaseExpired` (planned test).

---

## Consequences

### Positive
- **Guaranteed Reclamation**: Dead or partitioned workers no longer strand jobs in `RUNNING`.
- **Mathematical Deadlock Freedom**: Adherence to the strict canonical locking order (`jobs -> workers -> attempts -> results -> job_events`) prevents database deadlocks.
- **Clock Drift Immunity**: Relies 100% on database time (`clock_timestamp()`).
- **Audit Traceability**: Late results are preserved as diagnostics without corrupting system truth.
- **Restart & Outage Resilience**: Startup and outage grace periods prevent mass expiration of healthy jobs across coordinator restarts and database blips.
- **Minimized Duplication**: Accepting unreaped commits while in `RUNNING` avoids duplicate execution while strictly barring resurrection once reaped.

### Negative / Trade-offs
- Active workers must maintain a background renewal goroutine while executing tasks.
- Network partitions between worker and coordinator cause lease expiration if the partition exceeds the 30-second lease window plus grace.

### Invariants Protected
- Invariant 2: Central State Validation
- Invariant 3: Stale Attempt Rejection (no resurrection of expired attempts)
- Invariant 6: Lease Expiry Meaning (coordinator loss of trust, not worker death)
