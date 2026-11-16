#!/usr/bin/env bash
# V2-0 fix round 1 mutation runs, from the throwaway worktree ~/mc-fix1-mut (detached at the fix
# commit). For each mutant: restore client.go, apply the diff, build the race test binary once, run
# every pool test on its own (-test.timeout 45s), print PASS/FAIL per test. "none" = unmodified.
set -u
REPO=/home/blazeee/MiniCloud-claude
HB=$REPO/docs/agents/handoffs/V2-0-evidence
EVID=$HB/fix1
BIN=${TMPDIR:-/tmp}/fix1-worker.test
cd ~/mc-fix1-mut
TESTS=$(go test -list '^(TestConcurrency_|TestLoadWorkerConfig_SlotsUpperBound)' ./internal/worker/ | grep '^Test')
for m in none a b c d e f f2 g g2 h h2; do
  git checkout -q internal/worker/client.go
  case $m in
    none) ;;
    a|b|c) git apply "$HB/mutant-$m.diff" || { echo "apply $m failed"; exit 1; } ;;
    *) git apply "$EVID/mutant-$m.diff" || { echo "apply $m failed"; exit 1; } ;;
  esac
  echo "=== mutant $m: $(git diff --stat internal/worker/client.go | tail -1)"
  go test -c -race -o "$BIN" ./internal/worker/ || { echo "build failed"; continue; }
  killed=0
  for t in $TESTS; do
    out=$("$BIN" -test.run "^$t\$" -test.count=1 -test.timeout 45s 2>&1); rc=$?
    if [ $rc -eq 0 ]; then echo "PASS $t"; else killed=$((killed+1)); echo "FAIL $t :: $(printf '%s\n' "$out" | grep -m1 -E 'concurrency_test.go:[0-9]+:|^panic: |DATA RACE' | sed 's/^ *//' | cut -c1-200)"; fi
  done
  echo "=== mutant $m: $killed tests fail"
done
git checkout -q internal/worker/client.go
git diff --quiet internal/worker/client.go && echo "client.go restored"
