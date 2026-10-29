package faultinject_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

func setup(t *testing.T, kind string, workers ...string) *sectest.Rig {
	t.Helper()
	r := sectest.NewRig(t, kind)
	for _, w := range workers {
		if resp := r.Register(w, 1); resp.Status != http.StatusOK {
			t.Fatalf("register %s: %s", w, resp)
		}
	}
	return r
}

// TestFault_LateResult_AfterSweep is FS-04: worker 1 is silent past its lease, the sweeper reaps
// attempt 1, attempt 2 runs on worker 2, then worker 1 returns with its old token.
func TestFault_LateResult_AfterSweep(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := setup(t, kind, "w1", "w2")
		job := r.Submit(3)
		a1 := r.Poll("w1")
		r.Expire()
		r.MustSweep()
		if j := r.Job(job); j.State != domain.StateRetrying {
			t.Fatalf("after sweep job %s, want RETRYING", j.State)
		}
		r.MakeRetryDue()
		a2 := r.Poll("w2")
		if a2 == nil || a2.AttemptNo != 2 || a2.JobID != job {
			t.Fatalf("second dispatch = %+v, want attempt 2 of %s", a2, job)
		}

		for i := 0; i < 3; i++ { // the zombie retries; every try is rejected, one diagnostic only
			resp := r.Result(job, a1.AttemptID, a1.LeaseToken, "w1", 0, "", "")
			if resp.Status != http.StatusConflict || resp.ErrCode() != "LEASE_EXPIRED" {
				t.Fatalf("late commit #%d = %s, want 409 LEASE_EXPIRED", i, resp)
			}
		}
		if n := r.CountEvents(job, domain.JobEventDiagnosticLateResult); n != 1 {
			t.Fatalf("DIAGNOSTIC_LATE_RESULT events = %d, want exactly 1", n)
		}
		if n := r.ResultRows(job); n != 0 {
			t.Fatalf("result rows after rejected late commits = %d, want 0", n)
		}
		if resp := r.Renew(job, a1.AttemptID, a1.LeaseToken, "w1"); resp.Status != http.StatusConflict {
			t.Fatalf("zombie renew = %s, want 409", resp)
		}

		if resp := r.Result(job, a2.AttemptID, a2.LeaseToken, "w2", 0, "", ""); resp.Status != http.StatusOK {
			t.Fatalf("attempt 2 commit = %s, want 200", resp)
		}
		atts := r.Attempts(job)
		if atts[0].State != domain.AttemptLost || atts[1].State != domain.AttemptCompleted {
			t.Fatalf("attempt states %s/%s, want LOST/COMPLETED (no resurrection)", atts[0].State, atts[1].State)
		}
		if j := r.Job(job); j.State != domain.StateSucceeded || r.ResultRows(job) != 1 {
			t.Fatalf("job %s results %d, want SUCCEEDED with one result", j.State, r.ResultRows(job))
		}
		sectest.CheckInvariants(t, r, []string{job})
	})
}

