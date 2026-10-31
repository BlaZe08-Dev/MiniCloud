#!/bin/bash
# Auditor probe runner for P4-08b. Copies the probe files into the packages, runs them on the
# unmutated tree, re-runs each surviving mutation with its probe present (red, restore, green),
# then removes the copies. Mutation harness: mut.py (kept beside this script as mut.py.txt).
set -u
R=/home/blazeee/MiniCloud-claude
E=$R/docs/agents/reviews/P4-08b-evidence
M=$E/mut.py.txt
cd $R || exit 1
cp $E/audit_probe_storage_test.go.txt internal/storage/audit_probe_storage_test.go
cp $E/audit_probe_coordinator_test.go.txt internal/coordinator/audit_probe_coordinator_test.go
cp $E/audit_probe_config_test.go.txt internal/config/audit_probe_config_test.go
cleanup() { rm -f internal/storage/audit_probe_storage_test.go internal/coordinator/audit_probe_coordinator_test.go internal/config/audit_probe_config_test.go; }
trap cleanup EXIT

echo "== probes on the unmutated tree (46ca30a)"
env -u MINICLOUD_TEST_DATABASE_URL go test -count=1 -v -run 'TestAudit_Probe_' ./internal/storage/ ./internal/coordinator/ ./internal/config/ > $E/probes-clean.log 2>&1
echo "EXIT=$?" >> $E/probes-clean.log
grep -E '^(--- |ok|FAIL|EXIT)' $E/probes-clean.log

IA=internal/storage/inmemory_assignment.go
T=$'\t'
echo "== surviving mutations re-run against their probe"
python3 $M P04 $IA "${T}sort.Strings(ids)" "${T}_ = sort.Strings" ./internal/storage/ '^TestAudit_Probe_EventCandidatesInIDOrder$'
python3 $M P05 $IA 'j.TaskType == ty && j.AssignedWorkerID == nil && r.isDispatchableLocked(j) && j.AssignmentLapses < p.MaxLapses' 'j.TaskType == ty && j.AssignedWorkerID == nil && r.isDispatchableLocked(j) && j.AssignmentLapses <= p.MaxLapses' ./internal/storage/ '^TestAudit_Probe_PassExcludesJobAtLapseLimit$'
python3 $M P07 $IA 'if j.AssignmentLapses >= opts.MaxLapses {' 'if j.AssignmentLapses >= opts.MaxLapses-1 {' ./internal/storage/ '^TestAudit_Probe_LapseArmClosedBelowLimit$'
python3 $M P08 $IA 'j.AssignmentExpiresAt.After(now) {
			return r.claimLocked(j, workerID, domain.DispatchPolicyRoundRobin,' '!j.AssignmentExpiresAt.Before(now) {
			return r.claimLocked(j, workerID, domain.DispatchPolicyRoundRobin,' ./internal/storage/ '^TestAudit_Probe_OwnArmClosedAtExactExpiry$'
python3 $M P09 $IA 'res.Revoked++' 'res.Revoked++; j.AssignmentLapses++' ./internal/storage/ '^TestAudit_Probe_UnhealthyRevokeCountsNoLapse$'
python3 $M P10 $IA '!dispatchableSince(j).After(silentBefore)' '!j.CreatedAt.After(silentBefore)' ./internal/storage/ '^TestAudit_Probe_AgeArmUsesNotBefore$'
python3 $M P11 $IA 'if r.computeAvailableSlotsLocked(workerID, worker.TotalSlots) <= 0 {' 'if false && r.computeAvailableSlotsLocked(workerID, worker.TotalSlots) <= 0 {' ./internal/storage/ '^TestAudit_Probe_ClaimRespectsCapacity$'
python3 $M P14 internal/config/config.go 'if cfg.AssignmentInterval > cfg.AssignmentAcceptWindow {' 'if cfg.AssignmentInterval >= cfg.AssignmentAcceptWindow {' ./internal/config/ '^TestAudit_Probe_IntervalEqualToWindowAccepted$'
python3 $M P17 internal/domain/assignment.go 'return name == DispatchPolicyFifoPull || name == DispatchPolicyRoundRobin' 'return name == DispatchPolicyFifoPull || name == DispatchPolicyRoundRobin || name == DispatchPolicyRoundRobinFallback' ./internal/coordinator/ '^TestAudit_Probe_SetDispatchPolicyRejectsFallbackName$'
git status --short
