# P4-08b mutation index (generated from the logs in this directory)

Runs: 42. M01 to M22 are 24 invariant mutations (M14 has the variants a, b and c). M23 is a policy guard from fix round 1. M24 to M40 are from fix round 2 (P4-08b audit): M24 to M30 guard the F-5a-1 and F-5a-8 fixes, M31 to M39 re-run the audit's surviving mutations (A11, A05, A07, A09, A08, A10, A04, A01, A17) against the adopted tests, and M40 re-runs the M20 change against the INV-19 outage test.

Each row is one single-site mutation of production code. The harness applied it, ran only the named test in red, restored the file from the branch commit with `git checkout`, and ran the same test again in green. Logs: `mut-<id>-red.log`, `mut-<id>-green.log`; the change: `mut-<id>.diff`. M01 to M23 ran inside `~/MiniCloud-claude`; M24 to M40 ran in a throwaway worktree `~/mc-mut-claude` at commit c10787e with `go test -race -count=1 -v -run '^<test>$'`, which was removed afterwards.

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
| M24 | F-5a-1 (not an INV): `NewService` without the default assignment settings | `internal/coordinator/service.go` | `TestSetDispatchPolicy_RoundRobinWithoutSettings_UsesDefaults` | FAIL | PASS | +0/-1 |
| M25 | F-5a-1 (not an INV): `SetAssignmentSettings` skips Validate | `internal/coordinator/assignment.go` | `TestSetAssignmentSettings_RejectsInvalidAndKeepsPrevious` | FAIL | PASS | +1/-1 |
| M26 | F-5a-1 (not an INV): `SetDispatchPolicy(round_robin)` skips the settings check | `internal/coordinator/assignment.go` | `TestSetDispatchPolicy_RoundRobinRejectsInvalidSettings` | FAIL | PASS | +1/-1 |
| M27 | A1 rule 1 in `Validate`: `>` to `>=` | `internal/coordinator/assignment.go` | `TestAssignmentSettings_Validate` | FAIL | PASS | +1/-1 |
| M28 | Section 5.5 range in `Validate`: max lapses lower bound 1 to 0 | `internal/coordinator/assignment.go` | `TestAssignmentSettings_Validate` | FAIL | PASS | +1/-1 |
| M29 | F-5a-8 (not an INV): config ignores assignment parse errors | `internal/config/config.go` | `TestLoadCoordinatorConfig_AssignmentMalformedValuesRejected` | FAIL | PASS | +1/-1 |
| M30 | A1 rule 1 in config: `>` to `>=` (audit A14) | `internal/config/config.go` | `TestLoadCoordinatorConfig_AssignmentIntervalEqualToWindowAccepted` | FAIL | PASS | +1/-1 |
| M31 | claim capacity check removed (audit A11) | `internal/storage/inmemory_assignment.go` | `TestConformance_Claim_RespectsWorkerCapacity` | FAIL | PASS | +1/-1 |
| M32 | D9, implementation note 1: pass candidate filter `<` to `<=` (audit A05) | `internal/storage/inmemory_assignment.go` | `TestConformance_AssignPass_ExcludesJobAtLapseLimit` | FAIL | PASS | +1/-1 |
| M33 | D9 L-1: lapse arm opens one lapse early (audit A07) | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimLapseArm_ClosedBelowLimit` | FAIL | PASS | +1/-1 |
| M34 | D8: unhealthy revoke counts a lapse (audit A09) | `internal/storage/inmemory_assignment.go` | `TestConformance_AssignPass_UnhealthyRevokeCountsNoLapse` | FAIL | PASS | +1/-1 |
| M35 | own arm claimable at exact expiry (audit A08) | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimOwnArm_ClosedAtExactExpiry` | FAIL | PASS | +1/-1 |
| M36 | age arm uses `created_at` instead of `dispatchable_since` (audit A10) | `internal/storage/inmemory_assignment.go` | `TestConformance_ClaimAgeArm_UsesNotBefore` | FAIL | PASS | +1/-1 |
| M37 | JOB_ASSIGNED candidates not sorted (audit A04) | `internal/storage/inmemory_assignment.go` | `TestConformance_AssignEvent_CandidatesInIDOrder` | FAIL | PASS | +1/-1 |
| M38 | INV-22 (memory): pass blocks on the pass lock instead of skipping (audit A01) | `internal/storage/inmemory_assignment.go` | `TestConformance_AssignPass_SkipsWhenPassLockHeld` | FAIL | PASS | +1/-1 |
| M39 | policy name guard accepts `round_robin_fallback` (audit A17) | `internal/domain/assignment.go` | `TestSetDispatchPolicy_RejectsInvalidPolicyName` | FAIL | PASS | +1/-1 |
| M40 | INV-19 (memory) outage variant: same change as M20 | `internal/coordinator/assignment.go` | `TestPoll_AgeArm_AfterOutage_RefreshesFirst` | FAIL | PASS | +1/-1 |

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
| INV-19 | M20 (restart variant, `TestPoll_AgeArm_AfterRestart_RefreshesFirst`); M40, the same change as M20, also kills the outage variant `TestPoll_AgeArm_AfterOutage_RefreshesFirst` | PostgreSQL variant deferred to 5b |
| INV-20 | none | deferred to 5c: repeat yield loop |
| INV-21 | none | deferred to 5c: repeat rule |
| INV-22 | M38 (memory variant, `TestConformance_AssignPass_SkipsWhenPassLockHeld`) | PostgreSQL variant (advisory lock) deferred to 5b |
| INV-23 | M21 | killed in memory |
| INV-24 | M22 (memory variant) | PostgreSQL variant deferred to 5b |

## Not pinned in 5a (deferred to slice 5c)

The P4-08b audit mutations A12 (refresh threshold `2*interval` to `interval` in `refreshIfStaleLocked`) and A13 (no poll-write throttle in `touchPollLocked`) survive, and stay deferred to slice 5c (ADR-0017 implementation note 9). No run in this directory covers them.

## Totals

42 runs: 42 KILLED, 0 survived. The verdict of each run M24 to M40 was checked in its logs: the red log has a `--- FAIL` line for the named test with its assertion, and the green log has one `--- PASS` line and no `--- FAIL`.
