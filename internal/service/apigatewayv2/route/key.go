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
// segments after its leading slash, so "/" is one empty Literal and a
// trailing slash is a last empty Literal.
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
	// Problem names the constraint the key breaks, as a clause.
	Problem string
}

func (e *KeyError) Error() string {
	return fmt.Sprintf("route key %q is not valid: %s", e.Key, e.Problem)
}

// methods are the route key methods, in the order a KeyError lists them.
func methods() []Method {
	return []Method{MethodAny, MethodDelete, MethodGet, MethodHead, MethodOptions, MethodPatch, MethodPost, MethodPut}
}

// variableName is the name inside {name} or {name+}. The documents give no
// character set (apigateway-http.md 3.2); this one, inferred, holds every
// name their examples and Terraform's use.
var variableName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ParseKey parses a route key (apigateway-http.md 2.4, 3.2, 13.5):
// "$default", or exactly "<METHOD> <path>" with an upper-case METHOD of
// methods and a path starting with "/". The path's segments are literals,
// {name} variables and, last only, one {name+} greedy variable; a name
// appears once, a literal holds no brace, and only the last segment may
// be empty, so "/" is one empty literal and "/a/" ends with one. The
// problems' wording is inferred (APIGW2-01).
func ParseKey(s string) (Key, *KeyError) {
	if s == "$default" {
		return Default{}, nil
	}
	refuse := func(problem string) (Key, *KeyError) { return nil, &KeyError{Key: s, Problem: problem} }
	method, path, ok := strings.Cut(s, " ")
	if !ok {
		return refuse(`it is neither $default nor "<METHOD> <path>"`)
	}
	if !slices.Contains(methods(), Method(method)) {
		names := make([]string, 0, len(methods()))
		for _, m := range methods() {
			names = append(names, string(m))
		}
		return refuse(fmt.Sprintf("method %q is not one of %s", method, strings.Join(names, ", ")))
	}
	rest, ok := strings.CutPrefix(path, "/")
	if !ok {
		return refuse(fmt.Sprintf("path %q does not start with /", path))
	}
	segs, problem := parseSegments(strings.Split(rest, "/"))
	if problem != "" {
		return refuse(problem)
	}
	return Keyed{Method: Method(method), Segments: segs}, nil
}

// parseSegments reads a path's segments, or says why they are not a route
// key's.
func parseSegments(parts []string) (segs []Segment, problem string) {
	segs = make([]Segment, 0, len(parts))
	names := map[string]bool{}
	for i, part := range parts {
		last := i == len(parts)-1
		if part == "" && !last {
			return nil, "the path has an empty segment before its end"
		}
		seg, problem := parseSegment(part)
		if problem != "" {
			return nil, problem
		}
		var name string
		switch seg := seg.(type) {
		case Var:
			name = seg.Name
		case Greedy:
			if !last {
				return nil, fmt.Sprintf("greedy variable {%s+} is not the last segment", seg.Name)
			}
			name = seg.Name
		case Literal:
			segs = append(segs, seg)
			continue
		}
		if names[name] {
			return nil, fmt.Sprintf("variable name %q appears twice", name)
		}
		names[name] = true
		segs = append(segs, seg)
	}
	return segs, ""
}

// parseSegment reads one path segment, or says why it is not one.
func parseSegment(part string) (seg Segment, problem string) {
	inner, braced := strings.CutPrefix(part, "{")
	if braced {
		inner, braced = strings.CutSuffix(inner, "}")
	}
	if !braced {
		if strings.ContainsAny(part, "{}") {
			return nil, fmt.Sprintf("segment %q mixes literal text with a brace; a variable is a whole segment, {name} or {name+}", part)
		}
		return Literal{Value: part}, ""
	}
	name, greedy := strings.CutSuffix(inner, "+")
	if !variableName.MatchString(name) {
		return nil, fmt.Sprintf("variable name %q is not one or more letters, digits, '.', '_' or '-'", name)
	}
	if greedy {
		return Greedy{Name: name}, ""
	}
	return Var{Name: name}, ""
}
