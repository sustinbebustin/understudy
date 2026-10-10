package ruler

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// This file holds what the lexer needs to word its errors as Jackson
// 2.22.2 does. Jackson reads UTF-16 code units, so a character above U+FFFF
// reaches its checks as a high surrogate.

const validValues = "(JSON String, Number, Array, Object or token 'null', 'true' or 'false')"

// maxErrorTokenLength is the default of Jackson's
// ErrorReportConfiguration.getMaxErrorTokenLength: an unrecognized token is
// quoted up to this many characters, then "...".
const maxErrorTokenLength = 256

// maxSourceSnippet is the default of
// ErrorReportConfiguration.getMaxRawContentLength: the most UTF-16 code units
// of the source a location quotes.
const maxSourceSnippet = 500

// maxCharArraySource is the longest String Jackson's JsonFactory parses from
// a copy in a char array; a longer one is read through a StringReader, which
// a location names without quoting it.
const maxCharArraySource = 0x8000

// javaChar decodes the character at the start of b as one Java char: a
// character above U+FFFF is its high surrogate. size is the bytes of the
// whole UTF-8 sequence.
func javaChar(b []byte) (r rune, size int) {
	r, size = utf8.DecodeRune(b)
	if hi, _ := utf16.EncodeRune(r); hi != utf8.RuneError {
		r = hi
	}
	return r, size
}

// isJavaIdentifierStart is Java's Character.isJavaIdentifierStart for a
// char: a letter, a letter number, a currency symbol or a connector
// punctuation such as "_". It decides whether Jackson reports an unexpected
// value as a token or as a character.
func isJavaIdentifierStart(r rune) bool {
	return unicode.IsLetter(r) || unicode.In(r, unicode.Nl, unicode.Sc, unicode.Pc)
}

// isJavaIdentifierPart is Java's Character.isJavaIdentifierPart for a char:
// an identifier start, a digit, a combining or non-spacing mark, or a
// character Character.isIdentifierIgnorable accepts (most ASCII and C1
// controls, and format characters). It bounds the token Jackson quotes.
func isJavaIdentifierPart(r rune) bool {
	switch {
	case r <= 0x08, r >= 0x0E && r <= 0x1B, r >= 0x7F && r <= 0x9F:
		return true
	}
	return isJavaIdentifierStart(r) || unicode.In(r, unicode.Nd, unicode.Mn, unicode.Mc, unicode.Cf)
}

// charDesc describes a character as Jackson's _getCharDesc does, with a
// character above U+FFFF reported as its high surrogate, which encodes to
// "?" in a UTF-8 message.
func charDesc(r rune) string {
	if r < 0x20 || (r >= 0x7F && r <= 0x9F) {
		return "(CTRL-CHAR, code " + strconv.Itoa(int(r)) + ")"
	}
	text := string(r)
	if hi, _ := utf16.EncodeRune(r); hi != utf8.RuneError {
		r, text = hi, "?"
	}
	if r > 0xFF {
		return "'" + text + "' (code " + strconv.Itoa(int(r)) + " / 0x" + strconv.FormatInt(int64(r), 16) + ")"
	}
	return "'" + text + "' (code " + strconv.Itoa(int(r)) + ")"
}

// location renders p as Jackson's JsonLocation.toString does for a String
// source, as Ruler parses one: the source, quoted up to maxSourceSnippet code
// units with its controls other than CR and LF escaped, and the 1-based line
// and column, counted in UTF-16 code units.
func (l *lexer) location(p sourcePos) string {
	var b strings.Builder
	b.WriteString("[Source: ")
	if units := utf16Len(l.src); units > maxCharArraySource {
		b.WriteString("(StringReader)")
	} else {
		b.WriteString(`(String)"`)
		writeSnippet(&b, l.src)
		b.WriteByte('"')
		if units > maxSourceSnippet {
			b.WriteString("[truncated " + strconv.Itoa(units-maxSourceSnippet) + " chars]")
		}
	}
	b.WriteString("; line: " + strconv.Itoa(p.line) +
		", column: " + strconv.Itoa(utf16Len(l.src[p.lineStart:p.offset])+1) + "]")
	return b.String()
}

// writeSnippet writes the first maxSourceSnippet code units of src. A pair
// the cut splits leaves its high surrogate, which encodes to "?".
func writeSnippet(b *strings.Builder, src []byte) {
	units := 0
	for len(src) > 0 && units < maxSourceSnippet {
		r, size := utf8.DecodeRune(src)
		src = src[size:]
		switch n := utf16.RuneLen(r); {
		case n == 2 && units+n > maxSourceSnippet:
			b.WriteByte('?')
		case r != '\r' && r != '\n' && (r < 0x20 || (r >= 0x7F && r <= 0x9F)):
			b.WriteString(`\u`)
			hex := strings.ToUpper(strconv.FormatInt(int64(r), 16))
			b.WriteString(strings.Repeat("0", 4-len(hex)) + hex)
		default:
			b.WriteRune(r)
		}
		units += max(utf16.RuneLen(r), 1)
	}
}

// utf16Len is the length of b in UTF-16 code units, with each invalid UTF-8
// byte one unit.
func utf16Len(b []byte) int {
	n := 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		n += max(utf16.RuneLen(r), 1)
	}
	return n
}
