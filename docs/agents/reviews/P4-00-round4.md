# Review P4-00 (round 4, final targeted re-audit)

## Commit reviewed
`69b3864` on `origin/docs/phase4-design` (`docs(agents): update P4-00 handoff with round-3 resolutions
and owner decisions (D-R3-03)`). Review branch: `agent/claude/P4-00-review-4`, based on `69b3864`.
Scope: the 11 commits after `623837e`, `15d7bcd` to `69b3864` (`git diff 623837e..HEAD`; list in the
`start` section of the evidence file), checked against round 3
(`docs/agents/reviews/P4-00-round3.md`) and the owner decisions D-R3-01, D-R3-02, D-R3-03 and D-R3-06.
I edited no design doc, no Go code and not the allowlist. I re-derived every result below and did not
rely on the handoff or on Gemini's resolution tables. The raw output is in
`docs/agents/reviews/P4-00-round4-evidence.txt`. Each section there is headed `### [G..]`, and the
sections are cited below by those labels.

## Verdict: CHANGES REQUIRED

A verdict of APPROVE requires that no blocker and no major finding is open. One major finding is
open, and there is no blocker.
- R4-01 (major): the handoff still contains false claims and wrong fix locations, so D-R3-03 ("handoff
  rewritten truthfully") is not met. Examples: it says the renewal status is `"ACTIVE"` at
  `docs/protocol.md:386` (the line is blank, and every document says `"RENEWED"`). It gives the sweeper
  cadence as 10 s (the accepted value is 5 s). About 25 fix locations point at the wrong line.

**The design documents themselves have no open major finding.** All 20 items carried over from round 3
are resolved in the design text, except R2-10, which has a two-word residual. The four new design
findings (R4-02 to R4-05) are minor.

**Can it be fixed in one follow-up commit without another audit?**
- R4-02, R4-03, R4-04 and R4-05 (minor): yes. They are wording and labelling edits, and each has an
  exact location and suggested text below.
- R4-01 (major): it is handoff-only and mechanical, but by the AGENTS.md rule it is major, so the fix
  needs a check. That check does not need a new design audit. It is the `sed -n` dump in G6
  (`G6-handoff-plan-cites`, `G6-handoff-other-cites`) re-run against the corrected handoff, plus the
  two greps in G2b and G2c. The owner can run it directly.

## Preflight and gates (executed; evidence: preflight, start, G8-*)
- Preflight: `worktree: /home/blazeee/MiniCloud-claude`, `test DB:  minicloud_claude_test`,
  `preflight OK`. No other worktree is involved. Tip `69b3864`, working tree clean before the review
  files. Go `go1.22.2 linux/amd64`.
- `gofmt -l .` lists nothing. `go vet ./...` and `go build ./...` exit 0.
- `make test-docs` prints `ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	0.371s`. Uncached
  `go test -count=1 ./tests/docs` prints `ok ... 0.351s`.
- `env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=1 ./...` exits 0 (G8-race-unset).
- `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...` gives `PASS 77 SKIP 26 FAIL 0`
  (G8-counts), the expected numbers. The 26 skips are database tests and are not passes. `make test-db`
  was not run, because this is a docs-only re-audit.

## Status of every item open after round 3

| ID | Status | Fix location (verified with `sed -n` / `awk` / `git grep`) | What remains |
|---|---|---|---|
| R3-01 | RESOLVED | `docs/adr/0014-lease-model-and-expiry.md:179`, `docs/schema.md:399`, `docs/protocol.md:409`, `docs/failure-model.md:10`, `docs/failure-model.md:28`, `docs/phase4-plan.md:29`, `docs/phase4-plan.md:179` | All seven copies are byte-identical (G2e-sentence-diff, G2e-fs08-diff; FS-08 differs only by a lowercase mid-sentence "the"). No old wording survives (G2e-old-wording: only unrelated hits in ADR-0009, ADR-0013, architecture.md, findings.md, test-plan.md, plus the handoff saying the phrase was removed). The planned tests follow the rule with no contradicting pair (G2e-grace-testnames). |
| R3-02 | RESOLVED | `docs/failure-model.md:26`, `docs/fault-injection-plan.md:71`, `docs/protocol.md:475`, `docs/adr/0014-lease-model-and-expiry.md:230`, `docs/phase4-plan.md:133`, `docs/security/findings.md:383` | No document promises 200 `COMMITTED` for a retried commit on a terminal job (G2f-200committed: every remaining 200 `COMMITTED` is a non-terminal commit). Two summary lines still omit the "RUNNING only" qualifier (R4-05). |
| R3-03 | PARTIAL (R4-01, major) | `docs/agents/handoffs/P4-00.md` | The `make test-db` claim is now true (`:199`). Line 5 names `c77056f`, the last commit before the handoff commit itself (acceptable). New and remaining false claims are listed in R4-01. |
| R3-04 | RESOLVED | `CHANGELOG.md:21`, `docs/PROJECT_STATE.md:21`, `docs/agents/handoffs/P4-00.md:213`, `:248` | Every use of the executed probe says "audit probe, executed on `agent/claude/P4-00-review`" (G-R3-04, G-R3-04-labels). |
| R3-05 | RESOLVED | design docs | None of the stale code citations remains in the design docs (G-R3-05). The handoff's own citations are in R4-01. |
| R3-06 | RESOLVED | `docs/state-machine.md:82`, `:143-146`, `:154`, `:172`, `docs/schema.md:350`, `docs/protocol.md:477-481` | The attempt end states match D-R3-06 everywhere. One leftover phrase, "awaiting worker acknowledgement" at `docs/state-machine.md:15`, is in R4-05. The labelling of the job-outcome change is in R4-02. |
| R3-07 | RESOLVED | `docs/adr/0014-lease-model-and-expiry.md:85`, `:217`, `docs/protocol.md:408`, `docs/state-machine.md:150`, `docs/failure-model.md:11`, `:60`, `docs/adr/0016-cancellation-semantics.md:157`, `:159` | Each accept-until-swept statement has the `CANCEL_REQUESTED` exception. The bound at ADR-0016:157 gives no wrong number. The wrong "(10s)" exists only in the handoff (R4-01). |
| R3-08 | RESOLVED | `docs/protocol.md:371`, `:380`, `docs/adr/0014-lease-model-and-expiry.md:89`, `docs/adr/0016-cancellation-semantics.md:84`, `docs/phase4-plan.md:163-164` (all `"RENEWED"`); renewal 400 codes at `docs/adr/0014-lease-model-and-expiry.md:102`, `docs/protocol.md:417`, `docs/failure-model.md:69`, `docs/phase4-plan.md:174`; renewal 409 list at `docs/phase4-plan.md:172`; sweep event at `docs/phase4-plan.md:127`; worker `LEASE_EXPIRED` gives 400 at `docs/protocol.md:460` | Every item R3-08 named is fixed (G2c-status, G2c-400codes, G2d-lease-expired). The handoff misstates the status value (R4-01). |
| R3-09 | RESOLVED | `docs/phase4-plan.md:53-71`, `docs/adr/0015-retry-policy-and-failure-classification.md:126` | Six `spyQueue` tests and one plain-queue test, each correctly named and lined, with constructor fate, `Enqueue` and `CommitResult` `Ack` covered (G2g-*). One queue call is still not listed (R4-04). |
| R2-01 | RESOLVED | as R3-09 | |
| R2-03 | RESOLVED | `docs/adr/0016-cancellation-semantics.md:113-125`, `docs/adr/0014-lease-model-and-expiry.md:96-98`, `docs/protocol.md:477-481`, `docs/state-machine.md:54`, `docs/failure-model.md:31`, `docs/phase4-plan.md:133`, `:225` | The outcome is consistent across documents. How the plan labels the change is in R4-02. |
| R2-05 | RESOLVED | as R3-01; in-lock check `docs/adr/0014-lease-model-and-expiry.md:140`, `docs/schema.md:387` | |
| R2-07 | PARTIAL (R4-01) | `docs/agents/handoffs/P4-00.md` | |
| R2-10 | PARTIAL (minor residual, R4-05) | `docs/state-machine.md:46-48`, `:163-175`; `docs/fault-injection-plan.md:32` | The terminal-job sweep event is `LEASE_EXPIRED` in all three sweeper specs. Lowercase `diagnostic_late_result` survives at `docs/fault-injection-plan.md:47` and `docs/security/threat-model.md:132` (G-R2-10). |
| R2-11 | RESOLVED | design docs (G-R3-05, G2g-lines, G6-misc2) | |
| R-03 | RESOLVED | `CHANGELOG.md:24` now says "4-tier" with four values (G-R23, G4-errorclass) | |
| R-04 | RESOLVED | as R3-02 | |
| R-09 | RESOLVED | as R3-01 | |
| R-10 | RESOLVED | as R3-08 (400 and 404 renewal codes); `docs/protocol.md:483` keeps `ATTEMPT_NOT_RUNNING` only as a deprecated code | See R4-05 for the planned `ErrAttemptNotRunning`. |
| R-23 | RESOLVED | `docs/phase4-plan.md:167`, `docs/PROJECT_STATE.md:193`, `docs/security/threat-model.md:128` | `CHANGELOG.md:31` still says "documented in-place defenses". The same line says "designed, not implemented", so I treat it as informational. |

**Handoff status columns against this judgement.** Round-3 table (`docs/agents/handoffs/P4-00.md:86-94`):
- R3-01, R3-02 and R3-04 to R3-09 match.
- R3-03 does not: the handoff says RESOLVED, and I judge it PARTIAL.

Round-1 and round-2 tables (`:41-64`, `:70-80`): they still carry the round-3 statuses.
- R-03, R-04, R-09, R-10, R-23, R2-01, R2-03, R2-05 and R2-11 are PARTIAL in the handoff and RESOLVED
  here.
- Several of these rows say "PARTIAL (R3-0x)" for a finding that the handoff's own round-3 table calls
  RESOLVED. For example, R-09 and R2-05 cite R3-01. The tables contradict each other (R4-01).

## Suspect items (task item 2)

### 2a. Failed commit on `CANCEL_REQUESTED`: what the code does today (confirmed by code reading, G2a-*)
- In-memory (`internal/storage/inmemory.go:578-580`), the terminal check runs first. `CANCEL_REQUESTED`
  is not terminal, so the commit proceeds. A failure outcome sets `att.State = domain.AttemptFailed`
  and `targetState = domain.StateFailed` (`internal/storage/inmemory.go:619-622`). `job.TransitionTo`
  (`:626`) succeeds because `internal/domain/transition.go:22` permits `CANCEL_REQUESTED -> FAILED`.
- PostgreSQL (`internal/storage/postgres/repository.go:983-985`, `:1050-1054`) does the same: the
  attempt becomes `FAILED`, the job becomes `FAILED`, and the event is `JOB_FAILED`.
- **Today: attempt `FAILED`, job `FAILED`.** The owner's D-R3-06 text calls "job `CANCELLED`" today's
  behavior. That is not what the code does. The design's `CANCELLED` is a behavior change (cancel wins,
  as decided in D-R2-03).
