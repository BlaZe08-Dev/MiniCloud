# Handoff P4-4c-b

Branch / last commit: `agent/claude/P4-4c-b`, stacked on `agent/claude/P4-4c-a` (head `85b2246`, under audit, not merged). The last code commit is listed by `git log` on the branch; the handoff commit follows it. `main` untouched. Nothing merged.

## What changed (by package)

- `internal/worker/client.go`
  - `Clock` / `Timer` interfaces (timers only; worker wall-clock time is never read, ADR-0014), `Agent.SetClock`. Production uses `time.NewTimer`.
  - `executeAndReport` is now exported as `ExecuteAndReport` (tests and the end-to-end test drive one assignment without the poll loop). `Run` calls it unchanged.
  - Renewal loop (`renewLoop`, `renewOnce`): one goroutine per executing attempt, interval = `lease_duration_seconds / 3` from the poll assignment (default 10 s when absent, floor 1 s). It runs from task start until delivery ends and is stopped and awaited (`wg.Wait`) before `ExecuteAndReport` returns.
  - Verdicts per protocol 6.1: 200 + `cancel_requested` cancels the task context and stops renewing, the outcome is still reported; 409 (any code) or 403 aborts the task and suppresses the result; 404 with a recognised code (`JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`) aborts, 404 without one disables renewals and runs to completion (old coordinator); 400 disables renewals and keeps running; 5xx, transport errors, an unreadable 200 body and any other status are transient.
  - Transient renewal failures retry after `min(ComputeBackoffWithJitter(failures, 1 s, interval), interval)`, back to the interval after one success. They never abort the attempt.
  - Result delivery (`deliverResult`): 60 s budget (`ResultDeliveryBudget`) on the injected clock, backoff `ComputeBackoffWithJitter(n, 200 ms, 5 s)`, the in-flight request is cancelled at the budget. Stops at success, 403/404/409 (existing `IsNonRetryableReportError`), a deterministic 400, a lost lease (checked before every attempt and during the backoff wait), shutdown, or the budget. Renewals continue during delivery.
- `internal/worker/workertest/clock.go` (new): `FakeClock` (manual `Advance`, `NewTimer`, `Now`, `Pending`, `BlockUntil`, `Requested`). One instance can drive the agent, the coordinator service and the in-memory repository.
- `internal/worker/renewal_test.go` (new), `tests/integration/worker_lease_e2e_test.go` (new).
- Not touched: `internal/coordinator`, `internal/api`, `internal/storage`, `internal/domain`, `internal/config`, `cmd/worker` (no worker setting was needed: the interval comes from the assignment, as 4c-a D9 states), every design doc, `TASKS.md`, `CHANGELOG.md`, `PROJECT_STATE.md`, `findings.md`.

## Design decisions and deviations (with reasons)

