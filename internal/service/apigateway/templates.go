package apigateway

import (
	"regexp"
	"strconv"
	"strings"
)

// IsLiteralTemplate reports whether a mapping template has none of '$',
// '#' and '\', the only characters Velocity interprets (the Velocity 1.7
// VTL reference), so it renders unchanged (apigateway-rest.md 12.7). It
// is the only template code understudy has: no VTL is rendered.
func IsLiteralTemplate(t string) bool {
	return !strings.ContainsAny(t, `$#\`)
}

// mockStatusPattern finds the statusCode a MOCK request template renders,
// accepting a quoted or unquoted key and a quoted or bare number
// (contract 7.5).
var mockStatusPattern = regexp.MustCompile(`"?statusCode"?\s*:\s*"?(\d+)`)

// MockStatus reads the status a rendered MOCK request template selects,
// leniently: {statusCode: 200} works as {"statusCode": 200} does
// (recorded, apigateway-rest.md 6.3). No match, or a number outside
// 100..599, selects nothing (inferred).
func MockStatus(rendered string) (int, bool) {
	m := mockStatusPattern.FindStringSubmatch(rendered)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 100 || n > 599 {
		return 0, false
	}
	return n, true
}
