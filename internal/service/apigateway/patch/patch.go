// Package patch applies API Gateway's patch operations: JSON Pointer
// paths with their own per-resource path tables, not RFC 6902
// (apigateway-rest.md 3.8). A Table lists the paths and ops one resource
// accepts; Apply runs a request's operations in order on a copy and
// changes nothing when any operation is refused.
//
// It is not implemented yet: Segments returns nil and Table.Apply refuses
// every request with NotImplemented. The message helpers return the
// recorded texts already.
package patch

import (
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

// FromModel converts the generated patch operations. It is not implemented
// yet and returns nil.
func FromModel([]apigatewayapi.PatchOperation) []Operation { return nil }

// Segments splits a JSON Pointer into its segments, unescaping "~1" to "/"
// and "~0" to "~" in each ([S] PatchOperation$path). It is not implemented
// yet and returns nil.
func Segments(string) []string { return nil }

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

// errNotImplemented refuses every request until Apply is implemented.
func errNotImplemented() *awserr.Error {
	return apigatewayapi.ErrNotImplemented("understudy does not implement API Gateway patch operations yet; nothing was changed. Leave out patchOperations, or set the value with the create call")
}

// Apply applies ops to a copy of v in order and returns the copy, or v and
// the first refusal (all-or-nothing, apigateway-rest.md 12.4). It is not
// implemented yet and refuses every request.
func (Table[T]) Apply(v T, _ []Operation) (T, *awserr.Error) {
	return v, errNotImplemented()
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
