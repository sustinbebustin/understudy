package awserr

import (
	"errors"
	"fmt"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Error is the only failure a handler returns. It is always AWS-shaped, and it
// deliberately does not implement error, so a typed nil can never pass for
// success; AsError adapts it at the rare boundary that needs an error.
type Error struct {
	ID Identity
	// Message is the text AWS sends; empty only where AWS sends none.
	Message string
	// Messages override Message on one protocol (SQS NonExistentQueue over
	// Query says "... for this wsdl version.").
	Messages []ProtocolMessage
	// Fields are extra wire members: S3 BucketName, Key, UploadId,
	// ArgumentName; Lambda Type, Reason.
	Fields []Field
	// Headers are extra response headers: Retry-After, x-amz-bucket-region.
	Headers []Header
	Origin  Origin
	// members is the generated value of the error's shape, for members
	// beyond the message; only WithMembers sets it, with ID from the
	// value's own identity. Code that later sets ID to another shape
	// detaches it (see value), so ID and the value never disagree.
	members Members
}

// Members is a generated error structure: the value an Error carries for
// members beyond its message. Every generated error type implements it on
// its pointer.
type Members interface {
	smithy.Serializable
	// ErrorIdentity returns the identity of the value's error shape, the
	// value the generated Identity<X> function returns.
	ErrorIdentity() Identity
}

// DecodableMembers is a generated error structure that also decodes, so a
// fault rule's members can be read into it. Only the packages that
// generate a deserializer for the shape return one (the generated
// NewErrorMembers).
type DecodableMembers interface {
	Members
	smithy.Deserializable
}

// errNoMembers is the cause WithMembers reports for a nil value.
var errNoMembers = errors.New("awserr: WithMembers was given a nil value, so the error has no shape; " +
	"build it with the generated Err<X>Members constructor of the error shape")

// WithMembers returns the error of v's shape carrying v, with message as
// its text and origin as its origin. Its ID is v's ErrorIdentity, and a
// later ID of another shape detaches v, so the two never disagree on the
// wire. Codecs write the message under the identity's
// MessageKey, never v's own message member (smithy-protocol-tests.md
// 14.7). A nil v is a bug, answered as an Internal error.
func WithMembers(v Members, message string, origin Origin) *Error {
	if v == nil {
		return Internalf(errNoMembers)
	}
	return &Error{ID: v.ErrorIdentity(), Message: message, Origin: origin, members: v}
}

// Members returns the generated value e carries, or nil when it carries
// none.
func (e *Error) Members() Members {
	return e.value()
}

// WireMembers returns the value e carries as a codec writes it after its
// envelope: every member but the message member, which the codec writes
// itself from MessageFor under ID.MessageKey, so the message has one
// source (smithy-protocol-tests.md 14.7). It reports false when e carries
// no value.
func (e *Error) WireMembers() (smithy.Serializable, bool) {
	v := e.value()
	if v == nil {
		return nil, false
	}
	return withoutMessage{v: v}, true
}

// withoutMessage serializes an error value without its message member.
type withoutMessage struct{ v Members }

func (w withoutMessage) Serialize(s smithy.Serializer) {
	w.v.Serialize(&messageFilter{Serializer: s})
}

// messageFilter forwards every write except the error structure's message
// member: the string member named message or Message, the members the
// generator derives MessageKey from (internal/codegen/ir_errors.go).
type messageFilter struct {
	smithy.Serializer
	// depth counts the open containers; 1 is the error structure itself.
	depth int
}

func (f *messageFilter) WriteStruct(m *smithy.Member, s *smithy.Schema) {
	f.depth++
	f.Serializer.WriteStruct(m, s)
}

func (f *messageFilter) CloseStruct() {
	f.depth--
	f.Serializer.CloseStruct()
}

func (f *messageFilter) WriteUnion(m *smithy.Member, s *smithy.Schema) {
	f.depth++
	f.Serializer.WriteUnion(m, s)
}

func (f *messageFilter) CloseUnion() {
	f.depth--
	f.Serializer.CloseUnion()
}

func (f *messageFilter) WriteList(m *smithy.Member, n int) {
	f.depth++
	f.Serializer.WriteList(m, n)
}

func (f *messageFilter) CloseList() {
	f.depth--
	f.Serializer.CloseList()
}

func (f *messageFilter) WriteMap(m *smithy.Member, n int) {
	f.depth++
	f.Serializer.WriteMap(m, n)
}

func (f *messageFilter) CloseMap() {
	f.depth--
	f.Serializer.CloseMap()
}

func (f *messageFilter) WriteString(m *smithy.Member, v string) {
	if f.depth == 1 && m != nil && (m.Name == "message" || m.Name == "Message") {
		return
	}
	f.Serializer.WriteString(m, v)
}

// Identity is everything a codec needs to render an error on any protocol.
// For modeled and overlay-added shapes it is a generated value; composite
// literals outside generated code and internal/awserr are forbidden
// (literals_test.go), so handler code names errors through generated
// constructors.
type Identity struct {
	// Shape is the error shape, or a synthetic id for generic errors.
	Shape smithy.ShapeID
	// JSONType is __type exactly as AWS sends it: namespaced for awsJson1_0
	// services, the bare name for awsJson1_1.
	JSONType string
	// QueryCode is the awsQueryError code, else the shape name.
	QueryCode string
	// Status is the httpError status, else 400 for client and 500 for server
	// faults, or an overlay statusOverride.
	Status int
	// QueryModeStatus is awsQueryError.httpResponseCode, used when the request
	// sends x-amzn-query-mode: true (CloudWatch's 404); 0 means Status.
	QueryModeStatus int
	// QueryErrorWhen is when a query-compatible service's JSON or CBOR
	// answer sends x-amzn-query-error: the overlay's queryErrorHeader,
	// else on every error.
	QueryErrorWhen QueryErrorWhen
	Fault          Fault
	MessageKey     MessageKey
	// Retryable is the retryable trait or membership in the throttling set.
	Retryable bool
}

// QueryErrorHeader is the x-amzn-query-error value a query-compatible
// service's JSON or CBOR answer sends for id, <QueryCode>;<Sender or
// Receiver>, and false when id sends none. queryMode says whether the
// request sent x-amzn-query-mode: true.
func (id Identity) QueryErrorHeader(queryMode bool) (string, bool) {
	if id.QueryErrorWhen == QueryErrorInQueryMode && !queryMode {
		return "", false
	}
	return id.QueryCode + ";" + id.Fault.String(), true
}

// QueryErrorWhen says when a query-compatible service's JSON or CBOR error
// sends x-amzn-query-error.
type QueryErrorWhen uint8

// QueryErrorWhen values.
const (
	// QueryErrorAlways sends it on every error, as Smithy's query-compatible
	// protocol tests require; the zero value.
	QueryErrorAlways QueryErrorWhen = iota
	// QueryErrorInQueryMode sends it only when the request sent
	// x-amzn-query-mode: true (CloudWatch's IAM denial,
	// iam-enforcement-surfaces.md 12.3).
	QueryErrorInQueryMode
)

// Fault says whose mistake an error is; Query renders it as <Type>.
type Fault uint8

// Fault values.
const (
	Sender   Fault = iota + 1 // the caller's mistake (client fault)
	Receiver                  // the service's failure (server fault)
)

// String returns "Sender" or "Receiver", the Query <Type> and the suffix of
// x-amzn-query-error.
func (f Fault) String() string {
	switch f {
	case Sender:
		return "Sender"
	case Receiver:
		return "Receiver"
	default:
		return fmt.Sprintf("Fault(%d)", uint8(f))
	}
}

// MessageKey is the spelling of the shape's message member.
type MessageKey uint8

// MessageKey values.
const (
	MessageLower MessageKey = iota + 1 // "message" (KMS, some Lambda shapes)
	MessageUpper                       // "Message" (Secrets Manager, most Lambda shapes)
	NoMessage                          // the shape has no message member
)

// ProtocolMessage overrides an Error's message on one protocol.
type ProtocolMessage struct {
	Protocol ref.ProtocolID
	Message  string
}

// Field is an extra member an error carries on the wire.
type Field struct {
	Name  string
	Value string
}

// Header is an extra response header an error carries.
type Header struct {
	Name  string
	Value string
}

// MessageFor returns the message to send over protocol p.
func (e *Error) MessageFor(p ref.ProtocolID) string {
	for _, m := range e.Messages {
		if m.Protocol == p {
			return m.Message
		}
	}
	return e.Message
}

// AsError adapts e to error for a boundary that needs one. A nil e is a nil
// error, so the typed-nil trap cannot occur here.
func (e *Error) AsError() error {
	if e == nil {
		return nil
	}
	return &BoundaryError{Err: e}
}

// value is the value e carries while it is of e's shape. Setting ID to
// another shape turns the error into that shape, as the S3 auth mapping
// does, and the value's members belong to the shape it was built for, so
// from then on e carries none and renders as the new shape's message-only
// error. A change of ID that keeps the shape, such as its status, keeps
// the value.
func (e *Error) value() Members {
	if e.members == nil || e.members.ErrorIdentity().Shape != e.ID.Shape {
		return nil
	}
	return e.members
}

// BoundaryError is an Error carried through an error-typed boundary. AsError
// builds it with a non-nil Err; one built by hand without it still reports
// itself instead of panicking.
type BoundaryError struct {
	Err *Error
}

func (b *BoundaryError) Error() string {
	if b.Err == nil {
		return "awserr: BoundaryError carries no Error; build it with (*Error).AsError"
	}
	code := b.Err.ID.Shape.Name()
	if b.Err.Message == "" {
		return fmt.Sprintf("%s (HTTP %d)", code, b.Err.ID.Status)
	}
	return fmt.Sprintf("%s (HTTP %d): %s", code, b.Err.ID.Status, b.Err.Message)
}

// Unwrap returns the cause of an Internal error, else nil.
func (b *BoundaryError) Unwrap() error {
	if b.Err == nil {
		return nil
	}
	if in, ok := b.Err.Origin.(Internal); ok {
		return in.Cause
	}
	return nil
}

// Origin says how an Error came about; the journal and coverage read it.
//
//sumtype:decl
type Origin interface{ isOrigin() }

// Modeled is an error shape from the model or an overlay, chosen by a handler.
type Modeled struct{}

// Generic is a protocol-level error from the generic catalogue:
// SerializationException, UnknownOperationException, the auth family.
type Generic struct{}

// Validation is a constraint failure found before the handler ran.
type Validation struct{ Violations []Violation }

// Unimplemented is a call to an operation no handler is bound to.
type Unimplemented struct{ Op ref.OperationID }

// Injected is an error a fault injection rule produced.
type Injected struct{ Rule ref.RuleID }

// Denial is an authorization refusal: pipeline step 8 or kernel.Authorize
// (iam-enforcement-surfaces.md 12.1).
type Denial struct{}

// Internal is a bug: rendered as 500 InternalFailure; Cause is journaled and
// logged, never sent.
type Internal struct{ Cause error }

func (Modeled) isOrigin()       {}
func (Generic) isOrigin()       {}
func (Validation) isOrigin()    {}
func (Unimplemented) isOrigin() {}
func (Injected) isOrigin()      {}
func (Denial) isOrigin()        {}
func (Internal) isOrigin()      {}

// Internalf wraps a bug as a 500 InternalFailure whose cause stays inside the
// emulator.
func Internalf(cause error) *Error {
	return &Error{
		ID:      InternalFailure(),
		Message: "The request processing has failed because of an unknown error, exception or failure.",
		Origin:  Internal{Cause: cause},
	}
}

// RequestAborted is the outcome of a call whose client went away (a closed
// long poll). The client never sees it; the journal records it.
func RequestAborted() *Error {
	return &Error{
		ID:      RequestAbortedException(),
		Message: "The client closed the connection before the request completed.",
		Origin:  Generic{},
	}
}
