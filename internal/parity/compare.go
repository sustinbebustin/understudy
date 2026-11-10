package parity

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/normalize"
)

// DiffKind says how a compared field differs.
type DiffKind uint8

// The difference kinds.
const (
	// Changed is a field both sides hold with different values.
	Changed DiffKind = iota + 1
	// Missing is a field only AWS's response holds.
	Missing
	// Extra is a field only understudy's response holds.
	Extra
	// Reordered is the same members or child elements in another order.
	Reordered
)

// String returns "changed", "missing", "extra" or "reordered".
func (k DiffKind) String() string {
	switch k {
	case Changed:
		return "changed"
	case Missing:
		return "missing"
	case Extra:
		return "extra"
	case Reordered:
		return "reordered"
	}
	return fmt.Sprintf("DiffKind(%d)", uint8(k))
}

// Difference is one compared field where understudy's response differs
// from the recorded one, both normalized.
type Difference struct {
	Path Path
	Kind DiffKind
	// Expected is the recorded value, empty for Extra.
	Expected string
	// Actual is understudy's value, empty for Missing.
	Actual string
}

// String renders the difference on one line.
func (d Difference) String() string {
	switch d.Kind {
	case Changed:
		return fmt.Sprintf("%s: %s -> %s", d.Path, d.Expected, d.Actual)
	case Missing:
		return fmt.Sprintf("%s: missing (AWS has %s)", d.Path, d.Expected)
	case Extra:
		return fmt.Sprintf("%s: extra (understudy has %s)", d.Path, d.Actual)
	case Reordered:
		return fmt.Sprintf("%s: order %s -> %s", d.Path, d.Expected, d.Actual)
	}
	return fmt.Sprintf("%s: %v %s -> %s", d.Path, d.Kind, d.Expected, d.Actual)
}

// CompareOptions are a comparison's bound values and waivers.
type CompareOptions struct {
	// Bindings replace understudy's bound values with the AWS values
	// (Old: understudy's, New: AWS's), the bound normalizer.
	Bindings []normalize.Replacement
	// Waivers drop the differences at and below each path.
	Waivers []Path
}

// Compare returns the differences between the recorded response and
// understudy's, both normalized with normalize.Exchange, in comparison
// order, without those a waiver covers.
//
// The order is status, error/code, error/message, the compared headers by
// lower-case name, then the body in the recorded document's order, where an
// Extra member or element follows the recorded ones of its parent and a
// Reordered follows both. Understudy's side gets the bindings (Substitute,
// Old: understudy's value, New: AWS's) in its JSON string values, XML text
// and attribute values, header values and text bodies before the rest of
// the set.
func Compare(expected, actual Response, opts CompareOptions) []Difference {
	set := normalize.Exchange()
	c := comparison{
		aws:  side{set: set},
		ours: side{set: set, bindings: opts.Bindings},
	}
	exp, act := readBody(expected), readBody(actual)
	if expected.Status != 0 && expected.Status != actual.Status {
		c.add(statusPath, Changed, strconv.Itoa(expected.Status), strconv.Itoa(actual.Status))
	}
	if expected.Status >= 400 || actual.Status >= 400 {
		c.errorFields(expected, exp, actual, act)
	}
	c.headers(expected.Headers, actual.Headers)
	c.body(exp, act)
	return slices.DeleteFunc(c.diffs, func(d Difference) bool {
		return slices.ContainsFunc(opts.Waivers, func(w Path) bool { return w.Covers(d.Path) })
	})
}

// ComparedHeader reports whether the comparison compares the response
// header name: Content-Type, and x-amz- and x-amzn- headers that are not
// ids, dates or a body digest. The x-amzn- headers carry error identity
// (x-amzn-query-error, testdata/parity/sqs/R01/05-cmv-deleted.json).
func ComparedHeader(name string) bool {
	name = strings.ToLower(name)
	switch name {
	case "content-type":
		return true
	case "x-amzn-requestid", "x-amz-request-id", "x-amz-id-2", "x-amz-sts-extended-request-id",
		"x-amzn-trace-id", "x-amz-cf-id", "x-amz-date", "x-amz-crc32":
		return false
	}
	return strings.HasPrefix(name, "x-amz-") || strings.HasPrefix(name, "x-amzn-")
}

