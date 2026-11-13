# P4-08f mutation index

Each row is one single-site mutant applied alone in the throwaway worktree `~/mc-mut-claude` (detached at the task tip), run red, restored with `git checkout`, and run green. `mut/<ID>.diff` is the mutation, `mut/<ID>-red.log` and `mut/<ID>-green.log` are the filtered outputs (the first line of each names the command and whether the PostgreSQL URL was set). A PostgreSQL URL was set only where the row says PostgreSQL; the other rows ran the in-memory variants. Result: 30 killed, 0 surviving.

| ID | Group | Mutant | File | Killed by | Result |
|---|---|---|---|---|---|
| A01 | abuse tests | a foreign worker claims a live reservation | `internal/storage/inmemory_assignment.go` | TestAbuse_Poll_ForeignAssignment_Returns204 | red then green, killed |
| A02 | abuse tests | RecordPoll accepts an UNHEALTHY worker | `internal/storage/inmemory_assignment.go` | TestAbuse_Poll_UnhealthyWorker_Still503 | red then green, killed |
| A03 | abuse tests | poll freshness write not throttled (memory) | `internal/storage/inmemory_assignment.go` | TestAbuse_Poll_Flood_ThrottlesFreshnessWrites | red then green, killed |
| A04 | abuse tests | poll freshness write not throttled (PostgreSQL) | `internal/storage/postgres/assignment.go` | TestAbuse_Poll_Flood_ThrottlesFreshnessWrites | red then green, killed |
| A05 | abuse tests | cursor resets to the lowest worker on every decision | `internal/scheduler/round_robin_rule.go` | TestAbuse_ConstantPoller_NoLargerShare | red then green, killed |
| A06 | abuse tests | a worker may hold several reservations | `internal/storage/inmemory_assignment.go` | TestAbuse_LargeTotalSlots_NoLargerShare | red then green, killed |
| A07 | abuse tests | fifo_pull accepts an UNHEALTHY worker | `internal/storage/inmemory.go` | TestAbuse_Poll_UnhealthyWorker_Still503 | red then green, killed |
| F01 | fault tests (Section 6.4) | memory pass does not count a lapse | `internal/storage/inmemory_assignment.go` | TestFault_AssignedWorkerNeverPolls, TestFault_AssignmentLapseLoop_BoundedByLimit, TestFault_PollRoundTripBeyondWindow | red then green, killed |
| F02 | fault tests (Section 6.4) | PostgreSQL pass does not count a lapse | `internal/storage/postgres/assignment.go` | TestFault_AssignedWorkerNeverPolls, TestFault_AssignmentLapseLoop_BoundedByLimit, TestFault_PollRoundTripBeyondWindow | red then green, killed |
| F03 | fault tests (Section 6.4) | lapse arm opens one lapse late (limit off by one) | `internal/storage/inmemory_assignment.go` | TestFault_AssignmentLapseLoop_BoundedByLimit | red then green, killed |
| F04 | fault tests (Section 6.4) | UNHEALTHY revoke counts a lapse | `internal/storage/inmemory_assignment.go` | TestFault_AssignedWorkerDiesBeforePoll | red then green, killed |
| F05 | fault tests (Section 6.4) | sweeper does not close the attempt of a lost response | `internal/storage/inmemory.go` | TestFault_PollResponseLostAfterClaim | red then green, killed |
| F06 | fault tests (Section 6.4) | grace ignored: every lapse counts | `internal/storage/assignment.go` | TestFault_CoordinatorCrash_BetweenAssignAndPoll, TestFault_DatabaseOutage_DuringPass | red then green, killed |
| F07 | fault tests (Section 6.4) | revocation skipped under grace (job stuck) | `internal/storage/inmemory_assignment.go` | TestFault_CoordinatorCrash_BetweenAssignAndPoll, TestFault_DatabaseOutage_DuringPass | red then green, killed |
| F08 | fault tests (Section 6.4) | cancel keeps the reservation | `internal/storage/inmemory.go` | TestFault_CancelRacesAccept | red then green, killed |
| F09 | fault tests (Section 6.4) | refresh not rate limited | `internal/coordinator/assignment.go` | TestFault_OutageRecovery_RateWindow, TestFault_PersistentPassFailure_AgeClaimRate | red then green, killed |
| F10 | fault tests (Section 6.4) | poll-time refresh removed | `internal/coordinator/assignment.go` | TestFault_OutageRecovery_NoAgeClaim, TestFault_RestartThenPoll_NoAgeClaim | red then green, killed |
| F11 | fault tests (Section 6.4) | age arm removed | `internal/storage/inmemory_assignment.go` | TestFault_OutageRecovery_RateWindow, TestFault_PersistentPassFailure_AgeClaimRate | red then green, killed |
| F12 | fault tests (Section 6.4) | repeat without waiting between passes | `internal/coordinator/assignment_loop.go` | TestFault_DeepBacklog_PollsNotStarved | red then green, killed |
| F13 | fault tests (Section 6.4) | repeat without progress | `internal/coordinator/assignment_loop.go` | TestFault_DeepBacklog_PollsNotStarved | red then green, killed |
| P01 | property test | a foreign worker claims a live reservation (property) | `internal/storage/inmemory_assignment.go` | TestProperty_AssignmentSubState_ModelConformance | red then green, killed |
| P02 | property test | own arm ignores the window expiry (property) | `internal/storage/inmemory_assignment.go` | TestProperty_AssignmentSubState_ModelConformance | red then green, killed |
| P03 | property test | pass never revokes an expired reservation (property) | `internal/storage/inmemory_assignment.go` | TestProperty_AssignmentSubState_ModelConformance | red then green, killed |
| P04 | property test | age arm removed (property, INV-14) | `internal/storage/inmemory_assignment.go` | TestProperty_AssignmentSubState_ModelConformance | red then green, killed |
| P05 | property test | cancel keeps the reservation (property, INV-8) | `internal/storage/inmemory.go` | TestProperty_AssignmentSubState_ModelConformance | red then green, killed |
| P06 | property test | one reservation check dropped from eligibility (property, INV-10) | `internal/storage/inmemory_assignment.go` | TestProperty_AssignmentSubState_ModelConformance | red then green, killed |
| T01 | worker tidy-ups | failure-count clamp removed: poll backoff overflows to 0 | `internal/worker/client.go` | TestPollTiming_RunSleepsWithinMaxBackoff | red then green, killed |
| T02 | worker tidy-ups | failure-count clamp removed: delivery row sees 0 | `internal/worker/client.go` | TestComputeBackoffWithJitter_CallerSettings | red then green, killed |
| T03 | worker tidy-ups | delivery maximum changed: the row guard trips | `internal/worker/client.go` | TestComputeBackoffWithJitter_CallerSettings | red then green, killed |
| T04 | worker tidy-ups | idle poll interval is zero: the poll-loop rows use the constant | `internal/worker/client.go` | TestComputeBackoffWithJitter_CallerSettings | red then green, killed |

