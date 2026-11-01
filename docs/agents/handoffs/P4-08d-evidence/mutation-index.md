# P4-08d mutation index

Each row is one single-site mutation of production code, applied by `mut_run.py.txt` (mutant table: `mut_mutants.py.txt`) in a throwaway worktree `~/mc-mut-claude` at commit 2c64627, with the `.env` of `~/MiniCloud-claude` (database `minicloud_claude_test`). For each row the harness checked that the original text occurs exactly once, applied the change, saved `mut-<id>.diff`, ran only the named test with `go test -race -count=1 -v -timeout 300s -run '^<test>$' <package>` (red), restored the file with `git checkout`, and ran the same test again (green). Test processes ran one at a time. JSON log lines from `slog` are dropped from the saved logs. The worktree was removed afterwards.

Harness correction: in the first run D06 (`l.yield(ctx, 0)`, which left `minYield` unused) and D18 (`if true {`, which left the `domain` import unused) failed to build, and the harness reported them as CHECK, not KILLED. They were re-run as `l.yield(ctx, minYield*0)` and `if svc.DispatchPolicy() == domain.DispatchPolicyRoundRobin || true {`; their logs and rows below are from that re-run. Every red log below contains a `--- FAIL` line for the named test.

| Mutation | What the mutant does | File | Test (run alone) | Package | Red | Green | Verdict | Diff (+/-) |
|---|---|---|---|---|---|---|---|---|
| D01 | INV-9 loop: memory pass ignores the grace input (every lapse counted) | `internal/storage/inmemory_assignment.go` | `TestCoordinator_AssignmentSweep_GraceDoesNotCountLapse` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D02 | INV-9 loop: the sweeper hands an empty grace state to the pass | `internal/coordinator/sweeper.go` | `TestCoordinator_AssignmentSweep_GraceDoesNotCountLapse` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D03 | INV-9 loop: memory pass skips revocation under grace | `internal/storage/inmemory_assignment.go` | `TestCoordinator_AssignmentSweep_GraceStillRevokes` | `./internal/coordinator/` | FAIL | PASS | KILLED | +3/-0 |
| D04 | INV-9 PostgreSQL: pass ignores the grace input | `internal/storage/postgres/assignment.go` | `TestConformance_AssignPass_GraceRevokesWithoutCounting` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| D05 | INV-9 memory repository: pass ignores the grace input (shared case) | `internal/storage/inmemory_assignment.go` | `TestConformance_AssignPass_GraceRevokesWithoutCounting` | `./internal/storage/` | FAIL | PASS | KILLED | +1/-1 |
| D06 | INV-20: no gap between repeated passes (yield of zero) | `internal/coordinator/assignment_loop.go` | `TestCoordinator_AssignmentRepeat_YieldsToPolls` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D07 | INV-20: s.mu held across the yield | `internal/coordinator/assignment_loop.go` | `TestCoordinator_AssignmentRepeat_YieldsToPolls` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D08 | INV-21: repeat on a full batch regardless of reservations | `internal/coordinator/assignment_loop.go` | `TestCoordinator_AssignmentRepeat_OnlyWithProgress` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D09 | INV-23 cost: refresh rate limit removed | `internal/coordinator/assignment.go` | `TestPoll_RefreshAttempts_AtMostOnePerInterval` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D10 | INV-24 loop: age arm opened by refreshLastFailed alone | `internal/coordinator/assignment.go` | `TestCoordinator_AssignmentLoop_RateLimitedPoll_NoAgeClaim` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D11 | A2: stale heartbeat ERROR log without its rate limit | `internal/coordinator/assignment_loop.go` | `TestCoordinator_AssignmentLoop_StaleHeartbeatErrorRateLimited` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D12 | Refresh threshold lowered from 2 x to 1 x interval (memory) | `internal/coordinator/assignment.go` | `TestPoll_RefreshThreshold_TwiceInterval` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D13 | Refresh threshold raised from 2 x to 3 x interval (memory) | `internal/coordinator/assignment.go` | `TestPoll_RefreshThreshold_TwiceInterval` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D14 | Refresh threshold lowered from 2 x to 1 x interval (PostgreSQL) | `internal/coordinator/assignment.go` | `TestPoll_RefreshThreshold_TwiceInterval` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| D15 | Refresh threshold raised from 2 x to 3 x interval (PostgreSQL) | `internal/coordinator/assignment.go` | `TestPoll_RefreshThreshold_TwiceInterval` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| D16 | Startup failure: SetAssignmentSettings error ignored | `cmd/coordinator/main.go` | `TestMain_ConfigureService_FailsOnInvalidAssignmentSettings` | `./cmd/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D17 | Startup failure: SetDispatchPolicy error ignored | `cmd/coordinator/main.go` | `TestMain_ConfigureService_FailsOnRejectedPolicy` | `./cmd/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D18 | Binary starts the assignment loop under fifo_pull too | `cmd/coordinator/main.go` | `TestMain_BackgroundLoops_AssignmentLoopOnlyForRoundRobin` | `./cmd/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D19 | Poll-write throttle removed (PostgreSQL) | `internal/storage/postgres/assignment.go` | `TestConformance_RecordPoll_ThrottlesFreshnessWrites` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| D20 | Poll-write throttle removed (memory) | `internal/storage/inmemory_assignment.go` | `TestConformance_RecordPoll_ThrottlesFreshnessWrites` | `./internal/storage/` | FAIL | PASS | KILLED | +1/-1 |
| D21 | Note 16: grace never pending (passes before the first sweeper tick count lapses) | `internal/coordinator/sweeper.go` | `TestCoordinator_AssignmentLoop_GracePendingUntilFirstSweep` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D22 | One validator: config batch limit ceiling drifts from 500 to 501 | `internal/config/config.go` | `TestMain_AssignmentSettings_ConfigAndServiceAgree` | `./cmd/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D23 | One validator: service A1 fallback-age rule drifts from > to >= | `internal/coordinator/assignment.go` | `TestMain_AssignmentSettings_ConfigAndServiceAgree` | `./cmd/coordinator/` | FAIL | PASS | KILLED | +1/-1 |
| D24 | Submit trigger removed | `internal/coordinator/service.go` | `TestCoordinator_AssignmentLoop_SubmitTriggersPass` | `./internal/coordinator/` | FAIL | PASS | KILLED | +1/-1 |

Total: 24 mutants, 24 killed, 0 survived.

Required by the task: INV-9 loop (D01, D02, D03; PostgreSQL pass D04; memory repository D05), INV-20 (D06, D07), INV-21 (D08), INV-23 (D09), INV-24 loop (D10), the heartbeat ERROR rate limit (D11), the refresh threshold (D12 to D15), the startup failure (D16, D17). Further guards: loop only under round_robin (D18), poll-write throttle (D19, D20), note 16 (D21), one validator (D22 config side, D23 service side), submit trigger (D24).
