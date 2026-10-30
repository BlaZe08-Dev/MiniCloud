package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "valid UTF-8 text",
			input: "Hello, World! café 🚀 日本語 123",
			want:  "Hello, World! café 🚀 日本語 123",
		},
		{
			name:  "preserves tabs and newlines",
			input: "\tfirst line\n\tsecond line\n",
			want:  "\tfirst line\n\tsecond line\n",
		},
		{
			name:  "ESC clear-screen",
			input: "\x1b[2J",
			want:  `\x1b[2J`,
		},
		{
			name:  "OSC title",
			input: "\x1b]0;pwned-title\x07",
			want:  `\x1b]0;pwned-title\x07`,
		},
		{
			name:  "backspace",
			input: "foo\x08\x08bar",
			want:  `foo\x08\x08bar`,
		},
		{
			name:  "CR - lone carriage return",
			input: "hello\rworld",
			want:  `hello\rworld`,
		},
		{
			name:  "CR - CRLF normalized to LF",
			input: "line1\r\nline2\r\n",
			want:  "line1\nline2\n",
		},
		{
			name:  "CR - multiple CR before LF",
			input: "\r\r\n",
			want:  `\r` + "\n",
		},
		{
			name:  "C1 control characters",
			input: "\u0080\u009b",
			want:  `\u0080\u009b`,
		},
		{
			name:  "DEL",
			input: "abc\x7fdef",
			want:  `abc\x7fdef`,
		},
		{
			name:  "NUL byte",
			input: "start\x00end",
			want:  `start\x00end`,
		},
		{
			name:  "C0 controls NUL and SOH",
			input: "start\x00\x01end",
			want:  `start\x00\x01end`,
		},
		{
			name:  "bidi overrides U+202E and U+202D",
			input: "abc\u202edef\u202dghi",
			want:  `abc\u202edef\u202dghi`,
		},
		{
			name:  "zero-width space U+200B",
			input: "zero\u200bwidth",
			want:  `zero\u200bwidth`,
		},
		{
			name:  "zero-width no-break space / BOM U+FEFF",
			input: "\ufeffbom",
			want:  `\ufeffbom`,
		},
		{
			name:  "line and paragraph separators U+2028 and U+2029",
			input: "line1\u2028line2\u2029line3",
			want:  `line1\u2028line2\u2029line3`,
		},
		{
			// Policy for NBSP (U+00A0): Escaped as `\u00a0`. NBSP renders visually identical
			// to standard ASCII space (U+0020), enabling terminal visual spoofing of task names,
			// worker IDs, and CLI flags. Escaping makes non-ASCII whitespace explicitly visible to operators.
			name:  "NBSP U+00A0 (escaped to prevent visual spoofing)",
			input: "non\u00a0breaking",
			want:  `non\u00a0breaking`,
		},
		{
			name:  "invalid UTF-8 bytes",
			input: "bad\xff\xfe\x80bytes",
			want:  `bad\xff\xfe\x80bytes`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitize(tc.input)
			if got != tc.want {
				t.Fatalf("sanitize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSanitize_64KiBMixedHostileText(t *testing.T) {
	// Build a 64-byte chunk containing mixed hostile elements:
	// - ASCII and multi-byte UTF-8
	// - C0 ANSI escape sequence (\x1b[31m)
	// - CRLF and lone CR
	// - DEL (\x7f)
	// - C1 control rune (\u0080)
	// - Bidi override (\u202e)
	// - Zero-width space (\u200b)
	// - BOM (\ufeff)
	// - Separators (\u2028, \u2029)
	// - NBSP (\u00a0)
	// - NUL byte (\x00)
	// - Invalid UTF-8 byte (\xff)
	chunk := "A\x1b[31mB\r\nC\rD\x7fE\u0080F\u202eG\u200bH\ufeffI\u2028J\u2029K\u00a0L\x00M\xffN01234567890123456789"
	if len(chunk) != 64 {
		t.Fatalf("chunk length = %d, want 64", len(chunk))
	}

	wantChunk := sanitize(chunk)
	// Exactly 1024 repetitions = 65,536 bytes (64 KiB).
	input := strings.Repeat(chunk, 1024)
	if len(input) != 65536 {
		t.Fatalf("input length = %d, want 65536", len(input))
	}
	want := strings.Repeat(wantChunk, 1024)

	got := sanitize(input)
	if got != want {
		t.Fatalf("sanitize(64 KiB mixed hostile text) output mismatch; len(got)=%d, len(want)=%d", len(got), len(want))
	}
}

func TestSanitizeJSON(t *testing.T) {
	t.Run("standard valid JSON unchanged", func(t *testing.T) {
		input := `{"task_type":"echo","payload":{"msg":"hello world"},"count":42}`
		got := sanitizeJSON(input)
		if got != input {
			t.Fatalf("sanitizeJSON(%q) = %q, want %q", input, got, input)
		}
		if !json.Valid([]byte(got)) {
			t.Fatalf("sanitizeJSON produced invalid JSON: %s", got)
		}
	})

	t.Run("hostile C1 and DEL rewritten and decode-equal", func(t *testing.T) {
		// Hostile JSON containing DEL (0x7f) and C1 controls (U+0080, U+009B, U+009F).
		orig := "{\"status\":\"ok\x7f\",\"worker_id\":\"w_\u0080\u009b\u009f_test\",\"data\":\"normal text\"}"
		got := sanitizeJSON(orig)

		// Must be valid JSON.
		if !json.Valid([]byte(got)) {
			t.Fatalf("sanitizeJSON result is not valid JSON: %s", got)
		}

		// Must have rewritten DEL and C1 to \u00xx escapes.
		if strings.Contains(got, "\x7f") {
			t.Fatalf("sanitizeJSON output still contains raw DEL: %s", got)
		}
		if strings.Contains(got, "\u0080") || strings.Contains(got, "\u009b") || strings.Contains(got, "\u009f") {
			t.Fatalf("sanitizeJSON output still contains raw C1 characters: %s", got)
		}
		if !strings.Contains(got, `\u007f`) || !strings.Contains(got, `\u0080`) || !strings.Contains(got, `\u009b`) || !strings.Contains(got, `\u009f`) {
			t.Fatalf("sanitizeJSON output missing expected \\u00xx escapes: %s", got)
		}

		// Decode-equality: decoding the sanitized JSON must yield identical data to decoding original JSON.
		var origMap, gotMap map[string]string
		if err := json.Unmarshal([]byte(orig), &origMap); err != nil {
			t.Fatalf("unmarshaling orig failed: %v", err)
		}
		if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
			t.Fatalf("unmarshaling got failed: %v", err)
		}
		if !reflect.DeepEqual(origMap, gotMap) {
			t.Fatalf("decode equality mismatch:\norig: %+v\ngot:  %+v", origMap, gotMap)
		}
	})
}
