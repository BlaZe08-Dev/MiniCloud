Task: V2-1d-AUDIT
Branch: agent/gemini/V2-1d-audit
Work tip (last commit before this report): 590b363
Date: 2026-11-27

# V2-1d Independent Audit Report

Logs: `~/gates-v21d-audit/` (`goversion-*.log`, `gate.log`, `unset-nonrace.log`, `db.log`, `sec.log`, `worker-race50.log`, `worker-race50-node.log`, `mutants.log`, `audit-probes.log`, `audit-probes-p3f-p4.log`).

## 1. Verdict

**Verdict: merge as is** (executed + read).

Reasons:
- All orchestrator decisions D1–D12 and ADR-0022 Sections 4.2, 4.3, 4.5, and 10 are implemented accurately in `internal/worker/node.go`, `internal/worker/client.go`, and `internal/config/config.go` (read + executed).
- Fix round 1 (F-01) is verified: `internal/worker/node.go:250` (`regErr != nil || n.a.nodeAttached.Load() == nil`) detaches and retries on any in-loop worker registration failure, confirmed by `TestWorker_InLoopWorkerRegisterFailure_Retried` and mutant (g) (executed).
- All gates pass on `GOTOOLCHAIN=go1.22.2`: `make gate` 574/136/0 (0 data races), non-race 574/136/0, `make test-db` 212/0/0, `make test-security` 80/0/0, and `-race -count=50 -run '^TestWorker_' ./internal/worker` passes in 111.656s with 0 data races (executed).
- Forbidden paths (`internal/storage/postgres/migrations/`, `go.mod`, `go.sum`, `internal/coordinator`, `internal/api`, `internal/storage`, `internal/domain`, `internal/platform`, `README.md`, `docs/adr/0022-node-capability-model.md`, `docs/PROJECT_STATE.md`, `CHANGELOG.md`) have empty `git diff origin/main` (executed).
- Remaining findings are minor test gaps (disclosed by the builder) and minor documentation/edge-case notes; none are blockers or majors.

## 2. Findings Table

| ID | Severity | Location | What is wrong | Impact | Evidence | Recommended fix |
|---|---|---|---|---|---|---|
| F-AUDIT-1 | Minor | `internal/worker/node_test.go`, `internal/worker/node.go:125,131,150`, `internal/worker/client.go:392` | Four mutants survived the `TestWorker_*` suite: (c) `safeCode` returning input unchanged, (d) removing the 64 KiB `io.LimitReader`, (e) removing the 1 s `Retry-After` floor (`"0"`), and (b) replacing `CompareAndSwap` with unconditional `Store(nil)` in `register()`. | Regression in `safeCode`, body size cap, `Retry-After: 0` floor, or concurrent D5 CAS guard would not be caught by existing unit tests. | executed (`~/gates-v21d-audit/mutants.log`: `MUTANT_b_RC=0`, `MUTANT_c_RC=0`, `MUTANT_d_RC=0`, `MUTANT_e_RC=0`) | Add unit tests for `Retry-After: 0` -> `1s`, hostile error codes in `slog`, and >64 KiB response bodies (optional follow-up). |
| F-AUDIT-2 | Minor | `internal/worker/node.go:221-236` | `apply()` stops `regT` and `resendT` on `outAttached`/`outRateLimited` but does not stop `telT`. If `register()` detached via `CompareAndSwap` while `telT` was armed, the old `telT` can fire early after re-attach. | One extra telemetry send before a full `interval` elapses after a D5 fallback re-attach. | read (`internal/worker/node.go:221-236`, `client.go:392`) | Call `n.stop(&n.telT)` in `apply()` when `!wasAttached`. |
| F-AUDIT-3 | Info | `docs/protocol.md:628,632` | Section 7.6 does not mention the `1 s` floor on `Retry-After: 0` (`node.go:31,150`) or that the first telemetry report is sent immediately on attach (`node.go:236`) rather than waiting one `interval`. | Minor doc-vs-code detail gap. | read (`docs/protocol.md:628,632`, `node.go:31,150,236`) | Clarify both in a future docs pass if desired. |
| F-AUDIT-4 | Info | `internal/config/config.go:356-361` | `MINICLOUD_NODE_TELEMETRY_INTERVAL=1ns` is accepted without a lower bound (OD-23 open). | If misconfigured to `1ns`, `sendTelemetry` runs in a tight loop saturating the node goroutine and coordinator telemetry endpoint. | executed (`TestAudit_Point7_ConfigMatrix`) | Set lower/upper bounds once OD-23 is decided. |
| F-AUDIT-5 | Info | `internal/worker` | Running the entire `./internal/worker` package with `-race -count=50` hit Go's default 10m timeout (`600.026s`) due to pre-existing real-time sleep tests in `client_test.go`; `-run '^TestWorker_' -race -count=50` passed in `111.656s`. | Test runtime only; no race or failure in node tests. | executed (`worker-race50.log`, `worker-race50-node.log`) | Use `-timeout 25m` if running full `./internal/worker` 50x under `-race`. |

