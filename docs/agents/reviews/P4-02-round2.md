# Review P4-02, round 2 (fix pass)

Commit reviewed: `d441c3e` (`agent/gemini/P4-02`), 12 fix commits on top of `33c31ed`. Audit branch: `agent/claude/P4-02-review-2`.
Auditor worktree: `~/MiniCloud-claude-audit` only. Builder: Gemini. Docs-only task.
Verdict: **APPROVE WITH MINORS** (0 blockers, 0 majors, 4 minors, 1 nit).
All verbatim output is in `docs/agents/reviews/P4-02-round2-evidence.txt`; labels in brackets below match its `#####` headings.

No database test was run. `MINICLOUD_TEST_DATABASE_URL` was unset for every Go command ([g-count]). The builder's own `make test-db` is not re-run; the builder now says it was skipped (`P4-02.md`, "Commands run"), which is honest, and the code is identical to `8183a59` ([code-vs-8183a59]).

I did not trust the builder's chat report. Every statement below comes from the files at `d441c3e`.

## 1. Result

All 17 round-1 findings are applied with the wording I gave, line by line ([diff-state], [diff-docs], [diff-handoffs]). No false factual claim was introduced. What is left are four minors and one nit:
three come from lines I did not enumerate in round 1 (R8-02, R8-04, and part of R8-01), and two are builder lapses (R8-03, R8-05).
The two blockers the owner asked about, R7-01 and R7-02, are closed with the exact replacement text in both files. The builder's chat report was wrong about both, the files are right.

Safe to merge into `main` as one pull request together with slice 4a: **yes**. Code is byte-identical to `8183a59`, gates are green, the allowlist diff is exactly the six entries. None of the findings changes code or a gate; R8-01 to R8-03 are cheap to fix before the merge, and are the ones I would fix there.

## 2. R7-01 to R7-17, each compared with my replacement wording

