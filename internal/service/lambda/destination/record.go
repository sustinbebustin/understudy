package destination

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
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

	// endCondition follows the last value and is no condition: Conditions
	// and declared count up to it, so a new value is one line above it plus
	// its String case.
	endCondition
)

// Conditions returns every declared condition in value order.
func Conditions() []Condition {
	all := make([]Condition, 0, endCondition-Success)
	for c := Success; c < endCondition; c++ {
		all = append(all, c)
	}
	return all
}

// String returns the wire value, such as "RetriesExhausted", or
// "Condition(<n>)" for an undeclared value.
func (c Condition) String() string {
	switch c {
	case Success:
		return "Success"
	case RetriesExhausted:
		return "RetriesExhausted"
	case EventAgeExceeded:
		return "EventAgeExceeded"
	case ZeroReservedConcurrency:
		return "ZeroReservedConcurrency"
	default:
		return "Condition(" + strconv.Itoa(int(c)) + ")"
	}
}

// Failed reports whether the condition sends a record to the on-failure
// destination and the event to the dead-letter queue: every declared
// condition but Success (lambda-destinations-layers.md 5.3).
func (c Condition) Failed() bool { return c.declared() && c != Success }

// DetailType is the detail-type of the condition's EventBridge event
// (lambda-destinations-layers.md 4.4), "" for an undeclared value.
func (c Condition) DetailType() string {
	switch {
	case c == Success:
		return "Lambda Function Invocation Result - Success"
	case c.Failed():
		return "Lambda Function Invocation Result - Failure"
	default:
		return ""
	}
}

func (c Condition) declared() bool { return c >= Success && c < endCondition }

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

// timestampLayout is the record's timestamp: UTC, milliseconds, Z
// (lambda-destinations-layers.md 3.2). Format cuts finer digits.
const timestampLayout = "2006-01-02T15:04:05.000Z"

// Build encodes r as AWS writes an invocation record: a compact envelope
// with its keys in AWS's order at every level, version, timestamp,
// requestContext, requestPayload, responseContext, responsePayload, and
// both payloads embedded byte for byte as the caller sent them and the
// function's runtime returned them, whitespace included
// (lambda-destinations-layers.md 3.1, 11.4). AWS recorded the bytes on an
// SQS destination (both payloads, LDL-02) and an SNS one (the response,
// LDL-05); S3 and Lambda destinations carrying the same bytes is inferred.
// Whitespace around a whole payload is unrecorded and left out, so the
// envelope stays compact. A response payload that is not JSON is left out
// and reported in Built.ResponseOmitted rather than guessed at (LDL-02).
// Build refuses, with a *RecordError, a record AWS never writes.
func Build(r Record) (Built, error) {
	if err := r.check(); err != nil {
		return Built{}, err
	}
	rc := wireRequestContext{
		RequestID:   string(r.RequestID),
		FunctionARN: r.FunctionARN,
		Condition:   r.Condition.String(),
	}
	ran, didRun := r.Invocation.(Ran)
	if didRun {
		rc.ApproximateInvokeCount = ran.Attempts
	}
	var b bytes.Buffer
	b.WriteString(`{"version":"1.0","timestamp":`)
	err := encodeCompact(&b, r.At.UTC().Format(timestampLayout))
	b.WriteString(`,"requestContext":`)
	err = errors.Join(err, encodeCompact(&b, rc))
	b.WriteString(`,"requestPayload":`)
	b.Write(trimJSONSpace(r.Payload))
	omitted := false
	if didRun {
		b.WriteString(`,"responseContext":`)
		// statusCode is 200 for a success and for a function error alike
		// (lambda-destinations-layers.md 3.2).
		err = errors.Join(err, encodeCompact(&b, wireResponseContext{
			StatusCode:      200,
			ExecutedVersion: ran.Response.ExecutedVersion,
			FunctionError:   ran.Response.FunctionError,
		}))
		if json.Valid(ran.Response.Payload) {
			b.WriteString(`,"responsePayload":`)
			b.Write(trimJSONSpace(ran.Response.Payload))
		} else {
			omitted = true
		}
	}
	if err != nil {
		return Built{}, &RecordError{Field: "Record", Reason: "could not be encoded: " + err.Error()}
	}
	b.WriteByte('}')
	return Built{JSON: b.Bytes(), ResponseOmitted: omitted}, nil
}

