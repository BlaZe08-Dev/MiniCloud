# P4-17 mutation index

Each row is one single-site change to `internal/config/config.go` (`dispatchPolicyFromEnv`), made in a throwaway worktree `~/mc-mut-claude` (removed afterwards) at commit e118f4e with this worktree's `.env`. For each row the three tests below were run with `go test -race -count=1 -v -run '<pattern>' ./internal/config ./cmd/coordinator` (red), the file was restored with `git checkout`, and the same command was run again (green). Logs are `mut-<id>-red.log` and `mut-<id>-green.log`, the change is `mut-<id>.diff`. Slog lines are filtered out. Evidence label: executed.

Tests: `TestLoadCoordinatorConfig_AssignmentDefaults`, `TestLoadCoordinatorConfig_DispatchPolicySelection`, `TestMain_DispatchPolicyFromEnv_EndToEnd`.

| Mutation | Change | Sites | Red (top-level FAIL / PASS) | Green (FAIL / PASS) | Verdict |
|---|---|---|---|---|---|
| M01 | default back to fifo_pull: `return "round_robin"` becomes `return "fifo_pull"` | 1 | 3 / 0 | 0 / 3 | KILLED |
| M02 | an explicit fifo_pull is ignored: `if v == ""` becomes `if v == "" \|\| v == "fifo_pull"` | 1 | 2 / 1 | 0 / 3 | KILLED (Defaults passes by design: it only checks unset) |
| M03 | empty is not defaulted: `if v == ""` becomes `if false` (unset then yields an invalid empty policy) | 1 | 3 / 0 | 0 / 3 | KILLED |
