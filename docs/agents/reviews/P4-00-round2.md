# Review P4-00 (round 2)

## Commit reviewed
`c719880` on `origin/docs/phase4-design` (`docs(phase4): update allowlist entries count to four in
plan and handoff`). Review branch: `agent/claude/P4-00-review-2`, based on `c719880`. Scope of the
re-review: `git diff aa13a4c..HEAD` (16 commits, 20 files), checked against round 1
(`docs/agents/reviews/P4-00.md`, R-01 to R-24) and the part-3 owner decision on cancel. I edited no
design doc, no Go code and not the allowlist. Every result below was re-derived. I did not rely on
the handoff. Raw output is in `docs/agents/reviews/P4-00-round2-evidence.txt` (sections G1 to G15).

## Verdict: CHANGES REQUIRED

There is no blocker. The cancel contract now matches the code and the owner decision. SEC-022 is
recorded correctly, and the scope is clean. However, of the 24 round-1 findings only 8 are fully
resolved, 16 are partial, and none is unaddressed. Round 2 adds 11 findings: 6 major and 5 minor.
The most serious are these:
- R2-01: the slice 4a in-memory rewiring breaks seven existing coordinator tests that the plan says
  keep passing.
- R2-02: a live worker running a task that ignores `ctx` can hold a job in `CANCEL_REQUESTED`
  indefinitely, despite the "the lease bounds it" decision.
- R2-03: a failure commit on a `CANCEL_REQUESTED` job has no defined outcome.
- R2-07: the handoff's resolution table is misaligned with round 1 from R-13 on.

## Preflight and gates (executed, G1, G2)
- Preflight: `test DB:  minicloud_claude_test`, `preflight OK`. Go `go1.22.2 linux/amd64`.
- `gofmt -l .` lists nothing. `go vet ./...` exits 0. `go build ./...` exits 0. `make test-docs`
  prints `ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	0.307s`.
- `env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=1 ./...` exits 0, and every package is ok.
- `go test -v -race -count=1 ./...` (DB unset): `PASS 77 SKIP 26 FAIL 0`, which matches the
  expected 77/26/0. No DB test was run (docs-only task), so the 26 skips are not passes.

## Handoff claims re-run

