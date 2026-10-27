# Task board

Owner merges. Orchestrator writes specs. One builder per task. A file owned by an
active task may not be edited by any other task.

Builder rule (AGENTS.md section 7): Claude Code builds complex and hard tasks; Gemini
builds the rest. The other agent audits.

## Reserved shared touch points

| Resource | Held by | Notes |
|---|---|---|
| Next migration number | 000004 | Held by slice 4b; reserved for the Phase 4 Postgres slice (4b) |
| `go.mod` / `go.sum` | none | |
| `internal/domain` | none | |
| `internal/storage/storagetest` | none | Shared conformance suite |
| `docs/security/findings.md` | P4-4a | Held for the SEC-022 status line only; next free SEC number SEC-024 |

## Active

| ID | Title | Builder | Auditor | Branch | Status |
|---|---|---|---|---|---|
| P0-01 | Two-agent workflow files | Claude Code | Orchestrator review | agent/claude/P0-01 | ready |
| P4-01 | Phase 4 design follow-up (round-5 minors) | Gemini | Claude Code | agent/gemini/P4-01 | in review after push. |
| P4-4a | Phase 4 slice 4a (domain FSM, in-memory parity, SEC-022 fix, memory-mode dispatch rewire) | Claude Code | Gemini | agent/claude/P4-4a | in progress. |

### P0-01 — Two-agent workflow files
- Goal: commit the agent rules so both agents read the same file.
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

### P4-01 — Phase 4 design follow-up (round-5 minors)
- Goal: fix the two minor findings of the round-5 audit of P4-00, bring the round-5 review onto main, and register the next two tasks on the task board.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-01`. Status: in review after push.
- Owned files: `docs/agents/handoffs/P4-00.md`, `docs/agents/handoffs/P4-00-citation-check.txt` (to delete), `docs/phase4-plan.md`, `docs/protocol.md`, `docs/adr/0014-lease-model-and-expiry.md`, `docs/agents/TASKS.md`, `docs/agents/reviews/P4-00-round5.md` and `docs/agents/reviews/P4-00-round5-evidence.txt`.

### P4-4a — Phase 4 slice 4a (domain FSM, in-memory parity, SEC-022 fix, memory-mode dispatch rewire)
- Goal: Phase 4 slice 4a (domain FSM, in-memory parity, SEC-022 fix, memory-mode dispatch rewire).
- Builder: Claude Code. Auditor: Gemini. Branch: `agent/claude/P4-4a`. Status: in progress.
- Owned files: internal/domain/**, internal/storage/** (in-memory and postgres repositories, storagetest), internal/coordinator/**, internal/api/** only if the plan requires it, tests/** (including tests/review/sec022_probe_test.go and tests/docs/citation_allowlist.txt entry removal), docs/security/findings.md (SEC-022 status line only), docs/agents/handoffs/P4-4a.md.
- Out of scope: any migration (000004 stays reserved for slice 4b), docs/phase4-plan.md and all other design docs, CHANGELOG.md and PROJECT_STATE.md (owner adds at merge).

## Done

| ID | Title | Merged in |
|---|---|---|
| P4-00 | Phase 4 design close-out | PR #17, merge commit f6c7ec8 |
