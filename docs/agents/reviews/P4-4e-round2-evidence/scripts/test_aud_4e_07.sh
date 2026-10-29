#!/usr/bin/env bash
set -u

REPO_ROOT="/home/blazeee/MiniCloud-gemini"
EVIDENCE_DIR="$REPO_ROOT/docs/agents/reviews/P4-4e-round2-evidence"
TARGET_FILE="$REPO_ROOT/tests/abuse/oversize_worker_test.go"

cd "$REPO_ROOT"

echo "=== Testing AUD-4e-07 Guard ==="

# Step 1: One rename
echo "Step 1: One rename in tests/abuse/oversize_worker_test.go"
sed -i 's/func TestAbuse_OversizeOutput_PlainASCII(/func TestAbuse_OversizeOutput_PlainASCIIRenamed(/' "$TARGET_FILE"

OUT1="$EVIDENCE_DIR/aud_4e_07_rename_one.txt"
{
    echo "=== COMMAND: make test-security (single rename) ==="
    echo "=== TIMESTAMP: $(date -u +"%Y-%m-%dT%H:%M:%SZ") ==="
    make test-security
    echo "=== EXIT CODE: $? ==="
} > "$OUT1" 2>&1

echo "Single rename test completed with rc=$(grep "=== EXIT CODE:" "$OUT1" | awk '{print $4}')"

# Step 2: Rename plus add
echo "Step 2: Rename plus add in tests/abuse/oversize_worker_test.go"
cat << 'EOF' >> "$TARGET_FILE"

func TestAbuse_OversizeOutput_AddedForGuardDemo(t *testing.T) {}
EOF

OUT2="$EVIDENCE_DIR/aud_4e_07_rename_and_add.txt"
{
    echo "=== COMMAND: make test-security (rename plus add) ==="
    echo "=== TIMESTAMP: $(date -u +"%Y-%m-%dT%H:%M:%SZ") ==="
    make test-security
    echo "=== EXIT CODE: $? ==="
} > "$OUT2" 2>&1

echo "Rename plus add test completed with rc=$(grep "=== EXIT CODE:" "$OUT2" | awk '{print $4}')"

# Restore
git checkout "$TARGET_FILE"
echo "Restored $TARGET_FILE"
git diff --stat HEAD -- ':!docs' ':!reports'
