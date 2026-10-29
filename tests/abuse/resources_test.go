package abuse_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// TestAbuse_CrossResourceIDs (AB-10): an id of one resource kind used as another kind, or an id of
// another job, never reaches the wrong object and never changes state.
func TestAbuse_CrossResourceIDs(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		r.Register("w1", 1)
		r.Register("w2", 1)
		jA, jB := r.Submit(3), r.Submit(3)
		aA, aB := r.Poll("w1"), r.Poll("w2")
		snap := func() string {
			var b strings.Builder
			for _, id := range []string{jA, jB} {
				fmt.Fprintf(&b, "%s:%s:%d|", id, r.Job(id).State, len(r.Attempts(id)))
				for _, a := range r.Attempts(id) {
					fmt.Fprintf(&b, "%s=%s@%v,", a.ID, a.State, a.LeaseExpiresAt != nil)
				}
			}
			return b.String()
		}
		before := snap()
		type c struct {
			name, method, path string
			body               any
			want               []int
		}
		cases := []c{
			{"worker id as job id (get)", "GET", "/v1/jobs/w1", nil, []int{404}},
			{"attempt id as job id (get)", "GET", "/v1/jobs/" + aA.AttemptID, nil, []int{404}},
			{"attempt id as job id (attempts)", "GET", "/v1/jobs/" + aA.AttemptID + "/attempts", nil, []int{404}},
			{"attempt id as job id (cancel)", "POST", "/v1/jobs/" + aA.AttemptID + "/cancel", "", []int{404}},
			{"worker id as job id (cancel)", "POST", "/v1/jobs/w1/cancel", "", []int{404}},
			{"job id as worker id (poll)", "POST", "/v1/workers/" + jA + "/poll", "", []int{404}},
			{"job id as worker id (heartbeat)", "POST", "/v1/workers/" + jA + "/heartbeat", `{}`, []int{404}},
			{"job B with attempt of A (result)", "POST", "/v1/jobs/" + jB + "/attempts/" + aA.AttemptID + "/result",
				map[string]any{"lease_token": aA.LeaseToken, "worker_id": "w1", "exit_code": 0}, []int{404, 409}},
			{"job A with attempt of B (renew)", "POST", "/v1/jobs/" + jA + "/attempts/" + aB.AttemptID + "/renew",
				map[string]any{"lease_token": aB.LeaseToken, "worker_id": "w2"}, []int{404, 409}},
			{"job id as attempt id", "POST", "/v1/jobs/" + jA + "/attempts/" + jA + "/result",
				map[string]any{"lease_token": aA.LeaseToken, "worker_id": "w1", "exit_code": 0}, []int{404, 409}},
			{"w2 reports A's attempt with A's token", "POST", "/v1/jobs/" + jA + "/attempts/" + aA.AttemptID + "/result",
				map[string]any{"lease_token": aA.LeaseToken, "worker_id": "w2", "exit_code": 0}, []int{403}},
			{"w1 renews B's attempt with A's token", "POST", "/v1/jobs/" + jB + "/attempts/" + aB.AttemptID + "/renew",
				map[string]any{"lease_token": aA.LeaseToken, "worker_id": "w1"}, []int{403, 409}},
		}
		for _, x := range cases {
			resp := r.Do(x.method, x.path, x.body)
			safe(t, x.name, resp)
			ok := false
			for _, w := range x.want {
				ok = ok || resp.Status == w
			}
			if !ok {
				t.Errorf("%s = %s, want one of %v", x.name, resp, x.want)
			}
			if strings.Contains(string(resp.Body), aA.LeaseToken) || strings.Contains(string(resp.Body), aB.LeaseToken) {
				t.Errorf("%s: response echoes a lease token", x.name)
			}
		}
		if after := snap(); after != before {
			t.Fatalf("cross-resource requests changed state:\nbefore %s\nafter  %s", before, after)
		}
		sectest.CheckInvariants(t, r, []string{jA, jB})
	})
}

