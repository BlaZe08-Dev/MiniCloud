# P4-08e mutation index

Each row is one single-site mutation of production code, applied by `mut_run.py.txt` (mutant table: `mut_mutants.py.txt`) in a throwaway worktree `~/mc-mut-claude`, with the `.env` of `~/MiniCloud-claude` (database `minicloud_claude_test`). For each row the harness checked that the original text occurs exactly once, applied the change, saved `mut-<id>.diff`, ran only the named test with `go test -race -count=1 -v -timeout 300s -run '^<test>$' <package>` (red), restored the file with `git checkout`, and ran the same test again (green). Test processes ran one at a time. JSON log lines from `slog` are dropped from the saved logs. The worktree was removed afterwards. Every red log below contains a `--- FAIL` line for the named test, and every green log a `--- PASS` line.

Commits: E01 to E08 and E13 to E27 ran at 7567570. E09 to E12, E21 and E22 ran first at 7567570 (all killed) and were re-run at e3946e1, after `TestConformance_ClaimPolicy_PerArm` gained a top-level harness so it skips as a whole without a database; the rows and logs below are from that re-run (all killed). E28 to E31 ran first at 7567570, where E29 **survived** (logs in `first-run/`, results in `first-run/mut-results-first-run.tsv`). The cause is a pre-existing bug, not an equivalent mutant: `ComputeBackoffWithJitter` returns 0 s from 64 consecutive failures on (`probe-backoff-overflow.log`, executed), and the test's second error streak started at failure 301, so its backoffs were 0 s with or without the mutant. Commit 9136690 resets the failure count between the two streaks (one 204 poll) and checks each streak on its own. E28 to E31 were re-run at 9136690; the rows below are from that re-run. The bug itself is reported, not fixed (handoff, finding F-5c2-1).


Fix round 1 (E32 to E37, run at 8c904f1 by `fix1_mut.py.txt`, results in `mut-results-fix1.tsv`): E32 is the audit's I4, E33 its M2, E36 its M3p and E37 its M3; E34 is the PostgreSQL twin of M2 and E35 removes a field from the 201 encoding. Every red log has a `--- FAIL` line for the named test, every green log a `--- PASS` line; each ran alone and the worktree was removed afterwards.

