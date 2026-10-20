# ADR 0003: Pull-Based Worker Protocol

## Status
Accepted (2026-10-20, Amended in Phase 3)

## Context
MiniCloud needs a mechanism for workers to receive job attempts from the coordinator. Two primary communication models exist:
1. **Push-based:** The coordinator initiates network connections or HTTP requests to workers when a job is ready for execution.
2. **Pull-based (Worker Polling/Claiming):** Workers initiate requests to the coordinator to poll or claim available work when they have free capacity.

## Decision
We adopt a **pull-based** worker coordination model. Workers register with the coordinator, send periodic heartbeats, and issue poll requests (`POST /v1/workers/{id}/poll`) to claim pending job attempts according to available capacity slots.

## Consequences
- **Positive:**
  - **Natural backpressure:** Workers only request work when they have idle slots, preventing worker overload and queue pileup on slow nodes.
  - **Firewall & NAT friendly:** Workers connect outbound to the coordinator; workers do not need to expose open inbound HTTP server ports on their host machines.
  - **Simpler coordinator failure handling:** If a worker becomes unresponsive, the coordinator simply stops leasing work to it without dealing with connection timeouts during push attempts.
  - **Clean evolution path:** Maps directly to message queue consumption patterns (e.g., Redis Streams consumer groups in Phase 5).
- **Negative:**
  - Adds periodic polling traffic when the queue is empty (mitigated by short polling backoffs).

## Alternatives Considered
- *Push-based HTTP dispatch:* Requires workers to run an HTTP server and coordinator to maintain active worker endpoints and reachability through firewalls. Vulnerable to worker overload when job bursts occur.
- *Persistent bi-directional streaming (WebSockets / gRPC streams):* Adds connection state management and reconnect complexity before baseline dispatch correctness is proven.

---

## Amendment: Scheduler Participation in a Pull Model (Phase 3)
In an unconstrained pull protocol, dispatch defaults to first-poller-wins, leaving the scheduler inactive in the production dispatch loop.

In ADR 0011, three candidate models were evaluated against Blueprint Section 4 and Section 6. The repository owner has **ACCEPTED Candidate C (Honest FIFO Pull Baseline, policy name `"fifo_pull"`)** for Phase 3 and Slices 3a–3e:
1. **FIFO Pull Dispatch:** In V1 Phase 3, dispatch is executed directly by the transactional queue transport (`SELECT ... FOR UPDATE SKIP LOCKED`).
2. **Intentional Scheduler Non-Use:** `internal/scheduler` is **intentionally not called** in the V1 poll dispatch path. This non-use is temporary and preserves engineering honesty.
3. **Honest Dispatch Logging:** Dispatch logs record real facts only (`job_id`, `attempt_id`, `worker_id`, `policy="fifo_pull"`, `reason="claimed by polling worker with capacity"`). The system does not call `scheduler.SelectWorker` at poll time or emit misleading "scheduler decision" logs whose "selected" worker is merely the polling caller.
4. **Known Blueprint Deviation & Hard Gate:** While Blueprint Section 6 specifies scheduler-driven round-robin, V1 Phase 3 dispatch is FIFO pull. See [roadmap.md (Section 4)](../roadmap.md#4-post-phase-4-hard-gate-assignment-based-round-robin-slice) and [ADR 0011 (Section 2.4 item 4)](0011-postgres-dispatch-and-recovery.md#24-decision-honest-fifo-pull-baseline-candidate-c) for the mandatory hard gate requiring an explicit "Assignment-Based Round-Robin" slice immediately following Phase 4 before V1 completion or V2 benchmarking.
