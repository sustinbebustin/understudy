package policycatalogue

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
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

// ParseType reads a page's Type spelling exactly, and false for any other
// text.
func ParseType(s string) (Type, bool) {
	for t := AWSManaged; t <= JobFunction; t++ {
		if s == t.String() {
			return t, true
		}
	}
	return 0, false
}

// String is the page's spelling of t, "" for the zero Type.
func (t Type) String() string {
	switch t {
	case AWSManaged:
		return "AWS managed policy"
	case ServiceLinkedRole:
		return "Service-linked role policy"
	case ServiceRole:
		return "Service role policy"
	case JobFunction:
		return "Job function policy"
	default:
		return ""
	}
}

// Path is the ARN path t is published under, "" for the zero Type.
func (t Type) Path() string {
	switch t {
	case AWSManaged:
		return "/"
	case ServiceLinkedRole:
		return "/aws-service-role/"
	case ServiceRole:
		return "/service-role/"
	case JobFunction:
		return "/job-function/"
	default:
		return ""
	}
}

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

// arnPrefix is every AWS managed policy ARN up to its path: the aws
// account's policy namespace in the aws partition (iam-evaluation.md 10.3).
const arnPrefix = "arn:aws:iam::aws:policy"

// dateLayout is createDate and updateDate in policies.json: RFC 3339 in UTC
// with a literal Z, so parsing with it refuses any other offset.
const dateLayout = "2006-01-02T15:04:05Z"

// The IAM model's policyNameType and policyVersionIdType. The patterns are
// ASCII only, so their rune counts are byte counts.
var (
	policyNameRE      = regexp.MustCompile(`^[\w+=,.@-]{1,128}$`)
	policyVersionIDRE = regexp.MustCompile(`^v[1-9]\d*(\.[A-Za-z0-9-]*)?$`)
)

// Catalogue is a validated set of AWS managed policies in listing order:
// by name regardless of case, then by ARN. A nil *Catalogue is the empty
// catalogue, and every method works on it.
type Catalogue struct {
	policies []Policy
	byARN    map[string]int
}

// New checks every policy and returns them as a catalogue in listing
// order, or nil and every violation as an *InvalidError, joined, in input
// order. The policydoc rules are not New's but the generator's -verify
// check, so a policydoc gap never stops the emulator from starting.
func New(policies []Policy) (*Catalogue, error) {
	var errs []error
	firstByARN := make(map[string]int, len(policies))
	firstByName := make(map[string]int, len(policies))
	for i := range policies {
		p := &policies[i]
		invalid := func(f Field, format string, args ...any) {
			errs = append(errs, &InvalidError{Index: i, Name: p.Name, Field: f, Reason: fmt.Sprintf(format, args...)})
		}
		check(p, invalid)
		if j, dup := firstByARN[p.ARN]; dup {
			invalid(FieldARN, "repeats the ARN of policy %d", j)
		} else {
			firstByARN[p.ARN] = i
		}
		// IAM's policy names are unique regardless of case in an account
		// (iam-sts-kms.md 2.1), here applied to the aws account.
		lower := strings.ToLower(p.Name)
		if j, dup := firstByName[lower]; dup {
			invalid(FieldPolicyName, "repeats the name of policy %d (%q); names are unique regardless of case", j, policies[j].Name)
		} else {
			firstByName[lower] = i
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	c := &Catalogue{policies: slices.Clone(policies), byARN: make(map[string]int, len(policies))}
	slices.SortFunc(c.policies, func(a, b Policy) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.ARN, b.ARN))
	})
	for i := range c.policies {
		c.byARN[c.policies[i].ARN] = i
	}
	return c, nil
}

// check reports each rule of New that p breaks, in field order, except
// uniqueness, which needs the other policies.
func check(p *Policy, invalid func(f Field, format string, args ...any)) {
	if !policyNameRE.MatchString(p.Name) {
		invalid(FieldPolicyName, "is not 1 to 128 letters, digits and _+=,.@- (the IAM model's policyNameType)")
	}
	if !aws.ValidIAMPath(p.Path) {
		invalid(FieldPath, "%q is not an IAM path: \"/\", or printable ASCII between slashes, at most 512 characters", p.Path)
	}
	if want := arnPrefix + p.Path + p.Name; p.ARN != want {
		invalid(FieldARN, "is %q; the path and name give %q", p.ARN, want)
	}
	switch {
	case p.Type.String() == "":
		invalid(FieldType, "Type(%d) is none of the four the guide publishes", uint8(p.Type))
	case p.Type.Path() != p.Path:
		invalid(FieldType, "%q policies are published under path %q, not %q (iam-evaluation.md 10.4)", p.Type, p.Type.Path(), p.Path)
	}
	if !policyVersionIDRE.MatchString(p.VersionID) {
		invalid(FieldDefaultVersionID, "%q is not v<N> with an optional .<suffix> (the IAM model's policyVersionIdType)", p.VersionID)
	}
	if reason := timeProblem(p.Created); reason != "" {
		invalid(FieldCreateDate, "%s", reason)
	}
	if reason := timeProblem(p.Updated); reason != "" {
		invalid(FieldUpdateDate, "%s", reason)
	}
	switch {
	case p.Document == "":
		invalid(FieldDocument, "is empty")
	case !jsontext.Value(p.Document).IsValid(jsontext.AllowDuplicateNames(true)):
		invalid(FieldDocument, "is not one JSON value")
	}
}

