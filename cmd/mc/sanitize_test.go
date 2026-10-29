package main

import (
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
			name:  "C0 controls NUL and SOH",
			input: "start\x00\x01end",
			want:  `start\x00\x01end`,
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

func TestSanitize_VeryLongText(t *testing.T) {
	// 65 KiB of printable text interspersed with control characters.
	base := strings.Repeat("A", 1000)
	var sb strings.Builder
	for i := 0; i < 65; i++ {
		sb.WriteString(base)
		sb.WriteString("\x1b[31m")
		sb.WriteString("\r\n")
		sb.WriteString("line\r")
	}
	input := sb.String()
	out := sanitize(input)

	// Verify no raw control characters remain (except \n and \t).
	for i := 0; i < len(out); i++ {
		b := out[i]
		if (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f {
			t.Fatalf("sanitize produced raw control byte 0x%02x at offset %d", b, i)
		}
	}
	// Verify it still contains the base characters.
	if !strings.Contains(out, "AAAA") {
		t.Fatal("sanitized output lost expected printable content")
	}
}
