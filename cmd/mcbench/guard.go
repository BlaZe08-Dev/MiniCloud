package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"
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
