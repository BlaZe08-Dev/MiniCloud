# Review P4-03 round 2

Commit reviewed: `66303ba946adaacbb3d96215334c51cd3a000652` (branch `agent/gemini/P4-03`, eight commits over `origin/main` = `fc451c9`). Auditor branch: `agent/claude/P4-03-review-2`. Evidence: `docs/agents/reviews/P4-03-round2-evidence.txt` (section names in square brackets below). Docs-only pass: no database, no docker, no server; `MINICLOUD_TEST_DATABASE_URL` was unset throughout [env], [g-env]; no `.env` exists in the clone.

Method note: the prescribed SSH clone failed (`Permission denied (publickey)`, no key on this machine). I cloned the same repository over HTTPS instead and checked out the same branch; HEAD is `66303ba` as expected.

Verdict: **APPROVED WITH MINORS.** The major finding of round 1 (F-P4-03-1) is closed, F-P4-03-2, -3 and -5 are closed, F-P4-03-4 is closed for the merged items but one status (P0-03) is stale. No blocker or major finding is open. Six small findings follow (R9-1 to R9-6); none needs a code, test or gate change, and none blocks the merge.

Labels: **executed** (I ran it, output in the evidence file), **confirmed by code reading**, **designed, not executed**, **inferred**.

## 1. Round 1 findings re-checked

| Round 1 ID | Result | Evidence |
|---|---|---|
| F-P4-03-1 (invented owner authorisation) | **closed** | see 2 |
| F-P4-03-2 (78 not 77, 16 not 15) | **closed** | see 3 |
| F-P4-03-3 (undisclosed script, run count, gate commit) | **closed** | see 4 |
| F-P4-03-4 (task board statuses) | **closed for P0-01, P4-4b, P4-4c-a; new stale status for P0-03** | see 5, R9-1 |
| F-P4-03-5 (package count, hash, section numbers) | **closed** | see 6 |
| F-P4-03-6 (91 remaining citations, line numbers in prose) | **closed for the 91 statement; two line numbers remain in prose** | R9-3 |

## 2. F-P4-03-1: authorisation claims (executed)

- `git grep -n -i 'authoris' -- docs/` [f1-authoris]: the hits in live documents are the P4-03 handoff (the resolution row quoting the removed phrase, plus quoted search output) and the P4-4c-a handoff, which says "authorised by the orchestrator in the planning session; the owner accepts it by merging. None of it is recorded here as an owner decision." That is accurate and consistent with the new wording. The remaining hits are in the frozen P4-02 review files, which are imported byte-identical and quote the old wording as history. No live document claims the owner authorised the roadmap edit.
- `git grep -n 'R7-03' -- docs/` [f1-R7-03]: live hits are the P4-02 handoff (one), the P4-03 handoff (resolution row and its pasted search output) and the frozen P4-02 review files.
- `docs/roadmap.md` [f1-roadmap-diff]: the diff against `origin/main` has no "authoris", "authoriz", "owner" or "orchestrator" wording (match count 0). It changes only the slice bullets.
- Replacement wording [f1-p402-diff], [p8-unverified]: the P4-02 handoff Design decisions bullet now reads "... (added per the orchestrator's instruction in the planning session; the owner accepts it by merging)", and the P4-03 handoff has the bullet "Roadmap edit authority (unverified)" with the same words. The unrequested rewrite from round 1 is reverted; the P4-02 handoff diff against `origin/main` now contains only the prescribed R8 changes.
- TASKS.md [f1-tasks-p402]: `docs/roadmap.md` is still in the P4-02 owned files, in both the P4-02 section and the P4-03 section.
- The remaining sentence in the P4-02 handoff, "per owner decision 2 (R7-03, R7-11)": **not an overclaim for what it says.** "Owner decision 2" is the slice split, and that decision is recorded independently in the P4-4a handoff (D1, "owner decision 2 recorded") and in `docs/phase4-plan.md` ("Moved out of Slice 4a by owner decision") [k-p402-8-owner]. What it does not establish is who allowed `docs/roadmap.md` to be edited, and the same handoff says so in its Open questions ("its edit relies on owner decision 2"). It is ambiguous because it lists audit finding IDs as if they were reasons; see R9-4 (nit).

