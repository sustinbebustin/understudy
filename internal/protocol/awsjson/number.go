package awsjson

import (
	"math/big"
	"strconv"
	"strings"
	"time"
)

// maxIntegerDigits bounds the digits a number may expand to before it is
// refused as out of range, so a member like 1e999999999 costs nothing.
// bigInteger members get the same bound; 4096 digits exceeds any value an
// AWS model describes.
const maxIntegerDigits = 4096

// decimal is a JSON number split into its significant digits and the
// position of the decimal point: the value is 0.<digits> * 10^point, with
// the sign apart. digits has no leading zeros; it is empty for zero.
type decimal struct {
	neg    bool
	digits string
	point  int
}

// parseDecimal splits JSON number text, which the decoder has validated.
// It reports false only for an exponent too large to represent, which no
// sane value has.
func parseDecimal(text string) (decimal, bool) {
	var d decimal
	s := text
	if strings.HasPrefix(s, "-") {
		d.neg = true
		s = s[1:]
	}
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(strings.TrimPrefix(s[i+1:], "+"))
		if err != nil || e > 1<<30 || e < -(1<<30) {
			return decimal{}, false
		}
		exp = e
		s = s[:i]
	}
	intPart, fracPart, _ := strings.Cut(s, ".")
	all := intPart + fracPart
	point := len(intPart) + exp
	trimmed := strings.TrimLeft(all, "0")
	point -= len(all) - len(trimmed)
	d.digits = strings.TrimRight(trimmed, "0")
	d.point = point
	if d.digits == "" {
		d.neg = false
	}
	return d, true
}

// integral reports whether the value has no fractional part.
func (d decimal) integral() bool { return len(d.digits) <= d.point || d.digits == "" }

// floor returns the largest integer not above the value, as Coral converts
// a JSON number for an integer member (DynamoDB answers 1.9 as 1), or false
// when it has more than maxIntegerDigits digits.
func (d decimal) floor() (*big.Int, bool) {
	if d.digits == "" || d.point <= 0 {
		if d.neg && d.digits != "" {
			return big.NewInt(-1), true
		}
		return new(big.Int), true
	}
	if d.point > maxIntegerDigits {
		return nil, false
	}
	whole := d.digits
	if len(whole) > d.point {
		whole = whole[:d.point]
	} else {
		whole += strings.Repeat("0", d.point-len(whole))
	}
	n, ok := new(big.Int).SetString(whole, 10)
	if !ok {
		return nil, false
	}
	if d.neg {
		n.Neg(n)
		if !d.integral() {
			n.Sub(n, big.NewInt(1))
		}
	}
	return n, true
}

// int64In returns the floor of the value when it fits a signed integer of
// the given bit size.
func (d decimal) int64In(bits int) (int64, bool) {
	n, ok := d.floor()
	if !ok || n.BitLen() >= bits && (!d.neg || !isMinSigned(n, bits-1)) {
		return 0, false
	}
	return n.Int64(), true
}

// isMinSigned reports whether n is -2^exp, the minimum of a signed
// exp+1-bit integer: the one negative value whose magnitude needs exp+1
// bits yet fits.
func isMinSigned(n *big.Int, exp int) bool {
	m := new(big.Int).Neg(n)
	return m.BitLen() == exp+1 && m.TrailingZeroBits() == uint(exp)
}

// epochTime converts epoch seconds to a time, truncated toward zero to
// millisecond precision as the Smithy timestamp rules and the SDKs do. It
// reports false outside the range of int64 milliseconds.
func (d decimal) epochTime() (time.Time, bool) {
	ms := decimal{neg: d.neg, digits: d.digits, point: d.point + 3}
	if ms.digits == "" || ms.point <= 0 {
		return time.UnixMilli(0).UTC(), true
	}
	whole := ms.digits
	if len(whole) > ms.point {
		whole = whole[:ms.point]
	} else {
		if ms.point > 19 {
			return time.Time{}, false
		}
		whole += strings.Repeat("0", ms.point-len(whole))
	}
	v, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	if d.neg {
		v = -v
	}
	return time.UnixMilli(v).UTC(), true
}

// appendEpochSeconds writes t as epoch seconds with at most millisecond
// precision and no exponent: 1515531081, 1515531081.12, -1.5. A finer
// instant is truncated toward zero, as epochTime reads one, rather than
// floored as time.Time.UnixMilli does before 1970.
func appendEpochSeconds(b []byte, t time.Time) []byte {
	// Unix splits t into whole seconds floored and a non-negative
	// remainder; move a pre-1970 remainder to the seconds' sign so both
	// parts truncate toward zero.
	sec, nsec := t.Unix(), int64(t.Nanosecond())
	if sec < 0 && nsec > 0 {
		sec++
		nsec -= int64(time.Second)
	}
	ms := nsec / int64(time.Millisecond) // in (-1000, 1000), sec's sign
	if sec == 0 && ms < 0 {
		b = append(b, '-') // -0.5: AppendInt cannot sign a zero
	}
	b = strconv.AppendInt(b, sec, 10)
	if ms != 0 {
		digits := strconv.AppendInt([]byte{'.'}, 1000+max(ms, -ms), 10) // ".1xyz"
		digits = append(digits[:1], digits[2:]...)
		b = append(b, strings.TrimRight(string(digits), "0")...)
	}
	return b
}
