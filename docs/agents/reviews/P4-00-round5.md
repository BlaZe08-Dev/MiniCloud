# Review P4-00 (round 5, final check, handoff-focused)

## Commit reviewed
`a9efa6e` on `origin/docs/phase4-design` (`docs(handoff): avoid awaiting worker acknowledgement phrase in
handoff text`). Review branch: `agent/claude/P4-00-review-5`, based on `a9efa6e`.

Scope: the 10 commits after `69b3864` (`523d2e2` to `a9efa6e`; the list is in the `start` section of the
evidence file), checked against round 4 (`docs/agents/reviews/P4-00-round4.md`) and the owner decisions
D-R4-01 to D-R4-05.

I edited no design doc, no Go code and not the allowlist. I re-derived everything with my own commands
and did not rely on Gemini's report, the handoff or `docs/agents/handoffs/P4-00-citation-check.txt`. The
raw output is in `docs/agents/reviews/P4-00-round5-evidence.txt`, one `### [G..]` section per command.

## Verdict: APPROVE

- **Safe to merge:** commit `a9efa6e`.
- **No blocker and no major finding is open.** Two minor findings are open (R5-01, R5-02), plus one
  informational cleanup list.
- **R5-01 is the handoff.** It still contains about 30 citation tokens that point at the wrong line,
  plus a few unsupported sentences. I rate it minor under the owner's rule for this round: severity is
  major only if the claim would mislead the owner about what is merged or tested.
  - None of the errors misstates what is merged or tested. The gate output, the `make test-db was NOT
    run` statement and the test-name labels are all true.
  - The design documents the citations point into are correct.
  - The owner should not rely on the handoff's fix-location columns. Use the corrected locations in R5-01
    and in this review's status table.
- **R5-02 is a design-doc wording and sequencing issue** around `domain.ErrAttemptNotRunning`. It
  touches no code.
- **Both minor findings can be fixed after the merge without blocking it.** R5-02 should be fixed before
  slice 4b starts, because it decides which slice adds the HTTP mapping. R5-01 is a handoff-only cleanup
  and needs no further audit.

## Preflight and gates (executed; evidence: preflight, start, G7-*)
- Preflight: `worktree: /home/blazeee/MiniCloud-claude`, `test DB:  minicloud_claude_test`,
  `preflight OK`. No other worktree is involved. Tip `a9efa6e`, working tree clean before the review
  files. Go `go1.22.2 linux/amd64`. `AGENTS.md` is unchanged since `69b3864`.
- `gofmt -l .` lists nothing, and `go vet ./...` and `go build ./...` exit 0.
- `make test-docs` prints `ok ... tests/docs 0.367s`, and uncached it prints `ok ... 0.352s`.
- `env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=1 ./...` exits 0.
- `-v` counts: `PASS 77 SKIP 26 FAIL 0`, the expected numbers. The 26 skips are database tests and are
  not passes. `make test-db` was not run, because this is a docs-only audit.

## Check 1: status of the open items