| # | Claim (`docs/agents/handoffs/P4-00.md`) | Command / check | Result | Match |
|---|---|---|---|---|
| 1 | gofmt, vet, build, test-docs clean | G2 | as claimed | yes |
| 2 | PASS 77 SKIP 26 FAIL 0 | G2 | `PASS 77 SKIP 26 FAIL 0` | yes |
| 3 | Last commit before handoff `38c6dd1` (`docs/agents/handoffs/P4-00.md:5`) | `git log` | four later commits; tip `c719880` | no (stale) |
| 4 | Four probe allowlist entries added | `cat -n tests/docs/citation_allowlist.txt` | lines 19 to 22, plus 7 original entries | yes |
| 5 | "Added allowlist entries for ... review evidence files" (`docs/agents/handoffs/P4-00.md:32`) | same | no evidence-file entries exist | no |
| 6 | Entries are flagged stale by `RuleF_Allowlist_StaleFail` when the probe moves | executed simulation (G12) | 3 of 4 flagged; the 4th must stay | partly (R2-08) |
| 7 | Old drafts "claimed 202 as existing behavior", cited `P4-00-evidence.txt:32, 74` (`docs/agents/handoffs/P4-00.md:83`) | `sed -n 32p`, `sed -n 74p`, `git grep 202 aa13a4c` (G5, G4) | line 74 is blank (cancel #2 is at 34); `aa13a4c` proposed 202 as a change, it did not claim it was current | no |
| 8 | ADR-0014 cites Blueprint 3, 5, 8, 10, 13; ADR-0015 cites 5, 13 (`docs/agents/handoffs/P4-00.md:12-14`) | `git grep` (G11) | ADR-0014 cites 4, 5, 6, 10; ADR-0015 cites 5, 10 | no |
| 9 | Roadmap status "Design Accepted by Owner; Under Audit Revision" | `git diff aa13a4c..HEAD -- docs/roadmap.md` (G15) | text is "Design Accepted by Owner" | partly |
| 10 | "All 24 audit findings have been resolved" (`docs/agents/handoffs/P4-00.md:210`) | table below | 8 resolved, 16 partial | no |

## Round-1 findings

| ID | Status | Fix location (verified with `sed -n` / `git grep`) | What remains |
|---|---|---|---|
| R-01 | RESOLVED | `docs/security/findings.md:34`, `docs/security/findings.md:300-302`, `docs/security/findings.md:311`, `docs/security/findings.md:383`; `docs/phase4-plan.md:33-37` | Medium, Open (executed). The evidence block matches E1 (G13). The fix covers leaked rows through the commit path and the sweeper terminal rule, plus the stress invariant. |
| R-02 | PARTIAL | `docs/adr/0014-lease-model-and-expiry.md:140-144`, `docs/adr/0016-cancellation-semantics.md:132-133`, `docs/schema.md:386` | The rule is in place, and `CANCEL_REQUESTED` now always closes the attempt as `CANCELLED`. But `docs/schema.md:386` and `docs/phase4-plan.md:114` list the terminal job states as `COMPLETED`, `FAILED`, `CANCELLED` (R2-04). |
| R-03 | PARTIAL | `docs/adr/0015-retry-policy-and-failure-classification.md:59`, `docs/schema.md:314`, `docs/protocol.md:459` | These three agree on the four values. Left over: `docs/roadmap.md:65` ("3-tier ... `PERMANENT` vs `RETRYABLE`") and `docs/PROJECT_STATE.md:199` (`ClassRetryable`, "3-tier") (G8). |
| R-04 | PARTIAL | `docs/adr/0014-lease-model-and-expiry.md:223-227`, `docs/protocol.md:466-470`, `docs/phase4-plan.md:119-122` | The 409 order is consistent in all three: terminal, then `LOST`, then superseded/token. Still open: `docs/state-machine.md:183` maps a swept `LOST` commit to `ErrStaleAttempt`, against priority 2. `docs/failure-model.md:24` says "`STALE_ATTEMPT_REJECTED` or `LEASE_EXPIRED`" for one case. The table has no rows for 403 `WORKER_MISMATCH` or for the 404s, and no "diagnostic event written?" column. Priority 1 runs before the token check, so a forged commit closes the attempt on a terminal job. That attempt's end state appears only in `docs/security/findings.md:383`, not in ADR-0016 or the plan. |
| R-05 | PARTIAL | `docs/adr/0015-retry-policy-and-failure-classification.md:124-127`, `docs/phase4-plan.md:53-58` | The rule is unified, and the injected clock and actual `from_state` are specified. But the impact claim is wrong (R2-01). `docs/adr/0016-cancellation-semantics.md:56` still says a QUEUED cancel does `queue.Ack`, after the queue claim path is removed. |
| R-06 | PARTIAL | `docs/phase4-plan.md:44-49`, `docs/phase4-plan.md:52` | The cells are listed and marked as candidates for owner approval, and the planned HTTP test is labelled. But only two cells flip. `CANCEL_REQUESTED -> CANCEL_REQUESTED` is already invalid in `internal/domain/transition.go:19-23` and `internal/domain/transition_test.go:29-33` (G8), and the plan keeps it invalid. Even so, `docs/phase4-plan.md:45` says "Three matrix cells change validity" and `docs/phase4-plan.md:88` says "3 approved cell changes". `docs/state-machine.md:15` lists `CANCEL_REQUESTED` as an allowed next state of itself, which contradicts `docs/state-machine.md:33` and `docs/state-machine.md:40`. |
| R-07 | PARTIAL | `docs/state-machine.md:182`, `docs/security/test-plan.md:59` | `docs/phase4-plan.md:108` still reads "`RenewLease`: atomic transaction verifying attempt ownership, unexpired lease". `docs/security/test-plan.md:59` keeps "retries submission up to 5 times" next to the 60 s loop. |
| R-08 | PARTIAL | `docs/adr/0014-lease-model-and-expiry.md:121-134`, `docs/schema.md:361-376`, `docs/phase4-plan.md:112` | The SQL is consistent, `SKIP LOCKED` and `NOWAIT` are never combined, and the `<` / `>=` boundary is consistent. The grace check is still placed twice (R2-05). |
| R-09 | PARTIAL | `docs/adr/0014-lease-model-and-expiry.md:174-180` | Fixed: database-time gap measured from end to start, `/readyz` dropped, a cooldown added. Still open: the cooldown semantics leave the indefinite-postponement path open (R2-05). `docs/phase4-plan.md:29` still names "repository health checks" as a trigger. |
| R-10 | PARTIAL | `docs/adr/0014-lease-model-and-expiry.md:97-100`, `docs/protocol.md:410-417`, `docs/failure-model.md:61-66` | Renewal now keys on the body, and the citation is fixed (`docs/failure-model.md:73`, verified with `sed`, G9). Still open on the result-report path: 400, and 404 without the envelope, are in neither list (`docs/failure-model.md:56` and `docs/failure-model.md:63-65`). Today 400 is retryable, so under the 60 s loop a deterministic 400 is retried for a minute. The recognised 404 codes differ: `docs/adr/0014-lease-model-and-expiry.md:97` lists two, `docs/protocol.md:414` lists three. The 400 codes differ too: `docs/adr/0014-lease-model-and-expiry.md:98` against `docs/protocol.md:416`. |
| R-11 | PARTIAL | `docs/phase4-plan.md:163`, `docs/phase4-plan.md:186`, `docs/failure-model.md:78` | Moved to 4c. The 4c exit criteria (`docs/phase4-plan.md:167-172`) have no "result finished during a 45 s outage commits" criterion. The four delivery tests in `docs/failure-model.md:81-84` are not in the 4c gating list, which names a different one: `TestWorker_Client_ResultDeliveryLoop_BoundedRetry` (planned test). |
| R-12 | PARTIAL | `docs/adr/0015-retry-policy-and-failure-classification.md:20`, `docs/adr/0015-retry-policy-and-failure-classification.md:103`, `docs/adr/0016-cancellation-semantics.md:32-35` | Jitter and idempotent cancel are now attributed to the owner, and section 5 is quoted correctly. Wrong against the PDF (G11): `docs/adr/0014-lease-model-and-expiry.md:23` cites Section 6, which is "Scheduling Strategy and Research Question" (the lease design is at the end of section 5, on page 6, before the section 6 heading). `docs/adr/0016-cancellation-semantics.md:32` calls Section 8 "Initial HTTP API (suggested)", page 7, but the section title is "API, Data Model, and Configuration", page 8 ("Initial HTTP API (suggested)" is its first subheading). Section 10 is on pages 9 to 10, not 8 to 10 (ADR-0015:20, ADR-0016:32). `CHANGELOG.md:20` and `docs/PROJECT_STATE.md:20` justify `RETRYING -> RUNNING` with Section 13, which is the risks table and does not mention RETRYING. |
| R-13 | PARTIAL | `docs/state-machine.md:77-83`, `docs/state-machine.md:104` | Fixed: the writers are labelled and cited, `TIMED_OUT` is reserved, the alias row is fixed, and in-memory validation is planned. Left over: `docs/state-machine.md:153` ("`TIMED_OUT`: Attempt exceeded cumulative `timeout_seconds`") and `docs/adr/0015-retry-policy-and-failure-classification.md:144` (attempt "updated to `FAILED`, `LOST`, or `TIMED_OUT`") still describe it as written. |
| R-14 | RESOLVED | `docs/adr/0014-lease-model-and-expiry.md:51`, `docs/adr/0014-lease-model-and-expiry.md:53`, `docs/phase4-plan.md:9` | Neither of the old names appears anywhere (G8). |
| R-15 | RESOLVED | `docs/protocol.md:114-151`, `docs/adr/0016-cancellation-semantics.md:66-73` | Superseded by the owner decision: always 200, with the same body for a duplicate. 400 and 503 are listed. The `COMPLETED` naming at `docs/protocol.md:148` is R2-04. |
| R-16 | PARTIAL | `docs/adr/0014-lease-model-and-expiry.md:99` | Contradiction. ADR-0014 says to retry "with capped jittered backoff until receiving a terminal reply or until task `timeout_seconds` expires". `docs/protocol.md:417` and `docs/phase4-plan.md:162` say "retries lease renewal on the next interval until the lease expires". |
| R-17 | PARTIAL | `docs/adr/0014-lease-model-and-expiry.md:228`, `docs/security/threat-model.md:132` | A threat row exists. "At most one per attempt (`ON CONFLICT DO NOTHING`)" has no arbiter: 000004 (`docs/schema.md:295-338`) adds no unique index, and `job_events` has only the non-unique indexes at `internal/storage/postgres/migrations/000001_initial_schema.sql:163` and `internal/storage/postgres/migrations/000001_initial_schema.sql:166` (G8). Without a unique constraint, `ON CONFLICT DO NOTHING` never skips a row. Add a partial unique index, for example on `(job_id, (details->>'attempt_id')) WHERE event_type = 'diagnostic_late_result'`. |
| R-18 | RESOLVED | `docs/phase4-plan.md:62-64`, `docs/phase4-plan.md:90`; messages aligned in `docs/protocol.md:117-123` | |
| R-19 | RESOLVED | `docs/adr/0015-retry-policy-and-failure-classification.md:84` | |
| R-20 | RESOLVED | `docs/security/findings.md:375` (`transition.go:20`), `docs/failure-model.md:73` (`40-112`), both checked with `sed -n` (G9) | |
| R-21 | PARTIAL | | `docs/roadmap.md:69` still has `$\ge 1,000$`. It was added on this branch: absent on `origin/main`, present at `aa13a4c` (G14). |
| R-22 | RESOLVED | `docs/failure-model.md:27`, `docs/roadmap.md:16` | |
| R-23 | PARTIAL | `docs/security/threat-model.md:128` (labelled "designed, not yet implemented") | `docs/phase4-plan.md:154` and `docs/PROJECT_STATE.md:193` still call the short-circuit and the indexed lookup "in-place defenses" without the label. |
| R-24 | RESOLVED | handoff design summary and security review; `docs/security/findings.md:35` credits P0-01; "Established task board" gone; `docs/agents/TASKS.md:24` status updated; empty bullet gone (G14) | New handoff problems are R2-07. `docs/agents/TASKS.md:47` still says the review file is "not created yet". |

## Cancel contract (task item 2): confirmed
- **executed** (G3), `origin/main` at `a9453fc`. `HandleCancelJob` returns `writeJSON(w,
  http.StatusOK, ...)` with `job_id`, `state` and `"message": "cancellation processed"`
  (`internal/api/handlers.go:267`). An invalid ID returns 400 `INVALID_REQUEST`
  (`isValidIdentifier`). `mapDomainErrorToHTTP` maps `ErrJobNotFound` to 404 `JOB_NOT_FOUND` and
  `ErrJobAlreadyTerminal` to 409 `JOB_ALREADY_TERMINAL`. It maps connection, timeout and
  `55P03`/`57xxx` errors to 503 `SERVICE_UNAVAILABLE`, and `writeError` sets `Retry-After: 1`.
  Neither `StatusAccepted` nor `202` appears in the file. The branch changes no Go file. These are
  the statuses the docs list at `docs/protocol.md:145-149` and `docs/adr/0016-cancellation-semantics.md:73`.
- Leftovers that contradict the owner decision: `docs/security/test-plan.md:56`, and
  `docs/agents/handoffs/P4-00.md:9` and `docs/agents/handoffs/P4-00.md:48` (R2-06). Every other
  202, exception and `duplicate` hit is a negation ("There is no HTTP 202"), so I raise no finding
  for those.

## Existing-test impact (task item 3)
- Every existing caller of `CancelJob` or `/cancel` in a test (G6): `internal/storage/storagetest/conformance.go:326`,
  `internal/storage/storagetest/conformance.go:335`, `internal/storage/storagetest/conformance.go:356`;
  `internal/storage/inmemory_test.go:589`, `internal/storage/inmemory_test.go:607`;
  `internal/storage/postgres/concurrency_test.go:438`; `internal/coordinator/service_test.go:411`,
  `internal/coordinator/service_test.go:486`. No API, worker, integration or e2e test calls
  `/cancel`, and `tests/e2e` does not exist.
- Only two tests cancel the same job twice: conformance line 335 and in-memory line 607. Both do
  it on a terminal `CANCELLED` job and assert `ErrJobAlreadyTerminal`. Conformance line 356
  cancels a RUNNING job once and asserts `CANCEL_REQUESTED`. The plan's description of both
  (`docs/phase4-plan.md:39-40`) is correct, and so are the line numbers. The idempotent change
  breaks none of them (**confirmed by code reading**).
- The stress test cancels at random. It may hit `CANCEL_REQUESTED` twice, but it asserts only
  invariants 1 to 3 (`internal/storage/postgres/concurrency_test.go:456-512`), so it is unaffected.
  The plan adds the missing invariant (`docs/phase4-plan.md:37`). That line labels
  `docs/fault-injection-plan.md:141` "`worker_capacity` accounting", but the cited line is "No
  Active Attempt After Terminal State". The check is the same; the label is wrong.
- `service_test.go:411` and `service_test.go:486` cancel QUEUED jobs, so the duplicate-cancel change
  does not affect them. The dispatch rewiring does (R2-01).

## New findings

### R2-01 (major) Slice 4a rewiring breaks seven coordinator tests that the plan says keep passing
- Location: `docs/phase4-plan.md:53-58`, `docs/adr/0015-retry-policy-and-failure-classification.md:126`.
- Evidence label: **confirmed by code reading** (G7); not executed, because the change does not exist yet.
- Evidence: ADR-0015 says the memory coordinator is "rewired to dispatch directly via
  `repo.DispatchNextJob`, removing the separate queue claiming path". The plan says existing tests
  "continue to pass because `DispatchNextJob` claims `QUEUED` jobs identically". But seven tests
  construct `coordinator.NewServiceWithQueue` (`dispatchViaRepo: false`, `internal/coordinator/service.go:72`)
  with a spy queue, and they assert queue `Ack`/`Release` calls that only the claim loop
  (`internal/coordinator/service.go:401-409`) makes:
  - `TestService_Claim_JobNoLongerQueued_Dropped` (`internal/coordinator/service_test.go:386`):
    "expected dropped job 1 to be acked from queue".
  - `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` (`internal/coordinator/service_test.go:458`).
  - `TestService_Claim_Redelivery_NoDuplicateAttempt` (`internal/coordinator/service_test.go:526`).
  - `TestService_CommitResult_AckOnlyOnAccepted` (`internal/coordinator/service_test.go:575`).
  - `TestService_ClaimOutcome_TransientFailure_QueueReleased` (`internal/coordinator/service_test.go:665`).
  - `TestService_ClaimOutcome_JobNotFoundOrNotQueued_AckedNeverReleased` (`internal/coordinator/service_test.go (line 716 at the audited commit)`).
  - `TestService_ClaimOutcome_WorkerLacksCapacityOrUnhealthy_Released` (`internal/coordinator/service_test.go (line 764 at the audited commit)`).

  The two tests that cancel through the repository bypass `Service.CancelJob`'s own `Ack`
  (`internal/coordinator/service.go:237`). After the rewiring, PollWork never touches the queue, so
  their `Ack` assertions fail. The plan names only the redelivery test. It also says nothing about
  whether `NewServiceWithQueue` keeps the queue path. If it does, `RETRYING` jobs strand on that
  path, which was round 1's R-05 problem.
- Suggested fix: list all seven tests in the 4a impact list. State whether `NewServiceWithQueue`
  keeps the queue path, and if so how it dispatches `RETRYING` jobs. Any test whose assertion is
  removed or rewritten is an owner-approval item (AGENTS.md section 2, never weaken tests).

### R2-02 (major) `CANCEL_REQUESTED` is not bounded when the worker is alive but the task ignores `ctx`
- Location: `docs/adr/0016-cancellation-semantics.md:11`, `docs/adr/0016-cancellation-semantics.md:125`,
  `docs/adr/0016-cancellation-semantics.md:151` against `docs/adr/0014-lease-model-and-expiry.md:94`
  and `docs/adr/0014-lease-model-and-expiry.md:215`; `docs/failure-model.md:60`; `docs/phase4-plan.md:188`.
- Evidence label: **inferred** from the documented protocol.
- Evidence: a renewal on a `CANCEL_REQUESTED` job returns 200, `cancel_requested: true`, and
  extends `lease_expires_at`. Nothing says the worker stops renewing once it has seen
  `cancel_requested`. `docs/failure-model.md:60` requires renewals to keep running after the task
  ends, while the result is being delivered, so the renewal loop's lifetime is not tied to the
  task context. `docs/phase4-plan.md:188` says the goroutine ends on "attempt context
  cancellation", which leaves undecided which context that is. ADR-0016:157 accepts that in-process
  tasks may ignore `ctx.Done()`. With such a task, the worker keeps renewing, the lease never
  lapses, and the sweeper never resolves the job. It stays `CANCEL_REQUESTED` until the task
  returns, possibly never, because a task that ignores `ctx` also ignores its `timeout_seconds`
  context. That contradicts the owner decision "No separate `CANCEL_REQUESTED` deadline (the lease
  bounds it)", the "within 30 seconds" claim at ADR-0016:151, and the 4d exit criterion "Zero jobs
  stranded in `CANCEL_REQUESTED`" (`docs/phase4-plan.md:210`). Even with a cooperative worker, the
  worst case is lease + grace + one tick, not 30 s.
