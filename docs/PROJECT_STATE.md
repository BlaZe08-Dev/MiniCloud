# MiniCloud Project State & Handoff

## 1. Current Phase & Milestone
- **Phase 0:** Context Engineering & Design Docs — **COMPLETED**
- **Milestone 1:** Local Core — **IN PROGRESS**
- **Exit Criteria for Milestone 1:**
  - `go mod init` initialized with `github.com/BlaZe08-Dev/Minicloud` (standard library only).
  - Pure domain model in `internal/domain` with Job, Attempt, Worker, central FSM validation, and typed errors.
  - In-memory thread-safe repository in `internal/storage`.
  - Task executor with allowlist (`echo`, `sleep`, `sha256_loop`), bounded execution timeout, payload and output caps.
  - HTTP coordinator server (`POST /v1/jobs`, `GET /v1/jobs/{id}`, `GET /v1/jobs/{id}/attempts`, `GET /v1/workers`, `GET /healthz`).
  - Worker pull-based coordination protocol (register, heartbeat, poll/claim, report result with attempt verification).
  - Deterministic round-robin scheduler with decision logging.
  - CLI `cmd/mc` (`submit`, `get`, `attempts`, `workers`) and daemons `cmd/coordinator`, `cmd/worker`.
  - Makefile with `build`, `test`, `test-race`, `fmt`, `vet`, `run-coordinator`, `run-worker`, `demo`.
  - Comprehensive unit test suite (transition matrix, executor limits, scheduler, stale rejection) and multi-process integration test passing with `-race`.

---

## 2. Done
- `f5a4968` — `docs: add MiniCloud blueprint and instructions` (added blueprint PDF and instructions in `docs/source/`).
- Initialized Phase 0 design documentation:
  - `GEMINI.md` session handbook and operating rules.
  - `docs/architecture.md` component boundaries and ownership specification.
  - `docs/state-machine.md` FSM and state transition matrix.
  - `docs/failure-model.md` fault matrix and expected behaviors.
  - `docs/protocol.md` HTTP API and worker pull-based protocol.
  - `docs/adr/0001-record-architecture-decisions.md` ADR process.
  - `docs/adr/0002-go-stdlib-http-for-initial-api.md` Go stdlib net/http selection.
  - `docs/adr/0003-pull-based-worker-protocol.md` Pull-based worker protocol selection.
  - `CHANGELOG.md` and `README.md`.
  - `.gitignore` for Go projects.

---

## 3. In Progress
- Committing and pushing Phase 0 design and context documentation to `main`.
- Switching to branch `milestone/1-local-core` to implement Milestone 1.

---

## 4. Next Smallest Step
- Step 1 Completion: Commit Phase 0 context files and push `main`.
- Step 2 Start: Create branch `milestone/1-local-core`. Initialize `go.mod` with module path `github.com/BlaZe08-Dev/Minicloud`.

---

## 5. Open Questions & Ambiguities
- *None currently.* All requirements in Blueprint and Instructions are explicit and unambiguous for Milestone 1.

---

## 6. Known Defects & Limitations
- Durability is in-memory only (PostgreSQL arrives in Phase 3).
- Queue transport is in-memory only (Redis Streams arrives in Phase 5).
- No lease expiration background sweeper yet (full lease expiry and recovery arrive in Phase 4; Milestone 1 validates attempt ID/token on commit).
- Container isolation is not yet introduced (Docker arrives in Phase 5; execution is constrained via in-process allowlist).

---

## 7. Last Verified Commands & Actual Results
- **Date:** 2026-10-19
```bash
$ go version
go version go1.22.2 linux/amd64

$ git status
On branch main
Untracked files:
	.gitignore
	CHANGELOG.md
	GEMINI.md
	README.md
	docs/PROJECT_STATE.md
	docs/adr/
	docs/architecture.md
	docs/failure-model.md
	docs/protocol.md

$ git log --oneline -1
f5a4968 docs: add MiniCloud blueprint and instructions
```
