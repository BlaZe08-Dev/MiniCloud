#!/usr/bin/env bash
# V2-0b measurement (ADR-0020 Section 2.9): real coordinator + real worker binaries on the builder's
# own throwaway database minicloud_claudebench_test (never minicloud_test; not the gate database).
# Adapted from docs/agents/reviews/V2-0-evidence/run-binary.sh (no request proxy).
#
# usage: run-binary.sh LABEL POLICY SLOTS NJOBS SLEEP_MS COORD_BIN WORKER_BIN MIGRATE_BIN
set -u
ROOT=$(git rev-parse --show-toplevel)
EVID="$ROOT/docs/agents/handoffs/V2-0b-evidence/measure"
LABEL=$1 POLICY=$2 SLOTS=$3 NJOBS=$4 SLEEP_MS=$5 COORD=$6 WORKER=$7 MIGRATE=$8
OUT="$EVID/run-$LABEL"
rm -rf "$OUT"; mkdir -p "$OUT"

DB=minicloud_claudebench_test
BASE=$(grep -E '^MINICLOUD_TEST_DATABASE_URL=' "$ROOT/.env" | cut -d= -f2-)
URL=$(printf '%s\n' "$BASE" | sed -E "s#/[^/?]+([?].*)?\$#/$DB\\1#")
case "$DB" in minicloud_test|minicloud_claude_test) echo REFUSED; exit 1;; esac
printf '%s\n' "$DB" | grep -Eqx 'minicloud_[a-z0-9]+_test' || { echo REFUSED; exit 1; }
psqlq() { docker exec -i minicloud-postgres sh -c 'psql -X -A -t -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d '"$DB"; }

docker exec minicloud-postgres sh -c 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres -c "DROP DATABASE IF EXISTS $1" -c "CREATE DATABASE $1"' sh "$DB" >/dev/null
MINICLOUD_DATABASE_URL="$URL" "$MIGRATE" > "$OUT/migrate.log" 2>&1 || { cat "$OUT/migrate.log"; exit 1; }

CPORT=18090
MINICLOUD_HTTP_ADDR=127.0.0.1:$CPORT MINICLOUD_STORAGE_DRIVER=postgres MINICLOUD_DATABASE_URL="$URL" \
  MINICLOUD_DISPATCH_POLICY=$POLICY "$COORD" > "$OUT/coordinator.log" 2>&1 &
CPID=$!
for i in $(seq 100); do curl -sf http://127.0.0.1:$CPORT/healthz >/dev/null && break; sleep 0.1; done
MINICLOUD_COORDINATOR_URL=http://127.0.0.1:$CPORT MINICLOUD_WORKER_SLOTS=$SLOTS MINICLOUD_WORKER_ID=w1 \
  "$WORKER" > "$OUT/worker.log" 2>&1 &
WPID=$!
for i in $(seq 100); do grep -q 'successfully registered' "$OUT/worker.log" && break; sleep 0.1; done
sleep 0.5

for i in $(seq "$NJOBS"); do
  curl -s -X POST http://127.0.0.1:$CPORT/v1/jobs -H 'Content-Type: application/json' \
    -d "{\"task_type\":\"sleep\",\"payload\":{\"duration_ms\":$SLEEP_MS},\"max_attempts\":3,\"timeout_seconds\":120}" >> "$OUT/submit.log"
  echo >> "$OUT/submit.log"
done
for i in $(seq 1800); do
  N=$(echo "SELECT count(*) FROM jobs WHERE state IN ('SUCCEEDED','FAILED','DEAD_LETTER','CANCELLED')" | psqlq)
  [ "$N" = "$NJOBS" ] && break
  sleep 0.1
done
kill -TERM $WPID; wait $WPID 2>/dev/null
kill -TERM $CPID; wait $CPID 2>/dev/null

{
echo "label=$LABEL policy=$POLICY slots=$SLOTS jobs=$NJOBS sleep_ms=$SLEEP_MS coordinator=$(basename "$COORD") worker=$(basename "$WORKER")"
echo "== worker row: total_slots, executes_concurrently (NULL column = coordinator schema has none)"
echo "SELECT total_slots FROM workers" | psqlq
echo "SELECT executes_concurrently FROM workers" | psqlq 2>&1 | head -1
echo "== job states"; echo "SELECT state, count(*) FROM jobs GROUP BY state ORDER BY state" | psqlq
echo "== attempts per job (max)"; echo "SELECT max(n) FROM (SELECT count(*) n FROM attempts GROUP BY job_id) s" | psqlq
echo "== max concurrent attempts"
echo "WITH ev AS (SELECT started_at t, 1 d FROM attempts UNION ALL SELECT finished_at, -1 FROM attempts WHERE finished_at IS NOT NULL)
      SELECT max(c) FROM (SELECT sum(d) OVER (ORDER BY t, d ROWS UNBOUNDED PRECEDING) c FROM ev) s" | psqlq
echo "== wall time: first job created -> last attempt finished (s)"
echo "SELECT round(extract(epoch FROM (SELECT max(finished_at) FROM attempts) - (SELECT min(created_at) FROM jobs))::numeric, 2)" | psqlq
echo "== lapse events (JOB_ASSIGNMENT_LAPSED)"; echo "SELECT count(*) FROM job_events WHERE event_type = 'JOB_ASSIGNMENT_LAPSED'" | psqlq
echo "== JOB_DISPATCHED by policy"; echo "SELECT details->>'policy', count(*) FROM job_events WHERE event_type = 'JOB_DISPATCHED' GROUP BY 1 ORDER BY 1" | psqlq
echo "== attempt timeline (job, attempt_no, state, start, finish, relative to first job created)"
echo "SELECT a.job_id, a.attempt_no, a.state, round(extract(epoch FROM a.started_at - m.t0)::numeric,2), round(extract(epoch FROM a.finished_at - m.t0)::numeric,2)
      FROM attempts a, (SELECT min(created_at) t0 FROM jobs) m ORDER BY a.started_at" | psqlq
} > "$OUT/summary.txt" 2>&1
cat "$OUT/summary.txt"
