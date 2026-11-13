# P4-08g mutation index

Throwaway worktree `~/mc-mut-claude` (removed at the end), `.env` copied, database `minicloud_claude_test`, `-p 1`. Each mutant is single-site, applied alone, run red, restored with `git checkout`, run green. `mut/<ID>.diff` is the mutant, `mut/<ID>-red.log` and `-green.log` the outputs (first line is the command). Q10, Q11, Q18, C01 and H01 are the P4-08f audit's diffs, unchanged (the audit evidence on the branch `origin/agent/claude/P4-08f-review`). "Before" rows are in `mut/before/` (the same mutant run against the P4-08f suite at `origin/main`).

| ID | Mutant | Killing test (red) | Green after restore |
|---|---|---|---|
| Q11 | PostgreSQL age arm drops the pass-silence term (INV-16) | `TestConformance_ClaimAgeArm_NeedsSilentPass` (PostgreSQL) | pass |
| Q10 | PostgreSQL fallback arm ignores other workers' live reservations (INV-3) | `TestConformance_ClaimFallback_LiveReservationOfOther` (PostgreSQL) | pass |
| Q18 | PostgreSQL revoke ignores BatchLimit (`LIMIT $1 + 1000`) | `TestConformance_AssignRevoke_BatchLimit` (PostgreSQL) | pass |
| C01 | `assignment_loop.go` holds `s.mu` through the yield (the ADR's INV-20 mutant), both backends | `TestFault_DeepBacklog_PollsNotStarved` ("only 1 polls completed in yield 1") | pass |
| H01 | memory pass: every second pass reserves nothing (L2, intermittent reservation) | `TestFault_CancelRacesAccept` fails at once ("unreserved after a pass that ran") | pass |
| H2 | memory `ClaimForPoll` own arm accepts another holder's live reservation when a pass completed at the same instant (INV-3) | `TestProperty_AssignmentSubState_ModelConformance` (seed 22) | pass |
| L5 | PostgreSQL poll write throttle 1.0 s to 10.0 s | `TestAbuse_Poll_Flood_ThrottlesFreshnessWrites` (PostgreSQL: 1 write over 3.5 s, floor 2) | pass |

Not mutation-proved (said plainly):
- H2: the mutant is also killed by the old property test (seed 22, a poll without a pass). I did not find a production mutant that only the new post-refresh check kills; the gain is coverage (see the report's counts), not a new kill. Labelled executed for the kill, inferred for any extra power.
- L1 (FS-21 bound from the repository): no production mutant is killed only by it; it replaces a check that could not fail. Label: designed, executed green.
- L4 (`TestFault_OutageRecovery_NoAgeClaim`): the new assertions (no pass during the outage, one refresh at once after recovery, w1 claims by reservation) have no dedicated mutant; see the handoff.

## Reproduction before the fix (P4-08f suite at `origin/main`, `mut/before/`)
| ID | Result against the old suite |
|---|---|
| Q11 | survived (`go test ./internal/storage/postgres/` ok) |
| Q10 | survived (same) |
| Q18 | survived (same) |
| H01 | survived (`TestFault_CancelRacesAccept` ok) |
| C01 | memory survived (as the audit found); the PostgreSQL variant of the old test failed in 6 of 6 runs here (11 to 16 polls against the bound of 20, `C01-repeat.log`). The audit ran memory only. The new test fails both backends ("only 1 polls completed in yield 1"). |
| H2 | not run against the old suite as a separate step: the old property test also kills it (stated above, executed in the scratch worktree: seed 22) |
