# Review P4-00 (round 3, targeted re-audit)

## Commit reviewed
`623837e` on `origin/docs/phase4-design` (`docs(agents): rebuild P4-00 handoff with round 2
resolution and corrected claims`). Review branch: `agent/claude/P4-00-review-3`, based on `623837e`.
Scope: the 14 commits `884dc01..623837e` (`git diff c719880..HEAD`), checked against round 2
(`docs/agents/reviews/P4-00-round2.md`), round 1 (`docs/agents/reviews/P4-00.md`), the owner decisions
D-R2-01 to D-R2-10, and accepted rule R-09. I edited no design doc, no Go code and not the allowlist.
I re-derived every result below and did not rely on the handoff. The raw output is in
`docs/agents/reviews/P4-00-round3-evidence.txt`. Each section there is headed `### [G..]`, and the
sections are cited below by those labels.

## Verdict: CHANGES REQUIRED

A verdict of APPROVE requires that no blocker and no major finding is open. Three major findings
are open, and there is no blocker.
- R3-01 (major): the outage-grace trigger is narrowed to "connection-class errors only", which
  contradicts the owner rule R-09 (any failed tick). The five documents also word the trigger five
  different ways.
- R3-02 (major): a commit on a terminal job has two contradictory contracts. FS-06 promises HTTP 200
  `COMMITTED` on a retry after a successful commit, while protocol 6.3 and ADR-0014 return 409
  `JOB_ALREADY_TERMINAL`. The "close the attempt" rule is also not restricted to `RUNNING`
  attempts, so a literal implementation would rewrite `COMPLETED` or `LOST` attempts.
- R3-03 (major): the handoff still contains claims that cannot be verified or are false. The most
  serious is a `make test-db` run with "zero test failures" that has no output and is contradicted
  by the handoff's own "Not tested" section.

Round-2 status: 5 of 11 are resolved (R2-02, R2-04, R2-06, R2-08, R2-09) and 6 are partial (R2-01,
R2-03, R2-05, R2-07, R2-10, R2-11). Round-1 status: of the 16 partial findings, 11 are now resolved and 5 remain partial.

**Can it be fixed in one follow-up commit without another audit?** Not entirely. The minor
findings (R3-04 to R3-09) are mechanical text and citation fixes. They can go in one follow-up
commit without another audit. The three major findings are also text-only, but each one changes a
contract or a merge-relevant claim:
- R3-01 needs one owner-worded sentence in five documents, plus renamed planned tests.
- R3-02 needs a choice between 200 and 409 for a retried commit (I recommend 409, which is what the
  code does today).
- R3-03 needs the handoff claims corrected.

These need a short targeted re-check, not a full round 4: the greps in G3a, G9-fs06-origin and
G3b, plus `sed -n` on the handoff.

## Preflight and gates (executed; evidence: preflight, start, G8-gates, G8-testdocs-uncached, G8-race-unset, G8-counts)
- Preflight: `test DB:  minicloud_gemini_test`, `preflight OK`. This review ran in the
  `~/MiniCloud-gemini` worktree because the task setup specified it, so the preflight reports
  Gemini's database. No database test was run. Go `go1.22.2 linux/amd64`. Tip `623837e`, working
  tree clean.
- `gofmt -l .` lists nothing. `go vet ./...` and `go build ./...` exit 0. `make test-docs` prints
  `ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	(cached)`. Re-run uncached:
  `go test -count=1 ./tests/docs` gives `ok ... 0.316s`.
- `env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=1 ./...` exits 0 (tail in G8-race-unset).
- `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...` gives `PASS 77 SKIP 26 FAIL 0`,
  matching the expected 77/26/0. The 26 skips are database tests and are not passes. The DB URL was
  unset on purpose: this is a docs-only re-audit.

## Round-2 findings (R2-01 .. R2-11)

| ID | Status | Fix location (verified with `sed -n` / `git grep`) | What remains |
|---|---|---|---|
| R2-01 | PARTIAL (minor residual, R3-09) | `docs/phase4-plan.md:53-66`, `docs/adr/0015-retry-policy-and-failure-classification.md:126` | The core is fixed per D-R2-01: the "keep passing" claim is gone, and the seven `NewServiceWithQueue` tests are listed and marked candidates for owner approval. `go test -list` confirms all seven names, and `sed -n` confirms their lines 386, 458, 526, 575, 665, 716 and 764 (G3d-spyqueue, G3d-callers). What remains is R3-09. |
| R2-02 | RESOLVED | `docs/adr/0014-lease-model-and-expiry.md:94`, `docs/adr/0016-cancellation-semantics.md:87`, `docs/protocol.md:385`, `docs/state-machine.md:54`, `docs/failure-model.md:31`, `docs/phase4-plan.md:159`, `docs/phase4-plan.md:166` | Per D-R2-02: a renewal on `CANCEL_REQUESTED` returns `cancel_requested: true` and does not extend the lease. The bound therefore holds even when the worker keeps renewing. The bound wording and the general accept-until-swept text still need small edits (R3-07, minor). |
| R2-03 | PARTIAL (R3-06) | `docs/adr/0016-cancellation-semantics.md:113-123`, `docs/state-machine.md:54`, `docs/failure-model.md:31`, `docs/phase4-plan.md:217` | The job outcome matches D-R2-03 in all four documents (success gives `SUCCEEDED`, any failure gives `CANCELLED`, never `RETRYING`), and no wire acknowledgement is needed. The attempt end state still conflicts between documents, and protocol.md says nothing on it (R3-06). |
| R2-04 | RESOLVED, and partially invalid (see note) | `docs/schema.md:391`, `docs/phase4-plan.md:122`, `docs/protocol.md:148`, `docs/protocol.md:470` | `git grep COMPLETED -- docs` finds no remaining job-state use (G4b). |
| R2-05 | PARTIAL (R3-01, major) | `docs/adr/0014-lease-model-and-expiry.md:136`, `docs/adr/0014-lease-model-and-expiry.md:180-181`, `docs/schema.md:383-388`, `docs/schema.md:399-400`, `docs/protocol.md:409-410`, `docs/failure-model.md:10`, `docs/phase4-plan.md:29` | Fixed per D-R2-05: one authoritative in-lock check, with any earlier check labelled advisory; the cooldown starts when a window ends; maximum suppression is one window. Open: the trigger is narrowed against R-09, the wording differs across documents, ADR-0014, schema.md, protocol.md and failure-model.md give no cooldown length, and the planned tests contradict each other (R3-01). |
| R2-06 | RESOLVED | `docs/security/test-plan.md:56` | No HTTP 202 remains in a cancel context. Every hit is a negation (G4a). |
| R2-07 | PARTIAL (R3-03, major) | `docs/agents/handoffs/P4-00.md:35-78` | The table IDs now match round 1, and the round-1 subjects line up except at R-19. New false or unverifiable claims are listed in R3-03. |
| R2-08 | RESOLVED | `docs/phase4-plan.md:86`, `docs/agents/handoffs/P4-00.md:89` | Per D-R2-08, `TestReviewP400_SEC022` stays allowlisted. The handoff names the three entries that go stale. |
| R2-09 | RESOLVED | `docs/adr/0016-cancellation-semantics.md:46`, `docs/adr/0016-cancellation-semantics.md:141`, `docs/state-machine.md:40`, `docs/state-machine.md:52`, `docs/phase4-plan.md:48` | Per D-R2-09, the no-op lives in the repository under the row lock, and the domain matrix is unchanged. |
| R2-10 | PARTIAL (R3-08) | `docs/state-machine.md:163-175`, `docs/schema.md:391-395`, `docs/phase4-plan.md:122-125` | The dot notation is gone, and the uppercase spelling matches `internal/domain/events.go:9-15`. Open: the terminal-job sweep event is `LEASE_EXPIRED` in `docs/adr/0014-lease-model-and-expiry.md:144` and `docs/schema.md:391`, but `DIAGNOSTIC_LATE_RESULT` in `docs/phase4-plan.md:122`. Lowercase `cancelled` and `cancel_requested` still appear at `docs/state-machine.md:46-48`, describing today's code, which writes `JOB_CANCELLED` and `JOB_CANCEL_REQUESTED` (G-R2-10). |
| R2-11 | PARTIAL (R3-05) | `docs/phase4-plan.md:57` (`internal/api/api_test.go:27` is correct), `docs/state-machine.md:44`, `docs/phase4-plan.md:32` (ranges correct), `docs/phase4-plan.md:37` (label correct) | Old truncated ranges remain in three places, and new wrong citations were added (R3-05). |

