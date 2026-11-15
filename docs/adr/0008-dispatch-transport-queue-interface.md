# ADR 0008: Dispatch Transport Queue Interface and Delivery Semantics

## Status
Accepted (2026-10-20)

## Context
In MiniCloud V1, job submissions are stored in durable repository state and dispatched to workers via HTTP polling. Originally, the coordinator claimed work directly through repository queries (`ListQueuedJobs`), tightly coupling job persistence with dispatch signaling.

As planned in ADR 0004 (Version Boundaries) and ADR 0006 (Redis Streams Dispatch Evaluation in V2), MiniCloud requires an explicit dispatch-transport abstraction. In V2, we will benchmark Redis Streams against PostgreSQL pull dispatch. To make this comparison clean and prevent transport concerns from bleeding into the core domain, scheduling, or executor modules:
1. Dispatch transport must be isolated behind a dedicated queue interface (`internal/queue`).
2. The repository must remain the single source of truth for durable job state, payloads, and attempt history.
3. The queue must hold delivery signals only, never duplicate job payloads or timeout configurations.
4. Delivery semantics must be precisely defined and resilient against at-least-once delivery duplicates and worker failure races.

## Decision

1. **Queue Interface (`internal/queue`):**
   We define a minimal dispatch-transport interface:
   ```go
   type Queue interface {
       Enqueue(ctx context.Context, item *Item) error
       Claim(ctx context.Context, supportedTasks []string) (*Item, error)
       Release(ctx context.Context, jobID string) error
       Ack(ctx context.Context, jobID string) error
       Len(ctx context.Context) (int, error)
   }
   ```

2. **Routing Signal Only (`Item` Definition):**
   - `Item` contains only `JobID string` and `TaskType string`.
   - `TaskType` serves as a routing hint enabling workers to claim jobs compatible with their capability profile (`supportedTasks`).
   - `Item` explicitly excludes `Payload` and `TimeoutSeconds`. At dispatch time (`PollWork`), the coordinator reads the validated payload and timeout directly from the repository. The repository remains the single source of truth.

3. **At-Least-Once Delivery Semantics:**
   - `Claim` transitions an item from the pending queue to an in-flight state.
   - An unacknowledged delivery may be released back to pending (`Release`) or redelivered upon timeout/failure.
   - If attempt creation fails during dispatch (e.g., worker status change or database error), the coordinator calls `Release` so another worker can claim the job.

4. **Repository Re-Validation on Claim:**
   - Because delivery is at-least-once, a duplicate delivery signal may be received for an already-dispatched or cancelled job.
   - Upon claiming an item from the queue, the coordinator **must re-validate** against the repository that the job is still in `QUEUED` state.
   - If the job is no longer `QUEUED` (e.g., already `RUNNING`, `CANCELLED`, or `FAILED`), the coordinator drops the duplicate item, acknowledges it (`Ack`), and claims the next item.
   - Under no circumstances does a redelivered item create a duplicate attempt.

5. **Strict Result Commit Acknowledgement (`Ack`):**
   - The queue delivery is acknowledged (`Ack`) **strictly after an ACCEPTED result commit** in `CommitResult`.
   - Stale attempt results (HTTP 409 `STALE_ATTEMPT_REJECTED`), mismatched workers (HTTP 403 `WORKER_MISMATCH`), or results for already-terminal jobs (HTTP 409 `JOB_ALREADY_TERMINAL`) do **not** acknowledge the queue.

6. **Default In-Memory Implementation & Extensibility:**
   - `InMemoryQueue` implements `Queue` using standard Go slices, maps, and `sync.Mutex`.
   - `coordinator.NewService` defaults to `InMemoryQueue`, preserving complete backwards compatibility with all existing callers and tests.
   - `coordinator.NewServiceWithQueue` allows injecting alternative transport implementations (such as Redis Streams in V2) without modifying domain or scheduling logic. (Removed in slice 4a: memory-mode dispatch now goes through repo.DispatchNextJob; queue injection is no longer used.)

## Invariants Enforced
- **Single Source of Truth:** Repository owns durable state and payloads; queue carries ephemeral notification/routing signals.
- **No Duplicate Attempts:** At-least-once queue redelivery is idempotent at the coordinator boundary via repository state checks.
- **Guaranteed Queue Cleanup:** Only legitimate completions acknowledge queue items.

## Consequences
- **Positive:**
  - Decouples coordinator orchestration from concrete queue mechanisms.
  - Zero payload duplication in memory or transport buffers.
  - Architecture boundary tests enforce complete package isolation (executor cannot import queue; queue cannot import coordinator/api/worker/scheduler).
  - Clear upgrade path for V2 Redis Streams evaluation without touching core domain code.
- **Negative:**
  - An additional repository read occurs at dispatch time to fetch job payload and timeout from job ID (negligible in-memory; single primary-key lookup in PostgreSQL).

## Alternatives Considered
- *Full Payload in Queue Item:* Rejected because duplicating payload (up to 64 KiB) in queue and repository introduces dual-source-of-truth divergence, increases memory overhead, and violates ADR-0004/0006 boundaries.
- *Ack Immediately on Claim:* Rejected because if a worker crashes before completing the attempt, the queue signal is lost permanently without manual reconciler recovery. Deferring Ack to result commit aligns with at-least-once execution.
- *Mutator Method (`SetQueue`):* Rejected in favor of constructor injection (`NewServiceWithQueue`) to maintain immutable service dependencies and avoid concurrency hazards. (constructor since removed in slice 4a)

## Implementation note (2026-11-15, PV-01)
This ADR is kept as the historical record of the queue-driven design. The production dispatch path no longer uses it: PostgreSQL mode bypassed the queue from Phase 3 ([ADR 0011](0011-postgres-dispatch-and-recovery.md), `fifo_pull` through the repository), memory mode was rewired to `repo.DispatchNextJob` in slice 4a, and the default `round_robin` policy runs through the repository assignment code of [ADR 0017](0017-assignment-round-robin.md). In V1.0.0 no production package imports `internal/queue` (confirmed by code reading). The queue interface remains the candidate seam for the V2-4 Redis Streams evaluation ([ADR 0006](0006-redis-streams-dispatch-evaluation-in-v2.md)); that evaluation decides whether it is kept, changed or removed.
