#!/usr/bin/env bash
# V2-0 audit mutation runs. Run from the throwaway worktree ~/mc-mut-claude (audit tests copied in).
# For each mutant: restore client.go, apply the diff, build the race test binary once, run every
# pool test on its own (-test.timeout 45s) and print PASS/FAIL per test. "none" = unmodified code.
set -u
EVID=/home/blazeee/MiniCloud-claude/docs/agents/reviews/V2-0-evidence
HB=/home/blazeee/MiniCloud-claude/docs/agents/handoffs/V2-0-evidence
cd ~/mc-mut-claude
TESTS=$(go test -list '^(TestConcurrency_|TestLoadWorkerConfig_SlotsUpperBound|TestAudit_)' ./internal/worker/ | grep '^Test')
for m in none a b c d e; do
  git checkout -q internal/worker/client.go
  case $m in
    none) ;;
    a|b|c) git apply "$HB/mutant-$m.diff" || { echo "apply $m failed"; exit 1; } ;;
    d|e) git apply "$EVID/mutant-$m.diff" || { echo "apply $m failed"; exit 1; } ;;
  esac
  echo "=== mutant $m: $(git diff --stat internal/worker/client.go | tail -1)"
  go test -c -race -o /tmp/claude-1000/-home-blazeee-MiniCloud-claude/3d62218b-38a9-42ff-b5fc-951879402882/scratchpad/worker.test ./internal/worker/ || { echo "build failed"; continue; }
  killed=0
  for t in $TESTS; do
    out=$(/tmp/claude-1000/-home-blazeee-MiniCloud-claude/3d62218b-38a9-42ff-b5fc-951879402882/scratchpad/worker.test -test.run "^$t\$" -test.count=1 -test.timeout 45s 2>&1); rc=$?
    if [ $rc -eq 0 ]; then echo "PASS $t"; else killed=$((killed+1)); echo "FAIL $t :: $(printf '%s\n' "$out" | grep -m1 -E 'audit_v20_test.go:[0-9]+:|concurrency_test.go:[0-9]+:|panic: test timed out|DATA RACE' | sed 's/^ *//' | cut -c1-200)"; fi
  done
  echo "=== mutant $m: $killed tests fail"
done
git checkout -q internal/worker/client.go
git diff --quiet internal/worker/client.go && echo "client.go restored"