// TestFault_StaleTokenMatrix attacks /result and /renew with tokens, attempts, jobs and workers
// that do not belong together, and proves nothing changed and no token is echoed.
func TestFault_StaleTokenMatrix(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := setup(t, kind, "w1", "w2", "w3")
		j1, j2 := r.Submit(3), r.Submit(3)
		a1, a2 := r.Poll("w1"), r.Poll("w2")
		if a1.JobID != j1 || a2.JobID != j2 {
			t.Fatalf("dispatch order unexpected: %s %s", a1.JobID, a2.JobID)
		}
		j3 := r.Submit(3)
		a3 := r.Poll("w3")
		// All three leases lapse and are swept, so a1, a2 and a3 are LOST (superseded) attempts.
		r.Expire()
		r.MustSweep()
		r.MakeRetryDue()
		// Fresh attempts for the attack: w1 gets job 1 attempt 2, w2 gets job 2 attempt 2.
		x := r.Poll("w1")
		y := r.Poll("w2")
		if x == nil || y == nil || x.JobID != j1 || y.JobID != j2 {
			t.Fatalf("expected re-dispatch of jobs 1 and 2, got %+v %+v", x, y)
		}
		tokens := []string{x.LeaseToken, y.LeaseToken, a1.LeaseToken, a2.LeaseToken, a3.LeaseToken}

		type tc struct {
			name          string
			job, att, tok string
			worker        string
			wantStatus    []int
			wantCodes     []string
		}
		cases := []tc{
			{"wrong token", x.JobID, x.AttemptID, "AAAA", "w1", []int{409}, []string{"STALE_ATTEMPT_REJECTED"}},
			{"empty token", x.JobID, x.AttemptID, "", "w1", []int{409}, []string{"STALE_ATTEMPT_REJECTED"}},
			{"token of another job's attempt", x.JobID, x.AttemptID, y.LeaseToken, "w1", []int{409}, []string{"STALE_ATTEMPT_REJECTED"}},
			{"token of superseded attempt", x.JobID, x.AttemptID, a1.LeaseToken, "w1", []int{409}, []string{"STALE_ATTEMPT_REJECTED"}},
			{"superseded attempt id with its own token", a1.JobID, a1.AttemptID, a1.LeaseToken, "w1", []int{409}, []string{"LEASE_EXPIRED", "STALE_ATTEMPT_REJECTED"}},
			{"right token, other worker", x.JobID, x.AttemptID, x.LeaseToken, "w2", []int{403}, []string{"WORKER_MISMATCH"}},
			{"wrong token and other worker", x.JobID, x.AttemptID, "AAAA", "w2", []int{409}, []string{"STALE_ATTEMPT_REJECTED"}},
			{"unknown worker, right token", x.JobID, x.AttemptID, x.LeaseToken, "ghost", []int{403, 404}, []string{"WORKER_MISMATCH", "WORKER_NOT_FOUND"}},
			{"attempt of job Y under job X", x.JobID, y.AttemptID, y.LeaseToken, "w2", []int{404, 409}, []string{"ATTEMPT_NOT_FOUND", "STALE_ATTEMPT_REJECTED", "JOB_NOT_FOUND"}},
			// F-4e-1: protocol 6.3 row 6 says 404 ATTEMPT_NOT_FOUND; the code answers 409 STALE_ATTEMPT_REJECTED.
			{"unknown attempt id under a real job", x.JobID, "att_nope", x.LeaseToken, "w1", []int{404, 409}, []string{"ATTEMPT_NOT_FOUND", "STALE_ATTEMPT_REJECTED"}},
			{"unknown job", "job_nope", x.AttemptID, x.LeaseToken, "w1", []int{404}, []string{"JOB_NOT_FOUND", "ATTEMPT_NOT_FOUND"}},
		}
		snap := func() string {
			var b strings.Builder
			for _, id := range []string{j1, j2, j3} {
				fmt.Fprintf(&b, "%s:%s:%d|", id, r.Job(id).State, r.ResultRows(id))
				for _, a := range r.Attempts(id) {
					fmt.Fprintf(&b, "%s=%s,", a.ID, a.State)
				}
			}
			return b.String()
		}
		before := snap()
		for _, c := range cases {
			for _, ep := range []string{"result", "renew"} {
				var resp sectest.Resp
				if ep == "result" {
					resp = r.Result(c.job, c.att, c.tok, c.worker, 0, "", "")
				} else {
					resp = r.Renew(c.job, c.att, c.tok, c.worker)
				}
				okStatus := false
				for _, s := range c.wantStatus {
					okStatus = okStatus || resp.Status == s
				}
				okCode := false
				for _, s := range c.wantCodes {
					okCode = okCode || resp.ErrCode() == s
				}
				if !okStatus || !okCode {
					t.Errorf("%s /%s = %s, want status %v code %v", c.name, ep, resp, c.wantStatus, c.wantCodes)
				}
				body := string(resp.Body)
				for _, tok := range tokens {
					if tok != "" && strings.Contains(body, tok) {
						t.Errorf("%s /%s response echoes a lease token: %s", c.name, ep, body)
					}
				}
			}
		}
		if after := snap(); after != before {
			t.Fatalf("rejected requests changed stored state:\nbefore %s\nafter  %s", before, after)
		}
		// The legitimate holders still work.
		if resp := r.Result(x.JobID, x.AttemptID, x.LeaseToken, "w1", 0, "", ""); resp.Status != http.StatusOK {
			t.Fatalf("legitimate commit = %s", resp)
		}
		sectest.CheckInvariants(t, r, []string{j1, j2, j3})
	})
}

