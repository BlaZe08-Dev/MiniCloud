# AGENTS.md — rules for every coding agent on MiniCloud

This is the single source of rules for all agents (Claude Code, Gemini, any other).
`CLAUDE.md` and `GEMINI.md` only point here. If anything elsewhere conflicts with this
file, this file wins, except `docs/source/MINICLOUD_SECURITY_INSTRUCTIONS.md`, which
always applies in full.

## 1. Project

MiniCloud is a solo distributed job-execution platform in Go (stdlib plus pgx v5),
with PostgreSQL 16 as the durable source of truth. A coordinator accepts jobs, workers
pull and execute them under leases, and every state change is recorded in the database.
It is learning-oriented but engineering-serious: the goal is to understand, measure and
implement real distributed-systems behaviour (correct state transitions, worker
coordination, leases, retries, failure recovery).

Roadmap (strict boundaries; see `docs/roadmap.md` and ADR-0004):
- **V1 (Phases 0–4)**: core execution with in-process allowlisted tasks, pull dispatch
  baseline, PostgreSQL persistence, leases, bounded retries, cancellation, tested
  failure recovery. Includes assignment-based round-robin before V1 is complete.
- **V2 (Phase 6 + transport)**: resource-aware scheduling, more policies, benchmark
  generator, fault-injection harness, Redis Streams dispatch evaluated against the V1
  Postgres baseline with reproducible benchmarks (ADR-0006). Postgres stays the source
  of truth; Redis is dispatch only.
- **V3 (Docker + Phases 7–8)**: Docker task isolation behind the executor interface
  (ADR-0005), multi-machine, worker auth/TLS, hardening. Never run untrusted task
  payloads directly on the host, at any stage, including tests.

Build incrementally on the same codebase. Do not restart, overwrite or regenerate
working code. Major changes are designed and reviewed before they are built.

### Invariants (never break; code and tests enforce them)
1. Stable job ID; each execution gets a distinct attempt ID and a monotonically
   increasing attempt number.
2. State transitions are validated centrally by the `internal/domain` state machine and
   rejected with typed errors when invalid.
3. A worker can commit results only for its active attempt and lease; late or stale
   results are rejected.
4. Execution is bounded: maximum runtime via context, payload 64 KiB, output 64 KiB,
   bounded retry attempts (exact limits: `docs/security/field-limits.md`).
5. Semantics are at-least-once under retries. Never claim exactly-once execution for
   arbitrary external side effects.
6. Lease expiry means the coordinator no longer trusts the worker, not proof that the
   worker has stopped.
7. Only allowlisted tasks run until V3 Docker isolation; never execute arbitrary shell
   commands on the host.

### Engineering rules
- Design for extension, but do not implement future-version features early. Version
  gating follows ADR-0004.
- Keep boundaries strict: domain, coordinator/API, scheduling, worker communication,
  storage, executor and observability stay isolated (`tests/architecture` guards
  this). Models and contracts exist now; behaviour arrives per phase.
- Keep components replaceable: scheduler, queue transport and executor sit behind
  interfaces with no in-process assumptions.
- Compatibility: additive-only changes within the `/v1` API; forward-only migrations
  (ADR-0010); tests protect architectural invariants.
- No new dependency without explicit owner approval and an ADR (pgx was approved in
  ADR-0009; Redis client is a V2 decision).
- No Kubernetes, consensus/Raft/Paxos, AI scheduling, web UI/dashboards or
  multi-tenancy without demonstrated need.
- Before a V2/V3 feature, write up the problem, components and compatibility impact,
  and justify the change with concrete data.
- Inspect before modifying: check what already exists, verify which items are already
  satisfied, and explain the delta.
- Prefer table-driven tests, run with the race detector, return explicit errors.

## 2. Security instructions

Follow `docs/source/MINICLOUD_SECURITY_INSTRUCTIONS.md` at all times. In short: act as a
security engineer and adversarial tester, break before fix (report a finding with
evidence before fixing it), and never invent test, benchmark or load results.
Workflow detail: `docs/security/security-testing.md` and ADR-0012.

- Break before fix: understand, threat-model, attack, observe, report, recommend, fix
  (only when authorised), retest.
- No silent fixes: disclose every flaw and test failure you find, with reproduction
  steps.
- Never weaken tests: never delete tests, loosen bounds or weaken assertions to make a
  suite pass.
- Never claim a test passes or that coverage exists without running it, plus a mutation
  proof for new guards (SEC-005).
