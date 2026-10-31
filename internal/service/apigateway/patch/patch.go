// Package patch applies API Gateway's patch operations: JSON Pointer
// paths with their own per-resource path tables, not RFC 6902
// (apigateway-rest.md 3.8). A Table lists the paths and ops one resource
// accepts; Apply runs a request's operations in order on a copy and
// changes nothing when any operation is refused.
package patch

import (
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// Operation is one patch operation as sent.
type Operation struct {
	// Op is the operation; the generated validator has already refused one
	// outside the model's enum (apigateway-rest.md 3.8).
	Op apigatewayapi.Op
	// Path is the JSON Pointer as sent, "" when absent.
	Path string
	// Value and From are nil when absent.
	Value *string
	From  *string
}

// FromModel converts the generated patch operations, each as sent.
func FromModel(ops []apigatewayapi.PatchOperation) []Operation {
	out := make([]Operation, len(ops))
	for i, o := range ops {
		out[i] = Operation{Path: deref(o.Path), Value: clonePtr(o.Value), From: clonePtr(o.From)}
		if o.Op != nil {
			out[i].Op = *o.Op
		}
	}
	return out
}

// Segments splits a JSON Pointer into its segments, unescaping "~1" to "/"
// and then "~0" to "~" in each ([S] PatchOperation$path; RFC 6901 section
// 4). An empty path has no segments.
func Segments(path string) []string {
	if path == "" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	return parts
}

// match reports whether segs fit pattern, a Rule's Path, and returns the
// segments its "*" and "**" matched.
func match(pattern string, segs []string) ([]string, bool) {
	pat := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	var wild []string
	for i, p := range pat {
		if p == "**" && i == len(pat)-1 {
			if len(segs) <= i {
				return nil, false
			}
			return append(wild, segs[i:]...), true
		}
		if i >= len(segs) {
			return nil, false
		}
		switch p {
		case "*":
			wild = append(wild, segs[i])
		case segs[i]:
		default:
			return nil, false
		}
	}
	return wild, len(segs) == len(pat)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func clonePtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// Rule is one path a resource accepts and the ops it accepts there.
type Rule[T any] struct {
	// Path is the rule's pattern: literal segments, "*" for one segment,
	// "**" for one or more, last only.
	Path string
	Ops  []apigatewayapi.Op
	// OpText is the refusal text for an op not in Ops; nil uses the
	// table's.
	OpText func(op Operation) string
	// Apply applies op to t; wild are the segments "*" and "**" matched,
	// unescaped.
	Apply func(t *T, op Operation, wild []string) *awserr.Error
}

// Table is one resource's patch paths.
type Table[T any] struct {
	Rules []Rule[T]
	// UnknownPath is the refusal text for a path no rule matches.
	UnknownPath func(op Operation) string
	// OpText is the default refusal text for an op a rule does not accept.
	OpText func(op Operation) string
	// Clone deep-copies a value, so a refused request changes nothing.
	Clone func(T) T
}

// Apply applies ops to a copy of v in order ([S] ListOfPatchOperation) and
// returns the copy, or v and the first refusal (all-or-nothing, inferred,
// apigateway-rest.md 12.4). An operation is refused when no rule's Path
// matches it (UnknownPath), when its rule does not take its op (the rule's
// OpText, else the table's), or by its rule's Apply. The first rule whose
// Path matches decides.
func (t Table[T]) Apply(v T, ops []Operation) (T, *awserr.Error) {
	out := t.Clone(v)
	for _, op := range ops {
		if err := t.apply(&out, op); err != nil {
			return v, err
		}
	}
	return out, nil
}

// apply applies one operation to v.
func (t Table[T]) apply(v *T, op Operation) *awserr.Error {
	segs := Segments(op.Path)
	for _, r := range t.Rules {
		wild, ok := match(r.Path, segs)
		if !ok {
			continue
		}
		if !slices.Contains(r.Ops, op.Op) {
			text := t.OpText
			if r.OpText != nil {
				text = r.OpText
			}
			return apigatewayapi.ErrBadRequestException(text(op))
		}
		return r.Apply(v, op, wild)
	}
	return apigatewayapi.ErrBadRequestException(t.UnknownPath(op))
}

// pathOf is the path a refusal names: "null" when the operation has none,
// as recorded (test_apigateway_api.snapshot.json:L2762).
func pathOf(op Operation) string {
	if op.Path == "" {
		return "null"
	}
	return op.Path
}

// InvalidPath is the recorded "Invalid patch path <path>" text
// (apigateway-rest.md 3.8; test_apigateway_api.snapshot.json:L507).
func InvalidPath(op Operation) string { return "Invalid patch path " + pathOf(op) }

// MustBeOneOf returns the recorded text for a path outside paths, with its
// two spaces after "path" (test_apigateway_api.snapshot.json:L689).
func MustBeOneOf(paths ...string) func(Operation) string {
	return func(op Operation) string {
		return "Invalid patch path  '" + pathOf(op) + "' specified for op '" + string(op.Op) + "'. Must be one of: [" + strings.Join(paths, ", ") + "]"
	}
}

// ChooseSupported is the recorded text for a known path with an op it
// does not accept (test_apigateway_api.snapshot.json:L792).
func ChooseSupported(op Operation) string {
	return "Invalid patch path  '" + pathOf(op) + "' specified for op '" + string(op.Op) + "'. Please choose supported operations"
}

// OpsOneOf returns the recorded text for an op outside ops
// (test_apigateway_api.snapshot.json:L468).
func OpsOneOf(ops ...apigatewayapi.Op) func(Operation) string {
	names := make([]string, len(ops))
	for i, o := range ops {
		names[i] = string(o)
	}
	text := "Invalid patch operation specified. Must be one of: [" + strings.Join(names, ", ") + "]"
	return func(Operation) string { return text }
}

// OpsAddRemoveReplace is the recorded text for an op other than add,
// remove or replace on the paths that take those three
// (test_apigateway_api.snapshot.json:L4774).
func OpsAddRemoveReplace(Operation) string {
	return "Invalid patch operation specified. Must be 'add'|'remove'|'replace'"
}
