package protocoltest

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"maps"
	"math"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// MismatchError is a response that differs from what a case expects: where, and
// the expected and actual values there.
type MismatchError struct {
	// Where names the part of the response: "status", "header X-Foo",
	// "body $.a[2]".
	Where     string
	Want, Got string
}

func (m *MismatchError) Error() string {
	return fmt.Sprintf("%s: got %s, want %s", m.Where, m.Got, m.Want)
}

// maxCompareBytes bounds the XML bodies CompareBody parses.
const maxCompareBytes = 16 << 20

// CompareBody compares a response body with a case's expected body as the
// case's media type says (smithy-protocol-tests.md 4.2): JSON as values,
// with object member order ignored and numbers compared by value; XML on
// names resolved to namespace URI and local name as equalElement says,
// prefixes ignored, shape telling a map's entries, which
// have no order, from a list's members, which do; a form as its set of
// keys and their values; CBOR structurally, the expected body being base64
// as the corpus writes binary bodies; anything else byte for byte. shape
// is the schema of the body's shape, or nil when the body holds no map.
// Every difference is a *MismatchError.
func CompareBody(mediaType string, shape *smithy.Schema, want, got []byte) error {
	switch essence(mediaType) {
	case "application/json", "application/x-amz-json-1.0", "application/x-amz-json-1.1":
		return compareJSON(want, got)
	case "application/xml", "text/xml":
		return compareXML(xmlMapEntries(shape), want, got)
	case "application/x-www-form-urlencoded":
		return compareForm(want, got)
	case "application/cbor":
		return compareCBOR(want, got)
	default:
		if !bytes.Equal(want, got) {
			return &MismatchError{Where: "body", Want: fmt.Sprintf("%q", want), Got: fmt.Sprintf("%q", got)}
		}
		return nil
	}
}

// essence is a media type without its parameters, lowercase.
func essence(mediaType string) string {
	if mt, _, err := mime.ParseMediaType(mediaType); err == nil {
		return mt
	}
	return strings.ToLower(strings.TrimSpace(mediaType))
}

// CompareHeaders checks that every expected header is present with the
// expected value; extra headers are allowed. Names match case-insensitively
// and a header sent several times is compared joined with ", ".
func CompareHeaders(want map[string]string, got http.Header) error {
	for _, name := range slices.Sorted(maps.Keys(want)) {
		values := got.Values(name)
		if len(values) == 0 {
			return &MismatchError{Where: "header " + name, Want: fmt.Sprintf("%q", want[name]), Got: "no header"}
		}
		if v := strings.Join(values, ", "); v != want[name] {
			return &MismatchError{Where: "header " + name, Want: fmt.Sprintf("%q", want[name]), Got: fmt.Sprintf("%q", v)}
		}
	}
	return nil
}

// CompareHeaderPresence checks that every forbidden header is absent and
// every required one present with any value.
func CompareHeaderPresence(forbid, require []string, got http.Header) error {
	for _, name := range forbid {
		if v := got.Values(name); len(v) > 0 {
			return &MismatchError{Where: "header " + name, Want: "no header", Got: fmt.Sprintf("%q", strings.Join(v, ", "))}
		}
	}
	for _, name := range require {
		if len(got.Values(name)) == 0 {
			return &MismatchError{Where: "header " + name, Want: "the header with any value", Got: "no header"}
		}
	}
	return nil
}

func compareJSON(want, got []byte) error {
	w, err := readJSON(want)
	if err != nil {
		return &MismatchError{Where: "body", Want: "JSON", Got: "an expected body that is not JSON: " + err.Error()}
	}
	g, err := readJSON(got)
	if err != nil {
		return &MismatchError{Where: "body", Want: "JSON", Got: fmt.Sprintf("%q (%v)", got, err)}
	}
	return equalDocument("body $", w, g)
}

func readJSON(b []byte) (document.Value, error) {
	d := jsonx.NewDecoder(b)
	v, err := d.ReadValue()
	if err != nil {
		return nil, err
	}
	return v, d.End()
}