// timeProblem says why t is not a guide time, UTC to the minute
// (iam-evaluation.md 10.5), or "" when it is one.
func timeProblem(t time.Time) string {
	switch {
	case t.IsZero():
		return "is missing (the zero time)"
	case t.Location() != time.UTC:
		return fmt.Sprintf("%s is not in UTC", t)
	case t.Second() != 0 || t.Nanosecond() != 0:
		return t.Format(time.RFC3339Nano) + " is not a whole minute; the guide publishes minutes"
	default:
		return ""
	}
}

// Lookup returns the policy whose ARN is arn exactly, case-sensitively.
func (c *Catalogue) Lookup(arn string) (Policy, bool) {
	if c == nil {
		return Policy{}, false
	}
	i, ok := c.byARN[arn]
	if !ok {
		return Policy{}, false
	}
	return c.policies[i], true
}

// All yields every policy once, in listing order.
func (c *Catalogue) All() iter.Seq[Policy] {
	return func(yield func(Policy) bool) {
		if c == nil {
			return
		}
		for i := range c.policies {
			if !yield(c.policies[i]) {
				return
			}
		}
	}
}

// Len is the number of policies.
func (c *Catalogue) Len() int {
	if c == nil {
		return 0
	}
	return len(c.policies)
}

// fileJSON is policies.json. The pointers tell a missing or null member
// from an empty one, which encoding/json/v2 would both leave as the zero
// value.
type fileJSON struct {
	Policies *[]policyJSON `json:"policies"`
}

// policyJSON is one policy object of policies.json, members in file order.
type policyJSON struct {
	PolicyName       *string `json:"policyName"`
	ARN              *string `json:"arn"`
	Path             *string `json:"path"`
	Type             *string `json:"type"`
	Description      *string `json:"description"`
	DefaultVersionID *string `json:"defaultVersionId"`
	CreateDate       *string `json:"createDate"`
	UpdateDate       *string `json:"updateDate"`
	Document         *string `json:"document"`
}

func toJSON(p Policy) policyJSON {
	typ, created, updated := p.Type.String(), p.Created.Format(dateLayout), p.Updated.Format(dateLayout)
	return policyJSON{
		PolicyName: &p.Name, ARN: &p.ARN, Path: &p.Path, Type: &typ, Description: &p.Description,
		DefaultVersionID: &p.VersionID, CreateDate: &created, UpdateDate: &updated, Document: &p.Document,
	}
}

// fromJSON reads the policy at index i of policies.json, or reports every
// member that is missing, null or no value of its kind.
func fromJSON(i int, w policyJSON) (Policy, []error) {
	var errs []error
	text := func(f Field, v *string) string {
		if v == nil {
			errs = append(errs, fmt.Errorf("policy %d: member %q is missing or null, not a string", i, f))
			return ""
		}
		return *v
	}
	p := Policy{
		Name: text(FieldPolicyName, w.PolicyName), ARN: text(FieldARN, w.ARN), Path: text(FieldPath, w.Path),
		Description: text(FieldDescription, w.Description), VersionID: text(FieldDefaultVersionID, w.DefaultVersionID),
		Document: text(FieldDocument, w.Document),
	}
	typ := text(FieldType, w.Type)
	created, updated := text(FieldCreateDate, w.CreateDate), text(FieldUpdateDate, w.UpdateDate)
	if len(errs) > 0 {
		return Policy{}, errs
	}
	var ok bool
	if p.Type, ok = ParseType(typ); !ok {
		errs = append(errs, fmt.Errorf("policy %d (%q): type %q is none of the four the guide publishes", i, p.Name, typ))
	}
	date := func(f Field, s string) time.Time {
		t, err := time.Parse(dateLayout, s)
		if err != nil {
			errs = append(errs, fmt.Errorf("policy %d (%q): %s %q is not a UTC time of the form 2006-01-02T15:04:05Z", i, p.Name, f, s))
		}
		return t
	}
	p.Created, p.Updated = date(FieldCreateDate, created), date(FieldUpdateDate, updated)
	return p, errs
}

