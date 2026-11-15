# Review P4-06a (round 1)

Commit reviewed: `459f88f2a6f3b107194d5cadec552f56b9f722dd` (tip of `origin/agent/gemini/P4-06a`, base `origin/main` at `c85a58f`), fresh HTTPS clone, no `.env`, no database, no docker.
Auditor: Claude Code (did not build it). All command output is in `docs/agents/reviews/P4-06a-round1-evidence.txt` (real redirection, nothing retyped). No SEC numbers are assigned here; the audited documents were not edited.

## Verdict: ACCEPT WITH FIXES

The statements about the code are correct, the imports are byte-identical and nothing was overwritten. Five small documentation faults must be fixed before merge (section "Wrong items"), and the allowlist change needs an explicit decision (section "Allowlist recommendation").

## Item 1 — Scope: confirmed, with one unauthorised change

* Files changed against `origin/main` outside `docs/`, `CHANGELOG.md` and `reports/`: exactly `AGENTS.md`, `README.md`, `tests/docs/citation_allowlist.txt`, `tests/review/sec022_probe_test.go`. **Confirmed.**
* The sec022 change is one comment line, no code (diff in the evidence file). **Confirmed.**
* The allowlist change adds 2 `path` and 5 `test` entries for names cited only in the P4-T1 review record and in the builder's own handoff. It is not authorised by the task prompt; the builder reported it openly. Analysis below.

### Allowlist: how the checker matches

* `ParseAllowlist` builds three global sets (paths, tests, strings) from `kind|value|reason`. There is no per-document scope.
* Rule A (paths) skips a cited path only when the path, or its resolved form, equals an entry exactly. Rule C (tests) skips a cited identifier only when the identifier (text before any `/`) equals an entry exactly. No prefix or pattern matching.
* Rule F makes an entry fail the build if no scanned document cites its value any more, or if the path or test now exists. Scanned documents are the root markdown files and every markdown file under `docs/` (not `.txt`, not `reports/`).
* **Can the 7 entries mask a citation in a live document? Yes, but only these 7 exact strings, in any scanned document.** Six of them are scratch names that exist nowhere (two probe file paths, three scratch test names, one "missing test" name). The seventh is the bare name of the integration wiring test family: real tests carry that name as a prefix followed by an underscore, so the bare form is not a real test, and a future live document citing the bare prefix (a plausible truncation) would pass unflagged. Today only the P4-T1 review record and the builder's handoff cite them (checked by search, evidence file).
* **Experiments (scratch copy, audited branch untouched):** with the allowlist reverted to `main`, the build fails in both the P4-T1 record and the builder's handoff. With the allowlist reverted and the P4-T1 review and evidence removed, it still fails, in the builder's handoff only (two lines). With the allowlist reverted, the P4-T1 record removed and those two handoff lines neutralised, `make test-docs` passes.

### Recommendation: drop the P4-T1 record from this import

* The record cannot be imported without either the allowlist or a checker change (P4-T2 widened the checker for link targets only). Both touch `tests/docs`, which this task does not own.
* Without them the build passes if the P4-T1 review and its evidence file are not imported and the builder's handoff stops naming the scratch files and tests (experiment above). The record stays available on the branch `agent/gemini/P4-T1-review` and can be imported later, together with an explicit decision on the checker (for example extending the frozen-record scope to Rules A and C).
* If the orchestrator prefers to keep the import, the entries are narrow and each has a reason, so keeping them is acceptable, but the authorisation should be explicit and the bare-prefix entry is the one to refuse.

## Item 2 — Imports: confirmed

All 14 imported files are byte-identical to their source branches (`cmp`, 14 of 14 IDENTICAL, sizes match the handoff). All are additions (`A` in the name-status diff), none overwrites an existing file, no name collides, and no P4-4e file is imported. The builder's claimed proof file in the temporary directory is not available; I re-ran the comparison myself.

## Item 3 — Changed statements against the code

