package parity

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
)

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

// checkShape says what makes sh, at field of a scenario, not a shape the
// format accepts (testdata/README.md, scenarios); nil when it is one.
func checkShape(field string, sh Shape) error {
	switch sh := sh.(type) {
	case nil:
		return fieldError(field, `is missing: write a shape, such as {"kind": "uuid"}`)
	case UUIDShape:
	case IDShape:
		return sh.check(field)
	case ARNShape:
		return sh.check(field)
	case URLShape:
		return sh.check(field)
	}
	return nil
}

func (sh IDShape) check(field string) error {
	switch {
	case !slices.Contains([]Alphabet{Hex, Base32, Base64, Alnum, Digits}, sh.Alphabet):
		return fieldError(field+".alphabet", fmt.Sprintf("%q is not one of hex, base32, base64, alnum, digits", sh.Alphabet))
	case sh.Min < 1:
		return fieldError(field+".min", fmt.Sprintf("%d is below 1: an id has at least one character", sh.Min))
	case sh.Max < sh.Min:
		return fieldError(field+".max", fmt.Sprintf("%d is below min %d", sh.Max, sh.Min))
	case sh.Max < len(sh.Prefix):
		return fieldError(field+".max", fmt.Sprintf("%d is shorter than the prefix %q, so no value has the shape", sh.Max, sh.Prefix))
	}
	return nil
}

func (sh ARNShape) check(field string) error {
	switch {
	case !servicePattern.MatchString(sh.Service):
		return fieldError(field+".service", fmt.Sprintf("%q is not an ARN's service, such as kms or iam", sh.Service))
	case strings.ContainsAny(sh.Resource, "/:"):
		return fieldError(field+".resource", fmt.Sprintf("%q holds a / or :; the resource type is the text before the first of them, such as key or role, and empty for a resource with no type", sh.Resource))
	case sh.Name != nil:
		return checkShape(field+".name", sh.Name)
	}
	return nil
}

func (sh URLShape) check(field string) error {
	rest, ok := strings.CutPrefix(sh.Path, "/")
	for seg := range strings.SplitSeq(rest, "/") {
		ok = ok && (seg == "{account}" || seg == "{name}" || !strings.ContainsAny(seg, "{}?#"))
	}
	if !ok {
		return fieldError(field+".path", fmt.Sprintf("%q is not a path template of /-separated literal segments, {account} and {name}, such as /{account}/{name}", sh.Path))
	}
	return nil
}

// The kind member of each shape in a scenario file.
const (
	uuidKind = "uuid"
	idKind   = "id"
	arnKind  = "arn"
	urlKind  = "url"
)

// The shapes as a scenario file spells them, members in the canonical
// order.
type (
	uuidFile struct {
		Kind string `json:"kind"`
	}
	idFile struct {
		Kind     string   `json:"kind"`
		Alphabet Alphabet `json:"alphabet"`
		Min      int      `json:"min"`
		Max      int      `json:"max"`
		Prefix   string   `json:"prefix,omitempty"`
	}
	arnFile struct {
		Kind     string         `json:"kind"`
		Service  string         `json:"service"`
		Resource string         `json:"resource,omitempty"`
		Name     jsontext.Value `json:"name,omitzero"`
	}
	urlFile struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	}
)

// decodeShape reads the shape spelled raw, the member at field of a
// scenario file; empty raw is a missing shape. Each kind takes only its own
// members.
func decodeShape(field string, raw jsontext.Value) (Shape, error) {
	if len(raw) == 0 {
		return nil, checkShape(field, nil)
	}
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, decodeError(field, err)
	}
	switch head.Kind {
	case uuidKind:
		return decodeKind(field, raw, func(uuidFile) (Shape, error) { return UUIDShape{}, nil })
	case idKind:
		return decodeKind(field, raw, func(f idFile) (Shape, error) {
			return IDShape{Alphabet: f.Alphabet, Min: f.Min, Max: f.Max, Prefix: f.Prefix}, nil
		})
	case arnKind:
		return decodeKind(field, raw, func(f arnFile) (Shape, error) {
			sh := ARNShape{Service: f.Service, Resource: f.Resource}
			if len(f.Name) == 0 {
				return sh, nil
			}
			name, err := decodeShape(field+".name", f.Name)
			if err != nil {
				return nil, err
			}
			sh.Name = name
			return sh, nil
		})
	case urlKind:
		return decodeKind(field, raw, func(f urlFile) (Shape, error) { return URLShape{Path: f.Path}, nil })
	case "":
		return nil, fieldError(field, "has no kind: uuid, id, arn or url")
	}
	return nil, fieldError(field+".kind", fmt.Sprintf("%q is not uuid, id, arn or url", head.Kind))
}

// decodeKind reads raw strictly as the file form F of one shape kind and
// converts it.
func decodeKind[F any](field string, raw jsontext.Value, conv func(F) (Shape, error)) (Shape, error) {
	var f F
	if err := json.Unmarshal(raw, &f, json.RejectUnknownMembers(true)); err != nil {
		return nil, decodeError(field, err)
	}
	return conv(f)
}

// encodeShape writes sh in its file form.
func encodeShape(sh Shape) (jsontext.Value, error) {
	var f any
	switch sh := sh.(type) {
	case nil:
		return nil, errors.New("parity: encoding a nil shape")
	case UUIDShape:
		f = uuidFile{Kind: uuidKind}
	case IDShape:
		f = idFile{Kind: idKind, Alphabet: sh.Alphabet, Min: sh.Min, Max: sh.Max, Prefix: sh.Prefix}
	case ARNShape:
		af := arnFile{Kind: arnKind, Service: sh.Service, Resource: sh.Resource}
		if sh.Name != nil {
			name, err := encodeShape(sh.Name)
			if err != nil {
				return nil, err
			}
			af.Name = name
		}
		f = af
	case URLShape:
		f = urlFile{Kind: urlKind, Path: sh.Path}
	}
	return json.Marshal(f)
}