- Suggested fix: choose one rule and write it in ADR-0014, ADR-0016 and protocol 6.1. Either a
  renewal on `CANCEL_REQUESTED` never extends beyond `cancel_requested_at + lease_duration`, or
  the worker stops renewing once it has received `cancel_requested`. Add a planned test with a
  task that ignores `ctx` and must end `CANCELLED` within the bound. Restate the bound in
  ADR-0016:151.

### R2-03 (major) A failure commit on a `CANCEL_REQUESTED` job, and the cancel acknowledgement itself, are undefined
- Location: `docs/adr/0016-cancellation-semantics.md:100`, `docs/adr/0016-cancellation-semantics.md:117-120`,
  `docs/protocol.md:459`, `docs/state-machine.md:33`, `docs/phase4-plan.md:204`.
- Evidence label: **confirmed by code reading** of the docs and of `internal/domain/transition.go:19-23`.
- Evidence: the FSM allows `CANCEL_REQUESTED` to move only to `CANCELLED`, `SUCCEEDED` or `FAILED`.
  `RETRYING` is not allowed, and the docs keep it that way. No document says what `CommitAttempt`
  does when a `CANCEL_REQUESTED` job receives a `TRANSIENT` or `UNKNOWN` failure: `FAILED`, or
  `CANCELLED`? The acknowledgement signal is given as "`exit_code = 130` (or
  `domain.ErrTaskCancelled`)". That "or" is not a wire contract. `error_classification` has no
  cancel value (`PERMANENT`/`TRANSIENT`/`UNKNOWN`), and an omitted value defaults to `UNKNOWN`. A
  RUNNING job (no cancel requested) that receives exit 130 from a worker shutdown would therefore
  be treated as a retryable failure. Nothing says whether that is intended.