- Where the change is listed:
  - ADR-0016 lists it explicitly as behavior change 4, with today's citation and a planned test
    (`docs/adr/0016-cancellation-semantics.md:51-52`).
  - state-machine.md places it under "Phase 4 Behavior Changes Introduced" (`docs/state-machine.md:54`).
    It does not say that today's outcome is `FAILED`.
  - phase4-plan.md never marks it as a change. Two slices describe it (`docs/phase4-plan.md:133` in 4b
    and `:225` in 4d). Slice 4a's in-memory parity scope (`:84-88`) does not mention it.
    `TestConformance_CommitAttempt_FailureOnCancelRequestedYieldsCancelledJob` (planned test) appears in
    no gating list (G6-handoff-misc: 0 hits in the plan).
- Existing tests: none asserts the old outcome.
  - The only `CANCEL_REQUESTED` uses in tests are `internal/domain/transition_test.go:27-33` and the
    conformance check at `internal/storage/storagetest/conformance.go:360`, which asserts only the cancel
    transition (G2a-tests-cancelreq, G2a-conf-cancel).
  - The stress test commits `ExitCode: 0` only (`internal/storage/postgres/concurrency_test.go:432`,
    G2a-stress).
  - Nothing breaks, so the change is not silent. Its plan labelling is incomplete: **R4-02 (minor)**.

