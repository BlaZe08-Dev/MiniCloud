# ADR 0021: Benchmark Harness (`cmd/mcbench`)

## Status
Accepted (2026-11-20, Stage B of task V2-2, branch `agent/claude/V2-2`). Proposed in Stage A; approved by the orchestrator ("ADR APPROVED") with the decisions below; the record is [`V2-2-orchestrator-decisions.md`](../agents/handoffs/V2-2-orchestrator-decisions.md). Statements labelled **[Proposed]** below became **[Decided, orchestrator 2026-11-20; the owner can overrule]** with this approval, except where a decision below changes them. Builds on [ADR 0017](0017-assignment-round-robin.md), [ADR 0019](0019-worker-concurrency-semantics.md) and [ADR 0020](0020-round-robin-real-slots.md). Version gate: V2, slice V2-2, before V2-3.

**Decided (orchestrator, 2026-11-20; the owner can overrule):**
- Q1: both modes; `-start` is on by default; `mcbench` never runs migrations and stops with a clear message on an unmigrated database.
- Q2: the pgx driver already in go.mod for one read-only `SHOW server_version` over the same database URL; no docker exec, no psql. "Stdlib only" in section 1 reads "stdlib plus the pgx dependency already in go.mod".
- Q3: S3 uses the attempt fields `started_at` and `finished_at` (checked in a real run, see the V2-2 handoff); duration = `finished_at` minus `started_at`, labelled "claim to commit, includes result delivery".
- Q4: placeholders S3 N=5 and S2 200 polls, both flags and both recorded; not the V2-3 value (OD-22 stays with the owner).
- Q5: `--max-load` default 1.0 (absolute 1-minute load average); an override is recorded and marks `idle_check: overridden`.
- Q6: a result file under `docs/benchmarks/` needs a "kept by orchestrator decision" line in the V2-2 handoff; one real run per scenario.
- Q7: only 127.0.0.1:5433; any other database host is refused.
- A result is a baseline of this machine and these settings, not a performance claim. Real runs are made only on an idle machine; an idle-check failure is never worked around by raising `--max-load`.

Evidence: [`V2-2.md`](../agents/handoffs/V2-2.md).

## Purpose
**[Proposed]** A repeatable, honest way to measure MiniCloud: how fast jobs flow, how long a claim takes, how fast each worker runs a built-in task. No invented numbers. It produces the median-of-N speed inputs for the proposed V2-3 placement score ([`NODE_CAPABILITIES.md`](../post-v1/NODE_CAPABILITIES.md), "Placement algorithm (proposed)"), and gives measured data for SEC-036 (service mutex) and SEC-037 (poll cost, 20 flights per claiming poll) so the owner can decide with data. **[Decided, ADR-0018 / roadmap V2-2]** The harness uses built-in tasks only.

## 1. Shape
- **[Proposed]** A new Go command `cmd/mcbench`, standard library plus the pgx dependency already in go.mod (no change to `go.mod` or `go.sum`).
- **[Proposed]** It starts, or targets, a coordinator and W workers and drives them only over the real HTTP API (`POST /v1/jobs`, `GET /v1/jobs/{id}`, `GET /v1/jobs/{id}/attempts`, `POST /v1/workers/register`, `POST /v1/workers/{id}/poll`, `GET /v1/workers`). "Start" means `exec` of the `coordinator` and `worker` binaries built by `make build` from the same commit; the commit is recorded.
- **[Proposed]** Zero changes to production behaviour: nothing under `internal/` (domain, storage, api, coordinator, worker, executor, config) is edited. The coordinator is configured only through its existing `MINICLOUD_*` variables (for example `MINICLOUD_DISPATCH_POLICY`, `MINICLOUD_WORKER_SLOTS`, `MINICLOUD_DATABASE_URL`).
- **[Proposed]** Every address is `127.0.0.1`; `mcbench` refuses a coordinator URL whose host is not a loopback address.
- Anything that would need a production change is written as an open question (section 8), not done.

## 2. Scenarios (built-in tasks only: `echo`, `sleep`, `sha256_loop`)
All **[Proposed]**. Each scenario prints its parameters and the discarded warm-up result separately from the recorded runs.

| ID | Name | Parameters | Recorded |
|---|---|---|---|
| S1 | Throughput | M jobs, W workers, S slots per worker, policy `round_robin` or `fifo_pull`, task (default `sleep` with a short fixed duration) | wall time (first submit to last terminal state), jobs per second, max concurrent attempts observed |
| S2 | Claim cost | many polls against a running coordinator; also job submissions | latency of `PollWork` for (a) an idle poll (204) and (b) a poll that returns a job (200), and of `POST /v1/jobs`; each as min, median, p95, max and count |
| S3 | Worker speed | `sha256_loop` with a fixed `iterations` (default well below `MaxSHA256Iterations` = 1,000,000), N repeats per worker | every run's duration, the median, min, p95, max, N |
| S4 | Burst | B jobs submitted at once into an idle cluster | time to first claim, time to drain (last terminal state) |

