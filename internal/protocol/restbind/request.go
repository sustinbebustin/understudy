package restbind

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Location is where a head-bound member travels in a request.
type Location uint8

// Location values.
const (
	InLabel Location = iota + 1
	InQuery
	InHeader
)

// String returns "label", "query parameter" or "header".
func (l Location) String() string {
	switch l {
	case InLabel:
		return "label"
	case InQuery:
		return "query parameter"
	case InHeader:
		return "header"
	default:
		return fmt.Sprintf("Location(%d)", uint8(l))
	}
}

// BindError is a label, query or header value that does not parse as the
// type of the member it binds: a client mistake the codec answers with the
// service's malformed-request error.
type BindError struct {
	Location Location
	// Name is the wire name: the header or query parameter name, or the
	// label's member name.
	Name   string
	Value  string
	Reason string
}

func (e *BindError) Error() string {
	return fmt.Sprintf("the %s %s value %q cannot be read: %s", e.Name, e.Location, e.Value, e.Reason)
}

// ReadError is a request body that could not be read: the client went
// away, or the edge's body cap stopped the read (its error is Err's
// *awserr.BoundaryError).
type ReadError struct{ Err error }

func (e *ReadError) Error() string { return "restbind: reading the request body: " + e.Err.Error() }

// Unwrap returns the read failure.
func (e *ReadError) Unwrap() error { return e.Err }

// AWSError is the answer to the failed read: the edge's own error when its
// body cap stopped it (RequestEntityTooLarge), else RequestAborted, since
// the client went away mid-body.
func (e *ReadError) AWSError() *awserr.Error {
	if boundary, ok := errors.AsType[*awserr.BoundaryError](e.Err); ok && boundary.Err != nil {
		return boundary.Err
	}
	return awserr.RequestAborted()
}

// UsageError is generated code driving the deserializer or serializer in a
// way its schema does not allow: a bug, answered with InternalFailure.
type UsageError struct {
	Call   string
	Reason string
}

func (e *UsageError) Error() string { return "restbind: " + e.Call + ": " + e.Reason }

// Document is a body codec's deserializer over one body document: the
// input's unbound members, or a structure, union or document payload. At
// its root structure it skips members that carry an HTTP binding, whose
// values restbind reads from the head. End reports data after the root.
type Document interface {
	smithy.Deserializer
	End() error
}

// DocumentDecoder opens a Document over a non-empty body. payload is the
// structure, union or document payload member the body holds, nil when it
// holds the input's unbound members; restXml names its root after it.
type DocumentDecoder func(body []byte, payload *smithy.Member) Document

// PayloadMember returns s's httpPayload member.
func PayloadMember(s *smithy.Schema) (*smithy.Member, bool) {
	if s == nil {
		return nil, false
	}
	for _, m := range s.Members {
		if _, ok := m.HTTP.(smithy.HTTPPayload); ok {
			return m, true
		}
	}
	return nil, false
}

// HasRequestDocument reports whether a request of shape s carries a
// protocol document (a JSON object, an XML element) for its unbound
// members: s is a structure without a payload member that has no members
// at all or at least one member without an HTTP binding. An input whose
// members are all bound sends no body (restJson1 empty-input-output and
// malformed-content-type cases).
func HasRequestDocument(s *smithy.Schema) bool {
	return hasDocument(s, func(m *smithy.Member) bool { return m.HTTP != nil })
}

// HasResponseDocument reports whether a response of shape s carries a
// protocol document: s is a structure without a payload member that has at
// least one member the bindings BoundInResponse names leave in the
// document. Unlike a request, an output with no members at all sends no
// document (restXml EmptyInputAndEmptyOutput and NoInputAndOutput: "Empty
// output serializes no payload").
func HasResponseDocument(s *smithy.Schema) bool {
	return hasDocument(s, BoundInResponse) && len(s.Members) > 0
}

// BoundInResponse reports whether m's binding applies to a response: a
// header, prefix headers, the payload or the status. Smithy ignores
// httpLabel, httpQuery and httpQueryParams on an output structure, so such
// a member travels in the document (RestJsonIgnoreQueryParamsInResponse,
// IgnoreQueryParamsInResponse).
func BoundInResponse(m *smithy.Member) bool {
	switch m.HTTP.(type) {
	case smithy.HTTPHeader, smithy.HTTPPrefixHeaders, smithy.HTTPPayload, smithy.HTTPResponseCode:
		return true
	case smithy.HTTPLabel, smithy.HTTPQuery, smithy.HTTPQueryParams, nil:
	}
	return false
}

