# Review P4-05 round 2

Commit reviewed: 695de9b (`agent/gemini/P4-05`, round 3; first audit was ddfd4b0, review on `agent/claude/P4-05-review`). Current `origin/main` is 9fbbbcf (slice 4d PR #25 and P4-T1 PR #26); the builder branch already contains a merge of f13d97f. Auditor branch: `agent/claude/P4-05-review-2`. Builder: Gemini. Auditor: Claude Code (Sonnet 5.5).

Verdict: **APPROVED WITH FINDINGS.** No blocker, no major. Seven of the nine earlier findings are closed. F-04 and F-05 are only partly closed, and round 3 itself introduced one false statement (F-R2-02) and one unsupported "design decision" claim (F-R2-01). There are four new minor findings and three informational notes. All are docs-only; none changes behaviour. The scope, imports and gates are clean.

Labels: **executed** (real output in `docs/agents/reviews/P4-05-round2-evidence.txt`, produced by shell redirection of `/tmp/p405b-ev.sh` and `/tmp/p405b-gates.sh`), **confirmed by code reading**, **inferred**, **designed, not executed**. No line numbers are cited; locations are file plus section, function or constant name.

Method: fresh HTTPS clone in `/tmp/audit-p405b`, `git rev-parse --short HEAD` printed 695de9b before anything else, no `.env`. `MINICLOUD_TEST_DATABASE_URL` unset in the shell and removed with `env -u` for every Go run. No server started, `make test-db` and `make test-outage` not run, the Postgres container and the shared test database not touched. The `~/MiniCloud-claude` worktree was clean before the review branch was created from `origin/agent/gemini/P4-05`. The merge gate used a second scratch clone (`/tmp/audit-p405b-merge`, local branch `scratch-merge`, never pushed).

## 1. Status of F-01 to F-09

| ID | Status | Evidence (all in the evidence file) |
|---|---|---|
| F-01 | **closed** | CHANGELOG now reads "Imported review records for P4-4b, P4-4c-a, P4-4c-b, and P4-03 (P0-03 round 2 and 3 records deferred pending citation checker scoping; see TASKS.md follow-ups)". The TASKS goal for P4-05 says the same ("deferred pending citation checker scoping; see follow-ups"). The review directory holds only `P0-03.md` and `P0-03-evidence.txt` (executed ls). |
| F-02 | **closed** | New ADR-0014 text: "`internal/worker/client.go` function `deliverResult`) retries `ReportResult` with capped jittered backoff (200ms base to 5.0s cap) ... 60-second time-bounded delivery budget (`deliverResult` called from `ExecuteAndReport`)". Code constants (executed grep): `deliveryBackoffBase` is 200 ms, `deliveryBackoffMax` is 5 s, `ResultDeliveryBudget` is 60 s; `deliverResult` is called at the end of `ExecuteAndReport` (confirmed by code reading). No line range remains in ADR-0014. See F-R2-05 (informational) for the nominal cap. |
| F-03 | **closed** | The 500 ms example is gone (executed grep: no `500ms` in ADR-0014). New sentence: "In coordinator work dispatch (`internal/coordinator/service.go` method `RequestWork`), a configured lease duration shorter than one second produces `lease_duration_seconds: 0` via integer truncation, causing the worker's `renewalInterval` (`internal/worker/client.go`) to fall back to the 10-second default." It is true: `RequestWork` builds `LeaseDurationSeconds` as `int(s.leaseDuration / time.Second)` (truncation), and `renewalInterval` returns `DefaultRenewalInterval` when the value is zero or less (confirmed by code reading, grep output in the evidence file). It is phrased as an observation, not as a decision; the open question stays in the handoff. Nit: the field has `omitempty`, so on the wire the key is absent rather than 0; the worker result is the same. |
| F-04 | **partly closed** | See section 3. The renewal line no longer claims owner acceptance and the 30 s default regained its original "(Accepted by owner)" tag (the tag is on main's ADR-0014 text, executed git show). Still open: F-R2-01 and F-R2-02. |
| F-05 | **partly closed** | See section 4. Dead-worker, lease-sweeper, retry and open-finding statements are now true; section 8 no longer carries stale counts; the 403/404/409 wording was replaced. Still open or new: F-R2-03 and F-R2-04, plus the false "exact recognised set" claim (F-R2-02). |
| F-06 | **closed** | Executed grep over TASKS, PROJECT_STATE, CHANGELOG and the findings register finds no "next free SEC number" claim; the TASKS reserved row for the register now says numbers proposed in handoffs are provisional and the register is the authority. The only "next free" left in TASKS is the migration number 000005, which is true (the migrations directory ends at 000004). Residue: the P4-05 handoff's round 1 sections still say "next free SEC number SEC-029" (history, corrected by the F-06 row of round 3). See I-R2-2. |
| F-07 | **closed** | SEC-027 and SEC-028 now cite "builder finding F-4c-a-1 / F-4c-a-3 in P4-4c-a handoff, agreed by auditor in review" (confirmed against the 4c-a handoff and review, executed grep). SEC-025 label is "confirmed by code reading" and says the 4b auditor's throwaway probe is not preserved in any branch and was not reproduced. Source severities are named for SEC-025, SEC-026 and SEC-028 with the reclassification stated. The words "set by orchestrator" are not backed by a repository document, see F-R2-02. |
| F-08 | **closed** | TASKS follow-ups F-4c-b-2 and F-4c-b-3 match the 4c-b handoff text (cancel stops renewal; slow delivery up to 60 s can outlive the lease and race the sweeper; reaped as `CANCELLED`, late commit 409; an in-flight result cannot be recalled and is rejected under invariant 3). They match the 4c-b review ("Info, design-intended"). Nit: the TASKS entries omit the "Info" severity and the handoff's evidence labels (F-4c-b-3 is "inferred"). |
| F-09 | **closed** | Executed `grep -F` with the quoted row finds the identical string in TASKS (Active table) and in the handoff. The F-09 row of round 3 quotes it without a line number. |

## 2. Round 3 hunks (git diff fc4b417 695de9b, which excludes the main merge)

The merge of main brought only main's own files (executed `git diff --name-only ddfd4b0 fc4b417`: Go code, tests and `docs/agents/handoffs/P4-4d.md`). Everything else below is the builder's round 3 work, nine files.

| Hunk | Classification | Evidence |
|---|---|---|
| CHANGELOG P4-05 line (P0-03 deferred) | supported | imports and review directory (executed) |
| CHANGELOG 4c-b bullet "exact recognised set: 403 WORKER_MISMATCH, 409 LEASE_EXPIRED and STALE_ATTEMPT_REJECTED, and recognised 404 codes" | **unsupported** (false as "exact") | F-R2-02 |
| CHANGELOG `executeAndReport` renamed `deliverResult` | supported | the 60 s budget is in `deliverResult` |
| PROJECT_STATE section 1 milestone line and slice 4d "Done" bullets | supported (class names, CLI file, PR/merge hash); behaviour details inferred from the 4d handoff, not re-run | executed grep of `domain` constants and `cmd/mc/failure.go` in the merge file list |
| PROJECT_STATE section 4 heading and the SATISFIED markers (4a PR #19 e7403a9, 4b PR #20 7e58d4a, 4c PR #21 and #23, 4d PR #25 f13d97f) | supported for PR numbers and merge hashes (round 1 checked them against the merge commits). The 4d "SATISFIED" rests on the merge only: there is no 4d audit record in the review directory, see I-R2-3 | |
| PROJECT_STATE section 6 item 5 and section 7 dead-worker line | supported | `internal/coordinator/sweeper.go` exists and `SweepExpiredLeases` exists in both repositories (executed grep) |
| PROJECT_STATE section 7 SEC-025 to SEC-028 lines | supported | match the register rows |
| PROJECT_STATE section 8 rewritten | supported | the listed commands equal AGENTS.md section 6; no result is claimed |
| ADR-0014 lease line: "(Accepted by owner)" restored on the 30 s default | supported | present in main's ADR-0014 |
| ADR-0014 lease line and grace validation rule: "No minimum lease value is enforced in configuration (design decision taken during slice 4c-b; recorded here for the first time)" | the fact is true (config validation rejects only a lease of zero or less); the "design decision taken during slice 4c-b" is **invented** | F-R2-01 |
| ADR-0014 renewal line: "(orchestrator-authorised deviation)" for the bounded retry backoff | supported | 4c-b handoff deviation 2 and the "orchestrator's decisions" note at the end of its deviations |
| ADR-0014 delivery paragraph | supported (nominal cap, F-R2-05) | constants |
| ADR-0014 bullet "Expanding result delivery to a 60-second time-bounded loop (`deliverResult`) ... (orchestrator-authorised deviation)" | label **unsupported** by a repository document (main's text said "submitted for owner approval") | F-R2-02 |
| ADR-0014 new sub-second-lease sentence | supported | F-03 above |
| ADR-0015 `max_backoff` "(orchestrator-authorised deviation)" | fact supported (cap applies before jitter, 45 s maximum: 4b review executed the bound [0.5 s, 45 s]); label **unsupported** | F-R2-02 |
| protocol 6.3 note "(orchestrator-authorised deviation)" and blank-line removal | fact supported; label unsupported; blank line cosmetic | F-R2-02 |
| state-machine note "(...; orchestrator-authorised deviation)" and blank-line removal | fact supported (neither `RenewLease` writes an event); label unsupported; cosmetic | F-R2-02 |
| findings register SEC-025 to SEC-028 rows and sections | supported against the sources; "severity set by orchestrator" has no repository document | F-R2-02 |
| TASKS reserved row, P4-05 goal, P4-4d Done row, follow-ups F-4c-b-2 and F-4c-b-3 | supported | sections above |
| P4-05 handoff round 3 section | not a system claim; F-09 quote verified; the "Open Design Question" about a minimum lease contradicts the ADR wording | F-R2-01 |

## 3. Authority labels in added or changed lines (F-04)

Backed by a repository document:
- 30 s default "(Accepted by owner)": main's ADR-0014 text.
- Renewal retry backoff "orchestrator-authorised deviation": 4c-b handoff, deviation 2 and "Authorisations above are the orchestrator's decisions in the round 2 message".
- Wiring tests with equal small lease and grace (not labelled in the new text): 4c-a handoff Q3.

Not backed by any document in the repository (the authorisation may exist in chat; the repository cannot show it):
1. ADR-0014 "design decision taken during slice 4c-b" for the missing minimum lease. The 4c-b handoff Q7 is an open question ("Say if config should enforce a minimum lease"), the handoff has no answer, and the P4-05 handoff itself calls it an "Open Design Question". (F-R2-01)
2. ADR-0014 60 s delivery budget "orchestrator-authorised deviation": main's ADR said "submitted for owner approval"; the 4c-b handoff describes the budget but does not record an approval.
3. ADR-0015 45 s effective maximum: 4b handoff Q-3 is open and unanswered in the repository.
4. state-machine and protocol "no event on renewal" and "unknown attempt id returns 409": 4b handoff Q-4 and Q-5 are open and unanswered in the repository (the 4c-a handoff follows the 409 behaviour but does not record who decided).
5. Findings register "severity set by orchestrator" for SEC-025, SEC-026, SEC-028: no repository document records it other than the builder's own handoff row.

## Findings

### F-R2-01 "No minimum lease ... design decision taken during slice 4c-b" is invented
- Severity: minor. Label: executed (grep of the 4c-b and P4-05 handoffs) plus confirmed by code reading.
- Location: ADR-0014, "Attempt Lease Duration" line and the grace "Validation Rule" bullet (two places).
- Evidence: the sentence says a design decision was taken in slice 4c-b and is "recorded here for the first time". The 4c-b handoff records only question Q7 asking whether config should enforce a minimum; no answer exists in the repository. The P4-05 handoff (F-03 section) calls it an "Open Design Question for Orchestrator". Config indeed enforces only a lease greater than zero (confirmed by code reading of the lease validation in `internal/config/config.go`). The first audit's F-04 asked for exactly this to be confirmed or removed; round 3 swapped "owner decision" for another unrecorded decision.
- Fix: state the fact only ("config rejects a lease of zero or less and enforces no other minimum; whether to add one is an open question, see the P4-05 handoff"), or have the owner record the decision first.

### F-R2-02 Round 3 added a false "exact recognised set" claim and several unbacked authority labels
- Severity: minor. Label: confirmed by code reading, grep executed.
- Location: PROJECT_STATE slice 4c-b "Done" bullet and the CHANGELOG 4c-b bullet (false claim); ADR-0014 delivery-budget bullet, ADR-0015 `max_backoff`, protocol 6.3 note, state-machine renewal note, findings register severity notes (labels).
- Evidence: the text says the worker aborts with "the exact recognised set: 403 `WORKER_MISMATCH`, 409 `LEASE_EXPIRED` and `STALE_ATTEMPT_REJECTED`, and recognised 404 codes". In `renewOnce` the branch is on the status code alone: every 409 and every 403 returns the lost verdict, whatever the error code (so `JOB_ALREADY_TERMINAL` and any other 409 or 403 code also abort), and a 400 stops renewals without aborting. The named code set is therefore not "exact" for 403 and 409. Only the 404 branch checks codes (`recognizedNotFound`). The label problem is the list in section 3.
- Fix: write "any 403 or 409 (protocol 6.3 lists WORKER_MISMATCH, LEASE_EXPIRED, STALE_ATTEMPT_REJECTED, JOB_ALREADY_TERMINAL), and a 404 with a recognised code"; for the labels, say "authorised in the planning session; the owner accepts by merging" as the 4c handoffs do, or have the owner confirm the five items.

### F-R2-03 PROJECT_STATE still contains stale or false statements
- Severity: minor. Label: executed (grep and ls) plus confirmed by code reading.
- Location: PROJECT_STATE sections 2 and 4.
- Evidence (each checked against the code or merged documents):
  1. Section 2, slice 4b bullet: "Schema migration `000004_phase4_coordination.sql`". The real file is `000004_leases_retries_cancellation.sql` (executed ls).
  2. Section 4, slice 4b scope: "Migration `000004_leases_retries_cancellation.sql (proposed)`" in a slice marked SATISFIED, plus "(planned test)" markers on `TestDomain_ComputeBackoff_FullJitter` (section 4) and on it and `TestInMemoryRepository_LeaseRenewalAndExpiry` (section 2, slice 4a deferred items). Both functions exist (executed grep: `internal/domain/retry_test.go`, `internal/storage/inmemory_test.go`).
  3. Section 2, Phase 4 design entry ("Worker-Side Result Delivery Protocol"): "capped jittered backoff (500ms base to 5.0s cap)", "cumulative bound (lease + grace = 60s)" and a client.go line range. The implemented base is 200 ms and the budget is a fixed 60 s constant, not lease plus grace. This is a history entry labelled by branch name, not by tip hash; it contradicts the implemented behaviour and the corrected ADR. (This is the example the task named.)
  4. Section 7 open findings omit SEC-023 (Low, Open in the register; grep count 2 in the register, 0 in PROJECT_STATE). SEC-023 was already missing in round 1.
- Fix: correct 1 and 2 (the markers may stay if worded "implemented in 4b"); mark entry 3 as a design-time entry superseded by ADR-0014 as corrected, or fix the numbers; add SEC-023.

### F-R2-04 Live design doc still describes the old five-attempt delivery loop
- Severity: minor (outside the P4-05 diff). Label: executed (grep) plus confirmed by code reading.
- Location: `docs/failure-model.md`, FS-05 analysis and its "Assertion Impact" paragraph (unchanged by P4-05).
- Evidence: "Inspection of the current worker implementation in `internal/worker/client.go` (cited there by a line range, function `executeAndReport`) reveals that `ReportResult` retries at most 5 times with exponential backoff (100ms * 2^attempt) ...". That loop was replaced in 4c-b by `deliverResult` (60 s budget, 200 ms base). The corrected ADR-0014 and this document now disagree. The citation checker passes because it compares only the line number with the file length. P4-05 did not touch this file (executed: 0 lines in the diff against main).
- Fix: follow-up docs pass (the TASKS follow-ups list is the place to record it).

### F-R2-05 (informational) The "5.0s cap" is nominal
- Label: confirmed by code reading. `ComputeBackoffWithJitter` limits the multiplier to 16, so the delivery delay never exceeds 200 ms times 16 = 3.2 s before jitter, and the jitter factor is in [0.5, 1.0]. The constant 5 s is never reached. The 4c-b handoff says the effective ceiling is about 3.2 s. The ADR statement quotes the parameters correctly; consider adding "effective ceiling about 3.2 s".

## Informational notes

- I-R2-1: protocol 6.3 still does not document that a 400 on renew stops renewals without aborting (carried over from round 1).
- I-R2-2: the 4d handoff (now in the tree through the main merge) uses SEC-029 for a fixed minor finding that is not in the register, and the TASKS follow-ups do not track it. Without a "next free" claim there is no collision today, but a future finding could take SEC-029 blindly. The P4-05 handoff's round 1 text still says "next free SEC-029".
- I-R2-3: the 4d exit criteria are marked SATISFIED in PROJECT_STATE, but the review directory holds no 4d audit record, and the PROJECT_STATE milestone line does not mention P4-T1 (PR #26 merged after the branch point). Both belong to the owner's merge-time update; stated here so the merge does not miss them.

## Imports (check 3)

All ten files under `docs/agents/reviews/` are byte-identical to the current tips of their source branches (executed: `git show origin/<branch>:<path> | cmp`, rc 0, equal sha256 prefixes): `agent/gemini/P4-4b-review` (6a680d8), `agent/gemini/P4-4c-a-review` (523c923), `agent/gemini/P4-4c-b-review` (acfea2c), `agent/claude/P4-03-review` (cb8028a), `agent/claude/P4-03-review-2` (6afe95b). The P0-03 round 2 and round 3 records are absent (the directory lists only `P0-03.md` and `P0-03-evidence.txt`). Identity is against current tips; the handoff does not record the imported commits (unchanged from round 1).

## Scope (check 4)

Executed: the diff `origin/main...origin/agent/gemini/P4-05` lists 20 files; none is outside `docs/` and `CHANGELOG.md` (count 0, non-doc diff 0 bytes). `tests/` and `tests/docs/citation_allowlist.txt` are not in the diff (a first version of that line in my script had a `sed` typo and printed an error; the evidence file contains a corrected rerun and the grep that independently shows the same result).

## Gates (check 5, URL unset, counted with grep -c)

| Tree | gofmt / vet / build | make test-docs | go test -v -race -count=1 ./... | Expected |
|---|---|---|---|---|
| 695de9b (builder head) | clean / rc 0 / rc 0 | ok, rc 0 | PASS 195, SKIP 35, FAIL 0 | 195 / 35 / 0, match |
| 695de9b merged with 9fbbbcf in a scratch clone (clean merge, merge commit 838da6f, never pushed) | clean / rc 0 / rc 0 | ok, rc 0 | PASS 200, SKIP 35, FAIL 0 | 200 / 35 / 0, match |

The merge touched only Makefile, one handoff and Go tests (P4-T1), no document that P4-05 edits. All 35 skips are database-backed tests; none was exercised (prohibited by the task).

## Attacks attempted (all executed, outputs in the evidence file)

1. Compared the ADR-0014 delivery paragraph with the worker constants and `ComputeBackoffWithJitter`; searched all live docs for leftover `client.go` line ranges.
2. Read `RequestWork` and `renewalInterval` against the new sub-second-lease sentence; searched ADR-0014 for the removed 500 ms example.
3. Listed every authority label in the changed design docs and register and searched the 4b, 4c-a and 4c-b handoffs for the decision behind each.
4. Read all of PROJECT_STATE against the code (migration file names, test function existence, sweeper symbols, `renewOnce` status handling) and the register (SEC-023).
5. Searched TASKS, PROJECT_STATE, CHANGELOG and the register for any next-free SEC claim.
6. Compared the SEC-025 to SEC-028 rows with the 4c-a handoff and review; compared TASKS follow-ups and the P4-05 handoff quote with their sources.
7. `cmp` and sha256 of the ten imports against the source tips; listing of the review directory.
8. Scope diff against main; gates on the head and on a scratch merge with main.

Attacks that found nothing: import identity; P0-03 records absent; scope; gate counts on both trees; F-01, F-06, F-09 text; SEC-025 to SEC-028 attribution; the sub-second-lease sentence.

## Not verified

- Anything needing Postgres (`make test-db`, `make test-outage`, the 35 skipped tests): prohibited.
- Whether the orchestrator or owner authorised the five items in section 3 (the authorisations are in chat, not in the repository).
- Behavioural details of slice 4d in PROJECT_STATE (class mapping, cancel races, CLI presentation): read against the 4d handoff and file list only, not re-run.
- Whether the source review branches moved after the import (identity is against current tips).
- The sweeper batch size 50 in code (not part of this diff).

## Tests added

None. Docs-only audit; no production code and no test changed. Deliverables: this file and `docs/agents/reviews/P4-05-round2-evidence.txt`.