// TestFault_DuplicateAndReplayedCommit sends the same commit sequentially and 30-way concurrently.
func TestFault_DuplicateAndReplayedCommit(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := setup(t, kind, "w1")
		job := r.Submit(3)
		a := r.Poll("w1")
		if resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 0, "", ""); resp.Status != http.StatusOK {
			t.Fatalf("first commit = %s", resp)
		}
		for i := 0; i < 3; i++ {
			resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 0, "", "")
			if resp.Status != http.StatusConflict || resp.ErrCode() != "JOB_ALREADY_TERMINAL" {
				t.Fatalf("replay #%d = %s, want 409 JOB_ALREADY_TERMINAL", i, resp)
			}
		}
		// A replay with different content must not overwrite the stored result.
		resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 7, "forged", "PERMANENT")
		if resp.Status != http.StatusConflict {
			t.Fatalf("forged replay = %s, want 409", resp)
		}
		if j := r.Job(job); j.State != domain.StateSucceeded || j.Result == nil || j.Result.ExitCode != 0 {
			t.Fatalf("stored result was altered by a replay: %+v", j.Result)
		}
		if at := r.Attempts(job)[0]; at.State != domain.AttemptCompleted {
			t.Fatalf("attempt %s, want COMPLETED", at.State)
		}

		// Concurrent duplicates on a fresh job.
		job2 := r.Submit(3)
		b := r.Poll("w1")
		const n = 30
		codes := make([]int, n)
		var start, done sync.WaitGroup
		start.Add(1)
		for i := 0; i < n; i++ {
			done.Add(1)
			go func(i int) {
				defer done.Done()
				start.Wait()
				codes[i] = r.Result(job2, b.AttemptID, b.LeaseToken, "w1", 0, "", "").Status
			}(i)
		}
		start.Done()
		done.Wait()
		ok, conflict := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusOK:
				ok++
			case http.StatusConflict:
				conflict++
			}
		}
		if ok != 1 || conflict != n-1 {
			t.Fatalf("concurrent duplicate commits: %d x 200, %d x 409 (of %d), want 1 and %d: %v", ok, conflict, n, n-1, codes)
		}
		if r.ResultRows(job2) != 1 {
			t.Fatalf("result rows = %d, want 1", r.ResultRows(job2))
		}
		sectest.CheckInvariants(t, r, []string{job, job2})
	})
}

