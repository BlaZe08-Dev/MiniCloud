package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// ADR-0017 Section 5.1 on the wire (slice 5c, part 2): the poll body's policy and fallback_reason,
// and the four assignment fields of the job view. The values per backend are pinned by the shared
// conformance cases TestConformance_ClaimPolicy_PerArm and TestConformance_JobView_AssignmentFields;
// these tests pin the JSON the HTTP API writes.

var errWirePassFault = errors.New("injected assignment pass failure")

// wireRepo is the in-memory repository with a switch that makes every assignment pass fail, the
// stalled pass under which a poll's own refresh attempt fails and the age arm opens (Section 3.2).
type wireRepo struct {
	*storage.InMemoryRepository
	failPass atomic.Bool
}

func (r *wireRepo) AssignQueuedJobs(ctx context.Context, p storage.AssignParams) (storage.AssignResult, error) {
	if r.failPass.Load() {
		return storage.AssignResult{}, errWirePassFault
	}
	return r.InMemoryRepository.AssignQueuedJobs(ctx, p)
}

type wireHarness struct {
	t     *testing.T
	ts    *httptest.Server
	clock *apiTestClock
	repo  *wireRepo
	svc   *coordinator.Service
}

// newWireHarness starts an API server over the in-memory repository with policy (fifo_pull or
// round_robin) and the ADR-0017 default settings. Repository and service share one clock, so job
// age and pass silence move together.
func newWireHarness(t *testing.T, policy string) *wireHarness {
	t.Helper()
	clock := &apiTestClock{now: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	repo := &wireRepo{InMemoryRepository: storage.NewInMemoryRepository(storage.WithClock(clock))}
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
	svc.SetClock(clock)
	if err := svc.SetDispatchPolicy(policy); err != nil {
		t.Fatalf("SetDispatchPolicy(%s): %v", policy, err)
	}
	ts := httptest.NewServer(api.NewServer("127.0.0.1:0", svc).Handler())
	t.Cleanup(ts.Close)
	return &wireHarness{t: t, ts: ts, clock: clock, repo: repo, svc: svc}
}

func (h *wireHarness) do(method, path, body string) (int, []byte) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.ts.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (h *wireHarness) register(id string) {
	h.t.Helper()
	if code, body := h.do(http.MethodPost, "/v1/workers/register", `{"id":"`+id+`","version":"v","total_slots":1,"supported_tasks":["echo"]}`); code != http.StatusOK {
		h.t.Fatalf("register %s: %d %s", id, code, body)
	}
}

func (h *wireHarness) heartbeat(id string) {
	h.t.Helper()
	if code, body := h.do(http.MethodPost, "/v1/workers/"+id+"/heartbeat", `{}`); code != http.StatusOK {
		h.t.Fatalf("heartbeat %s: %d %s", id, code, body)
	}
}

func (h *wireHarness) submit() string {
	h.t.Helper()
	code, body := h.do(http.MethodPost, "/v1/jobs", `{"task_type":"echo","payload":{"message":"x"},"max_attempts":3,"timeout_seconds":30}`)
	if code != http.StatusCreated {
		h.t.Fatalf("submit: %d %s", code, body)
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &job); err != nil || job.ID == "" {
		h.t.Fatalf("submit body %s: %v", body, err)
	}
	return job.ID
}

// poll returns the keys of the 200 poll body's assignment object.
func (h *wireHarness) poll(id string) map[string]json.RawMessage {
	h.t.Helper()
	code, body := h.do(http.MethodPost, "/v1/workers/"+id+"/poll", ``)
	if code != http.StatusOK {
		h.t.Fatalf("poll %s: %d %s", id, code, body)
	}
	var env map[string]map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		h.t.Fatalf("poll body %s: %v", body, err)
	}
	a, ok := env["assignment"]
	if !ok || len(env) != 1 {
		h.t.Fatalf("poll body is not {\"assignment\": {...}}: %s", body)
	}
	return a
}

