package awserr

import (
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
	Fault           Fault
	MessageKey      MessageKey
	// Retryable is the retryable trait or membership in the throttling set.
	Retryable bool
}

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

// Internal is a bug: rendered as 500 InternalFailure; Cause is journaled and
// logged, never sent.
type Internal struct{ Cause error }

func (Modeled) isOrigin()       {}
func (Generic) isOrigin()       {}
func (Validation) isOrigin()    {}
func (Unimplemented) isOrigin() {}
func (Injected) isOrigin()      {}
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
