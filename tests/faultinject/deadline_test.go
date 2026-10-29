package faultinject_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// TestFault_RequestDeadline_NoLegitimateRequestCutOff walks every endpoint through a database that
// is slow (50 ms each way, so 100 ms per round trip, a WAN-like database) and proves each completes inside api.RequestTimeout, so
// the F-4e-2 deadline does not cut off a legitimate request. No endpoint long-polls: work polling
// returns 204 at once when nothing is queued (checked at the end). The measured durations are
// logged for the handoff.
func TestFault_RequestDeadline_NoLegitimateRequestCutOff(t *testing.T) {
	if api.RequestTimeout >= 10*time.Second {
		t.Fatalf("api.RequestTimeout %v must stay below the 10 s server WriteTimeout", api.RequestTimeout)
	}
	r, p := outageRig(t)
	r.Register("w1", 2)
	p.SetDelay(50 * time.Millisecond)
	p.SetMode(sectest.ModeDelay)

	big := strings.Repeat("o", 64*1024)    // maximum task output
	bigErr := strings.Repeat("e", 10*1024) // maximum stored error text
	var slowest time.Duration
	step := func(name string, want int, f func() sectest.Resp) sectest.Resp {
		t.Helper()
		start := time.Now()
		resp := f()
		el := time.Since(start)
		t.Logf("%-28s %d in %v", name, resp.Status, el.Round(time.Millisecond))
		if resp.Status != want {
			t.Errorf("%s = %s, want %d", name, resp, want)
		}
		if el >= api.RequestTimeout {
			t.Errorf("%s took %v, not below the %v request deadline", name, el, api.RequestTimeout)
		}
		if el > slowest {
			slowest = el
		}
		return resp
	}

	step("healthz", 200, func() sectest.Resp { return r.Do("GET", "/healthz", nil) })
	step("readyz", 200, func() sectest.Resp { return r.Do("GET", "/readyz", nil) })
	step("register worker", 200, func() sectest.Resp { return r.Register("w2", 1) })
	step("heartbeat", 200, func() sectest.Resp { return r.Do("POST", "/v1/workers/w1/heartbeat", `{}`) })
	step("list workers", 200, func() sectest.Resp { return r.Do("GET", "/v1/workers", nil) })
	var job string
	step("submit job", 201, func() sectest.Resp {
		resp := r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":{"message":"x"},"max_attempts":3}`)
		job = sectest.JobID(resp)
		return resp
	})
	step("get job", 200, func() sectest.Resp { return r.Do("GET", "/v1/jobs/"+job, nil) })
	var asgJob, asgAtt, asgTok string
	step("poll with work", 200, func() sectest.Resp {
		resp := r.Do("POST", "/v1/workers/w1/poll", "")
		asgJob, asgAtt, asgTok = sectest.AssignmentOf(resp)
		return resp
	})
	step("renew", 200, func() sectest.Resp { return r.Renew(asgJob, asgAtt, asgTok, "w1") })
	step("list attempts", 200, func() sectest.Resp { return r.Do("GET", "/v1/jobs/"+job+"/attempts", nil) })
	step("cancel running job", 200, func() sectest.Resp { return r.Cancel(job) })
	step("commit 64 KiB output", 200, func() sectest.Resp {
		return r.Do("POST", "/v1/jobs/"+asgJob+"/attempts/"+asgAtt+"/result",
			map[string]any{"lease_token": asgTok, "worker_id": "w1", "exit_code": 0, "output": big, "error": ""})
	})
	// A failing attempt with a maximum-size error text goes through the retry path.
	job2 := r.Submit(3)
	a2 := r.Poll("w1")
	step("commit failure, 10 KiB error", 200, func() sectest.Resp {
		return r.Do("POST", "/v1/jobs/"+job2+"/attempts/"+a2.AttemptID+"/result",
			map[string]any{"lease_token": a2.LeaseToken, "worker_id": "w1", "exit_code": 1, "output": "", "error": bigErr, "error_classification": "TRANSIENT"})
	})
	p.SetMode(sectest.ModeForward)
	start := time.Now()
	step("poll with no work (no long poll)", 204, func() sectest.Resp { return r.Do("POST", "/v1/workers/w2/poll", "") })
	if el := time.Since(start); el > time.Second {
		t.Errorf("an empty poll took %v, the poll must not block", el)
	}
	t.Logf("slowest legitimate request under a 50 ms-each-way database: %v; request deadline %v; server write timeout 10s", slowest.Round(time.Millisecond), api.RequestTimeout)
	if slowest*2 > api.RequestTimeout {
		t.Errorf("slowest request %v leaves less than 2x headroom below the %v deadline", slowest, api.RequestTimeout)
	}
	sectest.CheckInvariants(t, r, []string{job, job2})
}

