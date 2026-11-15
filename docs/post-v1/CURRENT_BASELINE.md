# MiniCloud V1.0.0 — Source Baseline and Audit Notes

**Baseline label: static review at 166c719, tests not re-run.** The owner's review below read code and documents only. Task PV-01 re-checked the claims it marks below by reading code and running `git`/`grep` commands; PV-01 also ran no Go test suite beyond `make test-docs` (see `docs/agents/handoffs/PV-01.md`).

## Review identity

- Repository: `BlaZe08-Dev/MiniCloud`
- Branch inspected: `main`
- Main commit inspected: `166c71998c6866418accccaa07df1dbeee069423`
- Tree: `b689c1b1b852da550bf19e12d6bc7945e04e8569`
- Release: `v1.0.0`, published 2026-11-15
- Release notes identify the V1 code baseline as `d7a397c` and state that code was unchanged after that baseline; subsequent main changes include documentation/audit-report archival.
- Module: `github.com/BlaZe08-Dev/Minicloud`
- Go version declared in `go.mod`: `1.22.2`
- Direct external dependency: `github.com/jackc/pgx/v5 v5.7.1`

## V1 release scope confirmed by repository and release notes

MiniCloud V1 is a Go distributed job-execution platform with:

- `cmd/coordinator`: HTTP coordinator and background lifecycle loops.
- `cmd/worker`: independent worker process.
- `cmd/mc`: command-line client.
- `cmd/migrate`: PostgreSQL migration runner.
- `internal/domain`: job, attempt, worker, state, transition, failure, lease, and event types.
- `internal/coordinator`: orchestration, worker liveness, retries, cancellation, lease sweeping, assignment loop.
- `internal/storage`: repository interfaces and in-memory storage.
- `internal/storage/postgres`: durable PostgreSQL implementation and migrations.
- `internal/api`: standard-library HTTP API.
- `internal/worker`: HTTP worker client/agent.
- `internal/executor`: bounded allowlisted in-process tasks.
- `internal/scheduler`: scheduler interface, an older deterministic round-robin selector (`RoundRobinScheduler`), and the pure ADR-0017 round-robin rule (`NextAfterCursor`, `CursorSet`). The binary constructs `RoundRobinScheduler` and injects it into the coordinator service, but no production code calls `SelectWorker`; the pure rule is on the production path through the assignment repositories (confirmed by code reading in PV-01).
- `internal/queue`: queue abstraction and in-memory implementation; it is not used by the current production dispatch path.

PostgreSQL is the durable source of truth for jobs, attempts, workers, results, and job events. The current migration sequence includes `000001` through `000005`; `000005` is already used for assignment-based round-robin.

## Current dispatch behavior

Two policies exist:

- `round_robin` is the coordinator binary's default. It uses assignment-based pre-reservation with an accept window, a coordinator assignment loop, repository assignment operations, and worker polling/claiming.
- `fifo_pull` remains selectable through `MINICLOUD_DISPATCH_POLICY=fifo_pull`. A polling worker claims the next eligible job through repository dispatch.

Important code distinction: `internal/scheduler.RoundRobinScheduler.SelectWorker` is not the production `round_robin` assignment mechanism. In the `fifo_pull` path, the coordinator explicitly does not call `scheduler.SelectWorker`. The assignment-based policy is implemented through `internal/coordinator/assignment*.go` and `storage.AssignmentRepository`, including the PostgreSQL implementation.

## Current worker execution model

- Worker registration currently advertises `echo`, `sleep`, and `sha256_loop`.
- `internal/executor.Registry` registers those three task implementations.
- The executor interface accepts validated payload bytes, a context, and a timeout; output is bounded.
- The worker process communicates with the coordinator over HTTP and handles registration, heartbeats, polling, lease renewal, execution, and result reporting.
- The current worker's polling/execution loop calls execution synchronously; it does not yet provide a general-purpose concurrent pool of heterogeneous AI CLI processes.
- **Finding carried into V2 (not a security finding):** the worker runs one task at a time, yet its register and heartbeat payloads report `available_slots` equal to `TotalSlots` (default 4, `MINICLOUD_WORKER_SLOTS`) at all times, so it advertises more capacity than it executes (confirmed by code reading in PV-01: `internal/worker/client.go` func `Run`, func `register`, func `sendHeartbeat`). The effect on dispatch fairness and on benchmarks was not tested. Slice V2-0 resolves it before any resource-aware scheduling or benchmark work.
- There is no provider adapter system and no CLI discovery/launch capability.

