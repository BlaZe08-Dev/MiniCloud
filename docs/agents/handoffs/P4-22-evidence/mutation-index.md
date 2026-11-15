# P4-22 mutation index (single-site, red then green)

Executed by `mut.py` (this folder) in the throwaway worktree `~/mc-mut-claude` (removed afterwards),
work tip 98e6944. Each mutation changes exactly one site, runs the named test (red), restores the
file and runs it again (green). `mut-<id>.diff` is the mutation, `mut-<id>-red.log` /
`mut-<id>-green.log` the real output. When the mutated site was the file's only use of
`logsafe`, the runner appends `var _ = logsafe.Err` so the mutant compiles (a first M1-M3 run
without that failed to build and was discarded; its logs were overwritten by the valid rerun).

| ID | Site mutated | Mutation | Test | Red | Green |
|---|---|---|---|---|---|
| M1 | `internal/coordinator/assignment_loop.go` "assignment pass failed" | log raw `err` | `TestLogSafe_AssignmentPassFailed_LogsClassOnly` | FAIL (raw connect text) | PASS |
| M1e | same | same | `TestFault_DBOutage_RoundRobin_CoordinatorLogsNoDBDetail` (real binary, DB) | FAIL: dial host:port, user, db name | PASS |
| M2 | `internal/coordinator/assignment.go` "assignment pass refresh failed" | log raw `err` (old key `error`) | `TestLogSafe_AssignmentPassRefreshFailed_LogsClassOnly` | FAIL | PASS |
| M3 | `internal/coordinator/sweeper.go` "lease sweeper tick failed" | log raw `err` | `TestLogSafe_LeaseSweeperTickFailed_LogsClassOnly` | FAIL | PASS |
| M3e | same | same | `TestFault_DBOutage_RoundRobin_CoordinatorLogsNoDBDetail` (real binary, DB) | FAIL: dial host:port, user, db name | PASS |
| M4 | `internal/api/handlers.go` "database outage or connection failure" | log raw `err` | `TestLogSafe_APISites_LogClassOnly_StatusUnchanged` | FAIL | PASS |
| M5 | `internal/api/handlers.go` "readiness probe failed" | log raw `err` | same | FAIL | PASS |
| M6 | `internal/api/handlers.go` "internal server error" | log raw `err` | same | FAIL | PASS |
| M7 | `cmd/coordinator/main.go` "failed to ping postgres database" | log raw `err` | `TestFault_CoordinatorStartup_DBUnreachable_LogsNoDBDetail` | FAIL: `leakuser` | PASS |
| M8 | `cmd/coordinator/main.go` "failed to parse database configuration" | log raw `err` | same | FAIL: `leakuser` | PASS |
| M9 | `internal/logsafe/logsafe.go` fallback | return `err.Error()` instead of `unclassified` | the three logsafe tests together: `TestLogsafe_Class_RealPgxConnectErrors`, `TestLogsafe_Class_ServerAndDomainErrors`, `TestLogsafe_Class_NeverEchoesInput` | FAIL | PASS |
| M10 | `internal/logsafe/logsafe.go` `sqlStateClass` | skip SQLSTATE validation | `TestLogsafe_Class_ServerAndDomainErrors` | FAIL (hostile code echoed) | PASS |
| M11 | `internal/logsafe/logsafe.go` SQLSTATE branch | append `err.Error()` to the label | the three logsafe tests together: `TestLogsafe_Class_RealPgxConnectErrors`, `TestLogsafe_Class_ServerAndDomainErrors`, `TestLogsafe_Class_NeverEchoesInput` | FAIL (28P01 case leaks user, db) | PASS |

Not covered by a mutation: `cmd/coordinator/main.go` "failed to connect to postgres pool"
(`pgxpool.NewWithConfig` fails only on config errors after a successful parse) and "failed to query
database schema version" (needs a database that pings but fails the query). Both use the same
helper; the helper itself is covered by M9-M11.
