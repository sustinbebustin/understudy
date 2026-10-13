package codegen

import (
	"go/token"
	"strings"
	"unicode"
	"unicode/utf8"
)

// exportName makes a Smithy identifier an exported Go identifier.
func exportName(s string) string {
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	out := string(r)
	if !token.IsIdentifier(out) {
		out = strings.Map(func(c rune) rune {
			if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' {
				return c
			}
			return '_'
		}, out)
	}
	return out
}

// lowerCamel is the member name with its first letter lower-cased, the
// path segment AWS prints (error-matrix.md 4).
func lowerCamel(s string) string {
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// enumConstName turns an enum member name into a Go constant suffix:
// SENDER_ID becomes SenderId, AWSTraceHeader stays.
func enumConstName(s string) string {
	if !strings.Contains(s, "_") && strings.ToUpper(s) != s {
		return exportName(s)
	}
	var b strings.Builder
	for part := range strings.SplitSeq(s, "_") {
		if part == "" {
			continue
		}
		r := []rune(strings.ToLower(part))
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	out := b.String()
	if first, _ := utf8.DecodeRuneInString(out); out == "" || !unicode.IsLetter(first) {
		out = "Value" + out
	}
	return exportName(out)
}

// namespacePrefix turns a namespace into a Go name prefix, leaving out the
// segments every AWS namespace shares: com.amazon.coral.validate is
// CoralValidate.
func namespacePrefix(ns string) string {
	var b strings.Builder
	for part := range strings.SplitSeq(ns, ".") {
		switch part {
		case "com", "amazon", "amazonaws", "aws":
			continue
		}
		b.WriteString(exportName(part))
	}
	return b.String()
}
