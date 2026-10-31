package storage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

const (
	// assignmentPollWriteThrottle is the minimum gap between two writes of a worker's poll
	// freshness (ADR-0017 Section 2.2): a write happens only when last_poll_at is older than this.
	assignmentPollWriteThrottle = time.Second

	// assignmentEventCandidateCap caps the candidate IDs in a JOB_ASSIGNED payload (ADR-0017
	// Section 2.1, INV-18). It is a constant, not configuration.
	assignmentEventCandidateCap = 10
)

// touchPollLocked records a poll of a HEALTHY worker, throttled (ADR-0017 Section 2.2). The caller
// holds r.mu for writing.
func (r *InMemoryRepository) touchPollLocked(w *domain.Worker, now time.Time) {
	if w.LastPollAt == nil || w.LastPollAt.Before(now.Add(-assignmentPollWriteThrottle)) {
		at := now
		w.LastPollAt = &at
	}
}

// activeAttemptsLocked counts a worker's PENDING and RUNNING attempts. Caller holds r.mu.
func (r *InMemoryRepository) activeAttemptsLocked(workerID string) int {
	n := 0
	for _, att := range r.attempts {
		if att.WorkerID == workerID && (att.State == domain.AttemptRunning || att.State == domain.AttemptPending) {
			n++
		}
	}
	return n
}

// liveReservationLocked reports whether any job holds an unexpired reservation to the worker.
// Caller holds r.mu.
func (r *InMemoryRepository) liveReservationLocked(workerID string, now time.Time) bool {
	for _, j := range r.jobs {
		if j.AssignedWorkerID != nil && *j.AssignedWorkerID == workerID && j.AssignmentExpiresAt != nil && j.AssignmentExpiresAt.After(now) {
			return true
		}
	}
	return false
}

