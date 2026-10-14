package logsfilter

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// allTerms is an unstructured pattern (rule N3) with a plain or - term:
// every required term must occur and no excluded term may. Any ? terms the
// pattern had are dropped, as AWS ignores them then.
type allTerms struct {
	required, excluded []term
}

// anyTerm is an unstructured pattern made only of ? terms: at least one must
// occur.
type anyTerm struct {
	optional []term
}

// term is one unstructured term.
//
//sumtype:decl
type term interface{ isTerm() }

// literalTerm is a bare term or a quoted phrase.
type literalTerm struct{ text string }

// regexTerm is a %regex% term.
type regexTerm struct{ re *regexp.Regexp }

func (literalTerm) isTerm() {}
func (regexTerm) isTerm()   {}

// parseText reads one or more unstructured terms; the caller has checked
// that a non-blank character remains.
func parseText(s *scanner) (matcher, *Error) {
	var required, excluded, optional []term
	for s.skipSpace(); !s.eof(); s.skipSpace() {
		list := &required
		switch s.peek() {
		case '-':
			list = &excluded
			s.pos++
		case '?':
			list = &optional
			s.pos++
		}
		t, err := s.term()
		if err != nil {
			return nil, err
		}
		if !s.eof() && !isSpace(s.peek()) {
			return nil, s.fail("terms must be separated by whitespace")
		}
		*list = append(*list, t)
	}
	if len(required)+len(excluded) > 0 {
		return allTerms{required: required, excluded: excluded}, nil
	}
	return anyTerm{optional: optional}, nil
}

func (s *scanner) term() (term, *Error) {
	switch s.peek() {
	case '"':
		phrase, err := s.quoted()
		if err != nil {
			return nil, err
		}
		return literalTerm{text: phrase}, nil
	case '%':
		re, err := s.regex()
		if err != nil {
			return nil, err
		}
		return regexTerm{re: re}, nil
	}
	start := s.pos
	for !s.eof() && !isSpace(s.peek()) {
		s.pos++
	}
	word := s.src[start:s.pos]
	if word == "" {
		return nil, s.fail("term expected after - or ?")
	}
	if offending := offendingTermChars(word); offending != "" {
		return nil, invalidTerm(offending)
	}
	return literalTerm{text: word}, nil
}

// offendingTermChars returns the characters of word outside the bare-term
// set, each once in order of first appearance, or "" when there are none.
// It walks runes so a multi-byte character is reported whole.
func offendingTermChars(word string) string {
	var out []rune
	for i, r := range word {
		if r < utf8.RuneSelf && isBareTermChar(word[i]) {
			continue
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return string(out)
}

func (p allTerms) match(message string, o Options) bool {
	occurs := func(t term) bool { return termOccurs(t, message, o.TermBoundary) }
	missing := slices.ContainsFunc(p.required, func(t term) bool { return !occurs(t) })
	return !missing && !slices.ContainsFunc(p.excluded, occurs)
}

func (p anyTerm) match(message string, o Options) bool {
	return slices.ContainsFunc(p.optional, func(t term) bool { return termOccurs(t, message, o.TermBoundary) })
}

func termOccurs(t term, message string, b TermBoundary) bool {
	switch t := t.(type) {
	case literalTerm:
		return termMatches(t.text, message, b)
	case regexTerm:
		return t.re.MatchString(message)
	}
	return false
}

// termMatches is the one place the token-boundary question (section 5) is
// decided. A term that is blank after trimming matches every event (N2).
// Blank means the pattern grammar's whitespace (isSpace), not Unicode's: a
// phrase of no-break spaces is searched for like any other text.
func termMatches(term, message string, b TermBoundary) bool {
	if isBlank(term) {
		return true
	}
	switch b {
	case TermSubstring:
		return strings.Contains(message, term)
	case TermToken:
		return containsRun(tokens(message), tokens(term))
	}
	return false
}

// isBlank reports whether s is made only of isSpace bytes. A multi-byte
// character is never blank, since none of its bytes is ASCII.
func isBlank(s string) bool {
	for i := range len(s) {
		if !isSpace(s[i]) {
			return false
		}
	}
	return true
}

// tokens cuts s at every character outside [A-Za-z0-9_].
func tokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !isWordRune(r) })
}

func isWordRune(r rune) bool {
	return r == '_' || ('0' <= r && r <= '9') || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')
}

// containsRun reports whether run occurs as a contiguous subsequence of ts.
// An empty run occurs everywhere.
func containsRun(ts, run []string) bool {
	for i := 0; i+len(run) <= len(ts); i++ {
		if slices.Equal(ts[i:i+len(run)], run) {
			return true
		}
	}
	return false
}