**R2-04 was partially invalid (D-R2-04).** I verified the lines the owner named, using `sed -n` and
`git grep` (G4b, G-R2-04-history). `docs/schema.md:205`, `docs/schema.md:346`,
`docs/phase4-plan.md:74` and `docs/protocol.md:102` use `COMPLETED` as an attempt state. These uses
are correct: they match `chk_attempts_state` at
`internal/storage/postgres/migrations/000001_initial_schema.sql:97` and `AttemptCompleted` at
`internal/domain/state.go:24`. `JOB_COMPLETED` is the event name `internal/domain/events.go:12`.

The four locations my round-2 text named (`docs/schema.md:386`, `docs/phase4-plan.md:114`,
`docs/protocol.md:148`, `docs/protocol.md:468` at `c719880`) were job-state uses (G-R2-04-history),
and they are now fixed. The invalid part of R2-04 is this: per D-R2-04, the job terminal states in
these lists are `SUCCEEDED`, `FAILED` and `CANCELLED`, so my demand to add `REJECTED` and to define
"terminal" as `IsTerminal()` is withdrawn. No reading of R2-04 should extend to the attempt state
`COMPLETED`, which stays.

## Round-1 findings that were PARTIAL in round 2 (16)

| ID | Status now | Evidence | What remains |
|---|---|---|---|
| R-02 | RESOLVED | `docs/adr/0014-lease-model-and-expiry.md:140-144`, `docs/schema.md:391`, `docs/phase4-plan.md:122`, `docs/adr/0016-cancellation-semantics.md:136`; includes `SUCCEEDED` jobs | The event-name conflict is tracked in R3-08. |
| R-03 | PARTIAL (minor) | `docs/roadmap.md:65` and `docs/PROJECT_STATE.md:199` are fixed (G5-r03) | `CHANGELOG.md:24` still says "3-tier failure classification taxonomy". |
| R-04 | PARTIAL (R3-02, major) | `docs/protocol.md:468-476` has the 403/404 rows and the diagnostic column. `docs/state-machine.md:183` and `docs/failure-model.md:24` now say `LEASE_EXPIRED`. | The terminal-commit rule contradicts FS-06 and is not restricted to `RUNNING` attempts (R3-02). |
| R-05 | RESOLVED | `docs/adr/0016-cancellation-semantics.md:54-57` (no `queue.Ack`), `docs/adr/0015-retry-policy-and-failure-classification.md:125-126` | The residual is under R2-01 / R3-09. |
| R-06 | RESOLVED | `docs/phase4-plan.md:45-48`, `docs/phase4-plan.md:96`, `docs/state-machine.md:15`, `docs/state-machine.md:33`; the test matrix is at `internal/domain/transition_test.go:34-38` (G5-matrix) | |
| R-07 | RESOLVED | `docs/phase4-plan.md:116`, `docs/security/test-plan.md:59` (G-misc) | The new gap is the missing `CANCEL_REQUESTED` exception in the accept-until-swept text (R3-07). |
| R-08 | RESOLVED | `docs/adr/0014-lease-model-and-expiry.md:121-137`, `docs/schema.md:366-388`, `docs/phase4-plan.md:120` | |
| R-09 | PARTIAL (R3-01, major) | | The trigger was narrowed against the owner rule. |
| R-10 | PARTIAL (R3-08) | The 404 codes now match: `docs/adr/0014-lease-model-and-expiry.md:97`, `docs/protocol.md:416`. The result path 400/404 is at `docs/failure-model.md:63-65`. | The renewal 400 codes still differ. `docs/failure-model.md:69` and `docs/phase4-plan.md:169` include `ATTEMPT_NOT_RUNNING`. `docs/adr/0014-lease-model-and-expiry.md:98` and `docs/protocol.md:418` do not, and `docs/protocol.md:478` calls it deprecated. |
| R-11 | RESOLVED | `docs/phase4-plan.md:178`, `docs/phase4-plan.md:196-199` | |
| R-12 | RESOLVED | G3e-blueprint, G3e-blueprint-detail; see "Blueprint" below | |
| R-13 | RESOLVED | `docs/state-machine.md:81`, `docs/state-machine.md:153`, `docs/adr/0015-retry-policy-and-failure-classification.md:144`, `docs/schema.md:349` | The writer of the `CANCELLED` attempt is tracked in R3-06. |
| R-16 | RESOLVED | `docs/adr/0014-lease-model-and-expiry.md:99`, `docs/protocol.md:419`, `docs/phase4-plan.md:170`, `docs/failure-model.md:70` agree: retry on the next interval until the lease expires | |
| R-17 | RESOLVED | `docs/schema.md:339-342`, `docs/adr/0014-lease-model-and-expiry.md:229`, `docs/phase4-plan.md:113`; `job_events.details` is `JSONB` (`internal/storage/postgres/migrations/000001_initial_schema.sql:159`), so the expression index is valid | |
| R-21 | RESOLVED | The only `$\...` line added versus `origin/main` is the handoff's own quotation at `docs/agents/handoffs/P4-00.md:59` (G-R21) | |
| R-23 | PARTIAL (minor) | `docs/security/threat-model.md:128` is labelled | `docs/phase4-plan.md:162` and `docs/PROJECT_STATE.md:193` still call the state short-circuit and the indexed lookup "in-place defenses". Those belong to an endpoint that does not exist yet. |

