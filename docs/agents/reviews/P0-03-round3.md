# Review P0-03, round 3

Commit reviewed: `f0e6cea` (branch `agent/gemini/P0-03`; README last changed in `3a04340`, handoff in `f0e6cea`). Merge-base with `origin/main`: `fc451c9` (slice 4c-a merge). `origin/main` is now `3e8ab9e` (P4-03 merge). Auditor: Claude, branch `agent/claude/P0-03-review-3`. Earlier rounds: `docs/agents/reviews/P0-03.md` (F-P0-03-1..14) and the round-2 review on branch `agent/claude/P0-03-review-2` (R2-1..R2-10). Raw output: `docs/agents/reviews/P0-03-round3-evidence.txt`; "ev NN" is the `=== NN - ...` header in that file.

Verdict: **APPROVED WITH MINORS.** The `.env` trap is closed (executed), the quickstart works as written (executed, 10 runs), the gates match (PASS 151 SKIP 35 FAIL 0), scope is exactly the four task files and the dry-run merge is clean. No blocker or major finding is open. There are 7 minor findings and 1 nit. Two of them matter beyond style: the README now says `make test-db` runs a narrower set than it does (R3-1), and it carries progress statements that become false when slice 4c-b merges (R3-3). Fix R3-3 before or with the 4c-b merge, and R3-1, R3-2, R3-6 and R3-7 in the same pass; none needs a quickstart re-run.

Method notes (deviations and disclosures):
- **Clone over HTTPS, not SSH.** The prescribed `git@github.com:` clone failed with `Permission denied (publickey)` in this environment (reproduced in ev 00b). I cloned `https://github.com/BlaZe08-Dev/Minicloud.git`, the URL this repository's own `origin` already uses. HEAD is `f0e6cea` as expected (ev 00a). The final push may need the same change; see the end of this file.
- No `.env` anywhere in the clone, no `MINICLOUD_*` variable in the environment (ev 00a), `MINICLOUD_TEST_DATABASE_URL` unset for every Go command (`env -u`, ev 29-33), no `make test-db`, no database or container touched. `docker compose config` was run against a throwaway env-file with a fake password outside the repository (ev 27); it does not contact the daemon. The fake file is deleted.
- Ports: 8080 and 18080 were free at the start (ev 00a). Everything I started ran on 18080 or 18081, with the real child PID recorded (`$!` of the direct background command, no `timeout` wrapper around servers) and killed by a trap on exactly those PIDs. Afterwards no listener on 18080 or 18081 (ev 07, 28). The `pgrep` line at the end of ev 28 prints one line: that is the shell that ran `pgrep` (its own command line contains the pattern), not a leftover process.
- `make build` created `bin/` in the clone (not gitignored). It is not staged or committed.
- I read the `agent/claude/P4-4c-b` worktree read-only (git log and git grep, ev 19, 46) to judge staleness. I changed nothing there.

## 1. Findings from earlier rounds: re-check

Label key: executed = I ran it; code = confirmed by code reading; inferred = reasoning only.

