# P4-16 mutation index

Each mutation is a single-site edit in a throwaway worktree (`~/mc-mut-claude`), run alone with the one test shown, then restored and rerun. Logs are the verbatim `go test` output (executed).

| ID | Mutation | Test | Red log | Green log |
|---|---|---|---|---|
| M1 | `getIntEnv` returns the default with no error on a parse failure | `TestGetIntEnv_Table` | M1-red.log | M1-green.log |
| M2 | `getDurationEnv` returns the default with no error on a parse failure | `TestGetDurationEnv_Table` | M2-red.log | M2-green.log |
| M3 | `getPositiveIntEnv` accepts zero (`<= 0` to `< 0`) | `TestGetPositiveIntEnv_Table` | M3-red.log | M3-green.log |
| M4 | `getPositiveDurationEnv` accepts zero (`<= 0` to `< 0`) | `TestGetPositiveDurationEnv_Table` | M4-red.log | M4-green.log |
| M5 | `LoadCoordinatorConfig` no longer returns `parseErr` | `TestLoadCoordinatorConfig_OldVariablesRejectBadValue` | M5-red.log | M5-green.log |
| M6 | `LoadWorkerConfig` ignores the slots error | `TestLoadWorkerConfig_OldVariablesRejectBadValue` | M6-red.log | M6-green.log |
| M7 | `getDurationEnv` swallows the parse error (process keeps running) | `TestStartup_BadEnvStopsProcess` (`tests/integration`) | M7-red.log | M7-green.log |

An earlier M7 draft (removing `os.Exit(1)` in `cmd/worker`) stayed green because a nil `cfg` panics with a non-zero exit; it was discarded as not discriminating.
