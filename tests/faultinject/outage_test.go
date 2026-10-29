package faultinject_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// outageRig builds a PostgreSQL rig whose pool reaches the test database only through a fault proxy.
func outageRig(t *testing.T) (*sectest.Rig, *sectest.FaultProxy) {
	t.Helper()
	pg := sectest.NewPGSchema(t)
	p := sectest.NewFaultProxy(t, pg.UpstreamAddr())
	r := sectest.NewRig(t, sectest.KindPostgres, sectest.Options{PG: pg, PoolURL: pg.URLVia(p.Addr()), PoolMaxConns: 4, Lease: 30 * time.Second})
	r.Client.Timeout = 20 * time.Second
	return r, p
}

func assertNoLeak(t *testing.T, what string, resp sectest.Resp) {
	t.Helper()
	body := strings.ToLower(string(resp.Body))
	for _, bad := range []string{"127.0.0.1", "localhost", "postgres", "password", "pgx", "sqlstate", "dial tcp", "5433", ".go:", "goroutine"} {
		if strings.Contains(body, bad) {
			t.Errorf("%s: response leaks %q: %s", what, bad, resp)
		}
	}
}

// TestFault_DBOutage_ProxyModes drops, resets and delays the coordinator's database connections
// through a TCP proxy (the shared container is never touched) and checks the 503 contract and the
// recovery without a coordinator restart.
func TestFault_DBOutage_ProxyModes(t *testing.T) {
	for _, mode := range []sectest.ProxyMode{sectest.ModeDrop, sectest.ModeReset} {
		mode := mode
		t.Run(mode.String(), func(t *testing.T) {
			r, p := outageRig(t)
			r.Register("w1", 1)
			job := r.Submit(3)
			a := r.Poll("w1")
			if resp := r.Do("GET", "/readyz", nil); resp.Status != http.StatusOK {
				t.Fatalf("readyz before fault = %s", resp)
			}

			p.SetMode(mode)
			probes := map[string]sectest.Resp{
				"readyz": r.Do("GET", "/readyz", nil),
				"submit": r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":{"message":"x"}}`),
				"poll":   r.Do("POST", "/v1/workers/w1/poll", ""),
				"result": r.Result(job, a.AttemptID, a.LeaseToken, "w1", 0, "", ""),
				"renew":  r.Renew(job, a.AttemptID, a.LeaseToken, "w1"),
				"get":    r.Do("GET", "/v1/jobs/"+job, nil),
			}
			for name, resp := range probes {
				if resp.Status != http.StatusServiceUnavailable || resp.ErrCode() != "SERVICE_UNAVAILABLE" || resp.Header.Get("Retry-After") != "1" {
					t.Errorf("%s during %s = %s (Retry-After %q), want 503 SERVICE_UNAVAILABLE with Retry-After: 1", name, mode, resp, resp.Header.Get("Retry-After"))
				}
				assertNoLeak(t, name, resp)
			}

			p.SetMode(sectest.ModeForward)
			deadline := time.Now().Add(15 * time.Second)
			for {
				if r.Do("GET", "/readyz", nil).Status == http.StatusOK {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("coordinator did not recover after the proxy healed")
				}
				time.Sleep(50 * time.Millisecond) // bounded poll for the pool to redial
			}
			if resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 0, "", ""); resp.Status != http.StatusOK {
				t.Fatalf("commit after recovery = %s, want 200", resp)
			}
			if r.ResultRows(job) != 1 {
				t.Fatalf("result rows = %d, want 1", r.ResultRows(job))
			}
			sectest.CheckInvariants(t, r, []string{job})
		})
	}

	t.Run("delay", func(t *testing.T) {
		r, p := outageRig(t)
		r.Register("w1", 1)
		p.SetDelay(40 * time.Millisecond)
		p.SetMode(sectest.ModeDelay)
		start := time.Now()
		job := r.Submit(3)
		if el := time.Since(start); el < 40*time.Millisecond {
			t.Fatalf("submit took %v through a 40 ms proxy, the fault was not applied", el)
		}
		a := r.Poll("w1")
		if resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 0, "", ""); resp.Status != http.StatusOK {
			t.Fatalf("commit under delay = %s", resp)
		}
		sectest.CheckInvariants(t, r, []string{job})
	})
}

