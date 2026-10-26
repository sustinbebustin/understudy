package parity

import "fmt"

// Shape is the form a bound value must have on both sides before it is
// bound: an ARN's service, type and name, an id's length and alphabet, or a
// URL's path.
//
//sumtype:decl
type Shape interface{ isShape() }

// UUIDShape is a lower-case 8-4-4-4-12 hex UUID.
type UUIDShape struct{}

// IDShape is Prefix followed by characters of Alphabet, Min to Max
// characters in all.
type IDShape struct {
	Alphabet Alphabet
	Min, Max int
	Prefix   string
}

// ARNShape is an ARN of Service in the scenario's account and region whose
// resource type is Resource ("" for a resource with no type). Name, when
// set, is the shape of the resource name on each side; when nil the names
// are equal.
type ARNShape struct {
	Service  string
	Resource string
	Name     Shape
}

// URLShape is an absolute http or https URL whose path matches Path, a
// template of literal segments, {account} and {name}; the paths of both
// sides are equal.
type URLShape struct {
	Path string
}

func (UUIDShape) isShape() {}
func (IDShape) isShape()   {}
func (ARNShape) isShape()  {}
func (URLShape) isShape()  {}

// Alphabet is the character set of an IDShape after its prefix.
type Alphabet string

// The alphabets.
const (
	Hex    Alphabet = "hex"
	Base32 Alphabet = "base32"
	Base64 Alphabet = "base64"
	Alnum  Alphabet = "alnum"
	Digits Alphabet = "digits"
)

// ShapeError reports a bound value that does not have its shape.
type ShapeError struct {
	// Side is "aws" or "understudy".
	Side   string
	Value  string
	Reason string
}

func (e *ShapeError) Error() string {
	return fmt.Sprintf("the %s value %q: %s", e.Side, e.Value, e.Reason)
}

// CheckBind checks both sides of one binding against sh for a scenario's
// account and region. Not implemented yet: it refuses every binding.
func CheckBind(_ Shape, aws, _, _, _ string) error {
	return &ShapeError{Side: "aws", Value: aws, Reason: notImplemented("CheckBind").Error()}
}
