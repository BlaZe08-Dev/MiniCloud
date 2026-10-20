# MiniCloud Project State & Handoff

## 1. Current Phase & Milestone
- **Current Branch:** `milestone/2b-dispatch-interface`
- **Current Milestone:** Phase 2, Slice 2 — Dispatch-Transport Interface & 3-Worker Integration — **COMPLETED & VERIFIED**
- **Next Milestone:** Phase 3 — Durable Storage (PostgreSQL schema, forward-only migrations, restart recovery).
- **Target Release:** MiniCloud V1 (Phases 0–4).

---

## 2. Done
- `f5a4968` — `docs: add MiniCloud blueprint and instructions`
- `2fb7958` — `docs: establish Phase 0 architecture, state machine, and context layer`
- `eabcc22` / `78f2aa2` — `feat: implement Milestone 1 local core coordinator, worker protocol, and CLI` (Merged PR #1 to `main`).
- `a636ba0` — `feat: implement Phase 2 Slice 1 worker liveness, reaper loop, and failure recovery` (Merged to `main`).
- **Phase 2, Slice 2 — Dispatch-Transport Interface & 3-Worker Integration (`milestone/2b-dispatch-interface`):**
  - Accepted ADR 0008: Dispatch Transport Queue Interface, Delivery Semantics, and Repository Single Source of Truth ([ADR 0008](file:///home/blazeee/MiniCloud/docs/adr/0008-dispatch-transport-queue-interface.md)).
  - Documented Known Gaps for V3 Container Executor in [ADR 0005](file:///home/blazeee/MiniCloud/docs/adr/0005-docker-isolation-in-v3.md).
  - Clarified HTTP 204 `NO_QUEUED_JOBS` vs. HTTP 503 `WORKER_UNHEALTHY` in [docs/protocol.md](file:///home/blazeee/MiniCloud/docs/protocol.md) and [ADR 0007](file:///home/blazeee/MiniCloud/docs/adr/0007-worker-liveness-and-recovery.md).
  - Implemented `internal/queue` interface (`Enqueue`, `Claim`, `Release`, `Ack`, `Len`) and `Item` routing hint struct (`JobID`, `TaskType` only).
  - Implemented thread-safe `InMemoryQueue` with FIFO ordering, capability matching, and in-flight delivery tracking.
  - Added `NewServiceWithQueue` in `internal/coordinator` (preserved `NewService` signature with in-memory default).
  - Enforced repository re-validation on claim, dropping non-`QUEUED` items and preventing duplicate attempts upon queue redelivery.
  - Enforced queue `Ack` strictly after accepted result commits; stale, mismatched, or rejected results do not acknowledge the delivery.
  - Automated architecture boundary tests enforcing that `internal/executor` does not import scheduler/queue and `internal/queue` does not import api/coordinator/worker/scheduler.
  - Automated 3-worker batch integration test executing 6 jobs across 3 independent worker processes.

---

## 3. Repository Inspection Findings (Supplemental Requirements)
Verified against active codebase:
- **(a) Domain logic independence:** SATISFIED. `internal/domain` has zero imports of `net/http`, storage, scheduler, api, coordinator, worker, queue, or executor. Enforced by automated test.
- **(b) Pluggable scheduler interface:** SATISFIED. `scheduler.Scheduler` interface enables new policies without altering job lifecycle state machine.
- **(c) Executor independence:** SATISFIED. `internal/executor` is isolated from scheduler, queue, and coordinator, operating purely on byte payloads and context deadlines.
- **(d) Storage repository abstraction:** SATISFIED. `storage.Repository` interface separates coordinator logic from storage implementation; ready for PostgreSQL implementation in Phase 3.
- **(e) Dispatch-transport queue abstraction:** SATISFIED. `internal/queue` interface decouples dispatch signals from repository storage, enabling V2 Redis Streams evaluation without touching domain or orchestration code.
- **(f) `/v1` API versioning:** SATISFIED. All client and worker endpoints explicitly versioned under `/v1/` prefix with health probes at `/healthz` and `/readyz`.
- **(g) Correctness primitives:** SATISFIED. Stable job IDs, distinct monotonic attempt numbers, central FSM validation, stale-attempt rejection (HTTP 409), worker ID verification (HTTP 403), worker liveness reaper, at-least-once queue delivery, and bounded payload/output/runtime enforced.

---

## 4. Remaining Follow-Up Gaps (Do not implement in this branch)
1. **Phase 3 (Durable State):** PostgreSQL schema, forward-only migrations, and coordinator restart recovery.
2. **Phase 4 (Retry & Recovery):** Automated lease expiry background sweeper and re-dispatch of abandoned jobs.
3. **Phase 6 / V2 Evaluation:** Redis Streams comparative benchmark harness vs. PostgreSQL baseline.
4. **Phase 5 / V3 Isolation:** Docker container executor and multi-machine mutual TLS/auth.

---

## 5. Known Defects & Limitations
- Durability is currently in-memory only (PostgreSQL arrives in Phase 3).
- Queue transport is in-memory only (PostgreSQL pull baseline in V1; Redis Streams evaluation in V2).
- Jobs assigned to dead workers in Phase 2 remain `RUNNING` until Phase 4 implements lease expiry and automatic requeue.
- Allowlisted in-process tasks only (`echo`, `sleep`, `sha256_loop`) until V3 Docker containers.

---

## 6. Last Verified Commands & Actual Results
- **Date:** 2026-10-20
```bash
$ go version
go version go1.22.2 linux/amd64

$ gofmt -s -w .
(clean exit, code 0)

$ go vet ./...
(clean exit, code 0)

$ go test -v -race -count=1 ./...
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api        (passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/config     (passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator(passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/domain     (passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/executor   (passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/queue      (passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/scheduler  (passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage    (passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/worker     (passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/tests/architecture  (passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration   (passed with race detector)
```