// TestAbuse_UnauthenticatedEndpoints (AB-11, SEC-004): every endpoint answers an anonymous caller
// exactly as it answers a caller sending bogus credentials, i.e. no authentication exists. This is
// the accepted V1 gap (mitigated by the 127.0.0.1 bind); the test records the behaviour, including
// the consequences that matter most: an anonymous caller can cancel any job, claim work as any
// registered worker (receiving its lease token) and re-register over an existing worker id. It
// pins the current behaviour so the arrival of authentication (V3) must update it consciously.
func TestAbuse_UnauthenticatedEndpoints(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		r.Register("victim", 2)
		job := r.Submit(3)
		bogus := []string{"Authorization", "Bearer not-a-token", "X-API-Key", "nope", "X-Admin", "1"}

		type call struct {
			name, method, path string
			body               any
			want               int
		}
		var jobForCancel string
		calls := []call{
			{"healthz", "GET", "/healthz", nil, 200},
			{"readyz", "GET", "/readyz", nil, 200},
			{"submit", "POST", "/v1/jobs", validSubmit, 201},
			{"get job", "GET", "/v1/jobs/" + job, nil, 200},
			{"list attempts", "GET", "/v1/jobs/" + job + "/attempts", nil, 200},
			{"list workers", "GET", "/v1/workers", nil, 200},
			{"register worker", "POST", "/v1/workers/register", `{"id":"anon","total_slots":1,"supported_tasks":["echo"]}`, 200},
			{"heartbeat as another worker", "POST", "/v1/workers/victim/heartbeat", `{}`, 200},
		}
		for _, c := range calls {
			plain := r.Do(c.method, c.path, c.body)
			with := r.Do(c.method, c.path, c.body, bogus...)
			if plain.Status != c.want || with.Status != c.want {
				t.Errorf("%s: anonymous %d, bogus credentials %d, want %d for both (no authentication, SEC-004)", c.name, plain.Status, with.Status, c.want)
			}
		}
		// Claim work as the victim without any credential, and use the stolen lease token.
		asg := r.Poll("victim")
		if asg == nil || asg.JobID != job || asg.LeaseToken == "" {
			t.Fatalf("anonymous poll as victim = %+v, want the queued job with a lease token (SEC-004)", asg)
		}
		if resp := r.Renew(asg.JobID, asg.AttemptID, asg.LeaseToken, "victim"); resp.Status != 200 {
			t.Errorf("anonymous renew with the stolen token = %s", resp)
		}
		// Re-register over a live worker: the capacity is derived from RUNNING attempts, so it cannot be
		// pushed below what is running by this route alone; record what the stored worker looks like.
		resp := r.Do("POST", "/v1/workers/register", `{"id":"victim","version":"hijack","total_slots":1,"supported_tasks":["sleep"]}`)
		ws, _ := r.Repo.ListWorkers(context.Background())
		var v *domain.Worker
		for _, w := range ws {
			if w.ID == "victim" {
				v = w
			}
		}
		t.Logf("re-registering a live worker: %d; stored version %q total_slots %d tasks %v (SEC-004: accepted, not an additional finding)", resp.Status, v.Version, v.TotalSlots, v.SupportedTasks)
		if resp.Status != 200 {
			t.Errorf("re-register = %s", resp)
		}
		// Anyone can cancel any job.
		jobForCancel = r.Submit(3)
		if resp := r.Cancel(jobForCancel); resp.Status != 200 {
			t.Errorf("anonymous cancel = %s", resp)
		}
		if resp := r.Result(asg.JobID, asg.AttemptID, asg.LeaseToken, "victim", 0, "", ""); resp.Status != 200 && resp.Status != 409 {
			t.Errorf("anonymous result with the stolen token = %s", resp)
		}
	})
}

// TestAbuse_ErrorLeakScan (AB-14): across a lifecycle with wrong tokens, foreign ids and malformed
// bodies, no response and no log line contains stack traces, SQL, file paths, connection details, or
// any lease token other than in the poll response that legitimately carries it.
func TestAbuse_ErrorLeakScan(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		logs := sectest.CaptureLogs(t)
		r := sectest.NewRig(t, kind)
		r.Register("w1", 1)
		job := r.Submit(3)
		a := r.Poll("w1")
		var bodies []string
		add := func(resp sectest.Resp) { bodies = append(bodies, string(resp.Body)) }
		for _, tok := range []string{"", "wrong", strings.Repeat("A", 4096), a.LeaseToken[:10]} {
			add(r.Result(job, a.AttemptID, tok, "w1", 0, "", ""))
			add(r.Renew(job, a.AttemptID, tok, "w1"))
		}
		add(r.Do("POST", "/v1/jobs", `{"task_type":"nope","payload":{}}`))
		add(r.Do("POST", "/v1/jobs", "{"))
		add(r.Do("GET", "/v1/jobs/"+job+"x", nil))
		add(r.Do("POST", "/v1/workers/ghost/poll", ""))
		add(r.Result(job, a.AttemptID, a.LeaseToken, "w2", 0, "", ""))
		for i, b := range bodies {
			scanLeak(t, fmt.Sprintf("response #%d", i), b, a.LeaseToken)
		}
		scanLeak(t, "coordinator log", logs.String(), a.LeaseToken)
		if strings.Contains(logs.String(), domain.HashLeaseToken(a.LeaseToken)) {
			t.Error("coordinator log contains the lease token hash")
		}
	})
}