// equalDocument compares two JSON values: objects as member sets, arrays
// in order, numbers by value.
func equalDocument(where string, want, got document.Value) error {
	mismatch := func() error {
		return &MismatchError{Where: where, Want: describeDocument(want), Got: describeDocument(got)}
	}
	switch w := want.(type) {
	case document.Map:
		g, ok := got.(document.Map)
		if !ok {
			return mismatch()
		}
		return equalMembers(where, w, g)
	case document.List:
		g, ok := got.(document.List)
		if !ok || len(g) != len(w) {
			return mismatch()
		}
		for i := range w {
			if err := equalDocument(fmt.Sprintf("%s[%d]", where, i), w[i], g[i]); err != nil {
				return err
			}
		}
		return nil
	case document.Number:
		g, ok := got.(document.Number)
		if !ok || !equalNumbers(string(w), string(g)) {
			return mismatch()
		}
		return nil
	case document.Null, document.Bool, document.String:
	}
	if want != got {
		return mismatch()
	}
	return nil
}

func equalMembers(where string, want, got document.Map) error {
	gotByKey := make(map[string]document.Value, len(got))
	for _, e := range got {
		gotByKey[e.Key] = e.Value
	}
	for _, e := range want {
		g, ok := gotByKey[e.Key]
		if !ok {
			return &MismatchError{Where: where + "." + e.Key, Want: describeDocument(e.Value), Got: "no member"}
		}
		if err := equalDocument(where+"."+e.Key, e.Value, g); err != nil {
			return err
		}
		delete(gotByKey, e.Key)
	}
	for _, key := range slices.Sorted(maps.Keys(gotByKey)) {
		return &MismatchError{Where: where + "." + key, Want: "no member", Got: describeDocument(gotByKey[key])}
	}
	return nil
}

// equalNumbers compares two JSON numbers by value, so 1, 1.0 and 1e0 are
// equal; 256 bits of precision keep big integers and decimals exact.
func equalNumbers(a, b string) bool {
	if a == b {
		return true
	}
	x, _, errA := big.ParseFloat(a, 10, 256, big.ToNearestEven)
	y, _, errB := big.ParseFloat(b, 10, 256, big.ToNearestEven)
	return errA == nil && errB == nil && x.Cmp(y) == 0
}

