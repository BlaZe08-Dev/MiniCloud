# ADR 0007: Worker Liveness Tracking, Reaper Loop, and Failure Recovery

## Status
Accepted (2026-10-20)

## Context
In MiniCloud V1, workers are autonomous, independent processes communicating with the coordinator strictly over HTTP. When a worker process fails abruptly (such as an unhandled panic, OOM termination, or host failure) or suffers a network partition, the coordinator must detect the absence of liveness telemetry and cease dispatching new work to that node.

Without liveness tracking:
1. The scheduler would attempt to dispatch queued jobs to dead workers, stalling progress.
2. The coordinator would retain stale capacity accounting.
3. System operators would have no visibility into worker health.

We must define the worker liveness contract, reaper loop mechanics, failure classification, and recovery semantics without violating V1 boundaries (leases, requeue, and retries arrive in Phase 4).

## Decision

1. **Heartbeat Telemetry Protocol:**
   - Active workers periodically transmit heartbeats to `POST /v1/workers/{id}/heartbeat` at `MINICLOUD_HEARTBEAT_INTERVAL` (default: 5s).
   - The payload updates current available slots and refreshes `last_heartbeat_at`.

2. **Coordinator Reaper Loop:**
   - The coordinator executes a periodic background reaper on `MINICLOUD_REAPER_INTERVAL`.
   - The reaper interval is configurable, validated as `> 0` and `<= MINICLOUD_WORKER_TIMEOUT`, and defaults to at most half of the worker timeout (`MINICLOUD_WORKER_TIMEOUT / 2`).

3. **Staleness Threshold & Configuration Validation:**
   - A worker is marked `UNHEALTHY` when `clock.Now() - last_heartbeat_at > MINICLOUD_WORKER_TIMEOUT` (default: 15s).
   - Configuration validation strictly rejects `MINICLOUD_WORKER_TIMEOUT <= 0`.
   - Configuration validation enforces `MINICLOUD_WORKER_TIMEOUT >= 3 * MINICLOUD_HEARTBEAT_INTERVAL` to prevent false-positive churn under minor network jitter.

4. **Atomic Staleness Check and Status Transition:**
   - To eliminate reaper/heartbeat races, the staleness comparison (`last_heartbeat_at.Before(cutoff)`) and the transition from `HEALTHY` to `UNHEALTHY` are performed atomically under the repository lock in a single method (`ReapStaleWorkers`).
   - A heartbeat arriving concurrently with a reap is serialized under the lock; fresh heartbeats are never overwritten with `UNHEALTHY`.

5. **Scheduling and Polling Ineligibility:**
   - The scheduler checks worker health status and excludes all `UNHEALTHY` workers from candidate selection.
   - Work polling (`POST /v1/workers/{id}/poll`) from an `UNHEALTHY` worker does **not** count as liveness telemetry and returns no work (HTTP 503 `NO_WORKERS_AVAILABLE`) until a valid heartbeat restores `HEALTHY` status.

6. **Automatic Recovery on Heartbeat:**
   - An `UNHEALTHY` worker that resumes communication is immediately restored to `HEALTHY` status upon its next successful heartbeat or re-registration.

7. **Result Acceptance for Unhealthy Workers:**
   - If a worker was marked `UNHEALTHY` while executing a job attempt, and subsequently reports an execution result for its still-current attempt, the coordinator **must accept** the result provided attempt ID, lease token, and worker ID match.
   - *Rationale:* Marking a worker `UNHEALTHY` indicates coordinator loss of telemetry trust, not proof that the worker has stopped or that its computation is invalid.

8. **Worker ID Verification and Error Classification:**
   - The coordinator verifies `attempt.WorkerID == reportingWorkerID` upon result submission. Mismatches are rejected with HTTP 403 `WORKER_MISMATCH`.
   - The worker client treats HTTP 403 `WORKER_MISMATCH` and HTTP 409 (`STALE_ATTEMPT_REJECTED`, `JOB_ALREADY_TERMINAL`) as permanent non-retryable rejections, preventing infinite retry loops.

9. **Version Boundary (V1 Phase 2 vs Phase 4):**
   - In Phase 2, dead workers are marked `UNHEALTHY` and excluded from new scheduling. Any job attempt already assigned to the dead worker remains `RUNNING`.
   - Lease expiry, execution timeouts, attempt cancellation, and automatic job requeuing are explicitly deferred to Phase 4.

## Invariants Enforced
- **Atomic Transition:** No race condition can mark a worker unhealthy if a newer heartbeat was recorded after the reap cutoff.
- **Strict Attempt Ownership:** A worker cannot commit results for attempts assigned to other workers.
- **Poll Isolation:** Polling does not substitute for heartbeats; unhealthy workers cannot drain queues.

## Consequences
- **Positive:**
  - Prevents job dispatch to unresponsive or dead workers.
  - Deterministic time management via coordinator `Clock` interface enables sub-millisecond, sleep-free unit testing.
  - Fully compatible with future PostgreSQL atomic UPDATE statements (`WHERE last_heartbeat_at < cutoff`).
- **Negative:**
  - Jobs assigned to dead workers in Phase 2 remain `RUNNING` until Phase 4 implements lease expiry and requeue.
