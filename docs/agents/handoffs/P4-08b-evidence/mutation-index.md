# P4-08b mutation index (generated from the logs in this directory)

Each row is one single-site mutation of production code. The harness applied it, ran only the named test in red, restored the file from the branch commit with `git checkout`, and ran the same test again in green. Logs: `mut-<id>-red.log`, `mut-<id>-green.log`; the change: `mut-<id>.diff`.

| Mutation | Invariant | File | Killing test (red) | Red | Green | Diff (+/-) |
|---|---|---|---|---|---|---|
| M01 | INV-1 (memory) | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimAssigned_ConcurrentPollsOneWinner` | FAIL | PASS | +1/-1 |
| M02 | INV-2 | `internal/storage/inmemory_assignment.go` | `TestConformance_Assign_CreatesNoAttempt` | FAIL | PASS | +1/-0 |
| M03 | INV-3 (a) own arm ignores the worker | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimAssigned_OnlyAssignedWithinWindow` | FAIL | PASS | +1/-1 |
| M04 | INV-3 (b) own arm ignores expiry | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimAssigned_OnlyAssignedWithinWindow` | FAIL | PASS | +1/-1 |
| M05 | INV-3 (c) fallback arms before own arm | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimAssigned_OnlyAssignedWithinWindow` | FAIL | PASS | +11/-11 |
| M06 | INV-4 pure rule resets cursor to lowest ID | `internal/scheduler/round_robin_rule.go` | `TestProperty_RoundRobin_CountsWithinOne` | FAIL | PASS | +1/-0 |
| M07 | INV-4b one cursor for all task types | `internal/scheduler/round_robin_rule.go` | `TestProperty_RoundRobin_NoDoubleSelectionPerType` | FAIL | PASS | +1/-0 |
| M08 | INV-6 revoke branch removed | `internal/storage/inmemory_assignment.go` | `TestCoordinator_AssignmentSweep_RevokesWithinOneInterval` | FAIL | PASS | +1/-1 |
| M09 | INV-7a lapse writes an attempt | `internal/storage/inmemory_assignment.go` | `TestConformance_AssignmentLapse_ConsumesNoAttempt` | FAIL | PASS | +1/-0 |
| M10 | INV-7b lapse fallback arm disabled | `internal/storage/inmemory_assignment.go` | `TestConformance_AssignmentLapse_FallbackAfterLimit` | FAIL | PASS | +1/-1 |
| M11 | INV-8 cancel does not clear the reservation | `internal/storage/inmemory.go` | `TestConformance_CancelAssigned_NeverClaimed` | FAIL | PASS | +0/-1 |
| M12 | INV-10 (a) no outstanding-reservation check | `internal/storage/inmemory_assignment.go` | `TestConformance_Assign_OneReservationPerWorker` | FAIL | PASS | +1/-1 |
| M13 | INV-10 (b) no PENDING/RUNNING check | `internal/storage/inmemory_assignment.go` | `TestConformance_Assign_SkipsWorkerWithRunningAttempt` | FAIL | PASS | +1/-1 |
| M14a | INV-11 (heartbeat freshness dropped) | `internal/storage/inmemory_assignment.go` | `TestConformance_Assign_SkipsStaleWorkers` | FAIL | PASS | +1/-1 |
| M14b | INV-11 (poll freshness dropped) | `internal/storage/inmemory_assignment.go` | `TestConformance_Assign_SkipsStaleWorkers` | FAIL | PASS | +1/-1 |
| M14c | INV-11 (health status dropped) | `internal/storage/inmemory_assignment.go` | `TestConformance_Assign_SkipsStaleWorkers` | FAIL | PASS | +1/-1 |
| M15 | INV-12 fifo claim honours reservations | `internal/storage/inmemory.go` | `TestConformance_FifoPull_IgnoresAssignmentColumns` | FAIL | PASS | +1/-1 |
| M16 | INV-14 (memory) age arm disabled | `internal/storage/inmemory_assignment.go` | `TestFault_AssignmentPassStalled_JobRunsWithinBound` | FAIL | PASS | +1/-1 |
| M17 | INV-15 one LIMIT B across all types | `internal/storage/inmemory_assignment.go` | `TestProperty_BatchHeadNeverStarved` | FAIL | PASS | +3/-1 |
| M18 | INV-16 age arm without pass silence | `internal/storage/inmemory_assignment.go` | `TestProperty_SaturatedPass_NoAgeFallback` | FAIL | PASS | +1/-1 |
| M19 | INV-18 no candidate cap | `internal/storage/inmemory_assignment.go` | `TestConformance_AssignEvent_CapsCandidates` | FAIL | PASS | +1/-1 |
| M20 | INV-19 (memory) no refresh before claim | `internal/coordinator/assignment.go` | `TestPoll_AgeArm_AfterRestart_RefreshesFirst` | FAIL | PASS | +1/-1 |
| M21 | INV-23 no single-flight guard | `internal/coordinator/assignment.go` | `TestPoll_RefreshSingleFlight` | FAIL | PASS | +0/-3 |
| M22 | INV-24 age arm opened by refreshLastFailed alone | `internal/coordinator/assignment.go` | `TestPoll_RateLimitedPoll_NoAgeClaim` | FAIL | PASS | +1/-1 |