### 2b. Cancellation bound numbers (executed grep, G2b-bound, G2b-numbers)
The design documents use no wrong number:
- `docs/adr/0016-cancellation-semantics.md:157` says "one lease duration (30 seconds) plus sweeper
  cadence, plus up to one grace window (30 seconds)". It gives no number for the cadence.
- Every other bound statement gives the lease (30 s) or the renewal interval (10 s) correctly.
- The sweeper interval is 5 s everywhere: `docs/adr/0014-lease-model-and-expiry.md:6`, `:53`,
  `docs/phase4-plan.md:9`, `:178`, `docs/PROJECT_STATE.md:193`, `CHANGELOG.md:23`.

**The only wrong number is the handoff's "sweeper cadence (10s)" at `docs/agents/handoffs/P4-00.md:92`
(R4-01).**

### 2c. Renewal response status and renewal 400 codes (G2c-*)
- Status value: every document says `"RENEWED"`: `docs/protocol.md:371`, `:380`,
  `docs/adr/0014-lease-model-and-expiry.md:89`, `docs/adr/0016-cancellation-semantics.md:84`, and
  `docs/phase4-plan.md:163`, `:164`. `"ACK"` remains only for the heartbeat.
- The handoff (`:93`) says Gemini unified on `"ACTIVE"`, "matching protocol.md:386". No document
  contains `"ACTIVE"`, and `docs/protocol.md:386` is a blank line. The documents are consistent; the
  handoff is false (R4-01).
- Renewal 400 codes are `INVALID_REQUEST` and `WORKER_TASK_UNSUPPORTED` in all four places:
  `docs/adr/0014-lease-model-and-expiry.md:102`, `docs/protocol.md:417`, `docs/failure-model.md:69`
  and `docs/phase4-plan.md:174`. `ATTEMPT_NOT_RUNNING` appears only as deprecated
  (`docs/protocol.md:483`) and in the handoff.
- Renewal on an attempt that is not `RUNNING`:
  - `docs/adr/0014-lease-model-and-expiry.md:82`, `:85` and the order table at `docs/protocol.md:469-471`
    define the outcome:
    - terminal job: 409 `JOB_ALREADY_TERMINAL`;
    - attempt `LOST`: 409 `LEASE_EXPIRED`;
    - any other non-`RUNNING` attempt, a superseded attempt or a token mismatch: 409
      `STALE_ATTEMPT_REJECTED`.
  - The worker treats all three as terminal (`docs/protocol.md:413`). This is consistent.
- The renew route does not exist in today's code, so no code path returns an undocumented code today.
  The shared mapper (`internal/api/handlers.go:110-154`) could return other 400 codes. See informational
  note 4.