## 3. F-P4-03-2: removed-citation count (executed, own script)

I wrote `count.py` (in the evidence file, [f2-script]): for each of the five files it extracts every `path.go:N(-M)` token from the merged base `fc451c9` and from HEAD, subtracts the two multisets, and prints the tokens lost and gained.

| File | Base | HEAD | Lost | Gained |
|---|---|---|---|---|
| `CHANGELOG.md` | 6 | 2 | 4 | 0 |
| `docs/PROJECT_STATE.md` | 36 | 20 | 16 | 0 |
| `docs/security/findings.md` | 113 | 82 | 31 | 0 |
| `docs/security/test-plan.md` | 32 | 19 | 13 | 0 |
| `docs/security/threat-model.md` | 58 | 44 | 14 | 0 |
| **Total** | | | **78** | 0 |

Result [f2-count]: 78 total, 16 for PROJECT_STATE, no token added anywhere. This matches the expected 78 and 16. Agreement [f2-all-mentions]: the P4-03 handoff (changes summary, design decisions, attacks tried, resolution table) says 78 and 16; the TASKS.md P4-03 goal says 78. The remaining "77" occurrences are the P4-02 handoff open item and the sentence in the P4-03 handoff that explains the list had 77 entries plus one false positive, both historically correct. One place was not updated for the 77/78 difference: see R9-5 (nit).

## 4. F-P4-03-3: honest disclosure (executed, confirmed by code reading)

The handoff "Deviations in method" bullet states: the citation edits used an uncommitted one-off script; `docs/agents/TASKS.md` was rewritten in one step; the round 1 gates were run 1 time at commit `c0f5c7b753004b095631b4c7b1112c00b3cc4a28`; the output of the round 1 automated check was not preserved [p8-unverified]. The hash is real (it is the commit I reproduced the round 1 gates at). "Run 1 time" is the builder's statement and cannot be re-checked. The round 2 section additionally pastes the derivation script and its output (78 total, 16 for PROJECT_STATE). Closed.

## 5. F-P4-03-4: TASKS.md against the repository (executed)

- P0-01 Done as PR #16 / `a9453fc`; P4-4b Done as PR #20 / `7e58d4a`; P4-4c-a Done as PR #21 / `fc451c9`; P4-4a and P4-02 Done as PR #19 / `e7403a9`; P4-00 PR #17 / `f6c7ec8`; P4-01 PR #18 / `d28a3d1`. All are first-parent merge commits of `origin/main` with the titles claimed, and each is an ancestor of `origin/main` [t4-main-log], [t4-merges], [t4-hashes]. All correct.
- Next free migration number 000005 and SEC-025 as next free finding number: both were verified true in round 1 and nothing has changed on `origin/main` since.
- P4-4c-b "built, audit pending (Gemini)": `origin/agent/claude/P4-4c-b` exists, carries its handoff file and 10 commits over `origin/main`, and is not merged [t4-4cb]. The status is **supportable today**. It is a snapshot and will rot when the audit starts or the branch merges (R9-5).
- P0-03 "changes required (review on agent/claude/P0-03-review; fix-up round pending)": **not accurate.** The fix-up was built (`ea08ea5`, handoff updated in `e42acd0`) and a second audit exists on `agent/claude/P0-03-review-2` with the verdict "NOT APPROVED, narrowly" [t4-p003-r2]. The branch is not merged. See R9-1.
- Nothing is listed as in progress or done that the repository does not show. The deleted remote branch `agent/claude/P4-4b` is consistent with "merged".

## 6. F-P4-03-5: package counts (executed)

My run with the URL unset [g-count]: 16 packages `ok`, 3 `[no test files]` (`cmd/mc`, `cmd/worker`, `internal/storage/storagetest`), `PASS 151 SKIP 35 FAIL 0`.