**Handoff resolution table check (task item 2).** The table at `docs/agents/handoffs/P4-00.md:37-62`
has one row per round-1 ID, R-01 to R-24, in order, and no ID is missing or shifted. It has no title
column. I compared each row's subject with the round-1 headings (`docs/agents/reviews/P4-00.md:57`
to `:426`, G-R21). Rows R-01 to R-18 and R-20 to R-24 describe the right finding.
- R-19 does not. It reads "Verified retry backoff jitter formulas", but round-1 R-19 is "The LOST
  budget consequence uses the wrong time formula" (the fix at
  `docs/adr/0015-retry-policy-and-failure-classification.md:84` is correct).
- The status column is a copy of the round-2 statuses. It matches my current judgement for 13
  rows: R-01, R-03, R-04, R-09, R-10, R-14, R-15, R-18 to R-20, and R-22 to R-24 (R-04 stays
  PARTIAL, for a new reason: R3-02). It does not match for the 11 rows I now judge RESOLVED: R-02,
  R-05 to R-08, R-11 to R-13, R-16, R-17 and R-21.
- The column also contradicts `docs/agents/handoffs/P4-00.md:191`, which says every partial
  finding is resolved (R3-03).

## New findings

### R3-01 (major) The outage-grace trigger is narrowed to connection-class errors, against accepted rule R-09; the five documents disagree on the wording
- Location:
  - `docs/adr/0014-lease-model-and-expiry.md:176`
  - `docs/schema.md:400`
  - `docs/protocol.md:410`
  - `docs/failure-model.md:10`
  - `docs/failure-model.md:28`
  - `docs/phase4-plan.md:29`
  - `docs/phase4-plan.md:174`
  - `docs/agents/handoffs/P4-00.md:13`, `:23`, `:25`, `:47`, `:72`, `:213`
- Evidence label: **executed** grep (G3a-grace) and **confirmed by code reading** of the cited lines.
- Evidence:
  - The owner rule R-09: ANY failed sweeper tick, or a database-time gap over 3x the interval,
    opens the grace window at the next successful tick. The cooldown starts when a window ends,
    and no new window opens during it.
  - The owner overruled my round-2 suggestion to count only connection-class errors. Counting too
    little (excluding deadline or timeout errors) can skip grace after a real partition or an
    overloaded database. The sweeper then reaps live attempts, and work runs twice. Counting too
    much only delays reaping, and the cooldown bounds the delay to one window.
  - The documents state five different triggers:
    - ADR-0014:176 limits a failed tick to "database connectivity errors (`net.OpError`, connection
      refused, connection reset, pool closed)". It says "Statement timeouts, lock timeouts, or slow
      query execution deadlines do NOT trigger grace", citing
      `TestPostgresRepository_Sweeper_SlowTick_DoesNotEnterGrace` (planned test). ADR-0014 never
      uses the phrase "connection-class", although the handoff (`:13`, `:72`) says it does.
    - schema.md:400 and protocol.md:410: "Outage detection counts connection-class errors, not slow
      ticks or query timeouts."
    - failure-model.md:10: "Failed sweeper ticks count connection-class errors only (not slow
      queries)". failure-model.md:28: "Sweeper counts connection-class errors only."
    - phase4-plan.md:29: "(connection-class errors only; not slow queries or health checks)".
    - phase4-plan.md:174: "any failed tick (connection-class errors only)". This contradicts itself.
  - The handoff calls the wording "Written identically across all 5 docs" (`:72`). That is false.
  - The cooldown length is given only in `docs/phase4-plan.md:174` ("30s cooldown") and in
    `CHANGELOG.md:19` / `docs/PROJECT_STATE.md:19` ("capped by a 30s cooldown"). ADR-0014:181,
    schema.md:400, protocol.md:410 and failure-model.md:10 define when the cooldown starts but not
    how long it lasts.
  - The planned tests contradict each other.
    `TestPostgresRepository_Sweeper_SlowTick_DoesNotEnterGrace` (planned test, ADR-0014:176 and
    :197) asserts that a timed-out tick does not enter grace.
    `TestCoordinator_LeaseSweeper_SlowTickOutageGrace` (planned test,
    `docs/phase4-plan.md:191`; described at `docs/agents/handoffs/P4-00.md:213`) asserts that slow
    ticks do enter grace.