func hasDocument(s *smithy.Schema, bound func(*smithy.Member) bool) bool {
	if s == nil || s.Type != smithy.TypeStructure {
		return false
	}
	if _, ok := PayloadMember(s); ok {
		return false
	}
	return len(s.Members) == 0 || slices.ContainsFunc(s.Members, func(m *smithy.Member) bool { return !bound(m) })
}

// Streaming reports whether m is a @streaming blob, which travels as a
// smithy.Body instead of bytes.
func Streaming(m *smithy.Member) bool {
	return m.Target != nil && m.Target.Streaming && m.Target.Type == smithy.TypeBlob
}

// Decode binds r onto in one pass of in's generated Deserialize: label,
// query, query-params, header and prefix-header members from the head, the
// payload member from the body, and the unbound members through doc.
// Binding head and body in one pass matters: a generated Deserialize resets
// its required-member tracking each time it runs.
//
// A non-streaming body is read whole only when the input has a document
// or a payload member; a @streaming payload is handed over unread as a
// *Body. A body of only whitespace is no document; only an empty body is
// no blob or string payload. A trailer-bound header member the head does
// not carry is left unset; its value arrives as the body's trailer
// (Body.Deferred).
//
// Errors: *BindError for a head value that does not parse, *ReadError for
// a body that cannot be read, *PathError and *UsageError for bugs, and
// whatever doc's Document returns for a body that does not decode.
func Decode(r *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable, doc DocumentDecoder) error {
	d, err := newDeserializer(r, op, body, doc)
	if err != nil {
		return err
	}
	if err := in.Deserialize(d); err != nil {
		return err
	}
	for _, x := range []Document{d.doc, d.payDoc} {
		if x != nil {
			if err := x.End(); err != nil {
				return err
			}
		}
	}
	return nil
}

// source is where the current top-level member's value comes from.
type source uint8

const (
	srcNone       source = iota
	srcHead              // a label, query, header or prefix-header field
	srcDoc               // the document of unbound members
	srcPayloadDoc        // a structure, union or document payload
	srcPayloadRaw        // a blob or string payload
	srcStream            // a @streaming blob payload
)

// queued is a top-level member the request carries outside the document.
type queued struct {
	m   *smithy.Member
	src source
	f   *field
}

// field is a head-bound member's text: one value for a scalar, the
// elements of a list, or the entries of a prefix-header or query-params
// map.
type field struct {
	loc     Location
	name    string
	values  []string
	entries []entry
}

type entry struct {
	key    string
	value  string   // a map<string, string> value
	values []string // a map<string, list<string>> value
}

// headState is the position inside the current head field.
type headState struct {
	inList bool
	list   []string
	next   int // index after the current list element
	inMap  bool
	entry  int // index after the current map entry
}

// deserializer merges the head, the payload and the document into one
// smithy.Deserializer for the generated input type.
type deserializer struct {
	queue   []queued
	doc     Document
	docDone bool
	payDoc  Document
	raw     []byte
	stream  *Body
	// depth counts open structures and unions; 1 is the input itself.
	depth int
	cur   source
	f     *field
	hs    headState
}

func newDeserializer(r *http.Request, op *registry.Operation, body smithy.Body, doc DocumentDecoder) (*deserializer, error) {
	d := &deserializer{}
	input := op.Input
	if input == nil || input.Type != smithy.TypeStructure {
		return d, nil
	}
	h, err := newHead(r, op)
	if err != nil {
		return nil, err
	}
	for _, m := range input.Members {
		if f, ok := h.field(m); ok {
			d.queue = append(d.queue, queued{m: m, src: srcHead, f: f})
		}
	}
	payload, hasPayload := PayloadMember(input)
	if hasPayload && Streaming(payload) {
		d.stream = &Body{inner: body}
		d.queue = append(d.queue, queued{m: payload, src: srcStream})
		return d, nil
	}
	if !hasPayload && !HasRequestDocument(input) {
		return d, nil
	}
	data, err := readAll(body)
	if err != nil {
		return nil, err
	}
	// Whitespace alone is no document, but it is a blob or string payload:
	// only an empty body leaves a raw payload unset.
	blank := len(bytes.TrimSpace(data)) == 0
	switch {
	case !hasPayload:
		if !blank {
			d.doc = doc(data, nil)
		}
	case isDocumentShape(payload.Target):
		if !blank {
			d.payDoc = doc(data, payload)
			d.queue = append(d.queue, queued{m: payload, src: srcPayloadDoc})
		}
	case len(data) > 0:
		d.raw = data
		d.queue = append(d.queue, queued{m: payload, src: srcPayloadRaw})
	}
	return d, nil
}

