# Review P4-05 round 3

Commit reviewed: `ab76a8d88f2ea2dcf14c6a68e53aa3a1074cc8bd` (tip of `origin/agent/gemini/P4-05`, docs-only, rounds 4 and 5 of the builder applied). Auditor branch: `agent/claude/P4-05-review-3`. Evidence: `docs/agents/reviews/P4-05-round3-evidence.txt` (section names in square brackets below). Fresh HTTPS clone, no `.env`, no database test run, `minicloud-postgres` untouched, `MINICLOUD_TEST_DATABASE_URL` unset for every Go command. Earlier P4-05 review rounds live on other branches (`agent/claude/P4-05-review`, `-review-2`), so this file takes the number 3.

Verdict: **ACCEPT WITH FIXES.** All seven prescribed checks are confirmed; no item is wrong. Item 8 (anything else) found three small imprecisions, none of which contradicts a gate or a decision; they can be fixed after merge.

Labels: **executed**, **confirmed by code reading**.

## 1. Scope (confirmed, executed) [e1, e1b]

- `git diff origin/main...HEAD` outside `docs/`, `CHANGELOG.md`, `reports/`: 0 bytes, 0 file names.
- 14 new files under `docs/agents/reviews/` (P0-03 round2/round3 md+evidence, P4-03 x4, P4-4b x2, P4-4c-a x2, P4-4c-b x2).
- `cmp` of the four P0-03 round2/round3 records against `origin/agent/claude/P0-03-review-2` and `-review-3`: all four IDENTICAL.

## 2. ADR-0014 label and the five orchestrator decisions (confirmed) [e2, e2b, e2c, e2d]

ADR-0014 labels "no minimum lease" as an orchestrator decision answering Q7 of the slice 4c-b handoff, and `docs/phase4-plan.md` has the section with exactly five numbered decisions.

| Decision | Check | Result |
|---|---|---|
| 1. 60 s delivery budget | `ResultDeliveryBudget = 60 * time.Second` in the worker client | confirmed |
| 2. Effective max retry backoff 45 s | job-retry policy: cap 30 s, jitter in [0.5, 1.5], cap applied before jitter (`internal/domain/retry.go`, `DefaultRetryPolicy`) so 30 x 1.5 = 45 s | confirmed (this is the job retry backoff, not the worker delivery backoff) |
| 3. Unknown attempt id on renew gives 409 `STALE_ATTEMPT_REJECTED` | `TestAPI_RenewLease_SuccessAndStaleRejection`, case "unknown attempt id is stale, as for commit"; run in memory with the database variable unset: PASS, six sub-cases PASS | confirmed, executed |
| 4. No minimum lease | config only rejects `LeaseDuration <= 0` and grace shorter than the lease; no lower bound | confirmed |
| 5. SEC-025/026/027/028 severities | findings.md rows: Low, Informational, Low, Informational | confirmed |

## 3. Worker renewal text (confirmed) [e3]

`renewOnce`: 200 with `cancel_requested` cancels; 403 and 409 give `renewLost`; 404 gives `renewLost` only when the body code is one of `JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`, otherwise a one-time warning and `renewDisable`; 400 gives `renewDisable` (task continues); other statuses and transport errors are transient. PROJECT_STATE.md, CHANGELOG.md and protocol.md (response handling and the worker action list) say exactly this. `git grep` for "exact recognised" outside frozen records finds only the P4-05 handoff, which quotes it as the superseded phrase.

## 4. failure-model delivery loop (confirmed, arithmetic done) [e3]

`ComputeBackoffWithJitter(n, 200 ms, 5 s)`: multiplier `1<<(n-1)` capped at 16. n=1..5 gives 200, 400, 800, 1600, 3200 ms; n>=6 stays at 200 ms x 16 = 3200 ms, below the 5 s cap, so the cap is never reached. Jitter factor is 0.5 + 0.5 x rand, i.e. [0.5, 1.0]. Budget is 60 s. The document states 200 ms, 5 s nominal, 3.2 s real ceiling, [0.5, 1.0], 60 s. Matches. `isTerminalDelivery` (403, 404, 409 and 400) matches the terminal list.

## 5. PROJECT_STATE slice list (confirmed) [e5]

`git log --merges` on main shows #19 e7403a9, #20 7e58d4a, #21 fc451c9, #22 3e8ab9e, #23 3553c25, #24 28560f6, #25 f13d97f, #26 9fbbbcf, #27 1e20143; PROJECT_STATE has the same pairs. `000004_leases_retries_cancellation.sql` exists; `TestDomain_ComputeBackoff_FullJitter` and `TestInMemoryRepository_LeaseRenewalAndExpiry` are named in the deferred-items text of the Slice 4b entry.

## 6. SEC-023 (confirmed) [e5]

Resolved by P4-T1 (PR #26, merge 9fbbbcf) in the findings table row, the detail section and PROJECT_STATE (twice). `isTestDatabaseName` exists in `dbname_guard_test.go`; `TestMigrator_PreExistingInvalidRow_ValidationFails` calls it (`migrator_test.go`). No live document still says the guard is missing.

## 7. Citations and gates (confirmed, executed) [e7, e7b]

- Added lines in live documents (everything under `docs/` except `docs/agents/reviews/` and `docs/agents/handoffs/`, plus CHANGELOG.md): no `.go:NNN`, no "line NNN".
- `make test-docs`: ok.
- `env -u MINICLOUD_TEST_DATABASE_URL go test -v -count=1 ./...`: top-level PASS 204, SKIP 35, FAIL 0, no failing package.

## 8. Other findings (minor)

- **R3-1 (minor, docs-only).** `CHANGELOG.md` Slice 4c design entry "Worker-Side Result Delivery Protocol" still says "500ms base to 5.0s cap" and "60s total delivery bound (lease + grace)" without the implemented-value note that the matching PROJECT_STATE.md entry carries (200 ms base, real ceiling 3.2 s, fixed 60 s budget). The two files now differ on the same entry.
- **R3-2 (minor, docs-only).** `docs/failure-model.md` (renewal path) and `docs/protocol.md` (5xx/transport bullet) say a transient renewal failure retries "on the next renewal interval". The code retries with jittered backoff from 1 s, never longer than the interval (`renewLoop`); ADR-0014 and protocol.md's later bullet state this correctly.
- **R3-3 (minor, comment in code, outside this branch's scope).** `tests/review/sec022_probe_test.go` still carries the comment "the guard SEC-023 reports missing elsewhere". Not changed by this branch; flagged for a later code task.
- Observation: the branch's own handoff (`docs/agents/handoffs/P4-05.md`) and `reports/gemini/P4-05-round4.md` and `-round5.md` contain line-number citations in prose (quoting removed or superseded text). They are not live reference documents and the checker passes, so this is not counted as wrong.

## Counts

Checks 1-7: 7 confirmed, 0 wrong. Item 8: 3 minor findings plus 1 observation. Blockers 0, majors 0.