- Suggested fix: define the acknowledgement on the wire, as one exact field and value. Add a table
  for `CommitAttempt` with rows `CANCEL_REQUESTED` and `RUNNING`, and columns success, permanent,
  transient/unknown and cancel-ack, giving the resulting job and attempt states. Add the matching
  planned conformance tests.

### R2-04 (major) Implementation-facing docs name the terminal job state `COMPLETED`; the job state is `SUCCEEDED`
- Location: `docs/schema.md:386`, `docs/phase4-plan.md:114`, `docs/protocol.md:148`, `docs/protocol.md:468`.
- Evidence label: **executed** grep (G8) plus **confirmed by code reading**.
- Evidence: `chk_jobs_state` allows `SUCCEEDED`, and has no `COMPLETED`
  (`internal/storage/postgres/migrations/000001_initial_schema.sql:28-33`); see also
  `docs/state-machine.md:17`. The sweeper terminal rule in schema.md and the plan, which is the
  R-02 fix, reads "Terminal Job (`COMPLETED`, `FAILED`, `CANCELLED`)". A sweeper written literally
  from that text would send a RUNNING attempt of a `SUCCEEDED` job to the "active" branch. That
  branch tries `SUCCEEDED -> RETRYING` or `SUCCEEDED -> FAILED`, the FSM rejects both, and the row
  becomes the poison candidate R-02 described. `REJECTED` is also missing from these lists. It
  cannot have attempts, but the protocol 409 lists should match `IsTerminal()`.