| Statement | Result |
|---|---|
| README `MINICLOUD_DEFAULT_MAX_ATTEMPTS`: name, default 3, range 1..20, applied | **confirmed**: `internal/config/config.go` reads it with default 3 and rejects outside 1..20 at startup; `cmd/coordinator/main.go` passes it to `SetDefaultMaxAttempts`; `internal/coordinator/service.go` enforces `MinMaxAttempts..MaxMaxAttempts`; the submit handler uses it when `max_attempts` is omitted |
| protocol 3.3 `error_class` field and values | **confirmed**: field `error_class` with `omitempty` on `domain.Attempt`; values PERMANENT, TRANSIENT, UNKNOWN, LEASE_EXPIRED; `NormalizeWorkerErrorClass` stores no class on success. Minor: "timed-out attempts" is imprecise, no production path sets state `TIMED_OUT` (a task deadline gives a FAILED attempt with class TRANSIENT) |
| protocol 6.3 unknown attempt id under a real job returns 409 `STALE_ATTEMPT_REJECTED` (priority 3); row 6 only when the job does not exist | **confirmed**: row 3 of the table names "unknown attempt ID"; `TestAPI_RenewLease_SuccessAndStaleRejection` run in memory without a database: PASS, 6 subtests including `unknown_attempt_id_is_stale,_as_for_commit` and `unknown_job` |
| ADR-0015 task deadline is TRANSIENT | **confirmed**: `Classify` maps `ErrTaskTimeout` and `context.DeadlineExceeded` to TRANSIENT; `TestExecutor_Classify` PASS (7 subtests) |
| ADR-0016 cancel wins stores a results row | **confirmed by code reading** in both repositories: `CommitAttempt` stores the result whenever `resolution.Retry` is false (PostgreSQL inserts into `results`, memory sets the job result); `ResolveFailure` never retries from `CANCEL_REQUESTED`. **The cited conformance test does not assert the stored result** (it checks states, events and rejection of replays), so "verified by `TestConformance_CommitAttempt_FailureOnCancelRequestedYieldsCancelledJob`" in the handoff overstates; it ran and passed in memory (PASS, 7 subtests) |
| SEC-029 (Makefile `db-reset-test`, `db-down`) | **confirmed by code reading**: both targets exist and take no confirmation (`db-down` stops the container; `db-reset-test` drops and recreates the shared `minicloud_test`, guarded only by the `_test` suffix check) |
| SEC-030 (`TestSecurity_PartD_LeakScan` guard, PR #26) | **confirmed**: the test calls `requireContainerControl` first, which skips without the opt-in; `TestEveryContainerRestartingTestCallsTheGuard` exists; PR #26 is merge `9fbbbcf` |
| Retired SEC-029 note in the P4-4d handoff | **confirmed**: the old provisional label is replaced by an explicit note pointing at the findings register |
| P4-05 correction: CHANGELOG delivery entry | **confirmed** against `deliverResult` and `ComputeBackoffWithJitter`: base 200 ms, multiplier capped at 16 so the ceiling is 3.2 s, jitter factor 0.5 to 1, the 5 s cap is never reached, budget 60 s |
| P4-05 correction: renewal retry wording | **confirmed** against `renewLoop`: failures back off from 1 s with jitter, capped at the renewal interval |
| P4-05 correction: sec022 comment | **confirmed**: comment only; the guard exists via P4-T1 |

## Item 4 — PROJECT_STATE, TASKS, AGENTS

* PR #28 and merge `c85a58f` are correct (the commit subject names PR #28, it is the `origin/main` tip). P4-05 is marked merged, P4-06a active, a Done row added. **Confirmed.**
* Stale text: the CHANGELOG entry for the second design revision still names `docs/phase4-design` in its heading (a historical log entry, left as is by the builder; PROJECT_STATE was cleaned). Minor.
* **AGENTS report-file rule: complete as to content, inconsistent with practice.** Existing files on this branch are named `P4-05-round4`, `P4-05-round5`, `P4-T2-review` and `P4-05-recheck`, not `<TASK-ID>.md`, because the rule forbids overwriting an existing report; the rule never says how a second or later report of one task is named. `reports/` is also not scanned by the citation checker, which is why it carries line numbers (below). Add one sentence: a follow-up report of the same task adds a suffix (`-review`, `-roundN`, `-recheck`, `-fix`).

## Item 5 — F-4c-b-2 and F-4c-b-3 (advice only)

Original findings: both are Info in the P4-4c-b handoff and were confirmed design-intended by the P4-4c-b review; F-4c-b-2 is labelled "confirmed by code reading and the design docs, not exercised end to end", F-4c-b-3 "inferred".

* **F-4c-b-2: closing without registration is justified.** After `cancel_requested` the renewal does not extend the lease, the worker stops renewing, the sweeper resolves a `CANCEL_REQUESTED` job whose lease passed to `CANCELLED` and never retries it (ADR-0016 race 2, the sweeper code path for that state); a late commit gets 409 `JOB_ALREADY_TERMINAL`, which the worker treats as final. The user asked for cancellation, so the outcome (`CANCELLED`, output of a cancelled task dropped) is the specified one; no invariant is bent.
* **F-4c-b-3: closing is justified.** Invariant 3 (late or stale results are rejected) is enforced under the row lock and is tested; the worker halts on 409. Nothing is lost that the invariant does not require.
* Caveats for the orchestrator: (a) the handoff proposes "closed" but `docs/agents/TASKS.md` still lists both as follow-ups, so one of them must change; (b) once P4-4e is merged, both have executed end-to-end evidence (the 4e suite exercises the cancel-versus-sweeper race and the late result after a sweep), which would upgrade the evidence labels from code reading and inferred. Closing now on the existing evidence is acceptable; waiting for the 4e merge gives stronger support.

## Item 6 — Gates and line numbers

* `make test-docs`: PASS. Unset `go test -v -count=1 ./...` (no race, no database, in the clone): **PASS 204 SKIP 35 FAIL 0**, matches the handoff.
* **New line-number citations: found, wrong.** Added lines in live documents: the PROJECT_STATE SEC-029 entry cites the two Makefile target positions as `Makefile:` plus numbers; the builder's handoff names a README line, a CHANGELOG line and two positions in the sec022 test file (four statements). The builder's report file repeats five. The checker does not catch them: its line-number pattern covers only `.go`, `.md`, `.yml` and `.sql` suffixes, and the positions in the handoff and report were not flagged. Rule: no line numbers in documents.

## Wrong items (fix before merge)

1. PROJECT_STATE SEC-029 entry: remove the Makefile line numbers.
2. Builder's handoff: remove the README, CHANGELOG and sec022-probe line numbers (four statements); same in `reports/gemini/P4-06a.md` (five).
3. Builder's handoff: the ADR-0016 results-row statement is "confirmed by code reading", not "verified by" the conformance test.
4. AGENTS report-file rule: add the suffix convention for follow-up reports.
5. Builder's handoff, audit-weakness list, the item about the 4e round 1 review: it says the review "overclaimed a poison loop that was not substantiated". The later executed P4-4e fix round reproduced exactly such a loop (built-in `echo`, all attempts lost, finding F-4e-21 in the P4-4e handoff, not yet merged). Reword or drop that clause.

Not faults, for the orchestrator: protocol 3.3 "timed-out attempts" wording and the example's `attempt_id` key versus the serialised `id` (pre-existing); TASKS follow-up entries for F-4c-b-2 and F-4c-b-3 once decided.

## Counts

Items checked: 6 task items. Confirmed: scope (4 files), imports (14 of 14), 11 changed statements (all true to the code; one evidence overstatement, one imprecise word). Wrong: 5 (above). Allowlist: 7 entries, exact-match and global, recommendation drop the P4-T1 record. Gates: `make test-docs` PASS; unset test PASS 204 SKIP 35 FAIL 0.