// TestFault_DBOutage_HalfOpen_OtherRequestsNotStalled executes what 4c-a only inferred: while the
// database is half-open and a poll is hung (PollWork holds the service-wide mutex across its
// database call), requests that do not need the database answer at once, and requests that do need
// it are answered 503 within the request deadline, none later.
func TestFault_DBOutage_HalfOpen_OtherRequestsNotStalled(t *testing.T) {
	r, p := outageRig(t)
	r.Register("w1", 1)
	r.Register("w2", 1)
	job := r.Submit(3)
	r.Do("GET", "/readyz", nil) // warm
	p.SetMode(sectest.ModeBlackhole)
	faultAt := time.Now()
	r.Client.Timeout = 15 * time.Second

	type result struct {
		name string
		resp sectest.Resp
		el   time.Duration
	}
	var mu sync.Mutex
	var results []result
	var wg sync.WaitGroup
	launch := func(name string, f func() sectest.Resp) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			resp := f()
			mu.Lock()
			results = append(results, result{name, resp, time.Since(start)})
			mu.Unlock()
		}()
	}
	launch("poll #1 (hangs holding the service mutex)", func() sectest.Resp { return r.Do("POST", "/v1/workers/w1/poll", "") })
	time.Sleep(300 * time.Millisecond) // lets poll #1 reach the database call and take the mutex; the checks below do not depend on its exact timing
	launch("poll #2 (queued behind the mutex)", func() sectest.Resp { return r.Do("POST", "/v1/workers/w2/poll", "") })
	launch("commit (queued behind the mutex)", func() sectest.Resp { return r.Result(job, "att_x", "tok", "w1", 0, "", "") })
	launch("get job (database, no mutex)", func() sectest.Resp { return r.Do("GET", "/v1/jobs/"+job, nil) })
	launch("heartbeat (database)", func() sectest.Resp { return r.Do("POST", "/v1/workers/w1/heartbeat", `{}`) })
	launch("healthz (no database)", func() sectest.Resp { return r.Do("GET", "/healthz", nil) })
	wg.Wait()
	t.Logf("all hung requests answered %v after the fault started", time.Since(faultAt))

	for _, x := range results {
		t.Logf("%-46s %d in %v", x.name, x.resp.Status, x.el.Round(10*time.Millisecond))
		switch {
		case strings.HasPrefix(x.name, "healthz"):
			if x.resp.Status != http.StatusOK || x.el > time.Second {
				t.Errorf("%s = %s after %v, want 200 immediately", x.name, x.resp, x.el)
			}
		default:
			if x.resp.Status != http.StatusServiceUnavailable {
				t.Errorf("%s = %s, want 503", x.name, x.resp)
			}
			if x.el > api.RequestTimeout+2*time.Second {
				t.Errorf("%s answered after %v, later than the %v request deadline", x.name, x.el, api.RequestTimeout)
			}
		}
	}
}

// TestFault_DBOutage_HalfOpen_RecoveryTime measures how long the coordinator needs after a
// half-open partition heals when every pool connection was held by a hung request. pgx closes a
// cancelled half-open connection asynchronously with a hard-coded 15 s deadline (pgconn asyncClose)
// and the pool slot is not reusable until that cleanup ends, so recovery takes up to ~15 s
// (finding F-4e-3, observation: self-healing, bounded, answered 503 meanwhile).
func TestFault_DBOutage_HalfOpen_RecoveryTime(t *testing.T) {
	pg := sectest.NewPGSchema(t)
	p := sectest.NewFaultProxy(t, pg.UpstreamAddr())
	r := sectest.NewRig(t, sectest.KindPostgres, sectest.Options{PG: pg, PoolURL: pg.URLVia(p.Addr()), PoolMaxConns: 2})
	t.Cleanup(p.Close)
	r.Client.Timeout = 15 * time.Second
	r.Register("w1", 1)
	job := r.Submit(3)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ { // two concurrent requests take both pool connections
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.Do("GET", "/v1/jobs/"+job, nil) }()
	}
	// Both requests must be inside the database call before the fault starts being observed; the
	// proxy is switched first so they hang there. The short wait lets them acquire connections.
	p.SetMode(sectest.ModeBlackhole)
	wg.Wait()
	p.SetMode(sectest.ModeForward)
	healed := time.Now()
	deadline := healed.Add(25 * time.Second)
	for r.Do("GET", "/readyz", nil).Status != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("no recovery within 25 s of healing")
		}
		time.Sleep(100 * time.Millisecond) // bounded poll
	}
	t.Logf("readyz healthy again %v after healing with the whole pool held by hung requests", time.Since(healed).Round(10*time.Millisecond))
}