// side normalizes the values of one side of a comparison: the recorded
// response, without bindings, or understudy's, with them.
type side struct {
	set      normalize.Set
	bindings []normalize.Replacement
}

// text normalizes free text: a header value, an XML attribute value, a text
// body or a derived error field.
func (s side) text(v string) string {
	return s.set.Text(normalize.Substitute(s.bindings, v))
}

// scalar normalizes a JSON scalar or XML leaf text held by the member or
// element called name: a member rule masks it whole, else a string gets the
// text rules. A number keeps its literal.
func (s side) scalar(name string, kind normalize.Kind, v string) string {
	if kind == normalize.String {
		v = normalize.Substitute(s.bindings, v)
	}
	if masked, ok := s.set.Member(name, kind, v); ok {
		return masked
	}
	if kind == normalize.String {
		return s.set.Text(v)
	}
	return v
}

// comparison accumulates the differences of one Compare in order.
type comparison struct {
	aws, ours side
	diffs     []Difference
}

func (c *comparison) add(p Path, k DiffKind, expected, actual string) {
	c.diffs = append(c.diffs, Difference{Path: p, Kind: k, Expected: expected, Actual: actual})
}

// field compares one optional value: absent on one side is Missing or
// Extra.
func (c *comparison) field(p Path, exp string, expOK bool, act string, actOK bool) {
	switch {
	case expOK && actOK && exp != act:
		c.add(p, Changed, exp, act)
	case expOK && !actOK:
		c.add(p, Missing, exp, "")
	case !expOK && actOK:
		c.add(p, Extra, "", act)
	}
}

// errorFields compares the error code and message both sides carry,
// derived from the headers and body as errorCode and errorMessage read them
// (they are compared beside the body nodes that carry them).
func (c *comparison) errorFields(expected Response, exp body, actual Response, act body) {
	ec, ecOK := errorCode(expected.Headers, exp.doc)
	ac, acOK := errorCode(actual.Headers, act.doc)
	c.field(errorCodePath, c.aws.text(ec), ecOK, c.ours.text(ac), acOK)
	em, emOK := errorMessage(exp.doc)
	am, amOK := errorMessage(act.doc)
	c.field(errorMessagePath, c.aws.text(em), emOK, c.ours.text(am), amOK)
}

// errorCode reads a response's error code: the x-amzn-ErrorType header,
// else the JSON body's __type, code or Code, with any shape namespace (up to
// the last '#') and anything from ':' on dropped; else, in an XML body,
// Error/Code under the root element (ErrorResponse).
func errorCode(hs []Header, doc document) (string, bool) {
	if v, ok := firstHeader(hs, "x-amzn-errortype"); ok {
		return codePart(v), true
	}
	switch d := doc.(type) {
	case jsonDocument:
		for _, name := range [...]string{"__type", "code", "Code"} {
			if v, ok := d.root.stringMember(name); ok {
				return codePart(v), true
			}
		}
	case xmlDocument:
		return d.root.leafText("Error", "Code")
	case textDocument:
	}
	return "", false
}

// codePart is the error code of an x-amzn-ErrorType value or a JSON __type:
// without anything from ':' on (the header's documentation URL) and without
// the shape namespace up to the last '#'.
func codePart(v string) string {
	v, _, _ = strings.Cut(v, ":")
	if i := strings.LastIndexByte(v, '#'); i >= 0 {
		v = v[i+1:]
	}
	return v
}

// errorMessage reads a response's error message: the JSON body's message or
// Message, or Error/Message under an XML body's root element.
func errorMessage(doc document) (string, bool) {
	switch d := doc.(type) {
	case jsonDocument:
		for _, name := range [...]string{"message", "Message"} {
			if v, ok := d.root.stringMember(name); ok {
				return v, true
			}
		}
	case xmlDocument:
		return d.root.leafText("Error", "Message")
	case textDocument:
	}
	return "", false
}

