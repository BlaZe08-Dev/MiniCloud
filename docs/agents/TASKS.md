# Task board

Owner merges. Orchestrator writes specs. One builder per task. A file owned by an
active task may not be edited by any other task.

Builder rule (AGENTS.md section 7): Claude Code builds complex and hard tasks; Gemini
builds the rest. The other agent audits.

Report file rule (AGENTS.md section 8): Every agent, before its final chat message, writes its final report to a new file `reports/<agent>/<TASK-ID>.md` at the repo root, commits it alone (`docs(report): ...`), pushes, then sends the final message.

## Reserved shared touch points

| Resource | Held by | Notes |
|---|---|---|
| Next migration number | V2-0b (000006) | `000006_worker_executes_concurrently.sql` is taken by V2-0b (ADR-0020, branch `agent/claude/V2-0b`). Next free migration number after it: 000007, expected taker V2-1 (node and capability model) |
| `go.mod` / `go.sum` | none | |
| `internal/domain` | none | |
| `internal/storage/storagetest` | none | Shared conformance suite |
| `docs/security/findings.md` | none | Released; numbers proposed in handoffs are provisional and `docs/security/findings.md` is the authority |

## Active

| ID | Title | Builder | Auditor | Branch | Status |
|---|---|---|---|---|---|
| PV-01 | Post-V1 documentation reconciliation and design freeze (docs only) | Claude Code | owner review (AGENTS.md change) | agent/claude/PV-01 | complete, merged in PR #55 |
| PV-02 | Record the proposed placement, cross-platform and AI-noise designs (docs only; every statement labelled Decided or Proposed) | Claude Code | owner review | agent/claude/PV-02 | complete on branch, awaiting owner merge |
| O1-check | Investigation: HTML escaping payload expansion (SEC-067) | Gemini | orchestrator review | agent/gemini/O1-check | complete |
| V1x-02 | Fix SEC-067: stop HTML escaping inflating poll payloads over worker limit | Claude Code | Gemini (read-only) | agent/claude/V1x-02 | merged in PR #60; audit APPROVE, archived in PR #61 (findings F-V1x02-1 and F-V1x02-2, both minor); SEC-067 Fixed; handoff `docs/agents/handoffs/V1x-02.md` |
| V2-0 | ADR and implementation: worker concurrency semantics (advertised slots equal real concurrency) | Claude Code | to be assigned | agent/claude/V2-0 | audited, fix round 1 (ADR-0019 Accepted; audit APPROVE WITH FIXES on agent/claude/V2-0-audit) |
| V2-0b | Round-robin uses every free slot: remove `domain.EffectiveWorkerSlots`, amend ADR-0017 | Claude Code | Opus (independent session) | agent/claude/V2-0b | built, awaiting audit (ADR-0020 Accepted; Stage B on the branch, handoff `docs/agents/handoffs/V2-0b.md`); before V2-1; must measure the `round_robin` assignment-pass overhead (V2-0 audit O-1: about 6 s slower than serial `fifo_pull` for 8 x 5 s jobs) |
| V2-1 | Node and capability model (D3) and migration 000007 | Claude Code | to be assigned | not started | planned, after V2-0; needs the V2-1 ADR with OD-19 answered |
| P4-19 | Docs part 1: security and ADR records (ADR-0017 status, findings register, test and fault plans, roadmap, V1 gate results) | Claude Code | to be assigned | agent/claude/P4-19 | merged in PR #51 (status corrected by PV-01) |
| P4-21 | Docs part 2 from the P4-18 audit | Gemini | to be assigned | agent/gemini/P4-21 | complete, merged in PR #49 |
| P4-15 | Register findings SEC-063..065 and correct P4-14 result delivery estimate | Gemini | Claude Code | agent/gemini/P4-15 | complete, merged in PR #43 |
| P4-16 | Fix SEC-062: older env helpers reject a set bad value at startup | Claude Code | to be assigned | agent/claude/P4-16 | merged in PR #46 (status corrected by PV-01) |
| P4-14 | Fix backoff overflow (finding F-5c2-1) | Gemini | Claude Code | agent/gemini/P4-14 | merged in PR #42 (status corrected by PV-01) |
| P4-12 | Friendlier make test-security guard | Gemini | Claude Code | agent/gemini/P4-12 | merged in PR #39 (status corrected by PV-01) |
| P4-11 | Register follow-ups and fix stale statuses | Gemini | Claude Code | agent/gemini/P4-11 | merged in PR #37 |
| P4-10 | Worker response body limit and URL path escaping (SEC-060, SEC-061) | Gemini | Claude Code | agent/gemini/P4-10 | merged in PR #36 |
| P4-09 | Worker execution timeout clamping (SEC-012) | Gemini | Claude Code | agent/gemini/P4-09 | merged in PR #34 |
| P4-08b | ADR-0017 slice 5a: domain, pure policy and in-memory parity (no database) | Claude Code | Gemini | agent/claude/P4-08b | merged in PR #35 |
| P4-08a | ADR-0017 assignment-based round-robin (design) | Claude Code | Orchestrator review | agent/claude/P4-08a | merged in PR #33 |
| P4-07 | CLI output sanitization (SEC-048 / F-4e-18) | Gemini | Claude Code | agent/gemini/P4-07 | merged in PR #32 (status corrected by PV-01) |
| P4-08d | ADR-0017 slice 5c, part 1: coordinator loop, grace, binary wiring | Claude Code | Claude Code (independent session) | agent/claude/P4-08d | merged in PR #40 (status corrected by PV-01) |
| P4-08e | ADR-0017 slice 5c, part 2: API fields (poll `policy` and `fallback_reason`, job view assignment fields) and worker timing (INV-17); fault and abuse suites move to P4-08f | Claude Code | to be assigned | agent/claude/P4-08e | merged in PR #41 (status corrected by PV-01) |
| P4-08f | ADR-0017 slice 5c, part 3: the fault, abuse and property suites, measurements, worker test tidy-ups | Claude Code | to be assigned | agent/claude/P4-08f | merged in PR #44 (status corrected by PV-01) |
| P4-08g | Fix round for the P4-08f audit: tests that kill the surviving mutants, property test and rig tightening, Makefile `-p 1`, ADR-0017 corrections | Claude Code | to be assigned | agent/claude/P4-08g | merged in PR #45 (status corrected by PV-01) |
| P4-17 | Flip the default dispatch policy to `round_robin` (ADR-0017 Q1) | Claude Code | to be assigned | agent/claude/P4-17 | complete, merged in PR #47 |
| P4-20 | Fix the test-outage LeakScan poll for the round_robin default (bounded retry, default policy kept) | Claude Code | to be assigned | agent/claude/P4-20 | complete, merged in PR #48 |
| P4-22 | SEC-066: stop raw database errors (host, port, user, database, SQL) reaching coordinator logs | Claude Code | to be assigned | agent/claude/P4-22 | complete, merged in PR #50 |
| P4-08c | ADR-0017 slice 5b: migration 000005 and the PostgreSQL assignment repository | Claude Code | Gemini | agent/claude/P4-08c | merged in PR #38 at 3b12ae5 |
| P4-06b | Phase 4 docs pass (findings SEC-031..058, review records, protocol/failure-model) | Gemini | Claude Code | agent/gemini/P4-06b | merged in PR #31 |