// TestFault_DBOutage_OutageGraceWindow_ProtectsSurvivingCommit: the database is unreachable while a
// lease lapses; the first successful sweeper tick after the outage must open the grace window and
// hold the attempt back, so the surviving worker's commit is accepted. The control without the
// outage reaps the same attempt.
func TestFault_DBOutage_OutageGraceWindow_ProtectsSurvivingCommit(t *testing.T) {
	t.Run("outage then recovery", func(t *testing.T) {
		r, p := outageRig(t)
		r.Register("w1", 1)
		job := r.Submit(3)
		a := r.Poll("w1")
		r.MustSweep() // healthy tick anchors the sweeper
		r.Expire()    // the lease lapses while the database is about to disappear
		p.SetMode(sectest.ModeDrop)
		if err := r.Sweep(); err == nil {
			t.Fatal("sweeper tick succeeded with the database unreachable")
		}
		p.SetMode(sectest.ModeForward)
		deadline := time.Now().Add(15 * time.Second)
		for r.Do("GET", "/readyz", nil).Status != http.StatusOK {
			if time.Now().After(deadline) {
				t.Fatal("no recovery")
			}
			time.Sleep(50 * time.Millisecond) // bounded poll for the pool to redial
		}
		r.MustSweep() // first successful tick after a failed one: opens the window, reaps nothing
		if at := r.Attempts(job)[0]; at.State != domain.AttemptRunning {
			t.Fatalf("attempt %s after the post-outage tick, want RUNNING (held back by grace)", at.State)
		}
		if resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 0, "", ""); resp.Status != http.StatusOK {
			t.Fatalf("surviving worker commit = %s, want 200", resp)
		}
		if j := r.Job(job); j.State != domain.StateSucceeded {
			t.Fatalf("job %s, want SUCCEEDED", j.State)
		}
		sectest.CheckInvariants(t, r, []string{job})
	})
	t.Run("control without outage reaps", func(t *testing.T) {
		r, _ := outageRig(t)
		r.Register("w1", 1)
		job := r.Submit(3)
		a := r.Poll("w1")
		r.MustSweep()
		r.Expire()
		r.MustSweep()
		if at := r.Attempts(job)[0]; at.State != domain.AttemptLost {
			t.Fatalf("control attempt %s, want LOST", at.State)
		}
		if resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 0, "", ""); resp.Status != http.StatusConflict {
			t.Fatalf("control late commit = %s, want 409", resp)
		}
	})
}

// TestFault_DBOutage_HalfOpen probes a database that accepts connections and never answers (a
// partition, not a clean refusal). The coordinator must answer 503 within a bounded time instead of
// hanging with the request, must not stay blocked once the network heals, and a hung call must not
// stall other requests.
func TestFault_DBOutage_HalfOpen(t *testing.T) {
	r, p := outageRig(t)
	r.Register("w1", 1)
	job := r.Submit(3)
	r.Do("GET", "/readyz", nil) // warm the pool
	p.SetMode(sectest.ModeBlackhole)
	r.Client.Timeout = 15 * time.Second // above the 10 s server WriteTimeout: a bounded server answers before this

	start := time.Now()
	resp := r.Do("GET", "/readyz", nil)
	t.Logf("readyz over a half-open database: %s after %v", resp, time.Since(start))
	if resp.Status != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("readyz = %s, want 503 with Retry-After: 1 within the bound", resp)
	}
	assertNoLeak(t, "readyz", resp)

	p.SetMode(sectest.ModeForward)
	deadline := time.Now().Add(20 * time.Second)
	for r.Do("GET", "/readyz", nil).Status != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("coordinator did not recover after the partition healed")
		}
		time.Sleep(100 * time.Millisecond) // bounded poll for the pool to redial
	}
	if j := r.Do("GET", "/v1/jobs/"+job, nil); j.Status != http.StatusOK {
		t.Fatalf("get job after recovery = %s", j)
	}
}
