# ADR 0003: Pull-Based Worker Protocol

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
