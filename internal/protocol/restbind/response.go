package restbind

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// ResponseBody is what an encoded response's body carries; the codec
// picks the Content-Type from it.
//
//sumtype:decl
type ResponseBody interface{ isResponseBody() }

// NoBody is a response without a body: a Unit output, or one the
// protocol's document rule gives no document and no payload member set.
type NoBody struct{}

// DocumentBody is a protocol document: the unbound members, or a
// structure, union or document payload.
type DocumentBody struct{ Bytes []byte }

// BlobBody is a blob payload; MediaType is its mediaType trait, empty when
// it has none.
type BlobBody struct {
	Bytes     []byte
	MediaType string
}

// TextBody is a string payload; MediaType is its mediaType trait, empty
// when it has none.
type TextBody struct {
	Text      string
	MediaType string
}

// StreamBody is a @streaming blob payload, streamed from Source.
type StreamBody struct {
	Source    smithy.BodySource
	MediaType string
}

func (NoBody) isResponseBody()       {}
func (DocumentBody) isResponseBody() {}
func (BlobBody) isResponseBody()     {}
func (TextBody) isResponseBody()     {}
func (StreamBody) isResponseBody()   {}

// Encoded is an output split by its HTTP bindings.
type Encoded struct {
	Status int
	Header http.Header
	Body   ResponseBody
}