### PV-01 — Post-V1 documentation reconciliation and design freeze
- Goal: apply sections 3 to 5 of the post-V1 integration spec (orchestrator proposal, 2026-11-15, owner decisions D1 to D3): add `docs/post-v1/` (13 files, corrected), ADR-0018 with the ADR-0004 amendment, the trust model (D2) and the device model (D3); amendment notes in ADR-0004 and ADR-0008; scoped AGENTS.md rule, context map, precedence list and three standing rules; README, PROJECT_STATE top section, roadmap V2/V3/V4 table, CHANGELOG reconciliation, architecture.md post-V1 section; CLAUDE.md and GEMINI.md navigation; this board (next migration 000006, stale rows closed against merged PRs).
- Builder: Claude Code. Auditor: owner review (the AGENTS.md change needs the owner). Branch: `agent/claude/PV-01`. Status: complete, merged in PR #55.
- Owned files: `docs/post-v1/**`, `docs/adr/0018-ai-assisted-workflow-dispatch.md`, `docs/adr/0004-version-boundaries-and-compatibility.md` (amendment note only), `docs/adr/0008-dispatch-transport-queue-interface.md` (note only), `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `README.md`, `docs/PROJECT_STATE.md` (top section and milestone line only), `docs/roadmap.md`, `docs/architecture.md`, `CHANGELOG.md`, `docs/agents/TASKS.md`, `docs/agents/handoffs/PV-01.md`, `reports/claude/PV-01.md`.
- Out of scope: any `.go` file, migrations, `go.mod`/`go.sum`, API routes, tests, `tests/docs/citation_allowlist.txt` (unless `make test-docs` needs it), `docs/security/**`, historical handoffs and reviews, running docker or `make test-outage`. No implementation task is started.

### PV-02 — Record proposed placement, cross-platform and AI-noise designs
- Goal: record the owner's three questions of 2026-11-17 as labelled design text so no future builder mistakes them for implemented or decided features: V2-3 placement algorithm (filter, score, round-robin tie-break), heterogeneous devices support status, and V4 AI noise controls. Open decisions OD-21 to OD-25 added, none answered.
- Builder: Claude Code. Auditor: owner review. Branch: `agent/claude/PV-02`. Status: complete on branch, awaiting owner merge.
- Owned files: `docs/post-v1/NODE_CAPABILITIES.md`, `docs/post-v1/ARCHITECTURE.md`, `docs/post-v1/INTELLIGENCE_DISPATCHER.md`, `docs/post-v1/SECURITY.md`, `docs/post-v1/OPEN_DECISIONS.md`, `docs/post-v1/ROADMAP.md`, `docs/post-v1/IMPLEMENTATION_PLAN.md`, `docs/roadmap.md`, `docs/agents/TASKS.md`, `docs/PROJECT_STATE.md` (one line), `docs/agents/handoffs/PV-02.md`, `reports/claude/PV-02.md`.
- Out of scope: any `.go`, `.sql`, `go.mod`, `go.sum`, `tests/` or `tests/docs/citation_allowlist.txt` change; `AGENTS.md`, any ADR, `docs/security/findings.md`; a new ADR (the V2-3 ADR is written later); database tests and `make test-outage`.

### O1-check — Investigation: HTML escaping payload expansion (SEC-067)
- Goal: determine whether HTML escaping in coordinator API response writer inflates valid payloads under 64 KiB over the worker execution limit, causing false-positive permanent failures.
- Builder: Gemini. Auditor: orchestrator review. Branch: `agent/gemini/O1-check`. Status: complete.
- Owned files: `docs/agents/reviews/O1-check-evidence/**`, `reports/gemini/O1-check.md`, `docs/security/findings.md`, `docs/agents/TASKS.md`.
- Out of scope: any `.go` file, migrations, `go.mod`/`go.sum`, `tests/docs/citation_allowlist.txt`, worker, executor, storage.

### V1x-02 — Fix SEC-067: stop HTML escaping inflating poll payloads over worker limit
- Goal: Fix SEC-067 by configuring coordinator HTTP response encoder with `SetEscapeHTML(false)` so valid payloads containing HTML characters preserve exact wire byte fidelity; ensure `X-Content-Type-Options: nosniff` header is present.
- Builder: Claude Code. Auditor: Gemini (read-only). Status: merged in PR #60 (branch `agent/claude/V1x-02`); audit APPROVE (review `docs/agents/reviews/V1x-02.md`), archived in PR #61; findings F-V1x02-1 and F-V1x02-2 are minor; SEC-067 is Fixed.
- Owned files: `internal/api/handlers.go`, `internal/api/*_test.go`, `docs/**`.
- Out of scope: worker, executor, storage, migrations.
- Acceptance criteria:
  1. A regression test shows the poll response bytes equal the submitted payload for `<`, `>`, `&`, and U+2028/2029, and fails when the fix is reverted (mutation proof).
  2. A header test verifies `X-Content-Type-Options: nosniff` on API responses.
  3. Worker and executor packages remain completely unchanged.
  4. An old worker still completes jobs successfully.
  5. Baseline test and benchmark counts are not lower.
  6. Auditor Gemini is read-only.

### V2-0 — Worker concurrency semantics (placeholder; full spec to be written by the orchestrator)
- Goal: decide and document slot semantics (real concurrent execution with supervision, or advertise what is actually executed), ADR first, then implementation. The worker today runs one task at a time while registration and heartbeat report `available_slots` equal to `TotalSlots` (confirmed by code reading, `internal/worker/client.go` func `Run` and func `register`; spec section 2). Exit: advertised capacity equals real concurrency; race tests; V1 worker protocol still compatible.
- Builder: Claude Code. Auditor: to be assigned. Branch: `agent/claude/V2-0`. Status: audited, fix round 1 (audit: review V2-0 on branch `agent/claude/V2-0-audit`, APPROVE WITH FIXES; findings A-1 to A-4 fixed on this branch). ADR-0019 (`docs/adr/0019-worker-concurrency-semantics.md`) approved by the orchestrator with answers Q1 to Q4 and set to Accepted at the end of Stage B.
- Owned files (Stage B): `internal/worker/*`, `internal/config/config.go` (slots upper bound), docs (`README.md`, `CHANGELOG.md`, `docs/PROJECT_STATE.md`, `docs/post-v1/CURRENT_BASELINE.md`, `docs/roadmap.md`, `docs/adr/0019-worker-concurrency-semantics.md`, `docs/adr/0017-assignment-round-robin.md` (Q5 note only), this board, `docs/agents/handoffs/V2-0.md`, `docs/agents/handoffs/V2-0-evidence/**`, `reports/claude/V2-0*.md`). A test under `tests/` only if it truly assumes a serial worker, with evidence first.
- Out of scope: coordinator, storage, migrations, API routes, `go.mod`/`go.sum`, `tests/docs/citation_allowlist.txt`, running `make test-outage`.

### V2-0b — Round-robin uses every free slot (placeholder; full spec to be written by the orchestrator)
- Goal: remove `domain.EffectiveWorkerSlots` so the ADR-0017 assignment pass reserves work up to `total_slots - running - reserved` per worker (ADR-0017 Section 2.4 and Q5, ADR-0019 Section 2.2 and Q1); amend ADR-0017; coordinator, storage (in-memory and PostgreSQL) and tests. Until it lands, parallelism under `round_robin` stays one job per worker.
- Builder: Claude Code. Auditor: Opus (independent session). Branch: `agent/claude/V2-0b`. Status: built, awaiting audit. ADR-0020 (`docs/adr/0020-round-robin-real-slots.md`) approved by the orchestrator (decisions Q1 to Q8) and set to Accepted at the end of Stage B; handoff `docs/agents/handoffs/V2-0b.md`, evidence `docs/agents/handoffs/V2-0b-evidence/`. Migration 000006. Before V2-1.
- Also measure the `round_robin` assignment-pass overhead: in the V2-0 audit (O-1) 8 `sleep` jobs of 5 s on one worker took 46.60 s under `round_robin` against 40.41 s under serial `fifo_pull` with one slot (about 6 s slower, attributed to the assignment pass interval, inferred).

### V1.0.x maintenance candidates added by V2-0 (not started)
- O1 (ADR-0019 Section 7, inferred, not tested): `coordinator.WorkAssignment.Payload` is a `json.RawMessage` that keeps the HTML-escaped bytes the coordinator sends, so a legal payload with many `<`, `>` or `&` characters may reach the worker longer than 64 KiB and be rejected by `executor.Registry.Execute` before decoding. Needs a reproduction test first (break before fix).
- `TestLogSafe_AssignmentPassRefreshFailed_LogsClassOnly` (coordinator, pre-existing flake seen in `make test-db` during V2-0): its leak list contains "6543", which can match a log timestamp (for example `02:31:39.463654363`). Fix: use a sentinel that cannot appear in a timestamp.
- `TestConcurrency_RenewalPanicTreatsLeaseAsLost` (worker, pre-existing flake seen in V2-0b unset runs and once on main 26b440a under parallel load; evidence `docs/agents/handoffs/V2-0b-evidence/preexisting-flake-main.txt`): times out waiting for j1's renewal; inferred cause: `BlockUntil(1, ...)` can match another fake-clock timer.
- [Proposed] Candidate from V2-0b fix round 1 (ADR-0020 Section 2.3 amendment): map the refused fallback claim (`ErrWorkerNoCapacity`, now 503 `NO_WORKERS_AVAILABLE`) to 204 in a later patch, so the worker idles 200 ms instead of backing off up to `MaxPollBackoff` (3 s).
- [Proposed] Candidate from V2-0b (ADR-0020 Q8, not a slice): trigger an assignment pass when a full worker frees a slot, to cut the per-wave `round_robin` overhead measured in `docs/agents/handoffs/V2-0b.md`.
- F-V1x02-1 (V1x-02 audit, comment only): the `MaxPollResponseBytes` comment in `internal/worker/client.go` is stale after SEC-067 (poll payloads are no longer HTML-escaped, so the escaping headroom it describes no longer applies); update the comment.

### V2-1 — Node and capability model (placeholder; full spec to be written by the orchestrator)
- Goal: device model D3 per `docs/post-v1/NODE_CAPABILITIES.md`: additive forward-only migration 000007 (000006 is taken by V2-0b) with `nodes` (identity only), `node_hardware`, `node_telemetry` and nullable `workers.node_id`; `node_tools` is designed in the V2-1 ADR but created in V4-3. Needs the V2-1 ADR with OD-08, OD-09 and OD-19 answered.
- Builder: Claude Code. Auditor: to be assigned. Status: planned, after V2-0.

### P4-21 — Docs part 2 from the P4-18 audit
- Goal: Fix docs drift identified in P4-18 audit: README (configuration table, env error handling, dispatch statement, make targets, SEC-012 timeout clamping), phase4-plan.md (C21 assignment pass vs lease sweeper), schema.md (migrations 000004 & 000005 merged), field-limits.md (chk_attempts_last_error_size and 000004/000005 constraints), roadmap.md & state-machine.md (default policy wording), architecture.md (Scheduler row and Slice 3d default policy), ADR-0003 & ADR-0011 (amendment line for default policy), and .env.example (commented-out coordinator variables).
- Builder: Gemini. Auditor: to be assigned. Branch: `agent/gemini/P4-21`. Status: complete, merged in PR #49.
- Owned files: `README.md`, `docs/phase4-plan.md`, `docs/schema.md`, `docs/security/field-limits.md`, `docs/roadmap.md`, `docs/state-machine.md`, `docs/architecture.md`, `docs/adr/0003-pull-based-worker-protocol.md`, `docs/adr/0011-postgres-dispatch-and-recovery.md`, `.env.example`, `docs/agents/TASKS.md`, `reports/gemini/P4-21.md`.
- Out of scope: Go code files, database schema or migrations, allowlist modifications, `docs/PROJECT_STATE.md`, `docs/adr/0017-assignment-round-robin.md`, `docs/security/findings.md`, `docs/security/test-plan.md`, `docs/fault-injection-plan.md`.

### P4-15 — Register findings SEC-063..065 and correct P4-14 result delivery estimate
- Goal: Register SEC-063 (submit flood / unauthenticated API), SEC-064 (backoff shift overflow F-5c2-1), and SEC-065 (mixed clocks ADR-0017 note 14) in `docs/security/findings.md`; correct retry count estimate arithmetic for result delivery in `docs/agents/handoffs/P4-14.md`.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-15`. Status: complete, merged in PR #43.
- Owned files: `docs/security/findings.md`, `docs/agents/handoffs/P4-14.md`, `docs/agents/TASKS.md`, `reports/gemini/P4-15.md`.
- Out of scope: Go code files, `docs/PROJECT_STATE.md`, `docs/adr/0017-assignment-round-robin.md`, database schema or migrations.

### P4-14 — Fix backoff overflow (finding F-5c2-1)
- Goal: Fix integer shift overflow in `ComputeBackoffWithJitter` (`internal/worker/client.go`) for failure counts >= 64; ensure positive, bounded backoff for all failure counts (0..MaxInt) with unchanged growth; add table and property tests in `internal/worker/client_test.go`.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-14`. Status: merged in PR #42 (corrected by PV-01).
- Owned files: `internal/worker/client.go`, `internal/worker/client_test.go`, `docs/agents/handoffs/P4-14.md`, `docs/agents/handoffs/P4-14-evidence/**`, `reports/gemini/P4-14.md`, `docs/agents/TASKS.md`.
- Out of scope: coordinator API changes, database schema or migrations, allowlist modifications, PROJECT_STATE.md.

### P4-12 — Friendlier make test-security guard (refuse shared database early)
- Goal: Add an early refusal guard (`tests/require-own-db.sh`) to `make test-security` when `MINICLOUD_TEST_DATABASE_URL` is unset or points to the shared database `minicloud_test`, printing a clear 5-line message explaining the own-database requirement and how to override with `ALLOW_SHARED_DB=1`.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-12`. Status: merged in PR #39 (corrected by PV-01).
- Owned files: `Makefile`, `tests/require-own-db.sh`, `docs/testing-postgres.md`, `docs/PROJECT_STATE.md`, `docs/agents/TASKS.md`, `docs/agents/handoffs/P4-12.md`, `reports/gemini/P4-12.md`.
- Out of scope: Go code modifications, schema migrations, allowlist edits, running docker or real security tests.

### P4-11 — Register follow-ups and fix stale statuses (P4-08a..P4-10, SEC-012, SEC-062)
- Goal: Register follow-ups and fix stale statuses; update `TASKS.md` and `PROJECT_STATE.md` with merged PRs #33-#36 (P4-08a, P4-09, P4-08b, P4-10); update `threat-model.md` for SEC-012 fix in Slice P4-09 (`ClampExecutionTimeout`); register SEC-062 in `findings.md` and `PROJECT_STATE.md` for older environment helpers silent fallback; import P4-08b 5a review records from `origin/agent/claude/P4-08b-review`; add orchestrator-verified gate entry for main `cb51adc` in `PROJECT_STATE.md`.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-11`. Status: merged in PR #37 (corrected by PV-01).
- Owned files: `docs/agents/TASKS.md`, `docs/PROJECT_STATE.md`, `docs/security/threat-model.md`, `docs/security/findings.md`, `docs/agents/reviews/P4-08b.md`, `reports/claude/P4-08b-review.md`, `docs/agents/reviews/P4-08b-evidence/**`, `docs/agents/handoffs/P4-11*`, `reports/gemini/P4-11.md`.
- Out of scope: production Go code, database schema or migrations, allowlist modifications.

### P4-10 — Worker response body limit and URL path escaping (SEC-060, SEC-061)
- Goal: Fix SEC-060 by bounding poll response reads in `internal/worker/client.go` func `pollWork` with `io.LimitReader` and named constant `MaxPollResponseBytes` (set above `executor.MaxPayloadSizeBytes` with headroom for envelope); fix SEC-061 by escaping `JobID` and `AttemptID` with `url.PathEscape` in func `renewOnce` and func `reportResult`; write unit tests `TestPollWork_OversizedResponseRejected`, `TestPollWork_MaxLegalAssignmentAccepted`, and `TestWorker_URLPathEscaping_RenewAndReport` in `internal/worker/hardening_test.go`; execute mutations M1-M4 with evidence; update `findings.md`, `PROJECT_STATE.md`, `TASKS.md`, and `protocol.md`.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-10`. Status: merged in PR #36.
- Owned files: `internal/worker/client.go`, `internal/worker/hardening_test.go`, `docs/security/findings.md`, `docs/PROJECT_STATE.md`, `docs/agents/TASKS.md`, `docs/protocol.md`, `docs/agents/handoffs/P4-10*`, `reports/gemini/P4-10.md`.
- Out of scope: coordinator API changes, database schema or migrations, allowlist modifications.

### P4-09 — Worker execution timeout clamping (SEC-012)
- Goal: Clamp coordinator-supplied `TimeoutSeconds` in `internal/worker/client.go` to `[1, 86400]` seconds (`MaxExecutionTimeoutSeconds = coordinator.MaxTimeoutSeconds`) with 64-bit integer multiplication overflow protection and warning logging; implement pure function `ClampExecutionTimeout`; write table-driven unit tests and end-to-end test `TestWorker_ExcessiveTimeoutClamped`; update SEC-012 to Confirmed, Fixed and register findings SEC-059..061.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-09`. Status: merged in PR #34.
- Owned files: `internal/worker/**`, `docs/security/findings.md`, `docs/PROJECT_STATE.md`, `docs/agents/TASKS.md`, `docs/protocol.md`, `docs/failure-model.md`, `docs/agents/handoffs/P4-09*`, `reports/gemini/P4-09.md`.
- Out of scope: coordinator API changes, database schema or migrations, allowlist modifications.

### P4-08d — ADR-0017 slice 5c, part 1 (coordinator loop, grace, binary wiring)
- Goal: the coordinator assignment loop of ADR-0017 Section 2.1 (timer, submit trigger, repeat rule and yield, INV-20, INV-21), decisions on the database clock only; the rate-limited stale heartbeat ERROR log (A2, Section 4.5); tests for the PostgreSQL refresh threshold and the poll-write throttle deferred by implementation note 9; grace wiring from the lease sweeper to the pass (INV-9, Section 4.4); binary wiring (P4-08c review F-R3): `cmd/coordinator` applies `SetAssignmentSettings` then `SetDispatchPolicy`, fails startup on either error, logs the effective policy and settings, and starts the loop only under round_robin (default stays `fifo_pull`); one validator for the A1 rules, with a cross-test between config and service.
- Builder: Claude Code. Auditor: Claude Code (an independent session). Branch: `agent/claude/P4-08d`. Status: merged in PR #40 (corrected by PV-01).
- Owned files: `internal/coordinator/assignment*.go`, `internal/coordinator/service.go`, `internal/coordinator/sweeper.go`, `internal/coordinator/export_assignment_test.go`, `cmd/coordinator/main.go`, `cmd/coordinator/main_test.go`, `internal/storage/assignment.go`, `internal/storage/inmemory_assignment.go`, `internal/storage/inmemory_assignment_test.go`, `internal/storage/storagetest/assignment.go`, `internal/storage/postgres/assignment.go`, `internal/storage/postgres/assignment_test.go`, `internal/storage/postgres/assignment_coordinator_test.go`, `docs/adr/0017-assignment-round-robin.md` (implementation notes 16 onward only), `docs/agents/handoffs/P4-08d*`, `docs/agents/handoffs/P4-08d-evidence/`, `reports/claude/P4-08d.md`, `docs/agents/TASKS.md`.
- Out of scope (part 2, P4-08e): API fields, worker timing constants (INV-17), the fault and abuse suites of Section 6.4, the 1,000-iteration property test, measurements, docs sync; any change to `fifo_pull` behaviour; the Q1 default flip.

### P4-08e — ADR-0017 slice 5c, part 2 (placeholder; builder, auditor, owned files and full spec to be written by the orchestrator)
- Goal: the rest of ADR-0017 Section 7 slice 5c: job view fields and the poll `policy` and `fallback_reason` fields, the worker constant extraction and `TestPollTiming_AcceptWindowExceedsBackoff` (planned test, INV-17), the fault and API abuse suites of Section 6.4 (including `TestAPI_Poll_Flood_ThrottlesFreshnessWrites` (planned test)), `TestProperty_AssignmentSubState_ModelConformance` at 1,000 iterations, measurements, and the docs sync.

### P4-08f — ADR-0017 slice 5c, part 3 (fault, abuse and property suites, measurements)
- Goal: every test ADR-0017 Section 6.4 plans for slice 5c, under `tests/faultinject` and `tests/abuse` so `make test-security` runs them; the 1,000-iteration sub-state property test; the Section 6 measurements (the P4-08d flood numbers are reused); the worker test tidy-ups from the P4-08d and P4-08e audits. No production code change.
- Builder: Claude Code. Auditor: to be assigned. Branch: `agent/claude/P4-08f`. Status: merged in PR #44 (corrected by PV-01).
- Owned files: `tests/faultinject/assignment_test.go`, `tests/abuse/assignment_test.go`, `tests/internal/sectest/rr.go`, `tests/internal/sectest/rig.go`, `tests/security-tests.list`, `internal/coordinator/assignment_test.go`, `internal/worker/export_test.go`, `internal/worker/client_test.go`, `internal/worker/poll_timing_test.go`, `docs/adr/0017-assignment-round-robin.md` (implementation notes 31 to 36 only), `docs/agents/handoffs/P4-08f.md`, `docs/agents/handoffs/P4-08f-evidence/**`, `reports/claude/P4-08f.md`, `docs/agents/TASKS.md`.

### P4-16 — fix SEC-062 (older env helpers fall back silently)
- Goal: `getIntEnv` and `getDurationEnv` (and two new positive variants) return an error naming the variable and the bad value for a set value that does not parse, overflows, or is non-positive where positive is required; unset or empty keeps the default. Defaults and variable names unchanged.
- Builder: Claude Code. Auditor: to be assigned. Branch: `agent/claude/P4-16`. Status: merged in PR #46 (corrected by PV-01).
- Owned files: `internal/config/config.go`, `internal/config/env_helpers_test.go`, `tests/integration/startup_env_test.go`, `docs/agents/handoffs/P4-16.md`, `docs/agents/handoffs/P4-16-evidence/**`, `reports/claude/P4-16.md`, `docs/agents/TASKS.md`.
- Out of scope: `docs/security/**`, `PROJECT_STATE.md`, README, ADRs (SEC-062 is flipped in the docs sync), the `lookup*EnvStrict` helpers.

### P4-08g — fix round for the P4-08f audit
- Goal: kill the P4-08f audit's surviving mutants (Q10, Q11, Q18) and the weak tests (property test INV-3 coverage, deep-backlog yield proof, FS-21 bound, `passUntilReserved`, flood lower bound, outage-recovery test); `-p 1` on the Makefile targets that run several database packages; ADR-0017 corrections (notes 31 to 36, Sections 6.2 and 6.4). No production code change.
- Builder: Claude Code. Auditor: to be assigned. Branch: `agent/claude/P4-08g`. Status: merged in PR #45 (corrected by PV-01).
- Owned files: `internal/storage/storagetest/assignment.go`, `internal/storage/inmemory_assignment_test.go`, `internal/storage/postgres/assignment_test.go`, `internal/coordinator/assignment_test.go`, `tests/faultinject/assignment_test.go`, `tests/abuse/assignment_test.go`, `tests/internal/sectest/rr.go`, `Makefile` (`-p 1` on `test` and `test-race`), `docs/adr/0017-assignment-round-robin.md` (notes 31 to 36 and Sections 6.2 and 6.4 only), `docs/agents/handoffs/P4-08g.md`, `docs/agents/handoffs/P4-08g-evidence/**`, `reports/claude/P4-08g.md`, `docs/agents/TASKS.md`.

### P4-17 — default dispatch policy flips to round_robin
- Goal: `MINICLOUD_DISPATCH_POLICY` unset or empty selects `round_robin`; `fifo_pull` stays selectable explicitly; an invalid value is still a startup error (ADR-0017 Q1). Tests, scripts and docs that assumed `fifo_pull` as the default state their policy or change their expectation.
- Builder: Claude Code. Auditor: to be assigned. Branch: `agent/claude/P4-17`. Status: complete, merged in PR #47.
- Owned files: `internal/config/config.go`, `internal/config/assignment_config_test.go`, `cmd/coordinator/main_test.go`, `tests/internal/sectest/proc.go`, `scripts/demo.sh`, `docs/adr/0017-assignment-round-robin.md` (note 37 and the three default wordings only), `docs/agents/handoffs/P4-17.md`, `docs/agents/handoffs/P4-17-evidence/`, `reports/claude/P4-17.md`.
- Out of scope: README, `docs/PROJECT_STATE.md`, `docs/security/` and every other doc that says `fifo_pull` is the default (listed in the handoff for the docs sync).

### P4-20 — LeakScan worker poll retries under the default policy
- Goal: `TestSecurity_PartD_LeakScan` stays on the default policy and its worker poll retries a bounded number of times until the assignment arrives. No production code changes.
- Builder: Claude Code. Auditor: to be assigned. Branch: `agent/claude/P4-20`. Status: complete, merged in PR #48.
- Owned files: `tests/integration/part_d_security_test.go`, `tests/integration/leakscan_poll_test.go`, `docs/agents/handoffs/P4-20.md`, `docs/agents/handoffs/P4-20-evidence/`, `reports/claude/P4-20.md`.
- Out of scope: production code, `make test-outage` (the owner runs it), README, `docs/PROJECT_STATE.md`, `docs/security/`, the ADR.

### P4-22 — SEC-066: no raw database error text in coordinator logs
- Goal: every coordinator log site that can print a storage, pool or network error logs only a fixed error class (`internal/logsafe`), never error text. Error values, HTTP statuses, grace counting and retry classification unchanged. Built on `agent/claude/P4-20` (owner decision; P4-20 not yet merged).
- Builder: Claude Code. Auditor: to be assigned. Branch: `agent/claude/P4-22`. Status: complete, merged in PR #50.
- Owned files: `internal/logsafe/**`, `internal/coordinator/assignment_loop.go`, `internal/coordinator/assignment.go`, `internal/coordinator/sweeper.go` (log lines only), `internal/coordinator/logsafe_sites_test.go`, `internal/api/handlers.go` (log lines and the removed regex sanitizer only), `internal/api/logsafe_sites_test.go`, `cmd/coordinator/main.go` (startup log lines only), `tests/faultinject/logleak_test.go`, `tests/security-tests.list`, `docs/agents/handoffs/P4-22.md`, `docs/agents/handoffs/P4-22-evidence/`, `reports/claude/P4-22.md`, `docs/agents/TASKS.md` (this row).
- Out of scope: `docs/security/` (SEC-066 is registered by P4-19), README, `docs/PROJECT_STATE.md`, the ADR, `make test-outage` (the owner runs it), `cmd/migrate` and worker log lines (open questions in the handoff).

### P4-08c — ADR-0017 slice 5b (migration 000005 and the PostgreSQL assignment repository)
- Goal: ADR-0017 Section 7 slice 5b: migration `000005_assignment_round_robin.sql`, the PostgreSQL implementation of `storage.AssignmentRepository` (advisory try-lock, revoke, per-type reserve with the worker row lock and re-check, claim with own arm then fallback arms, cancel and dispatch clearing, pass heartbeat on the database clock), the 5a assignment cases moved into the shared `storagetest` runner, and the PostgreSQL-only invariant tests (INV-1, INV-2b, INV-5, INV-10b, INV-13, INV-14, INV-19, INV-22, INV-24).
- Builder: Claude Code. Auditor: Gemini. Branch: `agent/claude/P4-08c`. Status: merged in PR #38 at 3b12ae5.
- Owned files: `internal/storage/postgres/migrations/000005_assignment_round_robin.sql`, `internal/storage/postgres/assignment*.go`, `internal/storage/postgres/migration_000004_test.go`, `internal/storage/postgres/migration_000005_test.go`, `internal/storage/postgres/repository.go`, `assignment_5a_skips_test.go` (PostgreSQL storage package; deleted in slice 5b by commit b67db2a) (deleted), `internal/storage/storagetest/assignment.go`, `internal/storage/assignment_staleness.go`, `internal/storage/inmemory.go`, `internal/storage/inmemory_assignment_test.go`, `internal/coordinator/assignment.go`, `docs/adr/0017-assignment-round-robin.md` (status note only), `docs/agents/handoffs/P4-08c*`, `docs/agents/handoffs/P4-08c-evidence/`, `reports/claude/P4-08c.md`, `docs/agents/TASKS.md`.
- Out of scope (slice 5c): the coordinator assignment loop, grace (INV-9), API field docs, worker changes, any change to `fifo_pull` behaviour.

### P4-08b — ADR-0017 slice 5a (domain, pure policy and in-memory parity)
- Goal: ADR-0017 Section 7 slice 5a only: assignment domain fields and events, the pure round-robin rule with per-type cursors, the in-memory assignment pass, round-robin claim (own reservation, then lapse and age arms), the poll-time refresh in memory, policy selection with `fifo_pull` as the default, and config validation including A1. No migration, no PostgreSQL code.
- Builder: Claude Code. Auditor: Gemini. Branch: `agent/claude/P4-08b`. Status: merged in PR #35.
- Owned files: `internal/domain/assignment.go`, `internal/domain/job.go`, `internal/domain/worker.go`, `internal/domain/events.go`, `internal/scheduler/round_robin_rule*.go`, `internal/storage/assignment.go`, `internal/storage/inmemory*.go`, `assignment_5a_skips_test.go` (PostgreSQL storage package; deleted in slice 5b by commit b67db2a), `internal/config/config.go`, `internal/config/assignment_config_test.go`, `internal/coordinator/service.go`, `internal/coordinator/assignment*.go`, `internal/coordinator/export_assignment_test.go`, `docs/adr/0017-assignment-round-robin.md` (A1, A2), `docs/agents/handoffs/P4-08b*`, `docs/agents/handoffs/P4-08b-evidence/`, `reports/claude/P4-08b.md`, `docs/PROJECT_STATE.md`, `docs/agents/TASKS.md`.

### P4-08a — ADR-0017 assignment-based round-robin (design)
- Goal: Design assignment-based round-robin dispatch (Candidate A) in ADR-0017.
- Builder: Claude Code. Auditor: Orchestrator review. Branch: `agent/claude/P4-08a`. Status: merged in PR #33.
- Owned files: `docs/adr/0017-assignment-round-robin.md`, `reports/claude/P4-08a*.md`.

### P4-07 — CLI output sanitization (SEC-048 / F-4e-18)
- Goal: Sanitize worker- and task-supplied strings in `cmd/mc` before terminal printing; neutralize escape sequences, control characters, and lone carriage returns; un-skip `TestAbuse_MCOutput_HostileErrorText`; update SEC-048 to Confirmed, Fixed and SEC-031 to Documented.
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-07`. Status: merged in PR #32 (corrected by PV-01).
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
- Builder: Gemini. Auditor: Claude Code. Branch: `agent/gemini/P4-05`. Status: merged in PR #28 (corrected by PV-01).
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
| P4-08a | ADR-0017 assignment-based round-robin (design) | PR #33, merge commit 74eb433 |
| P4-09 | Worker execution timeout clamping (SEC-012) | PR #34, merge commit fc84042 |
| P4-08b | ADR-0017 slice 5a: domain, pure policy and in-memory parity (no database) | PR #35, merge commit 2ca971b |
| P4-10 | Worker response body limit and URL path escaping (SEC-060, SEC-061) | PR #36, merge commit cb51adc |
| P4-11 | Register follow-ups and fix stale statuses | PR #37, merge commit f522de7 |
| P4-08c | ADR-0017 slice 5b: migration 000005 and the PostgreSQL assignment repository | PR #38, merge commit 3b12ae5 |
| P4-06b | Phase 4 docs pass (findings SEC-031..058, review records, protocol/failure-model) | PR #31, merge commit e93f484 |
| P4-07 | CLI output sanitization (SEC-048 / F-4e-18) | PR #32, merge commit 79bf22a |
| P4-12 | Friendlier make test-security guard | PR #39, merge commit a112e17 |
| P4-08d | ADR-0017 slice 5c, part 1: coordinator loop, grace, binary wiring | PR #40, merge commit 4b45f0d |
| P4-08e | ADR-0017 slice 5c, part 2: API fields and worker timing | PR #41, merge commit cbf7849 |
| P4-14 | Fix backoff overflow (finding F-5c2-1) | PR #42, merge commit 2b87eb0 |
| P4-15 | Register findings SEC-063..065 | PR #43, merge commit 7ba68ab |
| P4-08f | ADR-0017 slice 5c, part 3: fault, abuse and property suites | PR #44, merge commit 6194127 |
| P4-08g | Fix round for the P4-08f audit | PR #45, merge commit a3b60d0 |
| P4-16 | Fix SEC-062 (older env helpers) | PR #46, merge commit 29b372e |
| P4-17 | Default dispatch policy flips to round_robin | PR #47, merge commit c97b817 |
| P4-20 | LeakScan worker poll retries under the default policy | PR #48, merge commit f4c6f5b |
| P4-21 | Docs part 2 from the P4-18 audit | PR #49, merge commit 9c00672 |
| P4-22 | SEC-066: no raw database error text in coordinator logs | PR #50, merge commit d7a397c |
| P4-19 | Docs part 1: security and ADR records | PR #51, merge commit 2111407 |
| (owner) | PROJECT_STATE update for the V1 gate (branch `docs/project-state-v1`) | PR #52, merge commit 05b47c2 |
| (owner) | PROJECT_STATE citation-checker fix (branch `docs/project-state-fix`) | PR #53, merge commit 4df57d7 |
| P4-23 | Archive unmerged audit records and review index | PR #54, merge commit 166c719 (tagged `v1.0.0`) |

Rows from PR #31 onward were added by PV-01 from `git log --merges` on `main` at 166c719.

## Follow-ups

- **F-4c-a-A1 (Makefile `test-db` false-green risk; Resolved in P4-T1, PR #26, merge commit 9fbbbcf)**: Resolved by adding a `-list` pattern check to `Makefile` `test-db` target.
- **F-4c-a-A2 (`startBackground` top-level invocation test)**: The top-level invocation of `startBackground` in `cmd/coordinator/main.go` `main()` is not exercised by automated tests. Fix: add a smoke test in `cmd/coordinator/main_test.go` that briefly boots the binary with `MINICLOUD_STORAGE_DRIVER=memory` and verifies background loops start and shut down cleanly.
- **Review records citation checker scoping (Resolved in P4-T2, PR #27, merge commit 1e20143)**: Citation checker Rule A link check was scoped to live documents in P4-T2, allowing historical review records to be treated as immutable. Review records for P0-03 round 2 and round 3 (`P0-03-round2.md`, `P0-03-round2-evidence.txt`, `P0-03-round3.md`, `P0-03-round3-evidence.txt`) were imported byte-identical in P4-05.
- **F-4e-18 (CLI ANSI terminal escape sequence sanitization; Resolved in P4-07, PR #32, SEC-048 Confirmed, Fixed; status corrected by PV-01)**: CLI `cmd/mc` does not strip or sanitize ANSI escape sequences from task outputs or error strings before printing to stdout/stderr. Risk: untrusted tasks could emit terminal escape sequences (cursor relocation, clear screen, OSC title setting) when output is viewed in an interactive terminal. Fix: implement terminal sanitization in `cmd/mc` to escape or strip non-printable/control sequences.
- **F-4e-17 (Rate limiting and connection bounding on unauthenticated endpoints; OPEN)**: All HTTP endpoints currently lack application-level rate limiting or connection bounding. Risk: an aggressive client or network flooder could starve connection pool or saturate coordinator memory. Mitigated in V1 by default localhost binding (`127.0.0.1:8080`) under SEC-004 accepted risk. Planned for V2/V3 hardening.
- **`make test-security` shared database detection (Resolved in P4-12)**: `make test-security` prints a 5-line explanatory error refusing execution (exit 2) when run against the shared database (`minicloud_test`) or when unset. Override with `ALLOW_SHARED_DB=1` to run against the shared database.
- **Per-worktree test database note**: `make test-security` must be run from a worktree configured with its own `minicloud_<agent>_test` database (e.g. `minicloud_gemini_test` or `minicloud_claude_test`). `make test-db` is the appropriate suite for the shared database.
- **Next Slices (Post-Phase 4)**: Phase 4 core is complete. Next milestone is the Post-Phase 4 Assignment-Based Round-Robin Slice (Candidate A pre-assignment with bounded accept window) required before V1 release tagging.
- **Post-V1 (PV-01)**: V1 is released (`v1.0.0` on 166c719). The next work is V2-0 (worker concurrency semantics), then V2-1 (node and capability model, migration 000007 since V2-0b took 000006); sequence in `docs/roadmap.md` and `docs/post-v1/ROADMAP.md`.