// headers compares each recorded header ComparedHeader selects, by its
// first value, in lower-case name order. A header only understudy sends is
// not compared.
func (c *comparison) headers(expected, actual []Header) {
	var names []string
	for _, h := range expected {
		if name := strings.ToLower(h.Name); ComparedHeader(name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		exp, _ := firstHeader(expected, name)
		act, ok := firstHeader(actual, name)
		c.field(headerPath.child(name), c.aws.text(exp), true, c.ours.text(act), ok)
	}
}

// firstHeader is the first value of the header name, matched without case.
func firstHeader(hs []Header, name string) (string, bool) {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value, true
		}
	}
	return "", false
}

// body is one side's body as the comparison reads it.
type body struct {
	// form is the recorded body; understudy's is NoBody, TextBody or
	// BinaryBody.
	form Body
	// raw is the body's bytes, when the side holds them.
	raw   []byte
	known bool
	// doc is raw parsed by its format; nil when raw is not known.
	doc document
}

// readBody reads r's body and parses it by its format (formatOf).
func readBody(r Response) body {
	b := body{form: r.Body}
	switch f := r.Body.(type) {
	case NoBody:
		b.known = true
	case TextBody:
		b.raw, b.known = []byte(f.Text), true
	case BinaryBody:
		b.raw, b.known = f.Bytes, true
	case OmittedBody, UnrecordedBody, nil:
		return b
	}
	b.doc = parseDocument(firstContentType(r.Headers), string(b.raw))
	return b
}

func firstContentType(hs []Header) string {
	v, _ := firstHeader(hs, "Content-Type")
	return v
}

// body compares the bodies: a recorded body over 64 KiB by length and
// digest, bytes that are not UTF-8 exactly, an unrecorded body not at all,
// and text by its format (rule 8).
func (c *comparison) body(exp, act body) {
	switch f := exp.form.(type) {
	case UnrecordedBody:
		return
	case OmittedBody:
		got := "unknown"
		if act.known {
			sum := sha256.Sum256(act.raw)
			got = digestText(int64(len(act.raw)), hex.EncodeToString(sum[:]))
		}
		if want := digestText(f.Bytes, f.SHA256); got != want {
			c.add(bodyPath, Changed, want, got)
		}
		return
	case BinaryBody:
		if !act.known || !slices.Equal(f.Bytes, act.raw) {
			c.add(bodyPath, Changed, base64.StdEncoding.EncodeToString(f.Bytes), c.rawText(act))
		}
		return
	case NoBody, TextBody, nil:
	}
	if !act.known {
		c.add(bodyPath, Changed, c.aws.text(string(exp.raw)), c.rawText(act))
		return
	}
	if len(exp.raw) == 0 || len(act.raw) == 0 {
		if len(exp.raw) != len(act.raw) {
			c.add(bodyPath, Changed, c.aws.text(string(exp.raw)), c.ours.text(string(act.raw)))
		}
		return
	}
	c.document(exp.doc, act.doc)
}

// document compares two parsed bodies of the same format; bodies of
// different formats are one Changed at body.
func (c *comparison) document(exp, act document) {
	switch e := exp.(type) {
	case jsonDocument:
		if a, ok := act.(jsonDocument); ok {
			c.json(bodyPath, e.root.normalize("", c.aws), a.root.normalize("", c.ours))
			return
		}
	case xmlDocument:
		if a, ok := act.(xmlDocument); ok {
			c.xmlChildren(bodyPath, []xmlNode{e.root.normalize(c.aws)}, []xmlNode{a.root.normalize(c.ours)})
			return
		}
	case textDocument:
		if a, ok := act.(textDocument); ok {
			if et, at := c.aws.text(e.text), c.ours.text(a.text); et != at {
				c.add(bodyPath, Changed, et, at)
			}
			return
		}
	}
	c.add(bodyPath, Changed, c.aws.text(exp.source()), c.ours.text(act.source()))
}

// rawText renders understudy's body for a difference against a recorded
// body that is not text.
func (c *comparison) rawText(b body) string {
	if !b.known {
		return "a body understudy did not keep"
	}
	return base64.StdEncoding.EncodeToString(b.raw)
}

func digestText(n int64, sha string) string {
	return fmt.Sprintf("%d bytes, sha256 %s", n, sha)
}
