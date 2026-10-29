# Review P0-03, round 2

Commit reviewed: `e42acd0076be3392922680dbc1342812eb50ae61` (branch `agent/gemini/P0-03`; README last changed in `ea08ea5`, handoff in `e42acd0`). Merge-base with `origin/main`: `e7403a9` (slice 4a merge). `origin/main` is now `7e58d4a` (slice 4b merged). Auditor: Claude, branch `agent/claude/P0-03-review-2`. Round 1: `docs/agents/reviews/P0-03.md` (F-P0-03-1..14). Raw output: `docs/agents/reviews/P0-03-round2-evidence.txt`; "ev NN" below is the `=== NN-... ===` header in that file.

Verdict: **NOT APPROVED, narrowly.** 11 of the 14 round-1 findings are fixed and re-verified against the code, and the remaining 3 are partly fixed. The fix-up added one major problem (the `.env` sourcing step primes the shared-container outage trap), one unsupported claim (SEC-012 "planned for slice 4d"), and several statements that are false or will be stale against `main` at `7e58d4a`. Four small edits make it approvable (must-fix list at the end).

Method notes: fresh `gh`/https clone to `~/mc-readme-audit2` (ev 00), no `.env` copied, no `MINICLOUD_*` variable in the environment (ev 18), no database touched, no `docker`, `go test` run only with `MINICLOUD_TEST_DATABASE_URL` unset. **Disclosure:** at the start, port 8080 was already held by a coordinator that is not mine (pid 264576, started from `/home/blazeee/MiniCloud`, ev 11). I did not stop it. My first quickstart run therefore talked to that process: my `mc submit` created one echo job (`job_1_bafa645b364f`) on it, and my worker `worker-1` registered with it for a few seconds before I killed my own worker by pid (ev 10, 11). That run is void. I redid the quickstart on port 18080 (ev 12, 13). So **the "ports 8080 and 8085 free" check could not be satisfied for 8080** (it is still held by the foreign process at the end); 8085 and 18080 are free and every process I started is gone (ev 13, 19, 22). Handoff, README and builder files are untouched.

## 1. Round-1 findings re-checked

Label key: executed = I ran it (evidence cited); code = confirmed by code reading; inferred = reasoning only.