// isDocumentShape reports whether a payload of shape s is a protocol
// document rather than raw bytes.
func isDocumentShape(s *smithy.Schema) bool {
	return s.Type == smithy.TypeStructure || s.Type == smithy.TypeUnion || s.Type == smithy.TypeDocument
}

func readAll(body smithy.Body) ([]byte, error) {
	rc, err := body.Open()
	if err != nil {
		return nil, &ReadError{Err: err}
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, &ReadError{Err: err}
	}
	return data, nil
}

// head is the request line and headers of one request.
type head struct {
	labels map[string]string
	query  map[string][]string
	header http.Header
}

// newHead reads r's labels by op's URI pattern and its query string. A
// label or query value with an invalid percent-escape is a *BindError.
func newHead(r *http.Request, op *registry.Operation) (head, error) {
	h := head{labels: map[string]string{}, header: r.Header}
	if op.HTTP != nil {
		labels, err := pathLabels(op.HTTP.Path, requestPath(r))
		if err != nil {
			return head{}, err
		}
		h.labels = labels
	}
	query, err := parseQuery(r.URL.RawQuery)
	if err != nil {
		return head{}, err
	}
	h.query = query
	return h, nil
}

// parseQuery reads a raw query string: pairs split on '&' only, so a ';'
// stays inside its key or value; key and value percent-decoded with '+' as
// a space; a pair without '=' has an empty value. url.ParseQuery drops a
// pair with an invalid escape or a ';', which would bind a value the
// client sent as absent; here it is a *BindError.
func parseQuery(raw string) (map[string][]string, error) {
	out := map[string][]string{}
	for pair := range strings.SplitSeq(raw, "&") {
		if pair == "" {
			continue
		}
		rawKey, rawValue, _ := strings.Cut(pair, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			return nil, &BindError{Location: InQuery, Name: rawKey, Value: rawValue, Reason: "its name has an invalid percent-escape"}
		}
		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			return nil, &BindError{Location: InQuery, Name: key, Value: rawValue, Reason: "it has an invalid percent-escape"}
		}
		out[key] = append(out[key], value)
	}
	return out, nil
}

// field returns m's head field when the request carries one.
func (h head) field(m *smithy.Member) (*field, bool) {
	switch loc := m.HTTP.(type) {
	case smithy.HTTPLabel:
		v, ok := h.labels[m.Name]
		return &field{loc: InLabel, name: m.Name, values: []string{v}}, ok
	case smithy.HTTPQuery:
		v, ok := h.query[loc.Name]
		return &field{loc: InQuery, name: loc.Name, values: v}, ok && len(v) > 0
	case smithy.HTTPQueryParams:
		f := &field{loc: InQuery, name: m.Name, entries: h.queryParams()}
		return f, len(f.entries) > 0
	case smithy.HTTPHeader:
		lines := h.header.Values(loc.Name)
		return &field{loc: InHeader, name: loc.Name, values: headerValues(m, lines)}, len(lines) > 0
	case smithy.HTTPPrefixHeaders:
		f := &field{loc: InHeader, name: loc.Prefix + "*", entries: h.prefixHeaders(loc.Prefix)}
		return f, len(f.entries) > 0
	case smithy.HTTPPayload, smithy.HTTPResponseCode, nil:
	}
	return nil, false
}

// headerValues is a header member's text: the first line for a scalar,
// every element of every line for a list, http-date elements paired back
// up.
func headerValues(m *smithy.Member, lines []string) []string {
	if m.Target.Type != smithy.TypeList || len(lines) == 0 {
		return lines
	}
	elem := m.Target.Members[0]
	httpDates := elem.Target.Type == smithy.TypeTimestamp && TimestampFormat(elem, smithy.HTTPDate) == smithy.HTTPDate
	var out []string
	for _, line := range lines {
		split := splitHeaderList(line)
		if httpDates {
			split = pairHTTPDates(split)
		}
		out = append(out, split...)
	}
	return out
}