// TestFault_CancelRacesCompletion is FS-10: cancel, duplicate cancel and commit start together.
func TestFault_CancelRacesCompletion(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		const n = 40
		r.Register("w", n)
		type item struct {
			job, att, tok string
			fail          bool
		}
		var items []item
		var ids []string
		for i := 0; i < n; i++ {
			j := r.Submit(1)
			ids = append(ids, j)
		}
		for i := 0; i < n; i++ {
			a := r.Poll("w")
			items = append(items, item{a.JobID, a.AttemptID, a.LeaseToken, i%2 == 1})
		}
		var start, done sync.WaitGroup
		start.Add(1)
		var mu sync.Mutex
		type out struct{ commit, c1, c2 int }
		res := map[string]*out{}
		for _, it := range items {
			it := it
			o := &out{}
			res[it.job] = o
			run := func(f func()) {
				done.Add(1)
				go func() { defer done.Done(); start.Wait(); f() }()
			}
			run(func() {
				exit, cls := 0, ""
				if it.fail {
					exit, cls = 1, "PERMANENT"
				}
				s := r.Result(it.job, it.att, it.tok, "w", exit, map[bool]string{true: "boom", false: ""}[it.fail], cls).Status
				mu.Lock()
				o.commit = s
				mu.Unlock()
			})
			run(func() { s := r.Cancel(it.job).Status; mu.Lock(); o.c1 = s; mu.Unlock() })
			run(func() { s := r.Cancel(it.job).Status; mu.Lock(); o.c2 = s; mu.Unlock() })
		}
		start.Done()
		done.Wait()
		for _, it := range items {
			o := res[it.job]
			j := r.Job(it.job)
			if o.commit != http.StatusOK {
				t.Errorf("%s: commit status %d, want 200 (accepted before or after the cancel)", it.job, o.commit)
			}
			for _, c := range []int{o.c1, o.c2} {
				if c != http.StatusOK && c != http.StatusConflict {
					t.Errorf("%s: cancel status %d, want 200 or 409", it.job, c)
				}
			}
			// If the cancel took effect before the commit (a JOB_CANCEL_REQUESTED event exists) the failed
			// commit must end CANCELLED (cancel wins, never FAILED or RETRYING); if the commit won, FAILED.
			if it.fail {
				want := domain.StateFailed
				if r.CountEvents(it.job, domain.JobEventCancelRequested) == 1 {
					want = domain.StateCancelled
				}
				if j.State != want {
					t.Errorf("%s: failed commit raced with cancel ended %s, want %s", it.job, j.State, want)
				}
			}
			if !it.fail && j.State != domain.StateSucceeded {
				t.Errorf("%s: success commit raced with cancel ended %s, want SUCCEEDED", it.job, j.State)
			}
			if c := r.CountEvents(it.job, domain.JobEventCancelRequested); c > 1 {
				t.Errorf("%s: %d JOB_CANCEL_REQUESTED events, want at most 1", it.job, c)
			}
		}
		sectest.CheckInvariants(t, r, ids)
		if ws, _ := r.Repo.ListWorkers(context.Background()); len(ws) == 1 && ws[0].AvailableSlots != n {
			t.Errorf("worker slots after all jobs finished = %d, want %d (no leaked slot)", ws[0].AvailableSlots, n)
		}
	})
}

// TestFault_CancelRacesLeaseExpiry races cancel against sweeper ticks on expired leases, and also
// runs the deterministic order "cancel first, then the sweep" so the cancel-wins rule of the sweeper
// is exercised on every run, not only when the race happens to land that way.
func TestFault_CancelRacesLeaseExpiry(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		for _, mode := range []string{"cancel then sweep", "concurrent"} {
			mode := mode
			t.Run(mode, func(t *testing.T) {
				r := sectest.NewRig(t, kind)
				const n = 20
				r.Register("w", n)
				var ids []string
				for i := 0; i < n; i++ {
					ids = append(ids, r.Submit(3))
				}
				for i := 0; i < n; i++ {
					r.Poll("w")
				}
				if mode == "cancel then sweep" {
					for _, id := range ids {
						if resp := r.Cancel(id); resp.Status != http.StatusOK {
							t.Fatalf("cancel = %s", resp)
						}
						if j := r.Job(id); j.State != domain.StateCancelRequested {
							t.Fatalf("job %s, want CANCEL_REQUESTED", j.State)
						}
					}
					r.Expire()
					r.MustSweep()
				} else {
					r.Expire()
					var start, done sync.WaitGroup
					start.Add(1)
					done.Add(1)
					go func() {
						defer done.Done()
						start.Wait()
						for i := 0; i < 3; i++ {
							if err := r.Sweep(); err != nil {
								t.Errorf("sweep: %v", err)
							}
						}
					}()
					for _, id := range ids {
						id := id
						done.Add(1)
						go func() { defer done.Done(); start.Wait(); r.Cancel(id) }()
					}
					start.Done()
					done.Wait()
					r.MustSweep()
				}
				for _, id := range ids {
					if j := r.Job(id); j.State != domain.StateCancelled {
						t.Errorf("%s ended %s, want CANCELLED (cancel wins over expiry, never RETRYING)", id, j.State)
					}
				}
				sectest.CheckInvariants(t, r, ids)
			})
		}
	})
}