// Parse reads policies.json strictly: one object whose one member,
// policies, is an array of policy objects of exactly the nine string
// members, with no member repeated and nothing after it. Anything else is
// a *DecodeError. The policies must then pass New, an InvalidError's Index
// being the position in the array. Parse requires neither the canonical
// layout nor the canonical order; -verify does.
func Parse(data []byte) (*Catalogue, error) {
	var f fileJSON
	if err := json.Unmarshal(data, &f, json.RejectUnknownMembers(true)); err != nil {
		return nil, &DecodeError{Err: err}
	}
	if f.Policies == nil {
		return nil, &DecodeError{Err: errors.New(`the "policies" array is missing or null`)}
	}
	policies := make([]Policy, len(*f.Policies))
	var errs []error
	for i, w := range *f.Policies {
		p, perrs := fromJSON(i, w)
		policies[i] = p
		errs = append(errs, perrs...)
	}
	if len(errs) > 0 {
		return nil, &DecodeError{Err: errors.Join(errs...)}
	}
	return New(policies)
}

// Encode writes c as canonical policies.json: listing order, two-space
// indent, members in file order, no HTML escaping, UTF-8 as is and one
// trailing newline. A nil catalogue encodes as the empty catalogue.
func (c *Catalogue) Encode() ([]byte, error) {
	ps := make([]policyJSON, 0, c.Len())
	for p := range c.All() {
		ps = append(ps, toJSON(p))
	}
	b, err := json.Marshal(fileJSON{Policies: &ps}, jsontext.WithIndent("  "))
	if err != nil {
		return nil, fmt.Errorf("policycatalogue: encode policies.json: %w", err)
	}
	return append(b, '\n'), nil
}

// Loader returns the catalogue to serve. It may be called on every request
// that needs the catalogue and returns the same result each time.
type Loader func() (*Catalogue, error)

// embedded decompresses and parses the compiled-in catalogue on first use,
// so starting the emulator does not pay for it (ARCHITECTURE.md 5.7, the
// start and idle RSS budgets).
//
//nolint:gochecknoglobals // a lazily parsed immutable catalogue, not mutable state
var embedded = sync.OnceValues(func() (*Catalogue, error) {
	data, err := gunzipEmbedded()
	if err != nil {
		return nil, fmt.Errorf("policycatalogue: the embedded %s does not decompress, so no AWS managed policy can be served; 'go run ./internal/cmd/managedpolicies -verify' reports why: %w", awsmanagedpolicies.PoliciesGzipFile, err)
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("policycatalogue: the embedded %s does not load, so no AWS managed policy can be served; 'go run ./internal/cmd/managedpolicies -verify' reports why: %w", awsmanagedpolicies.PoliciesFile, err)
	}
	return c, nil
})

// maxSizeHint caps the capacity gunzip reserves from a gzip trailer, which
// a corrupt file could set to 4 GiB; the catalogue of 2026-10-26
// decompresses to 6.2 MB.
const maxSizeHint = 64 << 20

// gunzipEmbedded is policies.json, decompressed from the embedded
// awsmanagedpolicies.PoliciesGzipFile.
func gunzipEmbedded() ([]byte, error) {
	f, err := awsmanagedpolicies.FS().Open(awsmanagedpolicies.PoliciesGzipFile)
	if err != nil {
		return nil, err
	}
	data, err := gunzip(f)
	if err := errors.Join(err, f.Close()); err != nil {
		return nil, err
	}
	return data, nil
}

// gunzip decompresses f into one buffer reserved at the size its gzip
// trailer records, up to maxSizeHint, so the text is allocated once rather
// than regrown through a series of copies, each garbage once the next is
// made: the first Embedded call stays near the catalogue's own size on the
// RSS budgets (ARCHITECTURE.md 5.7). bytes.MinRead of spare room lets the
// read that meets the end go without growing the buffer, and the buffer
// starts from its own slice rather than Grow, whose append copies through a
// temporary that a -race build allocates too.
func gunzip(f fs.File) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, min(trailerSize(f), maxSizeHint)+bytes.MinRead))
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	_, err = buf.ReadFrom(zr)
	// zr.Close repeats a read error, so it is asked only after a clean read.
	if err == nil {
		err = zr.Close()
	}
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// trailerSize is the uncompressed size f's gzip trailer records (ISIZE, the
// last four bytes, RFC 1952 2.3.1), or 0 when f cannot be read at an offset
// or is too short to hold one. It only sizes a buffer: the gzip reader
// checks it against the text.
func trailerSize(f fs.File) int {
	ra, ok := f.(io.ReaderAt)
	info, err := f.Stat()
	if !ok || err != nil || info.Size() < 4 {
		return 0
	}
	var isize [4]byte
	if _, err := ra.ReadAt(isize[:], info.Size()-4); err != nil {
		return 0
	}
	return int(binary.LittleEndian.Uint32(isize[:]))
}

// Embedded is the Loader of the catalogue compiled into the binary as
// awsmanagedpolicies.PoliciesGzipFile, decompressed and parsed on the first
// call; every later call returns the same catalogue or error.
func Embedded() (*Catalogue, error) { return embedded() }

// Static is a Loader that returns c.
func Static(c *Catalogue) Loader {
	return func() (*Catalogue, error) { return c, nil }
}