| ID | Rated | Status | Exact text found at `d441c3e` |
|---|---|---|---|
| R7-01 | blocker | **closed** | `CHANGELOG.md:17` and `docs/PROJECT_STATE.md:16`: "Slice 4a tip (8183a59), unit/integration gate (MINICLOUD_TEST_DATABASE_URL unset): PASS 89, SKIP 28, FAIL 1; the failure was TestCitations (stale line citations), repaired in P4-02. Merged tree after P4-02, same gate: PASS 90, SKIP 28, FAIL 0. Database test gate (make test-db) on the slice 4a tip: PASS 70, SKIP 0, FAIL 0; P4-02 changes no code." Identical to my text, both files. |
| R7-02 | blocker | **closed** | Both files: "Added deterministic seeded model-based property test `TestProperty_StateMachine_ModelConformance`: memory 8 seeds x 400 steps (3,200 steps) and PostgreSQL 3 seeds x 120 steps (360 steps), no invariant violations." Identical. |
| R7-03 | blocker | **partly closed** | Every line I specified is applied verbatim: `PROJECT_STATE.md:182-184`, `:189`, `:192`, `roadmap.md:56`, `:60` (appended sentence), `:63`. Residual: lines I did not list still carry the old split (`PROJECT_STATE.md:193`, `:208`, `:211`, `roadmap.md:60` first half, `:65`) -> R8-02. Also `docs/roadmap.md` is outside the P4-02 owned files (R8-03). |
| R7-04 | major | **closed** | `findings.md:26`, `:104`, `threat-model.md:54`, `:98`: `func SubmitJob (bounds MinMaxAttempts, MaxMaxAttempts, MinTimeoutSeconds, MaxTimeoutSeconds)` and `func CreateJob`. `phase4-plan.md:58`: helpers `setupTestCoordinator`, `setupTestServer`, `setupErrorServer`. `phase4-plan.md:79`, `PROJECT_STATE.md:180`: "CHECK constraint `chk_attempts_state`". All symbols exist ([regress]: 142 references checked, 0 genuine misses). |
| R7-05 | major | **closed** | `findings.md:427`: "capped at 1 per attempt by a NOT EXISTS check under the job row lock in PostgreSQL and a loop check in memory; the partial unique index idx_job_events_unique_diagnostic_late_result is planned for migration 000004 in slice 4b". Identical. The index is planned in `schema.md:340` and `phase4-plan.md:118` ([idx]). |
| R7-06 | major | **closed** | `P4-00.md:55`, `:59`, `:61`: the three replacement cells, identical to mine (R-14 configuration names, R-18 RETRYING-cancel case, R-20 "still carries six line citations"). |
| R7-07 | major | **closed** | `state-machine.md:44`, `:48`, `:52`; `adr/0016:19`, `:24`, `:45`: "Before slice 4a (baseline at f6c7ec8) ...", "which then included", "*Behavior before slice 4a*", "Before slice 4a, a second cancel". `f6c7ec8` is a real commit. Identical. |
| R7-08 | major | **closed** | `threat-model.md:114`: "(SEC-022, Medium, Fixed in slice 4a, executed)"; `:117`: "(executed in slice 4a)". |
| R7-09 | major | **closed** | `phase4-plan.md:235` reuse sentence; 4b mutation proofs `:149` (grace input) and `:150` (jitter and cap, moved from 4d); `:164` new gating test; `:202` 4c startup-grace proof; `:248` 4d "bypass the 4b backoff function". All as specified. |
| R7-10 | major | **closed** | `TASKS.md:62`, `:64`, `:65`: Goal, Owned files (`internal/storage/**`, `internal/domain/**`, `tests/**`, handoffs) and Out of scope identical to mine. |
| R7-11 | minor | **closed** (as listed) | The 9 listed labels changed (`CHANGELOG.md:40` and `findings.md:386` with ", since executed in slice 4a"); `phase4-plan.md:43`, `:52`, `:77` reworded; titles in `phase4-plan.md:114`, `PROJECT_STATE.md:186`, `roadmap.md:58`; `state-machine.md` tense. Residual outside my list: 4 labels in `P4-00.md` (R8-04, my omission). |
| R7-12 | minor | **partly closed** | Milestone, branch, SEC-024 bullet, changelog P4-02 bullet, deferred-items, "job row lock (PostgreSQL) or repository mutex (in-memory)": all identical. Section 8 block: see R8-01. |
| R7-13 | minor | **closed** | Reason text of the backoff entry is exactly my replacement; it is the only changed line ([allow-diff-fix]). Handoff D-P4-02-01 now says "[planned test named without a label in the P4-4a audit record]". |
| R7-14 | minor | **closed** | `P4-01.md:6-9`, `:16`, `:39` name `domain.ErrAttemptNotRunning` again; "Commands run" line carries the auditor-provenance sentence; "What changed" lists the `P4-00-citation-check.txt` deletion (file is absent); "Attacks tried: none by the builder; see docs/agents/reviews/P4-01.md." Section 3 below. |
| R7-15 | minor | **closed** | `adr/0008:52` and `:71` carry the two appended sentences, identical. |
| R7-16 | minor | **partly closed** | RuleB sentence applied; the scan now has a command and I re-ran it (rc=1) ([r714-scan-run]); the hash is `a3590bd`, not the tip (R8-05); the open question was replaced by "None. (... authorized and resolved by owner decision 2)" instead of my sentence, and "Known gaps" is still "None for P4-02" (R8-03). |
| R7-17 | minor | **closed** | `P4-00.md:206`: "R5-01 residual is the subject of P4-02 (see docs/agents/reviews/P4-02.md)."; `:101`: "Residual citation line inaccuracies are the subject of R5-01 and P4-02." Identical. |

Count: closed 14, partly closed 3 (R7-03, R7-12, R7-16), not closed 0, new error introduced 0.

## 3. Regressions

**Names, paths, symbols, ADR filenames** ([regress]): a script over every line added in `33c31ed..HEAD` checked backticked repo paths, `NNNN-*.md` names, `func X`, helper names, the four bound constants, constraint names and SEC ids: 142 references, 4 reported, all explained: `P4-00-citation-check.txt` (the handoff says it is deleted), `tests/review/audit_attacks_test.go` (allowlisted `path|` entry, review branch only), two Go package names in `roadmap.md` that are not paths. No invented ADR filename.