## 3. Go Version Check & Audit Point Tables

### Go Version Matrix (Point 0)

Tested on `origin/main` (`52c5cce`, throwaway worktree `~/mc-mut-gemini-v21d-main`, removed) and `agent/gemini/V2-1d-audit` (`590b363`):

| Test | `origin/main` default (`go1.27.4`) | `origin/main` `go1.22.2` | Branch default (`go1.27.4`) | Branch `go1.22.2` |
|---|---|---|---|---|
| `TestAPI_PollPayloadIsNotHTMLEscaped` | FAIL | PASS | FAIL | PASS |
| `TestAbuse_PathParameters` | FAIL | PASS | FAIL | PASS |
| `TestAbuse_HeaderAbuse` | FAIL | PASS | FAIL | PASS |

Evidence: executed (`goversion-main-default.log`, `goversion-main-1222.log`, `goversion-branch-default.log`, `goversion-branch-1222.log`). Builder's claim that all three pre-date V2-1d and pass on `go1.22.2` is confirmed.

### Point 1: Consent (ADR 4.5, D10)

Executed `TestAudit_Point1_Consent` with reporting off and `MINICLOUD_NODE_TELEMETRY_INTERVAL=garbage` across: (a) non-existing path, (b) existing directory mode `000`, (c) relative path, (d) path under `HOME` mode `000`. All 4 started, registered with byte-identical legacy body (`{"available_slots":1,"executes_concurrently":true,"id":"w1","supported_tasks":["echo","sleep","sha256_loop"],"total_slots":1,"version":"v0.1.0"}`, matching `origin/main:internal/worker/client.go:353-363`), ran an `echo` job, sent 0 node requests, created nothing, and logged exactly 1 `WARN` naming both settings with no path (executed).

### Point 2: State Machine vs D3 & D6

| Outcome | D Row | Code (`file:line`) | Matches |
|---|---|---|---|
| `POST /v1/nodes/register` 200 | D3 | `node.go:178-179`, `apply` `node.go:221-254` | Yes (read) |
| `POST /v1/nodes/register` 429 `RATE_LIMITED` | D3 | `node.go:180-181`, `parseRetryAfter` `node.go:145-151`, `apply` `node.go:226-228` | Yes (read) |
| `POST /v1/nodes/register` 404 / 400 / 429 `NODE_LIMIT_REACHED` | D3 | `node.go:182-185`, `apply` `node.go:255-265` | Yes (read) |
| `POST /v1/nodes/register` 5xx / transport / other | D3 | `node.go:156-188`, `apply` `node.go:266-274` | Yes (read) |
| `POST /v1/workers/register` 400 with `node_id` | D5 | `client.go:389-402`, `loop` `node.go:323-326`, `apply` `node.go:240-253` | Yes (read) |
| Telemetry 200 | D6 | `sendTelemetry` `node.go:387-388` | Yes (read) |
| Telemetry 409 `TELEMETRY_OUT_OF_ORDER` | D6 | `sendTelemetry` `node.go:389-393` | Yes (read) |
| Telemetry 404 | D6 | `sendTelemetry` `node.go:394-396`, `loop` `node.go:294-299` | Yes (read) |
| Telemetry 400 | D6 | `sendTelemetry` `node.go:397-398` | Yes (read) |
| Telemetry 5xx / transport / timeout | D6 | `sendTelemetry` `node.go:378-384,399-402`, `telFailed` `node.go:406-409` | Yes (read) |
| `nodeRun.loop` timer/wake/ctx dispatch | D8 | `node.go:280-329` | Yes (read) |

Scripted fake coordinator sequence (`TestAudit_Point2_StateMachineSequence`, executed):
- Flow: `[POST /v1/nodes/register, POST /v1/workers/register, POST /v1/nodes/register, POST /v1/workers/register, POST /telemetry, POST /v1/nodes/register, POST /telemetry, POST /telemetry, POST /telemetry, POST /telemetry, POST /v1/nodes/register, POST /v1/workers/register, POST /telemetry]`
- Telemetry seq/session: `[#0:seq=1,sess=35a86c09 #1:seq=2,sess=35a86c09 #2:seq=1,sess=58b37d6a #3:seq=2,sess=58b37d6a #4:seq=3,sess=58b37d6a #5:seq=4,sess=58b37d6a]`

### Point 3: Hostile or Broken Coordinator (1h Fake Time)

