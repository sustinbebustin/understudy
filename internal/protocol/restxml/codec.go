package restxml

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/restbind"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Dialect is a restXml service's conventions that its descriptor does not
// carry. The zero Dialect is Smithy's restXml: wrapped errors, date-time
// timestamps without forced milliseconds, and the service's
// malformedRequestError for every value that does not parse.
type Dialect struct {
	// Errors is how the service frames an error document.
	Errors ErrorForm
	// MillisecondTimestamps writes date-time values as S3 does: with
	// milliseconds always in bodies, 2009-10-12T17:50:30.000Z, and in
	// response headers only when they are not zero,
	// 2026-11-03T04:40:14Z and 2028-11-03T03:40:47.030Z
	// (s3-ssec-object-lock.md 6.7, for the one such header).
	MillisecondTimestamps bool
	// InvalidArgument answers a label, query or header value that does not
	// parse, with ArgumentName and ArgumentValue (S3's InvalidArgument,
	// error-matrix.md 5.4 T2). Zero means the service's
	// malformedRequestError.
	InvalidArgument awserr.Identity
	// ArgumentTexts is the InvalidArgument message for an unparsable
	// value of the label, query or header it names, in place of
	// "Invalid value for the <name> <location>.".
	ArgumentTexts map[string]string
	// Internal renders an awserr.Internal origin (a handler or codec bug,
	// a kernel panic) with S3's own identity and text, "We encountered an
	// internal error. Please try again.", in place of the generic
	// InternalFailure that awserr.Internalf carries. The error's status is
	// kept, so the 200-with-error path still answers 200. S3's is
	// InternalError (wire-rest.md 3.6, 3.7). Zero keeps InternalFailure.
	Internal awserr.Identity
	// OmitEmptyDocumentType sends an output whose members all bind
	// outside the body with no Content-Type. Smithy's restXml sends
	// application/xml there, for the empty document it still serializes
	// (RestXmlHttpResponseCode). The research records S3's bare head only
	// on HeadBucket (s3.md); S3Dialect applies it to every bodiless answer
	// until the others are recorded.
	OmitEmptyDocumentType bool
	// PartialContent answers 206 Partial Content, in place of the
	// operation's 200, for an output that sets a Content-Range header, as
	// S3 answers a ranged or partNumber GetObject and HeadObject (s3.md
	// 5.8, 5.9). The models give those operations no httpResponseCode
	// member a handler could set.
	PartialContent bool
	// InertStreams sends X-Content-Type-Options: nosniff and
	// Content-Security-Policy: sandbox with every @streaming payload, such
	// as a GetObject body. AWS sends neither, but understudy serves object
	// bodies on the origin of its own admin prefix, so a stored HTML or SVG
	// object opened in a browser must not run script there. SDKs ignore
	// both headers.
	InertStreams bool
}

// ErrorForm is the frame of a restXml error document. Each form writes Code,
// Message and the error's other fields inside <Error>.
type ErrorForm uint8

const (
	// SmithyWrapped is Smithy's restXml form:
	// <ErrorResponse><Error><Type>Sender</Type>...</Error><RequestId/></ErrorResponse>,
	// as Route 53 and CloudFront answer (wire-rest.md 2.4).
	SmithyWrapped ErrorForm = iota
	// Bare is the noErrorWrapping form, S3's: an <Error> root that ends with
	// RequestId and HostId (wire-rest.md 2.4).
	Bare
	// HostWrapped is S3 Control's: <ErrorResponse><Error> with no Type,
	// then RequestId and HostId; an apostrophe in the error's text is
	// written as itself
	// (testdata/parity/s3/R06/06-s3control-list-missing.json).
	HostWrapped
)

// internalMessage is S3's InternalError text (wire-rest.md 3.7).
const internalMessage = "We encountered an internal error. Please try again."

