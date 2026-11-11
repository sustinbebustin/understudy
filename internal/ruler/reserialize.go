package ruler

import (
	"bytes"
	"math"
	"strconv"
	"strings"
)

// reserialize writes src, well-formed JSON, as SNS hands a filter policy to
// Ruler: parsed into a Jackson tree and written back compact. A string is
// escaped as Jackson's generator escapes it; an integer keeps its digits
// ("-0" becomes "0"); any other number becomes a double, written as Java's
// Double.toString writes it, and one beyond binary64 becomes the quoted
// string "Infinity" or "-Infinity". sns/R09/H5-08-attr-subscribe.json
// records the last: {"n":[{"numeric":["=",1e309]}]} is refused for a
// non-numeric operand, with a location quoting
// {"n":[{"numeric":["=","Infinity"]}]}. A duplicate key is kept as written:
// whether SNS keeps only the last is not recorded.
func reserialize(src []byte) []byte {
	lx := newLexer(src)
	var b bytes.Buffer
	// first holds, per open container, whether its next entry is its first.
	var first []bool
	afterName := false
	for {
		t, err := lx.next()
		if err != nil {
			return src
		}
		switch t.kind {
		case tokEOF:
			return b.Bytes()
		case tokEndObject, tokEndArray:
			b.WriteString(t.display())
			first = first[:len(first)-1]
			continue
		case tokStartObject, tokStartArray, tokFieldName, tokString, tokNumber, tokTrue, tokFalse, tokNull:
		}
		if n := len(first); n > 0 && !afterName {
			if !first[n-1] {
				b.WriteByte(',')
			}
			first[n-1] = false
		}
		afterName = false
		switch t.kind {
		case tokFieldName:
			writeJacksonString(&b, t.text)
			b.WriteByte(':')
			afterName = true
		case tokString:
			writeJacksonString(&b, t.text)
		case tokNumber:
			b.WriteString(jacksonNumber(t.text))
		case tokStartObject, tokStartArray:
			b.WriteString(t.display())
			first = append(first, true)
		case tokTrue, tokFalse, tokNull, tokEOF, tokEndObject, tokEndArray:
			b.WriteString(t.text)
		}
	}
}

// writeJacksonString writes s quoted as Jackson's JsonGenerator does by
// default: '"' and '\' and the controls below U+0020 escaped, \b, \t, \n,
// \f and \r in their short forms and the rest as \u00XX; everything else,
// non-ASCII included, as is.
func writeJacksonString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := range len(s) {
		switch c := s[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte("0123456789ABCDEF"[c>>4])
				b.WriteByte("0123456789ABCDEF"[c&0xF])
				continue
			}
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

// jacksonNumber is a JSON number as Jackson's tree writes it back: an
// integer node keeps its digits, a double node is Java's Double.toString,
// and a double beyond binary64 is the quoted non-numeric token.
func jacksonNumber(text string) string {
	if !strings.ContainsAny(text, ".eE") {
		if text == "-0" {
			return "0"
		}
		return text
	}
	f, err := strconv.ParseFloat(text, 64)
	switch {
	case math.IsInf(f, 1):
		return `"Infinity"`
	case math.IsInf(f, -1):
		return `"-Infinity"`
	case err != nil:
		return text
	}
	return javaDoubleString(f)
}

// javaDoubleString is Java's Double.toString of a finite f: the digits of
// javaDecimal, written as a plain decimal with at least one fraction digit
// when 1e-3 <= |f| < 1e7, and otherwise as d.dddE<n>.
func javaDoubleString(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	sign := ""
	if f < 0 {
		sign = "-"
	}
	mantissa, exp, _ := strings.Cut(strings.TrimPrefix(javaDecimal(f), "-"), "e")
	// javaDecimal writes some one-digit values with two ("1.0e-01").
	digits := strings.TrimRight(strings.Replace(mantissa, ".", "", 1), "0")
	if digits == "" {
		digits = "0"
	}
	n, err := strconv.Atoi(exp)
	if err != nil {
		// Unreachable: javaDecimal writes FormatFloat's 'e' form.
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	if a := math.Abs(f); a < 1e-3 || a >= 1e7 {
		fraction := digits[1:]
		if fraction == "" {
			fraction = "0"
		}
		return sign + digits[:1] + "." + fraction + "E" + strconv.Itoa(n)
	}
	if n < 0 {
		return sign + "0." + strings.Repeat("0", -n-1) + digits
	}
	if len(digits) <= n+1 {
		return sign + digits + strings.Repeat("0", n+1-len(digits)) + ".0"
	}
	return sign + digits[:n+1] + "." + digits[n+1:]
}