// getJob returns the keys of the job view.
func (h *wireHarness) getJob(id string) map[string]json.RawMessage {
	h.t.Helper()
	code, body := h.do(http.MethodGet, "/v1/jobs/"+id, ``)
	if code != http.StatusOK {
		h.t.Fatalf("get job %s: %d %s", id, code, body)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil {
		h.t.Fatalf("job body %s: %v", body, err)
	}
	return keys
}

// wireString decodes a JSON string field, failing on a missing key or another type.
func wireString(t *testing.T, keys map[string]json.RawMessage, k string) string {
	t.Helper()
	raw, ok := keys[k]
	if !ok {
		t.Fatalf("key %q missing", k)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("key %q = %s, want a JSON string: %v", k, raw, err)
	}
	return s
}

// pollAssignmentKeys are the assignment keys a 200 poll carried before ADR-0017 (protocol Sections
// 4.3 and 6.2); they must all still be there.
var pollAssignmentKeys = []string{"job_id", "attempt_id", "attempt_no", "lease_token", "task_type", "payload", "timeout_seconds", "lease_expires_at", "lease_duration_seconds"}

// TestAPI_Poll_PolicyFieldOnWire: ADR-0017 Section 5.1 and TestConformance_PollResponse_PolicyField
// on the wire. Every 200 poll body carries assignment.policy with the value of the path that
// claimed; assignment.fallback_reason is present only on a round_robin_fallback claim (lapses or
// age) and absent otherwise; the pre-existing assignment keys are unchanged.
func TestAPI_Poll_PolicyFieldOnWire(t *testing.T) {
	cases := []struct {
		name       string
		policy     string
		setup      func(h *wireHarness, jobID string)
		wantPolicy string
		wantReason string // "" means the key must be absent
	}{
		{name: "fifo_pull", policy: domain.DispatchPolicyFifoPull, wantPolicy: domain.DispatchPolicyFifoPull},
		{name: "round_robin own reservation", policy: domain.DispatchPolicyRoundRobin, wantPolicy: domain.DispatchPolicyRoundRobin},
		{
			name:   "round_robin_fallback lapses",
			policy: domain.DispatchPolicyRoundRobin,
			setup: func(h *wireHarness, jobID string) {
				job, err := h.repo.GetJob(context.Background(), jobID)
				if err != nil {
					h.t.Fatalf("GetJob: %v", err)
				}
				job.AssignmentLapses = coordinator.DefaultAssignmentSettings().MaxLapses
				if err := h.repo.UpdateJob(context.Background(), job); err != nil {
					h.t.Fatalf("UpdateJob: %v", err)
				}
			},
			wantPolicy: domain.DispatchPolicyRoundRobinFallback,
			wantReason: domain.FallbackReasonLapses,
		},
		{
			name:   "round_robin_fallback age",
			policy: domain.DispatchPolicyRoundRobin,
			setup: func(h *wireHarness, _ string) {
				h.repo.failPass.Store(true)
				h.clock.Advance(coordinator.DefaultAssignmentSettings().FallbackAge + time.Second)
				h.heartbeat("w_1")
			},
			wantPolicy: domain.DispatchPolicyRoundRobinFallback,
			wantReason: domain.FallbackReasonAge,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newWireHarness(t, tc.policy)
			h.register("w_1")
			jobID := h.submit()
			if tc.setup != nil {
				tc.setup(h, jobID)
			}
			a := h.poll("w_1")
			for _, k := range pollAssignmentKeys {
				if _, ok := a[k]; !ok {
					t.Fatalf("pre-existing assignment key %q missing; keys=%v", k, keysOfRaw(a))
				}
			}
			if got := wireString(t, a, "job_id"); got != jobID {
				t.Fatalf("claimed %s, want %s", got, jobID)
			}
			if got := wireString(t, a, "policy"); got != tc.wantPolicy {
				t.Fatalf("policy = %q, want %q", got, tc.wantPolicy)
			}
			raw, has := a["fallback_reason"]
			switch {
			case tc.wantReason == "" && has:
				t.Fatalf("fallback_reason present (%s) on a %s claim", raw, tc.wantPolicy)
			case tc.wantReason != "":
				if got := wireString(t, a, "fallback_reason"); got != tc.wantReason {
					t.Fatalf("fallback_reason = %q, want %q", got, tc.wantReason)
				}
			}
		})
	}
}