**"(executed in slice 4a)"** ([labelcheck]): 11 occurrences in live docs (the "about 12" in the brief counts the `CHANGELOG.md:40` variant and the three `phase4-plan.md` rewordings): all 11 name a `func Test*` that is defined in the code at HEAD (`TestConformance_CancelJob_IdempotentCancelRequested` x6, `TestConformance_CancelJob_RetryingImmediatelyCancelled`, `TestAPI_CancelJob_SuccessReturns200`, `TestProperty_StateMachine_ModelConformance` x3). No non-existent test is labelled executed.

**"(planned test)"** ([labelcheck]): 126 occurrences outside the frozen reviews; 122 name tests that exist nowhere in the code (the backoff test is allowlisted, rest are unwritten tests); 4 name tests that exist (R8-04, all in `P4-00.md`, a historical record, untouched by the fix commits and missed by my R7-11 list).

**R7-14** (task item 3): the builder restored the symbol name in `P4-01.md` in six places, which is what I asked: it dropped the earlier rewording and says what was dropped. The wording also appears in `P4-02.md:41` inside the scan command, which is fine. The scan convention: `P4-02.md:41` runs `grep -rn ... docs CHANGELOG.md | grep -v "docs/agents/"` and I re-ran it: rc=1, zero matches ([r714-scan-run]); without the exclusion it hits only `docs/agents/handoffs/P4-01.md`, `P4-02.md` and `docs/agents/reviews/*` ([r714-scan-full]), all historical records. I found no doc that says the scan must be empty over a path that includes handoffs or reviews ([r714-symbol]; searched TASKS, plan, state, changelog, handoffs). One imprecision: `grep -v "docs/agents/"` also removes `docs/agents/TASKS.md`, which is a live file, and `P4-00.md` still says "non-running attempt error" in two historical rows (pre-existing, not a regression) (R8-05).

## 4. Allowlist (R7-13)

`git diff origin/agent/claude/P4-4a -- tests/docs/citation_allowlist.txt`: 7 added lines (1 comment, 6 entries), 0 removed ([allow-counts], [allow-diff-4a]). Against `33c31ed` the only change is the reason text of the `TestDomain_ComputeBackoff_FullJitter` entry ([allow-diff-fix]). Nit: the comment line above the block still says everything "lives on agent/gemini/P4-4a-review"; it is not checked by any test; no action required.

## 5. Scope

`git diff --name-only 33c31ed..HEAD` ([scope-names]): 14 files under `docs/` plus `CHANGELOG.md` and `tests/docs/citation_allowlist.txt`. Against the 4a base, the non-docs list is empty ([scope-vs-4a], rc=1). `git diff --stat 8183a59 HEAD` outside docs/changelog/allowlist is empty ([code-vs-8183a59]), so code is identical to `8183a59`. `docs/agents/reviews` is unchanged since `33c31ed` ([frozen-reviews]: empty), so the imported audits and the frozen round2/round3 edits are untouched. `docs/roadmap.md` is inside `docs/` but not in the P4-02 owned list in `TASKS.md:57-60` (R8-03).

## 6. Open items from round 1

Out of scope here, as agreed: the 77 wrong test-location citations and the stale allowlist entry when slice 4b adds the backoff test. The fix commits touch no test-location citation outside the hunks listed in section 2 (confirmed by diff reading, [diff-docs]), so nothing was changed to dodge them. They are **not** recorded in `docs/agents/handoffs/P4-02.md` as open items: "Known gaps / TODOs: None for P4-02." The stale-entry condition is recorded only in the allowlist reason text itself. -> R8-03.

## 7. Gates (DB URL unset)

