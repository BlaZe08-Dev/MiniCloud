# MiniCloud benchmarks (V2-2, ADR-0021)

`cmd/mcbench` measures MiniCloud over its real HTTP API on 127.0.0.1. Nothing here is a performance claim: a result is a baseline of **this machine and these settings** and nothing more. It is the input the owner uses to decide OD-22 (the value of N for the proposed V2-3 placement score) and to weigh SEC-036 and SEC-037 with data.

## How to run
1. `make db-up`, then migrate your own database with the migrator (`MINICLOUD_DATABASE_URL=<own url> go run ./cmd/migrate`). `mcbench` never runs migrations; on an unmigrated database the coordinator refuses to start and `mcbench` says so.
2. Close other work. `mcbench` aborts when the 1-minute load average exceeds `-max-load` (default 1.0). Do not raise it for a result you intend to keep; an override is recorded and marks the result `idle_check: overridden`.
3. `make bench SCENARIO=s1 BENCH_OUT=docs/benchmarks` (scenarios `s1` to `s4`). Extra flags go in `BENCH_ARGS`, for example `BENCH_ARGS="-policy fifo_pull -workers 3 -slots 2"`. `make bench` reads `MINICLOUD_TEST_DATABASE_URL` from `.env` and allows only `BENCH_ALLOWED_DB` (default `minicloud_claude_test`) on `127.0.0.1:5433`; `minicloud_test` is always refused. Write results to a directory outside the repository first, so the recorded `dirty` flag stays false, then copy kept files here.
4. The coordinator, PostgreSQL, the workers and `mcbench` all run on the same machine and compete for CPU and memory. Results are noisy and are not comparable across machines. `cpu_cores` in the record means logical cores (the i3-7020U has 2 physical cores and 4 threads).
5. The idle check reads only the 1-minute load average. Also wait until the 5-minute average is below 1.0 before you start.
6. `make bench` builds the binaries first, and compiling raises the load, so the idle check may refuse. Run `make build` first, wait for the load to settle, then run `make bench`.
7. The database URL may carry only the `sslmode` query key, and `sslmode=disable` must be spelled in lower case; the guard refuses every other key and any URL that pgx would resolve to another host, port or database.
8. Benchmarks are not part of `go test` or any gate. Keep a result in this directory only with a "kept by orchestrator decision" line in `docs/agents/handoffs/V2-2.md`.

## Scenarios
- **s1 throughput:** M jobs (`-m`), W workers (`-workers`), S slots (`-slots`), policy (`-policy`); wall time, jobs per second, max concurrent attempts (a lower bound from sampling).
- **s2 claim cost:** latency of `PollWork` (idle 204 and claiming 200) and of job submission, client-observed, with a probe worker registered over HTTP and no other worker running. Placeholder sample size `-polls 200`, `-claims 200`.
- **s3 worker speed:** `sha256_loop` with fixed `-iterations`, `-n` repeats per worker (placeholder default 5, not the V2-3 value). Duration is attempt `finished_at` minus `started_at` on one coordinator clock: claim to commit, includes result delivery.
- **s4 burst:** B jobs (`-b`) submitted at once into an idle cluster; time to first claim (earliest attempt `started_at` minus the first submit) and time to drain.

## How to read a result
Each run writes `<scenario>-<UTC date>-<short commit>.json` and `.md`. The JSON holds the command, parameters, the environment record, every warm-up value (labelled `warmup_discarded` and excluded from statistics), every recorded value and the statistics. Percentiles are nearest-rank; for N below 20 the p95 equals the maximum and the table says so. A series with N=0 shows `n/a` in the table and its JSON `stats` object holds only `"n": 0`. Nothing is trimmed and no mean is reported. A result whose environment record is incomplete is refused (`invalid`) and no file is written.

## Kept results
The files in this directory come from real runs on the machine described in each file's environment record (commit `deaa293`, one run per scenario). They are not examples.