| Finding | Status | Evidence |
|---|---|---|
| F-1 job ID placeholder | **Fixed** | README tells the reader to copy the `ID:` value and uses `<job_id>`; the extracted-and-run quickstart with the copied ID works (ev 06A, 06B, executed). |
| F-2 `MAX_JOB_RUNTIME` | **Fixed, one inexact clause (R3-4)** | "Not enforced by any component": `MaxJobRuntime` is referenced only in `internal/config/config.go` (ev 12, code). SEC-012 reference kept (finding exists, ev 42, code). |
| F-3 `LOG_LEVEL`, `DEFAULT_MAX_ATTEMPTS` | **Fixed** | `LogLevel` has no reader outside config; the handler uses `coordinator.DefaultMaxAttempts` (ev 12, code). |
| F-4 cancellation | **Fixed, rots with 4c-b (R3-3)** | README says the worker is not told; true on this base: no worker code calls renew (ev 17, code). Becomes false on the 4c-b branch (ev 46, code). |
| F-5 state lists | **Fixed** | 9 job-state and 7 attempt-state constants in `internal/domain/state.go` equal the README lists (ev 39, code). |
| F-6 PostgreSQL steps | **Fixed** | Labelled "designed, not executed"; subshell export exercised against a fake `.env` (ev 05, executed); `docker compose ... config -q` rc=0 and prints nothing, while without `-q` it prints the password (ev 27, executed). |
| F-7 container warning | **Partly fixed (R3-1)** | Warning is correct on the container-stop mechanics (helpers in `tests/integration`, ev 26, code), but it says `make test-db` "runs only `internal/storage/postgres`", which is no longer true (ev 25, executed). |
| F-8 quickstart prose | **Fixed** | Prerequisites, order, foreground note present; literal run works (ev 06A/06B). |
| F-9 jitter | **Fixed** | `baseBackoff` 200 ms and `maxBackoff` 3 s, with jitter via `ComputeBackoffWithJitter` (ev 40, code). |
| F-10 status block | **Partly fixed (R3-3)** | The Phase line is link-only now. The intro and the "Pending slice deliverables" paragraph still make status claims. |
| F-11 nine small errors | **Fixed** | `job_results` is gone, no `file:line` citation in the README, links resolve (ev 41, 43, executed). |
| F-12 make targets | **Fixed for the list, partly for descriptions (R3-1)** | The 16 targets in the README equal the Makefile `.PHONY` set (ev 23, executed). `make db-down` text matches the recipe (`compose down`, ev 25, executed). |
| F-13 handoff | **Partly fixed (R3-6, R3-7)** | See section 5. |
| F-14 marketing wording | **Fixed** | No match for the word list, no emoji (ev 41, executed). |
| R2-1 `.env` trap | **Fixed** | Section 2 below. |
| R2-2 SEC-012 / slice 4d | **Fixed** | Schedule claim gone, SEC-012 reference kept (section 4). One new clause is inexact (R3-4). |
| R2-3 status wording | **Partly fixed (R3-3)** | "Slice 4a done" gone; replaced by other statements that rot. |
| R2-4 port-busy | **Fixed** | Text matches measured behaviour exactly (ev 28, executed): second coordinator logs `address already in use` and exits; `mc workers`, `mc get`, `mc attempts` go to 8080 unless `MINICLOUD_COORDINATOR_URL` is set; with it set they reach the chosen port. |
| R2-5 startup order | **Fixed, with a caveat (R3-5)** | Order and readiness loop present; the log-line hint is unreliable on a busy port. |
| R2-6 PostgreSQL hygiene | **Fixed** | `make build` note, `-q` compose check, one exported variable in a subshell (ev 05, 27). |
| R2-7 handoff | **Partly fixed (R3-6, R3-7)** | Base commit, scope and the uncommitted script are stated; the quickstart output cannot be tied to a file, and prose line numbers are wrong. |
| R2-8 make targets | **Fixed** | `db-down` and the `.env` wording match the Makefile (ev 25). `test-db` scope is the new problem (R3-1). |
| R2-9 "shown here" | **Fixed** | README: "...yours will differ from the one shown here." |
| R2-10 `TIMED_OUT` | **Fixed, clumsy wording (R3-8)** | Reserved and not written in V1: confirmed (ev 13, code and docs). |

## 2. `.env` trap (executed)

- `git grep -n -i -E 'set -a|set -o allexport|\. \./\.env|source .*\.env|eval .*\.env|export \$\('` on README.md and the handoff (ev 03): README no match. **The handoff has one match** (the "BREAK" half of the break-before-fix demo). See R3-6.
- Whole-README read for other ways to export everything (ev 04): the only `.env` references are `cp .env.example .env`, `docker compose --env-file .env`, the two grep-and-cut subshell commands, a prose line that says not to source `.env`, `POSTGRES_PASSWORD (required in .env)`, and the `make test-db` description. No `allexport`, no `source`, no `eval`, no `export $(...)`. The only other `export` is `export MINICLOUD_COORDINATOR_URL=http://127.0.0.1:18080`, which is a single named variable.
- Fake `.env` test (ev 05): fake file with `POSTGRES_PASSWORD=ab&cd`, `MINICLOUD_DATABASE_URL=...ab&cd...&x=1`, and `MINICLOUD_TEST_DATABASE_URL`. The two README commands were extracted from README.md with awk (not retyped) and run in a clean `env -i` shell against stub binaries that print their environment. Result: both children see the full URL with every `&` intact; `MINICLOUD_TEST_DATABASE_URL` is unset in both children; after both commands the parent has `MINICLOUD_TEST_DATABASE_URL`, `MINICLOUD_DATABASE_URL` and `POSTGRES_PASSWORD` all unset.
- Residual (not a finding): the `grep | cut` extraction keeps surrounding quotes if someone writes `KEY="value"` in `.env`, as `make test-db` does too. `.env.example` has no quotes, so the documented path is fine (inferred for the quoted case).

