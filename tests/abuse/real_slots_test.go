package abuse_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// ADR-0020 Section 2.5 (V2-0b): a registration that declares concurrency and many slots is no longer
// ignored by the round-robin pass. These rows prove the named bound and the strict field type.

func registerDeclared(t *testing.T, r *sectest.RRRig, id string, slots int) {
	t.Helper()
	resp := r.Do("POST", "/v1/workers/register", map[string]any{
		"id": id, "version": "sec", "total_slots": slots,
		"supported_tasks":       []string{"echo", "sleep", "sha256_loop"},
		"executes_concurrently": true,
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("register %s: %s", id, resp)
	}
}

// TestAbuse_Register_LargeDeclaredSlots_ReservationCap: a worker that declares 1024 concurrent slots
// and polls once, then never again, holds at most domain.MaxReservationsPerWorker reservations,
// however deep the backlog. After the accept window its reservations lapse, it is no longer
// eligible (stale poll), and an honest worker is reserved one of those jobs.
func TestAbuse_Register_LargeDeclaredSlots_ReservationCap(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRRRig(t, kind, sectest.RROptions{})
		registerDeclared(t, r, "w_big", 1024)
		if resp := r.PollResp("w_big"); resp.Status != http.StatusNoContent {
			t.Fatalf("first poll of w_big = %s, want 204", resp)
		}
		jobs := make([]string, 20)
		for i := range jobs {
			jobs[i] = r.Submit(3)
		}
		r.Pass()
		held := 0
		for _, j := range jobs {
			if r.Holder(j) == "w_big" {
				held++
			}
		}
		if held != domain.MaxReservationsPerWorker {
			t.Fatalf("w_big holds %d of 20 jobs after one round, want the cap %d", held, domain.MaxReservationsPerWorker)
		}

		r.Elapse(r.Settings().AcceptWindow + time.Second)
		// An honest one-slot worker joins. Its first poll finds the pass heartbeat stale, refreshes
		// it (ADR-0017 Section 3.2): the refresh lapses w_big's reservations, w_big is not eligible
		// (stale poll), so the job is reserved to the honest worker, which claims it by its own arm.
		if resp := r.Register("w_honest", 1); resp.Status != http.StatusOK {
			t.Fatalf("register w_honest: %s", resp)
		}
		got := r.Poll("w_honest")
		if got == nil || got.Policy != domain.DispatchPolicyRoundRobin {
			t.Fatalf("honest worker's poll = %+v, want a round_robin claim", got)
		}
		claimedHeld := false
		for _, j := range jobs[:domain.MaxReservationsPerWorker] {
			if got.JobID == j {
				claimedHeld = true
			}
		}
		if !claimedHeld {
			t.Fatalf("honest worker claimed %s, want one of the jobs w_big held (the oldest)", got.JobID)
		}
		r.Pass()
		for _, j := range jobs {
			if r.Holder(j) == "w_big" {
				t.Fatalf("w_big holds %s after its poll went stale", j)
			}
		}
	})
}

// TestAbuse_Register_ExecutesConcurrentlyNotBoolean_400: a non-boolean executes_concurrently fails
// JSON decoding and gets the existing 400 MALFORMED_JSON; nothing is registered.
func TestAbuse_Register_ExecutesConcurrentlyNotBoolean_400(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRRRig(t, kind, sectest.RROptions{})
		for i, v := range []any{"yes", 1, []bool{true}, map[string]any{"x": true}} {
			id := "w_bad_" + string(rune('a'+i))
			resp := r.Do("POST", "/v1/workers/register", map[string]any{
				"id": id, "version": "sec", "total_slots": 4,
				"supported_tasks":       []string{"echo"},
				"executes_concurrently": v,
			})
			if resp.Status != http.StatusBadRequest || !strings.Contains(string(resp.Body), "MALFORMED_JSON") {
				t.Fatalf("executes_concurrently=%#v: %s, want 400 MALFORMED_JSON", v, resp)
			}
			if p := r.PollResp(id); p.Status != http.StatusNotFound {
				t.Fatalf("worker %s registered despite the 400: poll = %s, want 404", id, p)
			}
		}
	})
}
