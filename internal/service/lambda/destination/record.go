package destination

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/ids"
)

// Condition is a record's requestContext.condition: why the event ended
// (lambda-destinations-layers.md 3.2, 3.3).
type Condition uint8

// Condition values.
const (
	// Success: the function returned a response.
	Success Condition = iota + 1
	// RetriesExhausted: the function failed on its last allowed attempt.
	RetriesExhausted
	// EventAgeExceeded: the next attempt would fall past the event's
	// maximum age, or an attempt found the event already past it.
	EventAgeExceeded
	// ZeroReservedConcurrency: the function's reserved concurrency is 0.
	ZeroReservedConcurrency
)

// Conditions returns every declared condition in value order. Not
// implemented yet: it returns nil.
func Conditions() []Condition { return nil }

// String returns the wire value, such as "RetriesExhausted". Not
// implemented yet: it returns "".
func (Condition) String() string { return "" }

// Failed reports whether the condition sends a record to the on-failure
// destination and the event to the dead-letter queue. Not implemented yet:
// it returns false.
func (Condition) Failed() bool { return false }

// DetailType is the detail-type of the condition's EventBridge event
// (lambda-destinations-layers.md 4.4). Not implemented yet: it returns "".
func (Condition) DetailType() string { return "" }

// Record is what one invocation record says.
type Record struct {
	// At is the record's timestamp: the emulator clock when the event
	// settled.
	At time.Time
	// RequestID is the Invoke call's request id, which every attempt ran
	// under (lambda-destinations-layers.md 1).
	RequestID ids.LambdaRequestID
	// FunctionARN is requestContext.functionArn, qualified.
	FunctionARN string
	Condition   Condition
	Invocation  Invocation
	// Payload is the event as invoked, valid JSON: requestPayload.
	Payload []byte
}

// Invocation is whether, and how, the function ran.
//
//sumtype:decl
type Invocation interface{ isInvocation() }

// NeverRan is an event the function never ran: approximateInvokeCount 0
// and no response half (lambda-destinations-layers.md 1, 3.2).
type NeverRan struct{}

// Ran is an event the function ran Attempts times, at least once;
// Response is the last attempt's.
type Ran struct {
	Attempts int
	Response Response
}

func (NeverRan) isInvocation() {}
func (Ran) isInvocation()      {}

// Response is the response half of a record.
type Response struct {
	// ExecutedVersion is "$LATEST" or the version number that ran.
	ExecutedVersion string
	// FunctionError is "Unhandled" for a function error, timeouts and
	// crashes included, and "" for a success.
	FunctionError string
	// Payload is the function's response, or its error object as the
	// runtime posted it.
	Payload []byte
}

// Built is an encoded record.
type Built struct {
	JSON []byte
	// ResponseOmitted is true when the response payload was not JSON and
	// responsePayload was left out (lambda-destinations-layers.md 11.4).
	ResponseOmitted bool
}

// Build encodes r as AWS writes an invocation record
// (lambda-destinations-layers.md 3.1, 11.4). Not implemented yet: it always
// returns a *RecordError.
func Build(_ Record) (Built, error) {
	return Built{}, &RecordError{Field: "Build", Reason: "is not implemented yet"}
}

// RecordError reports a Record that cannot be encoded.
type RecordError struct {
	Field  string
	Reason string
}

func (e *RecordError) Error() string {
	return "destination: cannot build the invocation record: " + e.Field + " " + e.Reason
}