- Suggested fix: use `SUCCEEDED` in all four places, and define "terminal" once, as
  `JobState.IsTerminal()`.

### R2-05 (major) Outage-grace cooldown is ambiguous and can still postpone reaping indefinitely; grace is placed twice
- Location: `docs/adr/0014-lease-model-and-expiry.md:176`, `docs/adr/0014-lease-model-and-expiry.md:180`,
  `docs/adr/0014-lease-model-and-expiry.md:136`, `docs/adr/0014-lease-model-and-expiry.md:172-173`,
  `docs/schema.md:378-383`, `docs/phase4-plan.md:27-29`, `docs/phase4-plan.md:112`, `docs/phase4-plan.md:166`.
- Evidence label: **inferred** from the documented algorithm.
- Evidence:
  1. "A cooldown period equal to the grace window length (30s) prevents grace from re-activating
     more often than once per window". If the cooldown starts when grace activates, it expires at
     the same moment grace ends. A trigger on the next tick re-enters grace at once, so reaping
     never runs: this is round 1's R-09 problem again. If it starts when grace ends, reaping is
     guaranteed at most every other 30 s window. The text allows both readings.
  2. "Failed Tick" includes "context deadline exceeded". A slow tick whose statement hits its
     deadline therefore enters grace, which contradicts
     `TestPostgresRepository_Sweeper_SlowTick_DoesNotEnterGrace` (planned test) and
     `docs/agents/handoffs/P4-00.md:226` ("slow ticks ... trigger the 30s outage grace window").
  3. Grace suppresses whole passes in ADR-0014:172-173 and :180 and in plan:27, but is checked per
     candidate "inside the locks" in ADR-0014:136, schema.md:378-383 and plan:112.
  4. The startup grace does not say which clock it uses, database time or process time.
