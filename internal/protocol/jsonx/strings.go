package jsonx

import (
	"unicode/utf8"
)

const hexDigits = "0123456789abcdef"

// unquote decodes a JSON string literal that jsontext has already checked
// for syntax. Bytes that are not escapes are copied as they are, invalid
// UTF-8 included. A \u escape of a surrogate that is not half of a pair is
// written as the surrogate's three-byte generalized UTF-8 form (ED A0..BF
// 80..BF), which Go and every UTF-8 validator reject, so the string carries
// the defect for validators to classify.
func unquote(raw []byte) string {
	body := raw[1 : len(raw)-1]
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		switch body[i] {
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r := hex4(body[i+1 : i+5])
			i += 4
			if isHighSurrogate(r) && i+6 < len(body) && body[i+1] == '\\' && body[i+2] == 'u' {
				if lo := hex4(body[i+3 : i+7]); isLowSurrogate(lo) {
					out = utf8.AppendRune(out, 0x10000+(r-0xD800)<<10+(lo-0xDC00))
					i += 6
					continue
				}
			}
			out = appendCodePoint(out, r)
		default: // '"', '\\' and '/' stand for themselves
			out = append(out, body[i])
		}
	}
	return string(out)
}

// appendCodePoint appends r as UTF-8, encoding surrogates the way UTF-8
// would if it allowed them (WTF-8), which utf8.AppendRune refuses to do.
func appendCodePoint(b []byte, r rune) []byte {
	if !isHighSurrogate(r) && !isLowSurrogate(r) {
		return utf8.AppendRune(b, r)
	}
	return append(b, 0xE0|byte(r>>12&0x0F), 0x80|byte(r>>6&0x3F), 0x80|byte(r&0x3F))
}

func hex4(b []byte) rune {
	var r rune
	for _, c := range b {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			r |= rune(c - 'a' + 10)
		default: // 'A' to 'F'; jsontext rejected anything else
			r |= rune(c - 'A' + 10)
		}
	}
	return r
}

func isHighSurrogate(r rune) bool { return r >= 0xD800 && r <= 0xDBFF }

func isLowSurrogate(r rune) bool { return r >= 0xDC00 && r <= 0xDFFF }

// AppendString appends s as a JSON string literal, for a codec that writes
// a small fixed document without an Encoder. It escapes the quote, the
// backslash and the control characters (with the short forms where JSON
// has them), and writes a generalized UTF-8 surrogate as its \u escape, so
// a lone surrogate ReadString produced reads back as the same bytes. A high
// surrogate directly followed by a low one is the exception: their escapes
// would read back joined into one valid character, so the high one is
// written as � and the low one stays lone, and the string still fails
// utf8.ValidString when read back. Every other byte that is not valid UTF-8
// becomes �. Everything else is written raw.
func AppendString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			b = appendASCII(b, c)
			i++
			continue
		}
		if r, size := utf8.DecodeRuneInString(s[i:]); r != utf8.RuneError || size != 1 {
			b = append(b, s[i:i+size]...)
			i += size
			continue
		}
		sur, ok := encodedSurrogate(s[i:])
		if !ok {
			b = append(b, `�`...)
			i++
			continue
		}
		i += 3
		if next, ok := encodedSurrogate(s[i:]); ok && isHighSurrogate(sur) && isLowSurrogate(next) {
			b = append(b, `�`...)
		} else {
			b = appendEscape(b, sur)
		}
	}
	return append(b, '"')
}

func appendASCII(b []byte, c byte) []byte {
	switch c {
	case '"', '\\':
		return append(b, '\\', c)
	case '\b':
		return append(b, '\\', 'b')
	case '\f':
		return append(b, '\\', 'f')
	case '\n':
		return append(b, '\\', 'n')
	case '\r':
		return append(b, '\\', 'r')
	case '\t':
		return append(b, '\\', 't')
	}
	if c < 0x20 {
		return appendEscape(b, rune(c))
	}
	return append(b, c)
}

func appendEscape(b []byte, r rune) []byte {
	return append(b, '\\', 'u', hexDigits[r>>12&0xF], hexDigits[r>>8&0xF], hexDigits[r>>4&0xF], hexDigits[r&0xF])
}

// encodedSurrogate returns the surrogate whose three-byte generalized UTF-8
// form starts s, and reports whether s starts with one.
func encodedSurrogate(s string) (rune, bool) {
	if len(s) < 3 || s[0] != 0xED || s[1] < 0xA0 || s[1] > 0xBF || s[2] < 0x80 || s[2] > 0xBF {
		return 0, false
	}
	return 0xD000 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F), true
}
