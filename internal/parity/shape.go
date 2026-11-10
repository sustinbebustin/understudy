package parity

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
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

// The sides of a binding, as a ShapeError names them.
const (
	sideAWS        = "aws"
	sideUnderstudy = "understudy"
)

// CheckBind checks both sides of one binding against sh for a scenario's
// account and region: each side must have the shape,
// and what the shape leaves fixed must agree across sides (an ARN's name
// when the shape gives it none, a URL's path). A *ShapeError names the side
// that breaks a rule, the aws side first; a disagreement is the understudy
// side's.
func CheckBind(sh Shape, aws, ours, account, region string) error {
	sc := bindScope{account: account, region: region, noRegion: arnRegion(aws) == ""}
	if reason := sc.check(sh, aws); reason != "" {
		return &ShapeError{Side: sideAWS, Value: aws, Reason: reason}
	}
	if reason := sc.check(sh, ours); reason != "" {
		return &ShapeError{Side: sideUnderstudy, Value: ours, Reason: reason}
	}
	if reason := agree(sh, aws, ours); reason != "" {
		return &ShapeError{Side: sideUnderstudy, Value: ours, Reason: reason}
	}
	return nil
}

// bindScope is what a bound value's shape is checked against.
type bindScope struct {
	account, region string
	// noRegion lets an ARN leave its region empty: set when the recorded
	// side's ARN has none, as IAM's ARNs do.
	noRegion bool
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// chars are the characters a allows; none for an unknown alphabet, which
// the scenario loader refuses.
func (a Alphabet) chars() string {
	const upper, lower, digits = "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz", "0123456789"
	switch a {
	case Hex:
		return digits + "abcdef"
	case Base32:
		return upper + "234567"
	case Base64:
		return upper + lower + digits + "+/="
	case Alnum:
		return upper + lower + digits
	case Digits:
		return digits
	}
	return ""
}

// check says why v does not have the shape sh, or "" when it has.
func (sc bindScope) check(sh Shape, v string) string {
	switch sh := sh.(type) {
	case UUIDShape:
		if !uuidPattern.MatchString(v) {
			return "is not a lower-case UUID, 8-4-4-4-12 hex digits"
		}
	case IDShape:
		return sh.reason(v)
	case ARNShape:
		return sc.arnReason(sh, v)
	case URLShape:
		return sc.urlReason(sh, v)
	case nil:
		return "has no shape to check"
	}
	return ""
}

func (sh IDShape) reason(v string) string {
	rest, ok := strings.CutPrefix(v, sh.Prefix)
	switch {
	case !ok:
		return fmt.Sprintf("does not start with the prefix %q", sh.Prefix)
	case len(v) < sh.Min || len(v) > sh.Max:
		return fmt.Sprintf("has %d characters; the shape allows %d to %d", len(v), sh.Min, sh.Max)
	}
	if i := strings.IndexFunc(rest, func(r rune) bool { return !strings.ContainsRune(sh.Alphabet.chars(), r) }); i >= 0 {
		r, _ := utf8.DecodeRuneInString(rest[i:])
		return fmt.Sprintf("holds %q after the prefix, which is not in the %s alphabet", r, sh.Alphabet)
	}
	return ""
}

// arn is an ARN of the aws partition split into its fields.
type arn struct {
	service, region, account, resource string
}

func parseARN(v string) (arn, bool) {
	fields := strings.SplitN(v, ":", 6)
	if len(fields) != 6 || fields[0] != "arn" || fields[1] != "aws" {
		return arn{}, false
	}
	return arn{service: fields[2], region: fields[3], account: fields[4], resource: fields[5]}, true
}

// arnRegion is the region of the ARN v, "" when it has none or is no ARN.
func arnRegion(v string) string {
	a, _ := parseARN(v)
	return a.region
}

// split cuts a's resource into its type, before the first / or :, and its
// name; with no separator the whole resource is the name.
func (a arn) split() (kind, name string, typed bool) {
	i := strings.IndexAny(a.resource, "/:")
	if i < 0 {
		return "", a.resource, false
	}
	return a.resource[:i], a.resource[i+1:], true
}

func (sc bindScope) arnReason(sh ARNShape, v string) string {
	a, ok := parseARN(v)
	if !ok {
		return "is not an ARN arn:aws:<service>:<region>:<account>:<resource>"
	}
	kind, name, typed := a.split()
	switch {
	case a.service != sh.Service:
		return fmt.Sprintf("names the service %q, not %q", a.service, sh.Service)
	case a.region != sc.region && (a.region != "" || !sc.noRegion):
		return fmt.Sprintf("is in the region %q, not the scenario's %q", a.region, sc.region)
	case a.account != sc.account:
		return fmt.Sprintf("is in the account %q, not the scenario's %q", a.account, sc.account)
	case sh.Resource == "" && typed:
		return fmt.Sprintf("has the resource type %q where the shape has none", kind)
	case sh.Resource != "" && (!typed || kind != sh.Resource):
		return fmt.Sprintf("has the resource %q, not one of type %q", a.resource, sh.Resource)
	case name == "":
		return "has an empty resource name"
	case sh.Name != nil:
		if reason := sc.check(sh.Name, name); reason != "" {
			return fmt.Sprintf("has the resource name %q, which %s", name, reason)
		}
	}
	return ""
}

func (sc bindScope) urlReason(sh URLShape, v string) string {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "is not an absolute http or https URL"
	}
	want, got := strings.Split(sh.Path, "/"), strings.Split(u.Path, "/")
	ok := len(want) == len(got)
	for i := 0; ok && i < len(want); i++ {
		switch want[i] {
		case "{account}":
			ok = got[i] == sc.account
		case "{name}":
			ok = got[i] != ""
		default:
			ok = got[i] == want[i]
		}
	}
	if !ok {
		return fmt.Sprintf("has the path %q, which does not match %q in the account %s", u.Path, sh.Path, sc.account)
	}
	return ""
}

// agree says why ours differs from aws where sh fixes the value, both
// having the shape, or "" when they agree: an ARN's resource name when sh
// gives the name no shape, and a URL's path.
func agree(sh Shape, aws, ours string) string {
	switch sh := sh.(type) {
	case ARNShape:
		a, _ := parseARN(aws)
		o, _ := parseARN(ours)
		_, an, _ := a.split()
		_, on, _ := o.split()
		if sh.Name == nil && an != on {
			return fmt.Sprintf("names the resource %q where AWS's names %q; the shape gives the name no shape, so the names must be equal", on, an)
		}
	case URLShape:
		if a, o := urlPath(aws), urlPath(ours); a != o {
			return fmt.Sprintf("has the path %q where AWS's has %q", o, a)
		}
	case UUIDShape, IDShape, nil:
	}
	return ""
}

// urlPath is the path of the URL v, which CheckBind has parsed already.
func urlPath(v string) string {
	u, err := url.Parse(v)
	if err != nil {
		return ""
	}
	return u.Path
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
