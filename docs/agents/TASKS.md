# Task board

Owner merges. Orchestrator writes specs. One builder per task. A file owned by an
active task may not be edited by any other task.

Builder rule (AGENTS.md section 7): Claude Code builds complex and hard tasks; Gemini
builds the rest. The other agent audits.

Report file rule (AGENTS.md section 8): Every agent, before its final chat message, writes its final report to a new file `reports/<agent>/<TASK-ID>.md` at the repo root, commits it alone (`docs(report): ...`), pushes, then sends the final message.

## Reserved shared touch points

| Resource | Held by | Notes |
|---|---|---|
| Next migration number | none | Next free migration number 000005 (000004 merged in PR #20) |
| `go.mod` / `go.sum` | none | |
| `internal/domain` | none | |
| `internal/storage/storagetest` | none | Shared conformance suite |
| `docs/security/findings.md` | none | Released; numbers proposed in handoffs are provisional and `docs/security/findings.md` is the authority |

## Active

| ID | Title | Builder | Auditor | Branch | Status |
|---|---|---|---|---|---|
| P4-07 | CLI output sanitization (SEC-048 / F-4e-18) | Gemini | Claude Code | agent/gemini/P4-07 | active |
| P4-08b | ADR-0017 slice 5a: domain, pure policy and in-memory parity (no database) | Claude Code | Gemini | agent/claude/P4-08b | pushed for audit |
| P4-06b | Phase 4 docs pass (findings SEC-031..058, review records, protocol/failure-model) | Gemini | Claude Code | agent/gemini/P4-06b | merged in PR #31 |

### P4-08b — ADR-0017 slice 5a (domain, pure policy and in-memory parity)
- Goal: ADR-0017 Section 7 slice 5a only: assignment domain fields and events, the pure round-robin rule with per-type cursors, the in-memory assignment pass, round-robin claim (own reservation, then lapse and age arms), the poll-time refresh in memory, policy selection with `fifo_pull` as the default, and config validation including A1. No migration, no PostgreSQL code.
- Builder: Claude Code. Auditor: Gemini. Branch: `agent/claude/P4-08b`. Status: pushed for audit.
- Owned files: `internal/domain/assignment.go`, `internal/domain/job.go`, `internal/domain/worker.go`, `internal/domain/events.go`, `internal/scheduler/round_robin_rule*.go`, `internal/storage/assignment.go`, `internal/storage/inmemory*.go`, `internal/storage/postgres/assignment_5a_skips_test.go`, `internal/config/config.go`, `internal/config/assignment_config_test.go`, `internal/coordinator/service.go`, `internal/coordinator/assignment*.go`, `internal/coordinator/export_assignment_test.go`, `docs/adr/0017-assignment-round-robin.md` (A1, A2), `docs/agents/handoffs/P4-08b*`, `docs/agents/handoffs/P4-08b-evidence/`, `reports/claude/P4-08b.md`, `docs/PROJECT_STATE.md`, `docs/agents/TASKS.md`.

### P4-07 — CLI output sanitization (SEC-048 / F-4e-18)
- Goal: Sanitize worker- and task-supplied strings in `cmd/mc` before terminal printing; neutralize escape sequences, control characters, and lone carriage returns; un-skip `TestAbuse_MCOutput_HostileErrorText`; update SEC-048 to Confirmed, Fixed and SEC-031 to Documented.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-07`. Status: active.
- Owned files: `cmd/mc/**`, `tests/abuse/resources_test.go`, `docs/security/findings.md`, `docs/PROJECT_STATE.md`, `docs/agents/TASKS.md`, `docs/agents/handoffs/P4-07*`, `reports/gemini/P4-07.md`.
- Out of scope: coordinator API changes, database schema or migrations, allowlist modifications.

### P4-06b — Phase 4 docs pass (findings SEC-031..058, review records, protocol/failure-model)
- Goal: Register findings SEC-031 through SEC-058 from P4-4e; update `docs/protocol.md` and `docs/failure-model.md` with timeouts, `ClampText` semantics, worker fallback, and work-poll costs; import Gemini P4-4e review records and evidence with corrections; update `docs/agents/TASKS.md` and `docs/PROJECT_STATE.md`.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-06b`. Status: merged in PR #31.
- Owned files: `docs/security/findings.md`, `docs/protocol.md`, `docs/failure-model.md`, `docs/PROJECT_STATE.md`, `docs/agents/TASKS.md`, `docs/agents/reviews/P4-4e*`, `docs/agents/handoffs/P4-06b*`.
- Out of scope: production code, database migrations, allowlist changes.

### P4-06a — Phase 4 docs pass (SEC-029/030, report rule, review imports)
- Goal: Permanent report file rule in `AGENTS.md` and `docs/agents/TASKS.md`; register findings SEC-029 and SEC-030; import review records and evidence for P4-4d.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-06a`. Status: merged in PR #30.
- Owned files: `AGENTS.md`, `docs/agents/TASKS.md`, `docs/security/findings.md`, `docs/PROJECT_STATE.md`, `docs/agents/reviews/P4-4d*`, `docs/agents/handoffs/P4-06a*`.

### P4-4e — Phase 4 slice 4e (fault-injection suite, adversarial abuse suite, security review)
- Goal: Automated fault injection suite covering blueprint scenarios (FS-01 to FS-10); adversarial abuse suite covering 18 attack vectors; text clamping (`ClampText`); worker fallback failure report on HTTP 400 validation rejection; request and background timeouts (8s); benchmark work-poll database costs.
- Builder: Claude Code. Auditor: Gemini. Branch: `agent/claude/P4-4e`. Status: merged in PR #29.
- Owned files: `tests/faultinject/**`, `tests/abuse/**`, `internal/executor/**`, `internal/worker/**`, `internal/api/**`, `internal/coordinator/**`, `docs/agents/handoffs/P4-4e*`.

### P0-01 — Two-agent workflow files
- Goal: commit the agent rules so both agents read the same file.
- Builder: Claude Code. Auditor: Orchestrator review. Branch: `agent/claude/P0-01`. Status: merged in PR #16.
- Owned files: `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `docs/agents/**`,
  `tests/docs/citations_test.go` (only to add `AGENTS.md` and `docs/agents/` to its
  scan scope, if needed), `Makefile` (only if a per-worktree database target is needed).
- Acceptance:
  - Every rule from the old 88-line `GEMINI.md` that still applies is present in
    `AGENTS.md` (handoff lists each old section and where it went, or why dropped).
  - Section 9 of `AGENTS.md` holds verified commands to create and use per-worktree
    test databases, with real output from running `make test-db` (or the documented
    equivalent) against `minicloud_test_claude`.
  - `make test-docs` passes and covers `AGENTS.md`.
  - All gates pass with counts equal to `main` (no test changed).
- Out of scope: any Go code outside `tests/docs`, any design doc, `PROJECT_STATE.md`,
  `CHANGELOG.md` (the owner adds the changelog line at merge).

### P0-03 — README refresh
- Goal: refresh repository `README.md` with current project state, architecture, and roadmap alignment.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P0-03`. Status: merged in PR #24.
- Owned files: `README.md`, `docs/agents/handoffs/P0-03*` (when created).
- Out of scope: any Go code, database migrations, design docs.
- Spec written by the P4-03 builder from the branch diff; orchestrator to confirm.

### P4-4b — Phase 4 slice 4b (migration 000004, PostgreSQL parity)
- Goal: Schema migration 000004; RenewLease and SweepExpiredLeases in the in-memory and PostgreSQL repositories (in-lock grace check driven by a grace-state input, sweeper with row lock); pure job-retry backoff function in internal/domain; CommitAttempt 409 order; TestInMemoryRepository_LeaseRenewalAndExpiry (planned test).
- Builder: Claude Code. Auditor: Gemini. Branch: `agent/claude/P4-4b`. Status: merged in PR #20.
- Owned files: `internal/storage/**` (in-memory and PostgreSQL repositories, the Repository interface, storagetest), `internal/domain/**`, `tests/**`, `docs/agents/handoffs/P4-4b*` (when created).
- Out of scope: sweeper loop, outage grace and cooldown state, startup grace, worker renewal loop, delivery retry budget (slice 4c); cooperative cancel and failure classification (slice 4d).

### P4-4c-a — Phase 4 slice 4c-a (coordinator lease sweeper wiring, renew route, restart grace)
- Goal: Coordinator lease renewal route, sweeper loop wiring, grace configuration and metrics, and restart grace integration.
- Builder: Claude Code. Auditor: Gemini. Branch: `agent/claude/P4-4c-a`. Status: merged in PR #21.
- Owned files: `internal/coordinator/**`, `internal/api/**`, `internal/config/**`, `internal/storage/**`, `tests/integration/postgres_restart_grace_test.go`, `docs/agents/handoffs/P4-4c-a*` (when created).

### P4-4c-b — Phase 4 slice 4c-b (worker lease renewal & delivery)
- Goal: Periodic lease renewal loop in worker client, context cancellation on renew, task abort on expired/revoked lease, result delivery budget and backoff retry.
- Builder: Claude Code. Auditor: Gemini. Branch: `agent/claude/P4-4c-b`. Status: merged in PR #23.
- Owned files: `internal/worker/**`, `tests/**`, `docs/agents/handoffs/P4-4c-b*` (when created).

### P4-03 — Phase 4 slice 4a audit follow-up (minors, citation repair, task board)
- Goal: close the minors left after PR #19 (P4-02 round-2 audit findings R8-01..R8-05), the two open items from the first P4-02 audit (stale backoff allowlist entry note, repair 78 pre-existing test line citations across live docs (77 listed, 78 removed; see the P4-03 handoff)), import P4-02 audit records untouched, and update task board.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-03`. Status: merged in PR #22.
- Owned files: `docs/PROJECT_STATE.md`, `docs/roadmap.md`, `docs/agents/handoffs/P4-00.md`, `docs/agents/handoffs/P4-02.md`, `docs/agents/handoffs/P4-03.md`, `docs/agents/reviews/P4-02*`, `CHANGELOG.md`, `docs/security/findings.md`, `docs/security/test-plan.md`, `docs/security/threat-model.md`, `docs/agents/TASKS.md`.
- Out of scope: any Go code, SQL, database migrations, `tests/` or `internal/` code, allowlist changes.

### P4-05 — Phase 4 docs pass after slices 4a–4c
- Goal: import review records for P4-4b, P4-4c-a, P4-4c-b, P4-03, and P0-03 round 2 and round 3 records (imported after citation checker scoping in P4-T2, PR #27, merge commit 1e20143); register findings SEC-025 through SEC-028; record design-doc clarifications for slices 4a-4c (ADR-0014, ADR-0015, state-machine.md, protocol.md, phase4-plan.md); update PROJECT_STATE.md, TASKS.md, and CHANGELOG.md true on main; record follow-ups.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-05`. Status: active.
- Owned files: `docs/**` (design docs ONLY for listed clarifications), `CHANGELOG.md`, `docs/agents/TASKS.md`, `docs/PROJECT_STATE.md`, `docs/security/findings.md`, `docs/agents/handoffs/P4-05*`, `docs/agents/reviews/**`.
- Out of scope: all Go code, `Makefile`, migrations, `README.md`.

### P4-01 — Phase 4 design follow-up (round-5 minors)
- Goal: fix the two minor findings of the round-5 audit of P4-00, bring the round-5 review onto main, and register the next two tasks on the task board.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-01`. Status: merged in PR #18.
- Owned files: `docs/agents/handoffs/P4-00.md`, `docs/agents/handoffs/P4-01.md`, `docs/phase4-plan.md`, `docs/protocol.md`, `docs/adr/0014-lease-model-and-expiry.md`, `docs/PROJECT_STATE.md`, `docs/agents/TASKS.md`, `docs/agents/reviews/P4-00-round5.md` and `docs/agents/reviews/P4-00-round5-evidence.txt`.

### P4-4a — Phase 4 slice 4a (domain FSM, in-memory parity, SEC-022 fix, memory-mode dispatch rewire)
- Goal: Phase 4 slice 4a (domain FSM, in-memory parity, SEC-022 fix, memory-mode dispatch rewire).
- Builder: Claude Code. Auditor: Gemini. Branch: `agent/claude/P4-4a`. Status: merged in PR #19.
- Owned files: internal/domain/**, internal/storage/** (in-memory and postgres repositories, storagetest), internal/coordinator/**, internal/api/** only if the plan requires it, tests/** (including tests/review/sec022_probe_test.go and tests/docs/citation_allowlist.txt entry removal), docs/security/findings.md (SEC-022 status line only), docs/agents/handoffs/P4-4a.md.
- Out of scope: any migration, docs/phase4-plan.md and all other design docs, CHANGELOG.md and PROJECT_STATE.md, docs/agents/TASKS.md.

### P4-02 — Phase 4 slice 4a doc updates and follow-up (minors)
- Goal: repair line citations and drift, update Phase 4 plan with 4a/4b/4c scope split, register SEC-024, resolve P4-01 audit minors, update task board and changelog.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-02`. Status: merged in PR #19.
- Owned files: `docs/phase4-plan.md`, `docs/protocol.md`, `docs/state-machine.md`, `docs/adr/**`, `docs/security/**`, `docs/agents/**`, `CHANGELOG.md`, `docs/PROJECT_STATE.md`, `tests/docs/citation_allowlist.txt`, `docs/roadmap.md`.
- Out of scope: any code under `internal/`, `cmd/`, `tests/` (except `tests/docs/citation_allowlist.txt`), any database migrations.

## Done

| ID | Title | Merged in |
|---|---|---|
| P0-01 | Two-agent workflow files | PR #16, merge commit a9453fc |
| P4-00 | Phase 4 design close-out | PR #17, merge commit f6c7ec8 |
| P4-01 | Phase 4 design follow-up (round-5 minors) | PR #18, merge commit d28a3d1 |
| P4-4a | Phase 4 slice 4a (domain FSM, in-memory parity, SEC-022 fix, memory-mode dispatch rewire) | PR #19, merge commit e7403a9 |
| P4-02 | Phase 4 slice 4a doc updates and follow-up (minors) | PR #19, merge commit e7403a9 |
| P4-4b | Phase 4 slice 4b (migration 000004, PostgreSQL parity) | PR #20, merge commit 7e58d4a |
| P4-4c-a | Phase 4 slice 4c-a (lease renewal route, sweeper wiring, restart grace) | PR #21, merge commit fc451c9 |
| P4-03 | Phase 4 slice 4a audit follow-up (minors, citation repair, task board) | PR #22, merge commit 3e8ab9e |
| P4-4c-b | Phase 4 slice 4c-b (worker lease renewal & delivery) | PR #23, merge commit 3553c25 |
| P0-03 | README refresh | PR #24, merge commit 28560f6 |
| P4-4d | Phase 4 slice 4d (failure classification, bounded retries, cancel wins, failure presentation) | PR #25, merge commit f13d97f |
| P4-T1 | Test safety guards, test-db false-green guard, migrator name guard | PR #26, merge commit 9fbbbcf |
| P4-T2 | Citation checker scoping for frozen review records | PR #27, merge commit 1e20143 |
| P4-05 | Phase 4 docs pass after slices 4a–4c | PR #28, merge commit c85a58f |
| P4-4e | Phase 4 slice 4e (fault-injection suite, adversarial abuse suite, security review) | PR #29, merge commit 69396e5 |
| P4-06a | Phase 4 docs pass (SEC-029/030, report rule, review imports) | PR #30, merge commit 92f9a73 |

## Follow-ups

- **F-4c-a-A1 (Makefile `test-db` false-green risk; Resolved in P4-T1, PR #26, merge commit 9fbbbcf)**: Resolved by adding a `-list` pattern check to `Makefile` `test-db` target.
- **F-4c-a-A2 (`startBackground` top-level invocation test)**: The top-level invocation of `startBackground` in `cmd/coordinator/main.go` `main()` is not exercised by automated tests. Fix: add a smoke test in `cmd/coordinator/main_test.go` that briefly boots the binary with `MINICLOUD_STORAGE_DRIVER=memory` and verifies background loops start and shut down cleanly.
- **Review records citation checker scoping (Resolved in P4-T2, PR #27, merge commit 1e20143)**: Citation checker Rule A link check was scoped to live documents in P4-T2, allowing historical review records to be treated as immutable. Review records for P0-03 round 2 and round 3 (`P0-03-round2.md`, `P0-03-round2-evidence.txt`, `P0-03-round3.md`, `P0-03-round3-evidence.txt`) were imported byte-identical in P4-05.
- **F-4e-18 (CLI ANSI terminal escape sequence sanitization; OPEN)**: CLI `cmd/mc` does not strip or sanitize ANSI escape sequences from task outputs or error strings before printing to stdout/stderr. Risk: untrusted tasks could emit terminal escape sequences (cursor relocation, clear screen, OSC title setting) when output is viewed in an interactive terminal. Fix: implement terminal sanitization in `cmd/mc` to escape or strip non-printable/control sequences.
- **F-4e-17 (Rate limiting and connection bounding on unauthenticated endpoints; OPEN)**: All HTTP endpoints currently lack application-level rate limiting or connection bounding. Risk: an aggressive client or network flooder could starve connection pool or saturate coordinator memory. Mitigated in V1 by default localhost binding (`127.0.0.1:8080`) under SEC-004 accepted risk. Planned for V2/V3 hardening.
- **`make test-security` shared database detection**: `make test-security` prints 47 failures when inadvertently run against the shared database (`minicloud_test`) rather than a dedicated per-agent test database. Fix: update `make test-security` runner or preflight to check the database name and print exactly one clear explanatory error line refusing execution on the shared database.
- **Per-worktree test database note**: `make test-security` must be run from a worktree configured with its own `minicloud_<agent>_test` database (e.g. `minicloud_gemini_test` or `minicloud_claude_test`). `make test-db` is the appropriate suite for the shared database.
- **Next Slices (Post-Phase 4)**: Phase 4 core is complete. Next milestone is the Post-Phase 4 Assignment-Based Round-Robin Slice (Candidate A pre-assignment with bounded accept window) required before V1 release tagging.
