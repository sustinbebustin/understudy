// Package jsonpath is the closed JSONPath dialect of the Amazon States
// Language: Reference Paths (dot and bracket only) and Plain Paths with the
// documented or recorded constructs of stepfunctions-asl.md 4.5, over
// document.Value.
package jsonpath

import (
	"errors"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

var errNotImplemented = errors.New("jsonpath: not implemented yet")

// Kind is the grammar a path is parsed with.
type Kind int

// Kinds.
const (
	// Reference: a path to a single node, dot and bracket notation only
	// (stepfunctions-asl.md 4.5).
	Reference Kind = iota + 1
	// Plain: the fuller dialect InputPath, OutputPath and ".$" values take.
	Plain
)

// Path is a parsed path, built only by Parse.
type Path struct {
	_ struct{}
}

// SyntaxError is a path that does not parse.
type SyntaxError struct {
	Path   string // as written
	Reason string // understudy's wording
}

func (e *SyntaxError) Error() string { return "invalid JSONPath '" + e.Path + "': " + e.Reason }

// UnsupportedError is a path using a construct understudy refuses: a
// function such as length(), a multi-clause filter or a filter script
// (ASL-19).
type UnsupportedError struct {
	Path      string // as written
	Construct string // the refused construct as written
}

func (e *UnsupportedError) Error() string {
	return "understudy does not implement the JSONPath construct '" + e.Construct + "' in '" + e.Path + "'"
}

// MissingError is a definite path that selects nothing; the texts are the
// recorded ones (stepfunctions-asl.md 4.6).
type MissingError struct {
	Path string // as written
	// Reason is "No results for path: <normalized>" when the last step is
	// missing, and "Missing property in path <normalized prefix>" when an
	// earlier one is; <normalized> is $['a']['b'] for $.a.b.
	Reason string
}

func (e *MissingError) Error() string { return "Invalid path '" + e.Path + "' : " + e.Reason }

// Parse parses s with grammar k. The error is a *SyntaxError or an
// *UnsupportedError naming the refused construct.
//
// Not implemented yet: it fails.
func Parse(s string, k Kind) (Path, error) {
	_, _ = s, k
	return Path{}, errNotImplemented
}

// String returns p in canonical form: Parse(p.String(), k) succeeds with the
// kind p was parsed with and has the same String.
//
// Not implemented yet: it returns "".
func (p Path) String() string {
	_ = p
	return ""
}

// Eval evaluates p against input, or against context for a "$$" path. A
// definite path returns its one node or a *MissingError; an indefinite one
// (wildcard, slice, union, "..", filter) returns a document.List, empty when
// nothing matches (stepfunctions-asl.md 4.5).
//
// Not implemented yet: it fails.
func (p Path) Eval(input, context document.Value) (document.Value, error) {
	_, _, _ = p, input, context
	return nil, errNotImplemented
}
