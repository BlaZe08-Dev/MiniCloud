# GEMINI.md — MiniCloud Session Handbook

## Mission
MiniCloud is a learning-oriented but engineering-serious distributed job execution platform written in Go. A client submits a computational job, the control plane validates and tracks it, a scheduler assigns attempts to independent workers, and the platform records structured results. The objective is to understand, measure, and implement real distributed systems engineering—correct state transitions, worker coordination, leases, retries, and failure recovery.

## Version Boundaries & Staged Dependencies
- **V1 (Phases 0–4):** Core coordinator-worker execution, in-process allowlisted tasks, durable PostgreSQL state, round-robin, retries, leases, cancellation, tested failure handling. Pull dispatch baseline.
- **V2 (Phase 6 + Transport):** Resource-aware scheduling policies, benchmark generator, Redis Streams dispatch evaluation against PostgreSQL baseline (ADR-0006).
- **V3 (Docker + Phases 7–8):** Docker execution isolation (ADR-0005), multi-machine networking, worker auth/TLS, hardening.
- **Dependency Rule:** Standard library only in Milestone 1–2. PostgreSQL driver (Phase 3) and Redis client (V2) require explicit owner approval and an ADR before introduction. No unapproved dependencies.

## Critical Invariants
1. **Stable Job & Attempt IDs:** Stable job ID; distinct attempt ID and monotonically increasing attempt number per execution.
2. **Central State Validation:** State transitions validated centrally by `internal/domain` FSM; rejected with typed errors if invalid.
3. **Stale Attempt Rejection:** A worker can only commit results for its active attempt/lease; late or stale results strictly rejected.
4. **Bounded Execution:** Maximum runtime enforced via context; bounded payload (64 KiB), output (64 KiB), and retry attempts.
5. **Semantics:** At-least-once execution under retries. Never claim exactly-once execution for arbitrary external side effects.
6. **Lease Expiry Meaning:** Lease expiry means coordinator loss of trust in a worker, not proof the worker has stopped.
7. **Host Safety:** Never execute arbitrary untrusted shell commands on the host; allowlisted tasks only until V3 Docker containers.

## 10 Supplemental Engineering Rules
1. **Incremental build:** V1 -> V2 -> V3 on the same codebase; design for extension without implementing future features early.
2. **Strict boundaries:** Domain, coordinator/API, scheduling, worker communication, storage, executor, and observability remain isolated.
3. **Correctness from day one:** Models and contracts exist now; behavior arrives per phase.
4. **Replaceable components:** Round-robin first; scheduler, queue transport, and executor behind interfaces without in-process assumptions.
5. **Compatibility policy:** Additive-only changes within `/v1`; forward-only migrations; tests protecting architectural invariants.
6. **Version gating:** Follow V1/V2/V3 boundaries; Redis Streams and Docker deferred to their justified versions (ADR-0004).
7. **No premature distributed tech:** No Kubernetes, AI scheduling, Raft/Paxos, or multi-tenancy without demonstrated need.
8. **Pre-feature write-up:** Before V2/V3 features, document problem, components, compatibility, and justify changes with concrete data.
9. **Single milestone focus:** Small reviewable diffs; format and test after meaningful changes; report real results only.
10. **Inspect before modifying:** Never restart or regenerate existing work; inspect first, verify satisfied items, explain deltas.

## Exact Build & Test Commands
```bash
go version                    # Verify Go toolchain
gofmt -s -w .                 # Format all Go files
go vet ./...                  # Static analysis
go test -v -race -count=1 ./... # Run all tests with race detector
make build                    # Build coordinator, worker, mc binaries
make test-race                # Race-detector test target
make demo                     # End-to-end integration demo
```

## Context Map
| Question / Topic | Document to Read |
|---|---|
| Project status, current milestone, handoff, verified command output | [`docs/PROJECT_STATE.md`](file:///home/blazeee/MiniCloud/docs/PROJECT_STATE.md) |
| Release roadmap, V1/V2/V3 boundaries, exit criteria per phase | [`docs/roadmap.md`](file:///home/blazeee/MiniCloud/docs/roadmap.md) |
| High-level technical blueprint and complete project spec | [`docs/source/MiniCloud_Project_Blueprint.pdf`](file:///home/blazeee/MiniCloud/docs/source/MiniCloud_Project_Blueprint.pdf) |
| Engineering workflow and collaboration rules | [`docs/source/MINICLOUD_INSTRUCTIONS.md`](file:///home/blazeee/MiniCloud/docs/source/MINICLOUD_INSTRUCTIONS.md) |
| System components, responsibilities, ownership boundaries, data flow | [`docs/architecture.md`](file:///home/blazeee/MiniCloud/docs/architecture.md) |
| Job lifecycle, transition matrix, attempt states, invariants | [`docs/state-machine.md`](file:///home/blazeee/MiniCloud/docs/state-machine.md) |
| Fault scenarios, failure handling, recovery semantics per phase | [`docs/failure-model.md`](file:///home/blazeee/MiniCloud/docs/failure-model.md) |
| HTTP API contracts, worker protocol, payload limits, status codes | [`docs/protocol.md`](file:///home/blazeee/MiniCloud/docs/protocol.md) |
| Architectural Decision Records (ADRs) | [`docs/adr/`](file:///home/blazeee/MiniCloud/docs/adr/) |
| Version boundaries and compatibility policy | [`docs/adr/0004-version-boundaries-and-compatibility.md`](file:///home/blazeee/MiniCloud/docs/adr/0004-version-boundaries-and-compatibility.md) |
| Release history, user-facing changes | [`CHANGELOG.md`](file:///home/blazeee/MiniCloud/CHANGELOG.md) |

## Session Protocol
- **START Checklist:**
  1. Read `GEMINI.md` and `docs/PROJECT_STATE.md` only (load other docs on demand via Context Map).
  2. Run `git status`, `git log --oneline -10`, `go version`, `go build ./...`, `go test ./...`.
  3. Report actual results honestly. State current milestone and smallest next step.
  4. Wait for approval if the step changes architecture.
- **END Checklist:**
  1. Run `gofmt -s -w .`, `go vet ./...`, `go test -race -count=1 ./...`.
  2. Update `docs/PROJECT_STATE.md`, `CHANGELOG.md`, and any affected docs or ADRs.
  3. Commit with Conventional Commits on milestone branch.
  4. Push branch: `git push -u origin <branch>`.
  5. Provide final report (what was built, invariants enforced, real outputs, git status, limitations, next step).

## Do / Don't List
- **DO:** Small reviewable diffs, table-driven tests, race detection, honest reporting of failures, explicit errors.
- **DON'T:** No Kubernetes, consensus/Raft, AI scheduling, Web UI/dashboards, multi-tenancy.
- **DON'T:** Never invent command outputs or test results; never run destructive commands without approval.
- **DON'T:** Never commit secrets, tokens, or `.env` files; never commit implementation directly to `main` or force-push.