| Gate | Result |
|---|---|
| gofmt | clean ([g-gofmt]) |
| go vet ./... | rc=0 ([g-vet]) |
| go build ./... | rc=0 ([g-build]) |
| make test-docs | `ok tests/docs 5.753s`, rc=0 ([g-testdocs]) |
| go test -v -race -count=1 ./... | rc=0, `PASS 90 SKIP 28 FAIL 0` ([g-count]); 15 packages ok ([g-pkgs]) |

Matches the expected figure. `make test-db` not run (instruction); the 70/0/0 figure is from the 4a audit.

## 8. Findings

| ID | Severity | Location | Label | Summary |
|---|---|---|---|---|
| R8-01 | minor | `docs/PROJECT_STATE.md` section 8 | executed | "Last Verified Commands" block dated 2026-10-28 shows `make test-db` PASS 70 as if run; the handoff says it was skipped; the per-package race list is the unchanged 2026-10-26 P4-01 output |
| R8-02 | minor | `PROJECT_STATE.md:193`, `:204` (4c exit), `:208`, `:211`; `roadmap.md:60`, `:65` | executed | Split residue in lines I did not list in R7-03: 4b still owes the startup grace mutation proof and the roadmap's 4b bullet owns the 30 s grace periods; 4d still "implements" backoff and jitter distribution |
| R8-03 | minor | `docs/agents/handoffs/P4-02.md` ("Known gaps", "Open questions"); `TASKS.md:57-60` | executed, inferred | Open items not recorded; `roadmap.md` edit rests on "owner decision 2" that I cannot verify and is not in the owned files |
| R8-04 | minor | `docs/agents/handoffs/P4-00.md:219`, `:220`, `:221`, `:241` | executed | "(planned test)" on tests that exist (my round-1 R7-11 list missed them) |
| R8-05 | nit | `P4-02.md` ("Branch / last commit", scan line, "Attacks tried") | executed | Hash is not the tip; exclusion removes live `TASKS.md`; "legacy taxonomy names" check has no command |

### R8-01 (minor) Section 8 of PROJECT_STATE mixes runs and shows an unrun command
- Evidence: [diff-state]: only the date, the `make test-docs` time (0.354s -> 0.596s), the race command line, the count line and the new `make test-db` lines changed; the package timings (integration 8.611s etc.) are the 2026-10-26 output. `P4-02.md` "Commands run" says `make test-db: skipped`. AGENTS.md section 3 rule 5. I asked for the 70/0/0 line in round 1 without saying it must be labelled as carried over; the wording below fixes both.
- Exact replacement, the date line: `- **Date:** 2026-10-28 (go version, gofmt, go vet, make test-docs and the final count line from the P4-02 tree; per-package race timings are the 2026-10-26 P4-01 output; make test-db was not run in P4-02)`.
- Exact replacement, the last two lines of the block (`$ make test-db` and its result): `$ make test-db` then `(not run in P4-02; PASS: 70, SKIP: 0, FAIL: 0 is the slice 4a audit figure at 8183a59, code unchanged)`.
- If the `0.596s` figure is not from a saved output, replace the line `ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	0.596s` by `ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs` (the handoff already does this).

### R8-02 (minor) Slice split residue
Evidence: [backoff], [grace]. `phase4-plan.md` is correct; the following lines contradict it.
- `docs/PROJECT_STATE.md:193`: replace `startup grace window` with `the in-lock grace check driven by the grace-state input`.
- `docs/PROJECT_STATE.md` 4c Exit Criteria (the line ending "worker abort on lease lost."): replace `Mandatory mutation proofs for renewal cancellation delivery and worker abort on lease lost.` with `Mandatory mutation proofs for renewal cancellation delivery, worker abort on lease lost and the startup grace period.`
- `docs/PROJECT_STATE.md:208`: replace `Exponential backoff with full jitter ([0.5, 1.5]), base 1s, cap 30s, default 3 max attempts. ` with `Reuse of the slice 4b pure backoff function (full jitter [0.5, 1.5], base 1s, cap 30s) to set `not_before`; default 3 max attempts. `.
- `docs/PROJECT_STATE.md:211`: replace `failure classification and jitter distribution.` with `failure classification and bypassing the slice 4b backoff function.`
- `docs/roadmap.md:60`: replace `with sweeper re-check under row lock and startup/outage grace periods (30s).` with `with sweeper re-check under row lock (the 30 s startup and outage grace periods are owned by slice 4c).`
- `docs/roadmap.md:65`: replace `and exponential backoff with full jitter.` with `and reuse of the slice 4b backoff function for retry scheduling.`