- Accepted V1 gap: HTTP API and worker endpoints are unauthenticated (SEC-004),
  mitigated by the `127.0.0.1` bind default. Do not report it again as new; do not
  widen the bind.

Evidence labels, used in every report and in `docs/security/findings.md`:
- **executed**: you ran it and pasted the real output.
- **designed, not executed**: written or planned, not run.
- **confirmed by code reading**: verified by reading the cited lines, not run.
- **inferred**: reasoning only.

## 3. Hard rules

1. Work only in your own worktree and branch, and only on files your task owns in
   `docs/agents/TASKS.md`. Need another file? Stop and ask in your handoff.
2. Never edit `main`, never merge, never force-push, never rewrite shared history,
   never delete another agent's branch.
3. Never run untrusted task payloads directly on the host.
4. Postgres is the source of truth. Redis, if used, is dispatch only.
5. Never claim a test, benchmark or load result you did not run. Redirect output to a
   file and paste it verbatim; never retype or summarize command output.
6. Never cite a test name, file path or line number you have not verified. Check test
   names with `go test -list '.*' ./<pkg>/`. Label future tests "(planned test)" in
   their own clause.
7. If the design doc is ambiguous or wrong, stop and write the question in your
   handoff. Do not guess.
8. Small Conventional Commits, staged by explicit path (no `git add -A` / `git add .`).
   Push your branch regularly.
9. Never commit secrets, tokens or `.env`.
10. Never run destructive commands (dropping shared databases, deleting volumes,
    `make db-down`, `git reset --hard` on shared refs) without the owner's approval.

## 4. Context map (read what your task needs)

| Need | Read |
|---|---|
| Current status, what is done | `docs/PROJECT_STATE.md`, `CHANGELOG.md` |
| Phases and version boundaries | `docs/roadmap.md`, ADR-0004 |
| Full project spec (source) | `docs/source/MiniCloud_Project_Blueprint.pdf`, `docs/source/MINICLOUD_INSTRUCTIONS.md` |
| Components and data flow | `docs/architecture.md` |
| Job and attempt states | `docs/state-machine.md` |
| Failure handling | `docs/failure-model.md` (fault-injection plan: arrives with P4-00, not on `main` yet) |
| HTTP protocol, payload limits, status codes | `docs/protocol.md` |
| Database schema and locking | `docs/schema.md` (lock order: jobs, workers, attempts, results, job_events) |
| Local Postgres, test guardrails | `docs/testing-postgres.md`, section 9 below |
| Decisions | `docs/adr/` (0001–0013 on `main`; the Phase 4 lease, retry and cancellation ADRs arrive with P4-00) |
| Phase 4 plan and slices | arrives with P4-00, not on `main` yet |
| Security | `docs/security/` (threat model, findings register, field limits, test plan, testing workflow), ADR-0012 |
| Task board and ownership | `docs/agents/TASKS.md` |

The design docs above are the spec. Code that disagrees with them is a finding, not a
licence to change the docs silently.

## 5. Environment

- Worktrees: `~/MiniCloud` (main only, merges by the owner), `~/MiniCloud-claude`,
  `~/MiniCloud-gemini`. All share one `.git`.
- PostgreSQL runs in one Docker Compose container on `127.0.0.1:5433`. Start it only
  from `~/MiniCloud` (`make db-up`). Starting it from another worktree creates a second
  compose project that fights for the same port.
- Each worktree has its own gitignored `.env`, and its own test database
  (`minicloud_claude_test`, `minicloud_gemini_test`) so concurrent test runs never
  touch each other's data. The exact commands are in section 9.
- Docker is a snap install and cannot read `/tmp`. Throwaway worktrees (mutation
  tests) go under `~/mc-mut-<agent>`, never `/tmp`. Copy `.env` in and remove the
  worktree afterwards.
- Database tests skip when `MINICLOUD_TEST_DATABASE_URL` is unset. A skipped test is
  not a passed test: report PASS, SKIP and FAIL counts separately.

## 6. Quality gates (every task, before the handoff, and again before merge)

```bash
test -z "$(gofmt -l .)" || { gofmt -l .; echo "GOFMT FAILED"; }
go vet ./...
go build ./...
make test-docs
env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=1 ./... > /tmp/gate-unset.log 2>&1; tail -5 /tmp/gate-unset.log
make test-db > /tmp/gate-db.log 2>&1; tail -5 /tmp/gate-db.log   # when the task touches persistence
```

