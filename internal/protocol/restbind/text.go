package restbind

import (
	"encoding/base64"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// The text forms of scalars are shared by every place a REST protocol
// writes a value as text: labels, query strings, headers and XML bodies.

// TimestampFormat returns m's effective timestampFormat trait (the member's
// own, else its target's), or def when neither sets one.
func TimestampFormat(m *smithy.Member, def smithy.TimestampFormat) smithy.TimestampFormat {
	switch {
	case m == nil:
	case m.TimestampFormat != smithy.TimestampProtocolDefault:
		return m.TimestampFormat
	case m.Target != nil && m.Target.TimestampFormat != smithy.TimestampProtocolDefault:
		return m.Target.TimestampFormat
	}
	return def
}

// dateTimeLayout writes date-time with at most millisecond precision and
// trailing zeros dropped: 2019-12-16T23:48:18Z, 2019-12-16T23:48:18.5Z.
const dateTimeLayout = "2006-01-02T15:04:05.999Z"

// DateTimeMillis is the date-time layout S3 writes, milliseconds always
// present: 2009-10-12T17:50:30.000Z.
const DateTimeMillis = "2006-01-02T15:04:05.000Z"

// FormatTimestamp writes t in format f: date-time (also for
// TimestampProtocolDefault) in UTC with at most millisecond precision,
// http-date as IMF-fixdate, epoch-seconds as decimal seconds with at most
// millisecond precision and no exponent. Finer precision is truncated.
func FormatTimestamp(t time.Time, f smithy.TimestampFormat) string {
	t = t.UTC()
	switch f {
	case smithy.HTTPDate:
		return t.Format(http.TimeFormat)
	case smithy.EpochSeconds:
		return formatEpochSeconds(t)
	case smithy.DateTime, smithy.TimestampProtocolDefault:
	}
	return t.Truncate(time.Millisecond).Format(dateTimeLayout)
}

// formatEpochSeconds writes t as epoch seconds truncated toward zero to
// milliseconds: 1515531081, 1515531081.12, -1.5.
func formatEpochSeconds(t time.Time) string {
	// Unix floors the seconds and leaves a non-negative remainder; move a
	// pre-1970 remainder to the seconds' sign so both truncate toward zero.
	sec, nsec := t.Unix(), int64(t.Nanosecond())
	if sec < 0 && nsec > 0 {
		sec++
		nsec -= int64(time.Second)
	}
	ms := sec*1000 + nsec/int64(time.Millisecond)
	sign := ""
	if ms < 0 {
		sign, ms = "-", -ms
	}
	s := sign + strconv.FormatInt(ms/1000, 10)
	if frac := ms % 1000; frac != 0 {
		s += strings.TrimRight(fmt.Sprintf(".%03d", frac), "0")
	}
	return s
}

// ParseTimestamp reads s in format f (date-time for
// TimestampProtocolDefault) and truncates it to millisecond precision, as
// the Smithy timestamp rules and the SDKs do. http-date accepts the three
// forms RFC 9110 allows; epoch-seconds accepts a decimal number with an
// optional fraction and exponent.
func ParseTimestamp(s string, f smithy.TimestampFormat) (time.Time, error) {
	var (
		t  time.Time
		ok bool
	)
	switch f {
	case smithy.HTTPDate:
		t, ok = parsed(http.ParseTime(s))
	case smithy.EpochSeconds:
		t, ok = parseEpochSeconds(s)
	case smithy.DateTime, smithy.TimestampProtocolDefault:
		t, ok = parsed(time.Parse(time.RFC3339Nano, s))
	}
	if !ok {
		return time.Time{}, &TextError{Value: s, Want: formatName(f)}
	}
	return t.Truncate(time.Millisecond).UTC(), nil
}

// parsed drops a parser's error, which ParseTimestamp replaces with its
// own *TextError.
func parsed(t time.Time, err error) (time.Time, bool) { return t, err == nil }

// maxEpochExponent bounds an epoch-seconds exponent before big.Rat expands
// it: 1e999999 alone costs about 90 ms to reject, and a body can hold
// thousands. No timestamp a client writes comes near the bound.
const maxEpochExponent = 999

// parseEpochSeconds reads s as decimal epoch seconds, truncated to the
// millisecond; ok is false when s is not a decimal number, its exponent
// exceeds maxEpochExponent, or the instant does not fit in int64
// milliseconds.
func parseEpochSeconds(s string) (time.Time, bool) {
	if !isDecimal(s) {
		return time.Time{}, false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(strings.TrimLeft(s[i+1:], "+-"))
		if err != nil || exp > maxEpochExponent {
			return time.Time{}, false
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return time.Time{}, false
	}
	ms := new(big.Int).Quo(new(big.Int).Mul(r.Num(), big.NewInt(1000)), r.Denom())
	if !ms.IsInt64() {
		return time.Time{}, false
	}
	return time.UnixMilli(ms.Int64()), true
}

// isDecimal reports whether s is a JSON-style number: an optional minus,
// digits, an optional fraction and an optional exponent. It keeps big.Rat
// from accepting fractions such as 1/2 and hexadecimal forms.
func isDecimal(s string) bool {
	s = strings.TrimPrefix(s, "-")
	mantissa, exp, hasExp := strings.Cut(strings.ToLower(s), "e")
	whole, frac, hasFrac := strings.Cut(mantissa, ".")
	if !digits(whole) || hasFrac && !digits(frac) {
		return false
	}
	if hasExp {
		exp = strings.TrimLeft(exp, "+-")
		return digits(exp)
	}
	return true
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func formatName(f smithy.TimestampFormat) string {
	switch f {
	case smithy.HTTPDate:
		return "http-date timestamp"
	case smithy.EpochSeconds:
		return "epoch-seconds timestamp"
	case smithy.DateTime, smithy.TimestampProtocolDefault:
	}
	return "date-time timestamp"
}

// TextError is a text value that does not parse as the type its member
// targets.
type TextError struct {
	Value string
	// Want names the expected form, such as "boolean" or "32-bit integer".
	Want string
}

func (e *TextError) Error() string {
	return fmt.Sprintf("%q is not a valid %s", e.Value, e.Want)
}

// ParseBool accepts exactly true and false.
func ParseBool(s string) (bool, error) {
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, &TextError{Value: s, Want: "boolean"}
	}
}

// ParseInt reads a base-10 integer that fits in bits bits.
func ParseInt(s string, bits int) (int64, error) {
	v, err := strconv.ParseInt(s, 10, bits)
	if err != nil {
		return 0, &TextError{Value: s, Want: strconv.Itoa(bits) + "-bit integer"}
	}
	return v, nil
}

// ParseFloat reads a decimal number or one of NaN, Infinity and -Infinity,
// the Smithy text forms of the non-finite values. strconv's other spellings
// (inf, hexadecimal) are refused.
func ParseFloat(s string, bits int) (float64, error) {
	switch s {
	case "NaN":
		return math.NaN(), nil
	case "Infinity":
		return math.Inf(1), nil
	case "-Infinity":
		return math.Inf(-1), nil
	}
	if isDecimal(s) {
		if v, err := strconv.ParseFloat(s, bits); err == nil {
			return v, nil
		}
	}
	return 0, &TextError{Value: s, Want: strconv.Itoa(bits) + "-bit float"}
}

// FormatFloat writes v in its shortest decimal form, or NaN, Infinity or
// -Infinity.
func FormatFloat(v float64, bits int) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(v, 'g', -1, bits)
}

// ParseBigInt reads a base-10 integer of any size.
func ParseBigInt(s string) (*big.Int, error) {
	if v, ok := new(big.Int).SetString(s, 10); ok && digits(strings.TrimPrefix(s, "-")) {
		return v, nil
	}
	return nil, &TextError{Value: s, Want: "integer"}
}

// ParseBase64 reads padded standard base64, the text form of a blob and
// of a mediaType string in a header.
func ParseBase64(s string) ([]byte, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, &TextError{Value: s, Want: "base64 value"}
	}
	return b, nil
}

// ParseBigDecimal checks that s is a decimal number and keeps its text.
func ParseBigDecimal(s string) (smithy.BigDecimal, error) {
	if !isDecimal(s) {
		return "", &TextError{Value: s, Want: "decimal number"}
	}
	return smithy.BigDecimal(s), nil
}