- Suggested fix:
  - Put one identical sentence in ADR-0014 section 4, schema.md, protocol.md 6.1, failure-model.md
    (line 10 and FS-08) and phase4-plan.md (lines 29 and 174). For example: "Outage detection (R-09):
    any failed sweeper tick, whatever the error, or a database-time gap of more than 3x
    `MINICLOUD_SWEEPER_INTERVAL` between the end of one successful tick and the start of the next,
    opens the outage grace window (`MINICLOUD_OUTAGE_GRACE_WINDOW`, 30 s) at the next successful
    tick. A cooldown of one window length starts when a grace window ends, and no new window opens
    during it, so reaping is never suppressed for more than one window in a row."
  - Delete the connection-class lists and the "do NOT trigger" sentence.
  - Rename `TestPostgresRepository_Sweeper_SlowTick_DoesNotEnterGrace` (planned test) to a test that
    asserts a failed (timed-out) tick does enter grace, and that the cooldown bounds it.
  - Correct the handoff lines listed above.
  - An owner note on this rule follows under "Informational notes" (per-candidate lock timeouts).

### R3-02 (major) A commit on a terminal job has two contradictory contracts, and "close the attempt" can overwrite closed attempts
- Location:
  - `docs/failure-model.md:26` (FS-06) and `docs/fault-injection-plan.md:71` (FS-06 sub-case B),
    which say "if committed, worker receives 200 COMMITTED on retry" and "returning idempotent HTTP
    200 `COMMITTED`".
  - Against these: `docs/protocol.md:470`, `docs/protocol.md:476`,
    `docs/adr/0014-lease-model-and-expiry.md:225`, `docs/phase4-plan.md:128`,
    `docs/security/findings.md:383`.
- Evidence label: **executed** grep (G9-fs06-origin, G9-attempt-close) and **confirmed by code
  reading** (`internal/storage/inmemory.go:578-579`).
- Evidence:
  1. Both 200 claims were added on this branch: they are absent on `origin/main` and present at
     `HEAD` (G9-fs06-origin). Under the 409 order, a retried commit after a successful one (the
     response was lost) hits priority 1 and gets 409 `JOB_ALREADY_TERMINAL`. That is also what the
     code does today: `internal/storage/inmemory.go:578-579` returns `ErrJobAlreadyTerminal` before
     any attempt check. The gating test `TestFault_CoordinatorCrash_MidResultCommit` (planned test,
     `docs/phase4-plan.md:245`) would therefore be written to two contracts.
  2. Protocol 6.3 says a commit on a terminal job "closes the active attempt in the same transaction:
     sets attempt `state = 'CANCELLED'` if `jobs.state == 'CANCELLED'`, or `state = 'LOST'`
     otherwise". findings.md:383 and plan:128 say the same without any condition. Nothing restricts
     this to an attempt that is still `RUNNING`, and priority 1 runs before the attempt and token
     checks. Three cases follow:
     - (a) A retried commit of the winning attempt on a `SUCCEEDED` job turns its `COMPLETED`
       attempt into `LOST`.
     - (b) Cancel a `RETRYING` job, then let the old worker of the `LOST` attempt commit. Its `LOST`
       attempt turns into `CANCELLED`.
     - (c) A sweeper-closed `CANCELLED` attempt gets rewritten, with a token that was never checked
       (SEC-004 caller).
     Worker slots are derived from `RUNNING` attempts
     (`internal/storage/postgres/migrations/000002_schema_hardening.sql:22`,
     `internal/storage/inmemory.go:68`, G9-slots), so no slot is double-counted. The harm is
     rewritten attempt history.
