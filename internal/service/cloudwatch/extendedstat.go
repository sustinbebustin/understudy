package cloudwatch

import (
	"strconv"
	"strings"
)

// maxBoundDecimals is the most decimal places a statistic's number may
// carry (AWS documents ten for the range bounds; inferred for the rest).
const maxBoundDecimals = 10

// validExtendedStat reports whether s is a statistic of the grammar AWS
// documents beyond the basic five (CloudWatch statistics definitions):
//
//   - IQM;
//   - a percentile or one-number shorthand: p, tm, tc, ts or wm followed
//     by a number from 0 to 100 (p99.9, tm90);
//   - a range: TM, TC, TS or WM with (lower:upper), each bound a
//     percentage up to 100% or a value, either one omittable but not
//     both, and both of one kind when both are given (TM(10%:90%),
//     TC(80:500), TS(80%:));
//   - a percentile rank: PR(lower:upper) with value bounds (PR(:300)).
//
// Numbers are unsigned decimals: the statistics are undefined for negative
// data. The lowercase prefixes take one number, the uppercase ones a range,
// as the documentation says.
func validExtendedStat(s string) bool {
	if s == "IQM" {
		return true
	}
	for _, prefix := range []string{"p", "tm", "tc", "ts", "wm"} {
		if n, ok := strings.CutPrefix(s, prefix); ok {
			v, ok := decimal(n)
			return ok && v <= 100
		}
	}
	for _, prefix := range []string{"TM", "TC", "TS", "WM", "PR"} {
		if body, ok := strings.CutPrefix(s, prefix+"("); ok {
			body, closed := strings.CutSuffix(body, ")")
			lower, upper, colon := strings.Cut(body, ":")
			return closed && colon && validRange(lower, upper, prefix != "PR")
		}
	}
	return false
}

// validRange checks a range's two bounds: at least one given, and of one
// kind when both are.
func validRange(lower, upper string, percentages bool) bool {
	if lower == "" && upper == "" {
		return false
	}
	if lower != "" && upper != "" && strings.HasSuffix(lower, "%") != strings.HasSuffix(upper, "%") {
		return false
	}
	return validBound(lower, percentages) && validBound(upper, percentages)
}

// validBound checks one bound of a range: empty, a value, or, when the
// statistic takes them, a percentage up to 100%.
func validBound(b string, percentages bool) bool {
	if b == "" {
		return true
	}
	n, percent := strings.CutSuffix(b, "%")
	if percent && !percentages {
		return false
	}
	v, ok := decimal(n)
	return ok && (!percent || v <= 100)
}

// decimal parses digits with an optional fraction of at most ten digits.
func decimal(s string) (float64, bool) {
	whole, frac, dot := strings.Cut(s, ".")
	if !digits(whole) || dot && (len(frac) > maxBoundDecimals || !digits(frac)) {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// digits reports whether s is a non-empty run of ASCII digits.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