## Notes on the runs

- Four mutants needed a second attempt; none of the first attempts counts as a kill or a survivor. F12 first failed to compile (`minYield` unused) and became `minYield*0`. A01, P01 and P02 matched two sites (the own arm of the claim and the eligibility helper) and were pointed at the claim. A07 first mutated `DispatchJob`, which `fifo_pull` polls do not use (it survived, as a wrong site, not as a gap), and was re-pointed at the `DispatchNextJob` health check. T04 first failed to compile (`IdlePollInterval = 0` is an untyped constant) and became `time.Duration(0)`. The logs and results are those of the second attempt.
- PostgreSQL-side mutants: F02 (the lapse increment in SQL), F06 (the grace decision, shared by both backends) and A04 (the poll freshness throttle in SQL). The other mutants were applied to the in-memory repository or the coordinator, so their killing tests ran the in-memory variants; the PostgreSQL variants of those tests make the same assertions and ran green in the gates, but were not themselves mutated.
- F13 (repeat without progress) is killed by the pass count (25 passes in 5 s against the expected 3), not by a hang: the round runs under a 5 s context.
- T01 shows the new `> 0` check of `TestPollTiming_RunSleepsWithinMaxBackoff` at work (a poll sleep of a negative duration after the failure count overflows); T02 and T03 show the result-delivery row, T04 the poll-loop rows reading the named constants.
- Not mutated: the round_robin rig itself (`tests/internal/sectest/rr.go`). Its helpers are exercised by every killed row; a wrong `Elapse` or `Restart` would make the fault tests fail in their green runs.
