// Package resource is a REST API's resource paths: parsing a path part,
// normalizing a data-plane request path into its stage and resource path
// (apigateway-rest.md 4.2), and the backtracking, method-aware match of a
// request against a deployed resource tree (4.3, 4.4).
//
// Matching is not implemented yet: ParsePart refuses every part and Find
// matches nothing.
package resource

import (
	"fmt"
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

// ParsePart parses a CreateResource pathPart (apigateway-rest.md 3.2). It
// is not implemented yet and refuses every part.
func ParsePart(pathPart string) (Part, *PartError) {
	return nil, &PartError{PathPart: pathPart, Problem: "path part parsing is not implemented yet"}
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

// Find matches a request's method and Target.Path against rs: literal
// before variable before greedy at the first differing segment, backing
// out of a branch whose resource has no serving method
// (apigateway-rest.md 4.3, finding 4). It is not implemented yet and
// matches nothing.
func Find(_ []Resource, _, _ string, _ Methods) (Match, bool) { return Match{}, false }
