package xmlw

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// replacement is written for each byte of s that is not valid UTF-8.
const replacement = string(utf8.RuneError)

// escapeInto writes s to w with the escape table described in the package
// documentation, for character data and attribute values alike.
func escapeInto(w *Writer, s string) {
	last := 0
	for i := 0; i < len(s); {
		r, width := utf8.DecodeRuneInString(s[i:])
		if esc, ok := escapeRune(r, width); ok {
			w.write(s[last:i])
			w.write(esc)
			last = i + width
		}
		i += width
	}
	w.write(s[last:])
}

// escapeRune returns the replacement text for r, which was decoded from width
// bytes, and false when r is written as is.
func escapeRune(r rune, width int) (string, bool) {
	switch r {
	case '&':
		return "&amp;", true
	case '<':
		return "&lt;", true
	case '>':
		return "&gt;", true
	case '"':
		return "&#34;", true
	case '\'':
		return "&#39;", true
	case '\U00002028': // LINE SEPARATOR
		return numericReference(r), true
	case utf8.RuneError:
		return replacement, width == 1
	}
	if unicode.IsControl(r) || !isXMLChar(r) {
		return numericReference(r), true
	}
	return "", false
}

func numericReference(r rune) string {
	return "&#x" + strings.ToUpper(strconv.FormatInt(int64(r), 16)) + ";"
}

// isXMLChar reports whether r is in the XML 1.0 Char production.
func isXMLChar(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' ||
		r >= 0x20 && r <= 0xD7FF ||
		r >= 0xE000 && r <= 0xFFFD ||
		r >= 0x10000 && r <= utf8.MaxRune
}