- `ErrAttemptNotRunning` is named as a planned domain error only at `docs/PROJECT_STATE.md:174`, with no
  HTTP mapping in any design doc (R4-05).

### 2d. Terminal-job sweep event name (G2d-*)
- The name is `LEASE_EXPIRED` in all three sweeper specs: `docs/adr/0014-lease-model-and-expiry.md:148`,
  `docs/schema.md:391` and `docs/phase4-plan.md:127`. state-machine.md defines `LEASE_EXPIRED` as
  "Coordinator swept expired attempt" (`docs/state-machine.md:169`).
- The name is semantically right: the attempt's lease did expire, and the sweeper closed it. A
  diagnostic name would wrongly suggest a late-result rejection, which is what `DIAGNOSTIC_LATE_RESULT`
  means. **No finding on the name.**
- A different event disagreement came up: the event set for a sweep of a `CANCEL_REQUESTED` job. See
  **R4-03 (minor)**.

### 2e. Grace sentences: see R3-01 in the status table. **No finding.**
- All seven copies are byte-identical.
- No "connection-class", "health check" or exclusion wording survives.
- The planned tests cover all five cases with no contradicting pair, in both
  `docs/adr/0014-lease-model-and-expiry.md:193-199` and `docs/phase4-plan.md:196-200`:
  - gap at or below 3x: no grace;
  - gap above 3x: grace;
  - failed tick: grace;
  - lock timeout on one candidate: no grace;
  - the cooldown blocks a second window.

### 2f. D-R3-02 (G2f-*)
- FS-06 (`docs/failure-model.md:26`), fault-injection FS-06 sub-case B (`docs/fault-injection-plan.md:71`),
  protocol 6.3 (`docs/protocol.md:475`) and ADR-0014 (`docs/adr/0014-lease-model-and-expiry.md:230`) all
  give 409 `JOB_ALREADY_TERMINAL`. The security test plan has no terminal-job 200 claim
  (G2f-200committed).
- The order is the same in all three documents that state it: terminal, then `LOST`, then
  stale/token. This holds in `docs/adr/0014-lease-model-and-expiry.md:229-233`, `docs/protocol.md:467-473`
  and `docs/phase4-plan.md:132-135`. schema.md has no order table, and its attempt-`CANCELLED` writer
  says "closing a `RUNNING` attempt" (`docs/schema.md:350`).
- The "RUNNING only" rule is at `docs/adr/0014-lease-model-and-expiry.md:230`, `docs/protocol.md:475`,
  `docs/phase4-plan.md:133` and `docs/security/findings.md:383`. Two summary lines omit it (R4-05).

### 2g. Slice 4a impact list (executed, G2g-*)
- `go test -list '.*' ./internal/coordinator/` lists all seven names.
- The cited lines are correct: `internal/coordinator/service_test.go:386`, `:458`, `:526`, `:575`,
  `:665`, `:716` and `:764`, with `spyQueue` spanning `:338-381`.
- Six tests construct `newSpyQueue`: lines `:392`, `:465`, `:581`, `:677`, `:727` and `:775`.
- `TestService_Claim_Redelivery_NoDuplicateAttempt` uses a plain `queue.NewInMemoryQueue()` at `:531`
  and `q.Enqueue` at `:554`.
- The service lines are correct: `internal/coordinator/service.go:71-72` (`NewServiceWithQueue`),
  `:194-195` (`Enqueue`) and `:512-513` (`CommitResult` `Ack`).
- Other callers: `internal/api/api_test.go:30`, `:58`, `:588`; `internal/coordinator/service_test.go:47`,
  `:274`; `tests/integration/local_core_test.go:38`; `tests/integration/coordinator_restart_test.go:67`,
  `:181`; `tests/integration/worker_liveness_test.go:43`; `tests/integration/multi_worker_batch_test.go:40`.
  - None asserts queue calls.
  - Memory `DispatchNextJob` (`internal/storage/inmemory.go:467-495`) has the same error precedence,
    FIFO order and task filter as the queue path (`internal/coordinator/service.go:386-405`,
    `internal/queue/inmemory.go:43-61`), and the coordinator maps `ErrWorkerNoCapacity` the same way
    (`internal/coordinator/service.go:360-362`).
  - `internal/worker`, `tests/architecture` and the storage tests do not go through the coordinator's
    dispatch.
  - I found no further breaking test (**inferred**: the rewiring does not exist yet).
- One queue call is not in the plan's list: `CancelJob`'s `s.queue.Ack` at
  `internal/coordinator/service.go:237-239`. This is **R4-04 (minor)**.

### 2h. Handoff truthfulness: see R4-01.

## New findings

### R4-01 (major) The handoff still carries false claims and about 25 wrong fix locations (D-R3-03 not met)
- Location: `docs/agents/handoffs/P4-00.md` (lines below).
- Evidence label: **executed** (`sed -n` / `awk` dumps G6-handoff-plan-cites, G6-handoff-other-cites,
  G6-misc2, G6-handoff-misc; greps G2b-bound, G2c-status) and **confirmed by code reading**.