1. **404 on renew.** The orchestrator's task text lists 404 as terminal; plan 4c, protocol 6.1, ADR-0014 and ADR-0016 say: recognised not-found code is terminal, a missing or unrecognised envelope is an old coordinator and the worker degrades and runs to completion. I followed the design docs (the plan lists a gating test for the degradation, `TestWorker_Client_RenewOldCoordinatorGracefulDegradation`). Not an owner decision; flagged for the orchestrator.
2. **Transient renewal retry cadence.** The plan says "retry renewal next interval". The task asked for the existing bounded backoff and no hammering, so the retry wait is bounded exponential backoff, base 1 s, capped at the interval (0.5 s at the very least). Reason: with a 30 s lease and a 10 s interval, waiting a whole interval after each failure tolerates only two failures; a shorter bounded retry recovers inside the lease. Authorisation: orchestrator's task text for this task, not an owner decision.
3. **403 on renew is terminal** (protocol 6.1 and ADR-0016 list it; the task text lists only 409/404).
4. **Cancelled task still reports.** After `cancel_requested` the worker reports whatever the executor returned (exit code 1 and `context canceled`). With today's coordinator a failure commit on `CANCEL_REQUESTED` is not yet "cancel wins" (slice 4d). The end-to-end cancel test therefore asserts only that the attempt closed and the job left `RUNNING` and `CANCEL_REQUESTED`, so it will not break when 4d lands. After `cancel_requested` the worker stops renewing, so delivery of that result runs on an unextended lease (accepted-until-swept, protocol 6.1).
5. **No upper bound on the interval.** Floor 1 s, no ceiling: a coordinator advertising `lease_duration_seconds` of 86400 gets one renewal per 8 h. The coordinator is trusted (SEC-004); see F-4c-b-1.
6. **Delivery 400 is terminal in the delivery loop only.** `IsNonRetryableReportError` (and its existing test, which expects 400 to be false) is unchanged; `isTerminalDelivery` adds 400 as the plan asks.
7. **Redundant guards removed after mutation testing.** A pre-delivery lost check in `ExecuteAndReport` and an `if dctx.Err() != nil { break }` in `deliverResult` were equivalent mutants (the loop's own checks and the backoff `select` cover them), so I deleted them instead of adding tests.
8. `ExecuteAndReport` exported, `Clock`/`Timer`/`SetClock` added: the API surface needed for deterministic tests. `Agent.Run` still uses `time.Sleep` for the poll loop (not part of this slice).

## Tests added

Worker package (`internal/worker/renewal_test.go`, httptest coordinator, `FakeClock`, no real sleeps beyond 1 to 5 ms polling):

| Test | Guards | Mutation proof (ids in the table below) |
|---|---|---|
| `TestAgent_PeriodicLeaseRenewal_Success` | renewals are sent every interval with worker id and token; loop ends with the attempt | m01 m24 m28 |
| `TestWorker_Renewal_IntervalDerivedFromAssignment` | interval = lease/3, default 10 s, 1 s floor | m20 m21 m22 |
| `TestAgent_LeaseRenewal_ReceivesCancelRequested` | cancel delivered at the next renewal, result still reported, renewing stops | m06 m07 |
| `TestAgent_LeaseRenewal_CancelRequested_StopsRenewingEvenIfTaskIgnoresCancel` | no renewal after cancel even if the task ignores it (lease must lapse) | m08 |
| `TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult` (7 cases: 409 x3, 403, 404 x3) | zombie isolation: abort and no result | m02 m03 m04 m05 |
| `TestWorker_Client_RenewOldCoordinatorGracefulDegradation` (3 cases) | unrecognised 404 keeps the task running, one renewal only, result reported | m14 |
| `TestWorker_Client_Renew400KeepsRunning` (2 cases) | 400 keeps the task running | m15 |
| `TestWorker_Renewal_TransientErrorsNeverAbortAndAreBackedOff` | 503, 500, dropped connection never abort; bounded, capped backoff; back to interval | m16 m17 m18 m19 |
| `TestWorker_Client_ResultDeliveryLoop_BoundedRetry` | 60 s budget, bounded attempts, delivery backoff bounds | m09 m10 m11 m23 |
| `TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success` | 45 s outage then success inside the budget | m01 m23 |
| `TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport` | renewals continue while the result is retried, stop afterwards | m23 m24 |
| `TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace` | a black-holed result request is cut off at the budget | m09 m10 |
| `TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately` (5 cases) | 409/403/404/400 on delivery: exactly one attempt, no clock movement | m12 m13 |
| `TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying` | lost lease during delivery or backoff stops retries | m04 m26 m27 |
| `TestWorker_ExecuteAndReport_Shutdown_StopsRenewalAndReturns` | shutdown ends the loop and leaves no timer | (equivalent guard removed, see deviation 7) |

End to end, real worker agent against the real coordinator handler and in-memory repository, one `FakeClock` for the repository, the service, the sweeper ticks and the agent (`tests/integration/worker_lease_e2e_test.go`):

| Test | Property |
|---|---|
| `TestWorkerLeaseE2E_RenewalsKeepLongJobAlivePastOneLease` | 70 s job (more than two leases), sweeper ticking every 5 s, job and attempt stay RUNNING, commit succeeds |
| `TestWorkerLeaseE2E_WorkerWhoseRenewalsStopIsDeclaredLostAndAborts` | renewals dropped (503): worker keeps running, sweeper marks attempt LOST and job RETRYING; coordinator reachable again: the zombie's next renewal is 409 and it aborts without reporting |
| `TestWorkerLeaseE2E_CancelDeliveredThroughRenewalStopsTask` | `CancelJob` reaches the task only at the next renewal; outcome reported, attempt closed |
| `TestWorkerLeaseE2E_DeliveryBudgetExhaustedThenLeaseExpiryReclaimsJob` | results undeliverable: lease kept alive by renewals for 60 s, worker gives up, renewals stop, lease lapses, sweeper reclaims (RETRYING/LOST) |

Plan gating tests of the worker side of 4c, status: `TestAgent_PeriodicLeaseRenewal_Success`, `TestAgent_LeaseRenewal_ReceivesCancelRequested`, `TestWorker_Client_RenewOldCoordinatorGracefulDegradation`, `TestWorker_Client_Renew400KeepsRunning`, `TestWorker_Client_ResultDeliveryLoop_BoundedRetry`, `TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success`, `TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport`, `TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace`, `TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately`: all **executed** (names verified with `go test -list`). The plan's "worker zombie isolation test" and "cooperative cancellation test" mutation targets are `TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult` and `TestAgent_LeaseRenewal_ReceivesCancelRequested`.

Docs pass: the plan still labels these nine names "(planned test)"; `make test-docs` raised nothing, so I edited no doc. The docs pass should relabel them as executed.

## Red before green

The worker tests were committed before the implementation, against the old `ExecuteAndReport` (no renewal loop, no abort, fixed five-attempt retry). First run on the old code, verbatim:

```
--- FAIL: TestAgent_PeriodicLeaseRenewal_Success (5.00s)
--- FAIL: TestWorker_Renewal_IntervalDerivedFromAssignment (20.01s)
    --- FAIL: TestWorker_Renewal_IntervalDerivedFromAssignment/30_s_lease_renews_every_10_s (5.00s)
    --- FAIL: TestWorker_Renewal_IntervalDerivedFromAssignment/9_s_lease_renews_every_3_s (5.00s)
    --- FAIL: TestWorker_Renewal_IntervalDerivedFromAssignment/missing_duration_falls_back_to_10_s (5.00s)
    --- FAIL: TestWorker_Renewal_IntervalDerivedFromAssignment/tiny_lease_is_floored_at_1_s (5.00s)
--- FAIL: TestAgent_LeaseRenewal_ReceivesCancelRequested (5.00s)
--- FAIL: TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult (35.03s)
    --- FAIL: TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult/409_JOB_ALREADY_TERMINAL (5.01s)
    --- FAIL: TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult/409_LEASE_EXPIRED (5.01s)
    --- FAIL: TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult/409_STALE_ATTEMPT_REJECTED (5.00s)
    --- FAIL: TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult/403_WORKER_MISMATCH (5.00s)
    --- FAIL: TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult/404_JOB_NOT_FOUND (5.00s)
    --- FAIL: TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult/404_ATTEMPT_NOT_FOUND (5.00s)
    --- FAIL: TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult/404_WORKER_NOT_FOUND (5.00s)
--- FAIL: TestWorker_Client_RenewOldCoordinatorGracefulDegradation (15.02s)
    --- FAIL: TestWorker_Client_RenewOldCoordinatorGracefulDegradation/plain_text_404_from_a_coordinator_without_the_route (5.00s)
    --- FAIL: TestWorker_Client_RenewOldCoordinatorGracefulDegradation/404_with_an_unrecognised_error_code (5.01s)
    --- FAIL: TestWorker_Client_RenewOldCoordinatorGracefulDegradation/404_with_an_empty_body (5.00s)
--- FAIL: TestWorker_Client_Renew400KeepsRunning (10.00s)
    --- FAIL: TestWorker_Client_Renew400KeepsRunning/INVALID_REQUEST (5.00s)
    --- FAIL: TestWorker_Client_Renew400KeepsRunning/WORKER_TASK_UNSUPPORTED (5.00s)
--- FAIL: TestWorker_Renewal_TransientErrorsNeverAbortAndAreBackedOff (5.00s)
--- FAIL: TestWorker_Client_ResultDeliveryLoop_BoundedRetry (5.00s)
--- FAIL: TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success (5.00s)
--- FAIL: TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport (5.00s)
--- FAIL: TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace (5.00s)
--- FAIL: TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately (25.04s)
    --- FAIL: TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately/409_JOB_ALREADY_TERMINAL (5.00s)
    --- FAIL: TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately/409_LEASE_EXPIRED (5.00s)
    --- FAIL: TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately/403_WORKER_MISMATCH (5.01s)
    --- FAIL: TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately/404_JOB_NOT_FOUND (5.00s)
    --- FAIL: TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately/400_INVALID_REQUEST (5.01s)
--- FAIL: TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying (5.00s)
--- FAIL: TestWorker_ExecuteAndReport_Shutdown_StopsRenewalAndReturns (5.00s)
FAIL
FAIL	github.com/BlaZe08-Dev/Minicloud/internal/worker	151.732s
FAIL
```

Most red failures are "renewal timer never armed" or "ExecuteAndReport did not return": the old code has no renewal loop. `TestWorker_ExecuteAndReport_Shutdown_StopsRenewalAndReturns` and the integration tests were written after the implementation.

## Commands run + real output

Baseline before any change (HEAD 85b2246), then final state. Counts use top-level `--- PASS` lines for the unset run and all-level lines for `make test-db`, as in the task.

```
== preflight
test URL: postgres://***@127.0.0.1:5433/minicloud_claude_test?sslmode=disable
test DB: minicloud_claude_test
preflight OK
== baseline gofmt / vet / build / docs
GOFMT OK
vet=0
build=0
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	0.982s
docs=0
== baseline unset DB URL (go test -v -race -count=1 ./...): PASS 151 SKIP 35 FAIL 0
== baseline make test-db: PASS 180 FAIL 0
== final gofmt / vet / build / docs
GOFMT OK
vet=0
build=0
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	0.774s
docs=0
== final unset DB URL: PASS 170 SKIP 35 FAIL 0
ok  	github.com/BlaZe08-Dev/Minicloud/cmd/coordinator	1.496s
ok  	github.com/BlaZe08-Dev/Minicloud/cmd/migrate	1.032s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api	4.502s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/config	1.026s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator	1.169s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/domain	1.153s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/executor	1.080s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/queue	1.025s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/scheduler	1.026s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage	1.498s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres	1.134s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/worker	3.266s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/architecture	1.032s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	11.703s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	7.875s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/review	1.048s
== final make test-db (minicloud_claude_test): PASS 180 FAIL 0
    --- PASS: TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit/control_without_grace_reaps_and_rejects_the_late_commit (0.51s)
PASS
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	2.138s
== go test -race -count=20 ./internal/worker/
ok  	github.com/BlaZe08-Dev/Minicloud/internal/worker	44.797s
== go test -race -count=10 (end-to-end tests only, selected by name pattern) ./tests/integration/
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	2.427s
```

Counts: unset run PASS 151 to 170 (+19), SKIP 35 to 35, FAIL 0 to 0; `make test-db` PASS 180 to 180 (it does not run the worker or the in-memory end-to-end tests; those are in the unset run and are database-free), FAIL 0. No test was skipped, deleted or weakened. The database URL was never set for `./...`; `make test-db` was used only.

## Mutation proofs

Throwaway worktree under `~/mc-mut-claude` (`.env` copied in, worktree and `.env` removed afterwards). Each mutation edits `internal/worker/client.go`, runs `go test -race -count=1 ./internal/worker/ ./tests/integration/` restricted by `-run` to the agent, worker and end-to-end lease tests, with the database URL unset, then restores the file and re-runs (rc 0). `rc=124` is the 150 s timeout (killed by an unbounded test run, which also failed the tests named). Run 1 (code at the commit before the last one) is first; survivors m08, m11, m19 were then fixed with new tests, and m25, m29 were equivalent mutants whose guards I deleted (deviation 7). Run 2 re-ran every mutation that touches code changed since, on the final commit.

### Run 1 (verbatim)
```
m01-renewal-never-sent: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestAgent_LeaseRenewal_ReceivesCancelRequested, TestAgent_PeriodicLeaseRenewal_Success, TestWorkerLeaseE2E_CancelDeliveredThroughRenewalStopsTask, TestWorkerLeaseE2E_DeliveryBudgetExhaustedThenLeaseExpiryReclaimsJob, TestWorkerLeaseE2E_RenewalsKeepLongJobAlivePastOneLease, TestWorkerLeaseE2E_WorkerWhoseRenewalsStopIsDeclaredLostAndAborts, TestWorker_Client_Renew400KeepsRunning, TestWorker_Client_RenewOldCoordinatorGracefulDegradation, TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying, TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult, TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport, TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success, TestWorker_Renewal_TransientErrorsNeverAbortAndAreBackedOff
m02-lost-treated-as-ok: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorkerLeaseE2E_WorkerWhoseRenewalsStopIsDeclaredLostAndAborts, TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying, TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult
m03-lost-does-not-cancel-task: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorkerLeaseE2E_WorkerWhoseRenewalsStopIsDeclaredLostAndAborts, TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult
m04-lost-still-reports: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying, TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult
m05-403-not-lost: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult
m06-cancel-flag-ignored: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestAgent_LeaseRenewal_ReceivesCancelRequested, TestWorkerLeaseE2E_CancelDeliveredThroughRenewalStopsTask
m07-cancel-does-not-cancel-task: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestAgent_LeaseRenewal_ReceivesCancelRequested, TestWorkerLeaseE2E_CancelDeliveredThroughRenewalStopsTask
m08-cancel-keeps-renewing: mutated rc=0 (want 1), restored rc=0 (want 0)  killed-by: NONE
m09-budget-never-fires: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorkerLeaseE2E_DeliveryBudgetExhaustedThenLeaseExpiryReclaimsJob, TestWorker_Client_ResultDeliveryLoop_BoundedRetry, TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace
m10-budget-120s: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorkerLeaseE2E_DeliveryBudgetExhaustedThenLeaseExpiryReclaimsJob, TestWorker_Client_ResultDeliveryLoop_BoundedRetry, TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace
m11-delivery-no-backoff: mutated rc=0 (want 1), restored rc=0 (want 0)  killed-by: NONE
m12-409-retried-on-delivery: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately
m13-400-retried-on-delivery: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately
m14-unrecognised-404-is-lost: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_Client_RenewOldCoordinatorGracefulDegradation
m15-renew-400-is-lost: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_Client_Renew400KeepsRunning
m16-transient-aborts: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorkerLeaseE2E_WorkerWhoseRenewalsStopIsDeclaredLostAndAborts, TestWorker_Renewal_TransientErrorsNeverAbortAndAreBackedOff
m17-transport-error-aborts: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_Renewal_TransientErrorsNeverAbortAndAreBackedOff
m18-renew-retry-no-backoff: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_Renewal_TransientErrorsNeverAbortAndAreBackedOff
m19-renew-retry-uncapped: mutated rc=0 (want 1), restored rc=0 (want 0)  killed-by: NONE
m20-interval-half: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestAgent_LeaseRenewal_ReceivesCancelRequested, TestAgent_PeriodicLeaseRenewal_Success, TestWorkerLeaseE2E_CancelDeliveredThroughRenewalStopsTask, TestWorker_Client_Renew400KeepsRunning, TestWorker_Client_RenewOldCoordinatorGracefulDegradation, TestWorker_Client_ResultDeliveryLoop_BoundedRetry, TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying, TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult, TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport, TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success, TestWorker_Renewal_IntervalDerivedFromAssignment, TestWorker_Renewal_TransientErrorsNeverAbortAndAreBackedOff
m21-interval-ignores-assignment: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_Renewal_IntervalDerivedFromAssignment
m22-no-interval-floor: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_Renewal_IntervalDerivedFromAssignment
m23-renewal-stops-before-delivery: mutated rc=124 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_Client_ResultDeliveryLoop_BoundedRetry, TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying, TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport, TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success
m24-renewal-not-awaited-or-stopped: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestAgent_PeriodicLeaseRenewal_Success, TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport
m25-skip-lost-check-before-delivery: mutated rc=0 (want 1), restored rc=0 (want 0)  killed-by: NONE
m26-lost-during-delivery-ignored: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying
m27-lost-during-backoff-keeps-retrying: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying
m28-renew-body-token-swapped: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestAgent_PeriodicLeaseRenewal_Success, TestWorkerLeaseE2E_CancelDeliveredThroughRenewalStopsTask, TestWorkerLeaseE2E_DeliveryBudgetExhaustedThenLeaseExpiryReclaimsJob, TestWorkerLeaseE2E_RenewalsKeepLongJobAlivePastOneLease, TestWorkerLeaseE2E_WorkerWhoseRenewalsStopIsDeclaredLostAndAborts
m29-shutdown-ignored-by-delivery: mutated rc=0 (want 1), restored rc=0 (want 0)  killed-by: NONE
```

### Run 2 on the final code (verbatim)
```
m04-lost-still-reports: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying, TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult
m08-cancel-keeps-renewing: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestAgent_LeaseRenewal_CancelRequested_StopsRenewingEvenIfTaskIgnoresCancel
m09-budget-never-fires: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorkerLeaseE2E_DeliveryBudgetExhaustedThenLeaseExpiryReclaimsJob, TestWorker_Client_ResultDeliveryLoop_BoundedRetry, TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace
m10-budget-120s: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorkerLeaseE2E_DeliveryBudgetExhaustedThenLeaseExpiryReclaimsJob, TestWorker_Client_ResultDeliveryLoop_BoundedRetry, TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace
m11-delivery-no-backoff: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_Client_ResultDeliveryLoop_BoundedRetry
m12-409-retried-on-delivery: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately
m13-400-retried-on-delivery: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately
m19-renew-retry-uncapped: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_Renewal_TransientErrorsNeverAbortAndAreBackedOff
m23-renewal-stops-before-delivery: mutated rc=124 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_Client_ResultDeliveryLoop_BoundedRetry, TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying, TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport, TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success
m26-lost-during-delivery-ignored: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying, TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult
m27-lost-during-backoff-keeps-retrying: mutated rc=1 (want 1), restored rc=0 (want 0)  killed-by: TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying
```

Mutation to plan mapping: "renewal not sent" m01; "lease-lost not aborting" m02 m03 m05; "result still reported after lease loss" m04 m26 m27; "cancel not delivered" m06 m07; "delivery budget ignored or unbounded" m09 m10 m23; "delivery retried on terminal rejection" m12 m13. m14 to m22, m24 and m28 pin the degradation, 400, transient and interval behaviours.

## Known gaps / TODOs

- `Agent.Run` still polls with `time.Sleep`; only renewal and delivery timers are injectable. The poll loop is outside this slice.
- Task execution timeouts still use wall-clock `context.WithTimeout` in `executor.Registry.Execute` (SEC-012 clamp is slice 4d). A test that needs a task to time out would take real time; none does.
- No test against a real PostgreSQL or a real database outage: the hard rule forbids restarting the shared container, and the end-to-end tests are in-memory by design. The plan's "result finished during a 45 s outage commits within 60 s" is shown by `TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success` (a 45 s simulated 503 window) and not against a real outage (that is slice 4e, `TestFault_PostgresOutage_DuringLeaseExecution`, planned).
- Real-time behaviour of the production `realClock` is not exercised by a test (only through the unchanged poll loop tests). It is a thin wrapper over `time.NewTimer`.
- The 15 s `http.Client` timeout bounds a hung renewal request to the interval (the request context is capped at the interval); a hung result request is cut at the budget by the delivery context.
- `ComputeBackoffWithJitter` is unseeded `math/rand` (existing); tests assert bounds, not values.

## Open questions for the orchestrator

- **Q1 (404 on renew, deviation 1).** The task text says 404 is terminal; the plan, protocol and ADRs say recognised code terminal, unrecognised or missing envelope degrades. I implemented the design docs. Confirm, or tell me to make every 404 terminal (that would drop the old-coordinator degradation test and contradict protocol 6.4).
- **Q2 (retry cadence, deviation 2).** Confirm bounded backoff (at most one interval) for transient renewal failures instead of waiting a whole interval; the plan and protocol 6.1 say "next interval".
- **Q3 (cancelled outcome).** After `cancel_requested` the worker reports exit code 1 and `context canceled` and, until slice 4d, the coordinator turns a failure on `CANCEL_REQUESTED` into a failed job rather than a cancelled one. ADR-0016 section 4 and protocol 6.1 say the worker reports; slice 4d owns the commit outcome. No worker change is needed for 4d unless 4d wants an explicit classification (`error_classification`, protocol 6.2), which this slice does not send.
- **Q4 (interval ceiling).** Should the worker clamp the renewal interval to a maximum (for example the default 10 s) so a coordinator that advertises a very long lease cannot make the worker renew too rarely? The design is silent; I left it unclamped (F-4c-b-1).
- **Q5 (docs pass).** The plan's nine worker-side gating names should be relabelled from "(planned test)" to executed. I did not edit design docs. The plan's mutation-proof wording "worker zombie isolation test" and "cooperative cancellation test" can cite the two names given above.
- **Q6 (budget start).** The 60 s delivery budget starts when the task returns, not at lease expiry; protocol 6.1 and the plan agree, and the test name `..._BoundedTimeout_GivesUpAfterLeaseAndGrace` suggests lease plus grace (30 s plus 30 s). The two coincide only with default settings; the budget is a constant. Say if it should derive from the configured lease and outage window (the worker does not know the window).

## Security review

**Assumptions challenged**

- A1 *The renewal response is trustworthy.* The worker obeys it: `cancel_requested`, 409, 403 and a recognised 404 stop work. An attacker who can answer HTTP for the coordinator (SEC-004, no authentication, 127.0.0.1 default) can kill any running attempt. Within the accepted SEC-004 gap; not a new finding.
- A2 *Lease loss must stop the result.* A zombie reporting after reassignment is exactly invariant 3's threat. The worker drops the result on lease loss, and the coordinator still rejects it if it arrives (the 4b conformance). **executed**: `TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult`, `TestWorkerLeaseE2E_WorkerWhoseRenewalsStopIsDeclaredLostAndAborts`.
- A3 *Transient errors must not kill work.* A coordinator or database outage must not abort every running attempt. **executed**: `TestWorker_Renewal_TransientErrorsNeverAbortAndAreBackedOff` (500, 503, dropped connection).
- A4 *The retry loops must not become a DoS tool.* Renewal retry waits at least 0.5 s and at most one interval; delivery retries back off to about 3.2 s (the existing 16x multiplier cap; the 5 s ceiling is never reached) and stop at 60 s. **executed**: the backoff-bound assertions in the two tests above and the m11, m18, m19 mutations.
- A5 *Goroutine leaks.* The renewal goroutine is stopped and awaited on every exit path. **executed**: pending-timer assertions after completion, after cancel, after shutdown, plus `-count=20 -race`.
- A6 *Secrets in logs.* The lease token is sent in the request body and never logged; errors from the HTTP client contain the URL (job and attempt ids, no token). **confirmed by code reading** (`renewOnce`, `deliverResult`).
- A7 *A 200 with a hostile body.* The body is read through a 64 KiB limit and parsed for `cancel_requested` only; an unreadable body is transient. **confirmed by code reading**; not fuzzed.

**Attacks tried (real tests)**: every row of protocol 6.3 on renew (`TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult`), a plain-text 404 and a JSON 404 with an unknown code from an "old coordinator" (`TestWorker_Client_RenewOldCoordinatorGracefulDegradation`), 400 on renew (`TestWorker_Client_Renew400KeepsRunning`), a black-holed result request (`TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace`), a result endpoint answering 503 forever (`TestWorker_Client_ResultDeliveryLoop_BoundedRetry`), terminal rejections on delivery (`TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately`), the lease lost mid-delivery (`TestWorker_ExecuteAndReport_LeaseLostDuringDelivery_StopsRetrying`), an uncooperative task that ignores cancellation (`TestAgent_LeaseRenewal_CancelRequested_StopsRenewingEvenIfTaskIgnoresCancel`), a worker that goes silent and then returns (`TestWorkerLeaseE2E_WorkerWhoseRenewalsStopIsDeclaredLostAndAborts`), a worker that cannot deliver and gives up (`TestWorkerLeaseE2E_DeliveryBudgetExhaustedThenLeaseExpiryReclaimsJob`).

**Not tested**: a real network partition or process kill of a worker (slice 4e fault suite); a real database outage; many concurrent attempts on one agent (the agent runs one assignment at a time in `Run`, the renewal state is per call); a hostile `lease_duration_seconds` (negative values fall to the default, huge values are unclamped); a result delivery that is accepted by the coordinator but whose response is lost (the retry then gets 409 `JOB_ALREADY_TERMINAL` and stops, which is correct but is not asserted end to end); clock jumps of the real clock; HTTP redirects.

**Findings**

- **F-4c-b-1 (Low, within SEC-004).** The renewal interval is one third of whatever `lease_duration_seconds` the coordinator advertises, with a 1 s floor and no ceiling. A coordinator (or an attacker answering as one) that advertises a very long lease makes the worker renew rarely; if the real lease is shorter, the attempt is reaped while the worker believes it is alive. Evidence: **confirmed by code reading** (`renewalInterval`); the floor is pinned by `TestWorker_Renewal_IntervalDerivedFromAssignment`, the absence of a ceiling is not tested. Suggested: clamp to a maximum, orchestrator decision (Q4).
- **F-4c-b-2 (Info).** After `cancel_requested` the worker stops renewing but still reports a result on an unextended lease, so a slow result delivery (up to 60 s) can outlive the lease and race the sweeper, which reaps a `CANCEL_REQUESTED` job as `CANCELLED`. Outcome is safe (the late commit gets 409, the worker stops), design-intended (ADR-0016 race 2). Evidence: **confirmed by code reading** and by the design docs; the sequence is not exercised end to end.
- **F-4c-b-3 (Info).** A result can be in flight when the lease is declared lost; the worker cannot recall it. The coordinator rejects it (invariant 3). Evidence: **inferred**.

No new finding was edited into `docs/security/findings.md` (not owned). Next free number is SEC-029; the three findings above are labelled F-4c-b-N for the docs pass to number.
