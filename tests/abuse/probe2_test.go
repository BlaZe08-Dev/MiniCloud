package abuse_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

func TestProbe2(t *testing.T) {
	if os.Getenv("SECTEST_PROBE") == "" {
		t.Skip("exploration only")
	}
	r := sectest.NewRig(t, sectest.KindMemory)
	r.Register("w1", 1)
	job := r.Submit(3)
	a := r.Poll("w1")
	show := func(name string, resp sectest.Resp) {
		fmt.Printf("PROBE %-34s -> %d code=%q len=%d body=%.100q\n", name, resp.Status, resp.ErrCode(), len(resp.Body), strings.TrimSpace(string(resp.Body)))
	}
	valid := `{"task_type":"echo","payload":{"message":"x"}}`
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "application/json; charset=utf-16", "multipart/form-data", "", "application/xml"} {
		show("content-type "+ct, r.Do("POST", "/v1/jobs", valid, "Content-Type", ct))
	}
	big := strings.Repeat("a", 1024*1024+1)
	show("1MiB+1 body", r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":"`+big+`"}`))
	show("payload 65536", r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":"`+strings.Repeat("a", 65536-2)+`"}`))
	show("payload 65537", r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":"`+strings.Repeat("a", 65536-1)+`"}`))
	show("task_type 500KB", r.Do("POST", "/v1/jobs", `{"task_type":"`+strings.Repeat("t", 500000)+`","payload":{}}`))
	show("task_type 65", r.Do("POST", "/v1/jobs", `{"task_type":"`+strings.Repeat("t", 65)+`","payload":{}}`))
	res := func(output, errs string) sectest.Resp {
		return r.Do("POST", "/v1/jobs/"+job+"/attempts/"+a.AttemptID+"/result", map[string]any{"lease_token": "bad", "worker_id": "w1", "exit_code": 1, "output": output, "error": errs})
	}
	show("result bad token small", res("", "e"))
	show("result output 65537 bad tok", res(strings.Repeat("o", 65537), ""))
	show("result error 900KB bad tok", res("", strings.Repeat("e", 900000)))
	// with the right token
	good := func(output, errs, class string) sectest.Resp {
		return r.Do("POST", "/v1/jobs/"+job+"/attempts/"+a.AttemptID+"/result", map[string]any{"lease_token": a.LeaseToken, "worker_id": "w1", "exit_code": 1, "output": output, "error": errs, "error_classification": class})
	}
	show("result error 10241 good tok", good("", strings.Repeat("e", 10241), "TRANSIENT"))
	j := r.Job(job)
	fmt.Printf("PROBE job after: %s attempts=%d\n", j.State, len(r.Attempts(job)))
	if len(r.Attempts(job)) > 0 {
		at := r.Attempts(job)[0]
		fmt.Printf("PROBE attempt error len=%d state=%s\n", len(at.Error), at.State)
	}
	_ = coordinator.MaxTaskNameLength
	_ = time.Second
	addr := sectest.StartRealServer(t, r.Svc)
	raw := func(name, req string) {
		out, err := sectest.RawExchange(addr, []byte(req), 4*time.Second)
		first := strings.SplitN(out, "\r\n", 2)[0]
		fmt.Printf("PROBE raw %-30s -> %q err=%v\n", name, first, err)
	}
	raw("huge header 1.2MiB", "GET /healthz HTTP/1.1\r\nHost: x\r\nX-A: "+strings.Repeat("a", 1200000)+"\r\n\r\n")
	hdrs := strings.Repeat("X-H: 1\r\n", 20000)
	raw("20000 headers", "GET /healthz HTTP/1.1\r\nHost: x\r\n"+hdrs+"\r\n")
	raw("dup content-length", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\nContent-Length: 9\r\n\r\n{}")
	raw("CL + TE chunked", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n")
	raw("negative CL", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nContent-Length: -1\r\n\r\n")
	raw("non-numeric CL", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nContent-Length: abc\r\n\r\n")
	raw("CL 10GB, tiny body", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nContent-Length: 10000000000\r\n\r\n{}")
	raw("bad method FOO", "FOO /healthz HTTP/1.1\r\nHost: x\r\n\r\n")
	raw("HTTP/0.9 style", "GET /healthz\r\n\r\n")
	raw("no Host HTTP/1.1", "GET /healthz HTTP/1.1\r\n\r\n")
	raw("control char in path", "GET /healthz\x01 HTTP/1.1\r\nHost: x\r\n\r\n")
	raw("absolute URI", "GET http://evil.example/healthz HTTP/1.1\r\nHost: x\r\n\r\n")
	raw("expect 100", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nContent-Length: 2\r\n\r\n{}")
	raw("xff+host spoof", "GET /healthz HTTP/1.1\r\nHost: evil\r\nX-Forwarded-For: 1.2.3.4\r\nX-Forwarded-Host: evil\r\n\r\n")
	raw("percent path", "GET /v1/jobs/% HTTP/1.1\r\nHost: x\r\n\r\n")
	raw("long path 100KB", "GET /v1/jobs/"+strings.Repeat("a", 100000)+" HTTP/1.1\r\nHost: x\r\n\r\n")
	raw("long path 2MB", "GET /v1/jobs/"+strings.Repeat("a", 2000000)+" HTTP/1.1\r\nHost: x\r\n\r\n")
	out, _ := sectest.RawExchange(addr, []byte("GET /healthz HTTP/1.1\r\nHost: evil\r\nX-Forwarded-For: 1.2.3.4\r\nConnection: close\r\n\r\n"), 3*time.Second)
	fmt.Printf("PROBE echo of headers: %v\n", strings.Contains(out, "evil") || strings.Contains(out, "1.2.3.4"))
}
