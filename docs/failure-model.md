# MiniCloud Failure Model & Fault Matrix

## 1. Failure Handling Principles
MiniCloud is engineered around the reality that distributed systems fail partially and asynchronously:
- **Crash-Recovery Model:** Any component (coordinator, worker, database, transport) may crash at any time and restart with its durable state intact.
- **Lease Expiry vs. Process Death:** A worker that stops responding cannot be assumed dead. It may be partitioned, GC-paused, or slow. MiniCloud uses lease tokens to prevent zombie workers from committing stale outcomes.
- **At-Least-Once Delivery & Deduplication:** Under failure and retry, duplicate executions may occur. All operations on the result commit boundary must be idempotent.

---

## 2. Fault Scenarios & Expected Behavior

| ID | Fault Scenario | Expected Behavior | Phase Introduced | Scope in Milestone 1 |
|---|---|---|:---:|---|
| **FS-01** | **Worker crashes before accepting job** | Job remains in `QUEUED` state. Another eligible worker is selected by the scheduler upon polling. | Phase 1 | In Scope (in-memory scheduler picks next registered worker). |
| **FS-02** | **Worker crashes after starting execution** | Lease expires without renewal. Coordinator detects missing heartbeat/expired lease, increments attempt count, and transitions job to `RETRYING` then `QUEUED`. | Phase 4 | Documented in M1; basic timeout detection in worker runner. |
| **FS-03** | **Worker finishes but result is lost/delayed** | Worker attempts retry reporting or lease expires. If lease expires, next attempt runs. Result commit validates attempt token to reject late submissions. | Phase 4 | In Scope for attempt token validation (HTTP 409 on mismatch). |
| **FS-04** | **Old worker returns result after lease expired & new attempt started** | Coordinator validates `attempt_id` and `lease_token`. Since current attempt ID has advanced, old result is rejected (`ErrStaleAttempt`, 409 Conflict). | Phase 4 | **In Scope for Milestone 1:** Central validation checks attempt ID match. |
| **FS-05** | **Coordinator crashes after submission but before dispatch** | Upon coordinator restart, persistent storage is read. Unassigned `QUEUED` jobs are reloaded into the scheduling pool. | Phase 3 | Out of Scope for M1 (in-memory state resets on restart; durable in Phase 3). |
| **FS-06** | **Coordinator crashes while committing result** | Database transaction provides atomicity. If uncommitted, worker retries result submission or lease expires; if committed, client sees `SUCCEEDED`. | Phase 3 | Out of Scope for M1 (requires PostgreSQL transactional outbox in Phase 3). |
| **FS-07** | **Dispatch transport (Redis) unavailable** | Coordinator retains persistent job state in database. Jobs are requeued upon transport reconnection. | Phase 5 | Out of Scope for M1 (no Redis used). |
| **FS-08** | **Database temporarily unavailable** | Coordinator rejects or buffers incoming requests with HTTP 503; retries DB connection with exponential backoff. | Phase 3 | Out of Scope for M1. |
| **FS-09** | **Worker heartbeats delayed or duplicated** | Duplicate heartbeats update `last_heartbeat_at` idempotently. Delayed heartbeats trigger worker eviction if delay exceeds `worker_timeout`. | Phase 2 | Basic worker heartbeat tracking implemented in M1. |
| **FS-10** | **Task times out, emits excessive output, or crashes** | Executor aborts task via `context.WithTimeout`, truncates output to max size limit, and reports structured failure with exit code/error. | Phase 1 | **In Scope for Milestone 1:** Enforced in `internal/executor`. |
| **FS-11** | **Cancellation races with job completion** | Coordinator serializes requests under mutex/row lock. If result commits first, job is `SUCCEEDED`; if cancel commits first, job is `CANCELLED`. | Phase 4 | In Scope for domain transition matrix validation. |

---

## 3. Failure Test Matrix Template

When writing automated fault injection tests, every test case must explicitly document:
1. **Target Fault Scenario:** Reference scenario ID (e.g., `FS-04`).
2. **Injection Mechanism:** Process kill (`SIGKILL`), network delay, simulated clock skew, dropped response.
3. **Expected Invariant:** State transition verification, stale rejection, or bounded retry.
4. **Evidence Collected:** Logs with `job_id` and `attempt_id`, status codes, final job state in repository.
5. **Recovery Mode:** Autonomous (system self-heals) vs. Operator-required (manual intervention).
