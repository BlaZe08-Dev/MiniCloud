package faultinject_test

import (
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// coordinatorLogLeaks returns the forbidden database detail found in coordinator log output: the
// address the coordinator dials (host:port and :port), the upstream database address, the database
// user and name in every form pgx renders them, the password and URL, and SQL statement text. The
// coordinator's own bind address is legitimate and is not a target.
func coordinatorLogLeaks(logs, dialAddr, upstreamAddr, dbURL string) []string {
	u, _ := url.Parse(dbURL)
	user, pass := "", ""
	if u != nil && u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	dbName := ""
	if u != nil {
		dbName = strings.TrimPrefix(u.Path, "/")
	}
	_, dialPort, _ := net.SplitHostPort(dialAddr)
	_, upPort, _ := net.SplitHostPort(upstreamAddr)
	targets := []struct{ name, pattern string }{
		{"dial_host_port", dialAddr},
		{"dial_port_colon", ":" + dialPort},
		{"upstream_host_port", upstreamAddr},
		{"upstream_port_colon", ":" + upPort},
		{"db_url", dbURL},
		{"db_password", pass},
		{"db_user_kv", "user=" + user},
		{"db_name", dbName},
		{"db_name_kv", "database=" + dbName},
		{"dial_error", "dial tcp"},
		{"dial_error_pgx", "dial error"},
		{"failed_to_connect", "failed to connect to"},
		{"sql_select", "SELECT "},
		{"sql_insert", "INSERT INTO"},
		{"sql_update", "UPDATE "},
		{"sql_delete", "DELETE FROM"},
		{"sql_from", " FROM "},
		{"sql_advisory", "pg_try_advisory"},
		{"sql_search_path", "search_path"},
	}
	var found []string
	for _, tg := range targets {
		if tg.pattern == "" || tg.pattern == ":" || tg.pattern == "user=" || tg.pattern == "database=" {
			continue
		}
		if strings.Contains(logs, tg.pattern) {
			found = append(found, tg.name+" ("+tg.pattern+")")
		}
	}
	return found
}

// offendingLines returns the log lines that contain any of the patterns, for the failure message.
func offendingLines(logs string, patterns ...string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		for _, p := range patterns {
			if p != "" && strings.Contains(line, p) {
				out = append(out, line)
				break
			}
		}
	}
	return out
}

