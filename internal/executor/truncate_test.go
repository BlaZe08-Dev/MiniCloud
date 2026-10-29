package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampText(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  string // "" means: check the invariants only
	}{
		{"short unchanged", "hello", 100, "hello"},
		{"exactly at limit unchanged", strings.Repeat("a", 100), 100, strings.Repeat("a", 100)},
		{"ascii over limit", strings.Repeat("a", 200), 100, strings.Repeat("a", 100-len(TruncationMarker)) + TruncationMarker},
		{"cut inside a 3-byte rune backs up", strings.Repeat("€", 100), 100, ""},
		{"cut inside a 4-byte rune backs up", strings.Repeat("😀", 100), 99, ""},
		{"invalid bytes replaced one for one", strings.Repeat("\xff", 10), 100, strings.Repeat("�", 10)},
		{"invalid bytes that grow past the limit", strings.Repeat("\xff", 40), 100, ""},
		{"limit smaller than the marker", strings.Repeat("a", 50), 3, TruncationMarker},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClampText(c.in, c.limit)
			if c.want != "" && got != c.want {
				t.Fatalf("ClampText = %q, want %q", got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}
			if c.limit >= len(TruncationMarker) && len(got) > c.limit {
				t.Fatalf("result is %d bytes, over the limit %d", len(got), c.limit)
			}
			// The length the coordinator sees after a JSON round trip must equal the length sent.
			raw, _ := json.Marshal(got)
			var back string
			if err := json.Unmarshal(raw, &back); err != nil || len(back) != len(got) {
				t.Fatalf("JSON round trip changed the length: %d -> %d (%v)", len(got), len(back), err)
			}
		})
	}
}

// TestRegistryExecute_OutputCapOnFailure guards that a failing task's output is clamped like a
// succeeding one's (it used to be returned unbounded).
func TestRegistryExecute_OutputCapOnFailure(t *testing.T) {
	r := NewRegistry()
	r.Register(failBigTask{})
	out, code, err := r.Execute(context.Background(), "failbig", nil, 0)
	if err == nil || code != 1 {
		t.Fatalf("code %d err %v, want a failure", code, err)
	}
	if len(out) != MaxOutputSizeBytes || !strings.HasSuffix(out, TruncationMarker) {
		t.Fatalf("failed task output is %d bytes, want %d ending in the marker", len(out), MaxOutputSizeBytes)
	}
}

type failBigTask struct{}

func (failBigTask) Name() string            { return "failbig" }
func (failBigTask) Validate(_ []byte) error { return nil }
func (failBigTask) Execute(_ context.Context, _ []byte) (string, error) {
	return strings.Repeat("o", 3*MaxOutputSizeBytes), errors.New("boom")
}
