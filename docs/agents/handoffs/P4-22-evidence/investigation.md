# P4-22 investigation (Step 1): raw database errors in coordinator logs (SEC-066)

Base: `agent/claude/P4-20` tip 468229f (owner decision: P4-20 not yet merged; P4-22 builds on it).

## Reproduction without container control (executed)

New test `TestFault_DBOutage_RoundRobin_CoordinatorLogsNoDBDetail` in
`tests/faultinject/logleak_test.go`: the real coordinator binary on `round_robin` (default
assignment interval 1s) reaches the test database only through the fault proxy
(`sectest.NewFaultProxy`, the same proxy the security suite uses); the proxy switches to
`ModeDrop` mid-run; the shared container is never touched. Output: `repro-red.log` (this folder).

Exact offending lines (from `repro-red.log`, unfixed code, proxy port 42411):

```
{"level":"WARN","msg":"lease sweeper tick failed","err":"failed to connect to `user=postgres database=minicloud_claude_test`: 127.0.0.1:42411 (127.0.0.1): failed to receive message: read tcp 127.0.0.1:37568->127.0.0.1:42411: read: connection reset by peer"}
{"level":"WARN","msg":"assignment pass failed","err":"failed to connect to `user=postgres database=minicloud_claude_test`: 127.0.0.1:42411 (127.0.0.1): failed to receive message: unexpected EOF"}
{"level":"ERROR","msg":"database outage or connection failure","err":"failed to connect to `user=postgres database=minicloud_claude_test`: [REDACTED_HOST_PORT] ([REDACTED_HOST_PORT]): failed to receive message: read tcp [REDACTED_HOST_PORT]->[REDACTED_HOST_PORT]: read: connection reset by peer"}
```

Findings from that run:
1. The sweeper line is first (it ticks every 500ms in the rig, before the 1s assignment pass), so
   the leak is NOT specific to round_robin: the sweeper runs under every policy, and fifo_pull
   leaks the same way (inferred: under fifo_pull the sweeper is the only loop; that LeakScan
   passed before is then a matter of whether a sweeper tick fell inside the outage, not verified).
2. The existing HTTP-path `sanitizeError` (regex) redacts IPv4:port but still prints the database
   user and database name (`user=postgres database=minicloud_claude_test`), and it would miss a
   hostname that is not an IPv4 literal or `localhost` (for example `db.internal:5432`).
3. Cause: `pgconn.ConnectError.Error()` is `failed to connect to \`user=%s database=%s\`: <addr>
   (<host>): <inner>` (pgx v5.7.1 `pgconn/errors.go`). `mapPgError`
   (`internal/storage/postgres/repository.go`) ends in `return err`, and about 30 other repository
   paths return `err` raw (`return nil, err`), so this text reaches every caller unchanged.

## Every slog/log call in cmd/ and internal/ that can print an error from storage, the pool or a net call

Confirmed by code reading. "DB detail" = database host, port, user, database name or SQL text.

