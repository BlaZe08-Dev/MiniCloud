# GEMINI.md — MiniCloud Session Handbook

## Mission
MiniCloud is a learning-oriented but engineering-serious distributed job execution platform written in Go. A client submits a computational job, the control plane validates and tracks it, a scheduler assigns attempts to independent workers, and the platform records structured results. The objective is to understand, measure, and implement real distributed systems engineering—correct state transitions, worker coordination, leases, retries, and failure recovery.

## Locked Tech Stack & Phased Introduction
- **Current Stack (Milestone 1):** Go standard library only (`net/http`, `log/slog`, `sync`, `crypto/sha256`).
- **Strictly Not Yet Allowed:** PostgreSQL (Phase 3), Redis / Redis Streams (Phase 5), Docker (Phase 5), Prometheus / Grafana (Phase 8), gRPC (Phase 7+), external HTTP frameworks. Introduce dependencies only when a milestone explicitly requires them.

## Critical Invariants
1. **Stable Job & Attempt IDs:** Every job has a stable ID; every execution attempt has a unique attempt ID and monotonically increasing attempt number.
2. **Central State Validation:** State transitions are validated centrally by domain logic and rejected with typed errors if invalid.
3. **Stale Attempt Rejection:** A worker can only report results for its active attempt/lease; late or stale attempt results are strictly rejected.
4. **Bounded Retries:** Retries are bounded by max-attempt policy; errors are classified as retryable or permanent.
5. **Semantics:** At-least-once execution under retries. Never claim exactly-once execution for arbitrary external side effects.
6. **Lease Expiry Meaning:** Lease expiry means coordinator loss of trust in a worker, not proof the worker has stopped.
7. **Host Safety:** Never execute arbitrary untrusted shell commands on the host; use predefined allowlisted tasks with validated payloads and bounded runtime/output.

## Working Rules
`plan -> list files -> implement scoped milestone only -> add tests -> gofmt/vet/test -> honest report -> update docs -> commit -> push`
- Docs must be updated in the SAME commit as the behavior they describe.
- If `PROJECT_STATE.md` and code disagree, verify against code and fix the doc.

## Exact Build & Test Commands
```bash
go version                    # Verify Go toolchain
gofmt -s -w .                 # Format all Go files
go vet ./...                  # Static analysis
go test -v -race ./...        # Run all tests with race detector
make build                    # Build coordinator, worker, mc binaries
make test-race                # Race-detector test target
make demo                     # End-to-end integration demo
```

## Context Map
| Question / Topic | Document to Read |
|---|---|
| Project status, current milestone, handoff, verified command output | [`docs/PROJECT_STATE.md`](file:///home/blazeee/MiniCloud/docs/PROJECT_STATE.md) |
| High-level technical blueprint and complete project spec | [`docs/source/MiniCloud_Project_Blueprint.pdf`](file:///home/blazeee/MiniCloud/docs/source/MiniCloud_Project_Blueprint.pdf) |
| Engineering workflow and collaboration rules | [`docs/source/MINICLOUD_INSTRUCTIONS.md`](file:///home/blazeee/MiniCloud/docs/source/MINICLOUD_INSTRUCTIONS.md) |
| System components, responsibilities, ownership boundaries, data flow | [`docs/architecture.md`](file:///home/blazeee/MiniCloud/docs/architecture.md) |
| Job lifecycle, transition matrix, attempt states, invariants | [`docs/state-machine.md`](file:///home/blazeee/MiniCloud/docs/state-machine.md) |
| Fault scenarios, failure handling, recovery semantics per phase | [`docs/failure-model.md`](file:///home/blazeee/MiniCloud/docs/failure-model.md) |
| HTTP API contracts, worker protocol, payload limits, status codes | [`docs/protocol.md`](file:///home/blazeee/MiniCloud/docs/protocol.md) |
| Architectural Decision Records (ADRs) | [`docs/adr/`](file:///home/blazeee/MiniCloud/docs/adr/) |
| Release history, user-facing changes | [`CHANGELOG.md`](file:///home/blazeee/MiniCloud/CHANGELOG.md) |

## Session Protocol
- **START Checklist:**
  1. Read `GEMINI.md` and `docs/PROJECT_STATE.md` only (load other docs on demand via Context Map).
  2. Run `git status`, `git log --oneline -10`, `go version`, `go build ./...`, `go test ./...`.
  3. Report actual results honestly. State current milestone and smallest next step.
  4. Wait for approval if the step changes architecture.
- **END Checklist:**
  1. Run `gofmt -s -w .`, `go vet ./...`, `go test -race ./...`.
  2. Update `docs/PROJECT_STATE.md`, `CHANGELOG.md`, and any affected docs or ADRs.
  3. Commit with Conventional Commits on milestone branch.
  4. Push branch: `git push -u origin <branch>`.
  5. Provide final report (what was built, invariants enforced, real outputs, git status, limitations, next step).

## Do / Don't List
- **DO:** Small reviewable diffs, table-driven tests, race detection, honest reporting of failures, explicit errors.
- **DON'T:** No Kubernetes, consensus/Raft, AI scheduling, Web UI/dashboards, multi-tenancy.
- **DON'T:** Never invent command outputs or test results.
- **DON'T:** Never run destructive commands without approval.
- **DON'T:** Never commit secrets, tokens, or `.env` files.
- **DON'T:** Never commit implementation directly to `main` or force-push.