Report counts with:
```bash
go test -v -race -count=1 ./... > /tmp/gate-v.log 2>&1
echo "PASS $(grep -c '^--- PASS' /tmp/gate-v.log) SKIP $(grep -c '^--- SKIP' /tmp/gate-v.log) FAIL $(grep -c '^--- FAIL' /tmp/gate-v.log)"
```

Other targets: `make fmt` (`gofmt -s -w .`), `make build` (coordinator, worker, mc,
migrate into `bin/`), `make test-race`, `make demo` (end-to-end local demo).

Benchmarks only when the task asks, with real numbers, the command, Go version, CPU and
RAM. Every new test that guards a property needs a mutation proof: break the code in a
throwaway worktree, show the test fails, restore, show it passes.

## 7. Task lifecycle

### Who builds what
- **Claude Code builds complex and hard tasks**: state machines, concurrency, locking
  and transactions, migrations and repository code, lease/retry/cancellation logic,
  recovery paths, schedulers, and anything touching shared touch points.
- **Gemini builds the other tasks**: small isolated packages, validation and limits,
  CLI and config plumbing, docs updates, extra test cases for an already-designed
  behavior.
- The agent that did not build a task audits it. Every task has exactly one builder.
- The Auditor's claims are re-run like the Builder's: a review finding without real
  output counts as unverified.

0. **Start**: read this file, `docs/PROJECT_STATE.md` and your task entry; load other
   docs on demand via the context map. Run the database preflight from section 9 and
   stop if it fails. Run `git status`, `git log --oneline -10`, `go version`,
   `go build ./...` and the gates, and report the real results. If the next step
   changes architecture, stop and ask before building.
1. **Spec**: the orchestrator writes the task in `docs/agents/TASKS.md` (goal,
   acceptance criteria, owned files, out of scope, builder, auditor, migration number
   if any).
2. **Build**: the Builder works on `agent/<builder>/<task-id>`, commits small, runs the
   gates, writes `docs/agents/handoffs/<task-id>.md`, commits, pushes, stops.
3. **Audit**: the Auditor checks out the Builder's branch read-only, reviews the diff
   against the spec and design doc, tries to break it, writes
   `docs/agents/reviews/<task-id>.md` on `agent/<auditor>/<task-id>-review`. It may add
   tests there (based on the Builder's branch) but never changes production code.
4. **Fix**: the Builder fixes each finding with its own commit, cherry-picks the
   Auditor's accepted tests onto its branch, updates the handoff. Repeat 3–4 until no
   blocker or major finding is open.
5. **Merge**: the owner runs the gates in `~/MiniCloud` and merges the Builder's branch
   (which already carries the accepted tests). `docs/PROJECT_STATE.md` and
   `CHANGELOG.md` are updated at merge by the owner, or by a task that owns them;
   affected design docs, ADRs and security registers are updated by the task that owns
   them.
6. **Sync**: both agents start the next task from the new `main`.

## 8. Report formats

### Handoff (Builder) — `docs/agents/handoffs/<task-id>.md`
```
# Handoff <task-id>
Branch / last commit:
What changed (by package):
Design decisions and deviations from the design doc (with reasons):
Tests added (name, file, what property it guards, mutation proof):
Commands run + real output (gofmt, vet, build, test-docs, test -race counts, test-db):
Known gaps / TODOs:
Open questions for the orchestrator:
Security review (assumptions challenged, attacks tried, not tested, findings with evidence labels):
```

The security review is mandatory. Name the real tests that exercise each attack, list
what was not tested, and give every finding a severity and an evidence label.

### Review (Auditor) — `docs/agents/reviews/<task-id>.md`
```
# Review <task-id>
Commit reviewed:
Verdict: APPROVE | CHANGES REQUIRED | BLOCKED
Handoff claims re-run (each claim, the command, real output, match yes/no):
Findings (each): ID, severity (blocker/major/minor), location, evidence label,
  evidence (repro steps or test + real output), suggested fix
Attacks attempted that did NOT find issues:
Tests added (branch + files + current result):
```

Security findings that affect the system also go in `docs/security/findings.md` with
the next SEC number, via the task that owns that file.

## 9. Per-worktree test database

Verified by task P0-01 (real output in `docs/agents/handoffs/P0-01.md`).

Names must end in `_test`: every Postgres test helper refuses any other database name
with `SAFETY VIOLATION`. The per-worktree databases are therefore
`minicloud_claude_test` (`~/MiniCloud-claude`) and `minicloud_gemini_test`
(`~/MiniCloud-gemini`). `minicloud_test` stays for `~/MiniCloud`.

The container must already be running (started from `~/MiniCloud` with `make db-up`).
The commands below use `docker exec` on the container by name, so they are safe from
any worktree and never start a second compose project. They print no credentials.

**Create both databases (once; idempotent, existing ones are skipped):**
```bash
docker exec -i minicloud-postgres sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres' <<'SQL'
SELECT 'CREATE DATABASE minicloud_claude_test'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'minicloud_claude_test')\gexec
SELECT 'CREATE DATABASE minicloud_gemini_test'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'minicloud_gemini_test')\gexec
SQL
```

**Point this worktree's `.env` at its database** (shown for Claude; Gemini uses
`minicloud_gemini_test`). Copy `.env` from `~/MiniCloud` first if it is missing:
```bash
sed -i -E 's#^(MINICLOUD_TEST_DATABASE_URL=.*/)minicloud_test\?#\1minicloud_claude_test?#' .env
grep -E '^MINICLOUD_TEST_DATABASE_URL=' .env | sed -E 's#//[^@]*@#//***@#'   # check, password redacted
```

