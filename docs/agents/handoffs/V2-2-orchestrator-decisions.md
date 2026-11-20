ADR APPROVED. Start Stage B of V2-2 on branch agent/claude/V2-2 (tip dc2e0b6). Follow ADR-0021 as written, with these decisions (orchestrator decisions; the owner can overrule). Set ADR-0021 to Accepted with the date and mark these as Decided.

Q1 Both modes. --start is on by default. mcbench never runs migrations: if the allowed database is not migrated, stop with a clear message ("run make db-up / the migrator for your own database first").
Q2 Use the pgx driver that is already in go.mod (no change to go.mod or go.sum) for one read-only query, SHOW server_version, over the same database URL. No docker exec, no psql. Amend the ADR wording "stdlib only" to "stdlib plus the pgx dependency already in go.mod".
Q3 Use the attempt fields started_at and finished_at that the attempts API returns (domain.Attempt, internal/domain/attempt.go). BEFORE you rely on them, check the real response of GET /v1/jobs/{id}/attempts in a real run and show it. S3 duration = finished_at minus started_at (one coordinator clock, no skew); label it "claim to commit, includes result delivery". If the endpoint does not return both, fall back to claim-response to terminal time, label it, and do NOT change production code.
Q4 Placeholders accepted: S3 N=5, S2 200 polls, both flags and both recorded. Not the V2-3 value (OD-22 stays with the owner).
Q5 --max-load default 1.0 (absolute 1-minute load average), recorded; an override is allowed, recorded and marks the result idle_check: overridden.
Q6 Yes. A result file under docs/benchmarks/ needs a "kept by orchestrator decision" line in docs/agents/handoffs/V2-2.md. Keep ONE real run per scenario (S1..S4), no more.
Q7 Yes. Only 127.0.0.1:5433 (the .env.example default). Refuse any other database host.

Extra rules:
- A benchmark result on this machine is a baseline of THIS machine and these settings. It is not a performance claim; say so in the README and in each markdown summary.
- Do not run the real benchmark runs while any other test process is using this machine. The idle check must pass without override; if it does not, stop and tell the owner to close other work, do not raise --max-load.
- Copy this file verbatim to docs/agents/handoffs/V2-2-orchestrator-decisions.md and cite it from the ADR and TASKS (do not cite it with a path pattern the docs check rejects; run make test-docs).

USAGE (Claude is at peak hours, keep it small):
- Do not re-read ADR-0017/0019/0020 or whole files; read only what you edit or call.
- While building run only targeted tests for the package you touched (go test -run ... -count=1); run -race only on the new tests.
- Run the full gates ONCE at the end: gofmt -l ., go vet ./..., go build ./..., make test-docs, unset suite, make test-race, make test-db, make test-security (own database only). Never run make test-outage.
- The four real runs (S1..S4) are required once each; report the tables from the real runs only, with the command.
- Commit in small steps and push after each stage so a usage cutoff loses nothing. If you hit a limit, write what is done and what remains to reports/claude/V2-2-progress.md before stopping.

Report in chat under 15 lines: tip, gate counts (top-level ^--- PASS/SKIP/FAIL counts), the four result tables, what the attempts API really returned for Q3, any ADR deviation.
