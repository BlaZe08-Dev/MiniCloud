# Review P4-4c-b

Verdict: **APPROVED**
Commit reviewed: `6268073` (Round 2 tip: `docs(handoff): P4-4c-b round 2 (interval ceiling, production wiring proof, real-binary smoke run)`)
Branch: `agent/claude/P4-4c-b` (audited on `agent/gemini/P4-4c-b-review`)
Base: `85b2246` (Slice 4c-a, merged to main as PR #21)

---

## 1. Executive Summary

Task P4-4c-b implements the worker-side lease renewal and recovery lifecycle for Phase 4 Slice 4c:
1. Dedicated background renewal loop per executing attempt ticking at `clamp(lease_duration_seconds / 3, 1 s, 10 s)` (falling back to 10 s for absent/zero/negative leases).
2. Immediate task cancellation and result suppression upon lease loss (HTTP 409 Conflict, 403 Forbidden, or 404 Not Found with recognized error codes `JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`).
3. Graceful degradation on missing renew route (404 without recognized error code from legacy coordinators) or 400 Bad Request: warning/error logged once, renewals disabled, task runs to completion and result delivered.
4. Transient renewal failures (HTTP 5xx, network drops, 503 database outages) retried via bounded exponential backoff (base 1 s, capped at the renewal interval) without aborting the task or spinning.
5. Cooperative cancellation delivery exclusively via `cancel_requested: true` in the renewal response, immediately cancelling the task context, stopping renewals (allowing the unextended lease to lapse), and reporting the finished execution outcome.
6. Result delivery bounded by a strict 60 s retry budget (`ResultDeliveryBudget`) on the injected clock, retrying with exponential backoff and jitter, cutting off in-flight requests at expiry, continuing renewals while delivering, and halting immediately on terminal rejections (400, 403, 404, 409), lease loss, or shutdown.
7. Test harness doubles (`workertest.FakeClock`) completely isolated in `internal/worker/workertest` with production `Agent` defaulting to real timers, and `ExecuteAndReport` exported to allow deterministic driving of individual assignments.
8. Wiring verified through `TestAgent_Run_ProductionPathStartsRenewals` and a full end-to-end real-binary smoke test including positive run, negative control (renewal disabled), and mid-job worker kill.

All quality gates pass (gofmt, vet, build, make test-docs, race detector count=20 on worker, count=10 on integration, DB-unset test suite count: PASS 172 SKIP 35 FAIL 0).

---

## 2. Handoff Claims Re-Run

All commands were re-run by the Auditor in the fresh audit clone (`~/mc-gemini-audit-4cb`) with `MINICLOUD_TEST_DATABASE_URL` unset. Full raw outputs are recorded in `docs/agents/reviews/P4-4c-b-evidence.txt`.

| Claim in Handoff | Command Run | Real Output / Result | Match? |
|---|---|---|---|
| Go formatting clean | `gofmt -l .` | Empty output (exit 0) | Yes |
| Go vet clean | `go vet ./...` | Empty output (exit 0) | Yes |
| Go build clean | `go build ./...` | Empty output (exit 0) | Yes |
| Documentation links and anchors valid | `make test-docs` | `ok github.com/BlaZe08-Dev/Minicloud/tests/docs` (exit 0) | Yes |
| DB-unset suite test counts | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...` | `PASS 172 SKIP 35 FAIL 0` | Yes |
| Worker package stress test | `go test -race -count=20 ./internal/worker/` | `ok github.com/BlaZe08-Dev/Minicloud/internal/worker 100.081s` | Yes |
| Integration e2e stress test | `go test -race -count=10 ./tests/integration/...` | `ok github.com/BlaZe08-Dev/Minicloud/tests/integration 70.563s` | Yes |
| Scope clean | `git diff --name-only origin/main...HEAD` | Only `internal/worker`, `tests`, and handoff | Yes |
| Interval ceiling fix (F-4c-b-1) | `go test -v -run TestWorker_Renewal_IntervalDerivedFromAssignment ./internal/worker/` | Both 3600 s and 31 s clamped to 10 s: PASS | Yes |
| Production path wiring | `go test -v -run TestAgent_Run_ProductionPathStartsRenewals ./internal/worker/` | `Agent.Run` starts renewals on production path: PASS | Yes |

---

## 3. Findings

| ID | Severity | Location | Evidence Label | Evidence & Description | Concrete Fix |
|---|---|---|---|---|---|
| **F-4c-b-A1** | Nit | `internal/worker/client.go:renewalInterval` | executed | For advertised leases under 3 s (e.g. 1 s or 2 s leases in edge-case testing), `renewalInterval` floors the interval at 1 s (`MinRenewalInterval`), so the renewal interval is equal to or greater than half the lease duration rather than one-third. This safely avoids spamming the coordinator renew route (<1 req/s) and matches the accepted owner decision for Q7 (no minimum lease duration enforced in config). | None required. Keep 1 s floor. Test suites requiring the exact 1/3 ratio should configure leases >= 3 s. |

*Note on Builder's Findings:*
- **F-4c-b-1** (interval ceiling missing): **Fixed in Round 2**. Verified by `TestWorker_Renewal_IntervalDerivedFromAssignment` and `TestAgent_LeaseRenewal_LongAdvertisedLease_CancelStillReachesTaskWithin10s`.
- **F-4c-b-2** (unextended lease during slow cancel delivery races sweeper): **Info**. Confirmed design-intended per ADR-0016 Race 2 and protocol section 6.1. If sweeper reaps to CANCELLED, late result gets 409 `JOB_ALREADY_TERMINAL`, which worker treats as non-retryable and halts.
- **F-4c-b-3** (in-flight result upon lease-lost rejection): **Info**. Confirmed design-intended per invariant 3. Coordinator rejects stale results with 409.

---

## 4. Standing Checks

- **Branch Hygiene**: Work performed on `agent/gemini/P4-4c-b-review` in fresh clone `~/mc-gemini-audit-4cb`. Neither `main` nor `MiniCloud` nor `MiniCloud-gemini` was touched.
- **Source of Truth**: PostgreSQL remains the source of truth. No Redis or unauthorized dependencies introduced.
- **Field & Runtime Limits**: Field limits preserved (`io.LimitReader(resp.Body, 64<<10)` bounds renewal response bodies to 64 KiB, invariant 4; timeouts bounded by context).
- **Lease Tokens**: Renewal requests include `lease_token` in body, but tokens are never written to log lines.
- **Destructive Commands**: No database drops, container resets, or shared volume deletions.
- **Evidence Integrity**: All captured outputs generated by direct command redirection (`>> docs/agents/reviews/P4-4c-b-evidence.txt 2>&1`) with real timestamps and PIDs.

---

## 5. Attacks Attempted That Did NOT Find Issues

Every item from the audit checklist was independently attacked and verified:

1. **Renewal Interval Derivation and Clamping (Checklist Item 1)** [evidence: executed]:
   - Tested negative lease durations (`-5 s`), zero duration (`0 s`), huge durations (`3600 s`, `1000000 s`), and tiny leases (`1 s`). Negative and zero fall back to 10 s; huge values clamp to 10 s ceiling; 1 s floors at 1 s.
   - Goroutine leaks investigated: `a.renewLoop` is launched with `wg.Add(1)` and deferred `wg.Wait()`. On every exit path (success, failure, cancel, lease lost, 400 disable, shutdown, delivery budget expiry), `stopRenew()` cancels context and timers are stopped. Stress test under race detector (`go test -race -count=20`) passed with zero goroutine leaks or race conditions.
2. **Lease Loss Rejections and Legacy Degradation (Checklist Item 2)** [evidence: executed & confirmed by code reading]:
   - Verified that HTTP 409 (`JOB_ALREADY_TERMINAL`, `LEASE_EXPIRED`, `STALE_ATTEMPT_REJECTED`), HTTP 403 (`WORKER_MISMATCH`), and HTTP 404 with recognized error codes (`JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`) immediately abort the task context, mark the run lost, and suppress result reporting without making a `POST /result` request.
   - Verified HTTP 403 is an explicitly documented renew rejection in `docs/protocol.md` sections 6.1 and 6.3 (Priority 4) and ADR-0014 section 3.
   - Verified that unrecognized or plain-text 404 responses log one warning, disable further renewals for the attempt, keep running the task to completion, and deliver the result normally.
3. **Transient Errors Backoff and No-Spin (Checklist Item 3)** [evidence: executed]:
   - Probed transient failures (HTTP 500, HTTP 503 database outage, dropped TCP connections via hijacking). Worker continues executing without aborting.
   - Retries follow bounded exponential backoff from 1 s with jitter factor in [0.5, 1], capped at the renewal interval.
   - Verified that when the fake clock is stationary, the loop blocks on `<-t.C()` and does not spin or hammer the coordinator.
4. **Cooperative Cancellation via Renewal Response (Checklist Item 4)** [evidence: executed]:
   - Verified that receiving `cancel_requested: true` in a 200 OK renewal response calls `cancelTask()`, immediately stops renewal requests, and reports the finished outcome.
   - Stubborn tasks ignoring context cancellation were audited: renewals remain stopped, allowing the coordinator lease to lapse so the sweeper resolves the job to `CANCELLED`. Late results arriving after sweeper resolution receive HTTP 409 `JOB_ALREADY_TERMINAL`, which `isTerminalDelivery` treats as final (exiting immediately without retry).
   - Long advertised leases (3600 s) verify the 10 s ceiling ensures `cancel_requested` reaches the running task within 10 s.
5. **Result Delivery Budget and Concurrent Renewals (Checklist Item 5)** [evidence: executed]:
   - Verified the 60 s delivery budget (`ResultDeliveryBudget`): in-flight requests are cancelled via context when the budget timer fires.
   - Loop exits immediately upon terminal status codes (400 Bad Request, 403 Forbidden, 404 Not Found, 409 Conflict), lease-lost signal, or agent shutdown.
   - On budget exhaustion, the worker logs an error and drops the result. Crucially, background renewals continue throughout `deliverResult` and only stop when `ExecuteAndReport` returns, allowing the lease to lapse naturally for coordinator reclamation.
6. **Isolated FakeClock Test Double (Checklist Item 6)** [evidence: confirmed by code reading & executed]:
   - `workertest.FakeClock` resides exclusively in `package workertest` (`internal/worker/workertest/clock.go`) and is imported only in test files.
   - `NewAgent` unconditionally defaults to `realClock{}` (thin wrapper over `time.NewTimer`). `SetClock` is mutex-protected and used only during test harness initialization.
7. **Deleted Equivalent Guard Analysis (Checklist Item 7)** [evidence: confirmed by code reading]:
   - Guard `m25` (`if run.isLost()` in `ExecuteAndReport`): Redundant because `deliverResult` unconditionally checks `if run.isLost()` on line 1 before making any network request or timer allocation.
   - Guard `m29` (`if dctx.Err() != nil { break }` before backoff timer): Redundant because the subsequent `select` immediately evaluates `case <-dctx.Done():` and breaks out cleanly. Deleting both guards eliminated unexecutable mutants without impacting safety.
8. **Real Sleeps in New Tests (Checklist Item 10)** [evidence: executed]:
   - Grepped all new tests for `time.Sleep`. The only sleeps present are 1 ms to 20 ms polling yields. Zero real sleeps above 20 ms exist in the new tests.

---

## 6. Real-Binary Smoke Execution & Controls (Checklist Item 9)

Built `bin/coordinator`, `bin/worker`, and `bin/mc` from audit clone, listening on free port `18080` with in-memory storage, 6 s lease, 1 s sweeper interval, and 6 s grace windows. All real process outputs were captured into `docs/agents/reviews/P4-4c-b-evidence.txt`:

1. **Positive Run (10 s sleep job with 6 s lease)** [evidence: executed]:
   - Coordinator PID 411215, Worker PID 411230.
   - Job `job_1_3524e2ee3cd7` submitted.
   - Remained in `RUNNING` through t+1s to t+9s (surviving past the 6 s lease due to periodic renewals).
   - At t+10s, completed as `SUCCEEDED` (exit code 0, output `{"slept_ms":10000}`). Attempt 1 marked `COMPLETED`.
   - Coordinator log confirmed: `{"level":"INFO","msg":"job result committed","job_id":"job_1_3524e2ee3cd7",...}`.
2. **Negative Control (Worker with renewal start removed from ExecuteAndReport)** [evidence: executed]:
   - Coordinator PID 411516, Worker-neg PID 411545.
   - Job `job_1_6e3b66cd6432` submitted.
   - Remained `RUNNING` until t+6s.
   - At t+7s, the 6 s lease expired without renewals: sweeper closed Attempt 1 as `LOST` and transitioned job to `RETRYING`.
   - Coordinator log confirmed: `{"level":"INFO","msg":"lease sweeper closed an expired attempt","job_id":"job_1_6e3b66cd6432","attempt_id":"att_1_233d1753ba10","attempt_state":"LOST","job_state":"RETRYING"}`.
   - Proves the positive control was valid: jobs staying past 6 s require functioning renewals.
3. **Mid-Job Worker Kill (`kill -9`)** [evidence: executed]:
   - Coordinator PID 411828, Worker PID 411857.
   - Job `job_1_d089ca249df5` submitted.
   - Worker process killed with `kill -9 411857` at t+2s while job was running.
   - At t+5s, sweeper detected lease expiry, closed attempt as `LOST`, and transitioned job to `RETRYING`.
   - Coordinator log confirmed: `CONFIRMED: No job result committed` for the killed attempt.
4. **Process Cleanup**:
   - All PIDs terminated cleanly; verified via `ps` and `ss -ltn` that port 18080 closed.

---

## 7. Mutation Spot Checks (Checklist Item 8)

Executed in a throwaway copy (`~/mc-gemini-mut`), verified failing when broken and passing when restored:

| Mutation | Description | Broken Test Result | Restored Test Result | Proof / Killed By |
|---|---|---|---|---|
| **m-spot-1** | Renewal never sent (`switch renewOK`) | FAIL (exit 1) | PASS (exit 0) | `TestWorkerLeaseE2E_DeliveryBudgetExhaustedThenLeaseExpiryReclaimsJob` |
| **m-spot-2** | Lease-lost not aborting task (`cancelTask()` omitted) | FAIL (exit 1) | PASS (exit 0) | `TestWorker_ExecuteAndReport_LeaseLost_AbortsAndSuppressesResult` |
| **m-spot-3** | `cancel_requested` not delivered to task (`cancelTask()` omitted) | FAIL (exit 1) | PASS (exit 0) | `TestAgent_LeaseRenewal_ReceivesCancelRequested`, `TestWorkerLeaseE2E_CancelDeliveredThroughRenewalStopsTask` |
| **m-spot-4** | Result delivery budget unbounded (budget set to 24 h) | FAIL (exit 1) | PASS (exit 0) | `TestWorker_Client_ResultDeliveryLoop_BoundedRetry`, `TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace` |
| **m-spot-5** | Interval ceiling removed (`min(..., 10s)` removed) | FAIL (exit 1) | PASS (exit 0) | `TestWorker_Renewal_IntervalDerivedFromAssignment`, `TestAgent_LeaseRenewal_LongAdvertisedLease_CancelStillReachesTaskWithin10s` |
| **m-spot-6** | Renewal start removed from production path | FAIL (exit 1) | PASS (exit 0) | `TestAgent_Run_ProductionPathStartsRenewals` |

---

## 8. Scope Verification (Checklist Item 11)

Output of `git diff --name-only origin/main...HEAD`:
- `docs/agents/handoffs/P4-4c-b.md`
- `internal/worker/client.go`
- `internal/worker/renewal_test.go`
- `internal/worker/run_wiring_test.go`
- `internal/worker/workertest/clock.go`
- `tests/integration/worker_lease_e2e_test.go`

Untouched: `internal/coordinator`, `internal/api`, `internal/storage`, `internal/domain`, `internal/config`, `cmd/worker`, design docs, `TASKS.md`, `CHANGELOG.md`, `PROJECT_STATE.md`. Scope is strictly compliant with the task assignment.

---

## 9. Tests Added on Review Branch

No tests added. All mutation checks were verified in throwaway copies, and the builder's unit and end-to-end integration tests comprehensively cover the specification and attack surface.

---

## 10. Recommendation

Branch `agent/claude/P4-4c-b` is thoroughly tested, robust, and completely adheres to ADR-0014, ADR-0016, and protocol.md 6.1/6.3. The branch is recommended for merge into `main` by the repository owner.
