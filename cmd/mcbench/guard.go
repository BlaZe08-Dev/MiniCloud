package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	allowedDBHost = "127.0.0.1"
	allowedDBPort = "5433"
	forbiddenDB   = "minicloud_test"
)

// CheckDatabaseURL is the database guard (ADR-0021 section 3.5, Q7). It parses only; it never
// connects. It refuses every name except the caller's own, always refuses minicloud_test,
// requires the _test suffix, and allows only 127.0.0.1:5433.
func CheckDatabaseURL(raw, allowedName string) error {
	if allowedName == "" {
		return fmt.Errorf("--allowed-db is required")
	}
	if allowedName == forbiddenDB || !strings.HasSuffix(allowedName, "_test") {
		return fmt.Errorf("SAFETY VIOLATION: --allowed-db %q must end in _test and must not be %s", allowedName, forbiddenDB)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("database URL does not parse")
	}
	if u.Hostname() != allowedDBHost || u.Port() != allowedDBPort {
		return fmt.Errorf("SAFETY VIOLATION: database host must be %s:%s", allowedDBHost, allowedDBPort)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name != allowedName {
		return fmt.Errorf("SAFETY VIOLATION: database %q is not the allowed database %q", name, allowedName)
	}
	// pgx lets query parameters override the URL path and host, so only sslmode is allowed.
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fmt.Errorf("SAFETY VIOLATION: database URL query does not parse")
	}
	for k := range q {
		if !strings.EqualFold(k, "sslmode") {
			return fmt.Errorf("SAFETY VIOLATION: database URL query parameter %q is not allowed (only sslmode)", k)
		}
	}
	// Belt and braces: parse the way the driver does (never connect) and check the result.
	cfg, err := pgconn.ParseConfig(raw)
	if err != nil {
		return fmt.Errorf("SAFETY VIOLATION: database URL does not parse as a connection string")
	}
	if cfg.Database != allowedName || cfg.Host != allowedDBHost || fmt.Sprint(cfg.Port) != allowedDBPort || len(cfg.Fallbacks) > 0 {
		return fmt.Errorf("SAFETY VIOLATION: effective connection %s:%d/%s is not the allowed %s:%s/%s",
			cfg.Host, cfg.Port, cfg.Database, allowedDBHost, allowedDBPort, allowedName)
	}
	return nil
}

// CheckLoopbackURL refuses a coordinator URL whose host is not a loopback address.
func CheckLoopbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("coordinator URL does not parse")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("coordinator host %q is not a loopback address; only 127.0.0.1 is allowed", u.Hostname())
	}
	return nil
}