// queryParams are every query parameter, sorted by name, including those
// an httpQuery member also binds: the server corpus expects them in the
// map (RestJsonServersPutAllQueryParamsInMap). The httpQuery-wins rule of
// wire-rest.md 1.3 is for serializing.
func (h head) queryParams() []entry {
	var out []entry
	for k, v := range h.query {
		if len(v) > 0 {
			out = append(out, entry{key: k, value: v[0], values: v})
		}
	}
	slices.SortFunc(out, func(a, b entry) int { return cmp.Compare(a.key, b.key) })
	return out
}

// prefixHeaders are the headers starting with prefix, compared without
// case, including those an httpHeader member also binds, as the server
// corpus expects (HttpEmptyPrefixHeadersRequestServer). Keys are the
// lowercase remainder of the name, as S3 stores user metadata; the lines
// of a repeated header are joined with commas.
func (h head) prefixHeaders(prefix string) []entry {
	prefix = strings.ToLower(prefix)
	var out []entry
	for name, lines := range h.header {
		key, ok := strings.CutPrefix(strings.ToLower(name), prefix)
		if !ok || key == "" {
			continue
		}
		var values []string
		for _, l := range lines {
			values = append(values, splitHeaderList(l)...)
		}
		out = append(out, entry{key: key, value: strings.Join(lines, ","), values: values})
	}
	slices.SortFunc(out, func(a, b entry) int { return cmp.Compare(a.key, b.key) })
	return out
}

func (d *deserializer) ReadStruct(s *smithy.Schema) error {
	if d.depth == 0 {
		d.depth = 1
		if d.doc != nil {
			return d.doc.ReadStruct(s)
		}
		return nil
	}
	b, err := d.body("ReadStruct")
	if err != nil {
		return err
	}
	if err := b.ReadStruct(s); err != nil {
		return err
	}
	d.depth++
	return nil
}

func (d *deserializer) ReadStructMember() (*smithy.Member, bool, error) {
	if d.depth > 1 {
		b, err := d.body("ReadStructMember")
		if err != nil {
			return nil, false, err
		}
		m, ok, err := b.ReadStructMember()
		if err == nil && !ok {
			d.depth--
		}
		return m, ok, err
	}
	if d.depth == 0 {
		return nil, false, &UsageError{Call: "ReadStructMember", Reason: "no structure is open"}
	}
	d.hs = headState{}
	if len(d.queue) > 0 {
		q := d.queue[0]
		d.queue = d.queue[1:]
		d.cur, d.f = q.src, q.f
		return q.m, true, nil
	}
	if d.doc != nil && !d.docDone {
		m, ok, err := d.doc.ReadStructMember()
		if err != nil || ok {
			d.cur = srcDoc
			return m, ok, err
		}
		d.docDone = true
	}
	d.depth, d.cur = 0, srcNone
	return nil, false, nil
}

func (d *deserializer) ReadUnion(s *smithy.Schema) (*smithy.Member, error) {
	b, err := d.body("ReadUnion")
	if err != nil {
		return nil, err
	}
	m, err := b.ReadUnion(s)
	if err == nil {
		d.depth++
	}
	return m, err
}

func (d *deserializer) CloseUnion() error {
	b, err := d.body("CloseUnion")
	if err != nil {
		return err
	}
	if err := b.CloseUnion(); err != nil {
		return err
	}
	d.depth--
	return nil
}

func (d *deserializer) ReadList(m *smithy.Member) error {
	if d.cur != srcHead {
		b, err := d.body("ReadList")
		if err != nil {
			return err
		}
		return b.ReadList(m)
	}
	switch {
	case d.hs.inMap && d.hs.entry > 0:
		d.hs.list = d.f.entries[d.hs.entry-1].values
	case !d.hs.inMap:
		d.hs.list = d.f.values
	default:
		return &UsageError{Call: "ReadList", Reason: "a map value read before its key"}
	}
	d.hs.inList, d.hs.next = true, 0
	return nil
}