- Suggested fix: start the cooldown when grace ends, and require at least one completed reaping
  pass between two grace windows. Count only connection-class errors as an outage, not deadline or
  lock timeouts. Check grace once, at the start of the tick, on database time.

### R2-06 (minor) Cancel-contract leftovers that contradict the owner decision
- Location: `docs/security/test-plan.md:56` ("Idempotently returns HTTP 202 `CANCEL_REQUESTED`"),
  `docs/agents/handoffs/P4-00.md:9` ("idempotent 202 no-op"), `docs/agents/handoffs/P4-00.md:48`
  ("idempotent 202 cancel").
- Evidence label: **executed** grep (G4).
- Suggested fix: change all three to HTTP 200 with the same body as the first cancel.

### R2-07 (major) The handoff misreports the finding resolution and the history
- Location: `docs/agents/handoffs/P4-00.md:5`, `docs/agents/handoffs/P4-00.md:12-14`,
  `docs/agents/handoffs/P4-00.md:32`, `docs/agents/handoffs/P4-00.md:60-71`,
  `docs/agents/handoffs/P4-00.md:83`, `docs/agents/handoffs/P4-00.md:86-99`,
  `docs/agents/handoffs/P4-00.md:210`, `docs/agents/handoffs/P4-00.md:225-229`, `docs/agents/TASKS.md:47`.
- Evidence label: **executed** (G4, G5, G11, G15) and **confirmed by code reading** against
  `docs/agents/reviews/P4-00.md`.
- Evidence:
  - From R-13 on, every row of the resolution table describes a different round-1 finding. For
    example, the table's R-13 row is about dropping a default "on attempts.error_class" (round 1:
    the attempt mapping table, and the default was on `lease_expires_at`). Its R-14 row is
    `TIMED_OUT` (round 1: config names). Its R-16 and R-17 rows are cast as cancel "owner
    overrides", but round-1 R-16 is renewal transport errors and R-17 is the diagnostic-event bound,
    and neither is overridden. Its R-18 to R-24 rows are likewise shifted.
  - "Owner override R-01 ... R-12" presents every fix as an owner override. No owner decision is
    recorded for these.
  - "All 24 audit findings have been resolved" is not true (table above).
  - Further false claims are in the claims table (rows 3, 5, 7, 8 and 9): the stale last commit,
    the evidence line 74, the 202 history, the Blueprint section lists and the roadmap text.
  - The planned names in the security review differ from those in the plan:
    `TestPostgresRepository_Sweeper_TerminalJobResolution` (planned test) against
    `TestPostgresRepository_Sweeper_TerminalJobClosesAttempt` (planned test), and
    `TestCoordinator_Sweeper_GraceCooldown` (planned test) against
    `TestCoordinator_LeaseSweeper_CooldownCap` (planned test).
  - `docs/agents/TASKS.md:47` still says the review file is "not created yet".
- Why major: the owner merges on the handoff. A resolution table that is wrong for 12 of 24 rows
  is an unverifiable claim under AGENTS.md section 10.
- Suggested fix: rebuild the table against `docs/agents/reviews/P4-00.md` and this review, one row
  per real finding. Drop the "owner override" wording unless a decision is recorded. Correct
  every claim above.

### R2-08 (minor) "Remove the four probe allowlist entries" would break `make test-docs`
- Location: `docs/phase4-plan.md:78`, `docs/agents/handoffs/P4-00.md:81`.
- Evidence label: **executed** (G12). The simulation ran in the throwaway worktree
  `~/mc-mut-claude` (removed afterwards). It stubbed `tests/review/sec022_probe_test.go` with the
  two probe test names, which is the state after the 4a cherry-pick.
- Evidence: with all four entries kept, Rule F flags three as stale (allowlist lines 19, 20 and 22).
  With all four removed, Rule C fails: `docs/agents/handoffs/P4-00.md:80` and
  `docs/security/findings.md:313` both report `cited test "TestReviewP400_SEC022" does not exist`.
  That string is the `-run` pattern inside verbatim evidence, so it never becomes a real test.
  `docs/phase4-plan.md:78` also names none of the four entries.
