package abuse_test

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

func statusLine(raw string) string { return strings.SplitN(raw, "\r\n", 2)[0] }

func healthy(t *testing.T, addr string) {
	t.Helper()
	if got := sectest.DoRaw("http://" + addr + "/healthz"); got != http.StatusOK {
		t.Errorf("server unhealthy: GET /healthz = %d", got)
	}
}

// TestAbuse_PathParameters (AB-08): traversal, encoded separators, control characters, unicode
// tricks and very long ids on every id-bearing route, sent raw so the client cannot normalise them.
func TestAbuse_PathParameters(t *testing.T) {
	r := sectest.NewRig(t, sectest.KindMemory)
	addr := sectest.StartRealServer(t, r.Svc)
	r.Register("w1", 1)
	job := r.Submit(3)
	ids := map[string]string{
		"traversal":         "..%2f..%2fetc%2fpasswd",
		"encoded dots":      "%2e%2e",
		"NUL":               "a%00b",
		"newline":           "a%0ab",
		"CRLF injection":    "a%0d%0aX-Injected:%201",
		"RTL override":      "%E2%80%AEjob",
		"fullwidth letters": "%EF%BD%8Aob",
		"zero width":        "job%E2%80%8B1",
		"NFD accent":        "e%CC%81",
		"emoji":             "%F0%9F%92%A5",
		"space":             "a%20b",
		"semicolon":         "a;b",
		"quote":             "a%27b",
		"sql":               "x%27%20OR%20%271%27%3D%271",
		"backslash":         "a%5Cb",
		"encoded slash":     "a%2Fb",
		"percent only":      "%",
		"bad escape":        "%zz",
		"129 chars":         strings.Repeat("a", 129),
		"100 KB":            strings.Repeat("a", 100000),
	}
	routes := []struct{ method, tmpl string }{
		{"GET", "/v1/jobs/%s"}, {"GET", "/v1/jobs/%s/attempts"}, {"POST", "/v1/jobs/%s/cancel"},
		{"POST", "/v1/workers/%s/heartbeat"}, {"POST", "/v1/workers/%s/poll"},
		{"POST", "/v1/jobs/%s/attempts/att_1/result"}, {"POST", "/v1/jobs/" + job + "/attempts/%s/renew"},
	}
	for name, id := range ids {
		for _, rt := range routes {
			req := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: x\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}", rt.method, fmt.Sprintf(rt.tmpl, id))
			out, _ := sectest.RawExchange(addr, []byte(req), 4*time.Second)
			line := statusLine(out)
			if strings.HasPrefix(line, "HTTP/1.1 5") {
				t.Errorf("%s on %s: %s", name, rt.tmpl, line)
			}
			if !(strings.HasPrefix(line, "HTTP/1.1 400") || strings.HasPrefix(line, "HTTP/1.1 404") || strings.HasPrefix(line, "HTTP/1.1 431") || strings.HasPrefix(line, "HTTP/1.1 414")) {
				t.Errorf("%s on %s %s: got %q, want 400/404/414/431", name, rt.method, rt.tmpl, line)
			}
			if strings.Contains(strings.ToLower(out), "x-injected") {
				t.Errorf("%s: CRLF injection reached the response headers: %.200q", name, out)
			}
			if len(out) > 2048 {
				t.Errorf("%s on %s: %d-byte response to a hostile id", name, rt.tmpl, len(out))
			}
			scanLeak(t, name, out)
		}
	}
	// Path cleaning: the standard mux redirects dot segments instead of resolving them in place.
	out, _ := sectest.RawExchange(addr, []byte("GET /v1/jobs/../workers HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"), 3*time.Second)
	if !strings.HasPrefix(statusLine(out), "HTTP/1.1 301") || strings.Contains(strings.ToLower(out), "location: http") {
		t.Errorf("dot segments: %q, want a relative 301", statusLine(out))
	}
	if j := r.Job(job); j.State != "QUEUED" {
		t.Errorf("hostile ids changed job state to %s", j.State)
	}
	healthy(t, addr)
}

