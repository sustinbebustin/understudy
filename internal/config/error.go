package config

import (
	"strings"
)

// Error is a setting Load refuses. Where names the setting as its source
// spells it ("--listen", "UNDERSTUDY_LISTEN", "understudy.json: s3.crossRegion");
// Closest is the valid name nearest an unknown one; Hint is the next thing
// to try. Secrets never appear in an Error.
type Error struct {
	Where   string
	Reason  string
	Closest string
	Hint    string
}

// Error renders the refusal as one line: "config: ", Where, the reason,
// then the closest name and the hint when set.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("config: ")
	b.WriteString(e.Where)
	b.WriteString(": ")
	b.WriteString(e.Reason)
	if e.Closest != "" {
		b.WriteString("; the closest valid name is ")
		b.WriteString(e.Closest)
	}
	if e.Hint != "" {
		b.WriteString("; ")
		b.WriteString(e.Hint)
	}
	return b.String()
}

// closest returns the candidate with the smallest edit distance to name,
// ignoring case, the first in candidates on a tie.
func closest(name string, candidates []string) string {
	best, bestDist := "", -1
	for _, c := range candidates {
		d := distance(strings.ToLower(name), strings.ToLower(c))
		if bestDist < 0 || d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// distance is the Levenshtein distance between a and b in bytes.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
