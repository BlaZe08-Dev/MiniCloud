# P4-08b mutation index (generated from the logs in this directory)

Runs: 25 (24 invariant mutations M01 to M22 with the a/b/c variants, plus the policy guard M23, which is not an ADR invariant).

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
| M23 | policy guard (not an INV): SetDispatchPolicy accepts round_robin without AssignmentRepository | `internal/coordinator/assignment.go` | `TestSetDispatchPolicy_RoundRobinRequiresAssignmentRepository` | FAIL | PASS | +1/-1 |

## Coverage of INV-1 to INV-24

Sub-labels (2b, 4b, 7a, 7b, 10, 10b) are listed with their parent. Every ADR invariant appears once.

| Invariant | Killed by (mutation) | Status |
|---|---|---|
| INV-1 | M01 (memory variant; `TestConformance_ClaimAssigned_ConcurrentPollsOneWinner`) | PostgreSQL variant deferred to 5b |
| INV-2 | M02 | killed in memory |
| INV-2b | none | not mutable in memory: the shape CHECK and PostgreSQL clearing are 5b (planned test `TestPostgresRepository_CancelAssigned_ClearsAssignment`, slice 5b) |
| INV-3 | M03 (a), M04 (b), M05 (c) | killed in memory |
| INV-4 | M06 | killed in memory |
| INV-4b | M07 | killed in memory |
| INV-5 | none | deferred to 5b: the test is a PostgreSQL restart test; memory cursors are not persisted |
| INV-6 | M08 | killed in memory |
| INV-7a | M09 | killed in memory |
| INV-7b | M10 | killed in memory |
| INV-8 | M11 | killed in memory |
| INV-9 | none | deferred to 5c: grace is not wired in 5a (Section 7) |
| INV-10 | M12 (a), M13 (b) | killed in memory |
| INV-10b | none | not mutable in memory: the worker row lock is PostgreSQL (5b) |
| INV-11 | M14a (heartbeat), M14b (poll), M14c (status) | killed in memory |
| INV-12 | M15 | killed in memory |
| INV-13 | none | not mutable in memory: lock order is a PostgreSQL property (5b) |
| INV-14 | M16 (memory variant) | PostgreSQL variant deferred to 5b |
| INV-15 | M17 | killed in memory |
| INV-16 | M18 | killed in memory |
| INV-17 | none | deferred to 5c: worker constant extraction and timing test |
| INV-18 | M19 | killed in memory |
| INV-19 | M20 (restart variant; outage variant has no own mutation) | PostgreSQL variant deferred to 5b |
| INV-20 | none | deferred to 5c: repeat yield loop |
| INV-21 | none | deferred to 5c: repeat rule |
| INV-22 | none | not mutable in memory: advisory lock is PostgreSQL (5b). The memory TryLock exists but has no 5a test |
| INV-23 | M21 | killed in memory |
| INV-24 | M22 (memory variant) | PostgreSQL variant deferred to 5b |