### R8-03 (minor) Open items and the roadmap ownership
- `docs/agents/handoffs/P4-02.md`, "Known gaps / TODOs": replace `- None for P4-02. Follow-on task P4-4b implements PostgreSQL parity and migration 000004.` with:
  `- Open item, not fixed here: 77 line citations (path:N) to test files in live docs name the wrong test; each was already wrong at origin/main (P4-02 review, section 6).`
  `- Open item: the allowlist entry test|TestDomain_ComputeBackoff_FullJitter becomes stale when slice 4b adds the test; slice 4b must remove it.`
  `- Follow-on task P4-4b implements slice 4b (migration 000004, in-memory and PostgreSQL RenewLease and SweepExpiredLeases, pure backoff function).`
- "Open questions": replace `None. (The docs/roadmap.md scope alignment question from R7-03 was authorized and resolved by owner decision 2).` with `None. docs/roadmap.md is not in the P4-02 owned files in docs/agents/TASKS.md; its edit relies on owner decision 2.`
- `docs/agents/TASKS.md` P4-02 Owned files: append `, `docs/roadmap.md``. If the owner did not authorise the roadmap edit, revert `docs/roadmap.md` to its `33c31ed` content instead; I cannot see decision 2, so this part is **inferred**, not checked.

### R8-04 (minor) Planned labels on existing tests in the P4-00 handoff
`docs/agents/handoffs/P4-00.md:219-221` and `:241` (section "Attacks designed with planned-test names") label `TestConformance_CancelJob_IdempotentCancelRequested`, `TestConformance_CancelJob_RetryingImmediatelyCancelled`, `TestAPI_CancelJob_SuccessReturns200` and `TestProperty_StateMachine_ModelConformance` "(planned test)"; all four exist ([labelcheck]). Historical record, so keep the wording and append: replace ` (planned test):` with ` (planned test, since executed in slice 4a):` on those four lines only. These lines were never changed by the fix commits; I omitted them from R7-11.

### R8-05 (nit) P4-02 handoff accuracy
- "Branch / last commit": `a3590bd` is the commit before the one that carries this text (`d441c3e`); a file cannot name its own commit. Replace `/ `a3590bd` (`docs(agents): apply R7-14 to P4-01 handoff`)` with `/ last commit before this handoff update: `a3590bd``.
- Scan line: append `; the exclusion also drops docs/agents/TASKS.md, which does not contain the symbol (checked with the same grep without the exclusion)`.
- "Attacks tried": replace `Checked for obsolete error strings and legacy taxonomy names across all active docs.` with `Checked for the two obsolete symbol names with the command under "Commands run"; no scripted check of taxonomy names.`

## 9. Attacks attempted that did NOT find issues

- Invented test names, paths, ADR filenames, functions, helpers, constants, constraints or SEC ids in the fix commits: none ([regress], [labelcheck]).
- Test labelled executed that does not exist: none. Planned label on a name that exists in the code outside the P4-00 handoff: none.
- Any edit to code, migrations, the imported audits or the frozen review files: none ([code-vs-8183a59], [frozen-reviews]).
- Allowlist entry added, removed or altered beyond the backoff reason: none ([allow-diff-fix]).
- A fix that contradicts another fixed line inside `phase4-plan.md`, `findings.md`, `state-machine.md`, ADR-0016: none found; the contradictions are in the files listed under R8-02.

## 10. Tests added

None. This review adds no tests and edits nothing of the builder's. Branch `agent/claude/P4-02-review-2`: `docs/agents/reviews/P4-02-round2.md` and `docs/agents/reviews/P4-02-round2-evidence.txt`.
