package api_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// SEC-066 (P4-22): the three API log sites that print a repository error. Each request also checks
// that the HTTP status is the one the error mapped to before the change (503 for a connection
// error and a failed readiness probe, 500 for an unmapped error): only the log text changed.

type apiLeakConnectError struct{}

func (apiLeakConnectError) Error() string {
	return "failed to connect to `user=leakuser database=leakdb_test`: 10.9.8.7:6543 (db.leak.internal): " +
		"dial error: dial tcp 10.9.8.7:6543: connect: connection refused"
}

func (apiLeakConnectError) Unwrap() error {
	return &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("10.9.8.7"), Port: 6543},
		Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
}

type apiLockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *apiLockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *apiLockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureAPILogs(t *testing.T) *apiLockedBuffer {
	t.Helper()
	buf := &apiLockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestLogSafe_APISites_LogClassOnly_StatusUnchanged(t *testing.T) {
	unmapped := errors.New(`ERROR: relation "jobs_secret" does not exist; SELECT id FROM jobs_secret; host=pg.internal:5432 user=secret_user password=secret_password`)
	cases := []struct {
		name       string
		err        error
		method     string
		path       string
		body       string
		wantStatus int
		msg        string
		class      string
		secrets    []string
	}{
		{"connection error on submit", apiLeakConnectError{}, "POST", "/v1/jobs", `{"task_type":"echo","payload":{"m":"x"}}`,
			http.StatusServiceUnavailable, "database outage or connection failure", "connection_refused",
			[]string{"leakuser", "leakdb_test", "10.9.8.7", "6543", "db.leak.internal", "dial"}},
		{"readiness probe", apiLeakConnectError{}, "GET", "/readyz", "",
			http.StatusServiceUnavailable, "readiness probe failed", "connection_refused",
			[]string{"leakuser", "leakdb_test", "10.9.8.7", "6543", "db.leak.internal", "dial"}},
		{"unmapped error", unmapped, "POST", "/v1/jobs", `{"task_type":"echo","payload":{"m":"x"}}`,
			http.StatusInternalServerError, "internal server error", "unclassified",
			[]string{"jobs_secret", "SELECT", "pg.internal", "5432", "secret_user", "secret_password"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := captureAPILogs(t)
			ts, _ := setupErrorServer(c.err)
			defer ts.Close()
			req, err := http.NewRequest(c.method, ts.URL+c.path, strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("status %d, want %d", resp.StatusCode, c.wantStatus)
			}
			out := logs.String()
			var hit bool
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, `"msg":"`+c.msg+`"`) {
					hit = true
					if !strings.Contains(l, `"err":"`+c.class+`"`) {
						t.Fatalf("%q line does not carry class %q: %s", c.msg, c.class, l)
					}
				}
			}
			if !hit {
				t.Fatalf("positive control: no %q line was logged; logs:\n%s", c.msg, out)
			}
			for _, s := range c.secrets {
				if strings.Contains(out, s) {
					t.Fatalf("SECURITY LEAK: logs contain %q:\n%s", s, out)
				}
			}
		})
	}
}
