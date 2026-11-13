package abuse_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// Slice 5c, part 3 (P4-08f): the API abuse rows of ADR-0017 Sections 6.2 and 6.4. The ADR names the
// first three TestAPI_...; they are TestAbuse_... here so that make test-security (which selects
// TestFault_ and TestAbuse_) runs them. Every test runs on the in-memory and the PostgreSQL backend.

// lastPollMicros is the stored last_poll_at of a worker in microseconds since the epoch (0 when it
// was never written). The PostgreSQL repository does not load the column into domain.Worker, so a
// PostgreSQL rig reads it with SQL.
func lastPollMicros(t *testing.T, r *sectest.Rig, worker string) int64 {
	t.Helper()
	if r.PG != nil {
		return int64(r.PG.Count(t, "SELECT COALESCE((extract(epoch from last_poll_at) * 1000000)::bigint, 0) FROM workers WHERE id = $1", worker))
	}
	w, err := r.Repo.GetWorker(context.Background(), worker)
	if err != nil {
		t.Fatalf("GetWorker %s: %v", worker, err)
	}
	if w.LastPollAt == nil {
		return 0
	}
	return w.LastPollAt.UnixMicro()
}

// TestAbuse_Poll_ForeignAssignment_Returns204: a worker that is not the holder of a live reservation
// never gets that job, however often it polls, and a poll never touches the reservation. An unknown
// worker id gets 404, not a claim. The holder then claims it.
func TestAbuse_Poll_ForeignAssignment_Returns204(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRRRig(t, kind, sectest.RROptions{})
		r.Join("w1", "w2", "w3")
		job := r.Submit(3)
		r.Pass()
		h := r.Holder(job)
		if h == "" {
			t.Fatal("pass reserved nothing")
		}
		for i := 0; i < 10; i++ {
			for _, w := range []string{"w1", "w2", "w3"} {
				if w == h {
					continue
				}
				if resp := r.PollResp(w); resp.Status != http.StatusNoContent {
					t.Fatalf("poll %d by foreign worker %s = %s, want 204", i, w, resp)
				}
			}
		}
		if resp := r.PollResp("ghost"); resp.Status != http.StatusNotFound {
			t.Fatalf("poll by an unknown worker = %s, want 404", resp)
		}
		if r.Holder(job) != h || len(r.Attempts(job)) != 0 {
			t.Fatalf("foreign polls changed the reservation: holder %q attempts %d", r.Holder(job), len(r.Attempts(job)))
		}
		got := r.Poll(h)
		if got == nil || got.JobID != job || got.Policy != domain.DispatchPolicyRoundRobin {
			t.Fatalf("holder's poll = %+v, want the job under round_robin", got)
		}
	})
}

// TestAbuse_Poll_UnhealthyWorker_Still503: an UNHEALTHY worker's poll is 503 WORKER_UNHEALTHY under
// both policies, writes no poll freshness, claims nothing and is reserved nothing.
func TestAbuse_Poll_UnhealthyWorker_Still503(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		for _, policy := range []string{domain.DispatchPolicyFifoPull, domain.DispatchPolicyRoundRobin} {
			policy := policy
			t.Run(policy, func(t *testing.T) {
				opts := sectest.Options{WorkerTimeout: 3 * time.Second}
				var r *sectest.Rig
				var rr *sectest.RRRig
				if policy == domain.DispatchPolicyRoundRobin {
					rr = sectest.NewRRRig(t, kind, sectest.RROptions{Options: opts})
					r = rr.Rig
				} else {
					r = sectest.NewRig(t, kind, opts)
				}
				if resp := r.Register("w1", 1); resp.Status != http.StatusOK {
					t.Fatalf("register: %s", resp)
				}
				job := r.Submit(3)
				r.AgeWorkers(4 * time.Second)
				reaped, err := r.Svc.ReapInactiveWorkers(context.Background())
				if err != nil || len(reaped) != 1 {
					t.Fatalf("reaped %v err %v, want w1", reaped, err)
				}
				before, err := r.Repo.GetWorker(context.Background(), "w1")
				if err != nil || before.Status != domain.WorkerStatusUnhealthy {
					t.Fatalf("worker after the reap: %+v err %v, want UNHEALTHY", before, err)
				}
				pollBefore := lastPollMicros(t, r, "w1")
				for i := 0; i < 3; i++ {
					resp := r.Do("POST", "/v1/workers/w1/poll", "")
					if resp.Status != http.StatusServiceUnavailable || resp.ErrCode() != "WORKER_UNHEALTHY" {
						t.Fatalf("poll %d by an UNHEALTHY worker = %s, want 503 WORKER_UNHEALTHY", i, resp)
					}
				}
				if after := lastPollMicros(t, r, "w1"); after != pollBefore {
					t.Fatalf("an UNHEALTHY worker's poll wrote last_poll_at: %d -> %d", pollBefore, after)
				}
				if rr != nil {
					rr.Pass()
					if rr.Holder(job) != "" {
						t.Fatalf("job reserved to %q although every worker is UNHEALTHY", rr.Holder(job))
					}
				}
				if j := r.Job(job); j.State != domain.StateQueued || len(r.Attempts(job)) != 0 {
					t.Fatalf("job %s with %d attempts, want QUEUED and untouched", j.State, len(r.Attempts(job)))
				}
			})
		}
	})
}

