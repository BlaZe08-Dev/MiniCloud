#!/usr/bin/env bash
# V2-0 audit: real coordinator + real worker binaries against the auditor's own database
# minicloud_claudeaudit_test (never minicloud_test, never minicloud_claude_test, which the gates use).
#
# usage: run-binary.sh LABEL POLICY SLOTS NJOBS SLEEP_MS [COORD_BIN] [WORKER_BIN] [SIGTERM_AFTER_S]
#   SIGTERM_AFTER_S: if set, SIGTERM the worker that many seconds after the jobs are submitted
#   (shutdown test) and then keep the coordinator up for 45 s to watch the leases expire.
# Every request the worker sends goes through auditproxy (AUDITPROXY env, logs each request).
set -u
ROOT=$(git rev-parse --show-toplevel)
EVID="$ROOT/docs/agents/reviews/V2-0-evidence"
LABEL=$1 POLICY=$2 SLOTS=$3 NJOBS=$4 SLEEP_MS=$5
COORD=${6:-$ROOT/bin/coordinator}
WORKER=${7:-$ROOT/bin/worker}
TERM_AFTER=${8:-}
OUT="$EVID/run-$LABEL"
rm -rf "$OUT"; mkdir -p "$OUT"

DB=minicloud_claudeaudit_test
BASE=$(grep -E '^MINICLOUD_TEST_DATABASE_URL=' "$ROOT/.env" | cut -d= -f2-)
URL=$(printf '%s\n' "$BASE" | sed -E "s#/[^/?]+([?].*)?\$#/$DB\\1#")
case "$DB" in minicloud_test|minicloud_claude_test) echo REFUSED; exit 1;; esac
psqlq() { docker exec -i minicloud-postgres sh -c 'psql -X -A -t -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d '"$DB"; }

docker exec minicloud-postgres sh -c 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres -c "DROP DATABASE IF EXISTS $1" -c "CREATE DATABASE $1"' sh "$DB" >/dev/null
MINICLOUD_DATABASE_URL="$URL" "$ROOT/bin/migrate" > "$OUT/migrate.log" 2>&1 || { cat "$OUT/migrate.log"; exit 1; }

CPORT=18080 PPORT=18081
MINICLOUD_HTTP_ADDR=127.0.0.1:$CPORT MINICLOUD_STORAGE_DRIVER=postgres MINICLOUD_DATABASE_URL="$URL" \
  MINICLOUD_DISPATCH_POLICY=$POLICY "$COORD" > "$OUT/coordinator.log" 2>&1 &
CPID=$!
for i in $(seq 100); do curl -sf http://127.0.0.1:$CPORT/healthz >/dev/null && break; sleep 0.1; done
"$AUDITPROXY" 127.0.0.1:$PPORT http://127.0.0.1:$CPORT "$OUT/proxy.log" 2> "$OUT/proxy.err" &
PPID_=$!
sleep 0.3
MINICLOUD_COORDINATOR_URL=http://127.0.0.1:$PPORT MINICLOUD_WORKER_SLOTS=$SLOTS MINICLOUD_WORKER_ID=w1 \
  "$WORKER" > "$OUT/worker.log" 2>&1 &
WPID=$!
for i in $(seq 100); do grep -q 'successfully registered' "$OUT/worker.log" && break; sleep 0.1; done

T0=$(date +%s%3N)
for i in $(seq "$NJOBS"); do
  curl -s -X POST http://127.0.0.1:$CPORT/v1/jobs -H 'Content-Type: application/json' \
    -d "{\"task_type\":\"sleep\",\"payload\":{\"duration_ms\":$SLEEP_MS},\"max_attempts\":3,\"timeout_seconds\":120}" >> "$OUT/submit.log"
  echo >> "$OUT/submit.log"
done
echo "submitted $NJOBS jobs at $T0" > "$OUT/summary.txt"

