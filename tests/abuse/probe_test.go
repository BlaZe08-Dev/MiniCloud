package abuse_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// TestProbe prints what the API does for a battery of hostile requests (exploration only; gated by env).
func TestProbe(t *testing.T) {
	if os.Getenv("SECTEST_PROBE") == "" {
		t.Skip("exploration only")
	}
	r := sectest.NewRig(t, sectest.KindMemory)
	r.Register("w1", 1)
	job := r.Submit(3)
	deep := strings.Repeat("[", 5000) + strings.Repeat("]", 5000)
	cases := []struct{ name, method, path, body string }{
		{"empty body", "POST", "/v1/jobs", ""},
		{"brace", "POST", "/v1/jobs", "{"},
		{"array", "POST", "/v1/jobs", "[]"},
		{"null", "POST", "/v1/jobs", "null"},
		{"trailing second object", "POST", "/v1/jobs", `{"task_type":"echo","payload":{}} {"task_type":"sleep"}`},
		{"trailing garbage", "POST", "/v1/jobs", `{"task_type":"echo","payload":{}}garbage`},
		{"BOM", "POST", "/v1/jobs", "\xef\xbb\xbf" + `{"task_type":"echo","payload":{}}`},
		{"dup field", "POST", "/v1/jobs", `{"task_type":"echo","task_type":"sleep","payload":{}}`},
		{"unknown field", "POST", "/v1/jobs", `{"task_type":"echo","payload":{},"admin":true}`},
		{"task NUL", "POST", "/v1/jobs", "{\"task_type\":\"ec\\u0000ho\",\"payload\":{}}"},
		{"task echo marker", "POST", "/v1/jobs", `{"task_type":"ECHOMARKER<script>","payload":{}}`},
		{"max_attempts huge", "POST", "/v1/jobs", `{"task_type":"echo","payload":{},"max_attempts":9223372036854775807}`},
		{"max_attempts overflow", "POST", "/v1/jobs", `{"task_type":"echo","payload":{},"max_attempts":99999999999999999999}`},
		{"max_attempts 1e999", "POST", "/v1/jobs", `{"task_type":"echo","payload":{},"max_attempts":1e999}`},
		{"max_attempts float", "POST", "/v1/jobs", `{"task_type":"echo","payload":{},"max_attempts":1.5}`},
		{"max_attempts string", "POST", "/v1/jobs", `{"task_type":"echo","payload":{},"max_attempts":"3"}`},
		{"timeout negative", "POST", "/v1/jobs", `{"task_type":"echo","payload":{},"timeout_seconds":-5}`},
		{"deep payload", "POST", "/v1/jobs", `{"task_type":"echo","payload":` + deep + `}`},
		{"payload string", "POST", "/v1/jobs", `{"task_type":"echo","payload":"x"}`},
		{"register negative slots", "POST", "/v1/workers/register", `{"id":"x1","total_slots":-1}`},
		{"register huge slots", "POST", "/v1/workers/register", `{"id":"x2","total_slots":9223372036854775807}`},
		{"register traversal id", "POST", "/v1/workers/register", `{"id":"../etc","total_slots":1}`},
		{"heartbeat negative", "POST", "/v1/workers/w1/heartbeat", `{"available_slots":-9223372036854775808}`},
		{"result exit huge", "POST", "/v1/jobs/" + job + "/attempts/att_x/result", `{"worker_id":"w1","lease_token":"t","exit_code":9223372036854775807}`},
		{"result exit overflow", "POST", "/v1/jobs/" + job + "/attempts/att_x/result", `{"worker_id":"w1","lease_token":"t","exit_code":99999999999999999999999}`},
		{"GET on POST route", "GET", "/v1/jobs", ""},
		{"DELETE job", "DELETE", "/v1/jobs/" + job, ""},
		{"PUT submit", "PUT", "/v1/jobs", "{}"},
		{"unknown path", "GET", "/v2/jobs", ""},
		{"trailing slash", "GET", "/v1/jobs/" + job + "/", ""},
		{"traversal", "GET", "/v1/jobs/..%2f..%2fetc%2fpasswd", ""},
		{"dotdot", "GET", "/v1/jobs/../workers", ""},
		{"NUL id", "GET", "/v1/jobs/a%00b", ""},
		{"newline id", "GET", "/v1/jobs/a%0ab", ""},
		{"unicode id", "GET", "/v1/jobs/%E2%80%AEjob", ""},
		{"fullwidth id", "GET", "/v1/jobs/%EF%BD%8Aob", ""},
		{"long id", "GET", "/v1/jobs/" + strings.Repeat("a", 129), ""},
		{"huge id", "GET", "/v1/jobs/" + strings.Repeat("a", 200000), ""},
		{"percent id", "GET", "/v1/jobs/%", ""},
		{"encoded slash id", "GET", "/v1/jobs/a%2Fb", ""},
		{"worker id in job route", "GET", "/v1/jobs/w1", ""},
		{"job id in worker poll", "POST", "/v1/workers/" + job + "/poll", ""},
	}
	for _, c := range cases {
		var resp sectest.Resp
		var body any
		if c.body != "" || c.method == "POST" {
			body = c.body
		}
		resp = r.Do(c.method, c.path, body)
		fmt.Printf("PROBE %-26s -> %d ct=%q code=%q body=%.110q\n", c.name, resp.Status, resp.Header.Get("Content-Type"), resp.ErrCode(), strings.TrimSpace(string(resp.Body)))
	}
}