// S3Dialect is S3's: bare <Error> roots, millisecond timestamps,
// invalidArgument for unparsable request values, no Content-Type on a
// bodiless output, 206 for a partial read, and inert object bodies. The
// composition root sets Internal to S3's InternalError identity.
func S3Dialect(invalidArgument awserr.Identity) Dialect {
	return Dialect{
		Errors: Bare, MillisecondTimestamps: true, InvalidArgument: invalidArgument, OmitEmptyDocumentType: true, PartialContent: true, InertStreams: true,
		ArgumentTexts: map[string]string{
			// s3-ssec-object-lock.md 6.2: S3SOL-18 18-bad-date, recorded 2026-11-03.
			"x-amz-object-lock-retain-until-date": "The retain until date must be provided in ISO 8601 format",
			// R12 05-list-max-keys-abc, recorded 2026-10-14 for ListObjectsV2;
			// ListObjects and ListObjectVersions share it (inferred).
			"max-keys": "Provided max-keys not an integer or within integer range",
		},
	}
}

// S3ControlDialect is S3 Control's: S3's, but with errors wrapped in
// <ErrorResponse><Error> (HostWrapped), since the S3 Control model does not
// set noErrorWrapping and the SDKs' s3control clients read Error>Code
// there.
func S3ControlDialect(invalidArgument awserr.Identity) Dialect {
	d := S3Dialect(invalidArgument)
	d.Errors = HostWrapped
	return d
}

// Codec is the restXml codec (S3 and S3 Control). Its zero value is
// usable and speaks the zero Dialect to every service.
type Codec struct {
	// Dialect returns a service's Dialect; nil means the zero Dialect.
	Dialect func(svc ref.ServiceID) Dialect
}

// Response is an encoded answer: status, the complete header set and a
// body the edge copies to the client.
type Response struct {
	Status int
	Header http.Header
	Body   io.Reader
}

const (
	mediaXML = "application/xml"
	// malformedXML is S3's MalformedXML text (wire-rest.md 2.3).
	malformedXML = "The XML you provided was not well-formed or did not validate against our published schema"
)

// Protocol returns ref.RESTXML.
func (Codec) Protocol() ref.ProtocolID { return ref.RESTXML }

// Body caps for an operation without MaxRequestBytes: a @streaming
// payload may be as large as S3's largest single PUT, 5 GiB; any other
// body is an XML configuration document, far smaller than the default.
const (
	DefaultMaxStreamingBytes int64 = 5 << 30
	DefaultMaxRequestBytes   int64 = 16 << 20
)

// BodyLimit is op's body cap: its MaxRequestBytes, else
// DefaultMaxStreamingBytes for a @streaming payload and
// DefaultMaxRequestBytes otherwise.
func (Codec) BodyLimit(op *registry.Operation) int64 {
	if op.MaxRequestBytes > 0 {
		return op.MaxRequestBytes
	}
	if p, ok := restbind.PayloadMember(op.Input); ok && restbind.Streaming(p) {
		return DefaultMaxStreamingBytes
	}
	return DefaultMaxRequestBytes
}

// Decode binds r onto in: the head through restbind, an XML payload or
// document through xmlw.Decode, matched on local names, and a @streaming
// payload as the body itself, unread. A body that is not well-formed or
// does not fit the input is the service's malformedRequestError (S3's
// MalformedXML) with S3's text; a head value that does not parse is the
// dialect's InvalidArgument naming it, else the malformedRequestError.
func (c Codec) Decode(r *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable) *awserr.Error {
	err := restbind.Decode(r, op, body, in, newDocument)
	if err == nil {
		return nil
	}
	var (
		read      *restbind.ReadError
		bind      *restbind.BindError
		decode    *decodeError
		malformed *xmlw.MalformedXMLError
	)
	switch {
	case errors.As(err, &read):
		return read.AWSError()
	case errors.As(err, &bind):
		return c.invalidArgument(op.Service, bind)
	case errors.As(err, &decode), errors.As(err, &malformed):
		return malformedRequest(op.Service, malformedXML)
	default:
		return awserr.Internalf(fmt.Errorf("decoding the %s input: %w", op.ID, err))
	}
}