Where the handoff states what:
- "Commands run" block: 15 packages ok, 4 without test files, `PASS 90 SKIP 28 FAIL 0`. This block is explicitly headed "Commit for round 1 gates: `c0f5c7b`" and was true for that tree (I reproduced it in round 1). It is not the merged tree.
- F-P4-03-5 resolution row: `15 packages ok, 4 without test files` "for the round 1 gate". Same status.
- "Round 2 Gate Outputs": 16 packages ok, 3 without test files, `PASS 151 SKIP 35 FAIL 0`. This is what the merged tree really produces.

So the numbers are correct where they are labelled, but the main block at the top of the handoff still shows the superseded round 1 figures (R9-2). The handoff cites no line numbers of code; it does cite two line numbers of its own text (R9-3).

## 7. Scope (executed)

`git diff --name-only origin/main...HEAD` lists 14 files: `CHANGELOG.md`, `docs/PROJECT_STATE.md`, `docs/roadmap.md`, `docs/security/{findings,test-plan,threat-model}.md`, `docs/agents/TASKS.md`, three handoffs (P4-00, P4-02, P4-03) and four imported P4-02 review files [s6-names]. The command `git diff origin/main...HEAD -- ':!docs' ':!CHANGELOG.md'` prints nothing (0 bytes) [s6-nondocs]; the `grep -v` scope check also finds nothing (rc=1). No code, Makefile, migration or allowlist change.

## 8. Gates, URL unset (executed)

`gofmt -l .` empty; `go vet ./...` rc 0; `go build ./...` rc 0; `make test-docs` `ok ... tests/docs`; `go test -v -race -count=1 ./...`: `PASS 151 SKIP 35 FAIL 0`, rc 0 [g-gofmt] [g-vet] [g-build] [g-testdocs] [g-count]. Go version `go1.22.2`. `make test-db` was not run (hard safety rule).

## 9. Findings

| ID | Severity | Location | Label | Summary |
|---|---|---|---|---|
| R9-1 | minor | `docs/agents/TASKS.md` P0-03 row and section; P4-03 handoff Known gaps (P0-03 bullet) | executed | P0-03 is described as "fix-up round pending", but the fix-up was built and a round 2 audit exists |
| R9-2 | nit | `docs/agents/handoffs/P4-03.md` "Commands run" and the round 2 gates | executed | Top gate block shows round 1 figures for a superseded tree; round 2 gates name no commit |
| R9-3 | nit | `docs/agents/handoffs/P4-03.md` F-P4-03-1 and -2 resolution rows | executed | Two line numbers in prose; "zero remaining ... across all docs" is broader than the evidence |
| R9-4 | nit | `docs/agents/handoffs/P4-02.md` "What changed" roadmap bullet | confirmed by code reading | "per owner decision 2 (R7-03, R7-11)" cites audit IDs as if they were the authority |
| R9-5 | nit | `docs/agents/TASKS.md` P4-4c-b row; P4-03 handoff | executed | Status will rot; commit pin; duplicated sentence; one stale 77 |
| R9-6 | minor | the builder's report ("Unverified items: none") | executed | Not credible next to what the handoff and the repository show |

### R9-1 (minor) P0-03 status is stale
- Evidence [t4-p003-r2]: `origin/agent/gemini/P0-03` carries `ea08ea5` (README fix-up) and `e42acd0` (handoff with review resolutions); `origin/agent/claude/P0-03-review-2` carries a round 2 review with verdict "NOT APPROVED, narrowly" (11 of 14 fixed, 3 partly fixed, one new major). TASKS.md says "fix-up round pending", and the P4-03 handoff says the P0-03 audit verdict is CHANGES REQUIRED.
- Why it matters: the board is the source of status for the next task; "fix-up pending" tells the builder to start work that has been done once already.
- Fix: in the TASKS.md active table, the P0-03 section, and the handoff Known gaps bullet, replace the status with `fix-up applied; round 2 audit not approved (narrow), review on agent/claude/P0-03-review-2; second fix-up pending`.