- Suggested fix:
  - Make FS-06 match the 409 order. Recommended: a retried commit after success gets 409
    `JOB_ALREADY_TERMINAL`, the worker halts, and the single result row stands. Today's code does
    this, and the worker loop already treats 409 as final. If the owner wants an idempotent 200
    instead, it must be added as a priority-0 row in all three 409 tables.
  - In protocol 6.3, ADR-0014, findings.md SEC-022 and plan 4b, write "closes the attempt only if it
    is still `RUNNING` (and it is the job's current attempt); otherwise no attempt is modified".
  - Add a planned conformance test for a retried commit on a `SUCCEEDED` job, asserting the attempt
    stays `COMPLETED`.

### R3-03 (major) The handoff still carries unverifiable and false claims
- Location: `docs/agents/handoffs/P4-00.md` lines 5, 13, 72, 70, 183, 191, 224, 50, 47, and the
  fix-location columns.
- Evidence label: **executed** (G-scope, G9-misc2, G6-handoff-doccites, G3a-grace) and **confirmed
  by code reading**.
- Evidence:
  - Line 183: "Database tests (`make test-db`) were run during preflight verification and gate
    checking. Zero test failures were observed." No command output is pasted. Line 224 says, under
    "Not tested": "Live PostgreSQL database test execution (`make test-db`) ...". The claim is new
    this round (absent at `c719880`, G9-misc2). Under AGENTS.md section 3 rule 5 and section 10, it
    is a test-result claim without output, and it counts as not done.
  - Line 191: "All round-2 audit findings (R2-01..R2-11) and partial round-1 findings have been
    resolved". The handoff's own table (lines 40-61) still marks 16 round-1 rows PARTIAL, and this
    review finds 5 round-2 rows partial.
  - Line 72: "Written identically across all 5 docs". False (R3-01).
  - Line 13 says ADR-0014 lists the 400 codes "`INVALID_REQUEST`, `ATTEMPT_NOT_RUNNING`,
    `WORKER_TASK_UNSUPPORTED`". `docs/adr/0014-lease-model-and-expiry.md:98` lists only
    `INVALID_REQUEST` and `WORKER_TASK_UNSUPPORTED`.
  - Line 5: "Last commit: `18e5fd5`". The branch has two later commits; the tip is `623837e`
    (G-scope).
  - Wrong fix locations (G6-handoff-doccites):
    - R2-05 cites `docs/schema.md:368`, which is a "```sql" fence.
    - R2-05 cites `docs/adr/0014-lease-model-and-expiry.md:130`, which is the attempts lock, not
      the grace check (`:136`).
    - R2-03 cites `docs/protocol.md:136-144`, which is the duplicate-cancel body. protocol.md never
      states the commit outcome on `CANCEL_REQUESTED`.
    - R-07 cites `docs/adr/0014-lease-model-and-expiry.md:94` (the cancellation channel) for the
      accept-until-swept rule, which is at `:85` and `:213-217`.
  - Line 70 repeats the wrong code citations of R3-05.
  - The R-19 row describes a different subject, and the status column is a stale round-2 copy
    (see the table check above).
- Suggested fix:
  - Delete line 183, or replace it with a real `make test-db` run and its pasted output.
  - Change line 191 to the true status.
  - Correct lines 5, 13 and 72 and the fix locations.
  - Rewrite the R-19 row.
  - Give the round-1 table a current status column, or say plainly that it shows round-2 statuses.

### R3-04 (minor) The executed audit probe is labelled "(planned test)" where it is cited as evidence
- Location: `CHANGELOG.md:21`, `docs/PROJECT_STATE.md:21` ("demonstrated by audit probe
  `TestReviewP400_SEC022_DoubleCancelLeaksWorkerSlot` (planned test)"),
  `docs/agents/handoffs/P4-00.md:9`, `:196` ("Proven false by audit probe ... (planned test)"),
  `:204` (listed under "Attacks designed with planned-test names").
- Evidence label: **executed** grep (G3b-probe-labels).
- Evidence: I checked every use of both probe names across docs/ and CHANGELOG.md.
  - Correct uses:
    - Executed probe: `docs/agents/handoffs/P4-00.md:227`, `:233`; the verbatim evidence at
      `docs/security/findings.md:314-356`.
    - Planned regression in 4a: `docs/phase4-plan.md:36`, `:100`; `docs/security/findings.md:34`,
      `:384`, `:385`; `docs/security/threat-model.md:117`, all phrased as "audit probe ... planned
      for slice 4a" or "to be cherry-picked".
    - Allowlist context: `docs/agents/handoffs/P4-00.md:89`. The only use of
      `TestReviewP400_OldCoordinator_RenewRouteResponse` is here, and it is correct.
  - Wrong uses: the five places listed under Location. They cite the probe as having demonstrated
    or proven the leak, so the label must say it was executed (E1 at
    `docs/agents/reviews/P4-00-evidence.txt`).
- Suggested fix: label these "(audit probe, executed on `agent/claude/P4-00-review`; planned
  regression test in slice 4a)". Move line 204 out of the "planned-test names" list.

### R3-05 (minor) Wrong or stale code citations
- Evidence label: **executed** `sed -n` / `awk` (G3c-numbered, G6-linecites).
- `internal/storage/postgres/repository.go:1037-1044` is cited for the success branch at
  `docs/adr/0016-cancellation-semantics.md:119`, `docs/state-machine.md:54`, `docs/phase4-plan.md:217`
  and the handoff (lines 15 and 70). Those lines are the end of the worker-mismatch check and the
  `var (` block. The success branch is at `internal/storage/postgres/repository.go:1046-1049`.
- `internal/domain/transition.go:22` is cited (same places) as permitting `CANCEL_REQUESTED -> SUCCEEDED`.
  Line 22 is `StateFailed: true`. The permission is at `internal/domain/transition.go:21`.
- `internal/domain/transition.go:6-36` (5 places, for example `docs/state-machine.md:40` and
  `docs/phase4-plan.md:48`): the table spans `internal/domain/transition.go:4-33`. Line 6 is inside
  `StateSubmitted`, and lines 34-36 are the next function's comment.
- `internal/coordinator/service_test.go:353-375` is cited for `spyQueue`, which spans
  `internal/coordinator/service_test.go:338-381`.
- The old truncated ranges `internal/storage/inmemory.go:658-683` and
  `internal/storage/postgres/repository.go:1135-1215` remain at
  `docs/adr/0016-cancellation-semantics.md:20`, `CHANGELOG.md:21` and `docs/PROJECT_STATE.md:21`.
  Line 683 is blank, and line 1215 is mid-function. The correct ranges are
  `internal/storage/inmemory.go:658-700` and `internal/storage/postgres/repository.go:1135-1233`.
- `docs/adr/0015-retry-policy-and-failure-classification.md:133` names `ErrNoJobsAvailable`. No
  such identifier exists in the code; dispatch returns `domain.ErrNoQueuedJobs`
  (`internal/storage/inmemory.go:494`).
- `docs/phase4-plan.md:136` still labels Invariant 4 "`worker_capacity` accounting".
  `docs/phase4-plan.md:140` maps a `CommitAttempt` mutation to a sweeper test,
  `TestPostgresRepository_Sweeper_TerminalJobClosesAttempt` (planned test).
- `internal/storage/inmemory.go:610-622` is acceptable: the branch is at 615-622.
- Suggested fix: correct each one.

### R3-06 (minor) The attempt end state after a cooperative cancel conflicts between documents; protocol.md is silent
- Location: `docs/adr/0016-cancellation-semantics.md:121`, `docs/state-machine.md:54` (attempt
  `FAILED`), against `docs/state-machine.md:82`, `docs/state-machine.md:143-146`,
  `docs/state-machine.md:154`, `docs/state-machine.md:172` and `docs/schema.md:350`.
- Evidence label: **confirmed by code reading**.
- Evidence:
  - Per D-R2-03, a commit on `CANCEL_REQUESTED` closes the attempt "with the reported outcome", so
    a cancelled task gives attempt `FAILED`. But the mapping table at state-machine.md:82 names
    "Worker acknowledges cancel" as a writer of attempt `CANCELLED`. The diagram (143-146) draws
    "Task Cancelled / Worker Reports Exit -> (CANCELLED)". state-machine.md:172 has `resolved_by:
    worker_ack`, and schema.md:350 says "Written by worker cancel acknowledgment". No acknowledgement
    signal exists any more.
  - protocol.md has no sentence on commit outcomes for `CANCEL_REQUESTED`. The handoff cites
    `docs/protocol.md:136-144` for it.
  - Today a failure commit on `CANCEL_REQUESTED` ends the job `FAILED`
    (`internal/storage/inmemory.go:619-622`). The design changes this to `CANCELLED`. That is a
    behavior change, but it is not listed among ADR-0016's "three concrete behavioral changes"
    (`docs/adr/0016-cancellation-semantics.md:43-50`) nor in 4b's `CommitAttempt` scope
    (`docs/phase4-plan.md:127-130`), and no planned conformance test pins it. No existing test
    asserts the old outcome (G9-cancelreq-tests, G9-cancelreq-conformance), so nothing breaks.
- Suggested fix:
  - Make the writers of attempt `CANCELLED` "sweeper, and the commit on a terminal `CANCELLED`
    job".
  - Fix the diagram, `resolved_by` and schema.md:350.
  - Add the commit-outcome rule to protocol 6.3.
  - List the `FAILED -> CANCELLED` change in ADR-0016 section 1, with a planned conformance test.

### R3-07 (minor) The accept-until-swept text says renewal always extends; cancel bounds and idempotency claims are overstated
- Location:
  - `docs/adr/0014-lease-model-and-expiry.md:85`, `docs/adr/0014-lease-model-and-expiry.md:216`,
    `docs/protocol.md:408`, `docs/state-machine.md:150`, `docs/failure-model.md:11`,
    `docs/failure-model.md:60`
  - `docs/adr/0016-cancellation-semantics.md:155`, `docs/adr/0016-cancellation-semantics.md:157`
- Evidence label: **inferred** from the documented protocol.
- Evidence:
  - Take a renewal that arrives after expiry but before the sweep, on a `CANCEL_REQUESTED` job
    (attack 3 below). ADR-0014:85 says it is "accepted and extended from current database time",
    while ADR-0014:94 says a renewal on `CANCEL_REQUESTED` is not extended. No text says which rule
    wins.
  - failure-model.md:60 says renewal during result delivery ensures "the attempt is never reaped
    while the worker is actively delivering". After `cancel_requested` the worker stops renewing,
    so that is false for a cancelled job. The outcome is still `CANCELLED`, which is acceptable.
  - ADR-0016:155 bounds resolution by "one lease duration plus sweeper cadence". A startup or outage
    grace window can add up to 30 s more (attack 7).
  - ADR-0016:157 says clients retrying a cancel "receive safe HTTP 200 responses". A retried cancel
    on a job that was `QUEUED` or `RETRYING` gets 409 `JOB_ALREADY_TERMINAL`, as ADR-0016:145 itself
    says.
- Suggested fix:
  - Add "except on a `CANCEL_REQUESTED` job, where the renewal returns the current expiry unchanged"
    to every accept-until-swept statement.
  - Restate the bound as lease + one grace window + one tick.
  - Limit the 200 claim to duplicates on `CANCEL_REQUESTED`.

### R3-08 (minor) Remaining cross-document mismatches
- Evidence label: **executed** grep (G9-misc2, G-R2-10, G5-r03, G-r1-leftovers) and **confirmed by
  code reading**.
- The renewal response `status` is `"ACK"` at `docs/phase4-plan.md:158-159`, but `"RENEWED"` at
  `docs/adr/0014-lease-model-and-expiry.md:89`, `docs/adr/0016-cancellation-semantics.md:82` and
  `docs/protocol.md:371`.
- Renewal 400 codes (R-10): `ATTEMPT_NOT_RUNNING` is at `docs/failure-model.md:69` and
  `docs/phase4-plan.md:169`, but absent at `docs/adr/0014-lease-model-and-expiry.md:98` and
  `docs/protocol.md:418`. `docs/protocol.md:478` calls it deprecated.
- The renewal 409 list at `docs/phase4-plan.md:167` omits `STALE_ATTEMPT_REJECTED`, which
  `docs/protocol.md:414` includes.
- The terminal-job sweep event is `LEASE_EXPIRED` at `docs/adr/0014-lease-model-and-expiry.md:144`
  and `docs/schema.md:391`, but `DIAGNOSTIC_LATE_RESULT` at `docs/phase4-plan.md:122` (R2-10).
  Lowercase event names remain at `docs/state-machine.md:46-48` and `docs/fault-injection-plan.md:32`.
- `CHANGELOG.md:24` "3-tier" (R-03); `docs/phase4-plan.md:162` and `docs/PROJECT_STATE.md:193`
  "in-place defenses" (R-23).
- Suggested fix: align each to ADR-0014 and protocol.md, and use the code's uppercase event
  spelling.

### R3-09 (minor) Residual of R2-01: the slice 4a impact list is accurate on breakage, but imprecise and silent on the constructor
- Location: `docs/phase4-plan.md:54-66`, `docs/adr/0015-retry-policy-and-failure-classification.md:126`.
- Evidence label: **executed** (`go test -list`, grep: G3d-spyqueue, G3d-callers, G3d-integration,
  G3d-restart) plus **confirmed by code reading** (`internal/coordinator/service.go (lines 352-464 at the audited commit)`,
  `internal/storage/inmemory.go:467-495`).
- Evidence:
  - The count and names are right: all seven tests construct `NewServiceWithQueue`
    (`internal/coordinator/service_test.go:393`, `:466`, `:532`, `:582`, `:678`, `:728`, `:776`).
    But only six use `spyQueue`. `TestService_Claim_Redelivery_NoDuplicateAttempt` uses a plain
    queue plus `q.Enqueue` and asserts only `ErrNoQueuedJobs` and one attempt. After the rewiring it
    would still pass but test nothing. My own round-2 text ("seven tests ... with a spy queue") had
    the same imprecision.
  - The plan does not say what happens to:
    - the `NewServiceWithQueue` constructor and its queue parameter, which is still passed by
      `tests/integration/multi_worker_batch_test.go:39-40`;
    - `SubmitJob`'s `Enqueue` (`internal/coordinator/service.go:194-195`);
    - `CommitResult`'s `Ack` (`internal/coordinator/service.go (lines 512-513 at the audited commit)`).
  - Other memory-mode callers are not listed: `internal/coordinator/service_test.go:47` and
    `:274`, `internal/api/api_test.go:58` and `:588`,
    `tests/integration/coordinator_restart_test.go:67` and `:181`,
    `tests/integration/worker_liveness_test.go:43`. None of them asserts on queue calls. The memory
    `DispatchNextJob` keeps FIFO `jobOrder`, checks health and capacity, and returns the same errors
    as the queue path (`internal/storage/inmemory.go:471-495` against
    `internal/coordinator/service.go:386-405`). So I expect no break (**inferred**, not executed;
    the change does not exist yet). I found no missing breaking test.
- Suggested fix:
  - Say "six assert `spyQueue` Ack/Release; the redelivery test loses its meaning and is rewritten
    or retired (candidate for owner approval)".
  - State the fate of the constructor, `Enqueue` and the `CommitResult` `Ack`.
  - List the other callers as "unaffected by assertion; run in 4a".

## Informational notes for the owner (not findings)
1. **A success commit on `CANCEL_REQUESTED` ends the job `SUCCEEDED`** (task item 3c, G3c-numbered,
   G3c-docs).
   - The code does this today: `internal/storage/inmemory.go:615-618` and
     `internal/storage/postgres/repository.go:1046-1049` set `AttemptCompleted`, `StateSucceeded`
     and `JobEventCompleted` with no special case for `CANCEL_REQUESTED`, and
     `internal/domain/transition.go:21` permits the transition. The cited 1037-1044 and :22 are off
     (R3-05).
   - ADR-0016:114-119, state-machine.md:54, failure-model.md:31 and phase4-plan.md:217 all say
     `SUCCEEDED`. protocol.md and ADR-0014 do not address it, so no two documents disagree. The
     consequence: a cancel can be "lost" to a task that finishes within one renewal interval.
   - `CANCEL_REQUESTED -> FAILED` stays allowed by the FSM (`internal/domain/transition.go:22`), but
     no Phase 4 path produces it any more.
2. **What counts as a failed tick (for R3-01).** Under "any failed tick", a `lock_timeout` on a
   single candidate (`docs/schema.md:381`) would open a grace window unless the sentence defines a
   tick failure as an error of the tick as a whole. Either reading is safe under the cooldown. The
   owner should choose one in the R3-01 sentence.
3. Startup grace is measured on process time (`docs/adr/0014-lease-model-and-expiry.md:172`), while
   outage grace and leases use database time. This is consistent, and it answers the clock question
   in my round-2 R2-05 item 4.
4. `docs/protocol.md:148` lists the job terminal states without `REJECTED`, while
   `docs/adr/0016-cancellation-semantics.md:70` includes it. Per D-R2-04 this is acceptable. A
   cancel on a `REJECTED` job returns 409 either way (`IsTerminal()`).

## Proofs re-run (task items 4, 5, 7)
- 202 in a cancel context (G4a-202): every hit is a negation ("There is no HTTP 202") or unrelated.
  No hit contradicts HTTP 200. `docs/security/test-plan.md:56` now says HTTP 200.
- `COMPLETED` (G4b-completed). Each non-review hit is either an attempt state, an event name or a
  correct pairing:
  - Attempt state: `docs/PROJECT_STATE.md:172`, `docs/adr/0011-postgres-dispatch-and-recovery.md:252`,
    `docs/adr/0015-retry-policy-and-failure-classification.md:41`,
    `docs/adr/0016-cancellation-semantics.md:115`, `docs/fault-injection-plan.md:32`, `:40`, `:71`,
    `docs/phase4-plan.md:74`, `docs/protocol.md:102`, `docs/schema.md:205`, `:346`,
    `docs/state-machine.md:54`, `:64`, `:66`, `:70`, `:79`, `:91`, `:107`, `:139`, `:151`.
  - Event name `JOB_COMPLETED`: `docs/adr/0016-cancellation-semantics.md:118`,
    `docs/state-machine.md:173`.
  - In the handoff, every hit is an attempt state or an event name.
  - No job-state use remains.
- `git grep -n -E 'tests/integration/sec022|ReturnsAccepted|exit 130|ErrTaskCancelled|four probe' --
  docs CHANGELOG.md ':!docs/agents/reviews'`: empty, exit 1 (G4c-forbidden).
- Cross-document checks (G5-errorclass, G5-timing, G5-matrix):
  - `error_class`: ADR-0015:59, schema.md:314 and phase4-plan.md:112 give the four values.
    protocol.md:461 and phase4-plan.md:211 give the three worker-reportable values. These agree.
    `CHANGELOG.md:24` is under R-03.
  - The 409 order is consistent in ADR-0014, protocol.md and the plan.
  - The lock order and the sweeper SQL are identical in ADR-0014 and schema.md.
  - Lease 30 s, renewal 10 s, sweeper 5 s and batch 50 appear with no other values.
  - `RETRYING -> RUNNING` via dispatch is consistent.
  - Exactly two matrix cells flip. Today `internal/domain/transition_test.go:35` has
    `RETRYING -> QUEUED` true, and `RUNNING` is absent from that row. The `CANCEL_REQUESTED` row
    (`:29-33`) is unchanged, consistent with D-R2-09.
  - The R-02 rule covers `SUCCEEDED` jobs.
  - The grace and cooldown wording is not identical (R3-01).
- Allowlist (G7-allowlist): `tests/docs/citation_allowlist.txt` has 7 original entries (line 8,
  lines 11-16) and 4 probe entries (lines 19-22), unchanged since `c719880`.
  `git diff --name-only aa13a4c..HEAD | grep -v -E '^(docs/|CHANGELOG.md)'` prints only
  `tests/docs/citation_allowlist.txt` (G-scope). The SEC-022 evidence block matches E1:
  `diff <(sed -n 313,359p docs/security/findings.md) <(sed -n 4,51p docs/agents/reviews/P4-00-evidence.txt)`
  differs only by E1's trailing blank line (`47a48`).

## Citations (task item 6)
- `make test-docs` passes (G8). The checker validates line bounds, not content, which is why the
  errors in R3-05 pass it.
- Line citations checked by hand with `sed -n` / `awk`: 30 (G3c-numbered, G6-linecites,
  G6-handoff-doccites, G3d-spyqueue).
  - Correct:
    - `internal/storage/inmemory.go:658-700`, `internal/storage/postgres/repository.go:1135-1233`
    - `internal/api/api_test.go:27`, `internal/api/handlers.go:267`
    - `internal/worker/client.go:324-355`, `internal/worker/client_test.go:16-38`,
      `internal/worker/client_test.go:40-112`
    - `internal/domain/transition.go:27`
    - `docs/agents/reviews/P4-00-evidence.txt:32`, `docs/agents/reviews/P4-00-evidence.txt:34`
    - `docs/fault-injection-plan.md:141`
    - `internal/storage/postgres/migrations/000001_initial_schema.sql:97`
    - the seven `internal/coordinator/service_test.go` test lines
    - `docs/adr/0016-cancellation-semantics.md:87`, `docs/phase4-plan.md:166`,
      `docs/state-machine.md:183`, `docs/phase4-plan.md:86`, `docs/schema.md:340`,
      `docs/adr/0014-lease-model-and-expiry.md:229`, `docs/protocol.md:408`
  - Wrong: the items in R3-05 and the handoff fix locations in R3-03.
- Test names (G6-testnames): every name added since `c719880` exists in `go test -list '.*' ./...`
  or is labelled "(planned test)" in its own clause. The exceptions are the probe names
  (allowlisted; labelling issue in R3-04) and the word "Tests". Existing names verified include the
  seven coordinator tests, `TestJobTransitionMatrix_AllPairs`,
  `TestPostgresRepository_Concurrency_MixedOperationsStress`, `TestIsNonRetryableReportError`,
  `TestAgent_ReportResult_RejectionHandling` and
  `TestSecurity_PartD_Outage_MidDispatchAndMidCommit`.
- Blueprint (G3e-blueprint, G3e-blueprint-detail; PDF read with `pdftotext`, 14 pages, page footers
  checked):
  - Section 5 "Job Lifecycle and Consistency Model" runs from page 5 to page 6. Its lease design is
    on page 6, before the section 6 heading.
  - Section 8 "API, Data Model, and Configuration" is on page 8, with "Initial HTTP API (suggested)"
    as its first subheading.
  - Section 10 "Test Strategy and Failure Injection" runs from page 9 to page 10. "Cancellation
    races with success completion" is on page 10.
  - Section 13 "Main Risks and Mitigations" is on page 11 and is no longer cited for
    `RETRYING -> RUNNING`. `CHANGELOG.md:20` and `docs/PROJECT_STATE.md:20` now cite Section 5.
  - All Blueprint citations in ADR-0014:23, ADR-0015:20, ADR-0015:103 and ADR-0016:32-34 are
    correct.

## Attacks on the new design (task item 9)
1. **A renewal that does not extend the lease on `CANCEL_REQUESTED`.** Defined. The lease lapses at
   most 30 s after the last extending renewal, whatever the worker does, so a buggy worker that
   keeps renewing cannot hold the job. No finding, apart from the wording in R3-07.
2. **A cancel between the last renewal and expiry.** Example: a renewal at t=20 sets the lease to
   t=50, the cancel arrives at t=21, and the renewal at t=30 returns `cancel_requested` with expiry
   t=50. Resolution comes at most at the next tick after t=50, plus any grace. If the t=30 response
   is lost, later renewals are still not extended, and after the sweep the worker gets 409
   `JOB_ALREADY_TERMINAL` (priority 1) and halts. Defined.
3. **A renewal after expiry but before the sweep, on `CANCEL_REQUESTED`.** Ambiguous between
   ADR-0014:85 and ADR-0014:94 (R3-07). The outcome is the same under the more specific rule.
4. **Sweeper against commit on `CANCEL_REQUESTED`.** Both lock `jobs` first. The sweeper's `SKIP
   LOCKED` yields to the commit. If the commit wins: success gives `SUCCEEDED`, failure gives
   `CANCELLED`. If the sweeper wins: the job and attempt become `CANCELLED`, and the later commit
   gets 409. That commit's "close the attempt" step must then be a no-op: R3-02.
5. **Cancel of a `RETRYING` job.** `RETRYING -> CANCELLED` with `not_before` cleared is defined. A
   later commit from the `LOST` attempt's worker hits priority 1 and could rewrite `LOST` to
   `CANCELLED` (R3-02(b)).
6. **A duplicate cancel racing a retry.** Both paths lock the job row.
   - If the cancel goes first, the job is `CANCELLED`, dispatch skips it, and the second cancel
     gets 409.
   - If dispatch goes first, the job is `RUNNING`, the cancel moves it to `CANCEL_REQUESTED`, and
     the second cancel is a 200 no-op.
   Defined. The 200 idempotency claim is overstated (R3-07).
7. **Coordinator restart during `CANCEL_REQUESTED`, with startup grace.** Startup grace (30 s,
   process time) suppresses the sweep, so resolution waits up to the remaining lease plus 30 s plus
   one tick. It is bounded, but longer than ADR-0016:155 states (R3-07). No new window can stack
   beyond the cooldown.
8. **A commit whose reported outcome conflicts with the job state.**
   - Any failure on `CANCEL_REQUESTED` (`PERMANENT`, `TRANSIENT` or `UNKNOWN`, at any
     `attempt_no`) gives `CANCELLED`. Defined.
   - A commit on a terminal job: R3-02.
   - `error_classification` is `LEASE_EXPIRED` from a worker: "not accepted"
     (`docs/protocol.md:461`), but the status code is not stated. If it is 400, the delivery loop
     halts on it (`docs/failure-model.md:63`). This is a small gap; I suggest naming 400
     `INVALID_REQUEST` there (folded into R3-08's fix list).

## Attacks attempted that did NOT find issues
- Slot double-free when a sweeper and a commit both "free the slot": impossible. Slots are derived
  from `RUNNING`/`PENDING` attempt counts, in the `available_slots` expression
  (`internal/storage/postgres/migrations/000002_schema_hardening.sql:22`) and in
  `computeAvailableSlotsLocked` (`internal/storage/inmemory.go:68`) (G9-slots).
- Unbounded `CANCEL_REQUESTED` with a worker that keeps renewing: closed by the no-extension rule.
- Lock order: the renewal, commit, cancel, dispatch and sweeper descriptions all take `jobs` first.
  No `attempts -> jobs` path is documented.
- Duplicate cancel no-op placement: repository-only, under the row lock (D-R2-09). Two concurrent
  first cancels serialise on the job row.
- The partial unique index for `DIAGNOSTIC_LATE_RESULT` is valid against `details JSONB`.
- Blueprint pages and titles: all correct.
- The scope and allowlist are unchanged and clean. SEC-022 matches E1.
- Existing tests broken by the `FAILED -> CANCELLED` change on a `CANCEL_REQUESTED` failure commit:
  none. No test asserts it (G9-cancelreq-tests, G9-cancelreq-conformance).

## Tests added
None. This is a docs-only targeted re-audit. No throwaway worktree was used.
