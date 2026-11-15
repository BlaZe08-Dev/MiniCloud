#!/usr/bin/env python3
"""V2-0 audit: poll gating from an auditproxy log (start_ms end_ms METHOD PATH STATUS per line).

Worker view of a slot: held from the start of a poll that answered 200 (an assignment) until the
end of the first successful (200) result request for that job. A poll that STARTS while SLOTS such
slots are held is a poll from a full worker (a violation). Prints the held-slot high-water mark,
poll counts and every violation.
usage: pollgate.py PROXY_LOG SLOTS
"""
import json
import sys

log, slots = sys.argv[1], int(sys.argv[2])
rows = []
with open(log) as f:
    for line in f:
        s, e, m, p, st = line.split()
        rows.append((int(s), int(e), p, int(st)))

# The proxy log does not carry the poll's response body, so the job a 200 poll handed out is
# matched to its result request in order: the i-th 200 poll's slot ends at the end of the i-th
# distinct job's first 200 result (FIFO), which only matters for the count, not the identity.
events = []
for s, e, p, st in rows:
    if p.endswith("/poll") and st == 200:
        events.append((s, 1, "acquire"))
results_seen = set()
for s, e, p, st in sorted(rows, key=lambda r: r[1]):
    if p.endswith("/result") and st == 200:
        job = p.split("/")[3]
        if job not in results_seen:
            results_seen.add(job)
            events.append((e, -1, "release"))
polls = sorted(s for s, e, p, st in rows if p.endswith("/poll"))
events.sort()
held, high, viol = 0, 0, []
# At equal milliseconds: releases first, then the poll check, then acquires (a 200 poll's own
# acquire has its own start time and must not count against itself).
timeline = sorted([(t, 0 if d < 0 else 2, d) for t, d, _ in events] + [(t, 1, 0) for t in polls])
for t, kind, d in timeline:
    if kind == 1:
        if held >= slots:
            viol.append(t)
        continue
    held += d
    high = max(high, held)
print(json.dumps({"slots": slots, "polls_total": len(polls),
                  "polls_200": sum(1 for r in rows if r[2].endswith("/poll") and r[3] == 200),
                  "results_200_distinct_jobs": len(results_seen),
                  "held_slots_high_water": high,
                  "polls_started_while_full": len(viol)}))
