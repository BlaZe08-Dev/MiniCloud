package version_test

import (
	"regexp"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/version"
)

// TestVersion_Format: Version is non-empty, within the coordinator's 64 character cap (SEC-016)
// and looks like a release tag.
func TestVersion_Format(t *testing.T) {
	v := version.Version
	if v == "" {
		t.Fatal("Version is empty")
	}
	if len(v) > 64 {
		t.Fatalf("Version length %d exceeds 64 (SEC-016)", len(v))
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+`).MatchString(v) {
		t.Fatalf("Version %q does not match ^v[0-9]+\\.[0-9]+\\.[0-9]+", v)
	}
}
