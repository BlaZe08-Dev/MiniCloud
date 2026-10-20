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
In an unconstrained pull protocol, dispatch defaults to "first-poller-wins", leaving the scheduler inactive in the production dispatch loop.

To fulfill the architectural requirement that the scheduler policy (round-robin baseline) actively governs execution and emits structured decision logs (`policy`, `candidates`, `selected`, `reason`), the pull dispatch handler in the coordinator integrates **Hybrid Pull with Scheduler Arbitration** (detailed in ADR 0011):
1. Polling workers present their ID and capacity.
2. The coordinator retrieves eligible candidates from the repository and invokes `scheduler.SelectWorker(eligibleWorkers, taskType)`.
3. If the polling worker matches the scheduler's selection, dispatch proceeds immediately. If another eligible worker is selected by policy, the coordinator reserves the job with a short scheduling window or evaluates the next candidate job.
4. Every scheduling decision is logged with policy, candidate set, selected worker, and explicit reason.