- Evidence: false content claims.
  - `:93` (R3-08 row) says: "renewal response status field is `status: "ACTIVE"` (matching
    protocol.md:386)". Every document says `"RENEWED"` (G2c-status), and `docs/protocol.md:386` is blank.
  - `:92` (R3-07 row) says: "lease duration (30s) plus sweeper cadence (10s)". The accepted sweeper
    interval is 5 s (`docs/adr/0014-lease-model-and-expiry.md:53`).
  - `:35` says TASKS.md now lists `P4-00-round3.md`. `docs/agents/TASKS.md:47` names only `P4-00.md` and
    `P4-00-round2.md`.
  - `:76` (R2-07 row) says the handoff "corrected all fix locations with verified `sed -n` citations".
    The wrong locations below show that this is false.
  - `:113` says all owner decisions were "verified with `make test-docs`". The checker validates
    citation bounds and labels, not decisions.
  - `:25` says fault-injection-plan event names were made uppercase, including `DIAGNOSTIC_LATE_RESULT`.
    `docs/fault-injection-plan.md:47` still has `diagnostic_late_result`.
  - `:226` lists `TestPostgresRepository_Sweeper_TerminalJobClosesAttempt` (planned test). No design doc
    uses that name any more. The plan instead cites, at `docs/phase4-plan.md:145` and `:151`:
    `TestPostgresRepository_CommitAttempt_TerminalJobClosesAttempt` (planned test).
  - Status columns: the round-1 and round-2 tables contradict the round-3 table (see the status-table
    note above). R3-03 is marked RESOLVED although the handoff is still inaccurate.
- Evidence: wrong fix locations (each line read with `sed -n`/`awk`; "actual" is where the cited
  content is).
  - `docs/phase4-plan.md` (the plan grew by about five lines in `94ba435`, and the handoff was not
    re-checked):
    - `:113` (R-17 index) is blank; actual `:118`.
    - `:116` (R-07) is the migration line; actual `:121`.
    - `:118-120` (R-08) are indexes and constraints; actual `:123-125`.
    - `:122` (R-02, R2-04, R2-10, R3-06, R3-08) is the `SweepExpiredLeases` line; actual `:127`.
    - `:126-130` (R-04, R3-02) are the sweeper rules; actual commit order `:132-135`.
    - `:162` (R-23) is the renew endpoint; actual `:167`.
    - `:166` (R2-02, R3-07) is the poll response; actual `:164`, `:171`.
    - `:170` (R-16) is a heading; actual `:175`.
    - `:171` (R-11) is the 200-cancel line; actual `:176`.
    - `:217` (R2-03, R3-06) is the validation line; actual `:225`.
    - `:74` (R2-04 `COMPLETED`) is a grace line; actual `:79`.
    - `:86` (R2-08) is `not_before`; actual `:91`.
  - `docs/adr/0014-lease-model-and-expiry.md`:
    - `:98` (R3-08, 400 codes) and `:96-100` (R-10) are commit outcomes; actual `:100-102`.
    - `:136` (R2-05 grace check) is the attempts `SELECT`; actual `:140`.
    - `:180-181` (R2-05) are config names; actual `:179`.
    - `:229` (R-17 index) is "Evaluation order"; actual `:234`.
  - `docs/adr/0016-cancellation-semantics.md`:
    - `:87` (R2-02, R3-07) is a closing JSON brace; actual `:89`.
    - `:124-128` (R-02 terminal-job sweep) is Race 1 / Race 2; actual `:137-138`.
    - `:119` (R3-05 success-branch citation); actual `:121`.
  - `docs/adr/0015-retry-policy-and-failure-classification.md:135` (R3-05 `ErrNoQueuedJobs`) is a
    heading; actual `:133`.
  - `docs/protocol.md`:
    - `:386` is blank.
    - `:419` (R-16) is blank; actual `:418`.
    - `:461` (R-03) is blank; actual `:460`.
    - `:470` is cited (R2-10, R3-08) as the terminal-job sweep event. It is the 409 `LEASE_EXPIRED`
      error-code row, not an event.
  - `docs/failure-model.md`:
    - `:24` (R3-02, FS-06) is FS-04; actual `:26`.
    - `:65` (R3-08, renewal 409) is the result-path 404; actual `:68`.
  - `docs/fault-injection-plan.md:108` (R3-02, FS-06 sub-case B) is the FS-11 race; actual `:71`.
  - `docs/schema.md`:
    - `:387` (R-09, R3-01 "sentence") is the in-lock grace bullet, not the sentence; actual `:399`.
    - `:17`'s "(line 386)" for terminal job states; actual `:391`.
- Why major: the owner made D-R3-03 specifically so the handoff would be truthful. AGENTS.md section 3
  rule 6 forbids unverified line citations, and section 10 counts an unverifiable claim as not done.
  Two of the false claims (`"ACTIVE"`, 10 s) misstate design content that an implementer or the owner
  could act on. No design document is wrong because of this.
- Suggested fix:
  - Correct lines 25, 35, 76, 92, 93, 113 and 226.
  - Replace every fix location above with the "actual" line.
  - Update the round-1 and round-2 status columns to match the round-3 table and this review (or say
    they are historical).
  - Set R3-03 to RESOLVED only after a `sed -n` pass over every cited location.

