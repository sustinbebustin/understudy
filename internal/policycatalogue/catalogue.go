package policycatalogue

import (
	"fmt"
	"iter"
	"time"
)

// Type is a policy page's "Type" (iam-evaluation.md 10.3). The four values
// and the ARN path each is published under are the one-for-one pairing
// iam-evaluation.md 10.4 counts.
type Type uint8

const (
	// AWSManaged is "AWS managed policy", on path "/".
	AWSManaged Type = iota + 1
	// ServiceLinkedRole is "Service-linked role policy", on path
	// "/aws-service-role/".
	ServiceLinkedRole
	// ServiceRole is "Service role policy", on path "/service-role/".
	ServiceRole
	// JobFunction is "Job function policy", on path "/job-function/".
	JobFunction
)

// ParseType reads a page's Type spelling, and false for any other text.
// Not implemented yet: it reads nothing.
func ParseType(string) (Type, bool) { return 0, false }

// String is the page's spelling of t, "" for the zero Type. Not
// implemented yet: it returns "".
func (Type) String() string { return "" }

// Path is the ARN path t is published under, "" for the zero Type. Not
// implemented yet: it returns "".
func (Type) Path() string { return "" }

// Policy is one AWS managed policy as the guide publishes it
// (iam-evaluation.md 10.3, 10.5).
type Policy struct {
	// Name is the PolicyName, the page's <Name>.
	Name string
	// ARN is arn:aws:iam::aws:policy followed by Path and Name.
	ARN string
	// Path is the ARN's IAM path, "/" or "/<segments>/".
	Path string
	Type Type
	// Description is the page's Description text; it may be empty.
	Description string
	// VersionID is the default version, v<N>.
	VersionID string
	// Created and Updated are the page's Creation and Edited times, UTC,
	// at minute precision.
	Created, Updated time.Time
	// Document is the default version's JSON document, byte for byte as
	// published.
	Document string
}

// Field is a policies.json member name, naming what an InvalidError is
// about.
type Field string

// The members of a policies.json policy object, in file order.
const (
	FieldPolicyName       Field = "policyName"
	FieldARN              Field = "arn"
	FieldPath             Field = "path"
	FieldType             Field = "type"
	FieldDescription      Field = "description"
	FieldDefaultVersionID Field = "defaultVersionId"
	FieldCreateDate       Field = "createDate"
	FieldUpdateDate       Field = "updateDate"
	FieldDocument         Field = "document"
)

// InvalidError is one policy that breaks a rule of New.
type InvalidError struct {
	// Index is the policy's position in New's input, or in policies.json's
	// array for Parse.
	Index int
	// Name is the policy's Name as given.
	Name   string
	Field  Field
	Reason string
}

func (e *InvalidError) Error() string {
	return fmt.Sprintf("policycatalogue: policy %d (%q): %s: %s", e.Index, e.Name, e.Field, e.Reason)
}

// DecodeError is policies.json text that is not the catalogue's format.
type DecodeError struct{ Err error }

func (e *DecodeError) Error() string {
	return "policycatalogue: policies.json is not a policy catalogue: " + e.Err.Error()
}

func (e *DecodeError) Unwrap() error { return e.Err }

// notImplementedError is the answer of every operation not built yet.
type notImplementedError struct{ op string }

func (e *notImplementedError) Error() string {
	return "policycatalogue: " + e.op + " is not implemented yet"
}

// Catalogue is a validated set of AWS managed policies in listing order:
// by name regardless of case, then by ARN. A nil *Catalogue is the empty
// catalogue, and every method works on it. Not implemented yet: it holds
// nothing.
type Catalogue struct{}

// New checks every policy and returns them as a catalogue, or every
// violation as an *InvalidError, joined. Not implemented yet: it refuses
// every input.
func New([]Policy) (*Catalogue, error) {
	return nil, &notImplementedError{op: "building a catalogue"}
}

// Parse reads policies.json strictly, then checks it as New does. Not
// implemented yet: it refuses every input.
func Parse([]byte) (*Catalogue, error) {
	return nil, &notImplementedError{op: "parsing policies.json"}
}

// Lookup returns the policy whose ARN is arn exactly. Not implemented yet:
// it finds none.
func (c *Catalogue) Lookup(string) (Policy, bool) { return Policy{}, false }

// All yields every policy once, in listing order. Not implemented yet: it
// yields none.
func (c *Catalogue) All() iter.Seq[Policy] { return func(func(Policy) bool) {} }

// Len is the number of policies. Not implemented yet: it is 0.
func (c *Catalogue) Len() int { return 0 }

// Encode writes c as canonical policies.json. Not implemented yet: it
// refuses.
func (c *Catalogue) Encode() ([]byte, error) {
	return nil, &notImplementedError{op: "encoding policies.json"}
}

// Loader returns the catalogue to serve. It may be called on every request
// that needs the catalogue and returns the same result each time.
type Loader func() (*Catalogue, error)

// Embedded is the Loader of the catalogue compiled into the binary,
// awsmanagedpolicies.PoliciesFile, parsed on the first call. Not
// implemented yet: it refuses.
func Embedded() (*Catalogue, error) {
	return nil, &notImplementedError{op: "loading the embedded catalogue"}
}

// Static is a Loader that returns c. Not implemented yet: it refuses.
func Static(*Catalogue) Loader {
	return func() (*Catalogue, error) {
		return nil, &notImplementedError{op: "a static catalogue"}
	}
}
