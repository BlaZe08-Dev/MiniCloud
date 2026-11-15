# P4-08f audit mutation index

Commit under test: `6194127` (detached in the throwaway worktree `~/mc-mut-claude`, removed afterwards). Each mutant is single-site, applied alone, run red, restored with `git checkout`, run green. `mut/<ID>.diff` is the mutant; `-red.log`/`-green.log` are filtered outputs whose first line is the command. Q rows: `internal/storage/postgres/assignment.go`, run against the PostgreSQL variants of the 18 P4-08f tests (`-run '<18 names>$/postgres'`, `-p 1`, URL from `.env`, database `minicloud_claude_test`). Survivors were also run against `go test ./internal/storage/postgres/` (`mut/<ID>-pgpkg.log`). Raw rows: `mut/results.tsv`.

| ID | Mutant | Killed by (P4-08f suite) | P4-08f suite | `internal/storage/postgres` |
|---|---|---|---|---|
| Q01 | RecordPoll writes `last_poll_at` for a non-HEALTHY worker | TestAbuse_Poll_UnhealthyWorker_Still503 | killed | not run |
| Q02 | revoke selects only expired reservations | TestFault_AssignedWorkerDiesBeforePoll | killed | not run |
| Q03 | UNHEALTHY revoke takes the lapse path | TestFault_AssignedWorkerDiesBeforePoll | killed | not run |
| Q04 | PostgreSQL revoke ignores grace (`counted := true`) | TestFault_CoordinatorCrash_BetweenAssignAndPoll, TestFault_DatabaseOutage_DuringPass | killed | not run |
| Q05 | candidate read ignores the lapse limit | none | **survived** | killed (TestConformance_AssignPass_ExcludesJobAtLapseLimit) |
| Q06 | cursor never persisted | TestAbuse_ConstantPoller_NoLargerShare, TestFault_PollRoundTripBeyondWindow | killed | not run |
| Q07 | pass heartbeat never written | 6 fault tests (see results.tsv) | killed | not run |
| Q08 | own arm ignores the holder | TestAbuse_Poll_ForeignAssignment_Returns204 (+2) | killed | not run |
| Q09 | own arm ignores the window expiry (INV-3 b) | none | **survived** | killed (TestConformance_ClaimAssigned_OnlyAssignedWithinWindow) |
| Q10 | fallback arm ignores other workers' live reservations | none | **survived** | **survived** |
| Q11 | age arm drops the pass-silence condition (INV-16 mutation) | none | **survived** | **survived** |
| Q12 | age arm drops the job-age condition | none | **survived** | killed (TestConformance_ClaimAgeArm_UsesNotBefore) |
| Q13 | age arm ignores AgeArmOpen | TestFault_OutageRecovery_RateWindow, TestFault_PersistentPassFailure_AgeClaimRate | killed | not run |
| Q14 | lapse fallback reported as `age` at the limit | TestFault_AssignmentLapseLoop_BoundedByLimit | killed | not run |
| Q15 | claim does not reset `assignment_lapses` | none | **survived** | killed (TestConformance_ClaimPolicy_BothArms +2) |
| Q16 | AssignmentPassStale always false | 4 FS-23 tests | killed | not run |
| Q17 | reservation window doubled | 4 fault tests | killed | not run |
| Q18 | revoke ignores BatchLimit (`LIMIT $1 + 1000`) | none | **survived** | **survived** |
| C01 | `internal/coordinator/assignment_loop.go`: hold `s.mu` during the yield (ADR INV-20 mutation), memory, 3 runs | none | **survived** 3/3 | coordinator pkg: killed (TestCoordinator_AssignmentRepeat_YieldsToPolls, `C01-coordpkg.log`) |
| H01 | `internal/storage/inmemory_assignment.go`: every 2nd pass silently reserves nothing, memory | 11 other P4-08f tests | TestFault_CancelRacesAccept **passes** (retry masks) | n/a |

Probes (test files added only in the throwaway worktree, deleted after): `fs22-probe.log` (late poll with a stale heartbeat), `property-probe.log` (counters inside the property test). `advisory-scope.txt`: advisory lock scope per database.