| Site | Error source | Can carry DB detail? |
|---|---|---|
| `internal/coordinator/assignment_loop.go:75` "assignment pass failed" | `AssignQueuedJobs` (pool) | yes: host, port, user, db name (executed above) |
| `internal/coordinator/assignment.go:245` "assignment pass refresh failed" | `AssignQueuedJobs` on the poll path | yes, same error shape |
| `internal/coordinator/sweeper.go:119` "lease sweeper tick failed" | `repo.Now`, `SweepExpiredLeases` | yes (executed above); all policies |
| `internal/api/handlers.go:114` "database outage or connection failure" | any repo error via the service | yes: user, db name survive `sanitizeError` (executed above); non-IPv4 host survives |
| `internal/api/handlers.go:158` "internal server error" | any unmapped repo error | yes, same; PgError message text |
| `internal/api/handlers.go:493` "readiness probe failed" | `pool.Ping` | yes, same as 114 |
| `internal/api/handlers.go:504` "recovered from panic" | panic value | no code path panics with a storage error (no `panic(` in internal/ or cmd/ non-test code); not changed |
| `cmd/coordinator/main.go:142` "failed to parse database configuration" | `pgxpool.ParseConfig` | yes: host, port, user, db name (pgx masks only the password) |
| `cmd/coordinator/main.go:158` "failed to connect to postgres pool" | `pgxpool.NewWithConfig` | yes in principle (connect error shape) |
| `cmd/coordinator/main.go:163` "failed to ping postgres database" | `pool.Ping` | yes: connect error |
| `cmd/coordinator/main.go:177` "failed to query database schema version" | query | yes: connect error or PgError |
| `cmd/coordinator/main.go:122,195` config / configure errors | config package, service setters | no: fixed text from our own validation |
| `cmd/coordinator/main.go:171` embedded migrations | embed FS | no |
| `cmd/coordinator/main.go:208,223` http server / shutdown | net listen, ctx | coordinator's own bind address only (legitimate, already logged at start) |
| `internal/coordinator/service.go:547` "lease renewal rejected" `reason` | only reached for the four domain sentinels; their text is fixed (mapPgError returns sentinels without raw text) | no |
| `cmd/migrate/main.go:53` "failed to connect to postgres" | `pgx.Connect` | yes: host, port, user, db name |
| `cmd/migrate/main.go:61` "migration failed" | migrator (wraps PgError and connect errors with the migration name) | yes |
| `cmd/migrate/main.go:41,44` | flag deprecation, missing URL | no |
| `internal/worker/client.go:171` heartbeat, `:203` work poll, `:448` renew transport, `:710,714,731` result delivery; `cmd/worker/main.go:56` | net calls to the coordinator | coordinator URL host:port only (the worker's own config); never DB detail: the coordinator answers 5xx with fixed bodies (`service temporarily unavailable`, `internal server error`) |
| `cmd/worker/main.go:26` | worker config | no |
| `internal/scheduler/round_robin.go:54,75` | none (decision data) | no |

## Choice: fix at the log boundary, not at the source

The source fix (connection-class repository error becomes a sanitized sentinel) is NOT safe here:

1. HTTP 503 classification `isConnectionOrTimeoutError` (`internal/api/handlers.go:36`) depends on
   `errors.As(err, &net.Error)`, `errors.As(err, &*pgconn.PgError)` (Class 08, 57, 55P03) and on
   substring matching of `err.Error()` ("connection refused", "failed to connect", "dial tcp",
   "closed pool", "broken pipe", "connection reset", "driver: bad connection"). A sentinel with
   no raw text breaks the substring path, so some outages would turn from 503 into 500
   (`INTERNAL_ERROR`), which the worker treats differently from 503 (`ErrServiceUnavailable`
   backoff in `internal/worker/client.go`). That is a status-code and retry change: the stop rule.
2. About 30 repository return paths bypass `mapPgError` (`return nil, err`), plus the lease token
   paths and the migrator; a source fix touches every one of them.
3. `internal/coordinator` must not import pgx (`tests/architecture`), so the coordinator could not
   re-classify a sentinel by pgx type anyway.

The log-boundary fix changes no error value: every `errors.Is`/`errors.As`, status mapping, grace
counting (`failedSince` in the sweeper) and retry decision sees exactly the same error. Only the
log attribute changes. One helper, `internal/logsafe`, stdlib only (no pgx, so the coordinator
boundary holds), returns a fixed class label (`connection_refused`, `connection_reset`,
`host_unreachable`, `timeout`, `deadline_exceeded`, `closed_pool`, `tls`, `sqlstate_<code>`,
`domain_<sentinel>`, ...) and never any text of the error.

Sites changed: the three coordinator loop sites, the three API sites, coordinator startup
142/158/163/177. Not changed, with reason: `cmd/migrate` (an operator CLI whose output goes to the
operator who supplied the URL; not coordinator logs; its migration-name context is needed to
diagnose checksum failures) and the worker (no DB detail possible). Both listed as open questions.