| Mutation | What the mutant does | File | Test (run alone) | Package | Red | Green | Verdict | Diff (+/-) |
|---|---|---|---|---|---|---|---|---|
| E01 | poll presence: fifo_pull claim carries no policy | `internal/coordinator/service.go` | `TestAPI_Poll_PolicyFieldOnWire` | `./internal/api/` | FAIL | PASS | KILLED | +0/-1 |
| E02 | poll presence: fallback_reason emitted on every claim (omitempty dropped) | `internal/coordinator/service.go` | `TestAPI_Poll_PolicyFieldOnWire` | `./internal/api/` | FAIL | PASS | KILLED | +1/-1 |
| E03 | poll presence: policy never on the wire | `internal/coordinator/service.go` | `TestAPI_Poll_PolicyFieldOnWire` | `./internal/api/` | FAIL | PASS | KILLED | +1/-1 |
| E04 | poll presence: round_robin poll drops the claim's fallback reason | `internal/coordinator/assignment.go` | `TestAPI_Poll_PolicyFieldOnWire` | `./internal/api/` | FAIL | PASS | KILLED | +0/-1 |
| E05 | job view presence: assignment_fallback_reason omitted when null | `internal/domain/job.go` | `TestAPI_Job_AssignmentFieldsAreAdditive` | `./internal/api/` | FAIL | PASS | KILLED | +1/-1 |
| E06 | job view presence: internal assigned_at exposed | `internal/domain/job.go` | `TestAPI_Job_AssignmentFieldsAreAdditive` | `./internal/api/` | FAIL | PASS | KILLED | +1/-1 |
| E07 | job view presence: assignment_lapses omitted when 0 | `internal/domain/job.go` | `TestConformance_JobView_AssignmentFields` | `./internal/storage/` | FAIL | PASS | KILLED | +1/-1 |
| E08 | job view presence: assigned_worker_id omitted when null (PostgreSQL run) | `internal/domain/job.go` | `TestConformance_JobView_AssignmentFields` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| E09 | fallback_reason value: memory age arm reports lapses | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimPolicy_PerArm` | `./internal/storage/` | FAIL | PASS | KILLED | +1/-1 |
| E10 | fallback_reason value: memory lapse arm reports age | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimPolicy_PerArm` | `./internal/storage/` | FAIL | PASS | KILLED | +1/-1 |
| E11 | fallback_reason value: PostgreSQL age arm reports lapses | `internal/storage/postgres/assignment.go` | `TestConformance_ClaimPolicy_PerArm` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| E12 | fallback_reason value: PostgreSQL lapse arm reports age | `internal/storage/postgres/assignment.go` | `TestConformance_ClaimPolicy_PerArm` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| E13 | parity: PostgreSQL own claim clears the recorded reason | `internal/storage/postgres/assignment.go` | `TestConformance_JobView_AssignmentFields` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| E14 | parity: memory own claim records an empty reason | `internal/storage/inmemory_assignment.go` | `TestConformance_JobView_AssignmentFields` | `./internal/storage/` | FAIL | PASS | KILLED | +1/-1 |
| E15 | parity: memory claim keeps the lapse count | `internal/storage/inmemory_assignment.go` | `TestConformance_JobView_AssignmentFields` | `./internal/storage/` | FAIL | PASS | KILLED | +0/-1 |
| E16 | parity: PostgreSQL claim keeps the lapse count | `internal/storage/postgres/assignment.go` | `TestConformance_JobView_AssignmentFields` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| E17 | parity: PostgreSQL GetJob reads no fallback reason | `internal/storage/postgres/repository.go` | `TestConformance_JobView_AssignmentFields` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| E18 | parity: PostgreSQL GetJob reads no expiry | `internal/storage/postgres/repository.go` | `TestConformance_JobView_AssignmentFields` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| E19 | parity: memory expiry two windows after the pass | `internal/storage/inmemory_assignment.go` | `TestConformance_JobView_AssignmentFields` | `./internal/storage/` | FAIL | PASS | KILLED | +1/-1 |
| E20 | parity: PostgreSQL expiry two windows after the pass | `internal/storage/postgres/assignment.go` | `TestConformance_JobView_AssignmentFields` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| E21 | policy value: memory own claim reported as fallback | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimPolicy_PerArm` | `./internal/storage/` | FAIL | PASS | KILLED | +1/-1 |
| E22 | policy value: PostgreSQL own claim reported as fallback | `internal/storage/postgres/assignment.go` | `TestConformance_ClaimPolicy_PerArm` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| E23 | INV-17 (ADR mutation 1): maximum poll backoff raised above the window floor | `internal/worker/client.go` | `TestPollTiming_AcceptWindowExceedsBackoff` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |
| E24 | INV-17 (ADR mutation 2): config floor lowered to 2 s | `internal/config/config.go` | `TestPollTiming_AcceptWindowExceedsBackoff` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |
| E25 | INV-17: service floor lowered to 2 s | `internal/coordinator/assignment.go` | `TestPollTiming_AcceptWindowExceedsBackoff` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |
| E26 | INV-17: named floor lowered to 3 s | `internal/coordinator/assignment.go` | `TestPollTiming_AcceptWindowExceedsBackoff` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |
| E27 | INV-17: backoff cap removed from ComputeBackoffWithJitter | `internal/worker/client.go` | `TestPollTiming_AcceptWindowExceedsBackoff` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |
| E28 | INV-17 loop: outage backoff capped at 2 x MaxPollBackoff | `internal/worker/client.go` | `TestPollTiming_RunSleepsWithinMaxBackoff` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |
| E29 | INV-17 loop: other-error backoff capped at 2 x MaxPollBackoff | `internal/worker/client.go` | `TestPollTiming_RunSleepsWithinMaxBackoff` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |
| E30 | INV-17 loop: idle sleep after 204 doubled | `internal/worker/client.go` | `TestPollTiming_RunSleepsWithinMaxBackoff` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |
| E31 | INV-17 loop: poll sleeps bypass the agent clock | `internal/worker/client.go` | `TestPollTiming_RunSleepsWithinMaxBackoff` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |

| E32 | audit-I4 (F-R1): maximum poll backoff 3 s -> 4.999 s, 1 ms below the window floor | `internal/worker/client.go` | `TestPollTiming_AcceptWindowExceedsBackoff` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |
| E33 | audit-M2 (F-R2): memory reports age when both fallback arms qualify | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimPolicy_BothArms` | `./internal/storage/` | FAIL | PASS | KILLED | +1/-1 |
| E34 | F-R2 PostgreSQL twin: both arms qualify, reason reported as age | `internal/storage/postgres/assignment.go` | `TestConformance_ClaimPolicy_BothArms` | `./internal/storage/postgres/` | FAIL | PASS | KILLED | +1/-1 |
| E35 | F-R3: assignment_lapses removed from the job encoding (201 body) | `internal/domain/job.go` | `TestAPI_SubmitJob_ResponseCarriesAssignmentFields` | `./internal/api/` | FAIL | PASS | KILLED | +1/-1 |
| E36 | audit-M3p (F-R4): production clock halves every timer, real-clock test alone | `internal/worker/client.go` | `TestPollTiming_RealClockSleeps` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |
| E37 | audit-M3 (F-R4): production clock halves every timer, whole worker package | `internal/worker/client.go` | `TestPollTiming_RealClockSleeps (whole package)` | `./internal/worker/` | FAIL | PASS | KILLED | +1/-1 |

Total: 37 mutants, 37 killed, 0 survived (31 at the first review, 6 added in fix round 1).