// TestAbuse_MethodsAndPaths (AB-03, F-4e-10): wrong methods and unknown paths. They are refused
// with 404/405 and an Allow header, but the body is the standard library's plain text, not the
// JSON error envelope that docs/protocol.md section 2 promises.
func TestAbuse_MethodsAndPaths(t *testing.T) {
	r := sectest.NewRig(t, sectest.KindMemory)
	r.Register("w1", 1)
	job := r.Submit(3)
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/jobs"}, {"PUT", "/v1/jobs"}, {"DELETE", "/v1/jobs/" + job}, {"PATCH", "/v1/jobs/" + job}, {"POST", "/v1/jobs/" + job},
		{"GET", "/v1/jobs/" + job + "/cancel"}, {"GET", "/v1/workers/w1/poll"}, {"GET", "/v1/workers/register"}, {"POST", "/v1/workers"},
		{"OPTIONS", "/v1/jobs"}, {"POST", "/healthz"}, {"DELETE", "/v1/workers/w1/heartbeat"},
	} {
		resp := r.Do(c.method, c.path, nil)
		if resp.Status != http.StatusMethodNotAllowed || resp.Header.Get("Allow") == "" {
			t.Errorf("%s %s = %s (Allow %q), want 405 with Allow", c.method, c.path, resp, resp.Header.Get("Allow"))
		}
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") { // F-4e-10
			t.Errorf("%s %s: Content-Type %q; F-4e-10 expects the mux's text/plain", c.method, c.path, resp.Header.Get("Content-Type"))
		}
		scanLeak(t, c.method+" "+c.path, string(resp.Body))
	}
	for _, p := range []string{"/", "/v2/jobs", "/v1", "/v1/", "/v1/jobs/", "/v1/jobs/" + job + "/", "/admin", "/debug/pprof/", "/metrics", "/.env", "/v1/jobs/" + job + "/attempts/x", "/v1/workers/w1"} {
		resp := r.Do("GET", p, nil)
		if resp.Status != http.StatusNotFound && resp.Status != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %s, want 404/405", p, resp)
		}
		scanLeak(t, p, string(resp.Body))
	}
	if resp := r.Do("HEAD", "/healthz", nil); resp.Status != http.StatusOK || len(resp.Body) != 0 {
		t.Errorf("HEAD /healthz = %d with %d body bytes", resp.Status, len(resp.Body))
	}
}

// TestAbuse_HeaderAbuse (AB-09): oversize and malformed headers and request framing, raw.
func TestAbuse_HeaderAbuse(t *testing.T) {
	r := sectest.NewRig(t, sectest.KindMemory)
	addr := sectest.StartRealServer(t, r.Svc)
	before := queued(t, r)
	cases := []struct {
		name, req, want string
	}{
		{"1.2 MiB header", "GET /healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\nX-A: " + strings.Repeat("a", 1200000) + "\r\n\r\n", "HTTP/1.1 431"},
		{"2 MB request target", "GET /v1/jobs/" + strings.Repeat("a", 2000000) + " HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", "HTTP/1.1 431"},
		{"duplicate Content-Length", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: 5\r\nContent-Length: 9\r\n\r\n{}", "HTTP/1.1 400"},
		{"Content-Length and chunked", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n", "HTTP/1.1 400"},
		{"negative Content-Length", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: -1\r\n\r\n", "HTTP/1.1 400"},
		{"non-numeric Content-Length", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: abc\r\n\r\n", "HTTP/1.1 400"},
		{"10 GB Content-Length", "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: 10000000000\r\n\r\n{}", "HTTP/1.1 400"},
		{"unknown method", "FOO /healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", "HTTP/1.1 405"},
		{"HTTP/0.9 style", "GET /healthz\r\n\r\n", "HTTP/1.1 400"},
		{"no Host header", "GET /healthz HTTP/1.1\r\nConnection: close\r\n\r\n", "HTTP/1.1 400"},
		{"control byte in target", "GET /healthz\x01 HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", "HTTP/1.1 400"},
		{"bare LF line endings in header", "GET /healthz HTTP/1.1\nHost: x\nConnection: close\n\n", "HTTP/1.1 200"},
		{"obsolete line folding", "GET /healthz HTTP/1.1\r\nHost: x\r\nX-A: a\r\n b\r\nConnection: close\r\n\r\n", "HTTP/1.1 200"},
		{"absolute-form target", "GET http://evil.example/healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", "HTTP/1.1 200"},
		{"spoofed forwarding headers", "GET /healthz HTTP/1.1\r\nHost: evil.example\r\nX-Forwarded-For: 6.6.6.6\r\nX-Forwarded-Host: evil.example\r\nX-Real-IP: 6.6.6.6\r\nConnection: close\r\n\r\n", "HTTP/1.1 200"},
		{"20000 small headers", "GET /healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n" + strings.Repeat("X-H: 1\r\n", 20000) + "\r\n", "HTTP/1.1 200"},
		{"smuggling shape: body on GET", "GET /healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: 40\r\n\r\nGET /v1/jobs HTTP/1.1\r\nHost: x\r\n\r\n", "HTTP/1.1 200"},
	}
	for _, c := range cases {
		out, _ := sectest.RawExchange(addr, []byte(c.req), 6*time.Second)
		if !strings.HasPrefix(statusLine(out), c.want) {
			t.Errorf("%s: got %q, want prefix %q", c.name, statusLine(out), c.want)
		}
		if strings.Contains(out, "evil.example") || strings.Contains(out, "6.6.6.6") {
			t.Errorf("%s: a request header value is reflected in the response", c.name)
		}
		if strings.Count(out, "HTTP/1.1 ") > 1 {
			t.Errorf("%s: more than one response to one request (desync)", c.name)
		}
		scanLeak(t, c.name, out)
	}
	// An oversized chunked body is cut at the 1 MiB limit.
	chunk := strings.Repeat("a", 64*1024)
	var b strings.Builder
	b.WriteString("POST /v1/jobs HTTP/1.1\r\nHost: x\r\nConnection: close\r\nTransfer-Encoding: chunked\r\n\r\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "%x\r\n%s\r\n", len(chunk), chunk)
	}
	b.WriteString("0\r\n\r\n")
	out, _ := sectest.RawExchange(addr, []byte(b.String()), 6*time.Second)
	if !strings.HasPrefix(statusLine(out), "HTTP/1.1 400") && !strings.HasPrefix(statusLine(out), "HTTP/1.1 413") {
		t.Errorf("1.25 MiB chunked body: %q, want 400/413", statusLine(out))
	}
	if after := queued(t, r); after != before {
		t.Errorf("header abuse created jobs: %d -> %d", before, after)
	}
	healthy(t, addr)
}

// TestAbuse_SlowClients (AB-12): slow headers and a stalled body are cut off by the server's
// read timeouts (2 s header, 5 s read), including 100 at once, while the server stays responsive.
// The waits are the timeouts under test, measured with a bounded deadline.
func TestAbuse_SlowClients(t *testing.T) {
	r := sectest.NewRig(t, sectest.KindMemory)
	addr := sectest.StartRealServer(t, r.Svc)

	closedWithin := func(c net.Conn, limit time.Duration) (time.Duration, bool) {
		start := time.Now()
		_ = c.SetReadDeadline(time.Now().Add(limit))
		buf := make([]byte, 4096)
		for {
			if _, err := c.Read(buf); err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					return time.Since(start), false
				}
				return time.Since(start), true
			}
		}
	}

	t.Run("slow headers", func(t *testing.T) {
		t.Parallel()
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _ = c.Write([]byte("GET /healthz HTTP/1.1\r\nHost: x\r\nX-Slow: "))
		stop := make(chan struct{})
		go func() { // one byte every 300 ms keeps the connection looking alive
			tk := time.NewTicker(300 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tk.C:
					if _, err := c.Write([]byte("a")); err != nil {
						return
					}
				}
			}
		}()
		el, closed := closedWithin(c, 6*time.Second)
		close(stop)
		if !closed || el > 4*time.Second {
			t.Errorf("slow-header connection closed=%v after %v, want closed within ~2 s (ReadHeaderTimeout)", closed, el)
		}
	})
	t.Run("stalled body", func(t *testing.T) {
		t.Parallel()
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _ = c.Write([]byte("POST /v1/jobs HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 200\r\n\r\n{\"task_type\":"))
		el, closed := closedWithin(c, 9*time.Second)
		if !closed || el > 8*time.Second {
			t.Errorf("stalled-body connection closed=%v after %v, want closed within ~5 s (ReadTimeout)", closed, el)
		}
	})
	t.Run("100 slowloris at once", func(t *testing.T) {
		t.Parallel()
		var wg sync.WaitGroup
		var mu sync.Mutex
		slow := 0
		for i := 0; i < 100; i++ {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = c.Write([]byte("GET /healthz HTTP/1.1\r\nHost: x\r\n"))
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				if el, closed := closedWithin(c, 6*time.Second); !closed || el > 4*time.Second {
					mu.Lock()
					slow++
					mu.Unlock()
				}
			}()
		}
		start := time.Now()
		healthy(t, addr) // served while 100 half-sent requests are open
		if el := time.Since(start); el > time.Second {
			t.Errorf("healthz took %v with 100 slowloris connections open", el)
		}
		wg.Wait()
		if slow > 0 {
			t.Errorf("%d of 100 slowloris connections outlived the header timeout", slow)
		}
	})
}