func describeDocument(v document.Value) string {
	switch v := v.(type) {
	case document.Map:
		keys := make([]string, 0, len(v))
		for _, e := range v {
			keys = append(keys, e.Key)
		}
		return fmt.Sprintf("an object with members %v", keys)
	case document.List:
		return fmt.Sprintf("an array of %d", len(v))
	case document.String:
		return fmt.Sprintf("%q", string(v))
	case document.Number:
		return string(v)
	case document.Bool:
		return strconv.FormatBool(bool(v))
	case document.Null:
		return "null"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func compareXML(entries mapEntries, want, got []byte) error {
	w, err := decodeXML(want)
	if err != nil {
		return &MismatchError{Where: "body", Want: "XML", Got: "an expected body that is not XML: " + err.Error()}
	}
	g, err := decodeXML(got)
	if err != nil {
		return &MismatchError{Where: "body", Want: "XML", Got: fmt.Sprintf("%q (%v)", got, err)}
	}
	return entries.equalElement("body /", w, g)
}

// mapEntries are the local names of the XML elements that hold a map's
// entries; same-named siblings by any other name are list members.
type mapEntries map[string]bool

// mapEntryElement names each entry of a map that is not flattened.
const mapEntryElement = "entry"

// xmlMapEntries returns the names of the elements holding the entries of
// every map reachable from shape: entry for a wrapped map, the member's
// own name for a map flattened into its structure. Two shapes may give one
// name to a map and a list; the list's members then compare unordered too.
func xmlMapEntries(shape *smithy.Schema) mapEntries {
	out := mapEntries{}
	seen := map[*smithy.Schema]bool{}
	var walk func(*smithy.Schema)
	walk = func(s *smithy.Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		structured := s.Type == smithy.TypeStructure || s.Type == smithy.TypeUnion
		for _, m := range s.Members {
			if m.Target == nil {
				continue
			}
			if m.Target.Type == smithy.TypeMap {
				switch {
				case structured && m.XMLFlattened && m.XMLName != "":
					out[m.XMLName] = true
				case structured && m.XMLFlattened:
					out[m.Name] = true
				default:
					out[mapEntryElement] = true
				}
			}
			walk(m.Target)
		}
	}
	walk(shape)
	return out
}

// Elements whose content a server mints rather than serializes from the
// case: the request id, and the awsQuery ResponseMetadata wrapper around it.
const (
	requestIDElement        = "RequestId"
	responseMetadataElement = "ResponseMetadata"
)

// equalElement compares two elements on their names resolved to namespace
// URI and local name, whatever prefixes bind them: attributes as a set,
// differently named children in any order, and text (trimmed only around
// child elements, where it is indentation). Same-named children compare
// in order when they are a list's members and as a multiset when they are
// a map's entries, which have no order on the wire. A RequestId's text is
// minted by the server, so only its presence is compared, and a
// ResponseMetadata child the expected body lacks is ignored: AWS sends one
// on every awsQuery response and clients skip it.
func (e mapEntries) equalElement(where string, want, got *xmlElement) error {
	where += want.Name.Local
	if want.Name != got.Name {
		return &MismatchError{Where: where, Want: "<" + qualified(want.Name) + ">", Got: "<" + qualified(got.Name) + ">"}
	}
	if !sameAttrs(want.Attrs, got.Attrs) {
		return &MismatchError{Where: where + " attributes", Want: fmt.Sprint(want.Attrs), Got: fmt.Sprint(got.Attrs)}
	}
	wantText, gotText := want.Text, got.Text
	if len(want.Children) > 0 || len(got.Children) > 0 {
		wantText, gotText = strings.TrimSpace(wantText), strings.TrimSpace(gotText)
	}
	if wantText != gotText && want.Name.Local != requestIDElement {
		return &MismatchError{Where: where + " text", Want: fmt.Sprintf("%q", wantText), Got: fmt.Sprintf("%q", gotText)}
	}
	gotChildren := got.Children
	if _, ok := want.child(responseMetadataElement); !ok {
		gotChildren = slices.DeleteFunc(slices.Clone(gotChildren), func(n *xmlElement) bool { return n.Name.Local == responseMetadataElement })
	}
	wc, gc := byName(want.Children), byName(gotChildren)
	if len(wc) != len(gc) {
		return &MismatchError{Where: where + " children", Want: childNames(wc), Got: childNames(gc)}
	}
	for start := 0; start < len(wc); {
		end := start + 1
		for end < len(wc) && wc[end].Name == wc[start].Name {
			end++
		}
		if err := e.equalGroup(where+"/", wc[start:end], gc[start:end]); err != nil {
			return err
		}
		start = end
	}
	return nil
}

// equalGroup compares same-named siblings in order, and a map's entries
// that differ in order as a multiset. The in-order mismatch is the one
// reported.
func (e mapEntries) equalGroup(where string, want, got []*xmlElement) error {
	var first error
	for i := range want {
		if err := e.equalElement(where, want[i], got[i]); err != nil {
			first = err
			break
		}
	}
	if first == nil || len(want) == 1 || !e[want[0].Name.Local] {
		return first
	}
	left := slices.Clone(got)
	for _, w := range want {
		i := slices.IndexFunc(left, func(g *xmlElement) bool { return e.equalElement(where, w, g) == nil })
		if i < 0 {
			return first
		}
		left = slices.Delete(left, i, i+1)
	}
	return nil
}

func sameAttrs(a, b []xml.Attr) bool {
	sorted := func(attrs []xml.Attr) []xml.Attr {
		return slices.SortedFunc(slices.Values(attrs), func(x, y xml.Attr) int {
			return cmp.Or(cmp.Compare(x.Name.Space, y.Name.Space), cmp.Compare(x.Name.Local, y.Name.Local), cmp.Compare(x.Value, y.Value))
		})
	}
	return slices.Equal(sorted(a), sorted(b))
}

// byName orders children by resolved name, keeping same-named ones in
// document order.
func byName(children []*xmlElement) []*xmlElement {
	out := slices.Clone(children)
	slices.SortStableFunc(out, func(x, y *xmlElement) int {
		return cmp.Or(cmp.Compare(x.Name.Local, y.Name.Local), cmp.Compare(x.Name.Space, y.Name.Space))
	})
	return out
}

func childNames(children []*xmlElement) string {
	names := make([]string, 0, len(children))
	for _, c := range children {
		names = append(names, qualified(c.Name))
	}
	return fmt.Sprint(names)
}

func compareForm(want, got []byte) error {
	w, err := url.ParseQuery(string(want))
	if err != nil {
		return &MismatchError{Where: "body", Want: "a form", Got: "an expected body that is not a form: " + err.Error()}
	}
	g, err := url.ParseQuery(string(got))
	if err != nil {
		return &MismatchError{Where: "body", Want: "a form", Got: fmt.Sprintf("%q (%v)", got, err)}
	}
	for _, key := range slices.Sorted(maps.Keys(w)) {
		if !slices.Equal(w[key], g[key]) {
			return &MismatchError{Where: "body key " + key, Want: fmt.Sprint(w[key]), Got: fmt.Sprint(g[key])}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(g)) {
		if _, ok := w[key]; !ok {
			return &MismatchError{Where: "body key " + key, Want: "no key", Got: fmt.Sprint(g[key])}
		}
	}
	return nil
}

func compareCBOR(want, got []byte) error {
	raw, err := base64.StdEncoding.DecodeString(string(want))
	if err != nil {
		return &MismatchError{Where: "body", Want: "CBOR", Got: "an expected body that is not base64: " + err.Error()}
	}
	if len(raw) == 0 && len(got) == 0 {
		return nil
	}
	w, err := cbor.Decode(raw)
	if err != nil {
		return &MismatchError{Where: "body", Want: "CBOR", Got: "an expected body that is not CBOR: " + err.Error()}
	}
	g, err := cbor.Decode(got)
	if err != nil {
		return &MismatchError{Where: "body", Want: "CBOR", Got: fmt.Sprintf("% x (%v)", got, err)}
	}
	return equalCBOR("body $", w, g)
}

// equalCBOR compares two CBOR values: maps as key sets, arrays in order,
// floats by value whatever their width (NaN equals NaN), everything else
// exactly.
func equalCBOR(where string, want, got cbor.Value) error {
	mismatch := &MismatchError{Where: where, Want: fmt.Sprintf("%#v", want), Got: fmt.Sprintf("%#v", got)}
	switch w := want.(type) {
	case cbor.Map:
		g, ok := got.(cbor.Map)
		if !ok || len(g) != len(w) {
			return mismatch
		}
		return equalCBORMap(where, w, g)
	case cbor.Array:
		g, ok := got.(cbor.Array)
		if !ok || len(g) != len(w) {
			return mismatch
		}
		for i := range w {
			if err := equalCBOR(fmt.Sprintf("%s[%d]", where, i), w[i], g[i]); err != nil {
				return err
			}
		}
		return nil
	case cbor.Tag:
		if g, ok := got.(cbor.Tag); ok && g.Number == w.Number {
			return equalCBOR(where, w.Content, g.Content)
		}
		return mismatch
	case cbor.Bool, cbor.Bytes, cbor.Float32, cbor.Float64, cbor.NegInt, cbor.Null, cbor.Text, cbor.Uint, cbor.Undefined:
	}
	if !equalCBORScalar(want, got) {
		return mismatch
	}
	return nil
}

// equalCBORScalar compares two values that are neither maps, arrays nor
// tags.
func equalCBORScalar(want, got cbor.Value) bool {
	switch w := want.(type) {
	case cbor.Bytes:
		g, ok := got.(cbor.Bytes)
		return ok && bytes.Equal(w, g)
	case cbor.Float32, cbor.Float64:
		wf, _ := asFloat(want)
		gf, ok := asFloat(got)
		return ok && (wf == gf || math.IsNaN(wf) && math.IsNaN(gf))
	case cbor.Bool, cbor.NegInt, cbor.Null, cbor.Text, cbor.Uint, cbor.Undefined, cbor.Array, cbor.Map, cbor.Tag:
	}
	return want == got
}

func equalCBORMap(where string, want, got cbor.Map) error {
	for _, e := range want {
		at := where + "." + e.Key
		i := slices.IndexFunc(got, func(x cbor.Entry) bool { return x.Key == e.Key })
		if i < 0 {
			return &MismatchError{Where: at, Want: fmt.Sprintf("%#v", e.Value), Got: "no entry"}
		}
		if err := equalCBOR(at, e.Value, got[i].Value); err != nil {
			return err
		}
	}
	return nil
}

func asFloat(v cbor.Value) (float64, bool) {
	switch v := v.(type) {
	case cbor.Float32:
		return float64(v), true
	case cbor.Float64:
		return float64(v), true
	case cbor.Array, cbor.Bool, cbor.Bytes, cbor.Map, cbor.NegInt, cbor.Null, cbor.Tag, cbor.Text, cbor.Uint, cbor.Undefined:
	}
	return 0, false
}