- **[Proposed]** S2 measures client-observed HTTP latency from `127.0.0.1` (includes the database path). The result states that it is end-to-end, not the in-coordinator cost. It reports the observed latency next to SEC-037's recorded flight counts (20, 4, 5) without asserting a per-flight cost it did not measure.
- **[Proposed]** S1 "max concurrent attempts" is the highest number of jobs seen `RUNNING` at one sampling instant (poll of `GET /v1/jobs`/`GET /v1/jobs/{id}`); it is a lower bound on the true peak and the result says so.
- **[Proposed]** S3 per-run duration is taken from the best timing the API exposes for the attempt; if the API does not expose a start and finish time the harness reports claim-response to job-terminal time and labels it (open question Q3). The final N is **not** decided here (**[Decided, OD-22 unanswered]**: the owner decides after seeing the data); N is a flag with a stated default (**[Proposed]** default 5, a placeholder for running the harness, not the V2-3 value).
- S1 under `round_robin` and `fifo_pull` is also the data source for the V2-0 audit observation O-1 (assignment-pass overhead); the harness reports it, it does not interpret it.

## 3. Method rules (all **[Proposed]**)
1. **Warm-up:** each scenario runs one warm-up pass first; it is discarded from statistics, printed, and labelled `warmup` in the JSON.
2. **Every repeat recorded:** the raw per-run values are in the JSON; summaries add min, median, p95, max and N. Outliers are never trimmed or averaged away; no mean is the headline.
3. **Percentile definition:** nearest-rank on the sorted sample, stated in the report; for N below 20 p95 equals max and the report says so.
4. **Idle machine:** `mcbench` reads the 1-minute load average (`/proc/loadavg`) and aborts with a clear message when it exceeds `--max-load` (default **[Proposed]** 1.0, an absolute 1-minute load average; see Q5). The check runs before the warm-up and the value is recorded. `--max-load` can be raised by the operator; the raised value is recorded and the result is marked `idle_check: overridden`.
5. **Database guard:** reusing the existing `_test` name-suffix guard idea (`isTestDatabaseName`, `internal/storage/postgres/dbname_guard_test.go`), `mcbench` parses the database name from its database URL and **refuses** any name that is not exactly the agent's own (`--allowed-db`, required, no default; Stage B's `make bench` passes `minicloud_claude_test`). It also refuses `minicloud_test` and any name not ending in `_test`, always, even if `--allowed-db` names it. It never connects before the check passes. It never touches `minicloud_test` or `minicloud-postgres` outside the allowed database.
6. **Clean state:** each run uses jobs and workers it created itself, with a run-unique worker ID prefix, and reports leftovers; it does not delete rows outside its own run (no schema changes, no migrations).

## 4. Environment record (**[Proposed]**, mandatory)
Written with every result. A result without a complete record is invalid, and `mcbench` refuses to write a result file if any field cannot be determined (it writes `unknown` only where stated below and marks the result `invalid_env: true`).

Fields: git commit (and a `dirty` flag), Go version, OS and kernel (`uname`), CPU model and core count, RAM total, PostgreSQL version (`SHOW server_version`, obtained read-only through the coordinator's database URL or reported as targeted), dispatch policy, slots per worker, worker count, N, load average before and after (1, 5, 15 minute), start time and end time (UTC). Timestamps come from the real clock only in the command; the statistics and formatting code takes injected values (section 6).

## 5. Output (**[Proposed]**)
- One JSON file and one short markdown summary per run, named `<scenario>-<UTC date>-<short commit>.json|md`, written under `docs/benchmarks/` only for runs the owner chooses to keep; otherwise `--out` points elsewhere (default: a temporary directory printed at the end).
- The JSON carries: schema version, scenario parameters, the environment record, warm-up runs (labelled), every recorded run, the summaries, and the field `"source": "real run"` plus the exact command line.
- **[Proposed]** Stage B commits no invented or example numbers. Committed result files come from a real run on this machine and say so in the file and in the benchmarks README (to be added in Stage B).

## 6. Test strategy (**[Proposed]**)
- Unit tests, deterministic, no sleeps on the real clock (injected clock and injected load-average reader): percentiles with known inputs (including N=1, N=2, ties, unsorted input, N<20 p95 rule); report format (JSON round trip, markdown rows, the invalid-record refusal); the idle check (below, at and above the threshold, unreadable `/proc/loadavg`); the database guard (own name accepted; `minicloud_test`, `minicloud`, `postgres`, empty, names without `_test`, a URL that does not parse are all refused; the check happens before any connect); the loopback-only URL check.
- A short smoke test: one tiny scenario (a few `echo` jobs) against the in-memory repository in-process or a fake HTTP server; it must finish well under a second without real-clock sleeps (poll by event, not by sleep).
- **Benchmarks are not part of `go test` or any gate.** `make bench` is added (own database only, documented in the benchmarks README (to be added in Stage B)); `make test`, `test-race`, `test-docs`, `test-db`, `test-security`, `test-outage` are unchanged. Real-run `Test...` names cited by Stage B must exist; none are cited in this ADR.

## 7. Out of scope (**[Decided, task V2-2 scope]**)
Changing dispatch behaviour; the SEC-036 and SEC-037 fixes (the harness only measures); new migrations (none in this task; 000007 stays reserved for V2-1); Docker; authentication; Redis; any network outside `127.0.0.1`; `internal/` production changes; `tests/docs/citation_allowlist.txt`.

## 8. Open questions
All seven (Q1 to Q7) were answered by the orchestrator; see Status.