// encodeCompact appends v to b as compact JSON, never HTML-escaped.
func encodeCompact(b *bytes.Buffer, v any) error {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	// Encode ends the value with a newline.
	b.Truncate(b.Len() - 1)
	return nil
}

// trimJSONSpace is p without the JSON whitespace around it.
func trimJSONSpace(p []byte) []byte { return bytes.Trim(p, " \t\r\n") }

// check refuses a record AWS never writes.
func (r Record) check() error {
	switch {
	case !r.Condition.declared():
		return &RecordError{Field: "Condition", Reason: "is " + r.Condition.String() +
			", which is no condition; the declared ones are Success, RetriesExhausted, EventAgeExceeded and ZeroReservedConcurrency"}
	case r.RequestID == "":
		return &RecordError{Field: "RequestID", Reason: "is empty; a record carries the Invoke call's request id"}
	case r.FunctionARN == "":
		return &RecordError{Field: "FunctionARN", Reason: "is empty; a record carries the qualified function ARN"}
	case !json.Valid(r.Payload):
		return &RecordError{Field: "Payload", Reason: "is not valid JSON (" + strconv.Itoa(len(r.Payload)) +
			" bytes); requestPayload embeds the event as invoked, which Invoke takes only as JSON"}
	}
	switch inv := r.Invocation.(type) {
	case nil:
		return &RecordError{Field: "Invocation", Reason: "is nil; a record says whether the function ran (Ran) or not (NeverRan)"}
	case NeverRan:
		if r.Condition == Success {
			return &RecordError{Field: "Invocation", Reason: "is NeverRan, but a Success record is of a function that ran (lambda-destinations-layers.md 3.3)"}
		}
	case Ran:
		return inv.check(r.Condition)
	}
	return nil
}

// check refuses a run AWS never reports under condition c.
func (inv Ran) check(c Condition) error {
	switch fe := inv.Response.FunctionError; {
	case inv.Attempts < 1:
		return &RecordError{Field: "Invocation.Attempts", Reason: "is " + strconv.Itoa(inv.Attempts) +
			"; Ran counts at least one attempt, and an event the function never ran is NeverRan"}
	case c == Success && fe != "":
		return &RecordError{Field: "Invocation.Response.FunctionError", Reason: "is " + strconv.Quote(fe) +
			", but a Success record has no function error (lambda-destinations-layers.md 3.2)"}
	case c.Failed() && fe == "":
		return &RecordError{Field: "Invocation.Response.FunctionError", Reason: "is empty, but a " + c.String() +
			" record of a function that ran ends on a function error (lambda-destinations-layers.md 3.3)"}
	default:
		return nil
	}
}

// wireRequestContext and wireResponseContext are the record's two
// contexts: the field order is AWS's key order
// (lambda-destinations-layers.md 3.1, 11.4).
type wireRequestContext struct {
	RequestID   string `json:"requestId"`
	FunctionARN string `json:"functionArn"`
	Condition   string `json:"condition"`
	// ApproximateInvokeCount is the attempts made, 0 when the function
	// never ran (lambda-destinations-layers.md 3.2).
	ApproximateInvokeCount int `json:"approximateInvokeCount"`
}

type wireResponseContext struct {
	StatusCode      int    `json:"statusCode"`
	ExecutedVersion string `json:"executedVersion"`
	// FunctionError is absent on success (lambda-destinations-layers.md 3.2).
	FunctionError string `json:"functionError,omitempty"`
}

// RecordError reports a Record that cannot be encoded.
type RecordError struct {
	Field  string
	Reason string
}

func (e *RecordError) Error() string {
	return "destination: cannot build the invocation record: " + e.Field + " " + e.Reason
}