// TestAbuse_ManyConnections (AB-13, F-4e-17): there is no cap on simultaneous connections; they are
// bounded only by the read timeouts. 400 idle connections are all accepted, the server keeps
// serving, the timeouts reap them, and 300 concurrent requests then all succeed.
func TestAbuse_ManyConnections(t *testing.T) {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err == nil && rl.Cur < 2000 {
		t.Skipf("file descriptor limit %d is too low for this test", rl.Cur)
	}
	r := sectest.NewRig(t, sectest.KindMemory)
	addr := sectest.StartRealServer(t, r.Svc)
	const idle = 400
	conns := make([]net.Conn, 0, idle)
	for i := 0; i < idle; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("connection %d refused: %v (a connection cap would be a defence, record it)", i, err)
		}
		conns = append(conns, c)
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	healthy(t, addr)
	reaped := 0
	deadline := time.Now().Add(6 * time.Second)
	for _, c := range conns {
		_ = c.SetReadDeadline(deadline)
		if _, err := c.Read(make([]byte, 1)); err != nil {
			if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
				reaped++
			}
		}
	}
	if reaped != idle {
		t.Errorf("%d of %d idle connections were reaped by the server's timeouts", reaped, idle)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for i := 0; i < 300; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if sectest.DoRaw("http://"+addr+"/healthz") != http.StatusOK {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if failed > 0 {
		t.Errorf("%d of 300 concurrent requests failed", failed)
	}
}