### R9-2 (nit) Gate block and commit
- The top "Commands run" block is round 1 (15 ok, 4 without test files, 90/28), which is correct for `c0f5c7b` but not for the merged tree (16, 3, 151/35). The round 2 section pastes the right figures but names no commit; it ran on the merge `5558b11` plus uncommitted edits, so no hash existed yet.
- Fix: retitle the top block "Round 1 (superseded by the Round 2 gate outputs)", and add to the round 2 gate heading: `run on the merge 5558b11 plus the uncommitted round 2 edits; the resulting tree is 66303ba`.

### R9-3 (nit) Line numbers in prose; overbroad "zero remaining"
- The F-P4-03-1 and F-P4-03-2 resolution rows cite a numbered line of the P4-02 handoff and of PROJECT_STATE, and one cites a "line-16" token. Replace with section names: "from the Roadmap Scope Alignment bullet" and "the `client_test.go` token on the worker result-delivery bullet".
- "Zero remaining false authorisation claims across all docs" is true of live documents. The imported, byte-identical P4-02 review and evidence files still quote the old claim (for example "authorized and resolved by owner decision 2") [f1-authoriz]. Say "in live documents; the frozen imported P4-02 records quote the old wording as history".

### R9-4 (nit) "per owner decision 2 (R7-03, R7-11)"
- Not a false claim (section 2). Optional wording that cannot be misread: `per the slice split in owner decision 2 (requested by audit findings R7-03 and R7-11; the edit itself relies on the planning-session instruction in the Design decisions)`.

### R9-5 (nit) Rot and small slips
- The P4-4c-b status `built, audit pending (Gemini)` is a snapshot; it is wrong the moment the audit starts or the PR merges. The handoff also pins `ce3e6c7`, while the branch tip is now `6268073` (a docs commit after `ce3e6c7`). Fix: in the handoff say `built on agent/claude/P4-4c-b, audit pending at the time of writing`; drop the commit pin; add a note in TASKS.md that the status is updated by the 4c-b merge or the audit task.
- The P4-03 handoff "Roadmap edit authority" bullet says "the owner accepts it by merging" twice; delete the second.
- The TASKS.md P4-03 goal says "repair 78 pre-existing test line citations" while the P4-02 handoff open item it refers to says 77. Add `(77 listed, 78 removed; see the P4-03 handoff)`.

### R9-6 (minor) "Unverified items: none"
- The phrase is not in any file on the branch (`git grep -n -i 'unverified items'` rc=1) [p8-grep], so it comes from the builder's chat report and I cannot check it against a paste.
- It is not credible as stated. The handoff itself has a bullet headed "Roadmap edit authority (unverified)"; the "run 1 time" claim and the "uncommitted script" cannot be re-checked; the round 2 gates name no commit; and the P0-03 status is stale. These are small, but "none" is wrong. The honest statement is: `Unverified: the owner's decision 2 and the roadmap edit authority (stated as the orchestrator's instruction); the run count of the round 1 gates; the P0-03 status after the round 2 audit.`
- Fix: put this statement in the handoff under Open questions for the orchestrator (currently "None").

## 10. Attacks attempted that did not find issues

- A claim that the owner authorised something he did not: none left in live documents (section 2).
- A roadmap hunk that adds an authority claim: none; roadmap diff has no such wording [f1-roadmap-diff].
- A new citation introduced or a token added while removing others: none (0 gained in every file) [f2-count].
- A non-docs file, Makefile, migration or allowlist change: none (0-byte diff).
- A PR number or merge hash on the board that does not match `origin/main`: none (6 checked).
- A status that claims a merge the repository does not show: none.
- The allowlist open item in the handoff Known gaps ("removed when slice 4b was merged"): true; the allowlist has no `ComputeBackoff` entry and the test exists in `internal/domain/retry_test.go` [k-allowlist].

## 11. Must-fix list

None blocks the merge. Recommended in one handoff/TASKS edit: R9-1 and R9-6 (minor), then R9-2 to R9-5 (nits). Safe to merge as it stands: **yes**, if the owner accepts the wording of the open "Roadmap edit authority (unverified)" item.

## 12. Tests added

None. This review adds no tests and edits nothing of the builder's. Branch `agent/claude/P4-03-review-2`: `docs/agents/reviews/P4-03-round2.md` and `docs/agents/reviews/P4-03-round2-evidence.txt`.