| Case | Result (1h fake time / probe) | Spins / blocks / OOM? | Evidence |
|---|---|---|---|
| (a) worker register 400 w/ `node_id`, node register 200 | `node_reg=36`, `worker_reg=72` | No (bounded by backoff) | executed (`TestAudit_Point3_HostileCoordinator/a_worker_register_400_node_200`) |
| (b) node register always 500 | `node_reg=61`, `worker_reg=1` | No | executed (`TestAudit_Point3_HostileCoordinator/b_node_register_500`) |
| (c) telemetry always 409 | `telemetry=61` | No | executed (`TestAudit_Point3_HostileCoordinator/c_telemetry_409`) |
| (d) telemetry always 500 (5s interval) | `telemetry=60`, `timers=60` (all >= 5s) | No | executed (`TestAudit_Point3_HostileCoordinator/d_telemetry_500`) |
| (e) `Retry-After`: `"0"`, `"-1"`, `"abc"`, `"99999999999999"`, HTTP date | `1s`, `1m0s`, `1m0s`, `24h0m0s`, `1m0s` | No | executed (`TestAudit_Point3_HostileCoordinator/e_retry_after_values`) |
| (f) 10 MB response body on node routes | Survived cleanly (`io.LimitReader(resp.Body, 64<<10)` at `node.go:125`) | No | executed + read (`TestAudit_Point3_HostileCoordinator/f_10MB_response_body`) |
| (g) Hostile error codes (`"BAD\x00\nCODE"`, ANSI, 100 KB) | Filtered to `""` by `safeCode`, nothing leaked in `slog` | No | executed (`TestAudit_Point3_HostileCoordinator/g_hostile_error_codes`) |

### Points 4, 5, 6: Concurrency, Real Coordinator, Leak Scan

- **Point 4 (Concurrency):** `TestAudit_Point4_Concurrency` (simultaneous heartbeat 404 + telemetry 404, plus `ctx` cancel while telemetry in-flight and while 429 timer pending) passed 20/20 with `-race` and 0 goroutine leaks (`audit-probes-p3f-p4.log`, executed). `TestWorker_*` passed 50/50 with `-race` (`worker-race50-node.log`, executed).
- **Point 5 (Real coordinator):** `TestAudit_Point5_CoordinatorRestart_InMemory` and `TestAudit_Point5_CoordinatorRestart_Postgres` (on `minicloud_gemini_test`) both passed: worker re-registered node and self after coordinator restart and resumed telemetry (`audit-probes.log`, executed). `WithLocalOnly(false)` verified via `TestNodeReporting_LocalOnlyAttachesAndLegacyWithout` (executed).
- **Point 6 (Leak scan):** `TestAudit_Point6_LeakScan` passed across 200, 429, 409, 404, 400, 500: zero hardware values, telemetry values, `session_id`, or state dir path in logs; node log lines carry only `node_id`, `status`, `code`, and `worker_id` (executed).

### Point 7: Config Matrix

| Variable | Input | Result | Matches task file? |
|---|---|---|---|
| `MINICLOUD_NODE_REPORTING_ENABLED` | `""` | `false` | Yes (executed) |
| `MINICLOUD_NODE_REPORTING_ENABLED` | `"true"`, `"TRUE"`, `"True"` | `true` | Yes (executed) |
| `MINICLOUD_NODE_REPORTING_ENABLED` | `"false"` | `false` | Yes (executed) |
| `MINICLOUD_NODE_REPORTING_ENABLED` | `"1"`, `"yes"`, `"on"`, `" true"`, `"true "`, `"ture"` | error naming key and value | Yes (executed) |
| `MINICLOUD_NODE_REPORTING_ENABLED` | `"true\x00"` (NUL byte) | `os.Setenv` returns `EINVAL` on Linux | N/A (OS rejects NUL in env; executed) |
| `MINICLOUD_NODE_STATE_DIR` (on) | `"relative"`, `"~/x"` | error naming key | Yes (executed) |
| `MINICLOUD_NODE_STATE_DIR` (on) | `"/"`, `"/tmp/a\nb"` | accepted (`filepath.IsAbs` true) | Yes (executed) |
| `MINICLOUD_NODE_TELEMETRY_INTERVAL` (on) | `"0"`, `"-1s"`, `"5"`, `"1e9h"` | error naming key and value | Yes (executed) |
| `MINICLOUD_NODE_TELEMETRY_INTERVAL` (on) | `"1ns"` | `1ns` | Yes (OD-23 open; executed) |
| `MINICLOUD_NODE_TELEMETRY_INTERVAL` (on) | `""` | `0s` | Yes (executed) |

### Points 8 & 10: Docs & Builder Report Accuracy

