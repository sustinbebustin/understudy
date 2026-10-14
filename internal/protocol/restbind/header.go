package restbind

import (
	"strings"
)

// splitHeaderList splits one header line holding a list (wire-rest.md 1.4):
// elements separated by commas with optional whitespace, an element in
// double quotes may hold commas and backslash-escaped quotes. Both the
// spec tests' ", " and boto3's bare "," separators parse. An empty line is
// an empty list.
func splitHeaderList(line string) []string {
	if strings.TrimSpace(line) == "" {
		return nil
	}
	var out []string
	for i := 0; ; i++ { // i++ steps over the comma
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		var elem string
		if i < len(line) && line[i] == '"' {
			elem, i = quoted(line, i+1)
			for i < len(line) && line[i] != ',' {
				i++
			}
		} else {
			end := strings.IndexByte(line[i:], ',')
			if end < 0 {
				end = len(line) - i
			}
			elem = strings.TrimRight(line[i:i+end], " \t")
			i += end
		}
		out = append(out, elem)
		if i >= len(line) {
			return out
		}
	}
}

// quoted reads a quoted element starting after its opening quote and
// returns it unescaped with the index after the closing quote. An
// unterminated element runs to the end of the line.
func quoted(line string, i int) (elem string, next int) {
	var b strings.Builder
	for i < len(line) {
		switch c := line[i]; c {
		case '\\':
			if i+1 < len(line) {
				b.WriteByte(line[i+1])
				i += 2
				continue
			}
			i++
		case '"':
			return b.String(), i + 1
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), i
}

// joinHeaderList writes a list header line: elements joined by ", ", a
// string element quoted (with its quotes and backslashes escaped) when it
// is empty, holds a comma or a double quote, or has leading or trailing
// space, so splitHeaderList reads it back. Pass quote false for elements
// that must stay bare, such as http-date timestamps.
func joinHeaderList(elems []string, quote bool) string {
	parts := make([]string, len(elems))
	for i, e := range elems {
		if quote && needsQuotes(e) {
			e = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(e) + `"`
		}
		parts[i] = e
	}
	return strings.Join(parts, ", ")
}

func needsQuotes(s string) bool {
	return s == "" || strings.ContainsAny(s, `,"`) || strings.TrimSpace(s) != s
}

// pairHTTPDates rejoins an http-date list that splitHeaderList cut at every
// comma: each IMF-fixdate holds one comma ("Mon, 16 Dec 2019 23:48:18
// GMT"), so elements pair up.
func pairHTTPDates(elems []string) []string {
	out := make([]string, 0, (len(elems)+1)/2)
	for i := 0; i < len(elems); i += 2 {
		if i+1 < len(elems) {
			out = append(out, elems[i]+", "+elems[i+1])
		} else {
			out = append(out, elems[i])
		}
	}
	return out
}