// TestAbuse_Poll_Flood_ThrottlesFreshnessWrites: a flood of polls writes last_poll_at at most once
// per worker per second (ADR-0017 Section 2.2). The writes are counted by reading the stored value
// after every poll and counting the changes, not by timing the flood. In memory the clock advances
// 100 ms per poll, so 300 polls span 30 s; PostgreSQL runs on real time.
func TestAbuse_Poll_Flood_ThrottlesFreshnessWrites(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRRRig(t, kind, sectest.RROptions{})
		workers := []string{"w1", "w2", "w3"}
		for _, w := range workers {
			if resp := r.Register(w, 1); resp.Status != http.StatusOK {
				t.Fatalf("register %s: %s", w, resp)
			}
		}
		const polls = 300
		last := map[string]int64{}
		writes := map[string]int{}
		start := time.Now()
		var memSpan time.Duration
		for i := 0; i < polls; i++ {
			for _, w := range workers {
				if resp := r.PollResp(w); resp.Status != http.StatusNoContent {
					t.Fatalf("poll %d of %s = %s, want 204", i, w, resp)
				}
				at := lastPollMicros(t, r.Rig, w)
				if at == 0 {
					t.Fatalf("worker %s: last_poll_at not written by poll %d", w, i)
				}
				if at != last[w] {
					writes[w]++
					last[w] = at
				}
			}
			if r.Clock != nil {
				r.Clock.Advance(100 * time.Millisecond)
				memSpan += 100 * time.Millisecond
			}
		}
		span := time.Since(start)
		if r.Clock != nil {
			span = memSpan
		}
		maxWrites := int(span/time.Second) + 1
		for _, w := range workers {
			if writes[w] < 1 || writes[w] > maxWrites {
				t.Fatalf("worker %s: %d freshness writes for %d polls over %v, want between 1 and %d (one per second)", w, writes[w], polls, span, maxWrites)
			}
		}
		if r.Clock != nil && writes["w1"] < 25 {
			t.Fatalf("memory: %d writes over %v: the throttle suppresses more than the one-second window", writes["w1"], span)
		}
		// PostgreSQL runs on real time: a write every 1 s of polling gives about span/1 s writes; one
		// per 1.25 s is the floor that tolerates slow polls and still fails an over-throttle of 2 s or
		// more (P4-08g L5; the measured value on this machine is 4 writes over 3.5 s).
		if r.Clock == nil {
			floor := int(span / (1250 * time.Millisecond))
			for _, w := range workers {
				if writes[w] < floor {
					t.Fatalf("postgres worker %s: %d writes over %v, want at least %d: the throttle suppresses more than the one-second window", w, writes[w], span, floor)
				}
			}
		}
		t.Logf("%s: %d polls per worker over %v wrote last_poll_at %v times", kind, polls, span, writes)
	})
}

// TestAbuse_ConstantPoller_NoLargerShare (Section 6.2): a worker that polls constantly to stay
// eligible gets no more decisions than the others. Three workers, nine jobs, one job at a time: the
// hammering worker polls 25 times before every pass, and every worker still claims exactly three.
func TestAbuse_ConstantPoller_NoLargerShare(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRRRig(t, kind, sectest.RROptions{})
		r.Join("w1", "w2", "w3")
		share := map[string]int{}
		for i := 0; i < 9; i++ {
			job := r.Submit(3)
			for k := 0; k < 25; k++ {
				if a, resp := r.PollAssignment("w1"); a != nil {
					t.Fatalf("round %d: w1 claimed before any pass reserved: %s", i, resp)
				}
			}
			r.Keepalive("w2", "w3")
			r.Pass()
			h := r.Holder(job)
			if h == "" {
				t.Fatalf("round %d: nothing reserved", i)
			}
			got := r.Poll(h)
			if got == nil || got.JobID != job {
				t.Fatalf("round %d: holder %s poll = %+v", i, h, got)
			}
			share[h]++
			if resp := r.Result(got.JobID, got.AttemptID, got.LeaseToken, h, 0, "", ""); resp.Status != http.StatusOK {
				t.Fatalf("commit: %s", resp)
			}
		}
		for _, w := range []string{"w1", "w2", "w3"} {
			if share[w] != 3 {
				t.Fatalf("shares %v, want 3 each: constant polling must not win extra decisions", share)
			}
		}
	})
}

// TestAbuse_LargeTotalSlots_NoLargerShare (Section 6.2): a worker that registers a huge total_slots
// holds one reservation like every other (the one effective slot). Three workers, six jobs, one
// pass: three reservations, one per worker.
func TestAbuse_LargeTotalSlots_NoLargerShare(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRRRig(t, kind, sectest.RROptions{})
		if resp := r.Register("w_big", 1024); resp.Status != http.StatusOK {
			t.Fatalf("register: %s", resp)
		}
		r.Join("w1", "w2")
		r.Keepalive("w_big")
		var jobs []string
		for i := 0; i < 6; i++ {
			jobs = append(jobs, r.Submit(3))
		}
		r.Pass()
		per := map[string]int{}
		for _, id := range jobs {
			if h := r.Holder(id); h != "" {
				per[h]++
			}
		}
		if len(per) != 3 || per["w_big"] != 1 || per["w1"] != 1 || per["w2"] != 1 {
			t.Fatalf("reservations per worker %v, want exactly one each (total_slots must not buy more)", per)
		}
	})
}