// TestAbuse_MissingLimits (AB-15, F-4e-17): the API has no rate limit and no cap on queued jobs or
// registered workers. The test submits and registers in bulk and records that all are accepted.
// Bounded by size only through SEC-010 (open): in-memory growth; PostgreSQL growth is limited by
// disk. Counts are modest on purpose (controlled test, not a load test).
func TestAbuse_MissingLimits(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		jobs, workers := 2000, 1000
		if kind == sectest.KindPostgres {
			jobs, workers = 300, 200
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		accepted, refused := 0, 0
		start := time.Now()
		sem := make(chan struct{}, 16)
		for i := 0; i < jobs; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				resp := r.Do("POST", "/v1/jobs", validSubmit)
				mu.Lock()
				defer mu.Unlock()
				if resp.Status == http.StatusCreated {
					accepted++
				} else {
					refused++
				}
			}()
		}
		wg.Wait()
		for i := 0; i < workers; i++ {
			resp := r.Do("POST", "/v1/workers/register", fmt.Sprintf(`{"id":"flood_%d","total_slots":1,"supported_tasks":["echo"]}`, i))
			if resp.Status != 200 {
				t.Errorf("worker %d refused: %s", i, resp)
			}
		}
		list := r.Do("GET", "/v1/workers", nil)
		t.Logf("%s: %d/%d jobs accepted, %d refused, %d workers registered, GET /v1/workers = %d bytes unpaginated, %v elapsed",
			kind, accepted, jobs, refused, workers, len(list.Body), time.Since(start).Round(10*time.Millisecond))
		if accepted != jobs || refused != 0 {
			t.Errorf("accepted %d refused %d of %d: a limit appeared, update F-4e-17", accepted, refused, jobs)
		}
		if resp := r.Do("GET", "/readyz", nil); resp.Status != 200 {
			t.Errorf("server unhealthy after the flood: %s", resp)
		}
	})
}

// mcRun runs the real mc binary against url and returns its stdout.
func mcRun(t *testing.T, url string, args ...string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(sectest.Binaries(t), "mc"), args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "MINICLOUD_COORDINATOR_URL=" + url}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mc %v: %v\n%s", args, err, out)
	}
	return string(out)
}

const hostileError = "\x1b[2J\x1b[31mRED\x1b[0m\x1b]0;pwned-title\x07 first line\r\nFAKE: Result: Exit Code: 0\x01\x08\x08\x08tail"

// TestAbuse_HostileErrorText_API: the API itself carries the hostile text safely: JSON escapes every
// control character, so no raw control byte is ever served, and the stored text is bounded.
func TestAbuse_HostileErrorText_API(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		r.Register("w1", 1)
		job := r.Submit(1)
		a := r.Poll("w1")
		long := strings.Repeat("L", 10240-len(hostileError))
		if resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 1, hostileError+long, "PERMANENT"); resp.Status != 200 {
			t.Fatalf("commit = %s", resp)
		}
		for _, path := range []string{"/v1/jobs/" + job, "/v1/jobs/" + job + "/attempts"} {
			resp := r.Do("GET", path, nil)
			for _, b := range resp.Body {
				if b < 0x20 && b != '\n' {
					t.Fatalf("%s served a raw control byte 0x%02x", path, b)
				}
			}
			if len(resp.Body) > 40_000 {
				t.Errorf("%s response is %d bytes", path, len(resp.Body))
			}
		}
	})
}

// TestAbuse_MCOutput_HostileErrorText (F-4e-18): mc get and mc attempts print worker-supplied error
// text raw, so terminal escape sequences, backspaces, carriage returns and NULs reach the user's
// terminal. cmd/mc is not an owned file of P4-4e, so the fix is proposed in the handoff and the
// demonstration is kept out of the default run: set MINICLOUD_SEC_DEMO_F4E18=1 to execute it (it
// fails until mc sanitises control characters).
func TestAbuse_MCOutput_HostileErrorText(t *testing.T) {
	if os.Getenv("MINICLOUD_SEC_DEMO_F4E18") == "" {
		t.Skip("finding F-4e-18 (mc prints raw terminal escape sequences from error text): demonstration test, set MINICLOUD_SEC_DEMO_F4E18=1 to run; the fix belongs to cmd/mc, outside this task's owned files")
	}
	r := sectest.NewRig(t, sectest.KindMemory)
	r.Register("w1", 1)
	job := r.Submit(1)
	a := r.Poll("w1")
	long := strings.Repeat("L", 10240-len(hostileError))
	if resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 1, hostileError+long, "PERMANENT"); resp.Status != 200 {
		t.Fatalf("commit = %s", resp)
	}
	for _, args := range [][]string{{"get", job}, {"attempts", job}, {"workers"}} {
		out := mcRun(t, r.Srv.URL, args...)
		for i := 0; i < len(out); i++ {
			if b := out[i]; (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f {
				t.Errorf("mc %v printed raw control byte 0x%02x at offset %d", args, b, i)
				break
			}
		}
		for _, line := range strings.Split(out, "\n") {
			if len(line) > 12_000 {
				t.Errorf("mc %v printed a %d-byte line", args, len(line))
			}
		}
	}
}
