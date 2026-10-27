# MiniCloud Failure Model & Fault Matrix

## 1. Failure Handling Principles
MiniCloud is engineered around the reality that distributed systems fail partially and asynchronously:
- **Crash-Recovery Model:** Any component (coordinator, worker, database, transport) may crash at any time and restart with its durable state intact.
- **Lease Expiry vs. Process Death:** A worker that stops responding cannot be assumed dead. It may be partitioned, GC-paused, or slow. MiniCloud uses lease tokens and database timestamps to prevent zombie workers from committing stale outcomes.
- **At-Least-Once Delivery & Deduplication:** Under failure and retry, duplicate executions may occur. All operations on the result commit boundary must be idempotent.
- **Phase 4 Lease Recovery & Grace Period Guarantees:** In Phase 4 (ADR-0014, ADR-0015, ADR-0016), dead worker reclamation is fully solved. When a worker process dies abruptly or loses connectivity while executing a `RUNNING` job, its attempt lease expires on database time (`lease_expires_at`). To prevent coordinator restarts or database outages from mass-expiring healthy attempts and violating Phase 3 surviving-worker commit guarantees (`TestPostgresWiring_CoordinatorRestartRecovery` in `tests/integration/postgres_wiring_test.go:167` and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` in `tests/integration/part_d_security_test.go:794`), MiniCloud implements:
  1. **Coordinator Startup Grace Period (`MINICLOUD_STARTUP_GRACE_PERIOD`, 30s)**: Suppresses sweeper reaping for 30s after boot, giving surviving workers time to renew or commit.
  2. **Database Outage Grace Window (`MINICLOUD_OUTAGE_GRACE_WINDOW`, 30s)**: Suppresses reaping for 30s after database reconnection.
  3. **Symmetric Accept-Until-Swept**: While `attempts.state == 'RUNNING'`, completed work is accepted and renewals are accepted even if `lease_expires_at` has passed on database time, extending the lease from current database time and minimizing duplicate execution. Once swept to `LOST`, late commits or renewals are strictly rejected (HTTP 409 `LEASE_EXPIRED` / `domain.ErrLeaseExpired`) and late results are recorded as `diagnostic_late_result` with **zero resurrection**.
- **Bounded Retries & Budget Consumption:** Every attempt marked `LOST` by the sweeper consumes 1 attempt of the job's `max_attempts` budget (default 3), bounding cluster execution during extended fleet outages.
- **Single Cancellation Delivery Channel:** Cooperative cancellation is delivered exclusively via the lease renewal response (`cancel_requested: true`) with worst-case delivery latency bounded by one renewal interval (10s).

---

## 2. Fault Scenarios & Expected Behavior

| ID | Fault Scenario | Expected Behavior | Phase Introduced | Scope in Phase 4 |
|---|---|---|:---:|---|
| **FS-01** | **Worker crashes before accepting job** | Job remains in `QUEUED` state. Another eligible worker claims it upon polling via `repo.DispatchNextJob`. | Phase 1 | Handled via transactional skip-locked dispatch. |
| **FS-02** | **Worker crashes after starting execution** | Lease expires without renewal (`clock_timestamp() > lease_expires_at`). Coordinator sweeper marks attempt `LOST` (consuming 1 attempt), checks `attempt_no < max_attempts`, and transitions job to `RETRYING` with backoff `not_before`. | Phase 4 | **Core Phase 4 deliverable:** Autonomous lease sweeper and retry backoff. |
| **FS-03** | **Worker finishes but result is lost/delayed** | Worker retries `ReportResult`. If commit arrives while attempt is still `RUNNING`, it is accepted (symmetric accept-until-swept). If sweeper already swept attempt to `LOST`, commit is rejected with HTTP 409 `LEASE_EXPIRED` and recorded as `diagnostic_late_result` (no resurrection). | Phase 4 | **Core Phase 4 deliverable:** Rejection of expired attempt commits; diagnostic auditing. |
| **FS-04** | **Old worker returns result after lease expired & new attempt started** | Coordinator validates `attempt_id`, `lease_token_hash`, and `state`. Since attempt was transitioned to `LOST` and superseded, commit is rejected (`ErrStaleAttempt`, HTTP 409 `LEASE_EXPIRED`). | Phase 4 | **Core Phase 4 deliverable:** Constant-time hash check and attempt state validation. |
| **FS-05** | **Coordinator crashes after submission or during execution** | Upon coordinator restart, persistent storage is read. Unassigned `QUEUED` jobs remain durable. For `RUNNING` jobs, sweeper startup grace period (30s) allows surviving workers to reconnect and commit (preserving Phase 3 guarantee in `TestPostgresWiring_CoordinatorRestartRecovery`). | Phase 3 / Phase 4 | Phase 3 durable storage + Phase 4 startup grace. |
| **FS-06** | **Coordinator crashes while committing result** | Single database transaction provides atomicity across `attempts`, `jobs`, and `results`. If uncommitted, worker retries `ReportResult`; if committed, worker receives 200 COMMITTED on retry. | Phase 3 | Handled in Phase 3 atomic repository methods. |
| **FS-07** | **Dispatch transport (Redis) unavailable** | Coordinator retains persistent job state in database. Jobs are requeued upon transport reconnection. | Phase 5 | Out of Scope for V1. |
| **FS-08** | **Database temporarily unavailable** | Coordinator returns HTTP 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1`. Worker client applies exponential backoff with jitter. Upon database restoration, outage grace window (30s) prevents mass-reaping of in-flight attempts, allowing workers to commit cleanly (`TestSecurity_PartD_Outage_MidDispatchAndMidCommit`). | Phase 3 / Phase 4 | Phase 3 outage resilience + Phase 4 outage grace. |
| **FS-09** | **Worker heartbeats delayed or process dies abruptly (SIGKILL/crash)** | Inactive workers exceeding `worker_timeout` are reaped to `UNHEALTHY`. Running attempts held by that worker expire when `lease_expires_at` lapses, are reaped to `LOST` by the sweeper, and scheduled for retry. | Phase 2 / Phase 4 | Phase 2 reaps worker; Phase 4 sweeps and reclaims in-flight attempts. |
| **FS-10** | **Task times out, emits excessive output, or crashes** | Executor aborts task via `context.WithTimeout`, truncates output to 64 KiB, and reports structured failure with `exit_code` and `error_classification`. Input validation failures are classified `PERMANENT`. | Phase 1 / Phase 4 | Task-level timeout enforced; Phase 4 classifies retryability. |
| **FS-11** | **Cancellation races with job completion or duplicate cancel** | Coordinator serializes under row lock. If result commits first, job is `SUCCEEDED`; if cancel commits first, job is `CANCEL_REQUESTED`, delivered via renewal response within 10s, and confirmed to `CANCELLED`. Duplicate cancel on `CANCEL_REQUESTED` is an idempotent no-op (HTTP 202). | Phase 4 | **Core Phase 4 deliverable:** Cooperative cancel context and race resolution (ADR-0016). |
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

### The Delivery Gap Under Extended Downtime
Inspection of the current worker implementation in `internal/worker/client.go:324-355` (`executeAndReport`) reveals that `ReportResult` retries at most 5 times with exponential backoff ($100\text{ms} \times 2^{\text{attempt}}$), yielding approximately 3.1 seconds of total retry duration ($100\text{ms} + 200\text{ms} + 400\text{ms} + 800\text{ms} + 1600\text{ms} = 3.1\text{s}$). If a coordinator restart or database outage lasts longer than 3.1 seconds (e.g., a 15–45 second coordinator crash), any task that completes *during* the outage discards its completed result at the worker side. Even if the coordinator restarts and enables its 30-second startup grace period, no result is delivered, resulting in lost work and redundant retry dispatch.

### Designed Worker Delivery Behavior
To guarantee end-to-end result preservation during coordinator or database outages, Phase 4 specifies the following worker client behavior for finished but unreported results:
1. **Capped, Jittered Backoff Loop**:
   When reporting a finished result, transient errors (HTTP 500, 502, 503, 504, connection reset, connection refused, context deadline) trigger retries using exponential backoff with full jitter:
   $$\text{backoff} = \min\left(\text{max\_report\_backoff}, \text{base\_report\_backoff} \times 2^{\text{attempt}}\right) \times \text{jitter}$$
   where `base_report_backoff = 500ms`, `max_report_backoff = 5.0s`, and `jitter \in [0.5, 1.5]`.
2. **Concurrent Ongoing Lease Renewal**:
   While the worker's result reporting loop retries, the worker's lease renewal mechanism **continues active lease renewal** (`POST /v1/jobs/{id}/attempts/{attempt_id}/renew`) every 10 seconds. Under the symmetric accept-until-swept rule (ADR-0014), renewals and commits are accepted while `attempts.state == 'RUNNING'`. Once the coordinator recovers, renewal extends `lease_expires_at` from database time, ensuring the attempt is never reaped while the worker is actively delivering its completed result.
3. **Terminal Rejection Halts Delivery**:
   If `ReportResult` or `RenewAttemptLease` receives an explicit terminal rejection:
   - HTTP 409 Conflict (`LEASE_EXPIRED` / `STALE_ATTEMPT_REJECTED` / `JOB_ALREADY_TERMINAL`)
   - HTTP 403 Forbidden (`WORKER_MISMATCH`)
   - HTTP 404 Not Found (e.g., attempt unknown to coordinator)
   The worker immediately aborts the delivery loop, logs the terminal rejection, and halts delivery without further retries.
4. **Explicit Delivery Bound**:
   The reporting retry loop is bounded by a cumulative deadline of $\text{lease\_duration} + \text{grace\_window}$ (default: $30\text{s} + 30\text{s} = 60\text{s}$), or terminates early if renewal receives HTTP 409 `LEASE_EXPIRED` (indicating the sweeper reaped the attempt).

### Existing Test Impact Analysis
- **Tests Pinning Current Behavior**:
  - `internal/worker/client_test.go:16-38` (`TestIsNonRetryableReportError`): Pins classification of HTTP 403, 404, 409 as terminal/non-retryable and 200, 400, 500, 502, 503, 504 as retryable.
  - `internal/worker/client_test.go:40-105` (`TestAgent_ReportResult_RejectionHandling`): Pins single-invocation status code rejection handling.
  - `internal/worker/client.go:324-355` (`executeAndReport`): Implements the current 5-attempt retry loop.
- **Assertion Impact**:
  Neither existing test asserts or depends on the 5-attempt iteration limit or the 3.1-second cap. Zero assertions in existing tests are weakened or removed.
- **Candidate for Owner Approval**:
  Expanding `executeAndReport` in `internal/worker/client.go:324-355` from a fixed 5-iteration loop to a 60-second time-bounded loop with capped jittered backoff and concurrent lease renewal during reporting is submitted as an additive resilience candidate for owner approval in Slice 4b.

### Planned Verification Tests
- `TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success` (planned test).
- `TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport` (planned test).
- `TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace` (planned test).
- `TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately` (planned test).