- Suggested fix: name the entries, remove the path entry and the two test-name entries in 4a, and
  keep the `TestReviewP400_SEC022` entry with its reason. If the probe moves to a different path,
  keep the path entry as well, because round-1 `docs/agents/reviews/P4-00.md` still cites the old
  path.

### R2-09 (minor) The layer of the duplicate-cancel no-op differs between documents
- Location: `docs/state-machine.md:40` ("handled at the coordinator service and HTTP API layer")
  against `docs/phase4-plan.md:74` and `docs/phase4-plan.md:123` (repository `CancelJob`).
- Evidence label: **inferred**.
- Evidence: a check at the service or HTTP layer that reads the state before calling
  `repo.CancelJob` does not stop two concurrent first cancels on a RUNNING job. Both pass the
  check. The first moves the job to `CANCEL_REQUESTED`, and the second falls into the repository
  `else` branch, which is SEC-022 again. Only the repository change, made under the job row lock,
  is safe.
- Suggested fix: state that the no-op lives in both repositories' `CancelJob`, under the row lock,
  and correct `docs/state-machine.md:40`.

### R2-10 (minor) The audit event vocabulary has three spellings and missing events
- Location: `docs/state-machine.md:163-175` (`job_cancelled`, `lease_expired`), `docs/schema.md:387-390`
  and `docs/phase4-plan.md:115-117` (`job.cancelled`, `attempt.lost`, `job.retrying`, `job.failed`),
  `docs/adr/0014-lease-model-and-expiry.md:144` (`lease_expired`); code `internal/domain/events.go:9-15`
  (`JOB_CANCELLED`).
- Evidence label: **executed** grep (G8).
- Evidence: the docs and the code use three spellings for the same events. `attempt.lost` has no
  entry in the state-machine event table, and the diagnostic event written by the terminal-job
  sweeper branch has no named type.
- Suggested fix: use one vocabulary, mapped to `JobEvent*` constants, in all three documents.

### R2-11 (minor) Citation errors that `make test-docs` cannot catch
- Location and evidence (**executed** `sed -n`, G9):
  - `docs/phase4-plan.md:57` cites `internal/api/api_test.go:21` as the place that uses the memory
    coordinator. Line 21 is an import; the setup is at `internal/api/api_test.go:27`.
  - `docs/state-machine.md:40` cites `internal/domain/transition.go:27` for "the diagonal is
    strictly —". Line 27 is `RETRYING -> CANCELLED`.
  - The `CancelJob` ranges `internal/storage/inmemory.go:658-683` and
    `internal/storage/postgres/repository.go:1135-1215` stop mid-function. The functions end at
    `internal/storage/inmemory.go:700` and `internal/storage/postgres/repository.go:1233`.
  - `docs/agents/handoffs/P4-00.md:83` cites `docs/agents/reviews/P4-00-evidence.txt:32, 74`. The
    second cancel is at `docs/agents/reviews/P4-00-evidence.txt:34`.
  - The invariant-4 label at `docs/phase4-plan.md:37` is wrong (see item 3 above).
- Suggested fix: correct each one.

## Cross-document checks that passed (task item 4)
- `error_class` values agree across ADR-0015, schema.md (000004 CHECK), protocol 6.2 and the plan.
  state-machine.md and failure-model.md name no values beyond `PERMANENT`. The two leftovers are
  under R-03.
- The 409 order (terminal, then `LOST`, then superseded/token) is the same in ADR-0014, protocol
  6.3 and the plan (state-machine.md:183 is under R-04).
- The canonical lock order `jobs -> workers -> attempts -> results -> job_events` is in ADR-0014:108
  and agrees with `AGENTS.md`. The per-candidate `jobs SKIP LOCKED`, then `workers`/`attempts
  FOR UPDATE` under `lock_timeout`, is identical in ADR-0014, schema.md and the plan.
- Lease 30 s, renewal 10 s, sweeper 5 s and batch 50 are the same everywhere. No other value
  appears (G8 grep). Both grace windows are 30 s, must be >= the lease, and the config names agree.
- `RETRYING -> RUNNING` via dispatch is consistent in ADR-0015, state-machine.md and the plan. Each
  of the flipped matrix cells is marked "candidate for owner approval".
- The R-02 terminal-job sweeper rule is the same in ADR-0014, ADR-0016, schema.md and the plan,
  apart from the naming in R2-04.

## Citations (task item 5)
- Line citations checked with `sed -n` (G9), 33 in all. All are correct except those in R2-11:
  `internal/coordinator/service.go:320-333`, `internal/coordinator/service.go:401-409`,
  `internal/storage/inmemory.go:577-579`, `internal/storage/postgres/repository.go:983-984`,
  `internal/storage/postgres/repository.go:634`, `internal/storage/postgres/repository.go:873`,
  `internal/storage/postgres/repository.go:946`, `internal/domain/transition.go:19-23`,
  `internal/domain/state.go:22-27`, `internal/api/handlers.go:267`, `internal/worker/client.go:267-270`,
  `internal/worker/client.go:324-355`, `internal/worker/client_test.go:16-38`, `internal/worker/client_test.go:40-112`,
  `tests/integration/postgres_wiring_test.go:167`, `tests/integration/part_d_security_test.go:794`,
  `internal/storage/storagetest/conformance.go:299-363`, `internal/storage/inmemory_test.go:559`,
  `tests/integration/local_core_test.go:25`, `tests/integration/multi_worker_batch_test.go:26`,
  `internal/storage/postgres/migrator_test.go:546-575`, `internal/domain/transition_test.go:10`,
  `docs/fault-injection-plan.md:141`, and `000001_initial_schema.sql:97`.
