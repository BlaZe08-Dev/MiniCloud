# MiniCloud Project State & Handoff

## 1. Current Phase & Milestone
- **Current Branch:** `docs/version-boundaries`
- **Current Milestone:** Version Boundaries, Architecture Boundaries, & Compatibility Alignment — **COMPLETED & VERIFIED**
- **Next Milestone:** Phase 2 — Independent Workers (worker heartbeat reaper, dead worker detection, attempt versioning, dispatch decoupling).
- **Target Release:** MiniCloud V1 (Phases 0–4).

---

## 2. Done
- `f5a4968` — `docs: add MiniCloud blueprint and instructions`
- `2fb7958` — `docs: establish Phase 0 architecture, state machine, and context layer`
- `eabcc22` / `78f2aa2` — `feat: implement Milestone 1 local core coordinator, worker protocol, and CLI` (Merged PR #1 to `main`).
- **Owner Decisions & Architecture Boundary Alignment (`docs/version-boundaries`):**
  - Accepted Owner Decision: Docker isolation deferred to V3 ([ADR 0005](file:///home/blazeee/MiniCloud/docs/adr/0005-docker-isolation-in-v3.md)).
  - Accepted Owner Decision: Redis Streams evaluated against PostgreSQL baseline in V2 ([ADR 0006](file:///home/blazeee/MiniCloud/docs/adr/0006-redis-streams-dispatch-evaluation-in-v2.md)).
  - Established V1/V2/V3 version boundaries and additive compatibility policy ([ADR 0004](file:///home/blazeee/MiniCloud/docs/adr/0004-version-boundaries-and-compatibility.md)).
  - Published phase-to-version mapping and exit criteria in [`docs/roadmap.md`](file:///home/blazeee/MiniCloud/docs/roadmap.md).
  - Enforced domain package independence via automated architecture test [`tests/architecture/boundaries_test.go`](file:///home/blazeee/MiniCloud/tests/architecture/boundaries_test.go).
  - Added Compatibility Policy section to [`docs/protocol.md`](file:///home/blazeee/MiniCloud/docs/protocol.md).
  - Updated [`GEMINI.md`](file:///home/blazeee/MiniCloud/GEMINI.md) handbook with the 10 supplemental engineering rules and roadmap links (76 lines).

---

## 3. Repository Inspection Findings (Supplemental Requirements)
Verified against active codebase:
- **(a) Domain logic independence:** SATISFIED. `internal/domain` has zero imports of `net/http`, storage, scheduler, api, coordinator, worker, or executor. Enforced by automated test.
- **(b) Pluggable scheduler interface:** SATISFIED. `scheduler.Scheduler` interface enables new policies without altering job lifecycle state machine.
- **(c) Executor independence:** SATISFIED. `internal/executor` is isolated from scheduler and coordinator, operating purely on byte payloads and context deadlines.
- **(d) Storage repository abstraction:** SATISFIED. `storage.Repository` interface separates coordinator logic from storage implementation; ready for PostgreSQL implementation in Phase 3.
- **(e) `/v1` API versioning:** SATISFIED. All client and worker endpoints explicitly versioned under `/v1/` prefix with health probes at `/healthz` and `/readyz`.
- **(f) Correctness primitives:** SATISFIED. Stable job IDs, distinct monotonic attempt numbers, central FSM validation, stale-attempt rejection (HTTP 409), and bounded payload/output/runtime enforced.

---

## 4. Remaining Follow-Up Gaps (Do not implement in this branch)
1. **Dispatch-Transport Abstraction:** "Define a dispatch-transport interface (`internal/queue`) for the Postgres pull baseline so Redis can be swapped in during V2, and keep the executor interface free of in-process assumptions."
2. **Phase 2 (Independent Workers):** Background worker reaper loop in coordinator to detect dead/timed-out workers (`now - last_heartbeat_at > worker_timeout`).
3. **Phase 3 (Durable State):** PostgreSQL schema, forward-only migrations, and coordinator restart recovery.
4. **Phase 4 (Retry & Recovery):** Automated lease expiry background sweeper and re-dispatch of abandoned jobs.
5. **Phase 6 / V2 Evaluation:** Redis Streams comparative benchmark harness vs. PostgreSQL baseline.
6. **Phase 5 / V3 Isolation:** Docker container executor and multi-machine mutual TLS/auth.

---

## 5. Known Defects & Limitations
- Durability is currently in-memory only (PostgreSQL arrives in Phase 3).
- Queue transport is in-memory only (PostgreSQL pull baseline in V1; Redis Streams evaluation in V2).
- Dead worker eviction is manual/implicit until Phase 2 worker reaper loop is added.
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
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api	(passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/domain	(passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/executor	(passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/scheduler	(passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage	(passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/tests/architecture	(passed with race detector)
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	(passed with race detector)
```
