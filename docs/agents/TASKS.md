# Task board

Owner merges. Orchestrator writes specs. One builder per task. A file owned by an
active task may not be edited by any other task.

Builder rule (AGENTS.md section 7): Claude Code builds complex and hard tasks; Gemini
builds the rest. The other agent audits.

## Reserved shared touch points

| Resource | Held by | Notes |
|---|---|---|
| Next migration number | 000004 | Reserved for the Phase 4 Postgres slice (4b) |
| `go.mod` / `go.sum` | none | |
| `internal/domain` | none | |
| `internal/storage/storagetest` | none | Shared conformance suite |
| `docs/security/findings.md` | P4-00 | Next free number after the design merge: SEC-024 |

## Active

| ID | Title | Builder | Auditor | Branch | Status |
|---|---|---|---|---|---|
| P0-01 | Two-agent workflow files | Claude Code | Orchestrator review | agent/claude/P0-01 | ready |
| P4-00 | Phase 4 design close-out | Gemini (author) | Claude Code | docs/phase4-design | in progress (Builder addressing audit findings) |

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

### P4-00 — Phase 4 design close-out
- Goal: the Phase 4 design (the lease, retry and cancellation ADRs numbered 0014 to
  0016, the Phase 4 plan doc and the docs it updates; none on `main` yet) is
  consistent, complete and merged; it becomes the spec for P4 tasks.
- Owned files: everything changed on docs/phase4-design, AGENTS.md section 4 (context map rows only), docs/agents/TASKS.md (the P4-00 entry and the reserved-numbers table), CHANGELOG.md and docs/PROJECT_STATE.md.
- Builder fixes; auditor reviews only. Auditor wrote reviews docs/agents/reviews/P4-00.md, docs/agents/reviews/P4-00-round2.md, docs/agents/reviews/P4-00-round3.md, and docs/agents/reviews/P4-00-round4.md.

## Done

| ID | Title | Merged in |
|---|---|---|