### R4-02 (minor) The failed-commit-on-`CANCEL_REQUESTED` change (job `FAILED` today, `CANCELLED` in the design) is not labelled as a change in the plan or in state-machine.md, and no slice owns it
- Location:
  - `docs/phase4-plan.md:133` (4b) and `:225` (4d), which say "per today's code" only for the success
    branch;
  - `docs/phase4-plan.md:84-88` (4a in-memory parity) and the gating lists `:100-108`, `:146-153`,
    `:235-239`;
  - `docs/state-machine.md:54`; `docs/failure-model.md:31`.
- Evidence label: **confirmed by code reading** (G2a-inmem-commit, G2a-pg-commit, G2a-transition) and
  **executed** grep (G2a-docs, G2a-tests-cancelreq, G2a-stress, G6-handoff-misc).
- Evidence: see 2a.
  - The change is correct per D-R2-03 and is listed in ADR-0016 section 1 item 4.
  - The plan does not say that it changes today's behavior. It does not say which slice changes the
    in-memory and PostgreSQL `CommitAttempt` (4b and 4d both describe it; 4a's parity list omits it).
  - The planned conformance test is in no gating list.
  - `docs/phase4-plan.md:133` also nests the `CANCEL_REQUESTED` outcome inside the priority-1 "Terminal
    job" bullet, but `CANCEL_REQUESTED` is not terminal.
  - No existing test asserts the old outcome, so nothing breaks.
- Suggested fix:
  - In 4a's parity scope (or 4b's `CommitAttempt` scope), add one line: "Behavior change (D-R2-03,
    ADR-0016 section 1 item 4): a failure commit on `CANCEL_REQUESTED` today gives job `FAILED`
    (`internal/storage/inmemory.go:619-622`, `internal/storage/postgres/repository.go:1050-1054`); Phase 4
    gives job `CANCELLED`."
  - Add `TestConformance_CommitAttempt_FailureOnCancelRequestedYieldsCancelledJob` (planned test) to that
    slice's gating list.
  - Move the `CANCEL_REQUESTED` sentence at `:133` out of the "Terminal job" item.
  - In `docs/state-machine.md:54`, add "(today: `FAILED`)" after the failure branch.

### R4-03 (minor) The event set for a sweep of a `CANCEL_REQUESTED` job differs between documents
- Location: `docs/protocol.md:481` ("recording `JOB_CANCELLED` and `LEASE_EXPIRED`"), against
  `docs/adr/0014-lease-model-and-expiry.md:153`, `docs/schema.md:392`, `docs/phase4-plan.md:128` and
  `docs/adr/0016-cancellation-semantics.md:136` (`JOB_CANCELLED` only).
- Evidence label: **executed** (G2c-409table, G-adr14-sweeper, G2d-schema, G-plan-full, G-R03-R23).
- Evidence: an implementer of 4b following ADR-0014 and schema.md writes one event. One following
  protocol.md writes two, and an event-count assertion in a conformance test would then differ.
- Suggested fix: pick one set and write it in all five places. Recording `LEASE_EXPIRED` plus
  `JOB_CANCELLED` matches the `RUNNING`-job rules (`docs/schema.md:394-395`).

### R4-04 (minor) Slice 4a does not say what happens to `CancelJob`'s queue `Ack`
- Location: `docs/phase4-plan.md:56` against `internal/coordinator/service.go:237-239`.
- Evidence label: **executed** (G2g-cancel-ack, G2g-service) and **confirmed by code reading**.
- Evidence:
  - The plan lists the removal of `SubmitJob`'s `Enqueue` and `CommitResult`'s `Ack`.
  - The memory path's claim loop (`:401-425`) is covered by the rewiring.
  - `CancelJob` also calls `s.queue.Ack` when the job ends `CANCELLED` in memory mode. Without
    `Enqueue`, this call becomes a no-op on an empty queue. It is harmless, but the plan claims to list
    the fate of the queue calls (R3-09).
  - No test drives `svc.CancelJob`: `internal/coordinator/service_test.go:411` and `:486` call the
    repository directly.
- Suggested fix: add "`CancelJob`'s `s.queue.Ack` (`internal/coordinator/service.go:237-239`) is removed"
  to `docs/phase4-plan.md:56`.

