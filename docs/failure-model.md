# MiniCloud Failure Model & Fault Matrix

## 1. Failure Handling Principles
MiniCloud is engineered around the reality that distributed systems fail partially and asynchronously:
- **Crash-Recovery Model:** Any component (coordinator, worker, database, transport) may crash at any time and restart with its durable state intact.
- **Lease Expiry vs. Process Death:** A worker that stops responding cannot be assumed dead. It may be partitioned, GC-paused, or slow. MiniCloud uses lease tokens and database timestamps to prevent zombie workers from committing stale outcomes.
- **At-Least-Once Delivery & Deduplication:** Under failure and retry, duplicate executions may occur. All operations on the result commit boundary must be idempotent.
- **Phase 4 Lease Recovery Resolution:** In Phase 4 (ADR-0014, ADR-0015, ADR-0016), dead worker reclamation is fully solved. When a worker process dies abruptly or loses connectivity while executing a `RUNNING` job, its attempt lease expires on database time (`lease_expires_at`). The coordinator lease sweeper marks the attempt `LOST` and transitions the job to `RETRYING` (with exponential backoff) or `FAILED` (if `max_attempts` budget is exhausted), eliminating stranding.

---

## 2. Fault Scenarios & Expected Behavior

| ID | Fault Scenario | Expected Behavior | Phase Introduced | Scope in Phase 4 |
|---|---|---|:---:|---|
| **FS-01** | **Worker crashes before accepting job** | Job remains in `QUEUED` state. Another eligible worker claims it upon polling via `repo.DispatchNextJob`. | Phase 1 | Handled via transactional skip-locked dispatch. |
| **FS-02** | **Worker crashes after starting execution** | Lease expires without renewal (`clock_timestamp() > lease_expires_at`). Coordinator sweeper marks attempt `LOST`, checks `attempt_no < max_attempts`, and transitions job to `RETRYING` with backoff `not_before`. | Phase 4 | **Core Phase 4 deliverable:** Autonomous lease sweeper and retry backoff. |
| **FS-03** | **Worker finishes but result is lost/delayed** | Worker retries `ReportResult` up to 5 times. If lease expires before commit succeeds, next attempt runs. When old worker eventually delivers, commit fails with HTTP 409 `STALE_ATTEMPT_REJECTED`. | Phase 4 | **Core Phase 4 deliverable:** Rejection of expired attempt commits; diagnostic auditing. |
| **FS-04** | **Old worker returns result after lease expired & new attempt started** | Coordinator validates `attempt_id`, `lease_token_hash`, and `lease_expires_at`. Since attempt is no longer `RUNNING` or token is superseded, commit is rejected (`ErrStaleAttempt`, HTTP 409). | Phase 4 | **Core Phase 4 deliverable:** Constant-time hash check and attempt state validation. |
| **FS-05** | **Coordinator crashes after submission but before dispatch** | Upon coordinator restart, persistent storage is read. Unassigned `QUEUED` jobs remain durable and dispatch to workers upon next poll. | Phase 3 | Handled in Phase 3 durable storage. |
| **FS-06** | **Coordinator crashes while committing result** | Single database transaction provides atomicity across `attempts`, `jobs`, and `results`. If uncommitted, worker retries `ReportResult`; if committed, worker receives 200 COMMITTED on retry. | Phase 3 | Handled in Phase 3 atomic repository methods. |
| **FS-07** | **Dispatch transport (Redis) unavailable** | Coordinator retains persistent job state in database. Jobs are requeued upon transport reconnection. | Phase 5 | Out of Scope for V1. |
| **FS-08** | **Database temporarily unavailable** | Coordinator returns HTTP 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1`. Worker client applies exponential backoff with jitter and resumes once database recovers. | Phase 3 | Handled in Phase 3 outage resilience. |
| **FS-09** | **Worker heartbeats delayed or process dies abruptly (SIGKILL/crash)** | Inactive workers exceeding `worker_timeout` are reaped to `UNHEALTHY`. Running attempts held by that worker expire when `lease_expires_at` lapses and are swept to `LOST` and retried. | Phase 2 / Phase 4 | Phase 2 reaps worker; Phase 4 sweeps and reclaims in-flight attempts. |
| **FS-10** | **Task times out, emits excessive output, or crashes** | Executor aborts task via `context.WithTimeout`, truncates output to 64 KiB, and reports structured failure with `exit_code` and `error_classification`. | Phase 1 / Phase 4 | Task-level timeout enforced; Phase 4 classifies retryability. |
| **FS-11** | **Cancellation races with job completion** | Coordinator serializes under row lock. If result commits first, job is `SUCCEEDED`; if cancel commits first, job is `CANCEL_REQUESTED`, delivered to worker via renewal, and confirmed to `CANCELLED`. | Phase 4 | **Core Phase 4 deliverable:** Cooperative cancel context and race resolution (ADR-0016). |
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
