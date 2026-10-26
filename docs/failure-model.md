# MiniCloud Failure Model & Fault Matrix

## 1. Failure Handling Principles
MiniCloud is engineered around the reality that distributed systems fail partially and asynchronously:
- **Crash-Recovery Model:** Any component (coordinator, worker, database, transport) may crash at any time and restart with its durable state intact.
- **Lease Expiry vs. Process Death:** A worker that stops responding cannot be assumed dead. It may be partitioned, GC-paused, or slow. MiniCloud uses lease tokens and database timestamps to prevent zombie workers from committing stale outcomes.
- **At-Least-Once Delivery & Deduplication:** Under failure and retry, duplicate executions may occur. All operations on the result commit boundary must be idempotent.
- **Phase 4 Lease Recovery & Grace Period Guarantees:** In Phase 4 (ADR-0014, ADR-0015, ADR-0016), dead worker reclamation is fully solved. When a worker process dies abruptly or loses connectivity while executing a `RUNNING` job, its attempt lease expires on database time (`lease_expires_at`). To prevent coordinator restarts or database outages from mass-expiring healthy attempts and violating Phase 3 surviving-worker commit guarantees (`TestPostgresWiring_CoordinatorRestartRecovery` in `tests/integration/postgres_wiring_test.go:167` and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` in `tests/integration/part_d_security_test.go:794`), MiniCloud implements:
  1. **Coordinator Startup Grace Period (`MINICLOUD_STARTUP_GRACE_PERIOD`, 30s)**: Suppresses sweeper reaping for 30s after boot, giving surviving workers time to renew or commit.
  2. **Database Outage Grace Window (`MINICLOUD_OUTAGE_GRACE_WINDOW`, 30s)**: Suppresses reaping for 30s after database reconnection.
  3. **Optimistic Late Commit (Accept-Until-Reaped)**: While `attempts.state == 'RUNNING'`, completed work is accepted even if slightly past `lease_expires_at`, minimizing duplicate execution. Once reaped to `LOST`, late commits are strictly rejected (HTTP 409 `STALE_ATTEMPT_REJECTED`) and recorded as `diagnostic_late_result` with **zero resurrection**.
- **Bounded Retries & Budget Consumption:** Every attempt marked `LOST` by the sweeper consumes 1 attempt of the job's `max_attempts` budget (default 3), bounding cluster execution during extended fleet outages.
- **Single Cancellation Delivery Channel:** Cooperative cancellation is delivered exclusively via the lease renewal response (`cancel_requested: true`) with worst-case delivery latency bounded by one renewal interval (10s).

---

## 2. Fault Scenarios & Expected Behavior

| ID | Fault Scenario | Expected Behavior | Phase Introduced | Scope in Phase 4 |
|---|---|---|:---:|---|
| **FS-01** | **Worker crashes before accepting job** | Job remains in `QUEUED` state. Another eligible worker claims it upon polling via `repo.DispatchNextJob`. | Phase 1 | Handled via transactional skip-locked dispatch. |
| **FS-02** | **Worker crashes after starting execution** | Lease expires without renewal (`clock_timestamp() > lease_expires_at`). Coordinator sweeper marks attempt `LOST` (consuming 1 attempt), checks `attempt_no < max_attempts`, and transitions job to `RETRYING` with backoff `not_before`. | Phase 4 | **Core Phase 4 deliverable:** Autonomous lease sweeper and retry backoff. |
| **FS-03** | **Worker finishes but result is lost/delayed** | Worker retries `ReportResult`. If commit arrives while attempt is still `RUNNING`, it is accepted (Option 2). If sweeper already reaped attempt to `LOST`, commit is rejected with HTTP 409 `STALE_ATTEMPT_REJECTED` and recorded as `diagnostic_late_result` (no resurrection). | Phase 4 | **Core Phase 4 deliverable:** Rejection of expired attempt commits; diagnostic auditing. |
| **FS-04** | **Old worker returns result after lease expired & new attempt started** | Coordinator validates `attempt_id`, `lease_token_hash`, and `state`. Since attempt was transitioned to `LOST` and superseded, commit is rejected (`ErrStaleAttempt`, HTTP 409). | Phase 4 | **Core Phase 4 deliverable:** Constant-time hash check and attempt state validation. |
| **FS-05** | **Coordinator crashes after submission or during execution** | Upon coordinator restart, persistent storage is read. Unassigned `QUEUED` jobs remain durable. For `RUNNING` jobs, sweeper startup grace period (30s) allows surviving workers to reconnect and commit (preserving Phase 3 guarantee in `TestPostgresWiring_CoordinatorRestartRecovery`). | Phase 3 / Phase 4 | Phase 3 durable storage + Phase 4 startup grace. |
| **FS-06** | **Coordinator crashes while committing result** | Single database transaction provides atomicity across `attempts`, `jobs`, and `results`. If uncommitted, worker retries `ReportResult`; if committed, worker receives 200 COMMITTED on retry. | Phase 3 | Handled in Phase 3 atomic repository methods. |
| **FS-07** | **Dispatch transport (Redis) unavailable** | Coordinator retains persistent job state in database. Jobs are requeued upon transport reconnection. | Phase 5 | Out of Scope for V1. |
| **FS-08** | **Database temporarily unavailable** | Coordinator returns HTTP 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1`. Worker client applies exponential backoff with jitter. Upon database restoration, outage grace window (30s) prevents mass-reaping of in-flight attempts, allowing workers to commit cleanly (`TestSecurity_PartD_Outage_MidDispatchAndMidCommit`). | Phase 3 / Phase 4 | Phase 3 outage resilience + Phase 4 outage grace. |
| **FS-09** | **Worker heartbeats delayed or process dies abruptly (SIGKILL/crash)** | Inactive workers exceeding `worker_timeout` are reaped to `UNHEALTHY`. Running attempts held by that worker expire when `lease_expires_at` lapses, are reaped to `LOST` by the sweeper, and scheduled for retry. | Phase 2 / Phase 4 | Phase 2 reaps worker; Phase 4 sweeps and reclaims in-flight attempts. |
| **FS-10** | **Task times out, emits excessive output, or crashes** | Executor aborts task via `context.WithTimeout`, truncates output to 64 KiB, and reports structured failure with `exit_code` and `error_classification`. Input validation failures are classified `PERMANENT`. | Phase 1 / Phase 4 | Task-level timeout enforced; Phase 4 classifies retryability. |
| **FS-11** | **Cancellation races with job completion or duplicate cancel** | Coordinator serializes under row lock. If result commits first, job is `SUCCEEDED`; if cancel commits first, job is `CANCEL_REQUESTED`, delivered via renewal response within 10s, and confirmed to `CANCELLED`. Duplicate cancel on `CANCEL_REQUESTED` is an idempotent no-op (HTTP 202). | Phase 4 | **Core Phase 4 deliverable:** Cooperative cancel context and race resolution (ADR-0016). |
| **FS-12** | **Unauthorized worker reports result for an attempt** | Coordinator checks `attempt.WorkerID == reportingWorkerID`. Mismatches rejected with HTTP 403 `WORKER_MISMATCH`. Worker client treats 403 as non-retryable. | Phase 2 | Enforced centrally at API and storage boundaries. |
| **FS-13** | **Worker marked UNHEALTHY completes still-current attempt** | Result accepted if `attempt_id`, `lease_token`, and `worker_id` match and lease is unexpired. Coordinator status marks telemetry trust, not task validity. | Phase 2 | Preserved in Phase 4. |

---

## 3. Failure Test Matrix Template

When writing automated fault injection tests, every test case must explicitly document:
1. **Target Fault Scenario:** Reference scenario ID (e.g., `FS-02`).
2. **Injection Mechanism:** Process kill (`SIGKILL`), network delay, simulated clock skew, dropped response.
3. **Expected Invariant:** State transition verification, stale rejection, or bounded retry.
4. **Evidence Collected:** Logs with `job_id` and `attempt_id`, status codes, final job state in repository.
5. **Recovery Mode:** Autonomous (system self-heals) vs. Operator-required (manual intervention).
