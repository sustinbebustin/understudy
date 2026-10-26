package parity

import "fmt"

// Path locates one compared field of a response: status, error/code,
// error/message, header/<name>, or body followed by RFC 6901 tokens (JSON)
// or element local names with [i] and @attr (XML), optionally prefixed
// exchange/<i>/ for an exchange before a step's final one. Waivers may name
// the prefixes error and header alone.
type Path string

// PathError reports text that is not a comparison path, or a path an
// operation cannot use.
type PathError struct {
	Text   string
	Reason string
}

func (e *PathError) Error() string {
	return fmt.Sprintf("comparison path %q: %s", e.Text, e.Reason)
}

// ParsePath returns the comparison path spelled s. Not implemented yet: it
// refuses every path.
func ParsePath(s string) (Path, error) {
	return "", &PathError{Text: s, Reason: notImplemented("ParsePath").Error()}
}

// Covers reports whether p is q or an ancestor of q, as a waiver on p covers
// q. Not implemented yet: it reports false.
func (p Path) Covers(Path) bool { return false }
