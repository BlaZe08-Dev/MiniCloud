package main

import (
	"strings"
	"testing"
)

func TestMigrateCLI_ResolveDatabaseURL_EnvironmentVariable(t *testing.T) {
	fakeEnv := map[string]string{
		"MINICLOUD_DATABASE_URL": "postgres://user:secret@localhost:5432/minicloud_dev?sslmode=disable",
	}
	getenv := func(key string) string {
		return fakeEnv[key]
	}

	var warnings []string
	warnLogger := func(msg string, args ...any) {
		warnings = append(warnings, msg)
	}

	url, err := ResolveDatabaseURL("", getenv, warnLogger)
	if err != nil {
		t.Fatalf("expected successful resolution, got error: %v", err)
	}
	if url != fakeEnv["MINICLOUD_DATABASE_URL"] {
		t.Fatalf("expected url %q, got %q", fakeEnv["MINICLOUD_DATABASE_URL"], url)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings when reading exclusively from environment, got: %v", warnings)
	}
}

func TestMigrateCLI_ResolveDatabaseURL_DeprecatedFlagWarning(t *testing.T) {
	getenv := func(key string) string {
		return ""
	}

	var warnings []string
	warnLogger := func(msg string, args ...any) {
		warnings = append(warnings, msg)
	}

	flagVal := "postgres://user:flag_secret@localhost:5432/minicloud_dev"
	url, err := ResolveDatabaseURL(flagVal, getenv, warnLogger)
	if err != nil {
		t.Fatalf("expected successful fallback to flag, got error: %v", err)
	}
	if url != flagVal {
		t.Fatalf("expected url %q, got %q", flagVal, url)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 deprecation warning, got %d", len(warnings))
	}
	if !strings.Contains(warnings[0], "process table") {
		t.Fatalf("expected deprecation warning to explain process table exposure, got: %s", warnings[0])
	}
}

func TestMigrateCLI_ResolveDatabaseURL_MissingURL(t *testing.T) {
	getenv := func(key string) string {
		return ""
	}
	warnLogger := func(msg string, args ...any) {}

	_, err := ResolveDatabaseURL("", getenv, warnLogger)
	if err == nil {
		t.Fatalf("expected error when neither flag nor env is provided, got nil")
	}
	if !strings.Contains(err.Error(), "MINICLOUD_DATABASE_URL") {
		t.Fatalf("expected error message to reference MINICLOUD_DATABASE_URL, got: %v", err)
	}
}