if [ -n "$TERM_AFTER" ]; then
  sleep "$TERM_AFTER"
  echo "RUNNING attempts just before SIGTERM: $(echo "SELECT count(*) FROM attempts WHERE state='RUNNING'" | psqlq)" >> "$OUT/summary.txt"
  TS=$(date +%s%3N); kill -TERM $WPID; wait $WPID; RC=$?; TE=$(date +%s%3N)
  echo "worker SIGTERM -> exit: $((TE-TS)) ms, exit code $RC" >> "$OUT/summary.txt"
  echo "attempt states right after worker exit: $(echo "SELECT string_agg(state||'='||n, ' ') FROM (SELECT state, count(*) n FROM attempts GROUP BY state ORDER BY state) s" | psqlq)" >> "$OUT/summary.txt"
  sleep 45
  echo "attempt states 45 s later: $(echo "SELECT string_agg(state||'='||n, ' ') FROM (SELECT state, count(*) n FROM attempts GROUP BY state ORDER BY state) s" | psqlq)" >> "$OUT/summary.txt"
  echo "job states 45 s later: $(echo "SELECT string_agg(state||'='||n, ' ') FROM (SELECT state, count(*) n FROM jobs GROUP BY state ORDER BY state) s" | psqlq)" >> "$OUT/summary.txt"
else
  for i in $(seq 1800); do
    N=$(echo "SELECT count(*) FROM jobs WHERE state IN ('SUCCEEDED','FAILED','DEAD_LETTER','CANCELLED')" | psqlq)
    [ "$N" = "$NJOBS" ] && break
    sleep 0.1
  done
  TS=$(date +%s%3N); kill -TERM $WPID; wait $WPID; RC=$?; TE=$(date +%s%3N)
  echo "worker SIGTERM -> exit (idle): $((TE-TS)) ms, exit code $RC" >> "$OUT/summary.txt"
fi
kill -TERM $PPID_ 2>/dev/null; wait $PPID_ 2>/dev/null
kill -TERM $CPID; wait $CPID 2>/dev/null

{
echo "== job states"; echo "SELECT state, count(*) FROM jobs GROUP BY state ORDER BY state" | psqlq
echo "== attempt states"; echo "SELECT state, count(*) FROM attempts GROUP BY state ORDER BY state" | psqlq
echo "== attempts per job (max)"; echo "SELECT max(n) FROM (SELECT count(*) n FROM attempts GROUP BY job_id) s" | psqlq
echo "== results rows / distinct jobs"; echo "SELECT count(*), count(DISTINCT job_id) FROM results" | psqlq
echo "== max concurrent attempts (attempts.started_at..finished_at, ends sorted before starts at equal time)"
echo "WITH ev AS (SELECT started_at t, 1 d FROM attempts UNION ALL SELECT finished_at, -1 FROM attempts WHERE finished_at IS NOT NULL)
      SELECT max(c) FROM (SELECT sum(d) OVER (ORDER BY t, d ROWS UNBOUNDED PRECEDING) c FROM ev) s" | psqlq
echo "== wall time: first job created -> last attempt finished (s)"
echo "SELECT round(extract(epoch FROM (SELECT max(finished_at) FROM attempts) - (SELECT min(created_at) FROM jobs))::numeric, 2)" | psqlq
echo "== attempt timeline (job, attempt_no, state, start, finish, relative to first job created)"
echo "SELECT a.job_id, a.attempt_no, a.state, round(extract(epoch FROM a.started_at - m.t0)::numeric,2), round(extract(epoch FROM a.finished_at - m.t0)::numeric,2)
      FROM attempts a, (SELECT min(created_at) t0 FROM jobs) m ORDER BY a.started_at" | psqlq
echo "== proxy: requests by kind"; awk '{split($4,p,"/"); print p[length(p)], $5}' "$OUT/proxy.log" | sort | uniq -c
} >> "$OUT/summary.txt" 2>&1
cat "$OUT/summary.txt"
