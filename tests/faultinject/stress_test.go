package faultinject_test

import (
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// TestFault_ManyWorkersManyJobs_Invariants (X-04): 8 workers with 2 slots each race for 80 jobs while
// attempts succeed, fail transiently, are abandoned silently (their lease lapses and the sweeper
// reaps them) and jobs are cancelled at random. When everything settles: every job is in exactly
// one terminal state and has exactly one terminal event; at most one attempt per job ever ran to
// completion; attempt numbers are 1..n; no job exceeded max_attempts; no worker ever held more
// RUNNING attempts than slots (checked mid-run on PostgreSQL with an atomic query); no result row
// is duplicated.
func TestFault_ManyWorkersManyJobs_Invariants(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		const workers, slots, jobs = 8, 2, 80
		for i := 0; i < workers; i++ {
			if resp := r.Register(fmt.Sprintf("w%d", i), slots); resp.Status != http.StatusOK {
				t.Fatal(resp)
			}
		}
		ids := make([]string, jobs)
		for i := range ids {
			ids[i] = r.Submit(3)
		}
		seed := time.Now().UnixNano()
		t.Logf("seed %d", seed)

		done := make(chan struct{})
		var wg sync.WaitGroup
		var committed, abandoned, failedCommits, rejected atomic.Int64
		for w := 0; w < workers; w++ {
			w := w
			wg.Add(1)
			go func() {
				defer wg.Done()
				rng := rand.New(rand.NewSource(seed + int64(w)))
				id := fmt.Sprintf("w%d", w)
				for {
					select {
					case <-done:
						return
					default:
					}
					resp := r.Do("POST", "/v1/workers/"+id+"/poll", "")
					if resp.Status != http.StatusOK {
						time.Sleep(time.Millisecond) // nothing to claim or no capacity yet: back off briefly
						continue
					}
					job, att, tok := sectest.AssignmentOf(resp)
					switch p := rng.Intn(100); {
					case p < 60:
						if c := r.Result(job, att, tok, id, 0, "", ""); c.Status == http.StatusOK {
							committed.Add(1)
						} else {
							rejected.Add(1)
						}
					case p < 80:
						if c := r.Result(job, att, tok, id, 1, "flaky", "TRANSIENT"); c.Status == http.StatusOK {
							failedCommits.Add(1)
						} else {
							rejected.Add(1)
						}
					default:
						abandoned.Add(1) // never reported: the lease must lapse and the sweeper reclaim it
					}
				}
			}()
		}

		// The controller: lease expiry, sweeps, backoff elapsing and random cancels, until all settle.
		rng := rand.New(rand.NewSource(seed))
		deadline := time.Now().Add(90 * time.Second)
		settled := false
		for time.Now().Before(deadline) && !settled {
			time.Sleep(15 * time.Millisecond) // lets workers make progress between control steps
			if r.PG != nil {
				if n := r.PG.Count(t, `SELECT count(*) FROM (SELECT a.worker_id FROM attempts a WHERE a.state = 'RUNNING' GROUP BY a.worker_id
					HAVING count(*) > (SELECT w.total_slots FROM workers w WHERE w.id = a.worker_id)) x`); n != 0 {
					t.Errorf("a worker held more RUNNING attempts than slots")
				}
				if n := r.PG.Count(t, "SELECT count(*) FROM (SELECT job_id FROM attempts WHERE state = 'RUNNING' GROUP BY job_id HAVING count(*) > 1) x"); n != 0 {
					t.Errorf("a job had more than one RUNNING attempt")
				}
			}
			if rng.Intn(4) == 0 {
				r.Cancel(ids[rng.Intn(len(ids))])
			}
			r.Expire()
			if err := r.Sweep(); err != nil {
				t.Errorf("sweep: %v", err)
			}
			r.MakeRetryDue()
			settled = true
			for _, id := range ids {
				if !r.Job(id).State.IsTerminal() {
					settled = false
					break
				}
			}
		}
		close(done)
		wg.Wait()
		if !settled {
			counts := map[domain.JobState]int{}
			for _, id := range ids {
				counts[r.Job(id).State]++
			}
			t.Fatalf("jobs did not all settle within 90 s: %v", counts)
		}
		t.Logf("%s: %d success commits, %d failure commits, %d abandoned, %d commits rejected (lost or cancelled races)", kind, committed.Load(), failedCommits.Load(), abandoned.Load(), rejected.Load())

		terminal := map[domain.JobEventType]bool{domain.JobEventCompleted: true, domain.JobEventFailed: true, domain.JobEventCancelled: true}
		for _, id := range ids {
			n := 0
			for _, e := range r.Events(id) {
				if terminal[e.EventType] {
					n++
				}
			}
			if n != 1 {
				t.Errorf("job %s (%s) has %d terminal events, want exactly 1", id, r.Job(id).State, n)
			}
			completed := 0
			for _, a := range r.Attempts(id) {
				if a.State == domain.AttemptCompleted {
					completed++
				}
			}
			if completed > 1 || (r.Job(id).State == domain.StateSucceeded) != (completed == 1) {
				t.Errorf("job %s (%s) has %d COMPLETED attempts (double execution or a lost success)", id, r.Job(id).State, completed)
			}
		}
		sectest.CheckInvariants(t, r, ids)
	})
}
