#!/bin/bash
# Runs the audit mutations A01..A20 one at a time. Each restores its file before the next.
M=/home/blazeee/MiniCloud-claude/docs/agents/reviews/P4-08b-evidence/mut.py.txt
IA=internal/storage/inmemory_assignment.go
CA=internal/coordinator/assignment.go
RR=internal/scheduler/round_robin_rule.go
ST="./internal/storage/ ./internal/coordinator/"
T=$'\t'
cd /home/blazeee/MiniCloud-claude || exit 1

python3 $M A01 $IA 'if !r.passMu.TryLock() {' 'if r.passMu.Lock(); false {' "$ST" '.'
python3 $M A02 $RR 'if id > cursor {' 'if id >= cursor {' "./internal/scheduler/ ./internal/storage/" '.'
python3 $M A03 $RR 'return sorted[0], true' 'return "", false' "./internal/scheduler/ ./internal/storage/" '.'
python3 $M A04 $IA "${T}sort.Strings(ids)" "${T}_ = sort.Strings" "$ST" '.'
python3 $M A05 $IA 'j.TaskType == ty && j.AssignedWorkerID == nil && r.isDispatchableLocked(j) && j.AssignmentLapses < p.MaxLapses' 'j.TaskType == ty && j.AssignedWorkerID == nil && r.isDispatchableLocked(j) && j.AssignmentLapses <= p.MaxLapses' "$ST" '.'
python3 $M A06 $IA 'if j.AssignmentLapses >= opts.MaxLapses {' 'if j.AssignmentLapses > opts.MaxLapses {' "$ST" '.'
python3 $M A07 $IA 'if j.AssignmentLapses >= opts.MaxLapses {' 'if j.AssignmentLapses >= opts.MaxLapses-1 {' "$ST" '.'
python3 $M A08 $IA 'j.AssignmentExpiresAt.After(now) {
			return r.claimLocked(j, workerID, domain.DispatchPolicyRoundRobin,' '!j.AssignmentExpiresAt.Before(now) {
			return r.claimLocked(j, workerID, domain.DispatchPolicyRoundRobin,' "$ST" '.'
python3 $M A09 $IA 'res.Revoked++' 'res.Revoked++; j.AssignmentLapses++' "$ST" '.'
python3 $M A10 $IA '!dispatchableSince(j).After(silentBefore)' '!j.CreatedAt.After(silentBefore)' "$ST" '.'
python3 $M A11 $IA 'if r.computeAvailableSlotsLocked(workerID, worker.TotalSlots) <= 0 {' 'if false && r.computeAvailableSlotsLocked(workerID, worker.TotalSlots) <= 0 {' "$ST" '.'
python3 $M A12 $CA 'if now.Sub(last) <= 2*interval {' 'if now.Sub(last) <= interval {' "./internal/coordinator/" '.'
python3 $M A13 $IA 'if w.LastPollAt == nil || w.LastPollAt.Before(now.Add(-assignmentPollWriteThrottle)) {' 'if true || w.LastPollAt.Before(now.Add(-assignmentPollWriteThrottle)) {' "$ST" '.'
python3 $M A14 internal/config/config.go 'if cfg.AssignmentInterval > cfg.AssignmentAcceptWindow {' 'if cfg.AssignmentInterval >= cfg.AssignmentAcceptWindow {' "./internal/config/" '.'
python3 $M A15 $CA 'FallbackReason:       claim.FallbackReason,' 'FallbackReason:       "",' "./internal/coordinator/" '.'
python3 $M A16 internal/coordinator/service.go 'Policy:               domain.DispatchPolicyFifoPull,' 'Policy:               "",' "./internal/coordinator/ ./internal/api/" '.'
python3 $M A17 internal/domain/assignment.go 'return name == DispatchPolicyFifoPull || name == DispatchPolicyRoundRobin' 'return name == DispatchPolicyFifoPull || name == DispatchPolicyRoundRobin || name == DispatchPolicyRoundRobinFallback' "./internal/domain/ ./internal/coordinator/ ./internal/config/" '.'
python3 $M A18 $IA 'r.passCompletedAt = now' '_ = now' "$ST" '.'
python3 $M A19 $CA 'if s.refreshInFlight {' 'if s.refreshInFlight || s.refreshLastFailed {' "./internal/coordinator/" '.'
python3 $M A20 $CA 'ageArm := s.refreshIfStaleLocked(ctx, ar)' 'ageArm := false' "./internal/coordinator/" '^TestPoll_AgeArm_AfterOutage_RefreshesFirst$'
git status --short
