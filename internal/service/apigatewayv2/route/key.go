// Package route parses API Gateway HTTP API route keys and selects the
// route a request matches (apigateway-http.md 3.2, 4.3, 4.4). It is pure:
// the caller resolves the stage and holds the routes of its deployment.
package route

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Method is a route key's method: one of the methods of
// $context.httpMethod, or ANY (apigateway-http.md 4.4).
type Method string

// The route key methods.
const (
	MethodAny     Method = "ANY"
	MethodDelete  Method = "DELETE"
	MethodGet     Method = "GET"
	MethodHead    Method = "HEAD"
	MethodOptions Method = "OPTIONS"
	MethodPatch   Method = "PATCH"
	MethodPost    Method = "POST"
	MethodPut     Method = "PUT"
)

// Key is a parsed route key: $default, or a method and a path.
//
//sumtype:decl
type Key interface{ isKey() }

// Default is the $default route key, which catches what no other route
// matches.
type Default struct{}

// Keyed is a route key "<METHOD> <path>". Segments are the path's
// segments after its leading slash, so "/" is one empty Literal; no other
// segment is empty, since AWS refuses a key with an empty path part
// (APIGW2-01).
type Keyed struct {
	Method   Method
	Segments []Segment
}

// Path is the key's path as a route key writes it, such as /pets/{id}.
func (k Keyed) Path() string {
	var b strings.Builder
	for _, s := range k.Segments {
		b.WriteByte('/')
		switch s := s.(type) {
		case Literal:
			b.WriteString(s.Value)
		case Var:
			b.WriteString("{" + s.Name + "}")
		case Greedy:
			b.WriteString("{" + s.Name + "+}")
		}
	}
	return b.String()
}

func (Default) isKey() {}
func (Keyed) isKey()   {}

// Segment is one segment of a route key's path.
//
//sumtype:decl
type Segment interface{ isSegment() }

// Literal matches a request segment equal to Value once unescaped.
type Literal struct{ Value string }

// Var is a path variable {Name}: it matches one non-empty segment.
type Var struct{ Name string }

// Greedy is a greedy path variable {Name+}, only ever the last segment: it
// matches the non-empty rest of the path.
type Greedy struct{ Name string }

func (Literal) isSegment() {}
func (Var) isSegment()     {}
func (Greedy) isSegment()  {}

// KeyError is a route key ParseKey refuses.
type KeyError struct {
	// Key is the route key as sent.
	Key string
	// Problem is the text AWS answers the key with, where APIGW2-01
	// recorded one, else understudy's clause naming the constraint.
	Problem string
}

func (e *KeyError) Error() string {
	return fmt.Sprintf("route key %q is not valid: %s", e.Key, e.Problem)
}

// The refusals' texts, as AWS sent them (APIGW2-01, recorded 2026-11-03:
// steps 10 to 13 and 14.1's "GET /slash/"; apigateway-http.md 2.3, 3.2).
const (
	problemFormat = `The provided route key is not formatted properly for HTTP protocol. Format should be "[HTTP METHOD] /[RESOURCE PATH]" or "$default"`
	problemEmpty  = "Part of the given route key path is empty"
	problemGreedy = "Greedy variables may only be in last position of route key"
	problemPart   = `One or more path parts appear to be invalid. Parts are validated against this regular expression: ^(\{[\w.:-]+\+?\}|[a-zA-Z0-9.:_-]+)$`
)

// methods are the route key methods.
func methods() []Method {
	return []Method{MethodAny, MethodDelete, MethodGet, MethodHead, MethodOptions, MethodPatch, MethodPost, MethodPut}
}

// pathPart is the expression AWS validates each path part against, as its
// refusal quotes it (APIGW2-01 step 13); Java's \w is [A-Za-z0-9_].
var pathPart = regexp.MustCompile(`^(\{[A-Za-z0-9_.:-]+\+?\}|[a-zA-Z0-9.:_-]+)$`)

// ParseKey parses a route key (apigateway-http.md 2.4, 3.2, 13.5):
// "$default", or exactly "<METHOD> <path>" with an upper-case METHOD of
// methods and a path starting with "/". Each path part is a literal of
// [a-zA-Z0-9.:_-], a {name} variable or, last only, one {name+} greedy
// variable, and none is empty but the root's, so "/" is one empty literal
// and "/a/" is refused (APIGW2-01, recorded). A name appears once (the
// text of that refusal is understudy's).
func ParseKey(s string) (Key, *KeyError) {
	if s == "$default" {
		return Default{}, nil
	}
	refuse := func(problem string) (Key, *KeyError) { return nil, &KeyError{Key: s, Problem: problem} }
	method, path, ok := strings.Cut(s, " ")
	if !ok || !slices.Contains(methods(), Method(method)) {
		return refuse(problemFormat)
	}
	rest, ok := strings.CutPrefix(path, "/")
	if !ok {
		return refuse(problemFormat)
	}
	if rest == "" {
		return Keyed{Method: Method(method), Segments: []Segment{Literal{}}}, nil
	}
	segs, problem := parseSegments(strings.Split(rest, "/"))
	if problem != "" {
		return refuse(problem)
	}
	return Keyed{Method: Method(method), Segments: segs}, nil
}

// parseSegments reads a path's parts, or says why they are not a route
// key's.
func parseSegments(parts []string) (segs []Segment, problem string) {
	segs = make([]Segment, 0, len(parts))
	names := map[string]bool{}
	for i, part := range parts {
		if part == "" {
			return nil, problemEmpty
		}
		if !pathPart.MatchString(part) {
			return nil, problemPart
		}
		var name string
		switch seg := parseSegment(part).(type) {
		case Var:
			name = seg.Name
			segs = append(segs, seg)
		case Greedy:
			if i != len(parts)-1 {
				return nil, problemGreedy
			}
			name = seg.Name
			segs = append(segs, seg)
		case Literal:
			segs = append(segs, seg)
			continue
		}
		if names[name] {
			return nil, fmt.Sprintf("variable name %q appears twice in the route key", name)
		}
		names[name] = true
	}
	return segs, ""
}

// parseSegment reads one path part that matches pathPart.
func parseSegment(part string) Segment {
	inner, braced := strings.CutPrefix(part, "{")
	if !braced {
		return Literal{Value: part}
	}
	inner = strings.TrimSuffix(inner, "}")
	if name, greedy := strings.CutSuffix(inner, "+"); greedy {
		return Greedy{Name: name}
	}
	return Var{Name: inner}
}
