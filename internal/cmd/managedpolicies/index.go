package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// index is llms.txt (iam-evaluation.md 10.2): the policies it lists, in file
// order, and the link to the single-file guide.
type index struct {
	policies []indexEntry
	full     string
}

// indexEntry is one listed policy: its name and the page llms.txt links.
type indexEntry struct{ name, page string }

// The ParseError sources and the fields of llms.txt.
const (
	indexSource     = "llms.txt"
	fullSource      = "full.md"
	fieldPolicyLine = "policy line"
	fieldFullLink   = "full link"
)

// The headings of llms.txt the parser reads under (iam-evaluation.md 10.2).
const (
	policiesHeading = "## [AWS managed policies]("
	fullHeading     = "## Full content"
)

var (
	// policyLineRE is a policy's list item: its name, its page and the
	// title naming it again (iam-evaluation.md 10.2).
	policyLineRE = regexp.MustCompile(`^- \[([^]]+)\]\((https://docs\.aws\.amazon\.com/aws-managed-policy/latest/reference/[^)]+\.md)\): About the AWS managed policy: (.+)$`)
	// linkItemRE is any list item that opens with a link.
	linkItemRE = regexp.MustCompile(`^- \[[^]]*\]\(([^)]*)\)`)
)

// lines splits a guide file on "\n", removing one trailing "\r" from each
// line.
func lines(body []byte) []string {
	ls := strings.Split(string(body), "\n")
	for i, l := range ls {
		ls[i] = strings.TrimSuffix(l, "\r")
	}
	return ls
}

// trimEnd removes trailing spaces and tabs, which the guide leaves on some
// lines (iam-evaluation.md 10.3).
func trimEnd(line string) string { return strings.TrimRight(line, " \t") }

// parseIndex reads llms.txt: every list item under the "AWS managed
// policies" heading is a policy line, and the list item under "Full
// content" linking a /full.md under the guide is the single-file guide.
// Every problem is a *ParseError, all of them joined.
func parseIndex(body []byte) (index, error) {
	r := indexReader{listed: map[string]int{}}
	section := ""
	for i, line := range lines(body) {
		line, n := trimEnd(line), i+1
		switch {
		case strings.HasPrefix(line, "## "):
			section = line
		case !strings.HasPrefix(line, "- "):
		case strings.HasPrefix(section, policiesHeading):
			r.policyItem(line, n)
		case section == fullHeading && r.fullAt == 0:
			r.fullItem(line, n)
		}
	}
	if r.items == 0 {
		r.bad(fieldPolicyLine, 0, "no list item under a %q...) heading; the index lists no policy", policiesHeading)
	}
	if r.fullAt == 0 {
		r.bad(fieldFullLink, 0, "no list item under %q links the single-file guide, a URL ending in /full.md", fullHeading)
	}
	if len(r.errs) > 0 {
		return index{}, errors.Join(r.errs...)
	}
	return r.idx, nil
}

// indexReader is parseIndex's state: what it read so far and every
// problem found.
type indexReader struct {
	idx  index
	errs []error
	// listed is the line each policy name was first listed on.
	listed map[string]int
	// items counts the list items under the policies heading, valid or not.
	items int
	// fullAt is the line of the single-file guide's link, 0 before it.
	fullAt int
}

func (r *indexReader) bad(field string, line int, format string, args ...any) {
	r.errs = append(r.errs, &ParseError{Source: indexSource, Field: field, Line: line, Problem: fmt.Sprintf(format, args...)})
}

// policyItem reads list item line n under the policies heading.
func (r *indexReader) policyItem(line string, n int) {
	r.items++
	e, problem := policyLine(line)
	if problem != "" {
		r.bad(fieldPolicyLine, n, "%s", problem)
		return
	}
	if first, dup := r.listed[e.name]; dup {
		r.bad(fieldPolicyLine, n, "lists %s again; line %d lists it first", e.name, first)
		return
	}
	r.listed[e.name] = n
	r.idx.policies = append(r.idx.policies, e)
}

// fullItem reads list item line n under the full content heading: the
// first one linking a /full.md is the single-file guide's, which must be
// under the guide.
func (r *indexReader) fullItem(line string, n int) {
	m := linkItemRE.FindStringSubmatch(line)
	if len(m) < 2 || !strings.HasSuffix(m[1], "/full.md") {
		return
	}
	r.fullAt = n
	if !strings.HasPrefix(m[1], guidePrefix) {
		r.bad(fieldFullLink, n, "the single-file guide is linked as %s, which is not under %s", m[1], guidePrefix)
		return
	}
	r.idx.full = m[1]
}

// policyLine reads one policy line, or says why it is not one: the name,
// the page's file name and the title must all be the policy's name.
func policyLine(line string) (e indexEntry, problem string) {
	m := policyLineRE.FindStringSubmatch(line)
	if m == nil {
		return indexEntry{}, fmt.Sprintf("%q is not of the form \"- [<Name>](%s<Name>.md): About the AWS managed policy: <Name>\"", line, guidePrefix)
	}
	name, page, title := m[1], m[2], m[3]
	if title != name {
		return indexEntry{}, fmt.Sprintf("lists %s but its title names %s", name, title)
	}
	if want := guidePrefix + name + ".md"; page != want {
		return indexEntry{}, fmt.Sprintf("lists %s but links %s, not %s", name, page, want)
	}
	return indexEntry{name: name, page: page}, ""
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
