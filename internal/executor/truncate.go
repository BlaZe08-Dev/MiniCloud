package executor

import (
	"strings"
	"unicode/utf8"
)

// ClampText returns s as the coordinator will store it, within limit bytes. Every invalid UTF-8
// byte becomes U+FFFD (3 bytes), exactly as JSON encoding does on the wire, so a cut or invalid
// input can never grow past limit in transit. A text over limit is cut at a rune boundary and
// ends with TruncationMarker; a text within limit is returned unchanged apart from that
// replacement.
func ClampText(s string, limit int) string {
	if !utf8.ValidString(s) {
		var b strings.Builder
		for i := 0; i < len(s); {
			r, w := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && w == 1 {
				b.WriteRune(utf8.RuneError)
			} else {
				b.WriteString(s[i : i+w])
			}
			i += w
		}
		s = b.String()
	}
	if len(s) <= limit {
		return s
	}
	cut := limit - len(TruncationMarker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + TruncationMarker
}