| ID | Status | Fix location (verified with `sed -n` / `awk`) | Evidence / what remains |
|---|---|---|---|
| R4-01 | PARTIAL (residual is R5-01, minor under the owner's rule) | `docs/agents/handoffs/P4-00.md` | The claims round 4 named are fixed: `"ACTIVE"` is gone, the sweeper is 5 s, `docs/agents/TASKS.md:47` lists the round-2, -3 and -4 reviews, and the status columns match round 4. About 30 citation tokens are still wrong, and a few sentences are unsupported (R5-01). |
| R4-02 | RESOLVED | `docs/phase4-plan.md:136`, `:229`, `:242`; `docs/adr/0016-cancellation-semantics.md:51-52`; `docs/state-machine.md:54` | See check 4. |
| R4-03 | RESOLVED | `docs/protocol.md:482` now says `JOB_CANCELLED` only. This matches `docs/adr/0014-lease-model-and-expiry.md:153`, `docs/schema.md:392`, `docs/phase4-plan.md:128` and `docs/adr/0016-cancellation-semantics.md:136` (G1-R4-05). | |
| R4-04 | RESOLVED | `docs/phase4-plan.md:56` names `CancelJob`'s `s.queue.Ack` (`internal/coordinator/service.go:237-239`). | |
| R4-05 | RESOLVED | See the item list below. | The `ErrAttemptNotRunning` text is now present but has wrong claims about today's code: new finding R5-02. |
| R3-03 | PARTIAL (R5-01, minor) | `docs/agents/handoffs/P4-00.md` | `:211` (`make test-db was NOT run`) is true. |
| R2-07 | PARTIAL (R5-01, minor) | `docs/agents/handoffs/P4-00.md` | |
| R2-10 | RESOLVED | `docs/fault-injection-plan.md:47`, `docs/security/threat-model.md:132` | No lowercase event name remains in the design docs (G1-R4-05, empty grep). |

R4-05 items (G1-R4-05, G-design-diff):
- Uppercase `DIAGNOSTIC_LATE_RESULT` everywhere.
- `docs/state-machine.md:15` now reads "awaiting the worker's commit or lease expiry".
- The "only if the attempt is still `RUNNING`" qualifier is at `docs/phase4-plan.md:35` and
  `docs/adr/0016-cancellation-semantics.md:46`.
- There is a `WORKER_NOT_FOUND` row at `docs/protocol.md:473`.
- The `ErrAttemptNotRunning` to 409 mapping is at `docs/protocol.md:471`, `:484`,
  `docs/adr/0014-lease-model-and-expiry.md:232`, `docs/phase4-plan.md:135` and `:169`.

## Check 2: the handoff, line by line (executed: G2-*)

**Mechanical regeneration.**
- I extracted every `path:line` / `path:start-end` token from the final handoff with my own script
  (`regen.sh`; the command is in G2-regen-diff).
- For each token I printed the first and last cited line, cut to 170 characters, and sorted the tokens.
- The result is **byte-identical** to `docs/agents/handoffs/P4-00-citation-check.txt` (`diff` prints
  nothing, then `IDENTICAL`; 190 tokens, 439 lines).
- So the file was produced mechanically from the final handoff text. It only prints lines, though. It
  does not judge whether a line supports its claim. That judgement is below.

**Per-line judgement.** I printed every token with its cited text, grouped by handoff line
(G2-perline-dump), and read each one against the sentence it supports. Most tokens are right. The
mismatches are listed in R5-01, with the handoff line number, the token, what is there, and where the
claim actually is (G2-actuals).

**Other handoff checks.**
- Numbers (G2-numbers): lease 30 s, renewal 10 s, sweeper 5 s, grace 30 s, cooldown 30 s and the 60 s
  delivery bound are all correct. One attribution is not: `:15` and `:92` say ADR-0016 states "sweeper
  cadence (5 s)". `docs/adr/0016-cancellation-semantics.md:157` says only "plus sweeper cadence". The
  value is right (`docs/adr/0014-lease-model-and-expiry.md:53`), but it is not in the cited line.
- Commit hashes (G2-hashes): all 16 hashes in backticks are commits, and all are ancestors of `HEAD`.
- Test names (G2-testnames):
  - Exist in `go test -list`: `TestJobTransitionMatrix_AllPairs`, the seven coordinator service-test names and
    `TestMigrator_PreExistingInvalidRow_ValidationFails`.
  - Allowlisted: the three audit-probe test names.
  - Every other name is labelled "(planned test)". Line 27 introduces two of them with "with planned
    tests" and puts a citation in the parentheses (see note 3).
  - `:237` describes `TestPostgresRepository_CommitAttempt_TerminalJobClosesAttempt` (planned test) as
    "Verifies sweeper closes attempt". The name and `docs/phase4-plan.md:146` make it the `CommitAttempt`
    path (R5-01).
- Status columns: compared row by row with `docs/agents/reviews/P4-00-round4.md` (and with round 3 for
  the rows round 4 did not list).
  - R-01 to R-24 are all RESOLVED.
  - R2-07 and R2-10 are PARTIAL, and all other R2 rows are RESOLVED.
  - R3-03 is "PARTIAL (R4-01, major)", and R3-01, R3-02 and R3-04 to R3-09 are RESOLVED.
  - All of these match round 4, as D-R4-01 required.
  - The round-4 table (`:100-104`) is the handoff's own claim. It marks R4-01 RESOLVED, which
    contradicts `:88` (R3-03 PARTIAL "R4-01") and is not true (R5-01).
- "Branch / last commit" (`:3-5`): it says `Last commit: d3cf819 (...)`. `d3cf819` exists on the branch,
  but two later commits follow it (`13288c8`, `a9efa6e`; G2-line5). The wording does not say that it
  names the last commit *before the handoff commits*. **This fails the stated test** (R5-01).
- `make test-db was NOT run` (`:211`): true, and consistent with "Not tested" (`:256`).
- Deviations (`:111-121`): the handoff does not say "none". It lists the allowlist deviation, which is
  true: scope shows only `tests/docs/citation_allowlist.txt` outside `docs/`.
- "Open questions: none" (`:219`): not quite true. The slice sequencing of `ErrAttemptNotRunning`
  (R5-02) is an open design question that the handoff should have raised.
- "All owner decisions have been implemented in full" (`:123`): not fully. D-R4-01 asked for one
  citation per token. Short-form citations remain at `:46` (`state-machine.md:15`), `:47`
  (`test-plan.md:59`), `:51` (`failure-model.md:81-84`), `:93` (`schema.md:391`, `protocol.md:470`,
  `ADR-0014:148`) and `:108` (`000001_initial_schema.sql:97`) (G2-shortform).

## Check 3: `ErrAttemptNotRunning` (executed: G3-*)
- **Today's code:** the identifier does not exist. `grep -rn ErrAttemptNotRunning --include=*.go .`
  finds nothing, and `internal/domain/errors.go:8-26` has no such variable.
  - A commit on a non-`RUNNING` attempt returns `domain.ErrStaleAttempt` in both repositories
    (`internal/storage/inmemory.go:589-590`, `internal/storage/postgres/repository.go:1023-1024`).
  - `ErrStaleAttempt` is already mapped to 409 `STALE_ATTEMPT_REJECTED` by `mapDomainErrorToHTTP`
    (`internal/api/handlers.go:125-126`). An unknown error falls to the default 500 branch
    (`internal/api/handlers.go:152-153`).
  - So "today it has no HTTP mapping and surfaces as HTTP 500" is true only for a future error that does
    not exist yet.
  - "`domain.ErrStaleAttempt` ... (surfaces as HTTP 500 today)" at `docs/phase4-plan.md:169` is false.
- **Who builds what:**
  - The definition of the error is named only in `docs/PROJECT_STATE.md:174` (4a typed errors). The 4a
    scope in `docs/phase4-plan.md:80-88` does not list it.
  - 4b makes the repository return it (`docs/phase4-plan.md:135`) and gates on
    `TestPostgresRepository_CommitAttempt_AttemptNotRunning_ReturnsStaleAttempt` (planned test) at
    `docs/phase4-plan.md:153`.
  - 4c adds the HTTP mapping (`docs/phase4-plan.md:169`) and gates on
    `TestAPI_CommitAttempt_AttemptNotRunning_Returns409StaleAttemptRejected` (planned test) at
    `docs/phase4-plan.md:196`.
  - protocol.md (`:471`, `:484`) and ADR-0014 (`:232`) say only "Phase 4 maps", with no slice. They do
    not contradict the plan.
  - Both planned tests are labelled and placed in gating lists.
- **The gap:** if 4b switches the repository from `ErrStaleAttempt` to `ErrAttemptNotRunning` before 4c
  adds the mapping, the API turns today's 409 into a 500 for the period between the two merges.
  - The existing API test only checks the wrong-token case (`internal/api/api_test.go:189-190`), and that
    case stays `ErrStaleAttempt`. So no current test would catch the regression.
  - This is **R5-02 (minor)**.

## Check 4: D-R4-02 (confirmed by code reading and executed: G4-*)
- Today, a failure outcome sets attempt `FAILED` and target job state `FAILED` in both repositories:
  - in-memory: `internal/storage/inmemory.go:619-622`, applied at `:625-628`;
  - PostgreSQL: `internal/storage/postgres/repository.go:1050-1054`, written by the `UPDATE jobs` at
    `:1074-1083`.
- Nothing special-cases `CANCEL_REQUESTED`, and the FSM permits the transition
  (`internal/domain/transition.go:22`). **Today: job `FAILED`.**
- The new outcome (job `CANCELLED`) is labelled as a behavior change from today in three places:
  - the plan: `docs/phase4-plan.md:229` (4d scope), with the 4b pointer "(built in Slice 4d)" at `:136`;
  - ADR-0016: `docs/adr/0016-cancellation-semantics.md:51-52`, section 1 item 4, with both today
    citations;
  - state-machine.md: `docs/state-machine.md:54`.
- The planned test `TestConformance_CommitAttempt_FailureOnCancelRequestedYieldsCancelledJob` (planned
  test) is in the 4d gating list at `docs/phase4-plan.md:242` (4d spans `:217-249`).
- No existing test asserts the old outcome. The plan's claim at `:229` matches round 4's evidence
  (`internal/domain/transition_test.go:27-33`, `internal/storage/storagetest/conformance.go:360`).

## Check 5: standing checks (executed: G5-*)
- 202 / Accepted in a cancel context (G5-202): no contradiction with HTTP 200.
  - `docs/adr/0016-cancellation-semantics.md:4` is "Accepted by Owner".
  - `:85` matched only because the timestamp contains "202".
  - The rest are HTTP 200 statements, negations, or SEC-022 log lines.
- `COMPLETED` (G5-completed): every non-review hit is an attempt state or the event `JOB_COMPLETED`.
  - `docs/PROJECT_STATE.md:173`, `docs/adr/0011-postgres-dispatch-and-recovery.md:252`,
    `docs/adr/0014-lease-model-and-expiry.md:97`, `:230`,
    `docs/adr/0015-retry-policy-and-failure-classification.md:41`,
    `docs/adr/0016-cancellation-semantics.md:117`, `:120`, `docs/fault-injection-plan.md:32`, `:40`,
    `docs/phase4-plan.md:79`, `:136`, `docs/protocol.md:102`, `:476`, `:480`, `docs/schema.md:205`,
    `:346`, `docs/state-machine.md:54`, `:64`, `:66`, `:70`, `:79`, `:91`, `:107`, `:139`, `:151`, `:173`.
  - No job-state use remains.
- Forbidden grep (G5-forbidden): **not empty.** It matches `docs/agents/handoffs/P4-00.md:49` and `:86`,
  which say the "connection-class" restriction was *removed*. No design document matches. These are
  historical mentions in the handoff (R5-01, cosmetic). They are not a design regression.
- `status.{1,4}(ACTIVE)` (G5-active): empty. Renewal status is `"RENEWED"` in all five places, and
  heartbeat status is `"ACK"`.
- Grace sentences (G5-grace): the extracted sentences at
  `docs/adr/0014-lease-model-and-expiry.md:179`, `docs/schema.md:399`, `docs/protocol.md:409`,
  `docs/phase4-plan.md:29`, `:182`, `docs/failure-model.md:10` and `:28` all hash to
  `a56db2f06490861ce1be412d9394d3979a701b0890a35dfc109e1f127858e076`, so they are byte-identical.
- Allowlist (G5-allowlist): 11 entries, 7 original plus 4 probe entries (lines 8, 11-16, 19-22), and no
  change since `69b3864`. Scope: `git diff --name-only aa13a4c..HEAD | grep -v -E '^(docs/|CHANGELOG.md)'`
  prints only `tests/docs/citation_allowlist.txt`.
- SEC-022 (G5-sec022): the diff against E1 shows only E1's trailing blank line (`47a48`). Neither file
  changed since `69b3864`.
- Blueprint (G5-blueprint; `pdftotext` per page of `docs/source/MiniCloud_Project_Blueprint.pdf`):
  - Section 4 "System Architecture" starts on page 4.
  - Section 5 "Job Lifecycle and Consistency Model" starts on page 5, with the `RETRYING` row on page 5
    and "Lease design" on page 6.
  - Section 8 "API, Data Model, and Configuration" is on page 8, with "Initial HTTP API (suggested)".
  - Section 10 "Test Strategy and Failure Injection" starts on page 9, with "Cancellation races with
    success completion" on page 10.
  - Every cited title and page range is correct.

## Check 6: citations (executed: G6-*, G7-testdocs)
- `make test-docs` passes. It checks bounds and labels, not meaning, which is why R5-01's tokens pass.
- I verified by hand the citations that the post-`69b3864` design changes added (G6-design-cites): 11
  distinct tokens, 26 occurrences.
  - Correct: `internal/coordinator/service.go:194-195`, `:237-239`, `:512-513`;
    `internal/domain/transition.go:21`; `internal/domain/transition_test.go:27-33`;
    `internal/storage/inmemory.go:615-618`, `:619-622`; `internal/storage/postgres/repository.go:1046-1049`,
    `:1050-1054`; `internal/storage/storagetest/conformance.go:360`.
  - `internal/api/handlers.go:110-154` is correct but stops one line short of the function's closing
    brace at `:155`, which is harmless.
- I also checked the design-doc lines cited by the handoff (more than 150 occurrences, G2-perline-dump).
- Test names added since `69b3864` (G6-testnames): none is claimed to exist. All three new names are
  labelled "(planned test)". The test names cited as existing in the handoff exist (check 2).

## New findings

### R5-01 (minor under the owner's rule) The handoff still has about 30 wrong citation tokens and a few unsupported sentences
- Location: `docs/agents/handoffs/P4-00.md` (line numbers below).
- Evidence label: **executed** (G2-perline-dump, G2-actuals, G2-shortform, G2-line5, G2-tasks-preflight,
  G2-testdesc, G5-forbidden).
- Why it is minor: none of these errors says something false about what is merged, built or tested.
  Each one points to the wrong line for a statement that is true in the design documents.

Wrong citation tokens. Format: handoff line, then the token, then what the cited line is, then where the
claimed content is.
- `docs/phase4-plan.md`:
  - `:27`, `:49`, `:51`, `:74`, `:86`: `docs/phase4-plan.md:183` (grace sentence) is "Exit Criteria"; the
    sentence is at `:182`.
  - `:27`, `:50`, `:104`: `docs/phase4-plan.md:134` (4b `ErrAttemptNotRunning`) is the `LOST` row; the
    mapping is at `:135`.
  - `:27`, `:101`: `docs/phase4-plan.md:246` (4d gating test) is the risks line; the test is at `:242`.
  - `:51`: `docs/phase4-plan.md:176` (45 s outage exit criterion) is renewal 404 handling; the criterion
    is at `:186`.
  - `:56`: `docs/phase4-plan.md:175` (retry renewal on next interval) is the 403/409 terminal rule; the
    retry is at `:178`.
  - `:71`, `:92`: `docs/phase4-plan.md:164` (renewal not extended) is the endpoint line; the content is
    at `:166`. `docs/phase4-plan.md:171` (same claim) is the "Worker Agent" heading; the content is at
    `:174`.
  - `:93`: `docs/phase4-plan.md:164` (`"RENEWED"`) is the endpoint line; the status is at `:165-166`.
  - `:103`: `docs/phase4-plan.md:55` (`CancelJob` `Ack`) is a heading; the content is at `:56`.
- `docs/PROJECT_STATE.md`:
  - `:31`, `:43`: `docs/PROJECT_STATE.md:199` (4-class taxonomy) is "- **Scope:**"; the content is at
    `:200`.
  - `:31`, `:63`: `docs/PROJECT_STATE.md:193` (planned endpoint defenses) is the sweeper-ticker line; the
    content is at `:194`.
- `docs/adr/0014-lease-model-and-expiry.md`:
  - `:13`, `:71`, `:92`: `:89` (renewal on `CANCEL_REQUESTED` not extended) is `"status": "RENEWED"`; the
    content is at `:95`.
  - `:13`: `:229-234` is cited as holding "403 `WORKER_MISMATCH` and 404s, plus 'Diagnostic Event
    Written?' column". ADR-0014 lists only the three 409s (`:229-233`). The 403/404 rows and the column
    are in `docs/protocol.md:467-474`.
- `docs/adr/0016-cancellation-semantics.md`:
  - `:15`: `:73` ("standardized event name `JOB_CANCEL_REQUESTED`") is the 409 `JOB_ALREADY_TERMINAL`
    line; the event is at `:66`.
  - `:15`: `:121` ("failure moves `CANCEL_REQUESTED -> CANCELLED`") is the success line; the failure
    branch is at `:124`.
- `docs/failure-model.md`:
  - `:23`: `:69` (deterministic 400 is terminal on the *result* path) is the *renewal*-path 400 rule;
    the result path is at `:63`.
  - `:23`: `:66` ("renewal 409 codes") is the result-path 409 line; the renewal rule is at `:68`, which
    is also cited.
  - `:23`: `:81-84` ("bounded result delivery loop") is the planned-test list; the bound is at `:72`.
  - `:60`: `:73` (R-20) is a blank line.
- `:77`: `docs/agents/handoffs/P4-00.md:99` (R2-08 allowlist note) is a `|---|` table separator; the note
  is at `:109`.
- `:93`: `protocol.md:470` (short form) is cited as the terminal-job sweep *event*. It is the 409
  `LEASE_EXPIRED` *error-code* row. Round 4 flagged this exact citation, and it is unchanged.

Unsupported or false sentences:
- `:5`: "Last commit: `d3cf819`". Two later commits exist (`13288c8`, `a9efa6e`), and the wording does
  not say that it names the last commit before the handoff commits.
- `:76` (R2-07) says the handoff "verified all fix locations with mechanically generated citation check".
  The generated file only prints lines (my regeneration is identical), and the locations above are wrong.
- `:100` marks R4-01 RESOLVED, with "every citation formatted as single token" and "mechanically
  verified". Short-form citations remain at `:46`, `:47`, `:51`, `:93` and `:108`. It also contradicts
  `:88`, where R3-03 is "PARTIAL (R4-01, major)".
- `:15`, `:92`: "sweeper cadence (5 s)" is attributed to `docs/adr/0016-cancellation-semantics.md:157`,
  which gives no number. The value is correct.
- `:50`: "(surfacing as HTTP 500 today)" for `ErrAttemptNotRunning`, which does not exist today (R5-02).
- `:147-148`: the pasted "verbatim" preflight command is corrupted.
  - `printf '%s\n'` became a literal line break.
  - `\1` became the control byte `0x01` (`cat -A` shows `^A`; G2-tasks-preflight).
  - Run as written, it could not print `test DB:  minicloud_gemini_test`. So it is not the command that
    produced the pasted output. AGENTS.md has `\1` at `:289`.
- `:237`: the planned `TestPostgresRepository_CommitAttempt_TerminalJobClosesAttempt` is described as
  "Verifies sweeper closes attempt". It tests `CommitAttempt` (`docs/phase4-plan.md:146`).
- `:123` says all owner decisions are "implemented in full". D-R4-01's one-token rule is not fully met
  (see the short forms above).
- `:219`: "Open questions: none". The `ErrAttemptNotRunning` slice sequencing (R5-02) was open.
- `:49`, `:86` mention "connection-class" (as removed), so the owner's forbidden grep is not empty.

Suggested fix (handoff only; no design change and no audit needed):
- Replace each token above with the line given.
- Expand the short forms to full-path tokens.
- Write `:5` as "Last design commit before this handoff: `d3cf819`; the handoff itself is committed after
  it".
- Paste the preflight from AGENTS.md section 9 exactly.
- Reword `:49` and `:86` without the phrase.
- Correct `:50`, `:76`, `:100`, `:123`, `:219` and `:237`.

### R5-02 (minor) The `ErrAttemptNotRunning` text misstates today's code and leaves a 4b-to-4c window that turns a 409 into a 500
- Location:
  - `docs/phase4-plan.md:135` and `:169`;
  - `docs/adr/0014-lease-model-and-expiry.md:232`;
  - `docs/protocol.md:484`;
  - `docs/PROJECT_STATE.md:174`, the only place that schedules the definition.
- Evidence label: **confirmed by code reading** (G3-code) and **executed** grep (G3-docs, G3-tests-stale).
- Evidence (check 3):
  - The identifier does not exist today, so it cannot "surface as HTTP 500 today".
  - The error that does exist for a non-`RUNNING` attempt, `ErrStaleAttempt`
    (`internal/storage/inmemory.go:589-590`, `internal/storage/postgres/repository.go:1023-1024`), is
    already mapped to 409 (`internal/api/handlers.go:125-126`). `docs/phase4-plan.md:169` says it
    "surfaces as HTTP 500 today", which is false.
  - The plan has 4b return the new error (`:135`) but 4c add the mapping (`:169`). If 4b merges first,
    the API answers 500 instead of today's 409 for a stale non-running attempt. No current test pins
    that path (`internal/api/api_test.go:189-190` only checks a wrong token).
- Suggested fix (pick one, before slice 4b starts):
  - Option (a): define `ErrAttemptNotRunning` as wrapping `ErrStaleAttempt` (for example
    `fmt.Errorf("%w: attempt not running", ErrStaleAttempt)`). The existing mapping then covers it from
    the first slice, and the 4c mapping line becomes a no-op.
  - Option (b): move the mapping line and its API test into 4b, the first slice that returns the error.
  - Either way:
    - Reword "today it has no HTTP mapping and surfaces as HTTP 500" as "if added without a mapping it
      would surface as HTTP 500".
    - Delete "and `domain.ErrStaleAttempt`" and "(surfaces as HTTP 500 today)" from `:169`.
    - Name the definition in 4a's scope.

## Informational notes for the owner (not findings)
1. The owner text called the `ErrAttemptNotRunning` 500 "today's" behavior in conditional form ("today it
   would surface as 500"). The documents dropped the condition. R5-02 restores it.
2. `docs/failure-model.md:31` (FS-11) gives the new failure outcome without the "behavior change from
   today" label. The three documents D-R4-02 named all carry the label, so this is cosmetic.
3. Handoff line 27 introduces the two new planned tests with "with planned tests `X` (`citation`)" rather
   than "`X` (planned test)". `make test-docs` accepts it, and the design docs label both names correctly
   (`docs/phase4-plan.md:153`, `:196`).
4. The handoff "Fixed in part 6" column is a new column that round 4 did not request. I did not audit it
   row by row beyond the decisions named in this task.

## Attacks attempted that did NOT find issues
- Citation-check file forged or hand-edited: no. My independent regeneration is byte-identical.
- Status columns changed against round 4: no. Every R-, R2- and R3- row matches.
- Commit hashes in the handoff that do not exist or are not on the branch: none.
- Old or contradictory numbers (10 s sweeper, `"ACTIVE"`, `"ACK"` for renewal): none in the design
  docs, and none in the handoff.
- A second event in the `CANCEL_REQUESTED` sweep: none left in any of the five documents.
- Lowercase event names in the design docs: none.
- Grace-rule drift: none. All seven copies hash the same.
- Scope creep beyond docs plus the allowlist: none. The allowlist is unchanged.
- An existing test broken by the D-R4-02 change: none (round-4 evidence, re-confirmed in check 4).
- Blueprint titles and pages: all correct against the PDF.

## Tests added
None. This is a docs-only audit, and no throwaway worktree was used.
