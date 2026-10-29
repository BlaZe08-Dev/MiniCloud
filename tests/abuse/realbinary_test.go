package abuse_test

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// TestAbuse_RealBinaryBattery runs the real coordinator binary against PostgreSQL, throws a battery
// of hostile requests at it, and checks the process survives, stays ready, created no job, and
// printed nothing sensitive (stack traces, SQL, the database password, lease tokens) on stdout or
// stderr.
func TestAbuse_RealBinaryBattery(t *testing.T) {
	pg := sectest.NewPGSchema(t)
	addr := sectest.FreePort(t)
	bin := sectest.Binaries(t)
	c := sectest.StartProc(t, "coordinator", filepath.Join(bin, "coordinator"), sectest.CoordinatorEnv(addr, pg.SchemaURL, 3*time.Second, 3*time.Second))
	base := "http://" + addr
	if !sectest.WaitFor(15*time.Second, func() bool { return sectest.DoRaw(base+"/readyz") == http.StatusOK }) {
		t.Fatalf("coordinator not ready:\n%s", c.Out.String())
	}
	u, _ := url.Parse(pg.SchemaURL)
	password, _ := u.User.Password()

	// One legitimate lifecycle so a lease token exists to look for in the output.
	reg := sectest.DoURL(t, http.DefaultClient, base, "POST", "/v1/workers/register", `{"id":"w1","total_slots":1,"supported_tasks":["echo"]}`)
	sub := sectest.DoURL(t, http.DefaultClient, base, "POST", "/v1/jobs", validSubmit)
	poll := sectest.DoURL(t, http.DefaultClient, base, "POST", "/v1/workers/w1/poll", "")
	jobID, attID, token := sectest.AssignmentOf(poll)
	if reg.Status != 200 || sub.Status != 201 || token == "" {
		t.Fatalf("setup failed: %s %s %s", reg, sub, poll)
	}
	jobsBefore := pg.Count(t, "SELECT count(*) FROM jobs")

	deep := strings.Repeat("[", 400000) + strings.Repeat("]", 400000)
	raw := []string{
		"POST /v1/jobs HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: 1\r\n\r\n{",
		"POST /v1/jobs HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: 5\r\n\r\n\x00\x00\x00\x00\x00",
		"POST /v1/jobs HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: " + fmt.Sprint(len(deep)+30) + "\r\n\r\n{\"task_type\":\"echo\",\"payload\":" + deep + "}",
		"GET /v1/jobs/" + strings.Repeat("a", 5000) + " HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n",
		"GET /v1/jobs/a%00b HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n",
		"GET /v1/jobs/%zz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n",
		"POST /v1/jobs/" + jobID + "/attempts/" + attID + "/result HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: 74\r\n\r\n{\"lease_token\":\"nope\",\"worker_id\":\"w1\",\"exit_code\":0,\"output\":\"\\u0000\",\"error\":\"\"}",
		"POST /v1/jobs/" + jobID + "/attempts/" + attID + "/renew HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: 2\r\n\r\n{}",
		"POST /v1/workers/register HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: 35\r\n\r\n{\"id\":\"x'--\",\"version\":\"v\",\"total_slots\":1}",
		"FOO / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n",
		"GET /healthz HTTP/1.1\r\nConnection: close\r\nX-A: " + strings.Repeat("a", 1200000) + "\r\n\r\n",
	}
	for i, req := range raw {
		out, _ := sectest.RawExchange(addr, []byte(req), 8*time.Second)
		if strings.HasPrefix(statusLine(out), "HTTP/1.1 5") {
			t.Errorf("raw request #%d: %q", i, statusLine(out))
		}
		scanLeak(t, fmt.Sprintf("raw response #%d", i), out, token)
	}
	if !c.Alive() {
		t.Fatalf("the coordinator died; output:\n%s", c.Out.String())
	}
	if got := sectest.DoRaw(base + "/readyz"); got != http.StatusOK {
		t.Errorf("readyz after the battery = %d", got)
	}
	if got := pg.Count(t, "SELECT count(*) FROM jobs"); got != jobsBefore {
		t.Errorf("jobs %d -> %d: a hostile request created a job", jobsBefore, got)
	}
	// The legitimate worker can still finish its attempt.
	if ok := sectest.DoURL(t, http.DefaultClient, base, "POST", "/v1/jobs/"+jobID+"/attempts/"+attID+"/result",
		map[string]any{"lease_token": token, "worker_id": "w1", "exit_code": 0, "output": "o"}); ok.Status != 200 {
		t.Errorf("legitimate commit after the battery = %s", ok)
	}
	out := c.Out.String()
	scanLeak(t, "coordinator output", out, token, password)
	if strings.Contains(out, "panic") || strings.Contains(out, "goroutine") {
		t.Errorf("coordinator output contains a panic or stack trace")
	}
	sectest.CheckInvariants(t, sectest.NewRig(t, sectest.KindPostgres, sectest.Options{PG: pg}), []string{jobID})
}
