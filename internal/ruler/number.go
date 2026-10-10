package ruler

import (
	"math"
	"math/big"
	"strconv"
	"strings"
)

// comparableNumber converts JSON number text to the float64 Ruler compares, or
// reports that Ruler cannot compare it. Ruler (ComparableNumber.generate)
// accepts a number only when its decimal value equals Java's Double.toString
// of the nearest binary64 (javaDecimal), so 0.1 and 1e20 are comparable while
// 9007199254740993 (2^53+1) and 1e309 are not.
func comparableNumber(text string) (float64, bool) {
	if !isJSONNumber(text) {
		return 0, false
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(f, 0) {
		return 0, false
	}
	if f == 0 {
		// Underflow parses to zero; only a literal zero is comparable.
		return 0, isZeroText(text)
	}
	if isShortInteger(text) {
		return f, true
	}
	exact, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0, false
	}
	java, ok := new(big.Rat).SetString(javaDecimal(f))
	if !ok || exact.Cmp(java) != 0 {
		return 0, false
	}
	return f, true
}

// javaDecimal is the decimal Java's Double.toString (JDK 19 and later)
// writes for f: the shortest decimal that rounds to f, except that when the
// shortest has one significant digit, the decimal of one or two digits
// closest to f. The exception matters only for the smallest subnormals:
// Double.MIN_VALUE is 4.9E-324, not 5E-324.
func javaDecimal(f float64) string {
	shortest := strconv.FormatFloat(f, 'e', -1, 64)
	if mantissa, _, _ := strings.Cut(strings.TrimPrefix(shortest, "-"), "e"); len(mantissa) > 1 {
		return shortest
	}
	two := strconv.FormatFloat(f, 'e', 1, 64)
	if back, err := strconv.ParseFloat(two, 64); err == nil && back == f {
		return two
	}
	return shortest
}

// isShortInteger reports whether a JSON number literal is an integer of at
// most 15 digits. binary64 holds every such integer exactly, so its value is
// the shortest decimal form of its float64 and no exact comparison is needed.
func isShortInteger(text string) bool {
	digits := strings.TrimPrefix(text, "-")
	return len(digits) <= 15 && strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// isZeroText reports whether a JSON number literal has no non-zero mantissa
// digit.
func isZeroText(text string) bool {
	for i := range len(text) {
		switch c := text[i]; {
		case c == 'e' || c == 'E':
			return true
		case c >= '1' && c <= '9':
			return false
		}
	}
	return true
}

// cannotCompare is Ruler's text for a rule number it cannot represent.
func cannotCompare(text string) string {
	return "Cannot compare number : " + text
}
