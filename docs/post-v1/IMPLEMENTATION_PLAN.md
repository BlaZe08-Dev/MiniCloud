# Post-V1 Implementation Plan and Engineering Gates

## Working rule

Do not begin by implementing the entire architecture. MiniCloud V1 took deliberate work on state machines, concurrency, migrations, fault injection, and audit evidence. Preserve that discipline.

Every implementation slice must have:
- a task entry in `docs/agents/TASKS.md`
- one builder and one independent auditor
- exact owned files and out-of-scope files
- an ADR/schema/API contract where needed
- acceptance criteria and negative tests
- a handoff with executed evidence
- mutation proof for new security/correctness guards where required by `AGENTS.md`

## Task board entries (PV-01 and following)

The pack's original P0 to P7 slice list is replaced by the merged roadmap in [`ROADMAP.md`](ROADMAP.md) (its labels also collided with the repository's historical task IDs such as P0-01 and P4-08a). The first entries are:

| ID | Task | Builder | Notes |
|---|---|---|---|
| PV-01 | Documentation reconciliation and design freeze (Gate 0) | Claude Code | docs only; owner reviews the `AGENTS.md` change |
| V2-0 | ADR and implementation: worker concurrency semantics | Claude Code | first V2 task; touches the worker and its tests |
| PV-02 | Record the proposed placement, cross-platform and AI-noise designs (docs only) | Claude Code | every statement labelled [Decided] or [Proposed]; nothing implemented |
| V2-1 | Node and capability model (D3) and migration 000006 | Claude Code | after V2-0; needs the V2-1 ADR with OD-19 answered |

PV-01 acceptance: no `.go`, migration, dependency or route change; `make test-docs` rc 0 with exact output; every relative link and ADR reference resolves; no historical record deleted; no claim that a future component is implemented; PASS/SKIP/FAIL reported separately; the report lists conflicts found and how each was resolved.

Later slices (V2-2 onward) are written into `docs/agents/TASKS.md` by the orchestrator one at a time, each with goal, acceptance criteria, owned files, out-of-scope files, builder, auditor and migration number if any. Open decisions are answered in an ADR amendment before the slice that needs them ([`OPEN_DECISIONS.md`](OPEN_DECISIONS.md)).

## Proposed designs recorded by PV-02 (not implemented)

- V2-3 placement (filter, score, round-robin tie-break; EMA with hysteresis; TTL): [`NODE_CAPABILITIES.md`](NODE_CAPABILITIES.md) section "Placement algorithm (proposed)". The V2-3 ADR is written later and must answer OD-21 to OD-24. V2-2 must produce the median-of-N speed inputs.
- Heterogeneous devices: [`ARCHITECTURE.md`](ARCHITECTURE.md) section 12 (Linux amd64 only is tested; a mixed-OS run is a V3-4 / V4-6 goal; OD-25).
- V4-4 validation pipeline and AI noise controls: [`INTELLIGENCE_DISPATCHER.md`](INTELLIGENCE_DISPATCHER.md) and [`SECURITY.md`](SECURITY.md).

## Quality gates

Continue the existing repository's quality gates (`AGENTS.md` section 6). At minimum, each code task must run the applicable:
- `gofmt`
- `go vet ./...`
- `go build ./...`
- `make test-docs`
- `go test -race -count=1 ./...` under the repository's test-database rules
- `make test-db` when persistence is touched
- `make test-security` and relevant fault-injection suites for security/dispatch changes

Never run destructive container/database targets without the safeguards and explicit owner approval already required by `AGENTS.md`.

## Evidence labels

Use the existing repository's evidence vocabulary consistently:
- executed
- confirmed by code reading
- designed, not executed
- inferred
- open question

Do not copy V1 test counts into post-V1 claims. New results require new command output and handoff evidence.