// Encode renders out, the handler's output for op, with the status the
// pipeline chose (an httpResponseCode member replaces it, and the
// dialect's PartialContent turns 200 into 206): header and
// prefix-header members as headers, the unbound members as an XML
// document whose root carries the service namespace, a payload structure
// as its own root, a blob or string payload as its bytes, a @streaming
// payload streamed, and S3's x-amz-request-id and x-amz-id-2. When the
// request asked for encoding-type=url and the output echoes EncodingType,
// every key field is percent-encoded. An output whose one unbound member
// shares the root's name is written unwrapped
// (aws.customizations#s3UnwrappedXmlOutput, GetBucketLocation). An output
// with members, all bound outside the body, carries Content-Type
// application/xml on a status that allows a body, unless a member binds
// it or the dialect omits it; an output without members carries none.
func (c Codec) Encode(r *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (Response, *awserr.Error) {
	d := c.dialect(op.Service)
	opts := writerOptions{
		namespace:  op.Service.XMLNamespace,
		millis:     d.MillisecondTimestamps,
		encodeKeys: r != nil && r.URL.Query().Get("encoding-type") == "url" && hasMember(op.Output, "EncodingType"),
		unwrapped:  unwrapped(op.Output),
	}
	stamp := func(t time.Time, f smithy.TimestampFormat) string {
		return formatHeaderTimestamp(t, f, d.MillisecondTimestamps)
	}
	enc, err := restbind.EncodeTimestamps(op, out, status, restbind.HasResponseDocument, func() restbind.DocumentWriter { return newWriter(opts) }, stamp)
	if err != nil {
		return Response{}, awserr.Internalf(fmt.Errorf("encoding the %s output: %w", op.ID, err))
	}
	if d.PartialContent && enc.Status == http.StatusOK && enc.Header.Get("Content-Range") != "" {
		enc.Status = http.StatusPartialContent
	}
	if _, empty := enc.Body.(restbind.NoBody); empty && !d.OmitEmptyDocumentType && hasMembers(op.Output) &&
		restbind.BodyAllowed(enc.Status) && enc.Header.Get("Content-Type") == "" {
		enc.Header.Set("Content-Type", mediaXML)
	}
	if _, stream := enc.Body.(restbind.StreamBody); stream && d.InertStreams {
		enc.Header.Set("X-Content-Type-Options", "nosniff")
		enc.Header.Set("Content-Security-Policy", "sandbox")
	}
	body, err := enc.Finish(mediaXML)
	if err != nil {
		return Response{}, awserr.Internalf(fmt.Errorf("opening the %s output stream: %w", op.ID, err))
	}
	stampRequestID(enc.Header, requestID)
	return Response{Status: enc.Status, Header: enc.Header, Body: body}, nil
}

// EncodeError renders e as S3 does (wire-rest.md 2.4, 3.7): status from
// the identity, which is 200 on the 200-with-error path of
// CompleteMultipartUpload, CopyObject and UploadPartCopy, where the
// pipeline has already set it; the XML declaration first, then <Error>
// with Code, Message (absent when empty), the members of the value e
// carries that bind to no header, as outputs write them
// (smithy-protocol-tests.md 11.2), the error's context fields in order,
// RequestId and HostId; Content-Type application/xml; the value's
// httpHeader members and the error's headers (x-amz-bucket-region); and
// x-amz-request-id and x-amz-id-2. A HEAD request, and a status that
// allows no body (1xx, 204, and 304 for S3's NotModified, wire-rest.md
// 3.6), get the status and headers only. The dialect's ErrorForm frames
// the document: S3's bare <Error> is described here. An awserr.Internal origin
// takes the dialect's Internal identity (S3's InternalError). A value that
// does not serialize is answered as InternalFailure. svc may be nil for a
// request no service claimed.
func (c Codec) EncodeError(r *http.Request, svc *registry.Service, requestID string, e *awserr.Error) Response {
	d := c.dialect(svc)
	e = d.rendered(e)
	h, members, err := errorMembers(e, d)
	if err != nil {
		return c.EncodeError(r, svc, requestID, awserr.Internalf(err))
	}
	for _, x := range e.Headers {
		h.Add(x.Name, x.Value)
	}
	stampRequestID(h, requestID)
	if r != nil && r.Method == http.MethodHead || !restbind.BodyAllowed(e.ID.Status) {
		return Response{Status: e.ID.Status, Header: h, Body: http.NoBody}
	}
	var buf bytes.Buffer
	x := xmlw.New(&buf)
	x.Declaration()
	switch d.Errors {
	case Bare:
		x.Start("Error")
		errorFields(x, e, members)
		textElement(x, "RequestId", requestID)
		textElement(x, "HostId", HostID(requestID))
		x.End()
	case HostWrapped:
		x.KeepApostrophes()
		x.Start("ErrorResponse")
		x.Start("Error")
		errorFields(x, e, members)
		x.End()
		textElement(x, "RequestId", requestID)
		textElement(x, "HostId", HostID(requestID))
		x.End()
	case SmithyWrapped:
		x.Start("ErrorResponse")
		x.Start("Error")
		textElement(x, "Type", e.ID.Fault.String())
		errorFields(x, e, members)
		x.End()
		textElement(x, "RequestId", requestID)
		x.End()
	}
	if err := x.Close(); err != nil {
		// The writer refuses only an element name, and error fields are
		// named by generated constructors; report the bug, not the error.
		return c.EncodeError(r, svc, requestID, awserr.Internalf(fmt.Errorf("rendering the %s error: %w", e.ID.Shape, err)))
	}
	h.Set("Content-Type", mediaXML)
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	return Response{Status: e.ID.Status, Header: h, Body: &buf}
}

// HostID is x-amz-id-2 and an error's HostId: a stable base64 SHA-256 of
// the request id, standing for the S3 host id, which clients log but never
// interpret. It matches the header the edge stamps.
func HostID(requestID string) string {
	sum := sha256.Sum256([]byte(requestID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// rendered is e as the dialect sends it: an Internal origin under the
// dialect's Internal identity and text, with e's status, when it has one.
// The rendered error is built anew, so it never carries a value of
// another shape under the dialect's identity.
func (d Dialect) rendered(e *awserr.Error) *awserr.Error {
	if _, ok := e.Origin.(awserr.Internal); !ok || d.Internal.Shape == "" {
		return e
	}
	id := d.Internal
	id.Status = e.ID.Status
	return &awserr.Error{ID: id, Message: internalMessage, Fields: e.Fields, Headers: e.Headers, Origin: e.Origin}
}

// errorMembers splits the value e carries by its HTTP bindings: the
// headers its httpHeader members set, and the elements of its other
// members, which go inside <Error>. Without a value they are no headers
// and no elements.
func errorMembers(e *awserr.Error, d Dialect) (http.Header, []*element, error) {
	v, ok := e.WireMembers()
	if !ok {
		return http.Header{}, nil, nil
	}
	var (
		doc     *writer
		payload bool
	)
	hasDocument := func(s *smithy.Schema) bool {
		_, payload = restbind.PayloadMember(s)
		return restbind.HasResponseDocument(s)
	}
	enc, err := restbind.EncodeStructure(v, e.ID.Status, hasDocument, func() restbind.DocumentWriter {
		doc = &writer{opts: writerOptions{millis: d.MillisecondTimestamps}}
		return doc
	})
	switch {
	case err != nil:
		return nil, nil, fmt.Errorf("encoding the members of the %s error: %w", e.ID.Shape, err)
	case payload:
		return nil, nil, fmt.Errorf("encoding the members of the %s error: an httpPayload member has no place in an error body", e.ID.Shape)
	case doc == nil:
		// The shape has no member outside the headers.
		return enc.Header, nil, nil
	case len(doc.root.attrs) > 0:
		return nil, nil, fmt.Errorf("encoding the members of the %s error: an xmlAttribute member has no place on <Error>", e.ID.Shape)
	}
	// The document's root is the value's structure; its children are the
	// members <Error> holds.
	return enc.Header, doc.root.children, nil
}

func (c Codec) dialect(svc *registry.Service) Dialect {
	if c.Dialect == nil || svc == nil {
		return Dialect{}
	}
	return c.Dialect(svc.ID)
}

// invalidArgument answers a head value that does not parse.
func (c Codec) invalidArgument(svc *registry.Service, bind *restbind.BindError) *awserr.Error {
	d := c.dialect(svc)
	id := d.InvalidArgument
	if id.Shape == "" {
		return malformedRequest(svc, bind.Error())
	}
	message, ok := d.ArgumentTexts[bind.Name]
	if !ok {
		message = fmt.Sprintf("Invalid value for the %s %s.", bind.Name, bind.Location)
	}
	return &awserr.Error{
		ID:      id,
		Message: message,
		Fields:  []awserr.Field{{Name: "ArgumentName", Value: bind.Name}, {Name: "ArgumentValue", Value: bind.Value}},
		Origin:  awserr.Generic{},
	}
}

// malformedRequest is svc's answer to a request that does not decode: its
// malformedRequestError, else SerializationException.
func malformedRequest(svc *registry.Service, message string) *awserr.Error {
	id := svc.MalformedRequestError
	if id.Shape == "" {
		id = awserr.SerializationException()
	}
	return &awserr.Error{ID: id, Message: message, Origin: awserr.Generic{}}
}

// errorFields writes Code, Message, the value's members and the context
// fields.
func errorFields(x *xmlw.Writer, e *awserr.Error, members []*element) {
	code := e.ID.QueryCode
	if code == "" {
		code = e.ID.Shape.Name()
	}
	textElement(x, "Code", code)
	if msg := e.MessageFor(ref.RESTXML); msg != "" {
		textElement(x, "Message", msg)
	}
	for _, m := range members {
		emit(x, m, nil)
	}
	for _, f := range e.Fields {
		textElement(x, f.Name, f.Value)
	}
}

func textElement(x *xmlw.Writer, name, text string) {
	x.Start(name)
	x.Text(text)
	x.End()
}

// stampRequestID sets S3's request id pair; the edge stamps the same
// values on every restXml answer.
func stampRequestID(h http.Header, requestID string) {
	h.Set("X-Amz-Request-Id", requestID)
	h.Set("X-Amz-Id-2", HostID(requestID))
}

// hasMembers reports whether output shape s is a structure with members.
func hasMembers(s *smithy.Schema) bool {
	return s != nil && s.Type == smithy.TypeStructure && len(s.Members) > 0
}

func hasMember(s *smithy.Schema, name string) bool {
	return s != nil && slices.ContainsFunc(s.Members, func(m *smithy.Member) bool { return m.Name == name })
}

// unwrapped reports whether output shape s has the form
// aws.customizations#s3UnwrappedXmlOutput describes: its one unbound
// member is a scalar whose element name is the root's, so the member's
// value is the document (GetBucketLocation's LocationConstraint).
func unwrapped(s *smithy.Schema) bool {
	if s == nil || s.XMLName == "" {
		return false
	}
	var body []*smithy.Member
	for _, m := range s.Members {
		if !restbind.BoundInResponse(m) {
			body = append(body, m)
		}
	}
	if len(body) != 1 || elementName(body[0]) != s.XMLName {
		return false
	}
	t := body[0].Target.Type
	return t != smithy.TypeStructure && t != smithy.TypeUnion && t != smithy.TypeList && t != smithy.TypeMap
}
