package logsafe_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/logsafe"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The secrets every connect error below is built with. None may appear in a log line.
const (
	leakUser = "leakuser"
	leakPass = "leakpass"
	leakDB   = "leakdb_test"
	leakHost = "db.leak.internal"
)

var (
	errHostUnreach = syscall.EHOSTUNREACH
	errNetUnreach  = syscall.ENETUNREACH
)

var classPattern = regexp.MustCompile(`^([a-z_]+|sqlstate_[0-9A-Z]{5})$`)

// closedPort returns a 127.0.0.1 port with no listener (bound, then released).
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	return port
}

// fakeServer accepts connections on 127.0.0.1 and hands each to handle. It is closed on cleanup.
func fakeServer(t *testing.T, handle func(c net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				handle(c)
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

// readStartup reads one length-prefixed startup-phase message (SSLRequest or StartupMessage).
func readStartup(c net.Conn) bool {
	var hdr [4]byte
	if _, err := ioReadFull(c, hdr[:]); err != nil {
		return false
	}
	n := int(binary.BigEndian.Uint32(hdr[:])) - 4
	if n < 0 || n > 10000 {
		return false
	}
	buf := make([]byte, n)
	_, err := ioReadFull(c, buf)
	return err == nil
}

func ioReadFull(c net.Conn, b []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := 0
	for got < len(b) {
		n, err := c.Read(b[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}

// errorResponse is a PostgreSQL ErrorResponse message with severity, code and message.
func errorResponse(code, msg string) []byte {
	var body bytes.Buffer
	for _, f := range []struct {
		k byte
		v string
	}{{'S', "FATAL"}, {'V', "FATAL"}, {'C', code}, {'M', msg}} {
		body.WriteByte(f.k)
		body.WriteString(f.v)
		body.WriteByte(0)
	}
	body.WriteByte(0)
	out := []byte{'E', 0, 0, 0, 0}
	binary.BigEndian.PutUint32(out[1:], uint32(body.Len()+4))
	return append(out, body.Bytes()...)
}

func connString(port, sslmode string) string {
	return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%s/%s?sslmode=%s&connect_timeout=5", leakUser, leakPass, port, leakDB, sslmode)
}

// connectErr runs a real pgconn connect with cfg and returns its error (nil is a test failure).
func connectErr(t *testing.T, ctx context.Context, cfg *pgconn.Config) error {
	t.Helper()
	c, err := pgconn.ConnectConfig(ctx, cfg)
	if err == nil {
		_ = c.Close(context.Background())
		t.Fatal("connect unexpectedly succeeded")
	}
	return err
}

func parse(t *testing.T, s string) *pgconn.Config {
	t.Helper()
	cfg, err := pgconn.ParseConfig(s)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg
}

// timeoutNetErr is a net.Error whose Timeout is true (an i/o timeout from a dialer).
type timeoutNetErr struct{}

func (timeoutNetErr) Error() string   { return "i/o timeout" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return true }

func dialFails(inner error) pgconn.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: network, Addr: &net.TCPAddr{IP: net.ParseIP("10.9.8.7"), Port: 6543}, Err: inner}
	}
}

// logLine renders err through logsafe.Err the way the coordinator does (JSON slog handler).
func logLine(err error) string {
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Warn("assignment pass failed", logsafe.Err(err))
	return buf.String()
}

// TestLogsafe_Class_RealPgxConnectErrors builds every connect-error class with the real pgx v5
// connect path (a closed port, a silent server, a dial that fails with the given errno, a closed
// pool, a TLS handshake against a non-TLS answer, a refused TLS request, a DNS failure, a server
// FATAL during startup) and checks the class label and that the logged line holds none of the
// host, port, user, database name, password or server message, while the raw error does (the
// positive control that the input really carries them).
func TestLogsafe_Class_RealPgxConnectErrors(t *testing.T) {
	type tc struct {
		name      string
		make      func(t *testing.T) (err error, port string)
		want      string
		rawLeaks  bool // the raw error text must contain user and database (pgconn.ConnectError)
		extraLeak []string
	}
	cases := []tc{
		{"connection refused", func(t *testing.T) (error, string) {
			p := closedPort(t)
			return connectErr(t, context.Background(), parse(t, connString(p, "disable"))), p
		}, "connection_refused", true, nil},
		{"timeout: server accepts and never answers (context deadline)", func(t *testing.T) (error, string) {
			p := fakeServer(t, func(c net.Conn) { _, _ = ioReadFull(c, make([]byte, 1<<16)) })
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			return connectErr(t, ctx, parse(t, connString(p, "disable"))), p
		}, "deadline_exceeded", true, nil},
		{"timeout: dial i/o timeout", func(t *testing.T) (error, string) {
			cfg := parse(t, connString("6543", "disable"))
			cfg.DialFunc = dialFails(timeoutNetErr{})
			return connectErr(t, context.Background(), cfg), "6543"
		}, "timeout", true, []string{"10.9.8.7"}},
		{"host unreachable", func(t *testing.T) (error, string) {
			cfg := parse(t, connString("6543", "disable"))
			cfg.DialFunc = dialFails(os.NewSyscallError("connect", errHostUnreach))
			return connectErr(t, context.Background(), cfg), "6543"
		}, "host_unreachable", true, []string{"10.9.8.7"}},
		{"network unreachable", func(t *testing.T) (error, string) {
			cfg := parse(t, connString("6543", "disable"))
			cfg.DialFunc = dialFails(os.NewSyscallError("connect", errNetUnreach))
			return connectErr(t, context.Background(), cfg), "6543"
		}, "host_unreachable", true, []string{"10.9.8.7"}},
		{"connection reset during startup", func(t *testing.T) (error, string) {
			p := fakeServer(t, func(c net.Conn) {
				readStartup(c)
				if tc, ok := c.(*net.TCPConn); ok {
					_ = tc.SetLinger(0)
				}
			})
			return connectErr(t, context.Background(), parse(t, connString(p, "disable"))), p
		}, "", true, nil},
		{"closed pool", func(t *testing.T) (error, string) {
			p := closedPort(t)
			pool, err := pgxpool.New(context.Background(), connString(p, "disable"))
			if err != nil {
				t.Fatalf("pool: %v", err)
			}
			pool.Close()
			err = pool.Ping(context.Background())
			if err == nil {
				t.Fatal("ping on a closed pool succeeded")
			}
			return err, p
		}, "closed_pool", false, nil},
		{"TLS: server answers S then plaintext", func(t *testing.T) (error, string) {
			p := fakeServer(t, func(c net.Conn) {
				if readStartup(c) {
					_, _ = c.Write([]byte("S"))
					_, _ = c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
				}
			})
			return connectErr(t, context.Background(), parse(t, connString(p, "require"))), p
		}, "tls", true, nil},
		{"TLS: server refuses TLS", func(t *testing.T) (error, string) {
			p := fakeServer(t, func(c net.Conn) {
				if readStartup(c) {
					_, _ = c.Write([]byte("N"))
				}
			})
			return connectErr(t, context.Background(), parse(t, connString(p, "require"))), p
		}, "tls", true, nil},
		{"DNS failure", func(t *testing.T) (error, string) {
			cfg := parse(t, strings.Replace(connString("6543", "disable"), "127.0.0.1", leakHost, 1))
			cfg.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
				return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			}
			return connectErr(t, context.Background(), cfg), "6543"
		}, "dns", true, []string{leakHost}},
		{"invalid sslmode in the database URL (parse error)", func(t *testing.T) (error, string) {
			_, err := pgconn.ParseConfig(strings.Replace(connString("6543", "bogus"), "127.0.0.1", leakHost, 1))
			if err == nil {
				t.Fatal("parse unexpectedly succeeded")
			}
			return err, "6543"
		}, "invalid_database_config", false, []string{leakUser, leakDB, leakHost}},
		{"server FATAL during startup (auth)", func(t *testing.T) (error, string) {
			p := fakeServer(t, func(c net.Conn) {
				if readStartup(c) {
					_, _ = c.Write(errorResponse("28P01", `password authentication failed for user "`+leakUser+`"`))
				}
			})
			return connectErr(t, context.Background(), parse(t, connString(p, "disable"))), p
		}, "sqlstate_28P01", true, []string{"password authentication failed"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err, port := c.make(t)
			raw := err.Error()
			if c.rawLeaks && (!strings.Contains(raw, "user="+leakUser) || !strings.Contains(raw, "database="+leakDB)) {
				t.Fatalf("positive control: raw pgx error does not carry user and database: %q", raw)
			}
			for _, x := range c.extraLeak {
				if !strings.Contains(raw, x) {
					t.Fatalf("positive control: raw pgx error does not carry %q: %q", x, raw)
				}
			}
			got := logsafe.Class(err)
			if c.want != "" && got != c.want {
				t.Fatalf("Class = %q, want %q (raw %q)", got, c.want, raw)
			}
			if c.want == "" && got != "connection_reset" && got != "connection_closed" {
				// A reset during startup surfaces as ECONNRESET or as EOF depending on timing.
				t.Fatalf("Class = %q, want connection_reset or connection_closed (raw %q)", got, raw)
			}
			if !classPattern.MatchString(got) {
				t.Fatalf("Class = %q is outside the closed label set", got)
			}
			line := logLine(err)
			forbidden := append([]string{leakUser, leakPass, leakDB, leakHost, "127.0.0.1", ":" + port, "10.9.8.7",
				"failed to connect", "dial", "SQLSTATE", "password authentication"}, c.extraLeak...)
			for _, f := range forbidden {
				if strings.Contains(line, f) {
					t.Fatalf("log line leaks %q: %s", f, line)
				}
			}
			if !strings.Contains(line, `"err":"`+got+`"`) {
				t.Fatalf("log line does not carry the class: %s", line)
			}
		})
	}
}

// TestLogsafe_Class_ServerAndDomainErrors covers errors that are not connect errors: a server
// error whose message, detail and internal query carry SQL and data, a hostile SQLSTATE, domain
// sentinels wrapped with text, context errors, and an unknown error with secrets in its text.
func TestLogsafe_Class_ServerAndDomainErrors(t *testing.T) {
	pgErr := &pgconn.PgError{Severity: "ERROR", Code: "42P01", Message: `relation "jobs_secret" does not exist`,
		Detail: "Key (id)=(job_secret_value)", InternalQuery: "SELECT * FROM jobs_secret", Where: "SQL statement"}
	cases := []struct {
		name   string
		err    error
		want   string
		secret []string
	}{
		{"pg error wrapped", fmt.Errorf("query: %w", pgErr), "sqlstate_42P01", []string{"jobs_secret", "job_secret_value", "SELECT"}},
		{"admin shutdown 57P01", &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}, "sqlstate_57P01", []string{"administrator"}},
		{"hostile sqlstate", &pgconn.PgError{Code: `0"}{x`, Message: "m"}, "sqlstate_invalid", []string{`0"}{x`}},
		{"lowercase sqlstate", &pgconn.PgError{Code: "abcde"}, "sqlstate_invalid", nil},
		{"domain wrapped", fmt.Errorf("%w: max_attempts must be between 1 and 20", domain.ErrInvalidRequest), "domain_invalid_request", []string{"max_attempts"}},
		{"domain lease expired", domain.ErrLeaseExpired, "domain_lease_expired", nil},
		{"invalid transition", &domain.InvalidStateTransitionError{From: "A", To: "B"}, "domain_invalid_state_transition", nil},
		{"context canceled", fmt.Errorf("x: %w", context.Canceled), "context_canceled", nil},
		{"deadline", context.DeadlineExceeded, "deadline_exceeded", nil},
		{"unknown with secrets", errors.New("pq: host=pg.internal:5432 user=secret_user password=secret_password SELECT 1"), "unclassified", []string{"pg.internal", "5432", "secret_user", "secret_password", "SELECT"}},
		{"nil", nil, "none", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := logsafe.Class(c.err)
			if got != c.want {
				t.Fatalf("Class = %q, want %q", got, c.want)
			}
			line := logLine(c.err)
			for _, s := range c.secret {
				if strings.Contains(line, s) {
					t.Fatalf("log line leaks %q: %s", s, line)
				}
			}
		})
	}
}

// TestLogsafe_Class_NeverEchoesInput: for arbitrary error text (including text that names a class
// keyword) the label is from the closed set and never contains the input.
func TestLogsafe_Class_NeverEchoesInput(t *testing.T) {
	for i := 0; i < 500; i++ {
		s := "Zq" + strconv.Itoa(i*7919) + " host=h" + strconv.Itoa(i) + ":" + strconv.Itoa(1000+i)
		for _, kw := range []string{"", " closed pool", " tls", " timeout", " failed to connect", " cannot parse"} {
			got := logsafe.Class(errors.New(s + kw))
			if !classPattern.MatchString(got) {
				t.Fatalf("label %q for %q is outside the closed set", got, s+kw)
			}
			if strings.Contains(got, "Zq") || strings.Contains(got, strconv.Itoa(1000+i)) {
				t.Fatalf("label %q echoes input %q", got, s+kw)
			}
		}
	}
}
