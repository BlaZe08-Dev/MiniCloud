package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const hexChars = "0123456789abcdef"

// sanitize neutralises control characters and escape sequences in worker- or
// task-supplied text so it cannot manipulate the operator's terminal.
// It keeps printable runes, newline (\n), and tab (\t).
// Every other control character (C0 controls, DEL, C1 range U+0080..U+009F),
// unprintable formatting characters (bidi overrides, zero-width spaces, NBSP),
// and invalid UTF-8 byte sequences are replaced with visible placeholders
// (e.g. \x1b, \x7f, \u0080, \u202e, \xff).
// Lone carriage returns (\r not followed by \n) are replaced with the visible
// escape `\r` so they cannot overwrite lines on the terminal, while standard
// Windows CRLF (\r\n) line endings are normalized to standard newline (\n).
func sanitize(s string) string {
	if s == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			if i+size < len(s) && s[i+size] == '\n' {
				// CRLF: skip \r so the \n in next iteration becomes LF.
				i += size
				continue
			}
			b.WriteString(`\r`)
		case r < 0x20:
			// C0 control character.
			b.WriteString(`\x`)
			b.WriteByte(hexChars[r>>4])
			b.WriteByte(hexChars[r&0x0f])
		case r == 0x7f:
			// DEL.
			b.WriteString(`\x7f`)
		case r >= 0x80 && r <= 0x9f:
			// C1 control character range (U+0080..U+009F).
			b.WriteString(`\u00`)
			b.WriteByte(hexChars[r>>4])
			b.WriteByte(hexChars[r&0x0f])
		case r == utf8.RuneError && size == 1:
			// Invalid UTF-8 byte: escape as hex \xHH.
			b.WriteString(`\x`)
			b.WriteByte(hexChars[s[i]>>4])
			b.WriteByte(hexChars[s[i]&0x0f])
		case unicode.IsPrint(r):
			b.WriteRune(r)
		default:
			// Any other unprintable rune (e.g. format controls).
			if r <= 0xffff {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		}
		i += size
	}
	return b.String()
}

// sanitizeJSON rewrites only C1 control characters (U+0080..U+009F) and DEL (0x7f)
// to JSON-valid \u00xx escapes, leaving all other bytes untouched. This preserves
// valid JSON syntax and decoder equality while neutralizing terminal control codes.
func sanitizeJSON(s string) string {
	if s == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		if s[i] == 0x7f {
			b.WriteString(`\u007f`)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x80 && r <= 0x9f {
			b.WriteString(`\u00`)
			b.WriteByte(hexChars[r>>4])
			b.WriteByte(hexChars[r&0x0f])
			i += size
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}
