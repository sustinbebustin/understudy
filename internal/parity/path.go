package parity

import (
	"fmt"
	"strings"
)

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

// The fields a path names, below any exchange/<i>/ prefix.
const (
	statusPath       Path = "status"
	errorPath        Path = "error"
	errorCodePath    Path = "error/code"
	errorMessagePath Path = "error/message"
	headerPath       Path = "header"
	bodyPath         Path = "body"
)

// ParsePath returns the comparison path spelled s.
func ParsePath(s string) (Path, error) {
	field := s
	if rest, ok := strings.CutPrefix(s, "exchange/"); ok {
		i, after, ok := strings.Cut(rest, "/")
		if !ok || !isIndex(i) {
			return "", &PathError{Text: s, Reason: "exchange/ takes a 0-based exchange index and a field, as in exchange/0/status"}
		}
		field = after
	}
	if reason := fieldReason(field); reason != "" {
		return "", &PathError{Text: s, Reason: reason}
	}
	return Path(s), nil
}

// fieldReason says why field is not a path's field, or "" when it is one.
func fieldReason(field string) string {
	switch Path(field) {
	case statusPath, errorPath, errorCodePath, errorMessagePath, headerPath, bodyPath:
		return ""
	}
	if name, ok := strings.CutPrefix(field, "header/"); ok {
		return headerNameReason(name)
	}
	if tokens, ok := strings.CutPrefix(field, "body/"); ok {
		for tok := range strings.SplitSeq(tokens, "/") {
			if !validToken(tok) {
				return fmt.Sprintf("body token %q has a ~ not followed by 0 or 1 (RFC 6901 writes ~ as ~0 and / as ~1)", tok)
			}
		}
		return ""
	}
	return "the field is not status, error, error/code, error/message, header, header/<name>, body or body/<token>..."
}

// headerNameReason says why name is not a header field's name, or "" when
// it is one: an HTTP token in lower case.
func headerNameReason(name string) string {
	if name == "" {
		return "header/ takes a header name"
	}
	if strings.ToLower(name) != name {
		return "header names are lower-case: write header/" + strings.ToLower(name)
	}
	for i := range len(name) {
		if c := name[i]; !isDigit(c) && ('a' > c || c > 'z') && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return fmt.Sprintf("%q is not an HTTP header name", name)
		}
	}
	return ""
}

// validToken reports whether every ~ in tok starts an RFC 6901 escape.
func validToken(tok string) bool {
	for i := range len(tok) {
		if tok[i] == '~' && (i+1 == len(tok) || tok[i+1] != '0' && tok[i+1] != '1') {
			return false
		}
	}
	return true
}

// isIndex reports whether s is a decimal index without leading zeros.
func isIndex(s string) bool {
	if s == "" || len(s) > 1 && s[0] == '0' {
		return false
	}
	for i := range len(s) {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// Covers reports whether p is q or an ancestor of q, as a waiver on p covers
// q: q equals p or continues it after a "/".
func (p Path) Covers(q Path) bool {
	return q == p || strings.HasPrefix(string(q), string(p)+"/")
}

// leafReason says why p, a parsed path, names no single value of a step's
// final response, which a bind and a settles field must name; "" when it
// names one. error and header alone are groups only a waiver may name, and
// exchange/<i>/ names an exchange before the final one.
func (p Path) leafReason() string {
	switch {
	case strings.HasPrefix(string(p), "exchange/"):
		return "names an exchange before the final one; binds and settles fields read the final exchange, whose paths carry no exchange/ prefix"
	case p == errorPath:
		return "names every error field, which only a waiver may; name error/code or error/message"
	case p == headerPath:
		return "names every header, which only a waiver may; name one, such as header/content-type"
	}
	return ""
}

// child is the path of p's member or element token, written as is.
func (p Path) child(token string) Path { return p + "/" + Path(token) }

// member is the path of p's JSON member name, escaped as an RFC 6901
// reference token.
func (p Path) member(name string) Path {
	return p.child(strings.NewReplacer("~", "~0", "/", "~1").Replace(name))
}
