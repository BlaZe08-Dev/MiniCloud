# ADR 0006: Redis Streams Dispatch Evaluation in MiniCloud V2

## Status
Accepted for evaluation by project owner (2026-10-20). Final transport decision pending benchmark results.

## Context
MiniCloud V1 establishes a durable PostgreSQL pull-based dispatch baseline where workers poll the coordinator and claim jobs persisted in the database. In distributed architectures, dedicated message streams (like Redis Streams) are frequently adopted for job dispatch, offering consumer groups, pending-entry tracking (`XPENDING`), and event fanout.

However, introducing Redis Streams introduces dual-write consistency challenges between PostgreSQL (the authoritative source of truth for job state) and Redis (the transient delivery mechanism). We need an empirical evaluation plan to decide whether the performance and operational benefits of Redis Streams justify the consistency overhead.

## Decision
1. **Plan Redis Streams as an empirical evaluation in MiniCloud V2:**
   - Redis Streams will be benchmarked directly against the PostgreSQL-backed V1 dispatch baseline.
   - The evaluation will occur in V2 after resource-aware scheduling policies are implemented.
2. **Dispatch-Transport Abstraction Requirement:**
   - A dedicated dispatch-transport interface (`internal/queue`) must decouple coordinator lifecycle logic from the underlying dispatch mechanism.
   - The coordinator and scheduler must not assume Redis Streams or PostgreSQL storage mechanics directly for queue operations.
3. **Postgres-to-Redis Consistency Mechanisms:**
   To prevent split-brain states where PostgreSQL records a job but Redis loses the event (or vice-versa), we evaluate two consistency patterns:
   - **Transactional Outbox Pattern:**
     - *Mechanics:* Job state and an outbox event are committed atomically in a single PostgreSQL transaction. A background relay process polls the outbox table and publishes events to Redis Streams with idempotency keys.
     - *Trade-off:* Strong consistency and guaranteed at-least-once publishing, but introduces slight relay latency and additional database read overhead.
   - **Background Reconciler Pattern:**
     - *Mechanics:* Coordinator writes to PostgreSQL and attempts an immediate push to Redis. A periodic reconciler scans for `QUEUED` jobs in PostgreSQL lacking an active Redis consumer or lease and re-publishes missing events.
     - *Trade-off:* Lower latency on the happy path, but potential delay during network blips or coordinator crash-recovery windows before the reconciler catches up.
4. **Evaluation Metrics & Controlled Benchmark Protocol:**
   The comparison must be executed on identical workloads and failure schedules, measuring:
   - **Throughput:** Completed jobs per second under sustained load.
   - **Latency:** Queue wait time and end-to-end latency (p50, p95, p99).
   - **Failure Recovery Time:** Time elapsed from worker crash / network partition to attempt reassignment.
   - **Duplicate Delivery Count:** Frequency of duplicate attempt claims observed at the result-commit boundary.
   - **Worker Utilization:** Percentage of available worker slots actively executing work.
5. **Final Architecture Decision Deferred:**
   The final choice of transport (retaining PostgreSQL pull dispatch vs. adopting Redis Streams) will be documented in a subsequent ADR only after empirical benchmark numbers are captured.

## Consequences
- **Positive:**
  - Avoids introducing Redis prematurely in V1 when PostgreSQL pull dispatch is sufficient for core correctness.
  - Ensures architectural choices are grounded in reproducible data rather than intuition.
  - Enforces clean separation between job state management and queue transport.
- **Negative:**
  - Requires maintaining transport abstraction and building comparative benchmark harnesses in V2.

## Alternatives Considered
- *Permanent In-Memory Queue:* Insufficient for multi-worker recovery across coordinator restarts.
- *Adopt Redis Streams immediately in V1:* Discarded because solving dual-write distributed transactions before establishing single-node recovery increases defect risk.
