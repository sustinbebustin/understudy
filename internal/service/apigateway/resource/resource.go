// Package resource is a REST API's resource paths: parsing a path part,
// normalizing a data-plane request path into its stage and resource path
// (apigateway-rest.md 4.2), and the backtracking, method-aware match of a
// request against a deployed resource tree (4.3, 4.4).
package resource

import (
	"cmp"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// Part is one path part of a resource, the text between two slashes of its
// template.
//
//sumtype:decl
type Part interface{ isPart() }

// Literal is a path part matched as written.
type Literal struct{ Value string }

// Var is {Name}: one non-empty segment.
type Var struct{ Name string }

// Greedy is {Name+}: one or more segments, the last part of a template
// only (apigateway-rest.md 3.2).
type Greedy struct{ Name string }

func (Literal) isPart() {}
func (Var) isPart()     {}
func (Greedy) isPart()  {}

// PartError is a path part ParsePart refuses.
type PartError struct{ PathPart, Problem string }

func (e *PartError) Error() string {
	return fmt.Sprintf("path part %q is not valid: %s", e.PathPart, e.Problem)
}

// The path part alphabets (apigateway-rest.md 3.2). A literal takes the
// known-issues page's "alphanumeric characters, underscores, hyphens,
// periods, commas, colons"; a variable's name is restricted to the same
// set less commas and colons, although the documentation allows "any
// string" for a greedy name (inferred). That CreateResource enforces them
// is inferred.
var (
	literalPart  = regexp.MustCompile(`^[A-Za-z0-9._,:-]+$`)
	variableName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// ParsePart parses a CreateResource pathPart (apigateway-rest.md 3.2):
// {name} is a Var, {name+} a Greedy, anything else a Literal of the
// literal alphabet. Braces must wrap the whole part.
func ParsePart(pathPart string) (Part, *PartError) {
	refuse := func(problem string) (Part, *PartError) {
		return nil, &PartError{PathPart: pathPart, Problem: problem}
	}
	if inner, ok := strings.CutPrefix(pathPart, "{"); ok {
		inner, ok = strings.CutSuffix(inner, "}")
		if !ok {
			return refuse("a path variable must be wrapped in braces from its first character to its last")
		}
		name, greedy := strings.CutSuffix(inner, "+")
		if !variableName.MatchString(name) {
			return refuse("a path variable's name must be one or more of A-Z, a-z, 0-9, '.', '_' and '-'")
		}
		if greedy {
			return Greedy{Name: name}, nil
		}
		return Var{Name: name}, nil
	}
	if !literalPart.MatchString(pathPart) {
		return refuse("a path part must be one or more of A-Z, a-z, 0-9, '.', '_', '-', ',' and ':', or a path variable {name} or {name+}")
	}
	return Literal{Value: pathPart}, nil
}

// Target is a data-plane request path split into the stage it names and
// the paths the Lambda proxy event reports (apigateway-rest.md 4.2).
type Target struct {
	// Stage is the first segment after the leading slashes, "" when none.
	Stage string
	// Path is the event's path: what follows the stage segment, leading
	// slashes dropped, "/" prepended; interior and trailing slashes kept.
	Path string
	// ContextPath is requestContext.path: the escaped path with a run of
	// leading slashes collapsed to one.
	ContextPath string
}

// Normalize splits an escaped request path, as sent after the host or the
// path fallback's API id, per the recorded table (apigateway-rest.md 4.2):
// nothing is decoded, interior and trailing slashes stay.
func Normalize(escapedPath string) Target {
	trimmed := strings.TrimLeft(escapedPath, "/")
	stage, rest, _ := strings.Cut(trimmed, "/")
	return Target{Stage: stage, Path: "/" + strings.TrimLeft(rest, "/"), ContextPath: "/" + trimmed}
}

// Methods is the method part of the apigateway.invoke.routing switch
// (apigateway-rest.md 4.4, APIGW1-08).
type Methods struct {
	// ExplicitBeforeAny serves a request with a resource's explicit method
	// before its ANY method.
	ExplicitBeforeAny bool
	// HeadMatchesGet lets a GET method serve a HEAD request; false serves
	// HEAD only with a HEAD or ANY method.
	HeadMatchesGet bool
}

// Resource is one resource of a deployed REST API.
type Resource struct {
	ID string
	// Path is the full template: "/", "/pets", "/pets/{id}", "/{proxy+}".
	Path string
	// Methods are the HTTP methods with a method resource, ANY included.
	Methods []string
}

// Match is the resource and method that serve a request.
type Match struct {
	ResourceID   string
	ResourcePath string
	// Method is the method resource that serves: the request's verb, or
	// ANY.
	Method string
	// PathParameters are the bound values as sent, percent-encoded; nil
	// when the resource binds none.
	PathParameters map[string]string
}

// Find matches a request's method and Target.Path against rs: of the
// resources whose template binds the path, literal before variable before
// greedy at the first segment where their templates differ, the first with
// a method that serves verb under m. A resource without one is backed out
// of, so a less specific resource that has the method serves
// (apigateway-rest.md 4.3, finding 4). Resources of equal precedence keep
// the order of rs. A template with a part ParsePart refuses never matches.
func Find(rs []Resource, verb, path string, m Methods) (Match, bool) {
	type candidate struct {
		r      Resource
		parts  []Part
		params map[string]string
	}
	segments := requestSegments(path)
	var cs []candidate
	for _, r := range rs {
		parts, ok := templateParts(r.Path)
		if !ok {
			continue
		}
		if params, ok := bind(parts, segments); ok {
			cs = append(cs, candidate{r, parts, params})
		}
	}
	slices.SortStableFunc(cs, func(a, b candidate) int { return precedence(a.parts, b.parts) })
	for _, c := range cs {
		if method, ok := m.serving(c.r.Methods, verb); ok {
			return Match{ResourceID: c.r.ID, ResourcePath: c.r.Path, Method: method, PathParameters: c.params}, true
		}
	}
	return Match{}, false
}

// serving is the method resource among methods that serves verb: the
// explicit method and ANY in the switch's order, then GET for HEAD when the
// switch lets it (apigateway-rest.md 4.4, APIGW1-08).
func (m Methods) serving(methods []string, verb string) (string, bool) {
	order := []string{verb, "ANY"}
	if !m.ExplicitBeforeAny {
		order = []string{"ANY", verb}
	}
	if verb == http.MethodHead && m.HeadMatchesGet {
		order = append(order, http.MethodGet)
	}
	for _, key := range order {
		if slices.Contains(methods, key) {
			return key, true
		}
	}
	return "", false
}

// templateParts parses a resource's full template into its path parts; the
// root "/" has none. A greedy part before another part is refused, as
// CreateResource refuses a child of a greedy resource (apigateway-rest.md
// 3.2), so such a template never matches.
func templateParts(template string) ([]Part, bool) {
	if template == "/" {
		return nil, true
	}
	rest, ok := strings.CutPrefix(template, "/")
	if !ok {
		return nil, false
	}
	var parts []Part
	for text := range strings.SplitSeq(rest, "/") {
		p, err := ParsePart(text)
		if err != nil {
			return nil, false
		}
		if len(parts) > 0 {
			if _, greedy := parts[len(parts)-1].(Greedy); greedy {
				return nil, false
			}
		}
		parts = append(parts, p)
	}
	return parts, true
}

// requestSegments splits a resource path into its segments, keeping
// interior empty ones and dropping a trailing slash (apigateway-rest.md
// 4.2); "/" has none.
func requestSegments(path string) []string {
	rest := strings.TrimPrefix(path, "/")
	if rest == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(rest, "/"), "/")
}

// bind matches parts against segments: a literal equals its segment, a
// variable takes one non-empty segment, a greedy variable the rest when
// any of it is non-empty, joined back with its interior slashes. The
// values stay as sent; params is nil when nothing binds.
func bind(parts []Part, segments []string) (map[string]string, bool) {
	var params map[string]string
	put := func(name, value string) {
		if params == nil {
			params = map[string]string{}
		}
		params[name] = value
	}
	for i, p := range parts {
		switch p := p.(type) {
		case Literal:
			if i >= len(segments) || segments[i] != p.Value {
				return nil, false
			}
		case Var:
			if i >= len(segments) || segments[i] == "" {
				return nil, false
			}
			put(p.Name, segments[i])
		case Greedy:
			rest := segments[min(i, len(segments)):]
			if !slices.ContainsFunc(rest, func(s string) bool { return s != "" }) {
				return nil, false
			}
			put(p.Name, strings.Join(rest, "/"))
			return params, true
		}
	}
	if len(parts) != len(segments) {
		return nil, false
	}
	return params, true
}

// precedence orders two templates that bind the same path: at the first
// position where their kinds differ, literal before variable before
// greedy (apigateway-rest.md 4.3).
func precedence(a, b []Part) int {
	for i := range min(len(a), len(b)) {
		if c := cmp.Compare(rank(a[i]), rank(b[i])); c != 0 {
			return c
		}
	}
	return 0
}

// rank is a part's precedence: the more specific, the lower.
func rank(p Part) int {
	switch p.(type) {
	case Literal:
		return 0
	case Var:
		return 1
	case Greedy:
		return 2
	}
	return 3
}
