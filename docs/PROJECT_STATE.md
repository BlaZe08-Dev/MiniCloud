# MiniCloud Project State & Handoff

## 1. Current Phase & Milestone
- **Phase 0:** Context Engineering & Design Docs — **COMPLETED**
- **Milestone 1:** Local Core — **COMPLETED & VERIFIED**
- **Next Milestone:** Phase 2 — Independent Workers (worker failure detection, heartbeat timeouts, attempt versioning, dispatch decoupling).
- **Exit Criteria for Milestone 1:**
  - [x] Go module initialized (`github.com/BlaZe08-Dev/Minicloud`, Go 1.22.2, standard library only).
  - [x] Pure domain model in `internal/domain` with Job, Attempt, Worker, central FSM validation, and typed errors.
  - [x] In-memory thread-safe repository in `internal/storage`.
  - [x] Task executor with allowlist (`echo`, `sleep`, `sha256_loop`), bounded execution timeout, payload and output caps.
  - [x] HTTP coordinator server (`POST /v1/jobs`, `GET /v1/jobs/{id}`, `GET /v1/jobs/{id}/attempts`, `GET /v1/workers`, `GET /healthz`, `GET /readyz`).
  - [x] Worker pull-based coordination protocol (register, heartbeat, poll/claim, report result with attempt verification).
  - [x] Deterministic round-robin scheduler with decision logging.
  - [x] CLI `cmd/mc` (`submit`, `get`, `attempts`, `workers`) and daemons `cmd/coordinator`, `cmd/worker`.
  - [x] Makefile with `build`, `test`, `test-race`, `fmt`, `vet`, `run-coordinator`, `run-worker`, `demo`.
  - [x] Comprehensive unit test suite (transition matrix, executor limits, scheduler, stale rejection) and multi-process integration test passing with `-race`.

---

## 2. Done
- `f5a4968` — `docs: add MiniCloud blueprint and instructions`
- `2fb7958` — `docs: establish Phase 0 architecture, state machine, and context layer`
- Milestone 1 implementation:
  - `go.mod` module setup.
  - `internal/domain`: `JobState`, `AttemptState`, `WorkerStatus`, `ValidateJobTransition`, `Job`, `Attempt`, `Worker`.
  - `internal/storage`: `Repository` interface and concurrent-safe `InMemoryRepository`.
  - `internal/executor`: `Task` interface, `Registry`, `EchoTask`, `SleepTask`, `SHA256LoopTask`, timeout and size bounds.
  - `internal/scheduler`: `Scheduler` interface, `RoundRobinScheduler`, deterministic candidate sorting, decision logging.
  - `internal/coordinator`: `Service` with `SubmitJob`, `GetJob`, `ListAttempts`, `CancelJob`, `RegisterWorker`, `Heartbeat`, `PollWork`, and `CommitResult` with stale attempt rejection.
  - `internal/api`: `Server` and `Handler` with Go 1.22 route patterns, size limits, and JSON error envelope.
  - `internal/worker`: `Agent` client loop with registration, heartbeats, polling, execution, and reporting.
  - `internal/config`: Startup validation and environment variable parsing.
  - `cmd/coordinator`, `cmd/worker`, `cmd/mc`: Executable entry points.
  - `Makefile` and `scripts/demo.sh`: Automated multi-process build, test, and demo harness.
  - Table-driven unit tests for 81 transition matrix pairs, executor limits, scheduler determinism, API validation, stale rejection, and multi-process integration test.

---

## 3. In Progress
- Final verification, commit, and push of `milestone/1-local-core`.
- Code review and PR preparation for Milestone 1.

---

## 4. Next Smallest Step (Phase 2 - Independent Workers)
- **First Step:** Implement worker health monitoring and heartbeat timeout detection loop in the coordinator (evicting or marking workers `UNHEALTHY` when `now - last_heartbeat_at > worker_timeout`).

---

## 5. Open Questions & Ambiguities
- *None.* All Milestone 1 requirements verified against Blueprint v1.0 and Instructions.

---

## 6. Known Defects & Limitations
- **Storage Durability:** In-memory repository only; state does not survive coordinator restarts (PostgreSQL arrives in Phase 3).
- **Transport:** In-memory dispatch; no external message broker (Redis Streams arrives in Phase 5).
- **Lease Expiry Sweeper:** Attempt matching prevents stale overwrites on commit; background lease sweeper for automatic re-queueing of abandoned jobs arrives in Phase 4.
- **Task Sandbox:** Execution is constrained by Go context timeouts and size caps using allowlisted task implementations; containerized process isolation arrives in Phase 5.

---

## 7. Last Verified Commands & Actual Results
- **Date:** 2026-10-19
```bash
$ go version
go version go1.22.2 linux/amd64

$ gofmt -s -w .
(clean, no output)

$ go vet ./...
(clean, no output)

$ go test -v -race ./...
?   	github.com/BlaZe08-Dev/Minicloud/cmd/coordinator	[no test files]
?   	github.com/BlaZe08-Dev/Minicloud/cmd/mc	[no test files]
?   	github.com/BlaZe08-Dev/Minicloud/cmd/worker	[no test files]
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api	1.057s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/domain	1.290s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/executor	1.084s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/scheduler	1.029s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage	1.030s
?   	github.com/BlaZe08-Dev/Minicloud/internal/worker	[no test files]
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	2.029s

$ make demo
(Clean run: starts coordinator + 2 workers, executes echo, sha256_loop, sleep jobs, prints mc status and attempt history, terminates cleanly)
```