// TestFault_DBOutage_RoundRobin_CoordinatorLogsNoDBDetail is SEC-066 (P4-22): the real coordinator
// binary on the round_robin policy reaches the test database only through a fault proxy; the proxy
// drops every connection mid-run (the shared container is never touched) so the assignment loop,
// the pass refresh, the lease sweeper and the HTTP handlers all fail against the database. The
// coordinator's whole log output is then scanned for the dialled host and port, the database user,
// name, password and URL, and SQL text. The log lines that prove the failure paths ran are a
// positive control: without them the scan would be vacuous.
func TestFault_DBOutage_RoundRobin_CoordinatorLogsNoDBDetail(t *testing.T) {
	pg := sectest.NewPGSchema(t) // skips when the database URL is unset
	proxy := sectest.NewFaultProxy(t, pg.UpstreamAddr())
	bin := sectest.Binaries(t)
	addr := sectest.FreePort(t)
	base := "http://" + addr
	dbURL := pg.URLVia(proxy.Addr())

	env := sectest.CoordinatorEnv(addr, dbURL, realLease, realGrace)
	for i, kv := range env {
		if strings.HasPrefix(kv, "MINICLOUD_DISPATCH_POLICY=") {
			env[i] = "MINICLOUD_DISPATCH_POLICY=round_robin"
		}
	}
	coord := sectest.StartProc(t, "coordinator", filepath.Join(bin, "coordinator"), env)
	if !sectest.WaitFor(15*time.Second, func() bool { return sectest.DoRaw(base+"/readyz") == http.StatusOK }) {
		t.Fatalf("coordinator not ready; output:\n%s", redact(coord.Out.String(), dbURL))
	}
	if !strings.Contains(coord.Out.String(), `"policy":"round_robin"`) {
		t.Fatalf("coordinator is not on round_robin; output:\n%s", redact(coord.Out.String(), dbURL))
	}

	c := &http.Client{Timeout: 10 * time.Second}
	if r := sectest.DoURL(t, c, base, "POST", "/v1/workers/register", `{"id":"leak-w1","total_slots":2,"supported_tasks":["echo"]}`); r.Status != http.StatusCreated && r.Status != http.StatusOK {
		t.Fatalf("register: %s", r)
	}
	if r := sectest.DoURL(t, c, base, "POST", "/v1/jobs", `{"task_type":"echo","payload":{"msg":"before"}}`); r.Status != http.StatusCreated {
		t.Fatalf("submit: %s", r)
	}
	_ = sectest.DoURL(t, c, base, "POST", "/v1/workers/leak-w1/poll", nil)

	// The outage: every live and new database connection is closed by the proxy.
	proxy.SetMode(sectest.ModeDrop)
	_ = sectest.DoRaw(base + "/readyz")
	_ = sectest.DoURL(t, c, base, "POST", "/v1/jobs", `{"task_type":"echo","payload":{"msg":"during"}}`)
	_ = sectest.DoURL(t, c, base, "POST", "/v1/workers/leak-w1/poll", nil)
	_ = sectest.DoURL(t, c, base, "GET", "/v1/jobs/job_outage_check", nil)

	controls := []string{"assignment pass failed", "lease sweeper tick failed"}
	ran := sectest.WaitFor(15*time.Second, func() bool {
		out := coord.Out.String()
		for _, m := range controls {
			if !strings.Contains(out, m) {
				return false
			}
		}
		return true
	})
	if !ran {
		t.Fatalf("positive control: the failure paths %q did not all log during the outage; output:\n%s", controls, redact(coord.Out.String(), dbURL))
	}

	// Recovery without a restart, so the scan also covers the recovery path.
	proxy.SetMode(sectest.ModeForward)
	if !sectest.WaitFor(15*time.Second, func() bool { return sectest.DoRaw(base+"/readyz") == http.StatusOK }) {
		t.Fatalf("coordinator did not recover; output:\n%s", redact(coord.Out.String(), dbURL))
	}
	coord.Term()
	coord.WaitExit(10 * time.Second)

	logs := coord.Out.String()
	if found := coordinatorLogLeaks(logs, proxy.Addr(), pg.UpstreamAddr(), dbURL); len(found) > 0 {
		_, dialPort, _ := net.SplitHostPort(proxy.Addr())
		lines := offendingLines(logs, ":"+dialPort, "dial tcp", "failed to connect", "SELECT ", " FROM ", "search_path")
		if len(lines) > 5 {
			lines = lines[:5]
		}
		t.Fatalf("SECURITY LEAK IN COORDINATOR LOGS: %v\nfirst offending lines:\n%s", found, redact(strings.Join(lines, "\n"), dbURL))
	}
}

// TestFault_CoordinatorLogLeakScanner_PositiveControl proves the scanner of the test above detects
// each class of database detail in a real pgx connect-error line (it runs without a database).
func TestFault_CoordinatorLogLeakScanner_PositiveControl(t *testing.T) {
	dial := "127.0.0.1:41234"
	up := "127.0.0.1:5433"
	dbURL := "postgres://scanuser:scanpass@" + dial + "/scan_claude_test?sslmode=disable"
	cases := map[string]string{
		"pgx connect error": `{"level":"WARN","msg":"assignment pass failed","err":"failed to connect to ` + "`user=scanuser database=scan_claude_test`" + `: 127.0.0.1:41234 (127.0.0.1): dial error: dial tcp 127.0.0.1:41234: connect: connection refused"}`,
		"upstream port":     `{"msg":"x","err":"127.0.0.1:5433"}`,
		"sql text":          `{"msg":"x","err":"ERROR: relation does not exist; SELECT id FROM jobs"}`,
		"password":          `{"msg":"x","err":"scanpass"}`,
		"db name":           `{"msg":"x","err":"scan_claude_test"}`,
	}
	for name, line := range cases {
		if found := coordinatorLogLeaks(line, dial, up, dbURL); len(found) == 0 {
			t.Errorf("%s: scanner missed the planted detail in %s", name, line)
		}
	}
	clean := `{"level":"WARN","msg":"assignment pass failed","err_class":"connection_refused"}` + "\n" +
		`{"level":"INFO","msg":"coordinator listening for requests","addr":"127.0.0.1:18080"}`
	if found := coordinatorLogLeaks(clean, dial, up, dbURL); len(found) > 0 {
		t.Errorf("scanner flagged a clean log: %v", found)
	}
}