## 3. Literal quickstart (executed)

Method (ev 06A, 06B, 07): the eight fenced `bash` blocks of "In-Memory Mode" were extracted from README.md with awk into files and run by a script that evaluates those files. Edits, and only these: (1) port `8080` replaced by `18080` with sed (the port-busy advice describes the same change); (2) the two long-running commands got a trailing `&` and a log redirect and were run directly, so `$!` is the real PID (`comm` shows `coordinator` and `worker`); (3) `<job_id>` replaced by the ID copied from the submit output, as the README tells the reader to do; (4) `export MINICLOUD_COORDINATOR_URL=http://127.0.0.1:18080` before the `mc` commands, as the README's port-busy advice says. The readiness loop block was run unchanged except the port, wrapped in `timeout 20` only so a failure could not hang the audit.

Results:
- Variant A (README commands only; no log grep, no extra sleep): every step works as written. `mc workers` shows `Registered Workers (1 total)` and `worker-1 HEALTHY 2/2 ... [echo sleep sha256_loop]`; submit gives `State: QUEUED`; `mc get` right after submit shows `QUEUED` with empty `Attempt ID` (exactly what the README says to expect: "repeat if State is still QUEUED or RUNNING"); after the README's "about a second" it shows `SUCCEEDED`, exit code 0, `{"echo":{"message":"hello minicloud"}}`; `mc attempts` shows one `COMPLETED` attempt, exit 0.
- Variant B (adds a wait for the worker's registration log line): same result.
- Race probe (ev 07): variant A eight more times. 8 of 8 runs: `Registered Workers (1 total)` immediately after the worker was started, immediate `get` `QUEUED`, `get` after one second `SUCCEEDED`. No connection-refused, no rc=1. So the missing registration wait is a theoretical race that I could not trigger (worker registration takes milliseconds); the README covers it in prose anyway (inferred for the theoretical case, executed for the 8 of 8).
- Race on `mc get` before the job finishes: real and visible in every run (immediate `get` shows `QUEUED`), and the README handles it with the sentence quoted above. Fine.

Gemini's `/tmp/test_quickstart.sh` (ev 08), compared with the README and with the handoff's pasted output:
- Not in the README: the `/healthz` poll loop (README uses `/readyz`), `sleep 0.5` after starting the worker, the `awk` job-ID parse, `sleep 1`, `timeout 30` wrappers around both servers, background plus PID capture, a cleanup trap, and a `ss` port check. The README has no log-grep command; the registration wait exists only as prose.
- The README works without all of them (variant A above). A person in three terminals is slower than any of those waits.
- **The file on disk is not the script that produced the pasted output.** It uses port 8080 (the handoff output says 18080), polls `/healthz` (the handoff says `/readyz`), does not contain the strings `Coordinator is ready`, `Worker registered successfully`, `Extracted job_id` or `Cleanup: killing`, and its modification time (13:09Z) is earlier than the timestamps in the pasted run (15:07Z). It also starts both servers behind `timeout 30` and records the wrapper's PID, which is what the owner's instruction about "find the real child, not a timeout wrapper" warns against. See R3-7.

## 4. New factual claims (checked against code and docs)

| Claim | Result | Evidence |
|---|---|---|
| `MINICLOUD_HTTP_ADDR`, `_COORDINATOR_URL`, `_WORKER_ID`, `_WORKER_SLOTS` names and defaults | Correct | `getEnv("MINICLOUD_HTTP_ADDR", "127.0.0.1:8080")`, `_COORDINATOR_URL` default `http://127.0.0.1:8080`, `_WORKER_SLOTS` default 4, `_WORKER_ID` default `worker_<hostname>_<pid>` built in the worker `main` (ev 09, 10, code). |
| `mc` reads `MINICLOUD_COORDINATOR_URL` for every subcommand; only `submit` has `--coordinator-url` | Correct | Single helper used by `get`, `attempts`, `workers`, and as the flag default for `submit` (ev 10, code); behaviour measured (ev 28, executed). |
| Port-busy advice | Correct | ev 28, executed (see R2-4 row). |
| Readiness path `/readyz` | Correct | Route registered in `internal/api/server.go` (ev 11, code); the loop returned success against a live coordinator (ev 06A/06B, executed). |
| "TIMED_OUT is reserved and not written in V1" | Correct | The constant is referenced only in `state.go` and tests/conformance; `docs/state-machine.md` says "Reserved state in schema; not written in V1" (ev 13, code). |
| `docker compose --env-file .env ... config -q` | Correct | rc=0, no output, no password; without `-q` the fake password is printed (ev 27, executed, no daemon contact). |
| "run `make build` first" before `./bin/migrate` | Correct | `make build` builds `bin/migrate` (ev 22, code). |
| `make db-down` description | Correct | Recipe is `docker compose ... down` without `-v` (ev 25, executed as dry run). |
| `make test-db` description and the "runs only `internal/storage/postgres`" sentence | **Wrong** | R3-1. |
| "per-job `timeout_seconds` (1..86400) is the only runtime bound today" | **Inexact** | R3-4. The range itself is right (ev 12, code). |
| "`MAX_JOB_RUNTIME` not enforced; worker does not clamp coordinator timeout" | Correct | The worker converts `TimeoutSeconds` straight to a duration and the executor only defaults when it is zero or negative (ev 16, 44, code). |

SEC-012 wording (task item 6): kept. The README names "open finding SEC-012 in docs/security/findings.md" and makes no schedule claim; SEC-012 exists in the register and is Informational and Open (ev 42). `grep -i clamp docs/phase4-plan.md` has no hit, so a schedule claim would have been unbacked; there is none.

## 5. Handoff honesty (task item 7)

- Base commit and diff scope: stated and correct (`fc451c9`, three-dot diff of four files; re-run, ev 00a, 01, 34). It does not say that `origin/main` has since moved to `3e8ab9e` or that the two-dot diff lists 18 files for that reason (ev 01). Nit-level, folded into R3-7.
- Raw gate output: the handoff pastes `rc=0` for gofmt, vet and build, the `make test-docs` line, and exactly two lines for the full test run (`exit: 0` and the count line). The counts reproduce (section 6). It does not paste the per-package `ok` lines, and `rc=0` is not the command's output. Folded into R3-7.
- Uncommitted script: the handoff says plainly that "the test runner script was executed in-shell and is not committed". It does not say what the script adds to the README steps (job-ID extraction, one-second sleep, registration wait by log grep, backgrounding), and the on-disk script differs from the pasted output (section 3).
- "literal / verbatim": the two "Verbatim output" labels are accurate: the scope command output matches (ev 01) and the break-before-fix output matches byte for byte when the block is extracted from the handoff and re-run (ev 38, executed). The quickstart block is not labelled verbatim. No misuse found.
- Line numbers in prose: **yes**, in the resolution table (every row has a `README.md:<number>` location). They are also wrong: the corresponding README locations are different (ev 35, 36, executed). AGENTS.md rule 6 forbids unverified line citations.
- Pasted `grep` output of the findings register (with its own line numbers) is quoted command output, not prose; fine.

## 6. Gates (executed, URL unset, no `.env`)

- `gofmt -l .`: empty, rc=0 (ev 29). `go vet ./...`: rc=0 (ev 30). `go build ./...`: rc=0 (ev 31).
- `make test-docs`: `ok .../tests/docs 0.620s`, rc=0 (ev 32).
- `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...`: exit 0, 16 packages `ok`, no `FAIL`, no `DATA RACE`, **`PASS 151 SKIP 35 FAIL 0`** (ev 33). Matches the handoff and the expected counts.

## 7. Scope (executed)

`git diff --name-only origin/main...HEAD` = `README.md`, `docs/agents/handoffs/P0-03.md`, `docs/agents/reviews/P0-03-evidence.txt`, `docs/agents/reviews/P0-03.md` (ev 01), exactly the expected set. Dry run `git merge --no-commit --no-ff origin/main`: "Automatic merge went well", no conflict; `git merge --abort` clean, HEAD back at `f0e6cea` (ev 02). This is without my own two files, which are on the review branch.

## 8. Findings

### R3-1 (minor, executed) `make test-db` is described as narrower than it is
Location: README, "Testing", Make Targets entry for `make test-db`, and the Test Guardrails bullet "Container Interruption Warning" ("runs only `internal/storage/postgres`").
Evidence (ev 25, 26): `make -n test-db` shows the recipe runs `go test` on `./internal/storage/postgres/...` **and** `./internal/coordinator/...`, and then a second `go test` with `-run '^TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit$'` on `./tests/integration/`. That test exists (`go test -list`). The function itself contains no container stop or start call (ev 26), so the safety conclusion stands, but both README sentences are false as written. A reader who trusts "only one package" will misjudge what `test-db` touches.
Fix: Make Targets entry: "Runs the PostgreSQL-backed tests in `internal/storage/postgres` and `internal/coordinator`, plus one coordinator-restart integration test in `tests/integration`, using the `MINICLOUD_TEST_DATABASE_URL` value read from `.env`." Guardrails bullet: replace "runs only `internal/storage/postgres`" with "runs a fixed, narrow set (see `make test-db` in the Makefile)".

### R3-2 (minor, executed) Configuration table omits four coordinator variables that are live on this base
Location: README, "Configuration" table and the lead sentence "All configuration is loaded from environment variables".
Evidence (ev 09, 14): `internal/config/config.go` reads `MINICLOUD_LEASE_DURATION` (30s), `MINICLOUD_SWEEPER_INTERVAL` (5s), `MINICLOUD_STARTUP_GRACE_PERIOD` (30s), `MINICLOUD_OUTAGE_GRACE_WINDOW` (30s); the coordinator `main` passes them to the repository and to `StartLeaseSweeper`. Config validation requires both grace values to be at least the lease duration. `grep -i -E 'LEASE_DURATION|SWEEPER|GRACE' README.md` finds nothing. They arrived with slice 4c-a, which is part of this branch's base, and the table was written for an older tree.
Fix: add four rows (Used By: Coordinator; the defaults above; one-line description each; note "grace values must be at least the lease duration").

### R3-3 (minor, must-fix before or with the 4c-b merge; code reading and executed) Progress and schedule statements that rot
The task said no progress statements. The README still has these, and they are false or stale at the next merge:
1. Introduction: "(worker-side lease renewal is not implemented yet; see docs/PROJECT_STATE.md)". True on this base (no worker code calls the renew route, ev 17). On the `agent/claude/P4-4c-b` branch the worker has a renewal loop, abort on lease loss, and cancel via the renewal response (ev 19, 46, code). It is false the moment 4c-b merges.
2. "What Works Today", last paragraph: "Pending slice deliverables (including worker-side lease renewal, automatic retries with backoff, and cancellation delivery to running workers)". Same rot, and it names specific items.
3. HTTP API, cancel endpoint: "the worker is not told yet (planned in Phase 4 slice 4d)". After 4c-b the worker is told through the renewal response; "slice 4d" is also a schedule claim. The README lists the renew route with "receive cancellation notifications" a few lines below, so the two statements will contradict each other.
4. Roadmap: "cancellation reaching running workers (planned in slice 4d; see docs/phase4-plan.md)". Same schedule claim.
5. The pointer target is itself stale: `docs/PROJECT_STATE.md` on `origin/main` still says "Phase 4 Slice 4a complete ... next: slice 4b" although 4b and 4c-a are merged (ev 18, 21). The README delegates status to a document that does not currently deliver it. (Not a README defect; the owner or the task that owns that file fixes it at merge. Mention in the PR.)
What to say instead: state timeless mechanisms and point at the specification, with no implemented/not-implemented verdicts.
- Introduction: "...independent worker processes pull and execute allowlisted tasks under a per-attempt lease (design: [ADR-0014](docs/adr/0014-...md) and [docs/protocol.md](docs/protocol.md))". (Use the actual ADR file name; check it exists.) Drop the parenthetical about renewal.
- "What Works Today": delete the "Pending slice deliverables" paragraph, or reduce it to "Per-slice implementation status is in [docs/PROJECT_STATE.md](docs/PROJECT_STATE.md); later versions are in [docs/roadmap.md](docs/roadmap.md)."
- Cancel endpoint: "A queued job is cancelled immediately. For a running job the coordinator records `CANCEL_REQUESTED`; the worker learns of it through the lease renewal response (ADR-0016, [docs/protocol.md](docs/protocol.md))." That sentence is a design statement, true on every branch.
- Roadmap V1 bullet: remove the parenthetical; keep "cancellation reaching running workers".
- "Phase 4 in progress" in the Status block is link-only and acceptable; it still ends when Phase 4 ends. Optional: drop the phase number and keep only the PROJECT_STATE link.

### R3-4 (minor, confirmed by code reading, partly inferred) "only runtime bound today" is inexact
Location: README configuration table, `MINICLOUD_MAX_JOB_RUNTIME` description.
Evidence: besides the per-job timeout, task payload caps bound the built-in tasks (`MaxSleepDurationMs` = 10000, `MaxSHA256Iterations` = 1,000,000, ev 15, code), and since 4c-a the coordinator's lease sweeper (30 s default lease, ev 14, 45, code) can mark an attempt LOST after the lease passes (inferred for the exact interaction with a long task; I did not run a task longer than the lease). "The only runtime bound" is therefore a stronger claim than the code supports.
Fix: "Parsed and validated at startup, but not enforced by any component. The worker applies the per-job `timeout_seconds` (1..86400) to task execution and does not clamp it (open finding SEC-012 in docs/security/findings.md)."

### R3-5 (minor, executed) Readiness hint is misleading when the port is busy
Location: Quick Start, "Wait until the coordinator is ready (it logs `coordinator listening for requests`), or verify with the readiness probe".
Evidence (ev 28): a second coordinator on a busy port logs `coordinator listening for requests` and then `http server failed ... address already in use` and exits; the `/readyz` loop in the README succeeds anyway because the first process answers. Both hints pass on a failed start. The README's port-busy paragraph above is correct and complete, so a reader who follows it is fine; this is about the wording of the wait.
Fix: "Check first that nothing listens on the port (`ss -ltn | grep ':8080'` prints nothing). The coordinator logs `coordinator listening for requests` before it binds; if the next log line is `http server failed`, the port was busy."

### R3-6 (minor, executed) Handoff still contains the auto-export command
Location: handoff, break-before-fix block ("BREAK (Old way...)").
Evidence (ev 03): `git grep` finds `set -o allexport; . "$DOT_ENV"; set +o allexport` in the handoff. The block runs in a subshell against a fake `.env` and is safe as written (re-run, ev 38), but it is a ready-to-copy form of the trap, in a file the task says must have no such line.
Fix: keep the FIX half and the observed outcome in prose ("sourcing the whole file exported `MINICLOUD_TEST_DATABASE_URL` and lost a `&` password; round 2 evidence ev 09"), or write the BREAK half so the pattern does not appear (describe it, do not show it).

### R3-7 (minor, executed) Handoff evidence gaps
Location: handoff, quickstart block, the gate blocks, the resolution table.
1. The pasted quickstart output cannot be matched to any file: `/tmp/test_quickstart.sh` differs (port, probe, wrappers, strings, timestamps; ev 08). Say "the quickstart was run by a helper script that is not committed and whose contents are not reproduced here", list what it adds to the README steps (job-ID extraction, a one-second sleep, registration wait by log grep, backgrounding with PID kill), and do not point at a path that holds a different script. Better: commit nothing, but paste the script's text into the handoff.
2. Gate blocks show `rc=0` instead of command output, and the full test run shows two summary lines. Paste the real tail of each command (for the test run, the 16 `ok` lines plus the count line).
3. Line numbers in prose are wrong and forbidden (ev 35, 36). Replace with section names or the quoted text.
4. Add one sentence: "`origin/main` moved to `3e8ab9e` after the base; the three-dot diff is the four task files, the two-dot diff lists more files only because of that."

### R3-8 (nit, confirmed by reading) Redundant wording for `TIMED_OUT`
Location: README, "What Works Today", Domain State Machine bullet: "`TIMED_OUT` (`TIMED_OUT` is reserved and not written in V1)". The token repeats. Fix: "`TIMED_OUT` (reserved, not written in V1)".

## Attacks and areas where I found no defect

- Fake `.env` with `&` in two values: no leak into the parent shell, no truncation (ev 05).
- Quickstart: no step failed in 10 runs; no leftover process or port (ev 06A, 06B, 07).
- Port-busy advice matches measured behaviour for all of `workers`, `get`, `attempts` (ev 28).
- All README links resolve (ev 43); no marketing wording, emoji or line citation in the README (ev 41).
- Make target set equals the Makefile (ev 23); route list in the README equals `internal/api/server.go` (ev 11, code), including the renew route.
- Job and attempt state lists, jitter constants, attempt and identifier bounds equal the code (ev 39, 40).
- Not tested by me: any PostgreSQL runtime path, `make demo`, `make db-*`, `make test-db`, anything on the 4c-b branch beyond reading it.

## Tests added
None (docs-only task).

## Must-fix summary
No blocker, no major. Before the next merge of this branch: R3-1, R3-2, R3-6, R3-7. Before or with the 4c-b merge: R3-3 (it is the owner's stated rule against progress statements and it becomes false in that merge). R3-4, R3-5, R3-8 at the builder's discretion. After these, no re-run of the quickstart is needed unless the Quick Start commands change.