### R4-05 (minor) Text leftovers
- Evidence label: **executed** grep (G-R2-10, G-R3-06, G2f-close-running, G2c-errnames, G2c-409table).
- Leftovers:
  - Lowercase `diagnostic_late_result` at `docs/fault-injection-plan.md:47` and
    `docs/security/threat-model.md:132` (R2-10). Use `DIAGNOSTIC_LATE_RESULT`.
  - "awaiting worker acknowledgement" at `docs/state-machine.md:15` (R3-06). No acknowledgement signal
    exists. Use "awaiting the worker's commit or lease expiry".
  - "close the attempt" without "only if still `RUNNING`" at `docs/phase4-plan.md:35` and
    `docs/adr/0016-cancellation-semantics.md:46` (R3-02). The full rule is at `docs/phase4-plan.md:133`.
    Add the qualifier.
  - `ErrAttemptNotRunning` at `docs/PROJECT_STATE.md:174`:
    - It is a planned domain error with no HTTP mapping in any design doc.
    - The mapper's default branch would turn it into 500 `INTERNAL_ERROR`
      (`internal/api/handlers.go:147-153`).
    - `docs/protocol.md:483` consolidates "not running" into `STALE_ATTEMPT_REJECTED`.
    - Either map it to that code in ADR-0014's error block, or drop it from PROJECT_STATE.md.
  - The protocol 6.3 table, priority 5 (`docs/protocol.md:473`), lists `JOB_NOT_FOUND` /
    `ATTEMPT_NOT_FOUND` but not `WORKER_NOT_FOUND`, which 6.1 treats as recognized (`docs/protocol.md:415`).
    Add it.

## Informational notes for the owner (not findings)
1. **The D-R3-06 premise is wrong on one point.** Today, a failed commit on `CANCEL_REQUESTED` gives job
   `FAILED` (`internal/storage/inmemory.go:619-622`, `internal/storage/postgres/repository.go:1050-1054`).
   The documents implement job `CANCELLED` as a change, which is consistent with D-R2-03 (cancel wins).
   Please confirm that this is intended. The handoff did not raise the discrepancy as an open question
   (`docs/agents/handoffs/P4-00.md:208`).
2. After D-R3-06, no Phase 4 path produces `CANCEL_REQUESTED -> FAILED`, but the FSM still allows it
   (`internal/domain/transition.go:22`). Keeping it is harmless. Removing it would flip a third matrix
   cell, so I do not recommend that in this phase.
3. The two flipped cells are confirmed: today `internal/domain/transition_test.go:35` has
   `RETRYING -> QUEUED` true, and `RUNNING` is absent from that row. The design table at
   `docs/state-machine.md:34` has `QUEUED` NO and `RUNNING` YES. The `CANCEL_REQUESTED` row
   (`internal/domain/transition_test.go:29-33`, `docs/state-machine.md:33`) is unchanged. Both flips are
   candidates for owner approval (`docs/phase4-plan.md:45-47`).
4. Renew-path 400s: if the planned renew handler reuses `mapDomainErrorToHTTP`, it can emit 400 codes
   other than the two documented ones (`UNKNOWN_TASK_TYPE`, `INVALID_STATE_TRANSITION` at
   `internal/api/handlers.go:130`, `:150`). The docs could say "any 400" for the worker's
   stop-renewing rule.
5. Protocol 6.3 says its table applies to renewals as well, with `DIAGNOSTIC_LATE_RESULT` written at
   priorities 1 and 2. Writing a "late result" event for a renewal is odd, and no document says whether a
   renewal on a terminal job closes a still-`RUNNING` attempt. After the SEC-022 fix this state should
   not arise, so this is a clarity point.
6. The cancellation bound at `docs/adr/0016-cancellation-semantics.md:157` says "if an outage
   happened". A coordinator restart (startup grace, process time) adds the same 30 s, so "outage or
   restart" would be more exact.

## Standing checks (task items 3 to 5)
- 202 / Accepted / duplicate in a cancel context (G3-202): every hit is HTTP 200, a negation, or
  unrelated (ADR-0008 and ADR-0011 queue duplicates, the SEC-022 log lines). No hit contradicts HTTP
  200.
- `COMPLETED` (G3-completed): every non-review hit is an attempt state or the event name
  `JOB_COMPLETED`, and no job-state use remains.
  - Attempt state: `docs/PROJECT_STATE.md:173`, `docs/adr/0011-postgres-dispatch-and-recovery.md:252`,
    `docs/adr/0014-lease-model-and-expiry.md:97`, `:230`,
    `docs/adr/0015-retry-policy-and-failure-classification.md:41`,
    `docs/adr/0016-cancellation-semantics.md:117`, `docs/fault-injection-plan.md:32`, `:40`,
    `docs/phase4-plan.md:79`, `:133`, `docs/protocol.md:102` (the attempts list), `:475`, `:479`,
    `docs/schema.md:205`, `:346`, `docs/state-machine.md:54`, `:64`, `:66`, `:70`, `:79`, `:91`, `:107`,
    `:139`, `:151`.
  - Event name: `docs/adr/0016-cancellation-semantics.md:120`, `docs/state-machine.md:173`, and inside
    `docs/adr/0014-lease-model-and-expiry.md:97` / `docs/protocol.md:479`.
  - In the handoff, every hit is an attempt state or an event name.
- `git grep -n -E 'tests/integration/sec022|ReturnsAccepted|exit 130|ErrTaskCancelled|four probe' --
  docs CHANGELOG.md ':!docs/agents/reviews'`: empty, exit 1 (G3-forbidden).
