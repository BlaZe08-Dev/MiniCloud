#!/usr/bin/env bash
# AGENTS.md section 6 quality gate. Log: .gate.log (gitignored). Exit 1 if any step fails.
# The test step unsets MINICLOUD_TEST_DATABASE_URL on purpose: with it set, ./... restarts the shared container.
set -u
cd "$(dirname "$0")/.."
LOG=.gate.log
# One gate at a time per worktree: concurrent runs wait here instead of overwriting the log.
exec 9> .gate.lock
if ! flock -n 9; then echo "another gate run is in progress; waiting for it"; flock 9; fi
: > "$LOG"
fail=0

step() {
  echo "== $*" >> "$LOG"
  if "$@" >> "$LOG" 2>&1; then echo "ok:     $*"; else echo "FAILED: $*"; fail=1; fi
}

echo "== gofmt -l ." >> "$LOG"
unformatted=$(gofmt -l . 2>&1)
if [ -n "$unformatted" ]; then
  echo "$unformatted" >> "$LOG"; echo "FAILED: gofmt -l . (files listed in $LOG)"; fail=1
else
  echo "ok:     gofmt -l ."
fi

step go vet ./...
step go build ./...
step make test-docs
step env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...

# AGENTS.md section 6 count grep, over the -v run above (no other step prints --- lines unless it fails).
pass=$(grep -c '^--- PASS' "$LOG"); skip=$(grep -c '^--- SKIP' "$LOG"); nfail=$(grep -c '^--- FAIL' "$LOG")
echo "PASS $pass SKIP $skip FAIL $nfail"

echo "-- tail of $LOG"; tail -5 "$LOG"
if [ "$fail" -ne 0 ]; then echo "GATE FAILED (full log: $LOG)"; exit 1; fi
if [ "$skip" -gt 0 ]; then echo "GATE PASSED ($skip skipped; a skip is not a pass, AGENTS.md section 5) (full log: $LOG)"; else echo "GATE PASSED (full log: $LOG)"; fi