// TestFault_RetriesExhausted checks the attempt budget, for failures and for lost leases.
func TestFault_RetriesExhausted(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		t.Run("transient failures", func(t *testing.T) {
			r := setup(t, kind, "w1")
			job := r.Submit(2)
			for i := 1; i <= 2; i++ {
				a := r.Poll("w1")
				if a == nil || a.AttemptNo != i {
					t.Fatalf("dispatch %d = %+v", i, a)
				}
				if resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 1, "flaky", "TRANSIENT"); resp.Status != http.StatusOK {
					t.Fatalf("fail commit %d = %s", i, resp)
				}
				r.MakeRetryDue()
			}
			if j := r.Job(job); j.State != domain.StateFailed {
				t.Fatalf("job %s, want FAILED after budget", j.State)
			}
			if a := r.Poll("w1"); a != nil {
				t.Fatalf("a third attempt was dispatched: %+v", a)
			}
			if n := len(r.Attempts(job)); n != 2 {
				t.Fatalf("attempts = %d, want exactly 2", n)
			}
			sectest.CheckInvariants(t, r, []string{job})
		})
		t.Run("lost leases consume the budget", func(t *testing.T) {
			r := setup(t, kind, "w1")
			job := r.Submit(2)
			for i := 1; i <= 2; i++ {
				a := r.Poll("w1")
				if a == nil || a.AttemptNo != i {
					t.Fatalf("dispatch %d = %+v", i, a)
				}
				r.Expire()
				r.MustSweep()
				r.MakeRetryDue()
			}
			if j := r.Job(job); j.State != domain.StateFailed {
				t.Fatalf("job %s, want FAILED after two lost attempts", j.State)
			}
			if a := r.Poll("w1"); a != nil {
				t.Fatalf("a third attempt was dispatched: %+v", a)
			}
			for _, a := range r.Attempts(job) {
				if a.State != domain.AttemptLost {
					t.Fatalf("attempt %d is %s, want LOST", a.AttemptNo, a.State)
				}
			}
			sectest.CheckInvariants(t, r, []string{job})
		})
	})
}

// TestFault_Heartbeat_DuplicateAndDelayed is FS-08.
func TestFault_Heartbeat_DuplicateAndDelayed(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind, sectest.Options{WorkerTimeout: 10 * time.Second})
		r.Register("w1", 2)
		var wg sync.WaitGroup
		codes := make([]int, 20)
		for i := range codes {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i] = r.Do("POST", "/v1/workers/w1/heartbeat", `{"available_slots":99}`).Status
			}(i)
		}
		wg.Wait()
		for _, c := range codes {
			if c != http.StatusOK {
				t.Fatalf("duplicate heartbeat status %d, want 200: %v", c, codes)
			}
		}
		ws, _ := r.Repo.ListWorkers(context.Background())
		if len(ws) != 1 || ws[0].AvailableSlots != 2 {
			t.Fatalf("workers = %+v, want one row and the ignored available_slots (ADR-0011)", ws)
		}
		r.AgeWorkers(time.Minute)
		if _, err := r.Svc.ReapInactiveWorkers(context.Background()); err != nil {
			t.Fatal(err)
		}
		if ws, _ = r.Repo.ListWorkers(context.Background()); ws[0].Status != domain.WorkerStatusUnhealthy {
			t.Fatalf("status %s, want UNHEALTHY after a delayed heartbeat", ws[0].Status)
		}
		r.Submit(3)
		if resp := r.Do("POST", "/v1/workers/w1/poll", ""); resp.Status != http.StatusServiceUnavailable || resp.ErrCode() != "WORKER_UNHEALTHY" {
			t.Fatalf("poll by unhealthy worker = %s", resp)
		}
		if resp := r.Do("POST", "/v1/workers/w1/heartbeat", `{}`); resp.Status != http.StatusOK {
			t.Fatalf("heartbeat = %s", resp)
		}
		if ws, _ = r.Repo.ListWorkers(context.Background()); ws[0].Status != domain.WorkerStatusHealthy {
			t.Fatalf("status %s, want HEALTHY after the next heartbeat", ws[0].Status)
		}
	})
}