| Finding | Status | What I checked and quote |
|---|---|---|
| F-1 job ID | **Fixed** (nit R2-9) | README 135: "Copy the `ID:` value from the submit output; the job ID is generated per submission and yours will differ ... Wait about a second ... (repeat if `State` is still `QUEUED` or `RUNNING`)", commands use `<job_id>`, line 141: "The IDs, attempt IDs and times in the outputs below come from one sample run." Executed (ev 13): copied-ID `get` and `attempts` work, rc=0. Literal `./bin/mc get <job_id>` is a bash syntax error (ev 10, rc=2), acceptable for a placeholder. |
| F-2 `MAX_JOB_RUNTIME` | **Fixed, with a new unsupported clause (R2-2)** | README 225: "Parsed and validated at startup, but not enforced by any component yet (worker timeout clamp planned for slice 4d, tracked as SEC-012); per-job `timeout_seconds` (1..86400) is the only runtime bound today". "Not enforced" confirmed by code (ev 05: only `config.go` and tests reference `MaxJobRuntime`). The parenthetical is not backed (R2-2). |
| F-3 `LOG_LEVEL`, `DEFAULT_MAX_ATTEMPTS` | **Fixed** | README 224 and 226 say "not yet applied by the coordinator; the effective default is a constant of 3 in code" and "Parsed, but not applied; logging is fixed at `info`". Code (ev 05): `handlers.go:188` uses `coordinator.DefaultMaxAttempts`; `service.go:99` is `= 3`; no reader of `LogLevel` outside config. Value list removed. |
| F-4 cancellation | **Fixed** | README 278: "A queued job is cancelled immediately; for a running job cancellation is recorded by the coordinator (`CANCEL_REQUESTED`), but the running job is not interrupted and the worker is not told yet (planned in Phase 4 slice 4d)"; 346: "cancellation reaching running workers (planned in slice 4d; see docs/phase4-plan.md)". `docs/phase4-plan.md` has "Slice 4d: ... Cooperative Cancellation" (ev 07). Handler returns 200 (`handlers.go` cancel handler, ev 07). Same on `origin/main` (no handler/coordinator change beyond `LEASE_EXPIRED` mapping, ev 14). |
| F-5 state lists | **Fixed** (nit R2-10) | Redone from the code, not the builder's table. README 20 job states `SUBMITTED, QUEUED, RUNNING, CANCEL_REQUESTED, RETRYING, SUCCEEDED, FAILED, CANCELLED, REJECTED` = 9 constants in `internal/domain/state.go:7-15` and 9 rows in `docs/state-machine.md` section 2 (ev 05). README attempt states `PENDING, RUNNING, COMPLETED, FAILED, TIMED_OUT, CANCELLED, LOST` = 7 constants in `state.go:23-29` and `state-machine.md:70` "Canonical Attempt States" (ev 05). Same on `origin/main` (diff of `state.go` HEAD vs main is empty, ev 04). The pointer "Section 3 and Section 4" is right (3 = job transition matrix, 4 = attempt lifecycle). |
| F-6 PostgreSQL steps | **Partly fixed; new defects R2-1, R2-6** | "designed, not executed" label kept; hardcoded password gone; export added. The export as worded is the trap in R2-1. |
| F-7 container warning | **Fixed** (nit R2-8) | README 340 matches my replacement. Checked: `container_name: minicloud-postgres` (ev 23); `stopPostgresContainer`/`startPostgresContainer` called from `postgres_wiring_test.go` and `part_d_security_test.go` (ev 23); `make test`/`test-race` are plain `go test [-race] ./...` (Makefile, ev 02); `make test-db` reads the URL from `.env` and runs only `./internal/storage/postgres/...` (ev 02). `db-reset-test` hardcodes `minicloud_test` (ev 02). |
| F-8 quickstart prose | **Partly fixed (R2-4, R2-5)** | README 85 has prerequisites ("Go 1.22.2 and `make`", repo root, Ctrl-C, port-busy advice); `go.mod` says `go 1.22.2` (ev 23); line 93 now says "in the foreground". Missing pieces are R2-4 and R2-5. |
| F-9 jitter | **Fixed** | README 24: "exponential backoff with jitter (200 ms base, 3 s cap) on HTTP 5xx or poll errors". Code (ev 06, 23): `baseBackoff := 200 * time.Millisecond`, `maxBackoff := 3 * time.Second`, applied on `ErrServiceUnavailable` and on other poll errors; jitter is 0.5..1.0 of the capped value. "full" removed. |
| F-10 status block | **Partly fixed (R2-3)** | Old text proof: `git diff 567c731 ea08ea5 -- README.md` (ev 24) shows the Phase line and the "Not implemented yet" paragraph as pure additions (no deleted Phase text), so nothing was there before. New text: line 8 "Phase 4 in progress (slice 4a done)" (I advised link-only) and line 26 "Not implemented yet: attempt leases and expiry sweep, automatic retries and backoff, cancellation reaching a running worker, assignment-based round-robin dispatch (dispatch is `fifo_pull`; `internal/scheduler` is not used), authentication and TLS, Docker isolation, Redis transport." Stale or inexact against `main`, see R2-3. |
| F-11 nine small errors | **All nine fixed** | Old text from the diff (ev 24) vs new README: (1) `job_results` -> `results` (line 70; table exists, `000001_initial_schema.sql:133`, ev 23); (2) `inmemory or postgres` -> "in-memory repository or internal/storage/postgres" (line 60); (3) "current attempt number" -> "current attempt ID" (262); (4) scheduler "(not yet used for dispatch)" (305); (5) "Pulls assigned work" -> "Polls the coordinator for work" (33); (6) `--coordinator-url` mentioned (261; `cmd/mc/main.go:68`, ev 16); (7) `DB_QUERY_TIMEOUT` "Boot timeout, and `statement_timeout`/`lock_timeout` for pool connections" (229; `cmd/coordinator/main.go:51,61-67`, ev 16); (8) `HEARTBEAT_INTERVAL` "Worker (coordinator validates it against `MINICLOUD_WORKER_TIMEOUT`)" (221; `config.go:81`, `client.go:74`, ev 16); (9) fallback sentence (235) true: `getIntEnv`/`getDurationEnv` return the default on a parse error (ev 16). |
| F-12 make targets | **Fixed** | Redone in both directions (executed, ev 08): targets in Makefile `.PHONY` set = `all build clean db-down db-psql db-reset-test db-up demo fmt run-coordinator run-worker test test-db test-docs test-race vet` (16); targets listed in README 320-335 = the same 16; `diff` rc=0. Old text: the README before the fix-up listed 13 (diff ev 24 shows `all`, `run-coordinator`, `run-worker` as additions). |
| F-13 handoff | **Partly fixed (R2-7)** | See section 4. |
| F-14 | **Fixed** | "engineering-serious" gone (grep, ev 23: the only hit for the word list is "not a production-ready cloud service"); line 3 now "is not designed to be exposed to untrusted networks". |

