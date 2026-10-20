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

To fulfill the architectural requirement that the scheduler policy actively governs execution and emits structured decision logs (`policy`, `candidates`, `selected`, `reason`), ADR 0011 re-evaluates three candidate models against blueprint Section 4:
1. **Candidate A (Scheduler Pre-Assignment with Bounded Accept Window):** Coordinator/scheduler selects a worker when the job is queued; workers poll only for their assigned jobs; stale assignments are bounded by an accept window and re-scheduled upon timeout or worker unhealthiness.
2. **Candidate B (Hold Window at Poll Time):** Coordinator arbitrates poll requests using a temporary hold window for round-robin target workers (rejected due to test determinism risks and idle worker delays).
3. **Candidate C (Honest FIFO Pull Baseline):** Acknowledge that V1 pull dispatch is FIFO queue-driven, name the policy `"fifo_pull"`, log structured candidate/selection decisions on every poll, and reserve advanced resource-aware scheduling for V2.

*The selection between Candidate A and Candidate C is PROPOSED in ADR 0011, to be settled prior to Slice 3d.*
