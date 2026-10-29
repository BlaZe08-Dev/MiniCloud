#!/usr/bin/env bash
set -u

REPO_ROOT="/home/blazeee/MiniCloud-gemini"
EVIDENCE_DIR="$REPO_ROOT/docs/agents/reviews/P4-4e-round2-evidence"
cd "$REPO_ROOT"

echo "=== Gate 1: gofmt ==="
OUT1="$EVIDENCE_DIR/gate_gofmt.txt"
{
  echo "=== COMMAND: gofmt -l . ==="
  echo "=== TIMESTAMP: $(date -u +"%Y-%m-%dT%H:%M:%SZ") ==="
  gofmt -l .
  echo "=== EXIT CODE: $? ==="
} > "$OUT1" 2>&1
echo "gofmt rc=$(grep "=== EXIT CODE:" "$OUT1" | awk '{print $4}')"

echo "=== Gate 2: go vet ==="
OUT2="$EVIDENCE_DIR/gate_govet.txt"
{
  echo "=== COMMAND: go vet ./... ==="
  echo "=== TIMESTAMP: $(date -u +"%Y-%m-%dT%H:%M:%SZ") ==="
  go vet ./...
  echo "=== EXIT CODE: $? ==="
} > "$OUT2" 2>&1
echo "go vet rc=$(grep "=== EXIT CODE:" "$OUT2" | awk '{print $4}')"

echo "=== Gate 3: make test-docs ==="
OUT3="$EVIDENCE_DIR/gate_test_docs.txt"
{
  echo "=== COMMAND: make test-docs ==="
  echo "=== TIMESTAMP: $(date -u +"%Y-%m-%dT%H:%M:%SZ") ==="
  make test-docs
  echo "=== EXIT CODE: $? ==="
} > "$OUT3" 2>&1
echo "make test-docs rc=$(grep "=== EXIT CODE:" "$OUT3" | awk '{print $4}')"

echo "=== Gate 4: env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./... ==="
OUT4="$EVIDENCE_DIR/gate_unset_v.txt"
{
  echo "=== COMMAND: env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./... ==="
  echo "=== TIMESTAMP: $(date -u +"%Y-%m-%dT%H:%M:%SZ") ==="
  env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...
  echo "=== EXIT CODE: $? ==="
} > "$OUT4" 2>&1
echo "go test unset rc=$(grep "=== EXIT CODE:" "$OUT4" | awk '{print $4}')"

OUTC="$EVIDENCE_DIR/gate_unset_counts.txt"
{
  echo "PASS $(grep -c '^--- PASS' "$OUT4") SKIP $(grep -c '^--- SKIP' "$OUT4") FAIL $(grep -c '^--- FAIL' "$OUT4")"
  echo "Top-level PASS: $(grep -c '^--- PASS: Test[A-Za-z0-9_]* (' "$OUT4")"
  echo "Top-level SKIP: $(grep -c '^--- SKIP: Test[A-Za-z0-9_]* (' "$OUT4")"
  echo "Top-level FAIL: $(grep -c '^--- FAIL: Test[A-Za-z0-9_]* (' "$OUT4")"
} > "$OUTC" 2>&1
cat "$OUTC"

echo "Sleeping 2s before database tests..."
sleep 2

echo "=== Gate 5: make test-db ==="
OUT5="$EVIDENCE_DIR/gate_test_db.txt"
{
  echo "=== COMMAND: make test-db ==="
  echo "=== TIMESTAMP: $(date -u +"%Y-%m-%dT%H:%M:%SZ") ==="
  make test-db
  echo "=== EXIT CODE: $? ==="
} > "$OUT5" 2>&1
echo "make test-db rc=$(grep "=== EXIT CODE:" "$OUT5" | awk '{print $4}')"
echo "test-db top-level PASS: $(grep -c '^--- PASS: Test[A-Za-z0-9_]* (' "$OUT5")"

echo "Sleeping 2s before test-security run 1..."
sleep 2

echo "=== Gate 6: make test-security (Run 1) ==="
OUT6="$EVIDENCE_DIR/gate_test_security_run1.txt"
{
  echo "=== COMMAND: make test-security (Run 1) ==="
  echo "=== TIMESTAMP: $(date -u +"%Y-%m-%dT%H:%M:%SZ") ==="
  make test-security
  echo "=== EXIT CODE: $? ==="
} > "$OUT6" 2>&1
echo "test-security run 1 rc=$(grep "=== EXIT CODE:" "$OUT6" | awk '{print $4}')"
echo "run 1 PASS $(grep -c '^--- PASS' "$OUT6") SKIP $(grep -c '^--- SKIP' "$OUT6") FAIL $(grep -c '^--- FAIL' "$OUT6")"
echo "run 1 Top-level PASS: $(grep -c '^--- PASS: Test[A-Za-z0-9_]* (' "$OUT6")"
echo "run 1 Top-level SKIP: $(grep -c '^--- SKIP: Test[A-Za-z0-9_]* (' "$OUT6")"

echo "Sleeping 2s before test-security run 2..."
sleep 2

echo "=== Gate 7: make test-security (Run 2) ==="
OUT7="$EVIDENCE_DIR/gate_test_security_run2.txt"
{
  echo "=== COMMAND: make test-security (Run 2) ==="
  echo "=== TIMESTAMP: $(date -u +"%Y-%m-%dT%H:%M:%SZ") ==="
  make test-security
  echo "=== EXIT CODE: $? ==="
} > "$OUT7" 2>&1
echo "test-security run 2 rc=$(grep "=== EXIT CODE:" "$OUT7" | awk '{print $4}')"
echo "run 2 PASS $(grep -c '^--- PASS' "$OUT7") SKIP $(grep -c '^--- SKIP' "$OUT7") FAIL $(grep -c '^--- FAIL' "$OUT7")"
echo "run 2 Top-level PASS: $(grep -c '^--- PASS: Test[A-Za-z0-9_]* (' "$OUT7")"
echo "run 2 Top-level SKIP: $(grep -c '^--- SKIP: Test[A-Za-z0-9_]* (' "$OUT7")"

echo "=== ALL GATES COMPLETED ==="
