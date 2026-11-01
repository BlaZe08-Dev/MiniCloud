Task: P4-08d fix round 1 (orchestrator prompt /tmp/p4-08d-fix1-prompt.txt, after the independent audit)
Branch: agent/claude/P4-08d
Work tip (last commit before this report): bbe8c50
Date: 2026-11-01

## Status per finding
- F-R1 (major): FIXED. The pending grace now ends after a cap on the repository clock. Executed: `TestCoordinator_AssignmentLoop_PendingGraceEndsAtCap` (control, never-ticked, failed-sweep modes; three counted lapses, then the fallback claim by a second worker).
- F-R2 (A05, failed tick marks pending): FIXED by `TestCoordinator_AssignmentLoop_FailedSweepMarksGracePending`. Mutation D28 killed.
- F-R3 (A04, trigger buffer of one): FIXED by `TestCoordinator_AssignmentLoop_SubmitBurstAddsOneRound` (5 submits, exactly 2 passes). D29 killed.
- F-R4 (A03, one-minute ERROR window): FIXED by `TestCoordinator_AssignmentLoop_StaleHeartbeatWindowIsOneMinute` (rounds at 0 s, 50 s, 61 s). D30 killed.
- F-R5 (A02, real yield waits): FIXED by `TestAssignmentLoop_RealYield_Waits` (production function, via the new `YieldForTest` accessor). D31 killed.
- F-5c-1 (submit flood): no code change. ADR-0017 note 26 holds the audit's numbers (385 submits/s, 6.6 passes/s, poll p50 138 ms / p99 325 ms; paced 22 ms / 99 ms) and the decision. I did not re-measure.

## Cap chosen and why
Section 4.4 defines two grace lengths, the startup grace and the outage window (both default 30 s, configurable), and no third. The cap matches the cause: the startup grace for the pending state before the first successful sweeper tick, the outage window for the pending state after a failed tick. A further failure does not extend a pending state. The start is anchored on the first `repo.Now` read after the pending state began (lag at most one pass interval); the end instant travels in the new `AssignParams.GracePendingUntil` and `storage.LapseCounts` decides on repository time. No process-clock value enters the decision. Worst case to fallback (FS-19): `L x (window + pass interval) + cap + one pass interval` (the anchor lag); with defaults 3 x 6 s + 30 s + 1 s = 49 s (ADR-0017 note 25, corrected in fix round 2).

## Predicted and real counts (predictions written before the first gate run)
| Gate | Predicted | Real |
|---|---|---|
| gofmt / vet / build / test-docs | clean / 0 / 0 / rc 0 | clean / rc 0 / rc 0 / rc 0 |
| unset suite, `-race` | 349/84/0 (343 + 6 new tests) | 349/84/0, 0 DATA RACE |
| `make test-security` | 54/0/0 | 54/0/0 (top-level), 0 DATA RACE |
| `make test-db`, 3 runs, alone | 155/0/0 (149 + 6) | 154/0/0 in all three, 0 DATA RACE |
| race, URL set: coordinator, storage, cmd | 234/0/0 | 234/0/0, 0 DATA RACE |
The `make test-db` prediction was wrong by one: that target runs `./internal/coordinator/...` but not `./internal/storage/...`, so `TestLapseCounts_PendingEndsAtUntil` (storage package) is not in it. 149 + 5 coordinator tests = 154.

## New mutation rows (D25 to D33, 33 of 33 killed; index and logs in docs/agents/handoffs/P4-08d-evidence/)
D25 cap removed; D26 cap +5 s; D27 cap -5 s; D28 failed-sweep pending removed (A05); D29 trigger buffer 64 (A04); D30 ERROR window 50 s (A03); D31 real yield no wait (A02); D32 cap boundary inclusive; D33 cap by cause swapped. D32 and D33 are mine, beyond the four requested. Each ran red then green in `~/mc-mut-claude` at 62b1c38 (worktree removed). Deviation: these ran the whole `./internal/coordinator` and `./internal/storage` suites without `-race` and without a database URL, not a single named test; the index says so.

## Deviations to disclose
- `TestCoordinator_AssignmentLoop_GracePendingUntilFirstSweep` changed: it used startup grace 0, and with a cap of 0 the pending state ends at once. It now uses 10 s; its last assertion is that the pass is no longer pending after the first tick. The "a lapse counts again when no grace applies" claim moved to the control mode of the cap test. This follows from note 25; no bound was loosened, but it is a changed existing test.
- The cap anchor is the first pass or poll after the pending state began, not the exact start.
- Review branch not modified. `~/MiniCloud` not touched. No docker command run.

## Diff stat (b5ac949..bbe8c50)
- Fix commit bbcce12 (production): 5 files, internal/coordinator and internal/storage/assignment.go.
- Test commit 62b1c38 and docs commit bbe8c50: ADR notes 25 and 26, mutation index, handoff, TASKS row (status only), 9 mutation diffs and 18 logs.
- Total: 41 files, +700/-30; without the evidence directory 12 files, +336/-29.
- PROJECT_STATE.md not edited. Allowlist diff against origin/main: empty. No line numbers in the added docs (grep empty).

## Tip
New tip is the commit after this report (see `git log -1` on origin/agent/claude/P4-08d).