## Current worker/node metadata

`domain.Worker` contains worker ID, version, status, total/available slots, supported task names, heartbeat/registration timestamps, and internal poll freshness. It does **not** contain:

- CPU model/core count or utilization
- total/free RAM
- GPU model, VRAM, driver, or utilization
- disk capacity/free space
- OS/architecture inventory beyond what might be inferred externally
- installed Claude/Codex/Gemini CLI names or versions
- CLI authentication/readiness status
- per-tool execution modes or capabilities

No separate node-capability registry exists in V1.

## Current user interfaces

- HTTP API routes cover job submission, job inspection, attempts, cancellation, worker listing, worker registration/heartbeat/poll, result reporting, lease renewal, and health/readiness.
- `mc` currently offers basic submit/get/attempts/workers commands (no cancel command; cancellation is an HTTP API call).
- There is no web chat UI, workflow DAG API, official SDK, or primary-agent integration adapter in the inspected V1 source.

## Security and availability boundaries

- The coordinator defaults to `127.0.0.1:8080`.
- The V1 release explicitly accepts the unauthenticated HTTP API/worker endpoints as SEC-004, with localhost binding as a mitigation. Do not expose this V1 service to untrusted networks.
- V1 does not implement mTLS, API authentication/authorization, multi-tenancy, or coordinator leader election.
- V1 executes allowlisted in-process tasks; it does not execute arbitrary shell commands on the host.
- PostgreSQL durability and restart recovery do not equal automatic coordinator failover. A replacement coordinator can recover from durable state, but V1 does not elect a replacement leader automatically.

## Documentation drift found during review

The current docs need a post-release synchronization pass:

1. `README.md` still describes V1 as actively in progress.
2. `docs/PROJECT_STATE.md` still says V1 is awaiting the tag/owner action, despite the published release.
3. `docs/agents/TASKS.md` contains stale active tasks and says migration `000005` is the next free migration even though `000005` exists.
4. `AGENTS.md` prohibits AI scheduling, web UI, and consensus/Raft absent demonstrated need. The new owner-approved direction is now a documented extension, but those features are still not implemented.
5. `docs/architecture.md` includes an `internal/observability` package that was not present in the inspected Go tree and describes queue/scheduler ownership that needs to be reconciled with the production path.
6. ADR-0008 and some older queue-oriented design text describe a queue-driven dispatch flow that later implementation decisions superseded. Preserve the ADR as history; add a supersession note rather than rewriting its historical decision.
7. `docs/source/MINICLOUD_INSTRUCTIONS.md` and the original blueprint instructions predate the actual V1 implementation and later version-boundary decisions. Keep them as historical source material; make the post-V1 architecture pack the active extension plan.
8. `CHANGELOG.md` and `docs/agents/TASKS.md` should be reconciled with the published V1.0.0 release and the actual merged task history.

## Reconciliation status after PV-01

Items 1 to 6 and 8 above were reconciled by PV-01 (README status, PROJECT_STATE post-release section, task board and next migration 000006, the scoped `AGENTS.md` rule, the `docs/architecture.md` post-V1 section and observability correction, the ADR-0008 implementation note, the CHANGELOG release section). Item 7 needs no change: the source documents stay as historical material and are excluded from the citation checker.

## Evidence honesty

This review inspected repository metadata, the release record, key live architecture/security/roadmap documents, and the implementation paths listed above. It was a **static source/document review**. No Go tests, build, benchmarks, or database tests were executed as part of this review. The test counts in the GitHub release notes are reported release evidence, not tests rerun during this review.

Before implementation, Claude must run the repository's required preflight and quality gates from the current `AGENTS.md`, using a safe worktree and the project's own test-database rules.
