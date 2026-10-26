package main

import "fmt"

// index is llms.txt (iam-evaluation.md 10.2): the policies it lists, in file
// order, and the link to the single-file guide.
type index struct {
	policies []indexEntry
	full     string
}

// indexEntry is one listed policy: its name and the page llms.txt links.
type indexEntry struct{ name, page string }

// parseIndex reads llms.txt. Not implemented yet: it refuses.
func parseIndex([]byte) (index, error) {
	return index{}, &notImplementedError{op: "parsing llms.txt"}
}

// ParseError is a line of llms.txt or of the single-file guide the parser
// cannot read, or a policy field it cannot find.
type ParseError struct {
	// Source is the file the problem is in, by the last element of its
	// URL: the index or the single-file guide.
	Source string
	// Policy is the policy's name, "" when the problem is not one policy's.
	Policy string
	Field  string
	// Line is 1-based in Source, 0 when the thing is missing.
	Line    int
	Problem string
}

func (e *ParseError) Error() string {
	where := e.Source
	if e.Line > 0 {
		where = fmt.Sprintf("%s:%d", e.Source, e.Line)
	}
	if e.Policy != "" {
		return fmt.Sprintf("%s: policy %s: %s: %s", where, e.Policy, e.Field, e.Problem)
	}
	return fmt.Sprintf("%s: %s: %s", where, e.Field, e.Problem)
}