func (d *deserializer) ReadListElement() (bool, error) {
	if d.cur != srcHead {
		b, err := d.body("ReadListElement")
		if err != nil {
			return false, err
		}
		return b.ReadListElement()
	}
	if !d.hs.inList {
		return false, &UsageError{Call: "ReadListElement", Reason: "no list is open"}
	}
	if d.hs.next == len(d.hs.list) {
		d.hs.inList = false
		return false, nil
	}
	d.hs.next++
	return true, nil
}

func (d *deserializer) ReadMap(m *smithy.Member) error {
	if d.cur != srcHead {
		b, err := d.body("ReadMap")
		if err != nil {
			return err
		}
		return b.ReadMap(m)
	}
	d.hs.inMap, d.hs.entry = true, 0
	return nil
}

func (d *deserializer) ReadMapKey() (key string, ok bool, err error) {
	if d.cur != srcHead {
		b, err := d.body("ReadMapKey")
		if err != nil {
			return "", false, err
		}
		return b.ReadMapKey()
	}
	if !d.hs.inMap {
		return "", false, &UsageError{Call: "ReadMapKey", Reason: "no map is open"}
	}
	if d.hs.entry == len(d.f.entries) {
		d.hs.inMap = false
		return "", false, nil
	}
	d.hs.entry++
	return d.f.entries[d.hs.entry-1].key, true, nil
}

// headValue parses the current head value with parse, wrapping a parse
// failure in a *BindError.
func headValue[T any](d *deserializer, parse func(string) (T, error)) (T, error) {
	var zero T
	s, err := d.text()
	if err != nil {
		return zero, err
	}
	v, err := parse(s)
	if err != nil {
		return zero, &BindError{Location: d.f.loc, Name: d.f.name, Value: s, Reason: err.Error()}
	}
	return v, nil
}

// scalar reads the current member as text from the head or as a raw
// payload, or returns use=false when a body document holds it.
func scalar[T any](d *deserializer, call string, parse func(string) (T, error)) (v T, use bool, err error) {
	switch d.cur {
	case srcHead:
		v, err = headValue(d, parse)
		return v, true, err
	case srcPayloadRaw:
		v, err = parse(string(d.raw))
		return v, true, err
	case srcStream:
		return v, true, &UsageError{Call: call, Reason: "a @streaming payload is read with ReadBody"}
	case srcNone, srcDoc, srcPayloadDoc:
	}
	return v, false, nil
}

func (d *deserializer) ReadString(m *smithy.Member) (string, error) {
	parse := func(s string) (string, error) { return s, nil }
	if d.cur == srcHead && d.f.loc == InHeader && m.Target != nil && m.Target.MediaType != "" {
		// A header string with a mediaType trait travels base64-encoded.
		parse = func(s string) (string, error) {
			b, err := ParseBase64(s)
			return string(b), err
		}
	}
	if v, use, err := scalar(d, "ReadString", parse); use {
		return v, err
	}
	b, err := d.body("ReadString")
	if err != nil {
		return "", err
	}
	return b.ReadString(m)
}

func (d *deserializer) ReadBool(m *smithy.Member) (bool, error) {
	if v, use, err := scalar(d, "ReadBool", ParseBool); use {
		return v, err
	}
	b, err := d.body("ReadBool")
	if err != nil {
		return false, err
	}
	return b.ReadBool(m)
}

func (d *deserializer) ReadInt8(m *smithy.Member) (int8, error) {
	v, err := d.readInt(m, 8)
	return int8(v), err //nolint:gosec // readInt checked the range for 8 bits
}

func (d *deserializer) ReadInt16(m *smithy.Member) (int16, error) {
	v, err := d.readInt(m, 16)
	return int16(v), err //nolint:gosec // readInt checked the range for 16 bits
}

func (d *deserializer) ReadInt32(m *smithy.Member) (int32, error) {
	v, err := d.readInt(m, 32)
	return int32(v), err //nolint:gosec // readInt checked the range for 32 bits
}

func (d *deserializer) ReadInt64(m *smithy.Member) (int64, error) {
	return d.readInt(m, 64)
}

func (d *deserializer) ReadFloat32(m *smithy.Member) (float32, error) {
	if v, use, err := scalar(d, "ReadFloat32", func(s string) (float64, error) { return ParseFloat(s, 32) }); use {
		return float32(v), err
	}
	b, err := d.body("ReadFloat32")
	if err != nil {
		return 0, err
	}
	return b.ReadFloat32(m)
}

