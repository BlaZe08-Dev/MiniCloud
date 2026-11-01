package storagetest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// Shared cases for the ADR-0017 Section 5.1 wire values (slice 5c, part 2): the policy and fallback
// reason a claim reports, and the four assignment fields of the job view. Both repositories run
// them, so the in-memory and the PostgreSQL backends must give the same values.

// jobViewAssignment is the assignment part of a job as the job view encodes it (domain.Job JSON).
type jobViewAssignment struct {
	keys         map[string]json.RawMessage
	worker       *string
	expiresAt    *time.Time
	lapses       int
	reason       *string
	hasAssigned  bool
	hasAllFields bool
}

// encodeJobView encodes a job exactly as GET /v1/jobs/{job_id} does (encoding/json of domain.Job)
// and decodes the assignment fields back.
func encodeJobView(t *testing.T, job *domain.Job) jobViewAssignment {
	t.Helper()
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("encode job view: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("decode job view keys: %v", err)
	}
	var v struct {
		Worker    *string    `json:"assigned_worker_id"`
		ExpiresAt *time.Time `json:"assignment_expires_at"`
		Lapses    int        `json:"assignment_lapses"`
		Reason    *string    `json:"assignment_fallback_reason"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode job view: %v", err)
	}
	out := jobViewAssignment{keys: keys, worker: v.Worker, expiresAt: v.ExpiresAt, lapses: v.Lapses, reason: v.Reason}
	_, out.hasAssigned = keys["assigned_at"]
	out.hasAllFields = true
	for _, k := range []string{"assigned_worker_id", "assignment_expires_at", "assignment_lapses", "assignment_fallback_reason"} {
		if _, ok := keys[k]; !ok {
			out.hasAllFields = false
		}
	}
	return out
}

func strOrNil(p *string) string {
	if p == nil {
		return "<null>"
	}
	return *p
}

// checkJobView fails unless the job view has all four fields, no assigned_at, and the given values.
// worker and reason are "" for null.
func checkJobView(t *testing.T, step string, v jobViewAssignment, worker string, lapses int, reason string) {
	t.Helper()
	if !v.hasAllFields {
		t.Fatalf("%s: job view lacks an assignment field; keys=%v", step, keysOf(v.keys))
	}
	if v.hasAssigned {
		t.Fatalf("%s: job view exposes the internal assigned_at", step)
	}
	if (worker == "") != (v.worker == nil) || (v.worker != nil && *v.worker != worker) {
		t.Fatalf("%s: assigned_worker_id = %s, want %q", step, strOrNil(v.worker), worker)
	}
	if (worker == "") != (v.expiresAt == nil) {
		t.Fatalf("%s: assignment_expires_at = %v with assigned_worker_id %s", step, v.expiresAt, strOrNil(v.worker))
	}
	if v.lapses != lapses {
		t.Fatalf("%s: assignment_lapses = %d, want %d", step, v.lapses, lapses)
	}
	if (reason == "") != (v.reason == nil) || (v.reason != nil && *v.reason != reason) {
		t.Fatalf("%s: assignment_fallback_reason = %s, want %q", step, strOrNil(v.reason), reason)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// RunClaimPolicyPerArmConformance: ADR-0017 Sections 3.2 and 5.1. A claim reports the arm that made
// it: the own reservation gives round_robin with no reason, the lapse arm round_robin_fallback with
// reason lapses, the age arm round_robin_fallback with reason age. The claimed job and the job read
// back carry the same reason (null for the own arm).
func RunClaimPolicyPerArmConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	cases := []struct {
		name       string
		setup      func(t *testing.T, h AssignmentHarness)
		ageArm     bool
		wantPolicy string
		wantReason string
	}{
		{
			name: "own reservation",
			setup: func(t *testing.T, h AssignmentHarness) {
				AssignPoll(t, h, "w_1")
				AssignRunPass(t, h)
			},
			wantPolicy: domain.DispatchPolicyRoundRobin,
		},
		{
			name: "lapse arm",
			setup: func(t *testing.T, h AssignmentHarness) {
				h.SetLapses(t, "job_1", AssignTestMaxLapses)
				AssignPoll(t, h, "w_1")
			},
			wantPolicy: domain.DispatchPolicyRoundRobinFallback,
			wantReason: domain.FallbackReasonLapses,
		},
		{
			name: "age arm",
			setup: func(t *testing.T, h AssignmentHarness) {
				h.Advance(t, AssignTestFallbackAge+time.Minute)
				AssignPoll(t, h, "w_1")
			},
			ageArm:     true,
			wantPolicy: domain.DispatchPolicyRoundRobinFallback,
			wantReason: domain.FallbackReasonAge,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			AssignAddWorker(t, h, "w_1", 1, "echo")
			AssignSubmit(t, h, "job_1", "echo")
			tc.setup(t, h)
			claim, err := h.assignRepo(t).ClaimForPoll(context.Background(), "w_1", AssignTestClaimOpts(tc.ageArm))
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if claim.Job.ID != "job_1" || claim.Policy != tc.wantPolicy || claim.FallbackReason != tc.wantReason {
				t.Fatalf("claim job=%s policy=%q reason=%q; want job_1 %q %q", claim.Job.ID, claim.Policy, claim.FallbackReason, tc.wantPolicy, tc.wantReason)
			}
			checkJobView(t, "claimed job", encodeJobView(t, claim.Job), "", 0, tc.wantReason)
			checkJobView(t, "job read back", encodeJobView(t, AssignJob(t, h, "job_1")), "", 0, tc.wantReason)
		})
	}
}

// RunJobViewAssignmentFieldsConformance: ADR-0017 Section 5.1 and Section 1.1. The job view always
// carries the four assignment fields: assigned_worker_id and assignment_expires_at (null unless a
// reservation is live), assignment_lapses (0 when none) and assignment_fallback_reason (null until
// a fallback claim, then never cleared). The internal assigned_at is not part of the view.
func RunJobViewAssignmentFieldsConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignAddWorker(t, h, "w_2", 1, "echo")
	AssignSubmit(t, h, "job_1", "echo")

	// 1. Submitted: no reservation, no lapse, no reason.
	checkJobView(t, "submitted", encodeJobView(t, AssignJob(t, h, "job_1")), "", 0, "")

	// 2. Reserved to w_1: the worker and the expiry, one accept window after the pass.
	AssignPoll(t, h, "w_1")
	before := h.Now(t)
	AssignRunPass(t, h)
	after := h.Now(t)
	v := encodeJobView(t, AssignJob(t, h, "job_1"))
	checkJobView(t, "reserved", v, "w_1", 0, "")
	// PostgreSQL stores microseconds, so allow the truncation below the earliest bound.
	if v.expiresAt.Before(before.Add(AssignTestAcceptWindow-time.Microsecond)) || v.expiresAt.After(after.Add(AssignTestAcceptWindow)) {
		t.Fatalf("reserved: assignment_expires_at = %v, want one accept window (%v) after the pass, within [%v, %v]",
			v.expiresAt, AssignTestAcceptWindow, before.Add(AssignTestAcceptWindow), after.Add(AssignTestAcceptWindow))
	}

	// 3. The window lapses with w_1 no longer fresh: the pass revokes and counts one lapse, and no
	// other worker is eligible, so the reservation stays empty.
	h.Advance(t, AssignTestAcceptWindow+time.Millisecond)
	AssignRunPass(t, h)
	checkJobView(t, "lapsed", encodeJobView(t, AssignJob(t, h, "job_1")), "", 1, "")

	// 4. At the lapse limit, w_2 claims by the lapse arm: lapses reset, reason lapses.
	h.SetLapses(t, "job_1", AssignTestMaxLapses)
	AssignPoll(t, h, "w_2")
	claim, err := h.assignRepo(t).ClaimForPoll(ctx, "w_2", AssignTestClaimOpts(false))
	if err != nil || claim.FallbackReason != domain.FallbackReasonLapses {
		t.Fatalf("fallback claim by w_2: err=%v claim=%+v", err, claim)
	}
	checkJobView(t, "fallback claim", encodeJobView(t, AssignJob(t, h, "job_1")), "", 0, domain.FallbackReasonLapses)

	// 5. A retryable failure puts the job in RETRYING: the reason is history and stays.
	if _, err := h.Repo.CommitAttempt(ctx, "job_1", claim.Attempt.ID, claim.Attempt.LeaseToken, "w_2",
		domain.AttemptOutcome{ExitCode: 1, Error: "transient", ErrorClassification: domain.ClassTransient},
		func(int) time.Duration { return 0 }); err != nil {
		t.Fatalf("CommitAttempt (transient failure): %v", err)
	}
	job := AssignJob(t, h, "job_1")
	if job.State != domain.StateRetrying {
		t.Fatalf("after a transient failure: state = %s, want RETRYING", job.State)
	}
	checkJobView(t, "retrying", encodeJobView(t, job), "", 0, domain.FallbackReasonLapses)

	// 6. A later own-reservation claim (round_robin, no reason) does not clear the recorded reason.
	h.Advance(t, time.Millisecond)
	AssignPoll(t, h, "w_1")
	AssignPoll(t, h, "w_2")
	AssignRunPass(t, h)
	reserved := AssignJob(t, h, "job_1")
	if reserved.AssignedWorkerID == nil {
		t.Fatalf("retrying job was not reserved by the pass")
	}
	holder := *reserved.AssignedWorkerID
	checkJobView(t, "reserved again", encodeJobView(t, reserved), holder, 0, domain.FallbackReasonLapses)
	own, err := h.assignRepo(t).ClaimForPoll(ctx, holder, AssignTestClaimOpts(false))
	if err != nil || own.Policy != domain.DispatchPolicyRoundRobin || own.FallbackReason != "" {
		t.Fatalf("own claim by %s: err=%v claim=%+v", holder, err, own)
	}
	checkJobView(t, "own claim after fallback", encodeJobView(t, AssignJob(t, h, "job_1")), "", 0, domain.FallbackReasonLapses)
}
