# MiniCloud Failure Model & Fault Matrix

## 1. Failure Handling Principles
MiniCloud is engineered around the reality that distributed systems fail partially and asynchronously:
- **Crash-Recovery Model:** Any component (coordinator, worker, database, transport) may crash at any time and restart with its durable state intact.
- **Lease Expiry vs. Process Death:** A worker that stops responding cannot be assumed dead. It may be partitioned, GC-paused, or slow. MiniCloud uses lease tokens and database timestamps to prevent zombie workers from committing stale outcomes.
- **At-Least-Once Delivery & Deduplication:** Under failure and retry, duplicate executions may occur. All operations on the result commit boundary must be idempotent.
- **Phase 4 Lease Recovery & Grace Period Guarantees:** In Phase 4 (ADR-0014, ADR-0015, ADR-0016), dead worker reclamation is fully solved. When a worker process dies abruptly or loses connectivity while executing a `RUNNING` job, its attempt lease expires on database time (`lease_expires_at`). To prevent coordinator restarts or database outages from mass-expiring healthy attempts and violating Phase 3 surviving-worker commit guarantees (`TestPostgresWiring_CoordinatorRestartRecovery` in `tests/integration/postgres_wiring_test.go:167` and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` in `tests/integration/part_d_security_test.go:794`), MiniCloud implements:
  1. **Coordinator Startup Grace Period (`MINICLOUD_STARTUP_GRACE_PERIOD`, 30s)**: Suppresses sweeper reaping for 30s after boot, giving surviving workers time to renew or commit.
  2. **Database Outage Grace Window (`MINICLOUD_OUTAGE_GRACE_WINDOW`, 30s)**: The sweeper opens an outage grace window of MINICLOUD_OUTAGE_GRACE_WINDOW (30 s) at the first successful tick after either (a) any failed sweeper tick, or (b) a database-time gap over 3x the sweep interval between consecutive successful ticks. A lock timeout or SKIP LOCKED skip on a single candidate is not a failed tick. After a window ends, a cooldown of the same length (30 s) begins, during which no new window opens and reaping proceeds, so suppression of reaping never exceeds one window length. The only authoritative grace check is the one inside the sweeper transaction lock.
  3. **Symmetric Accept-Until-Swept**: While `attempts.state == 'RUNNING'`, completed work is accepted and renewals are accepted even if `lease_expires_at` has passed on database time, extending the lease from current database time (except on a `CANCEL_REQUESTED` job, where the renewal returns the current expiry unchanged without extending the lease) and minimizing duplicate execution. Once swept to `LOST`, late commits or renewals are strictly rejected (HTTP 409 `LEASE_EXPIRED` / `domain.ErrLeaseExpired`) and late results are recorded as `DIAGNOSTIC_LATE_RESULT` with **zero resurrection**.
- **Bounded Retries & Budget Consumption:** Every attempt marked `LOST` by the sweeper consumes 1 attempt of the job's `max_attempts` budget (default 3), bounding cluster execution during extended fleet outages.
- **Single Cancellation Delivery Channel:** Cooperative cancellation is delivered exclusively via the lease renewal response (`cancel_requested: true`) with worst-case delivery latency bounded by one renewal interval (10s).
- **Server Timeouts & Background Deadlines:** HTTP API requests enforce an 8s `RequestTimeout` (`requestDeadlineMiddleware` in `internal/api/handlers.go`). Requests that block on database queries or locks for >8s abort and return HTTP 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1`. (Documented accepted limit F-4e-3 / SEC-033: after a half-open database partition heals, pgx connection recovery may take up to ~15s due to internal `asyncClose` cleanup). Background operations enforce an 8s `BackgroundCallTimeout` per iteration on sweeper ticks and worker reaper cycles (`internal/coordinator/service.go`), preventing hung database connections from blocking background maintenance indefinitely.
- **Defensive Text Clamping (`ClampText`):** Task outputs and error strings are clamped defensively (`internal/executor/truncate.go`). Any raw NUL (`\x00`) bytes and invalid UTF-8 byte sequences are replaced with `\uFFFD` (Unicode Replacement Character, 3 bytes in UTF-8). String length is measured after byte replacement; if it exceeds the limit (64 KiB for output, 10 KiB for error text), it is cut backward to the nearest valid UTF-8 rune boundary and suffixed with `\n[truncated]` so the final byte length does not exceed the limit.
- **Work-Poll Database Cost:** A work poll dispatch costs approximately 20 client-to-database network round trips (flights) when claiming a job, an empty poll costs 4 round trips, and a job submission costs 5 round trips. On networks with latency >400ms per round trip, polling operations approach the 8s request deadline.

---

## 2. Fault Scenarios & Expected Behavior

| ID | Fault Scenario | Expected Behavior | Phase Introduced | Scope in Phase 4 |
|---|---|---|:---:|---|
| **FS-01** | **Worker crashes before accepting job** | Job remains in `QUEUED` state. Another eligible worker claims it upon polling via `repo.DispatchNextJob`. | Phase 1 | Handled via transactional skip-locked dispatch. |
| **FS-02** | **Worker crashes after starting execution** | Lease expires without renewal (`clock_timestamp() > lease_expires_at`). Coordinator sweeper marks attempt `LOST` (consuming 1 attempt), checks `attempt_no < max_attempts`, and transitions job to `RETRYING` with backoff `not_before`. | Phase 4 | **Core Phase 4 deliverable:** Autonomous lease sweeper and retry backoff. |
| **FS-03** | **Worker finishes but result is lost/delayed** | Worker retries `ReportResult`. If commit arrives while attempt is still `RUNNING`, it is accepted (symmetric accept-until-swept). If sweeper already swept attempt to `LOST`, commit is rejected with HTTP 409 `LEASE_EXPIRED` (or `JOB_ALREADY_TERMINAL` if parent job is terminal) and recorded as `DIAGNOSTIC_LATE_RESULT` (no resurrection). | Phase 4 | **Core Phase 4 deliverable:** Rejection of expired attempt commits; diagnostic auditing. |
| **FS-04** | **Old worker returns result after lease expired & new attempt started** | Coordinator validates `attempt_id`, `lease_token_hash`, and `state`. Since attempt was transitioned to `LOST` and superseded, commit is rejected with HTTP 409 `LEASE_EXPIRED` (`domain.ErrLeaseExpired`) and recorded as `DIAGNOSTIC_LATE_RESULT`. | Phase 4 | **Core Phase 4 deliverable:** Constant-time hash check and attempt state validation. |
| **FS-05** | **Coordinator crashes after submission or during execution** | Upon coordinator restart, persistent storage is read. Unassigned `QUEUED` jobs remain durable. For `RUNNING` jobs, sweeper startup grace period (30s) allows surviving workers to reconnect and commit (preserving Phase 3 guarantee in `TestPostgresWiring_CoordinatorRestartRecovery`). | Phase 3 / Phase 4 | Phase 3 durable storage + Phase 4 startup grace. |
| **FS-06** | **Coordinator crashes while committing result / ambiguous commit retry** | Single database transaction provides atomicity across `attempts`, `jobs`, and `results`. If uncommitted, worker retries `ReportResult`; if committed, worker receives 409 `JOB_ALREADY_TERMINAL` on retry; single result row from initial commit stands; worker treats 409 as final and retry is idempotent (SEC-031, Informational). | Phase 3 | Handled in Phase 3 atomic repository methods. |
| **FS-07** | **Dispatch transport (Redis) unavailable** | Coordinator retains persistent job state in database. Jobs are requeued upon transport reconnection. | V2 (Transport Evaluation) | Out of Scope for V1. |
| **FS-08** | **Database temporarily unavailable** | Coordinator returns HTTP 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1`. Worker client applies exponential backoff with jitter. Upon database restoration, the sweeper opens an outage grace window of MINICLOUD_OUTAGE_GRACE_WINDOW (30 s) at the first successful tick after either (a) any failed sweeper tick, or (b) a database-time gap over 3x the sweep interval between consecutive successful ticks. A lock timeout or SKIP LOCKED skip on a single candidate is not a failed tick. After a window ends, a cooldown of the same length (30 s) begins, during which no new window opens and reaping proceeds, so suppression of reaping never exceeds one window length. The only authoritative grace check is the one inside the sweeper transaction lock. | Phase 3 / Phase 4 | Phase 3 outage resilience + Phase 4 outage grace. |
| **FS-09** | **Worker heartbeats delayed or process dies abruptly (SIGKILL/crash)** | Inactive workers exceeding `worker_timeout` are reaped to `UNHEALTHY`. Running attempts held by that worker expire when `lease_expires_at` lapses, are reaped to `LOST` by the sweeper, and scheduled for retry. | Phase 2 / Phase 4 | Phase 2 reaps worker; Phase 4 sweeps and reclaims in-flight attempts. |
| **FS-10** | **Task times out, emits excessive output, or crashes** | Executor aborts task via `context.WithTimeout` with duration clamped by worker to `[1, 86400]` s (`MaxExecutionTimeoutSeconds = coordinator.MaxTimeoutSeconds`, SEC-012), clamps output to 64 KiB and error text to 10 KiB via `ClampText` (replacing NUL/invalid UTF-8 with `\uFFFD` and cutting at rune boundaries with `\n[truncated]`), and reports structured failure with `exit_code` and `error_classification`. Input validation failures are classified `PERMANENT`. If coordinator rejects result report with HTTP 400 `INVALID_REQUEST` or `OUTPUT_TOO_LARGE`, worker sends exactly one fallback failure report (exit 1, empty output, `PERMANENT`, error summary citing the 400 rejection) with active renewals, moving the job to `FAILED` on attempt 1. `MALFORMED_JSON` is deliberately excluded from fallback to allow clean at-least-once retry upon transport corruption. | Phase 1 / Phase 4 | Task-level timeout enforced; Phase 4 classifies retryability and fallback. |
| **FS-11** | **Cancellation races with job completion or duplicate cancel** | Coordinator serializes under row lock. If result commits first, job is `SUCCEEDED`; if cancel commits first, job is `CANCEL_REQUESTED`. Renewal response carries `cancel_requested: true` and unextended expiry; worker cancels task context, ceases renewals, and reports result. If task ignores cancellation, lease expires within 30s and sweeper reaps to `CANCELLED`. Any commit on `CANCEL_REQUESTED` closes attempt with reported outcome (success moves `CANCEL_REQUESTED -> SUCCEEDED` per today's code; failure moves `CANCEL_REQUESTED -> CANCELLED`, cancel wins, never `RETRYING`). Duplicate cancel on `CANCEL_REQUESTED` is an idempotent repository no-op (HTTP 200). | Phase 4 | **Core Phase 4 deliverable:** Cooperative cancel context and race resolution (ADR-0016). |
| **FS-12** | **Unauthorized worker reports result for an attempt** | Coordinator checks `attempt.WorkerID == reportingWorkerID`. Mismatches rejected with HTTP 403 `WORKER_MISMATCH`. Worker client treats 403 as non-retryable. | Phase 2 | Enforced centrally at API and storage boundaries. |
| **FS-13** | **Worker marked UNHEALTHY completes still-current attempt** | Result accepted if `attempt_id`, `lease_token`, and `worker_id` match and attempt is still `RUNNING` in database (symmetric accept-until-swept). Coordinator status marks telemetry trust, not task validity. | Phase 2 | Preserved in Phase 4. |

---

## 3. Failure Test Matrix Template

When writing automated fault injection tests, every test case must explicitly document:
1. **Target Fault Scenario:** Reference scenario ID (e.g., `FS-02`).
2. **Injection Mechanism:** Process kill (`SIGKILL`), network delay, simulated clock skew, dropped response.
3. **Expected Invariant:** State transition verification, stale rejection, or bounded retry.
4. **Evidence Collected:** Logs with `job_id` and `attempt_id`, status codes, final job state in repository.
5. **Recovery Mode:** Autonomous (system self-heals) vs. Operator-required (manual intervention).

---

## 4. Worker-Side Result Delivery Protocol During Extended Outages

### The Delivery Protocol Under Extended Downtime
In `internal/worker/client.go` function `deliverResult` (called from `ExecuteAndReport`), the worker client retries `ReportResult` with bounded exponential backoff and jitter under a fixed 60-second delivery budget (`ResultDeliveryBudget = 60 * time.Second`). If an extended coordinator restart or database outage occurs (e.g., lasting up to 45 seconds), any task that completes during the outage continues to retry result reporting and concurrently renews its lease until the coordinator recovers. Under the coordinator's startup grace period (`MINICLOUD_STARTUP_GRACE_PERIOD`, 30s) and the symmetric accept-until-swept rule, the completed result is delivered and accepted without lost work or redundant dispatch.

### Implemented Worker Delivery Behavior
The worker client enforces the following result delivery protocol in `internal/worker/client.go` function `deliverResult`:
1. **Capped, Jittered Backoff Loop**:
   When reporting a finished result, transient errors trigger retries using `ComputeBackoffWithJitter` with base `deliveryBackoffBase = 200 * time.Millisecond` and nominal cap `deliveryBackoffMax = 5 * time.Second`. Because the multiplier sequence `1 << (failureCount - 1)` is capped at 16 (`200ms * 16 = 3.2s`), the real ceiling before jitter is 3.2s (the nominal 5.0s cap is never reached). The jitter factor is in `[0.5, 1.0]`.
2. **Concurrent Ongoing Lease Renewal**:
   While the worker's result delivery loop (`deliverResult`) retries, the worker's lease renewal mechanism (`renewLoop`) continues active lease renewal (`POST /v1/jobs/{id}/attempts/{attempt_id}/renew`) at its periodic interval (default 10s). Under the symmetric accept-until-swept rule (ADR-0014), renewals and commits are accepted while `attempts.state == 'RUNNING'`. Once the coordinator recovers, renewal extends `lease_expires_at` from database time (except on a `CANCEL_REQUESTED` job, where renewal returns the current unextended expiry and halts renewals), ensuring the attempt is not reaped while the worker is actively delivering its completed result.
3. **Terminal Rejection Halts Delivery**:
   On the result reporting path (`ReportResult`), deterministic non-retryable rejections (`isTerminalDelivery`) immediately abort the delivery loop without further retries:
   - HTTP 400 Bad Request (`INVALID_REQUEST` / payload limit or schema violation)
   - HTTP 403 Forbidden (`WORKER_MISMATCH`)
   - HTTP 404 Not Found (recognized envelope `JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`, or legacy un-enveloped 404)
   - HTTP 409 Conflict (`JOB_ALREADY_TERMINAL`, `LEASE_EXPIRED`, `STALE_ATTEMPT_REJECTED`)
   On the lease renewal path (`RenewAttemptLease`):
   - Terminal 409 (`JOB_ALREADY_TERMINAL`, `LEASE_EXPIRED`, `STALE_ATTEMPT_REJECTED`), 403 (`WORKER_MISMATCH`), and recognized 404 (`JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`) halt renewals immediately and mark the attempt lost, aborting delivery.
   - HTTP 400 (`INVALID_REQUEST`, `WORKER_TASK_UNSUPPORTED`) or legacy un-enveloped 404 causes the worker to cease renewals while letting the task continue running to completion, reporting the final outcome via `ReportResult`.
   - Transient errors (5xx, connection drop) retry with jittered backoff starting from 1 s, capped at the renewal interval (10s, func `renewLoop` in `internal/worker/client.go`), until lease expiry.
4. **Worker Fallback Failure Report on HTTP 400 Validation Rejection**:
   When the coordinator rejects a result report with HTTP 400 and error code `INVALID_REQUEST` or `OUTPUT_TOO_LARGE`:
   - The worker sends exactly **one fallback failure report** for the same attempt and lease: exit code 1, empty output, error class `PERMANENT`, and error summary `result rejected by coordinator: HTTP 400 <code>`.
   - The worker's renewal goroutine continues renewing the lease until fallback delivery completes.
   - If accepted, the job transitions to `FAILED` immediately on attempt 1, recording a single result row and preventing a poison loop of repeated lease expiries.
   - If the fallback report itself receives an HTTP 400 or other terminal rejection, the worker halts delivery and exits with status 1.
   - *Exclusion of `MALFORMED_JSON`*: When the coordinator answers 400 `MALFORMED_JSON`, no fallback report is sent. Because the worker always marshals valid JSON via `json.Marshal`, a malformed JSON response indicates transport-level corruption. The worker drops the result, the lease expires and is reaped to `LOST`, and the task re-runs safely (at-least-once).
5. **Explicit Delivery Bound**:
   The reporting retry loop in `deliverResult` is bounded by a fixed cumulative timer of `ResultDeliveryBudget = 60 * time.Second`, or terminates early if renewal receives a terminal rejection or the attempt is declared lost.

### Existing Test Impact Analysis
- **Tests Pinning Delivery Behavior**:
   - `internal/worker/client_test.go` (`TestIsNonRetryableReportError`): Pins classification of HTTP 403, 404, 409 as terminal/non-retryable.
   - `internal/worker/client_test.go` (`TestAgent_ReportResult_RejectionHandling`): Pins single-invocation status code rejection handling.
   - `internal/worker/client.go` function `deliverResult`: Implements the 60-second time-bounded delivery loop with capped jittered backoff.
- **Assertion Impact**:
   The 60-second time-bounded loop (`deliverResult`) and classifying deterministic HTTP 400 as non-retryable in `isTerminalDelivery` provides delivery durability during coordinator restarts and database outages up to 45 seconds without weakening any existing test assertion.

### Planned Verification Tests
- `TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success` (planned test).
- `TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport` (planned test).
- `TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace` (planned test).
- `TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately` (planned test).