## 2. Literal quickstart (executed)

Script (ev 25): the README commands copied as written; run from a clean shell in the clone under an outer `timeout 60`, with cleanup by pid. The only edits are the ones forced by running in one shell: the two long-running commands are followed by `&`, and `<job_id>` is replaced by the ID parsed from the submit output (the README itself says to do that).

- Run 1 (ports as written, ev 10): **void**. Port 8080 was held by a foreign coordinator; my coordinator logged `listen tcp 127.0.0.1:8080: bind: address already in use` (ev 11) and `mc` silently used the other process (`Registered Workers (0 total)`, then a job accepted by it). Literal `./bin/mc get <job_id>` gives `syntax error near unexpected token 'newline'` rc=2.
- Run 2 (port 18080, as the README's port-busy advice says, no pause, ev 12): `mc workers` and `mc submit` immediately after starting the coordinator fail with `dial tcp 127.0.0.1:18080: connect: connection refused`, rc=1. In three real terminals a person is slow enough to avoid this; a script or a quick paste is not.
- Run 3 (same, with a 2 s pause that is not in the README, ev 13): **works as written**: `Registered Workers (1 total)` with `worker-1 HEALTHY 2/2 ... [echo sleep sha256_loop]`; submit gives `State: QUEUED`; `get` (immediately and again after 1 s) gives `State: SUCCEEDED`, `Exit Code: 0`, `Output: {"echo":{"message":"hello minicloud"}}`; `attempts` gives `1 total ... COMPLETED 0`. All rc=0. The job finished in under a second, so the README's "wait about a second" is conservative but harmless.
- Does the README explain the waits a newcomer needs? Partly. It explains the wait before `get`. It does not say to wait for the coordinator before starting the worker and `mc` (R2-5), and the port-busy advice is incomplete and can silently aim `mc` at the wrong process (R2-4).

## 3. Attacks on the new text

### F-P0-03-R2-1 (major) Sourcing `.env` into the interactive shell primes the outage-test trap
README 192-196 (inside step 2, after `make db-up`):
```
.env is read by `make` and `docker compose`, not by the binaries. Export the configuration first:
set -a; . ./.env; set +a
```
Evidence:
- `.env.example` defines `MINICLOUD_TEST_DATABASE_URL=postgres://postgres:...@127.0.0.1:5433/minicloud_test?sslmode=disable` (ev 02), so a `.env` copied from it does too.
- Executed with a fake `.env` (no database): after `set -a; . ./.env; set +a` the variable is still exported in the shell (`env | grep -c` = 1, ev 09). `set +a` only stops further auto-export; it does not unexport.
- Code (ev 17, 23): `postgres_wiring_test.go:31` and `part_d_security_test.go:1011` read the variable and skip when it is empty; the outage tests call `stopPostgresContainer`/`startPostgresContainer`. So in that same shell `go test ./...`, `make test` and `make test-race` run them and stop and restart the shared `minicloud-postgres` container. The README warns about this 130 lines later (line 340), but the step that sets the variable is the one a reader follows first.
- Also exported: `POSTGRES_PASSWORD`, `POSTGRES_USER`, `POSTGRES_DB`, `POSTGRES_PORT`, and the real database URL, into every later child of that shell.
- Safe alternative checked: integration tests that start the coordinator pass `MINICLOUD_DATABASE_URL` through their own `cmd.Env` (ev 17), and nothing else reads `MINICLOUD_DATABASE_URL` in tests, so exporting only that variable cannot trigger tests.
Label: trap by code reading plus executed export persistence; the container stop itself was not run (by rule).
Replacement for lines 192-196 and steps 3-4 (use a subshell, or export only the URL):
````
`.env` is read by `make` and `docker compose`, not by the binaries. Do not `source` it into your interactive shell: it also defines `MINICLOUD_TEST_DATABASE_URL`, and `go test ./...` or `make test` in that shell would run tests that stop and restart the shared PostgreSQL container. Export only the one variable you need:

```bash
export MINICLOUD_DATABASE_URL="$(grep -E '^MINICLOUD_DATABASE_URL=' .env | cut -d= -f2-)"
```

3. Run database migrations (run `make build` first if `bin/` does not exist):

```bash
./bin/migrate
```
````
(Step 4 then stays as is.) If the builder prefers sourcing, the minimum is a subshell per command: `( set -a; . ./.env; set +a; ./bin/migrate )` and `( set -a; . ./.env; set +a; MINICLOUD_STORAGE_DRIVER=postgres exec ./bin/coordinator )`.

### F-P0-03-R2-2 (minor) "worker timeout clamp planned for slice 4d, tracked as SEC-012" is not backed
README 225 and handoff lines 11 and 113. `grep -ci clamp docs/phase4-plan.md` = 0 (ev 08); no doc maps SEC-012 to slice 4d. SEC-012 (`docs/security/findings.md:177`) is "Informational, Open (inferred from code review, unconfirmed)" and is about the worker not clamping a coordinator-supplied timeout, which is a different thing from enforcing `MINICLOUD_MAX_JOB_RUNTIME`. `MAX_JOB_RUNTIME` appears in no design doc (ev 08). Label: confirmed by code reading and doc grep.
Replacement for the Description: "Parsed and validated at startup, but not enforced by any component yet; per-job `timeout_seconds` (1..86400) is the only runtime bound today". Drop the same parenthetical from handoff lines 11 and 113.

### F-P0-03-R2-3 (minor, stale or inexact against `main` `7e58d4a`) Status wording
Checked against `git show origin/main:...` and the merge-base tree (ev 03, 04, 14, 15). What changed on main: migration `000004_leases_retries_cancellation.sql`, `RenewLease`/`SweepExpiredLeases` in both repositories, `domain.ComputeBackoff`, dispatch that stamps `lease_expires_at`, `ErrLeaseExpired` mapped to 409 `LEASE_EXPIRED`. What did not: no non-test caller of `RenewLease`/`SweepExpiredLeases` outside tests (ev 14: only `internal/api/api_test.go`), no sweeper loop, no renewal route, no worker renewal, `cmd/` and `internal/{coordinator,worker,config}` unchanged, job and attempt state lists unchanged, no new table, `docs/PROJECT_STATE.md` untouched by 4b (still "Slice 4a complete ... next: slice 4b").
- Line 8 "Phase 4 in progress (slice 4a done)": true today and stale as soon as `PROJECT_STATE.md` moves; a slice label in a README is the first thing to rot. This was the advice in F-10 and was not taken.
- Line 26 "attempt leases and expiry sweep, automatic retries and backoff" not implemented: runtime-true on main (nothing runs them) but false at repository level once 4b is merged (the methods, the migration and the pure backoff exist and are tested). It will go stale again when 4c wires the sweeper.
- Line 3 "worker processes pull and execute allowlisted tasks under leases" contradicts line 26. At the merge-base only lease tokens exist (`HashLeaseToken`, checked at commit; no `lease_expires_at` column in migrations 000001-000003, ev 15); expiry is not enforced.
- Line 24 says backoff exists (worker poll backoff) and line 26 says "backoff" is not implemented (job-retry backoff). Ambiguous.
Replacements:
- Line 8: `- **Phase:** V1, Phase 4 in progress. The current slice and what is verified are in [docs/PROJECT_STATE.md](docs/PROJECT_STATE.md).`
- Line 26: `Not wired end to end yet (see [docs/PROJECT_STATE.md](docs/PROJECT_STATE.md) for slice status): lease expiry and the sweeper, lease renewal by workers, automatic job retries with backoff, cancellation reaching a running worker, assignment-based round-robin dispatch (dispatch is `fifo_pull`; `internal/scheduler` is not used), authentication and TLS, Docker isolation, Redis transport.`
- Line 3: replace "under leases" with "with a per-attempt lease token (lease expiry is not enforced yet)".
No test counts, migration lists, or slice numbers elsewhere in the README (grep, ev 15), so nothing else is stale. Label: confirmed by code reading of `origin/main`.

### F-P0-03-R2-4 (minor) Port-busy remedy is incomplete and can silently target the wrong process
README 85: "If port 8080 is busy, the coordinator exits with `address already in use`; change `MINICLOUD_HTTP_ADDR` and `MINICLOUD_COORDINATOR_URL` together." The Terminal 2 command sets the URL inline; the Terminal 3 `mc` commands do not, so after changing the port `mc` still uses the default 8080 (`getCoordinatorURL`, `cmd/mc/main.go:18`). Executed (ev 10, 11): with another MiniCloud coordinator on 8080, my coordinator exited with `address already in use` in its own terminal while the worker and `mc` kept working against the other process, with no error. Also the coordinator logs "coordinator listening for requests" before the bind fails (ev 11), so that log line is not proof it is up.
Replacement: "If port 8080 is busy (`ss -tulpn | grep 8080`), the coordinator logs `address already in use` and exits, but the worker and `mc` will happily talk to whatever else owns 8080. Pick another port, set it in `MINICLOUD_HTTP_ADDR` for the coordinator, and `export MINICLOUD_COORDINATOR_URL=http://127.0.0.1:<port>` in the worker and `mc` terminals."

### F-P0-03-R2-5 (minor) No ordering or wait for startup
Executed (ev 12): `mc workers` right after starting the coordinator gives `connection refused` rc=1. Round 1 showed the worker exits rc=1 when no coordinator is up (round-1 ev 05). README 93-103 says nothing about order.
Replacement, add after the Terminal 1 command: "Wait for the log line `coordinator listening for requests` before starting the worker; start Terminal 3 commands after the worker logs `worker successfully registered with coordinator`." (Both strings are in the code and in my logs, ev 11, 25.)

### F-P0-03-R2-6 (minor) PostgreSQL step hygiene (besides R2-1)
- Fragile quoting (executed, ev 09): `POSTGRES_PASSWORD=ab&cd` in `.env` sourced with `. ./.env` gives an empty password (the `&` backgrounds the assignment and `cd` runs). The README tells people to "set local credentials", so a strong password with `&`, `$`, `#`, `;` or spaces breaks the documented sourcing step, while `docker compose` reads the same file with different rules. The `grep | cut` export in R2-1 avoids this (it also matches how `make test-db` reads the file).
- `./bin/migrate` needs `make build`, which is only in the in-memory section; a reader who jumps to PostgreSQL gets `No such file or directory` (inferred from the same behavior seen in round 1 with `./bin/coordinator`). Covered in the R2-1 replacement text.
- `docker compose ... config` (README 183) prints the password even with the new note. Suggest `docker compose --env-file .env -f deployments/docker-compose.yml config -q`, which validates without printing (inferred from Docker Compose documentation, not run: no docker by rule).
- The export sentence sits under step 2 and has no number, although it is a prerequisite of step 3. R2-1's replacement makes it part of step 3.

### F-P0-03-R2-7 (minor, handoff) Handoff overstates its evidence
- "Verified via automated script replicating README instructions" (design decisions) and "`/tmp/test_quickstart.sh` executing README instructions verbatim": its own pasted output shows edited step headers ("backgrounded for script with timeout"), an extra "Wait about a second" step and a captured job ID (the README now has `<job_id>`, which cannot run verbatim, ev 10). The script is not in the repo and not pasted, so the claim cannot be re-checked.
- F-13 said to paste verbatim command output. The gate blocks are `rc=0` and one `PASS 90 SKIP 28 FAIL 0` line; the raw `go test -v` output is not pasted. The numbers do reproduce (see section 5).
- F-13 asked for inferred statements to be labelled inferred; the handoff contains no such labels (grep for "inferred" finds none); and it still lists "F-P0-03-13" as remediated by the same handoff.
- It does not say that the branch is based on `e7403a9` and that `main` has moved to `7e58d4a`, and it contains no scope statement or diff explanation (grep for `origin/main`, `...`, `two-dot`, `three-dot`, `scope`, `rebase`, ev 20: no hit).
- It repeats the unsupported SEC-012/4d claim (R2-2), says "Open questions: None", and gives no stale-wording warning (R2-3).
What is true: tip `e42acd0` differs from `ea08ea5` only in the handoff (ev 20, `README.md` unchanged between them), so "gates executed at `ea08ea5`" holds for the README; the imported review files are byte-identical to `origin/agent/claude/P0-03-review` (sha256 match, ev 20); the hashes `567c731`, `521be86`, `5eb510f`, `ea08ea5`, `e42acd0` are the real commits and their contents match the handoff's per-commit description.
Replacement text for the handoff: (a) say "quickstart run with a script that follows the README commands, with `<job_id>` replaced by the parsed ID and the two servers backgrounded; script not committed"; (b) paste the raw `go test` count lines; (c) add "Based on `e7403a9`; `origin/main` was `7e58d4a` at audit time; `git diff --name-only origin/main...HEAD` is the 4 task files, and `origin/main..HEAD` lists 28 files only because main moved ahead (it shows main's own changes in reverse)"; (d) drop the SEC-012 clause.

### F-P0-03-R2-8 (nit) Make target wording
- README 333: `make db-down` "Stops the shared PostgreSQL container ... (named volumes preserved)": the recipe is `docker compose ... down` (Makefile, ev 02), which stops and removes the container and network. Say "Stops and removes the shared PostgreSQL container and network".
- README 327: `make test-db` "against `MINICLOUD_TEST_DATABASE_URL`": the target reads that value from `.env`, not from the environment (ev 02). Say "against the `MINICLOUD_TEST_DATABASE_URL` value in `.env`".

### F-P0-03-R2-9 (nit) README 135 says "the one shown below"; the submit output with the ID is above that line (the sample `get` output below also shows it). Say "the one shown here".

### F-P0-03-R2-10 (nit) README 20 lists `TIMED_OUT` as an attempt state; `docs/state-machine.md` lines 81 and 153 say it is reserved and not written in V1. Say "(`TIMED_OUT` is reserved and not written in V1)".

## 4. Handoff truthfulness, scope and diff

- Executed versus not executed: PostgreSQL path labelled "designed, not executed; checked by code reading only" (true, matches my review: I also did not run it); `make test-db` "Not run" (true, no persistence code touched); quickstart "executed" (true in substance, but see R2-7). I found no executed claim I could disprove.
- Scope (executed, ev 01): `git diff --name-only origin/main...HEAD` = `README.md`, `docs/agents/handoffs/P0-03.md`, `docs/agents/reviews/P0-03-evidence.txt`, `docs/agents/reviews/P0-03.md`. This is the allowed set (the handoff and the two review files, plus the README). Two-dot `origin/main..HEAD` lists 28 files because it includes main's 4b changes in reverse, not changes on this branch (`git log --oneline origin/main..HEAD` is 5 commits, all docs; no `.go` path is in any of them, ev 20). A dry-run merge (`git merge-tree --write-tree origin/main HEAD`) produced a tree with no conflict (ev 21); `main` did not touch `README.md`, the handoff or the review paths since `e7403a9`.
- Commit hashes in the handoff: correct (ev 20).

## 5. Gates (executed, URL unset, no `.env`, ev 18, 19, 22)

- `gofmt -l .`: empty, rc=0.
- `go vet ./...`: rc=0. `go build ./...`: rc=0.
- `make test-docs`: `ok .../tests/docs 0.553s`, rc=0 (handoff 0.635s: timing only).
- `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...`: rc=0, `PASS 90 SKIP 28 FAIL 0` (top-level `--- PASS/SKIP/FAIL` lines; no `FAIL` package line, no `DATA RACE`). This is the expected count on this older base (merge-base `e7403a9`); `origin/main` has more tests (slice 4b) and I did not run it. The count is not a README claim, so it does not affect the README.

## Areas checked where I found no defect

- All 11 endpoints in README 271-285 against `origin/main:internal/api/server.go:57-71` (ev 04): identical, no renewal route on main.
- Defaults in the config table against `internal/config/config.go` (ev 16): `HTTP_ADDR`, `HEARTBEAT_INTERVAL` 2s, `REAPER_INTERVAL` timeout/2, `DB_MAX_CONNS` 20, `DB_MIN_CONNS` 2, `DB_QUERY_TIMEOUT` 5s, `WORKER_SLOTS` 4, `COORDINATOR_URL`, `WORKER_ID` default `worker_<hostname>_<pid>` (`cmd/worker/main.go:32`): all correct. `WORKER_TIMEOUT >= 3x heartbeat` is enforced (`config.go:81`).
- "Not enforced" claims for `MAX_JOB_RUNTIME`, `LOG_LEVEL`, `DEFAULT_MAX_ATTEMPTS` are true on main too (config and `cmd/` unchanged).
- Cancellation text matches behavior on both bases; worker re-registration on 404 (`client.go:191,222`); no emoji, no badges, no line-number citations.
- State lists, make targets, job_events/results table names: checked above.
- Not tested by me: any PostgreSQL runtime path, `make demo`, `make db-*`, `make test-db`, `docker compose config`, and the code on `origin/main` (read only).

## Tests added
None (docs-only task, no tests proposed).

## Verdict: not approved (narrow); must-fix list
1. **R2-1**: stop sourcing `.env` into the interactive shell; export only `MINICLOUD_DATABASE_URL` (or use a subshell), with the exact wording above.
2. **R2-2**: remove "worker timeout clamp planned for slice 4d, tracked as SEC-012" from README 225 and handoff lines 11 and 113.
3. **R2-3**: replace "slice 4a done" with the PROJECT_STATE link, reword line 26 as "Not wired end to end yet", and fix line 3 "under leases" (they are false or will be stale against `main` `7e58d4a`).
4. **R2-7**: make the handoff say what the quickstart run really was, paste the raw gate counts, state the base commit and the diff scope, and drop the SEC-012 clause.

Should-fix: R2-4, R2-5, R2-6. Nits: R2-8, R2-9, R2-10. After the four must-fix edits I would approve; no re-run of the quickstart is needed unless the Quick Start commands change.