// eligibleWorkersLocked returns the IDs eligible for a reservation of taskType, sorted (ADR-0017
// Section 2.2). Caller holds r.mu.
func (r *InMemoryRepository) eligibleWorkersLocked(taskType string, now time.Time, p AssignParams) []string {
	var ids []string
	for id, w := range r.workers {
		if w.Status != domain.WorkerStatusHealthy || !w.SupportsTask(taskType) {
			continue
		}
		if !w.LastHeartbeatAt.After(now.Add(-p.WorkerTimeout)) {
			continue
		}
		if w.LastPollAt == nil || !w.LastPollAt.After(now.Add(-p.AcceptWindow)) {
			continue
		}
		// One effective slot (ADR-0017 Section 2.4): no PENDING or RUNNING attempt, and capacity.
		if r.activeAttemptsLocked(id) >= domain.EffectiveWorkerSlots {
			continue
		}
		if r.computeAvailableSlotsLocked(id, w.TotalSlots) <= 0 {
			continue
		}
		if r.liveReservationLocked(id, now) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// dispatchableSince is GREATEST(created_at, COALESCE(not_before, created_at)) (ADR-0017
// Section 3.2). It needs no column.
func dispatchableSince(j *domain.Job) time.Time {
	since := j.CreatedAt
	if j.NotBefore != nil && j.NotBefore.After(since) {
		since = *j.NotBefore
	}
	return since
}

// RecordPoll writes a poll freshness for a HEALTHY worker (ADR-0017 Section 2.2).
func (r *InMemoryRepository) RecordPoll(ctx context.Context, workerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, exists := r.workers[workerID]
	if !exists {
		return domain.ErrWorkerNotFound
	}
	if w.Status != domain.WorkerStatusHealthy {
		return domain.ErrWorkerUnhealthy
	}
	r.touchPollLocked(w, r.clock.Now())
	return nil
}

// AssignQueuedJobs runs one assignment pass (ADR-0017 Section 2.1). It is one unit under r.mu:
// revoke, then reserve per task type in FIFO order, then the cursors and the pass heartbeat.
func (r *InMemoryRepository) AssignQueuedJobs(ctx context.Context, p AssignParams) (AssignResult, error) {
	if !r.passMu.TryLock() {
		return AssignResult{Skipped: true}, nil
	}
	defer r.passMu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.clock.Now()
	res := AssignResult{CandidatesRead: make(map[string]int)}

	// 1. Revoke: at most BatchLimit reservations per pass (ADR-0017 D14).
	examined := 0
	for _, id := range r.jobOrder {
		if examined >= p.BatchLimit {
			break
		}
		j := r.jobs[id]
		if j == nil || j.AssignedWorkerID == nil {
			continue
		}
		holder := *j.AssignedWorkerID
		if w := r.workers[holder]; w == nil || w.Status != domain.WorkerStatusHealthy {
			j.ClearAssignment()
			r.recordEventLocked(&domain.JobEvent{
				JobID:     j.ID,
				EventType: domain.JobEventAssignmentRevoked,
				CreatedAt: now,
				Details:   map[string]any{"worker_id": holder, "reason": "worker_unhealthy"},
			})
			res.Revoked++
			examined++
			continue
		}
		if j.AssignmentExpiresAt != nil && !j.AssignmentExpiresAt.After(now) {
			j.ClearAssignment()
			j.AssignmentLapses++
			r.recordEventLocked(&domain.JobEvent{
				JobID:     j.ID,
				EventType: domain.JobEventAssignmentLapsed,
				CreatedAt: now,
				Details:   map[string]any{"worker_id": holder, "lapses": j.AssignmentLapses, "counted": true},
			})
			res.Lapsed++
			examined++
		}
	}

	// 2. Reserve, per task type, in the order each type first appears among unassigned dispatchable
	// jobs. Candidates of one type are read at most BatchLimit at a time (ADR-0017 Section 2.1).
	var types []string
	seenType := make(map[string]bool)
	for _, id := range r.jobOrder {
		j := r.jobs[id]
		if j != nil && j.AssignedWorkerID == nil && r.isDispatchableLocked(j) && j.AssignmentLapses < p.MaxLapses && !seenType[j.TaskType] {
			seenType[j.TaskType] = true
			types = append(types, j.TaskType)
		}
	}
	for _, ty := range types {
		var candidates []*domain.Job
		for _, id := range r.jobOrder {
			if len(candidates) >= p.BatchLimit {
				break
			}
			j := r.jobs[id]
			if j != nil && j.TaskType == ty && j.AssignedWorkerID == nil && r.isDispatchableLocked(j) && j.AssignmentLapses < p.MaxLapses {
				candidates = append(candidates, j)
			}
		}
		res.CandidatesRead[ty] = len(candidates)

		for _, j := range candidates {
			eligible := r.eligibleWorkersLocked(ty, now, p)
			if len(eligible) == 0 {
				continue
			}
			before := r.cursors.Cursor(ty)
			selected, ok := r.cursors.Select(ty, eligible)
			if !ok {
				continue
			}
			holder := selected
			assignedAt := now
			expires := now.Add(p.AcceptWindow)
			j.AssignedWorkerID = &holder
			j.AssignedAt = &assignedAt
			j.AssignmentExpiresAt = &expires

			candidates := eligible
			if len(candidates) > assignmentEventCandidateCap {
				candidates = candidates[:assignmentEventCandidateCap]
			}
			r.recordEventLocked(&domain.JobEvent{
				JobID:     j.ID,
				EventType: domain.JobEventAssigned,
				CreatedAt: now,
				Details: map[string]any{
					"worker_id":       selected,
					"expires_at":      expires,
					"policy":          domain.DispatchPolicyRoundRobin,
					"task_type":       ty,
					"cursor_before":   before,
					"selected":        selected,
					"candidates":      append([]string(nil), candidates...),
					"candidate_count": len(eligible),
					"reason":          "round_robin",
				},
			})
			res.Reserved++
		}
	}

	// 3. Pass heartbeat, as the pass's last statement (ADR-0017 Section 3.2).
	r.passCompletedAt = now
	return res, nil
}

// ClaimForPoll claims a job for a poll by workerID (ADR-0017 Sections 3.2 and 3.5). The own
// reservation is tried first. The fallback arms are then tried in FIFO order: the lapse arm, and
// the age arm, which needs this poll's own failed refresh (opts.AgeArmOpen) and a silent pass.
func (r *InMemoryRepository) ClaimForPoll(ctx context.Context, workerID string, opts ClaimOptions) (*AssignedClaim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	worker, exists := r.workers[workerID]
	if !exists {
		return nil, domain.ErrWorkerNotFound
	}
	if worker.Status != domain.WorkerStatusHealthy {
		return nil, domain.ErrWorkerUnhealthy
	}
	now := r.clock.Now()
	r.touchPollLocked(worker, now)
	if r.computeAvailableSlotsLocked(workerID, worker.TotalSlots) <= 0 {
		return nil, domain.ErrWorkerNoCapacity
	}

	// Own reservation first.
	for _, id := range r.jobOrder {
		j := r.jobs[id]
		if j == nil || !r.isDispatchableLocked(j) || !worker.SupportsTask(j.TaskType) {
			continue
		}
		if j.AssignedWorkerID != nil && *j.AssignedWorkerID == workerID && j.AssignmentExpiresAt != nil && j.AssignmentExpiresAt.After(now) {
			return r.claimLocked(j, workerID, domain.DispatchPolicyRoundRobin, "", now)
		}
	}

	// Fallback arms.
	silentBefore := now.Add(-opts.FallbackAge)
	passSilent := !r.passCompletedAt.After(silentBefore)
	for _, id := range r.jobOrder {
		j := r.jobs[id]
		if j == nil || !r.isDispatchableLocked(j) || !worker.SupportsTask(j.TaskType) {
			continue
		}
		if j.AssignedWorkerID != nil && j.AssignmentExpiresAt != nil && j.AssignmentExpiresAt.After(now) {
			continue // a live reservation belongs to its worker
		}
		if j.AssignmentLapses >= opts.MaxLapses {
			return r.claimLocked(j, workerID, domain.DispatchPolicyRoundRobinFallback, domain.FallbackReasonLapses, now)
		}
		if opts.AgeArmOpen && passSilent && !dispatchableSince(j).After(silentBefore) {
			return r.claimLocked(j, workerID, domain.DispatchPolicyRoundRobinFallback, domain.FallbackReasonAge, now)
		}
	}
	return nil, domain.ErrNoQueuedJobs
}

// claimLocked moves a dispatchable job to RUNNING under r.mu, creating its attempt exactly as
// DispatchNextJob does, and clears the reservation and lapse count (ADR-0017 Section 1.3).
func (r *InMemoryRepository) claimLocked(j *domain.Job, workerID, policy, reason string, now time.Time) (*AssignedClaim, error) {
	fromState := j.State
	if err := j.TransitionTo(domain.StateRunning); err != nil {
		return nil, err
	}
	j.NotBefore = nil
	j.ClearAssignment()
	j.AssignmentLapses = 0
	if reason != "" {
		fallback := reason
		j.AssignmentFallbackReason = &fallback
	}

	attemptNo := 0
	for _, att := range r.attempts {
		if att.JobID == j.ID && att.AttemptNo > attemptNo {
			attemptNo = att.AttemptNo
		}
	}
	attemptNo++

	r.idSeq++
	attemptID := fmt.Sprintf("att_%d_%s", r.idSeq, generateRandomHex(6))
	rawLeaseToken, err := domain.NewLeaseToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate secure lease token: %w", err)
	}
	stored := &domain.Attempt{
		ID:             attemptID,
		JobID:          j.ID,
		AttemptNo:      attemptNo,
		WorkerID:       workerID,
		LeaseTokenHash: domain.HashLeaseToken(rawLeaseToken),
		State:          domain.AttemptRunning,
		StartedAt:      now,
		LeaseExpiresAt: r.newLeaseExpiryLocked(),
	}
	r.attempts[attemptID] = stored
	j.CurrentAttemptID = attemptID
	j.UpdatedAt = now

	toState := domain.StateRunning
	details := map[string]any{
		"worker_id":  workerID,
		"attempt_no": attemptNo,
		"policy":     policy,
	}
	if reason != "" {
		details["fallback_reason"] = reason
	}
	r.recordEventLocked(&domain.JobEvent{
		JobID:     j.ID,
		AttemptID: &attemptID,
		EventType: domain.JobEventDispatched,
		FromState: &fromState,
		ToState:   &toState,
		CreatedAt: now,
		Details:   details,
	})

	retJob := *j
	retAttempt := *stored
	retAttempt.LeaseToken = rawLeaseToken
	return &AssignedClaim{Job: &retJob, Attempt: &retAttempt, Policy: policy, FallbackReason: reason}, nil
}

// AssignmentPassState returns the pass heartbeat (ADR-0017 Section 3.2).
func (r *InMemoryRepository) AssignmentPassState(ctx context.Context) (time.Time, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.passCompletedAt, nil
}

var _ AssignmentRepository = (*InMemoryRepository)(nil)
