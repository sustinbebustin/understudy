// Package route parses API Gateway HTTP API route keys and selects the
// route a request matches (apigateway-http.md 3.2, 4.3, 4.4). It is pure:
// the caller resolves the stage and holds the routes of its deployment.
package route

import "fmt"

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
// It is not implemented yet and returns "".
func (Keyed) Path() string { return "" }

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

// ParseKey parses a route key (apigateway-http.md 2.4, 3.2, 13.5). It is
// not implemented yet and refuses every key.
func ParseKey(s string) (Key, *KeyError) {
	return nil, &KeyError{Key: s, Problem: "route key parsing is not implemented yet"}
}
