# P4-08c mutation index (generated from the harness results in this directory)

Each row is one mutation of production code (Sites says how many occurrences of the same text were changed; every row is one change site except P21 and P23, where the eligibility predicate exists twice by design, once in the snapshot read and once in the re-check under the worker row lock, and both copies were changed). The harness ran in a throwaway worktree `~/mc-mut-claude` at commit e8e88fe with the `.env` of `~/MiniCloud-claude` (database `minicloud_claude_test`). For each row it applied the change, saved `mut-<id>.diff`, ran only the named test with `go test -race -count=1 -v -timeout 300s -run '^<test>$'` (red), restored the file with `git checkout`, and ran the same test again (green). The logs are filtered: log lines from `slog` are dropped. The worktree was removed afterwards.

Harness correction: in the first run, P29 replaced `sort.Strings(eligible)` with an empty line, which left the `sort` import unused; the red run was a build failure, and the harness reported it as SURVIVED because it found no `--- FAIL` line. P29 was re-run with the call replaced by `_ = sort.Strings`; the logs and the row below are from that re-run. The red logs of every other row contain a `--- FAIL` line for the named test, or (P07) a `--- PASS` line.

| Mutation | Invariant and change | File | Sites | Test (run alone) | Red | Green | Verdict | Diff (+/-) |
|---|---|---|---|---|---|---|---|---|
| P01 | INV-1 (PostgreSQL): fallback arm without the dispatchable gate | `internal/storage/postgres/assignment.go` | 1 | `TestPostgresRepository_ClaimAssigned_ConcurrentPollsOneWinner` | FAIL | PASS | KILLED | +0/-1 |
| P02 | INV-2b: CancelJob does not clear the reservation | `internal/storage/postgres/repository.go` | 1 | `TestPostgresRepository_CancelAssigned_ClearsAssignment` | FAIL | PASS | KILLED | +1/-2 |
| P03 | INV-5: the pass ignores the stored cursor | `internal/storage/postgres/assignment.go` | 1 | `TestPostgresRepository_AssignmentCursor_SurvivesRestart` | FAIL | PASS | KILLED | +1/-1 |
| P04 | INV-10b: no FOR UPDATE on the worker row in the pass | `internal/storage/postgres/assignment.go` | 1 | `TestPostgresRepository_Assign_SkipsWorkerLockedByClaim` | FAIL | PASS | KILLED | +1/-1 |
| P05 | INV-13: per-type candidate read waits on locked jobs (no SKIP LOCKED) | `internal/storage/postgres/assignment.go` | 1 | `TestPostgresRepository_AssignmentPass_NoDeadlockWithClaim` | FAIL | PASS | KILLED | +1/-1 |
| P06 | INV-13: same change as P05, concurrent mixed operations test | `internal/storage/postgres/assignment.go` | 1 | `TestPostgresRepository_AssignmentPass_ConcurrentMixedOperations` | FAIL | PASS | KILLED | +1/-1 |
| P07 | INV-13: ADR Section 6.3 mutation, the pass locks workers before jobs | `internal/storage/postgres/assignment.go` | 1 | `TestPostgresRepository_AssignmentPass_NoDeadlockWithClaim` | PASS | PASS | SURVIVED | +3/-0 |
| P08 | INV-22: blocking advisory lock instead of try-lock | `internal/storage/postgres/assignment.go` | 1 | `TestPostgresRepository_AssignmentPass_SecondPassSkips` | FAIL | PASS | KILLED | +1/-1 |
| P09 | INV-22: same change as P08, shared case on PostgreSQL | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_AssignPass_SkipsWhenPassLockHeld` | FAIL | PASS | KILLED | +1/-1 |
| P10 | INV-14 (PostgreSQL): age arm removed from the fallback SQL | `internal/storage/postgres/assignment.go` | 1 | `TestFault_AssignmentPassStalled_JobRunsWithinBound` | FAIL | PASS | KILLED | +1/-1 |
| P11 | INV-19 (PostgreSQL) restart: no refresh before the claim | `internal/coordinator/assignment.go` | 1 | `TestPoll_AgeArm_AfterRestart_RefreshesFirst` | FAIL | PASS | KILLED | +1/-1 |
| P12 | INV-19 (PostgreSQL) outage: same change as P11 | `internal/coordinator/assignment.go` | 1 | `TestPoll_AgeArm_AfterOutage_RefreshesFirst` | FAIL | PASS | KILLED | +1/-1 |
| P13 | INV-24 (PostgreSQL): age arm opened by refreshLastFailed alone | `internal/coordinator/assignment.go` | 1 | `TestPoll_RateLimitedPoll_NoAgeClaim` | FAIL | PASS | KILLED | +1/-1 |
| P14 | INV-24 (PostgreSQL) first poll after outage: same change as P11 | `internal/coordinator/assignment.go` | 1 | `TestPoll_FirstPollAfterOutage_AttemptsAndCloses` | FAIL | PASS | KILLED | +1/-1 |
| P15 | Note 8: database-clock staleness check never stale (threshold shifted by 1 h) | `internal/storage/postgres/assignment.go` | 1 | `TestPoll_AgeArm_AfterRestart_RefreshesFirst` | FAIL | PASS | KILLED | +1/-1 |
| P16 | INV-2b backstop: shape CHECK without the state condition | `internal/storage/postgres/migrations/000005_assignment_round_robin.sql` | 1 | `TestMigrator_Migration000005_FreshSchema` | FAIL | PASS | KILLED | +1/-2 |
| P17 | INV-3 (a) PostgreSQL: own arm ignores the worker | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_ClaimAssigned_OnlyAssignedWithinWindow` | FAIL | PASS | KILLED | +1/-1 |
| P18 | INV-3 (b) PostgreSQL: own arm ignores expiry | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_ClaimAssigned_OnlyAssignedWithinWindow` | FAIL | PASS | KILLED | +0/-1 |
| P19 | INV-7b PostgreSQL: lapse arm removed | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_AssignmentLapse_FallbackAfterLimit` | FAIL | PASS | KILLED | +1/-1 |
| P20 | INV-8 PostgreSQL: CancelJob does not clear the reservation (same change as P02) | `internal/storage/postgres/repository.go` | 1 | `TestConformance_CancelAssigned_NeverClaimed` | FAIL | PASS | KILLED | +1/-2 |
| P21 | INV-10 (a) PostgreSQL: no live-reservation check (both copies of the predicate) | `internal/storage/postgres/assignment.go` | 2 | `TestConformance_Assign_OneReservationPerWorker` | FAIL | PASS | KILLED | +2/-2 |
| P22 | INV-10 (b) PostgreSQL: EffectiveWorkerSlots raised to 2 | `internal/domain/assignment.go` | 1 | `TestConformance_Assign_SkipsWorkerWithRunningAttempt` | FAIL | PASS | KILLED | +1/-1 |
| P23 | INV-11 PostgreSQL: poll freshness dropped (both copies of the predicate) | `internal/storage/postgres/assignment.go` | 2 | `TestConformance_Assign_SkipsStaleWorkers` | FAIL | PASS | KILLED | +2/-2 |
| P24 | INV-12 PostgreSQL: fifo DispatchNextJob honours the reservation | `internal/storage/postgres/repository.go` | 1 | `TestConformance_FifoPull_IgnoresAssignmentColumns` | FAIL | PASS | KILLED | +1/-0 |
| P25 | INV-18 PostgreSQL: candidate cap removed | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_AssignEvent_CapsCandidates` | FAIL | PASS | KILLED | +1/-1 |
| P26 | D9 (F-5a-3) PostgreSQL: candidate read keeps jobs at the lapse limit | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_AssignPass_ExcludesJobAtLapseLimit` | FAIL | PASS | KILLED | +1/-1 |
| P27 | D8 (F-5a-4) PostgreSQL: an unhealthy revoke counts a lapse | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_AssignPass_UnhealthyRevokeCountsNoLapse` | FAIL | PASS | KILLED | +1/-1 |
| P28 | Section 3.2 (F-5a-6) PostgreSQL: age from created_at, not dispatchable_since | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_ClaimAgeArm_UsesNotBefore` | FAIL | PASS | KILLED | +1/-1 |
| P29 | Section 2.1 (F-5a-7) PostgreSQL: candidates not sorted by ID | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_AssignEvent_CandidatesInIDOrder` | FAIL | PASS | KILLED | +1/-1 |

Runs: 29. KILLED: 28. SURVIVED: 1. Other: 0.

## P07 (survived: equivalent mutant)

P07 is the mutation that ADR-0017 Section 6.3 names for INV-13: "Make the pass lock `workers` before `jobs`". It survives. The pass reads its job candidates with `FOR UPDATE SKIP LOCKED`, so it never waits on a job row, and a wait cycle needs the pass to wait on a job. Taking the worker locks first therefore does not produce `40P01` against a claim. P05 and P06 (the candidate read without `SKIP LOCKED`) do produce `40P01` and are killed by both INV-13 tests. Fix round 1: the orchestrator accepted this, and ADR-0017 implementation note 11 records P07 as an equivalent mutant and P05 as the INV-13 mutation.

## Coverage of the invariants that slice 5a left open

The 5a index (`P4-08b-evidence/mutation-index.md`) listed these as deferred to 5b or not mutable in memory.

| Invariant | Test | Killed by | Status |
|---|---|---|---|
| INV-1 (PostgreSQL) | `TestPostgresRepository_ClaimAssigned_ConcurrentPollsOneWinner` | P01 | killed |
| INV-2b | `TestPostgresRepository_CancelAssigned_ClearsAssignment`; `TestMigrator_Migration000005_FreshSchema` (the CHECK) | P02, P16 | killed |
| INV-5 | `TestPostgresRepository_AssignmentCursor_SurvivesRestart`; `TestPostgresRepository_AssignmentCursor_UpdatePathPersistsAcrossPasses` (update path, fix round 2) | P03, F04 | killed |
| INV-9 | none | none | slice 5c (ADR-0017 implementation note 10, fix round 1: the Section 7 listing under 5b is an erratum) |
| INV-10b | `TestPostgresRepository_Assign_SkipsWorkerLockedByClaim` | P04 | killed |
| INV-13 | `TestPostgresRepository_AssignmentPass_NoDeadlockWithClaim`, `TestPostgresRepository_AssignmentPass_ConcurrentMixedOperations` | P05, P06 | killed; P07 is an equivalent mutant (ADR-0017 implementation note 11, fix round 1) |
| INV-14 (PostgreSQL) | `TestFault_AssignmentPassStalled_JobRunsWithinBound` (package `internal/storage/postgres`) | P10 | killed |
| INV-17 | none | none | slice 5c |
| INV-19 (PostgreSQL) | `TestPoll_AgeArm_AfterRestart_RefreshesFirst`, `TestPoll_AgeArm_AfterOutage_RefreshesFirst` (package `internal/storage/postgres`) | P11, P12, P15 | killed |
| INV-20, INV-21 | none | none | slice 5c |
| INV-22 (PostgreSQL) | `TestPostgresRepository_AssignmentPass_SecondPassSkips`, `TestConformance_AssignPass_SkipsWhenPassLockHeld` (PostgreSQL) | P08, P09 | killed |
| INV-23 (cost variant) | none | none | slice 5c |
| INV-24 (PostgreSQL) | `TestPoll_RateLimitedPoll_NoAgeClaim`, `TestPoll_FirstPollAfterOutage_AttemptsAndCloses` (package `internal/storage/postgres`) | P13, P14 | killed |

The 5a audit mutations A12 (refresh threshold) and A13 (poll-write throttle) stay deferred to slice 5c (ADR implementation note 9); no run here covers them.

PostgreSQL sides of the shared 5a cases with their own PostgreSQL mutation: INV-3 (P17, P18), INV-7b (P19), INV-8 (P20), INV-10 (P21, P22), INV-11 poll freshness (P23), INV-12 (P24), INV-18 (P25), D9 lapse limit (P26), D8 unhealthy revoke (P27), dispatchable_since (P28), candidate ID order (P29). Shared cases with no PostgreSQL mutation in this run: INV-2, INV-7a, the claim capacity check, the lapse arm at L-1, the own arm at exact expiry, and the INV-11 heartbeat and status predicates; their in-memory mutations from 5a stand.

## Fix round 1 (F01 to F03)

Same harness and filters, in a fresh throwaway worktree `~/mc-mut-claude` at commit 1351839, removed afterwards.

| Mutation | Invariant and change | File | Sites | Test (run alone) | Red | Green | Verdict | Diff (+/-) |
|---|---|---|---|---|---|---|---|---|
| F01 | Heartbeat written at transaction start (clock_timestamp -> now) | `internal/storage/postgres/assignment.go` | 1 | `TestPostgresRepository_AssignmentPass_HeartbeatIsCompletionTime` | FAIL | PASS | KILLED | +1/-1 |
| F02 | SetDispatchPolicy accepts a repository without AssignmentPassStaleness | `internal/coordinator/assignment.go` | 1 | `TestSetDispatchPolicy_RoundRobinRequiresPassStaleness` | FAIL | PASS | KILLED | +1/-1 |
| F03 | Worker delete cascades to the reserved job (ON DELETE CASCADE) | `internal/storage/postgres/migrations/000005_assignment_round_robin.sql` | 1 | `TestPostgresRepository_DeleteWorkerWithReservation_Rejected` | FAIL | PASS | KILLED | +1/-1 |

Runs: 3. KILLED: 3. SURVIVED: 0. Other: 0.

F01 pins ADR-0017 implementation note 12 (heartbeat = completion time). F02 pins the `SetDispatchPolicy` staleness guard (note 13). F03 pins the rejection of a worker delete that would orphan a reservation. Totals over rounds 0 and 1: 32 runs, 31 killed, 1 equivalent mutant (P07); fix round 2 below brings them to 35, 34 and 1.

## Fix round 2 (F04 to F06, audit findings F-R4 to F-R6)

The mutants are the auditor's exact text replacements A01, A01b and A06b (`audit_new_mutants.py.txt` on the review branch), each with an occurrence check of 1. Same filters as above; the harness ran in a fresh throwaway worktree `~/mc-mut-claude` at commit 2b32089 (the commit that adds the three tests) with the `.env` of `~/MiniCloud-claude` (database `minicloud_claude_test`), ran only the named test (red), restored with `git checkout`, ran the same test again (green), and the worktree was removed afterwards.

| Mutation | Invariant and change | File | Sites | Test (run alone) | Red | Green | Verdict | Diff (+/-) |
|---|---|---|---|---|---|---|---|---|
| F04 | INV-5 cursor update path (audit A01): the cursor upsert keeps the old `last_worker_id` on conflict | `internal/storage/postgres/assignment.go` | 1 | `TestPostgresRepository_AssignmentCursor_UpdatePathPersistsAcrossPasses` | FAIL | PASS | KILLED | +1/-1 |
| F05 | Section 2.1 `cursor_before` (audit A01b): the in-pass cursor is not advanced after a decision | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_AssignEvent_CursorBeforeAdvancesWithinPass` | FAIL | PASS | KILLED | +1/-1 |
| F06 | Sections 3.2 and 3.5 fallback arm (audit A06b): a reservation stays live for the fallback arm 2 s past its expiry | `internal/storage/postgres/assignment.go` | 1 | `TestConformance_ClaimFallback_ExpiredUnrevokedReservation` | FAIL | PASS | KILLED | +1/-1 |

Runs: 3. KILLED: 3. SURVIVED: 0. Other: 0.

F05 and F06 are shared cases (`internal/storage/storagetest/assignment.go`); the rows above are their PostgreSQL side. The in-memory side runs the same cases and passes; no in-memory mutation was run in this round. Totals over all rounds: 35 runs, 34 killed, 1 equivalent mutant (P07).