func (d *deserializer) ReadFloat64(m *smithy.Member) (float64, error) {
	if v, use, err := scalar(d, "ReadFloat64", func(s string) (float64, error) { return ParseFloat(s, 64) }); use {
		return v, err
	}
	b, err := d.body("ReadFloat64")
	if err != nil {
		return 0, err
	}
	return b.ReadFloat64(m)
}

func (d *deserializer) ReadBigInt(m *smithy.Member) (*big.Int, error) {
	if v, use, err := scalar(d, "ReadBigInt", ParseBigInt); use {
		return v, err
	}
	b, err := d.body("ReadBigInt")
	if err != nil {
		return nil, err
	}
	return b.ReadBigInt(m)
}

func (d *deserializer) ReadBigDecimal(m *smithy.Member) (smithy.BigDecimal, error) {
	if v, use, err := scalar(d, "ReadBigDecimal", ParseBigDecimal); use {
		return v, err
	}
	b, err := d.body("ReadBigDecimal")
	if err != nil {
		return "", err
	}
	return b.ReadBigDecimal(m)
}

func (d *deserializer) ReadTimestamp(m *smithy.Member) (time.Time, error) {
	if d.cur == srcHead {
		def := smithy.DateTime
		if d.f.loc == InHeader {
			def = smithy.HTTPDate
		}
		format := TimestampFormat(m, def)
		return headValue(d, func(s string) (time.Time, error) { return ParseTimestamp(s, format) })
	}
	b, err := d.body("ReadTimestamp")
	if err != nil {
		return time.Time{}, err
	}
	return b.ReadTimestamp(m)
}

func (d *deserializer) ReadBlob(m *smithy.Member) ([]byte, error) {
	switch d.cur {
	case srcHead:
		return headValue(d, ParseBase64)
	case srcPayloadRaw:
		return d.raw, nil
	case srcNone, srcDoc, srcPayloadDoc, srcStream:
	}
	b, err := d.body("ReadBlob")
	if err != nil {
		return nil, err
	}
	return b.ReadBlob(m)
}

func (d *deserializer) ReadDocument(m *smithy.Member) (document.Value, error) {
	b, err := d.body("ReadDocument")
	if err != nil {
		return nil, err
	}
	return b.ReadDocument(m)
}

func (d *deserializer) ReadNull(m *smithy.Member) (bool, error) {
	if d.cur == srcHead {
		return false, nil
	}
	b, err := d.body("ReadNull")
	if err != nil {
		return false, err
	}
	return b.ReadNull(m)
}

func (d *deserializer) ReadBody(*smithy.Member) (smithy.Body, error) {
	if d.cur != srcStream {
		return nil, &UsageError{Call: "ReadBody", Reason: "the current member is not a @streaming payload"}
	}
	return d.stream, nil
}

// body returns the Document the current member's value is read from, or a
// usage error naming call.
func (d *deserializer) body(call string) (Document, error) {
	switch {
	case d.cur == srcDoc && d.doc != nil:
		return d.doc, nil
	case d.cur == srcPayloadDoc && d.payDoc != nil:
		return d.payDoc, nil
	default:
		return nil, &UsageError{Call: call, Reason: "the current member is not read from a body document"}
	}
}

// text is the current head value: a list element, a map value or the
// scalar.
func (d *deserializer) text() (string, error) {
	switch {
	case d.hs.inList && d.hs.next > 0:
		return d.hs.list[d.hs.next-1], nil
	case d.hs.inMap && d.hs.entry > 0:
		return d.f.entries[d.hs.entry-1].value, nil
	case !d.hs.inList && !d.hs.inMap && len(d.f.values) > 0:
		return d.f.values[0], nil
	default:
		return "", &UsageError{Call: "Read", Reason: "no head value is positioned"}
	}
}

func (d *deserializer) readInt(m *smithy.Member, bits int) (int64, error) {
	if v, use, err := scalar(d, "ReadInt", func(s string) (int64, error) { return ParseInt(s, bits) }); use {
		return v, err
	}
	b, err := d.body("ReadInt")
	if err != nil {
		return 0, err
	}
	switch bits {
	case 8:
		v, err := b.ReadInt8(m)
		return int64(v), err
	case 16:
		v, err := b.ReadInt16(m)
		return int64(v), err
	case 32:
		v, err := b.ReadInt32(m)
		return int64(v), err
	default:
		return b.ReadInt64(m)
	}
}