// Finish returns the body to send and completes the head: Content-Type,
// unless a member bound it, from the body kind (documentType for a
// document, the payload's mediaType, else application/octet-stream for
// blobs and streams and text/plain for strings), and Content-Length,
// unless a member bound it, for a body of known size. A status that allows
// no body (1xx, 204, 304) sends none and adds neither header. A stream
// that cannot be opened is the error.
func (e Encoded) Finish(documentType string) (io.Reader, error) {
	if !BodyAllowed(e.Status) {
		return http.NoBody, nil
	}
	var (
		r         io.Reader
		size      = int64(-1)
		mediaType string
	)
	switch b := e.Body.(type) {
	case NoBody:
		return http.NoBody, nil
	case DocumentBody:
		r, size, mediaType = bytes.NewReader(b.Bytes), int64(len(b.Bytes)), documentType
	case BlobBody:
		r, size, mediaType = bytes.NewReader(b.Bytes), int64(len(b.Bytes)), cmp.Or(b.MediaType, "application/octet-stream")
	case TextBody:
		r, size, mediaType = strings.NewReader(b.Text), int64(len(b.Text)), cmp.Or(b.MediaType, "text/plain")
	case StreamBody:
		rc, err := b.Source.Range(0, b.Source.Size())
		if err != nil {
			return nil, err
		}
		r, size, mediaType = rc, b.Source.Size(), cmp.Or(b.MediaType, "application/octet-stream")
	}
	if e.Header.Get("Content-Type") == "" {
		e.Header.Set("Content-Type", mediaType)
	}
	if e.Header.Get("Content-Length") == "" && size >= 0 {
		e.Header.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	return r, nil
}

// BodyAllowed reports whether a response with this status may carry a
// body (RFC 9110 6.4.1); net/http refuses a body for the others.
func BodyAllowed(status int) bool {
	return status >= http.StatusOK && status != http.StatusNoContent && status != http.StatusNotModified
}

// DocumentWriter is a body codec's serializer for one document. The first
// WriteStruct, WriteUnion or WriteDocument opens its root.
type DocumentWriter interface {
	smithy.Serializer
	Bytes() ([]byte, error)
}

// DocumentEncoder opens a DocumentWriter.
type DocumentEncoder func() DocumentWriter

// Encode splits out, the handler's output for op, by its HTTP bindings:
// header and prefix-header members into headers (timestamps as http-date
// unless a timestampFormat trait says otherwise, mediaType strings and
// blobs as base64, lists comma-joined with quoting), an httpResponseCode
// member into the status, the payload member into the body, and every
// unbound member into one document written through doc. hasDocument is
// the protocol's rule for whether the output shape has that document at
// all: restXml sends none when every member is bound (HasResponseDocument),
// restJson1 sends {}. A top-level httpHeader member wins over a
// prefix-header entry for the same header, whichever is written first.
// status is the status the pipeline chose; a non-zero httpResponseCode
// value replaces it. A misuse by generated code is a *UsageError; a
// document doc fails to write returns the writer's error.
func Encode(op *registry.Operation, out smithy.Serializable, status int, hasDocument func(*smithy.Schema) bool, doc DocumentEncoder) (Encoded, error) {
	s := &serializer{doc: doc, hasDocument: hasDocument, header: http.Header{}, bound: map[string]bool{}, status: status, body: NoBody{}}
	if op.Output != nil && op.Output.Type == smithy.TypeStructure {
		out.Serialize(s)
	}
	if s.err == nil && s.depth != 0 {
		s.fail("Serialize", "a structure, list or map was left open")
	}
	if s.err != nil {
		return Encoded{}, s.err
	}
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return Encoded{Status: s.status, Header: s.header, Body: s.body}, nil
}

// route is where writes inside the current top-level member go.
type route uint8

const (
	toDoc route = iota + 1
	toPayloadDoc
	toHeaderList
	toPrefixHeaders
)

// serializer is the smithy.Serializer generated output code drives; it
// sends each top-level member where its binding says.
type serializer struct {
	doc         DocumentEncoder
	hasDocument func(*smithy.Schema) bool
	header      http.Header
	// bound holds the canonical names of headers an httpHeader member
	// wrote; a prefix-header entry never overwrites one.
	bound  map[string]bool
	status int
	body   ResponseBody
	docW   DocumentWriter
	payW   DocumentWriter
	// depth counts open containers; 1 is the output structure.
	depth int
	route route
	// name is the header name (a list) or prefix (a map) being written.
	name  string
	list  []string
	quote bool
	key   string
	err   error
}

func (s *serializer) WriteStruct(m *smithy.Member, sch *smithy.Schema) {
	if s.err != nil {
		return
	}
	if s.depth == 0 {
		s.depth = 1
		if s.hasDocument(sch) {
			s.docW = s.doc()
			s.docW.WriteStruct(nil, sch)
		}
		return
	}
	s.open(m, func(w smithy.Serializer) { w.WriteStruct(m, sch) })
}

func (s *serializer) CloseStruct() {
	if s.err != nil {
		return
	}
	if s.depth == 1 {
		s.depth, s.route = 0, toDoc
		if s.docW != nil {
			s.docW.CloseStruct()
			s.finish(s.docW)
		}
		return
	}
	s.closeContainer(func(w smithy.Serializer) { w.CloseStruct() })
}

func (s *serializer) WriteUnion(m *smithy.Member, sch *smithy.Schema) {
	s.open(m, func(w smithy.Serializer) { w.WriteUnion(m, sch) })
}

func (s *serializer) CloseUnion() {
	s.closeContainer(func(w smithy.Serializer) { w.CloseUnion() })
}

func (s *serializer) WriteList(m *smithy.Member, n int) {
	if s.err == nil && s.depth == 1 {
		if h, ok := m.HTTP.(smithy.HTTPHeader); ok {
			elem := m.Target.Members[0].Target
			s.depth, s.route, s.name, s.list = 2, toHeaderList, h.Name, nil
			s.quote = elem.Type == smithy.TypeString || elem.Type == smithy.TypeEnum
			return
		}
	}
	s.open(m, func(w smithy.Serializer) { w.WriteList(m, n) })
}

func (s *serializer) CloseList() {
	if s.err == nil && s.depth == 2 && s.route == toHeaderList {
		s.depth = 1
		s.setBound(s.name, joinHeaderList(s.list, s.quote))
		return
	}
	s.closeContainer(func(w smithy.Serializer) { w.CloseList() })
}

func (s *serializer) WriteMap(m *smithy.Member, n int) {
	if s.err == nil && s.depth == 1 {
		if p, ok := m.HTTP.(smithy.HTTPPrefixHeaders); ok {
			s.depth, s.route, s.name = 2, toPrefixHeaders, p.Prefix
			return
		}
	}
	s.open(m, func(w smithy.Serializer) { w.WriteMap(m, n) })
}

func (s *serializer) WriteMapKey(key string) {
	if s.err != nil {
		return
	}
	if s.route == toPrefixHeaders && s.depth == 2 {
		s.key = key
		return
	}
	if w := s.writer("WriteMapKey"); w != nil {
		w.WriteMapKey(key)
	}
}

func (s *serializer) CloseMap() {
	if s.err == nil && s.depth == 2 && s.route == toPrefixHeaders {
		s.depth = 1
		return
	}
	s.closeContainer(func(w smithy.Serializer) { w.CloseMap() })
}

func (s *serializer) WriteString(m *smithy.Member, v string) {
	if s.payload(m) {
		s.body = TextBody{Text: v, MediaType: m.Target.MediaType}
		return
	}
	s.value(m, func() string {
		if m.Target != nil && m.Target.MediaType != "" {
			return base64.StdEncoding.EncodeToString([]byte(v))
		}
		return v
	}, func(w smithy.Serializer) { w.WriteString(m, v) })
}

func (s *serializer) WriteBool(m *smithy.Member, v bool) {
	s.value(m, func() string { return strconv.FormatBool(v) }, func(w smithy.Serializer) { w.WriteBool(m, v) })
}

func (s *serializer) WriteInt8(m *smithy.Member, v int8) {
	s.integer(m, int64(v), func(w smithy.Serializer) { w.WriteInt8(m, v) })
}

func (s *serializer) WriteInt16(m *smithy.Member, v int16) {
	s.integer(m, int64(v), func(w smithy.Serializer) { w.WriteInt16(m, v) })
}

func (s *serializer) WriteInt32(m *smithy.Member, v int32) {
	s.integer(m, int64(v), func(w smithy.Serializer) { w.WriteInt32(m, v) })
}

func (s *serializer) WriteInt64(m *smithy.Member, v int64) {
	s.integer(m, v, func(w smithy.Serializer) { w.WriteInt64(m, v) })
}

func (s *serializer) WriteFloat32(m *smithy.Member, v float32) {
	s.value(m, func() string { return FormatFloat(float64(v), 32) }, func(w smithy.Serializer) { w.WriteFloat32(m, v) })
}

func (s *serializer) WriteFloat64(m *smithy.Member, v float64) {
	s.value(m, func() string { return FormatFloat(v, 64) }, func(w smithy.Serializer) { w.WriteFloat64(m, v) })
}

func (s *serializer) WriteBigInt(m *smithy.Member, v *big.Int) {
	s.value(m, v.String, func(w smithy.Serializer) { w.WriteBigInt(m, v) })
}

func (s *serializer) WriteBigDecimal(m *smithy.Member, v smithy.BigDecimal) {
	s.value(m, func() string { return string(v) }, func(w smithy.Serializer) { w.WriteBigDecimal(m, v) })
}

func (s *serializer) WriteTimestamp(m *smithy.Member, v time.Time) {
	s.value(m, func() string { return FormatTimestamp(v, TimestampFormat(m, smithy.HTTPDate)) },
		func(w smithy.Serializer) { w.WriteTimestamp(m, v) })
}

func (s *serializer) WriteBlob(m *smithy.Member, v []byte) {
	if s.payload(m) {
		s.body = BlobBody{Bytes: v, MediaType: m.Target.MediaType}
		return
	}
	s.value(m, func() string { return base64.StdEncoding.EncodeToString(v) }, func(w smithy.Serializer) { w.WriteBlob(m, v) })
}

func (s *serializer) WriteDocument(m *smithy.Member, v document.Value) {
	if s.payload(m) {
		w := s.doc()
		w.WriteDocument(m, v)
		s.finish(w)
		return
	}
	s.value(m, nil, func(w smithy.Serializer) { w.WriteDocument(m, v) })
}

func (s *serializer) WriteNull(m *smithy.Member) {
	s.value(m, nil, func(w smithy.Serializer) { w.WriteNull(m) })
}

func (s *serializer) WriteBody(m *smithy.Member, b smithy.BodySource) {
	if s.err == nil && s.depth == 1 && Streaming(m) {
		if _, ok := m.HTTP.(smithy.HTTPPayload); ok {
			s.body = StreamBody{Source: b, MediaType: m.Target.MediaType}
			return
		}
	}
	s.fail("WriteBody", "only a top-level @streaming payload member is written as a body")
}

// payload reports whether m is the top-level payload member, written as
// raw bytes or text.
func (s *serializer) payload(m *smithy.Member) bool {
	if s.err != nil || s.depth != 1 || m == nil {
		return false
	}
	_, ok := m.HTTP.(smithy.HTTPPayload)
	return ok
}

// integer writes an integer, which an httpResponseCode member turns into
// the status.
func (s *serializer) integer(m *smithy.Member, v int64, fwd func(smithy.Serializer)) {
	if s.err == nil && s.depth == 1 && m != nil {
		if _, ok := m.HTTP.(smithy.HTTPResponseCode); ok {
			switch {
			case v == 0:
			case v < 100 || v > 599:
				s.fail("WriteInt", "httpResponseCode "+strconv.FormatInt(v, 10)+" is not an HTTP status")
			default:
				s.status = int(v)
			}
			return
		}
	}
	s.value(m, func() string { return strconv.FormatInt(v, 10) }, fwd)
}

// value routes a scalar: a top-level member to its header or the
// document, a nested value to the open header list or prefix map, or to
// the document writer of the current member. text renders the value for a
// header; nil means the value cannot travel in one.
func (s *serializer) value(m *smithy.Member, text func() string, fwd func(smithy.Serializer)) {
	if s.err != nil {
		return
	}
	switch {
	case s.depth == 1 && m != nil && BoundInResponse(m):
		h, ok := m.HTTP.(smithy.HTTPHeader)
		if !ok || text == nil {
			s.fail("Write", "member "+m.Name+" has a binding a scalar output value cannot use")
			return
		}
		s.setBound(h.Name, text())
	case s.depth == 2 && (s.route == toHeaderList || s.route == toPrefixHeaders):
		if text == nil {
			s.fail("Write", "a header list or prefix-header map holds a value a header cannot carry")
			return
		}
		switch name := s.name + s.key; {
		case s.route == toHeaderList:
			s.list = append(s.list, text())
		case !s.bound[http.CanonicalHeaderKey(name)]:
			// The name keeps the prefix's and the key's case rather than
			// Go's canonical form: S3 sends x-amz-meta-color, and clients
			// such as botocore keep the case of what follows the prefix as
			// the metadata key (s3.md 5.4).
			s.header[name] = []string{text()}
		}
	default:
		if s.depth == 1 {
			s.route = toDoc
		}
		if w := s.writer("Write"); w != nil {
			fwd(w)
		}
	}
}

// setBound writes an httpHeader member's header, replacing any
// prefix-header entry already written under that name (wire-rest.md 1.4).
func (s *serializer) setBound(name, value string) {
	s.bound[http.CanonicalHeaderKey(name)] = true
	// A prefix-header entry written earlier keeps its own case, so it is
	// found without case to be replaced.
	for k := range s.header {
		if strings.EqualFold(k, name) {
			delete(s.header, k)
		}
	}
	s.header.Set(name, value)
}

// open starts a container: at the top level it picks the document or the
// payload document by m's binding. A request-only binding is no binding in
// a response.
func (s *serializer) open(m *smithy.Member, fwd func(smithy.Serializer)) {
	if s.err != nil {
		return
	}
	if s.depth == 1 {
		switch m.HTTP.(type) {
		case nil, smithy.HTTPLabel, smithy.HTTPQuery, smithy.HTTPQueryParams:
			s.route = toDoc
		case smithy.HTTPPayload:
			s.route, s.payW = toPayloadDoc, s.doc()
		case smithy.HTTPHeader, smithy.HTTPPrefixHeaders, smithy.HTTPResponseCode:
			s.fail("Write", "member "+m.Name+" has a binding a container output value cannot use")
			return
		}
	}
	if w := s.writer("Write"); w != nil {
		fwd(w)
		s.depth++
	}
}

// closeContainer ends a container, completing a payload document when it
// closes the payload member.
func (s *serializer) closeContainer(fwd func(smithy.Serializer)) {
	if s.err != nil {
		return
	}
	if s.depth < 2 {
		s.fail("Close", "no container is open")
		return
	}
	w := s.writer("Close")
	if w == nil {
		return
	}
	fwd(w)
	s.depth--
	if s.depth == 1 && s.route == toPayloadDoc {
		s.finish(s.payW)
	}
}

// writer returns the document writer of the current route.
func (s *serializer) writer(call string) smithy.Serializer {
	switch {
	case s.route == toDoc && s.docW != nil:
		return s.docW
	case s.route == toPayloadDoc:
		return s.payW
	}
	s.fail(call, "an unbound member was written, but the output has no document body")
	return nil
}

// finish takes a completed document as the body.
func (s *serializer) finish(w DocumentWriter) {
	b, err := w.Bytes()
	if err != nil {
		s.err = err
		return
	}
	s.body = DocumentBody{Bytes: b}
}

func (s *serializer) fail(call, reason string) {
	if s.err == nil {
		s.err = &UsageError{Call: call, Reason: reason}
	}
}
