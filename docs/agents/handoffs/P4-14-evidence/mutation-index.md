# P4-14 mutation index

Each row is a single-site mutation of `ComputeBackoffWithJitter` in `internal/worker/client.go`, tested by hand in a throwaway worktree `~/mc-mut-gemini` based on `agent/gemini/P4-14`, with the `.env` of `~/MiniCloud-gemini` (test database `minicloud_gemini_test`).

For each mutation, the change was applied, verified by `git diff`, and tested with `go test -v -run 'TestComputeBackoffWithJitter' ./internal/worker`. All tests ran one process at a time. The throwaway worktree was removed after mutation verification.

| Mutation | What the mutant does | File | Test (run alone) | Package | Red | Green | Verdict | Diff (+/-) |
|---|---|---|---|---|---|---|---|---|
| M1 | Remove the clamp (`failureCount > 5` clamp removed completely) | `internal/worker/client.go` | `TestComputeBackoffWithJitter_Table`, `TestComputeBackoffWithJitter_Property`, `TestComputeBackoffWithJitter_CallerSettings` | `./internal/worker` | FAIL | PASS | KILLED | +0/-3 |
| M2 | Clamp one step too late: clamp after shift (`multiplier > 16` applied after `1 << (failureCount - 1)`) | `internal/worker/client.go` | `TestComputeBackoffWithJitter_Table`, `TestComputeBackoffWithJitter_Property`, `TestComputeBackoffWithJitter_CallerSettings` | `./internal/worker` | FAIL (count 64 returns 0s) | PASS | KILLED | +3/-3 |
| M2b | Clamp one step too late in doubling sequence: clamp `failureCount > 6` instead of `failureCount > 5` | `internal/worker/client.go` | `TestComputeBackoffWithJitter_Table`, `TestComputeBackoffWithJitter_Property` | `./internal/worker` | FAIL (count 6 backoff exceeds 16x) | PASS | KILLED | +1/-1 |

Total: 3 mutants, 3 killed, 0 survived.

### Evidence Files
- `mutant-1-remove-clamp.diff` / `mutant-1-remove-clamp.txt`: M1 diff and test failure output.
- `mutant-2-clamp-too-late.diff` / `mutant-2-clamp-too-late.txt`: M2 diff and test failure output demonstrating count 64 returning 0s across table, property, and caller settings tests.
- `mutant-2b-clamp-shift-step-too-late.diff` / `mutant-2b-clamp-shift-step-too-late.txt`: M2b diff and test failure output demonstrating count 6 exceeding 16x cap.
