Task: DEV-HOOKS-1 (Claude Code hooks and make gate)
Branch: agent/claude/setup-hooks
Work tip (last commit before this report): 5b9d462
Date: 2026-11-24

Results (all executed in this session):
- Stop hook hash: `.gate.hash` (gitignored) holds sha256 of `git diff HEAD -- '*.go'` plus untracked .go names and contents. Run 1 (new state) ran the gate, passed in 49s and wrote the hash; run 2 (unchanged) skipped in 0s; run 3 (content changed) re-ran, 49s. The hash is stored only after a PASS, so an unchanged but failing tree is re-gated rather than let through.
- gate.sh: runs `go test -v`, then the AGENTS.md section 6 grep. Output: `PASS 552 SKIP 136 FAIL 0` and `GATE PASSED (136 skipped; a skip is not a pass, AGENTS.md section 5)`. Clean-tree run: 50.8s real.
- Failure path 1 (executed, live PostToolUse hook): Write of an unformatted internal/logsafe/zz_hookcheck.go produced `gofmt -l flags .../zz_hookcheck.go (run gofmt -s -w on it)`. File removed.
- Failure path 2 (executed): deliberately failing test internal/logsafe/zz_hookcheck_test.go. Direct run of stop_gate.py: rc=2 after 73s, `FAILED: ... go test ...`, no .gate.hash written; with stop_hook_active=true: rc=0 (no loop). The live Stop hook also fired when the turn ended and blocked once with the same output. Test file removed.
- CLAUDE_PROJECT_DIR resolved in all three hooks, shown by live firing: PreToolUse (guard blocked a command), PostToolUse (gofmt report), Stop (feedback above).
- Known limitation: two gates run at the same time share .gate.log and overwrite each other (the concurrent runs reported PASS 645 and PASS 492; the clean single runs report 552). No lock was added.
- Not tested: a go vet failure in the PostToolUse hook; the guard has only the earlier 21 synthetic cases; DB tests (`make test-db`) are outside the gate.