func keysOfRaw(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestAPI_Job_AssignmentFieldsAreAdditive: ADR-0017 Section 5.1 and Section 6.4. GET
// /v1/jobs/{job_id} keeps every pre-existing field and adds assigned_worker_id and
// assignment_expires_at (null unless a reservation is live), assignment_lapses (an integer, 0 when
// none) and assignment_fallback_reason (null until a fallback claim). The internal assigned_at is
// never exposed.
func TestAPI_Job_AssignmentFieldsAreAdditive(t *testing.T) {
	h := newWireHarness(t, domain.DispatchPolicyRoundRobin)
	h.register("w_1")
	jobID := h.submit()

	type want struct {
		worker  string // "" means null
		expires bool
		lapses  string
		reason  string // "" means null
	}
	check := func(step string, keys map[string]json.RawMessage, w want) {
		t.Helper()
		for _, k := range []string{"id", "task_type", "payload", "state", "priority", "max_attempts", "timeout_seconds", "created_at", "updated_at"} {
			if _, ok := keys[k]; !ok {
				t.Fatalf("%s: pre-existing job key %q missing; keys=%v", step, k, keysOfRaw(keys))
			}
		}
		if _, ok := keys["assigned_at"]; ok {
			t.Fatalf("%s: job view exposes the internal assigned_at", step)
		}
		nullOr := func(k, v string) {
			t.Helper()
			raw, ok := keys[k]
			if !ok {
				t.Fatalf("%s: %q missing; keys=%v", step, k, keysOfRaw(keys))
			}
			if v == "" {
				if string(raw) != "null" {
					t.Fatalf("%s: %s = %s, want null", step, k, raw)
				}
				return
			}
			if got := wireString(t, keys, k); got != v {
				t.Fatalf("%s: %s = %q, want %q", step, k, got, v)
			}
		}
		nullOr("assigned_worker_id", w.worker)
		nullOr("assignment_fallback_reason", w.reason)
		exp, ok := keys["assignment_expires_at"]
		if !ok || (string(exp) == "null") == w.expires {
			t.Fatalf("%s: assignment_expires_at = %s, want set=%v", step, exp, w.expires)
		}
		if raw, ok := keys["assignment_lapses"]; !ok || string(raw) != w.lapses {
			t.Fatalf("%s: assignment_lapses = %s, want %s", step, raw, w.lapses)
		}
	}

	submitted := h.getJob(jobID)
	check("submitted", submitted, want{lapses: "0"})
	if wireString(t, submitted, "state") != string(domain.StateQueued) || wireString(t, submitted, "id") != jobID {
		t.Fatalf("submitted: pre-existing values changed: state=%s id=%s", submitted["state"], submitted["id"])
	}

	// A pass reserves the job to w_1, which is fresh after its poll records freshness. The
	// repository pass is called directly so the reservation is visible before any claim.
	h.heartbeat("w_1")
	if err := h.repo.RecordPoll(context.Background(), "w_1"); err != nil {
		t.Fatalf("RecordPoll: %v", err)
	}
	s := coordinator.DefaultAssignmentSettings()
	if _, err := h.repo.AssignQueuedJobs(context.Background(), storage.AssignParams{
		AcceptWindow: s.AcceptWindow, WorkerTimeout: 10 * time.Second, BatchLimit: s.BatchLimit, MaxLapses: s.MaxLapses,
	}); err != nil {
		t.Fatalf("AssignQueuedJobs: %v", err)
	}
	reserved := h.getJob(jobID)
	check("reserved", reserved, want{worker: "w_1", expires: true, lapses: "0"})
	var exp time.Time
	if err := json.Unmarshal(reserved["assignment_expires_at"], &exp); err != nil || !exp.Equal(h.clock.Now().Add(s.AcceptWindow)) {
		t.Fatalf("reserved: assignment_expires_at = %s, want %v (err %v)", reserved["assignment_expires_at"], h.clock.Now().Add(s.AcceptWindow), err)
	}

	// The window lapses with no poll: the pass counts one lapse and the reservation is gone.
	h.clock.Advance(s.AcceptWindow + time.Millisecond)
	if _, err := h.repo.AssignQueuedJobs(context.Background(), storage.AssignParams{
		AcceptWindow: s.AcceptWindow, WorkerTimeout: 10 * time.Second, BatchLimit: s.BatchLimit, MaxLapses: s.MaxLapses,
	}); err != nil {
		t.Fatalf("AssignQueuedJobs: %v", err)
	}
	check("lapsed", h.getJob(jobID), want{lapses: "1"})

	// At the lapse limit the poll claims by the lapse arm: lapses reset, the reason is recorded.
	job, _ := h.repo.GetJob(context.Background(), jobID)
	job.AssignmentLapses = s.MaxLapses
	if err := h.repo.UpdateJob(context.Background(), job); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}
	h.heartbeat("w_1")
	if got := wireString(t, h.poll("w_1"), "fallback_reason"); got != domain.FallbackReasonLapses {
		t.Fatalf("poll fallback_reason = %q", got)
	}
	claimed := h.getJob(jobID)
	check("fallback claim", claimed, want{lapses: "0", reason: domain.FallbackReasonLapses})
	if wireString(t, claimed, "state") != string(domain.StateRunning) {
		t.Fatalf("fallback claim: state = %s", claimed["state"])
	}
}

// TestAPI_SubmitJob_ResponseCarriesAssignmentFields: the 201 body of POST /v1/jobs is the same job
// view as GET /v1/jobs/{job_id} (ADR-0017 Section 5.1, amended by note 5c-2.4; review F-R3). A new
// job carries the four assignment fields at their unset values (assigned_worker_id null,
// assignment_expires_at null, assignment_lapses 0, assignment_fallback_reason null) and never the
// internal assigned_at.
func TestAPI_SubmitJob_ResponseCarriesAssignmentFields(t *testing.T) {
	h := newWireHarness(t, domain.DispatchPolicyRoundRobin)
	code, body := h.do(http.MethodPost, "/v1/jobs", `{"task_type":"echo","payload":{"message":"x"},"max_attempts":3,"timeout_seconds":30}`)
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %s", code, body)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("submit body %s: %v", body, err)
	}
	for k, want := range map[string]string{
		"assigned_worker_id":         "null",
		"assignment_expires_at":      "null",
		"assignment_lapses":          "0",
		"assignment_fallback_reason": "null",
	} {
		got, ok := keys[k]
		if !ok || string(got) != want {
			t.Fatalf("201 body: %s = %s (present %v), want %s; keys=%v", k, got, ok, want, keysOfRaw(keys))
		}
	}
	if _, ok := keys["assigned_at"]; ok {
		t.Fatalf("201 body exposes the internal assigned_at")
	}
	if wireString(t, keys, "state") != string(domain.StateQueued) {
		t.Fatalf("201 body state = %s", keys["state"])
	}
}
