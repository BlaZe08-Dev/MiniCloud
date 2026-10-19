#!/usr/bin/env bash
set -euo pipefail

PORT="8080"
COORDINATOR_URL="http://127.0.0.1:${PORT}"
export MINICLOUD_COORDINATOR_URL="${COORDINATOR_URL}"
export MINICLOUD_HTTP_ADDR="127.0.0.1:${PORT}"

echo ">> Starting coordinator on ${MINICLOUD_HTTP_ADDR}..."
./bin/coordinator &
COORD_PID=$!

cleanup() {
    echo ">> Cleaning up demo processes..."
    kill "${COORD_PID}" 2>/dev/null || true
    if [[ -n "${WORKER1_PID:-}" ]]; then kill "${WORKER1_PID}" 2>/dev/null || true; fi
    if [[ -n "${WORKER2_PID:-}" ]]; then kill "${WORKER2_PID}" 2>/dev/null || true; fi
    wait "${COORD_PID}" 2>/dev/null || true
    echo ">> Demo cleanup completed."
}
trap cleanup EXIT

# Wait for coordinator healthz
for i in {1..30}; do
    if curl -s "${COORDINATOR_URL}/healthz" >/dev/null 2>&1; then
        echo ">> Coordinator is healthy."
        break
    fi
    sleep 0.1
done

echo ">> Starting Worker 1 (worker-alpha)..."
MINICLOUD_WORKER_ID=worker-alpha MINICLOUD_WORKER_SLOTS=2 ./bin/worker &
WORKER1_PID=$!

echo ">> Starting Worker 2 (worker-beta)..."
MINICLOUD_WORKER_ID=worker-beta MINICLOUD_WORKER_SLOTS=2 ./bin/worker &
WORKER2_PID=$!

sleep 1

echo ">> Checking registered workers:"
./bin/mc workers
echo ""

echo ">> Submitting Job 1 (echo)..."
SUBMIT1=$(./bin/mc submit --task echo --payload '{"message":"Hello from MiniCloud Demo!"}')
echo "${SUBMIT1}"
JOB1_ID=$(echo "${SUBMIT1}" | grep "ID:" | awk '{print $2}')

echo ">> Submitting Job 2 (sha256_loop)..."
SUBMIT2=$(./bin/mc submit --task sha256_loop --payload '{"iterations":5000,"input":"minicloud-demo"}')
echo "${SUBMIT2}"
JOB2_ID=$(echo "${SUBMIT2}" | grep "ID:" | awk '{print $2}')

echo ">> Submitting Job 3 (sleep)..."
SUBMIT3=$(./bin/mc submit --task sleep --payload '{"duration_ms":300}')
echo "${SUBMIT3}"
JOB3_ID=$(echo "${SUBMIT3}" | grep "ID:" | awk '{print $2}')

echo ""
echo ">> Waiting for jobs to execute across workers..."
for JOB_ID in "${JOB1_ID}" "${JOB2_ID}" "${JOB3_ID}"; do
    for i in {1..50}; do
        STATE=$(curl -s "${COORDINATOR_URL}/v1/jobs/${JOB_ID}" | grep -o '"state":"[^"]*"' | cut -d'"' -f4)
        if [[ "${STATE}" == "SUCCEEDED" || "${STATE}" == "FAILED" ]]; then
            break
        fi
        sleep 0.1
    done
done

echo ""
echo ">> Querying Job Details:"
./bin/mc get "${JOB1_ID}"
echo ""
./bin/mc get "${JOB2_ID}"
echo ""
./bin/mc get "${JOB3_ID}"

echo ""
echo ">> Querying Attempt History:"
./bin/mc attempts "${JOB1_ID}"
echo ""
./bin/mc attempts "${JOB2_ID}"
echo ""
./bin/mc attempts "${JOB3_ID}"

echo ""
echo ">> Final Workers Status:"
./bin/mc workers

echo ""
echo "=== MiniCloud Local Core Demo Completed Successfully! ==="