**Run the database tests:** `make test-db` (reads the URL from `.env`).

**Confirm which database the tests used** (transaction counters rise on yours only):
```bash
docker exec minicloud-postgres sh -c 'psql -U "$POSTGRES_USER" -d postgres -c "SELECT datname, xact_commit FROM pg_stat_database WHERE datname LIKE '"'"'minicloud%'"'"' ORDER BY 1"'
```

**Preflight** (run at the start of every task, section 7 step 0, from the worktree
root). Prints the test database from `.env` with the password redacted, and fails if
`.env` is missing or an agent worktree still points at the shared `minicloud_test`:
```bash
(
  [ -f .env ] || { echo "STOP: no .env in $(pwd)" >&2; exit 1; }
  URL=$(grep -E '^MINICLOUD_TEST_DATABASE_URL=' .env | cut -d= -f2-)
  DB=$(printf '%s\n' "$URL" | sed -E 's#^.*/([^/?]+)([?].*)?$#\1#')
  echo "worktree: $(git rev-parse --show-toplevel)"
  echo "test URL: $(printf '%s\n' "$URL" | sed -E 's#//[^@]*@#//***@#')"
  echo "test DB:  $DB"
  [ -n "$DB" ] || { echo "STOP: MINICLOUD_TEST_DATABASE_URL not set in .env" >&2; exit 1; }
  if [ "$(git rev-parse --show-toplevel)" != "$HOME/MiniCloud" ] && [ "$DB" = minicloud_test ]; then
    echo "STOP: agent worktree points at the shared minicloud_test; fix .env (section 9)" >&2; exit 1
  fi
  echo "preflight OK"
)
```

**Reset your own database only** (never another agent's; never `make db-reset-test`
from an agent worktree, it resets the shared `minicloud_test`). Step 1 reads the name
from this worktree's `.env`. Step 2 refuses unless the name is `minicloud_<agent>_test`
(lowercase letters and digits for `<agent>`) and not `minicloud_test`. Copying it
from another worktree is therefore safe: it always acts on the database of the `.env`
it runs next to.
```bash
# 1. database name from this worktree's .env
DB=$(grep -E '^MINICLOUD_TEST_DATABASE_URL=' .env | cut -d= -f2- | sed -E 's#^.*/([^/?]+)([?].*)?$#\1#')
# 2. guarded drop and recreate
if [ "$DB" = minicloud_test ] || ! printf '%s\n' "$DB" | grep -Eqx 'minicloud_[a-z0-9]+_test'; then
  echo "REFUSED: '$DB' is not a per-agent test database (minicloud_<agent>_test)" >&2
  false   # exit status 1 without closing an interactive shell
else
  echo "resetting $DB"
  docker exec minicloud-postgres sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres -c "DROP DATABASE IF EXISTS $1" -c "CREATE DATABASE $1"' sh "$DB"
fi
```
The name reaches `psql` only after it matches the pattern above, so it cannot carry
SQL or shell syntax.

Throwaway mutation worktrees (`~/mc-mut-<agent>`) copy the agent's `.env`, so they use
the agent's own database too.

## 10. Disagreements

The design doc wins. If it is silent, the orchestrator decides and records the decision
in the design doc. An unverifiable claim in any report counts as not done.