- Blueprint (G11): the PDF has 14 pages. Section titles from the PDF: 3 "Users, Use Cases, and
  Requirements" (p3), 4 "System Architecture" (p4), 5 "Job Lifecycle and Consistency Model"
  (p5 to p6), 6 "Scheduling Strategy and Research Question" (p6), 8 "API, Data Model, and
  Configuration" (p8), 10 "Test Strategy and Failure Injection" (p9 to p10), 12 "Definition of Done
  and Benchmark Plan" (p10), 13 "Main Risks and Mitigations" (p11). Every Blueprint citation in the
  changed docs was checked. The wrong ones are listed under R-12. Correct: ADR-0014 sections 4, 5
  and 10; ADR-0015 section 5 content (bounded retries, retryable/permanent, at-least-once,
  monotonic attempt number, `RETRYING -> QUEUED, FAILED, CANCELLED` "initial policy"); ADR-0016
  section 8 content ("distinguishes requested from completed") and section 10 ("Cancellation races
  with success completion"); `docs/fault-injection-plan.md` section 12; the pre-existing section 6
  round-robin citations.
- Test names (G10): every name the Phase 4 docs cite as existing is in `go test -list '.*' ./...`
  or is a `t.Run` name. `RuleF_Allowlist_StaleFail` is a subtest of `TestFixtures`
  (`tests/docs/citations_test.go:970`), and `go test -run 'TestFixtures/RuleF_Allowlist_StaleFail'`
  passes. `TestAPI_CancelJob_SuccessReturns200` does not exist. It appears twice
  (`docs/phase4-plan.md:52`, `docs/phase4-plan.md:91`), labelled "(planned test)" both times.
  Every other nonexistent name in the changed docs is labelled planned, future or audit probe, or
  is a pre-existing SEC-018 "does not exist" disclosure, or is an allowlisted probe name.

## Allowlist, SEC-022, scope (task items 6 to 8)
- `tests/docs/citation_allowlist.txt` has 7 original entries (line 8, lines 11 to 16) and exactly
  the 4 probe entries (lines 19 to 22).
- `git grep -n -E 'tests/integration/sec022|ReturnsAccepted|three probe|probe allowlist' -- docs
  CHANGELOG.md` has a single hit, `docs/phase4-plan.md:78`. That hit names no entry (R2-08).
- The handoff Deviations entry names all four entries and a removal condition. The removal
  condition is wrong for the fourth entry (R2-08).
- SEC-022 is Medium, with status "Open (executed)" and evidence label **executed**.
  `diff <(sed -n 313,359p docs/security/findings.md) <(sed -n 4,51p docs/agents/reviews/P4-00-evidence.txt)`
  differs only by the trailing blank line of E1 (`47a48`). Nothing in SEC-022 claims an executed
  result beyond E1.
- SEC-023 is unchanged in meaning: Low, Open, crediting P0-01. Its evidence is the P0-01 block.
- `git diff --name-only aa13a4c..HEAD | grep -v -E '^(docs/|CHANGELOG.md)'` prints only
  `tests/docs/citation_allowlist.txt` (G12).

## Attacks attempted that did NOT find issues
- Cancel against commit: both lock `jobs` first. The FSM allows `CANCEL_REQUESTED` to move to
  `SUCCEEDED`, `FAILED` and `CANCELLED`, so a successful commit after cancel is defined, and so is
  a commit on a job already `CANCELLED` (closes the attempt, 409).
- Cancel against sweeper: both lock `jobs` first. The sweeper uses `SKIP LOCKED` and retries on the
  next tick, and a `CANCEL_REQUESTED` attempt is closed as `CANCELLED` with no retry.
- Renewal against sweeper: the re-check under the `attempts` lock (`state = RUNNING` and
  `lease_expires_at >= clock_timestamp()` mean skip) handles a renewal that lands between the scan
  and the lock. Lock order is safe as long as renewal never takes `jobs` after `attempts`
  (ADR-0014:107 requires the canonical order).
- Duplicate cancel against retry dispatch on a `RETRYING` job: `CancelJob` and `DispatchNextJob`
  both lock the job row, and `RETRYING -> CANCELLED` clears `not_before`. Either order yields one
  defined outcome.
- Restart: existing `RUNNING` rows receive `lease_expires_at` = migration time + 30 s from the
  temporary default, which is then dropped (`docs/schema.md:298-304`). With the 30 s startup grace,
  a surviving worker that renews is not reaped. An old worker that does not renew is the documented
  unsupported combination (`docs/protocol.md:483`).
- In-memory against PostgreSQL dispatch predicate, and `not_before` with the injected clock: same
  rule in both (ADR-0015:125).
- No Go code changed on the branch. `origin/main` handler behaviour equals the documented behaviour.

## Tests added
None. Docs-only re-review. The allowlist simulation (G12) used a stub file in a throwaway worktree
that was removed afterwards. Nothing from it is committed.