- Blueprint (G3-blueprint, G3-blueprint-cites, G3-adr14-bp; `pdftotext` per page on
  `docs/source/MiniCloud_Project_Blueprint.pdf`):
  - Section 5 "Job Lifecycle and Consistency Model" starts on page 5. Its "Lease design" is on page 6,
    and `RETRYING -> QUEUED, FAILED, CANCELLED` is on page 5, line 35.
  - Section 8 "API, Data Model, and Configuration" is on page 8, with "Initial HTTP API (suggested)" as
    its first subheading.
  - Section 10 "Test Strategy and Failure Injection" starts on page 9, and "Cancellation races with
    success completion" is on page 10.
  - All citations at `docs/adr/0014-lease-model-and-expiry.md:23`,
    `docs/adr/0015-retry-policy-and-failure-classification.md:20`, `:103`,
    `docs/adr/0016-cancellation-semantics.md:32-34`, `CHANGELOG.md:20` and `docs/PROJECT_STATE.md:20` are
    correct.
- Allowlist (G3-allowlist): 7 original entries (lines 8 and 11-16) and 4 probe entries (lines 19-22),
  unchanged since `623837e`.
  - `git diff --name-only aa13a4c..HEAD | grep -v -E '^(docs/|CHANGELOG.md)'` prints only
    `tests/docs/citation_allowlist.txt`.
- SEC-022 evidence (G3-sec022): `diff <(sed -n 313,359p docs/security/findings.md) <(sed -n 4,51p
  docs/agents/reviews/P4-00-evidence.txt)` differs only by E1's trailing blank line (`47a48`), as in
  round 3. `P4-00-evidence.txt` is unchanged since `623837e`.
- Cross-document consistency (G4-*, G-adr14-sweeper, G2d-schema):
  - `error_class` has four values in ADR-0015:59, schema.md:314 and plan:117, and three
    worker-reportable values in protocol.md:460 and plan:219. These agree.
  - The 409 order agrees (2f).
  - The lock order and the three sweeper SQL statements are identical in
    `docs/adr/0014-lease-model-and-expiry.md:127-136` and `docs/schema.md:370-376`.
  - The numbers agree: lease 30 s, renewal 10 s, sweeper 5 s, batch 50 (`LIMIT 50`), `lock_timeout`
    5 s, grace 30 s.
  - `RETRYING -> RUNNING` via dispatch agrees.
  - Exactly two matrix cells flip (note 3).
  - The rule for `RUNNING` attempts of terminal jobs covers `SUCCEEDED`, `FAILED` and `CANCELLED` jobs
    in ADR-0014:144-148, schema.md:391, plan:127 and ADR-0016:138.
- Citations:
  - `make test-docs` passes. The checker validates line bounds and labels, not content, which is why
    R4-01's wrong locations pass it.
  - Line citations checked by hand: over 60 (G2a-*, G2g-*, G-R3-05, G6-*).
  - Correct code citations: `internal/domain/transition.go:21`, `:4-33`;
    `internal/storage/inmemory.go:494`, `:578-579`, `:615-618`, `:619-622`, `:658-700`;
    `internal/storage/postgres/repository.go:983-985`, `:1046-1049`, `:1135-1233`;
    `internal/coordinator/service.go:71-72`, `:194-195`, `:512-513`;
    `internal/coordinator/service_test.go:338-381`, the seven test lines and `:47`;
    `internal/api/api_test.go:27`, `:58`; `tests/integration/multi_worker_batch_test.go:39-40`;
    `tests/integration/coordinator_restart_test.go:67`; `tests/integration/worker_liveness_test.go:43`;
    `docs/fault-injection-plan.md:141`.
  - Wrong citations: only the handoff locations in R4-01.
- Test names (G5-testnames, G2g-list): every test name added since `623837e` either exists in
  `go test -list` (the seven coordinator tests and `TestJobTransitionMatrix_AllPairs`), is labelled
  "(planned test)" or "(audit probe ...)" in its own clause, or is allowlisted.

## Attacks attempted that did NOT find issues
- Grace window stacking: under the identical sentence, a failed tick during a cooldown cannot open a
  second window, so suppression is bounded by one window. The lock-timeout exclusion keeps a hot row
  from opening grace.
- Retried commit after a lost response (FS-06 B): it gets 409 at priority 1. The `COMPLETED` attempt is
  not rewritten, because the "RUNNING only" rule is present in all four normative places.
- `LOST` attempt rewritten to `CANCELLED` after a cancel of a `RETRYING` job: blocked by the same rule.
- Renewal after expiry but before the sweep on `CANCEL_REQUESTED`: the more specific "no extension"
  rule is now stated next to every accept-until-swept statement. No ambiguity remains.
- The `"RENEWED"`, `"ACK"` and `"ACTIVE"` status values across all documents: consistent (`"RENEWED"`
  for renewals, `"ACK"` for heartbeats). `"ACTIVE"` appears only in the handoff.
- Slot double-free (sweeper against commit): slots are derived from `RUNNING`/`PENDING` attempts
  (unchanged since round 3).
- Hidden test breakage from memory dispatch rewiring: none beyond the seven listed (2g).
- Hidden test breakage from the `FAILED -> CANCELLED` change: none (2a).
- Old grace wording or slow-tick tests: none survive.

## Tests added
None. This is a docs-only targeted re-audit, and no throwaway worktree was used.