- **Docs (Point 8):** `.env.example` and `docs/protocol.md` 7.6 match `config.go` and `node.go` (except F-AUDIT-3 minor omissions). `make test-docs` passed inside `make gate` (executed).
- **Builder report (Point 10):** All "Beyond the ADR" items (`node.go:31,150,236,240-253`, `config.go:356-361`), N17 split (`N17a` in `node_config_test.go:109`, `N17b` in `node_test.go:356`), bounded N19/N30 (`runExpectingError` in `node_test.go:168-180`), F-01 fix (`node.go:250`, `node_test.go:720`), and empty diff on forbidden paths are all verified (read + executed). Sending first telemetry right after attach (`node.go:236`) is a minor practical deviation from ADR 4.5 ("on a timer") / 4.2 ("every `MINICLOUD_NODE_TELEMETRY_INTERVAL`"), already noted by the builder.

## 4. Mutant Table (Point 9)

Throwaway worktree `~/mc-mut-gemini-v21d` (removed after runs). Log: `~/gates-v21d-audit/mutants.log`.

| Mutant | Target | Mutant result | Restore result | Status |
|---|---|---|---|---|
| (a) telemetry seq not incremented on failed send | `TestWorker_TelemetrySeqIncreasesAndRetryUsesNewSeq` | FAIL (`RC=1`) | PASS (`RC=0`) | Killed (executed) |
| (b) D5 `CompareAndSwap` -> unconditional `Store(nil)` | `TestWorker_RegisterWithNodeID400_FallsBackAndReattaches` | PASS (`RC=0`) | PASS (`RC=0`) | Survived (executed) |
| (c) `safeCode` returns input unchanged | `^TestWorker_` | PASS (`RC=0`) | PASS (`RC=0`) | Survived (executed) |
| (d) 64 KiB `io.LimitReader` removed | `^TestWorker_` | PASS (`RC=0`) | PASS (`RC=0`) | Survived (executed) |
| (e) `Retry-After` 1 s floor removed | `^TestWorker_` | PASS (`RC=0`) | PASS (`RC=0`) | Survived (executed) |
| (f) panic recover in `guard` removed | `TestWorker_NodePanicIsRecovered` | FAIL (`panic: probe boom`, `RC=1`) | PASS (`RC=0`) | Killed (executed) |
| (g) F-01 `regErr != nil \|\|` removed | `TestWorker_InLoopWorkerRegisterFailure_Retried` | FAIL (`RC=1`) | PASS (`RC=0`) | Killed (executed) |
| (h) `node_id` sent while detached | `TestWorker_ReportingOff_LegacyRegistration` | FAIL (`RC=1`) | PASS (`RC=0`) | Killed (executed) |

## 5. Gate Counts (Point 11)

Go version on all gate runs:
```
go version go1.22.2 linux/amd64
GOROOT=/home/neelesh/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.22.2.linux-amd64
GOTOOLCHAIN=go1.22.2
```

| Gate | Result (PASS / SKIP / FAIL) | Log |
|---|---|---|
| `GOTOOLCHAIN=go1.22.2 make gate` (gofmt, vet, build, test-docs, `-race` unset DB) | `574 / 136 / 0` (0 data races) | `~/gates-v21d-audit/gate.log` (executed) |
| `env -u MINICLOUD_TEST_DATABASE_URL GOTOOLCHAIN=go1.22.2 go test -v -count=1 ./...` | `574 / 136 / 0` | `~/gates-v21d-audit/unset-nonrace.log` (executed) |
| `GOTOOLCHAIN=go1.22.2 make test-db` (`minicloud_gemini_test`) | `212 / 0 / 0` | `~/gates-v21d-audit/db.log` (executed) |
| `GOTOOLCHAIN=go1.22.2 make test-security` (`minicloud_gemini_test`) | `80 / 0 / 0` | `~/gates-v21d-audit/sec.log` (executed) |
| `GOTOOLCHAIN=go1.22.2 go test -race -count=50 -run '^TestWorker_' ./internal/worker` | `ok` (111.656s, 0 data races) | `~/gates-v21d-audit/worker-race50-node.log` (executed) |

Note: `make test-db` and `make test-security` initially failed (`138` and `72` FAILs) because `minicloud_gemini_test` did not exist yet; after creating `minicloud_gemini_test` via the `AGENTS.md` section 9 command, both passed (`212/0/0` and `80/0/0`).

## 6. Not Verified

- `make test-outage` (owner-only per `AGENTS.md` / task spec).
- Non-Linux OS builds (`!linux` fallback probe).
- `TestAudit_Point1_Consent` mode `000` checks if run as root (skipped when `os.Getuid() == 0`; executed here as non-root).

## Cleanup

Deleted throwaway worktrees `~/mc-mut-gemini-v21d-main` and `~/mc-mut-gemini-v21d` (including all throwaway audit probe test files inside them). Kept `~/gates-v21d-audit/` logs as instructed.
