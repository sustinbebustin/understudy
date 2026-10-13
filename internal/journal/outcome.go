package journal

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Outcome is how an API call ended. Pending marks a call that has begun and
// not yet ended.
//
//sumtype:decl
type Outcome interface{ isOutcome() }

// Pending is a call between Begin and End.
type Pending struct{}

// OK is a call that returned its output.
type OK struct{}

// Failed is a call that returned an AWS error.
type Failed struct {
	Identity ErrorIdentity
	Status   int
	Message  string
}

// Injected is a call a fault rule failed.
type Injected struct {
	Rule     ref.RuleID
	Identity ErrorIdentity
}

func (Pending) isOutcome()  {}
func (OK) isOutcome()       {}
func (Failed) isOutcome()   {}
func (Injected) isOutcome() {}

// ErrorIdentity names the error shape a call failed with. The journal does not
// import awserr; the kernel renders the identity's shape id.
type ErrorIdentity struct {
	Shape string // absolute shape id, such as com.amazonaws.sqs#QueueDoesNotExist
}

// DeliveryOutcome is how one delivery attempt ended.
//
//sumtype:decl
type DeliveryOutcome interface{ isDeliveryOutcome() }

// Delivered is an attempt the target accepted.
type Delivered struct{}

// Filtered is a message a subscription or rule filter did not match; it never
// reaches a dead-letter queue.
type Filtered struct{ Rule string }

// Retrying is a failed attempt with another scheduled at Next.
type Retrying struct{ Next clock.Instant }

// DeadLettered is a message moved to the dead-letter queue DLQ.
type DeadLettered struct{ DLQ aws.ARN }

// Dropped is a message abandoned for Reason with no dead-letter queue.
type Dropped struct{ Reason string }

func (Delivered) isDeliveryOutcome()    {}
func (Filtered) isDeliveryOutcome()     {}
func (Retrying) isDeliveryOutcome()     {}
func (DeadLettered) isDeliveryOutcome() {}
func (Dropped) isDeliveryOutcome()      {}

// InvocationOutcome is how one function invocation ended.
//
//sumtype:decl
type InvocationOutcome interface{ isInvocationOutcome() }

// InvocationOK is an invocation whose handler returned a response.
type InvocationOK struct{}

// FunctionError is an invocation whose handler returned an error of Type, the
// X-Amz-Function-Error value (Unhandled, Handled).
type FunctionError struct{ Type string }

// Timeout is an invocation that ran past the function timeout.
type Timeout struct{}

// ExitError is an invocation whose runtime process exited with Code.
type ExitError struct{ Code int }

// OOM is an invocation killed for exceeding the function's memory.
type OOM struct{}

// Throttled is an invocation refused for lack of concurrency.
type Throttled struct{}

func (InvocationOK) isInvocationOutcome()  {}
func (FunctionError) isInvocationOutcome() {}
func (Timeout) isInvocationOutcome()       {}
func (ExitError) isInvocationOutcome()     {}
func (OOM) isInvocationOutcome()           {}
func (Throttled) isInvocationOutcome()     {}

// ResourceState is one state of a resource's state machine. Each service
// declares its closed set of values as constants next to the state machine,
// such as lambda's Pending, Active, Inactive and Failed.
type ResourceState string

// AdvisoryKind is the closed set of advisories.
type AdvisoryKind uint8

// AdvisoryKind values.
const (
	// AdvisoryPolicyWouldDeny: a stored resource policy would deny a delivery
	// that understudy performed anyway.
	AdvisoryPolicyWouldDeny AdvisoryKind = iota + 1
	// AdvisoryUnmintedCredentials: a function container called with
	// credentials understudy did not mint for it.
	AdvisoryUnmintedCredentials
	// AdvisoryShadowedCodeUpdate: a code update is stored but a hot-reload
	// overlay keeps serving the local directory.
	AdvisoryShadowedCodeUpdate
	// AdvisoryCrossRegionServed: under the s3.crossRegion lenient switch, a
	// request that asserted one region was served from a bucket in another,
	// which AWS refuses or redirects.
	AdvisoryCrossRegionServed
	// AdvisoryFilterUndecided: a filter the emulator's matching engine could
	// not evaluate within its work bound, which AWS has no counterpart for,
	// so the message or event was treated as unmatched.
	AdvisoryFilterUndecided
	// AdvisoryScheduleFiresSkipped: a scheduled rule's fires that came due
	// while understudy was not running, or before a loaded snapshot's
	// time, were skipped rather than delivered in a burst.
	AdvisoryScheduleFiresSkipped
)

// String returns the wire name, such as "policy-would-deny".
func (k AdvisoryKind) String() string {
	switch k {
	case AdvisoryPolicyWouldDeny:
		return "policy-would-deny"
	case AdvisoryUnmintedCredentials:
		return "unminted-credentials"
	case AdvisoryShadowedCodeUpdate:
		return "shadowed-code-update"
	case AdvisoryCrossRegionServed:
		return "cross-region-served"
	case AdvisoryFilterUndecided:
		return "filter-undecided"
	case AdvisoryScheduleFiresSkipped:
		return "schedule-fires-skipped"
	default:
		return fmt.Sprintf("AdvisoryKind(%d)", uint8(k))
	}
}

// AdvisoryKindError reports text that names no advisory kind.
type AdvisoryKindError struct {
	Text string
}

func (e *AdvisoryKindError) Error() string {
	return fmt.Sprintf("%q names no advisory kind; known names are policy-would-deny, unminted-credentials, shadowed-code-update, cross-region-served, filter-undecided and schedule-fires-skipped", e.Text)
}

// ParseAdvisoryKind returns the AdvisoryKind whose wire name is s.
func ParseAdvisoryKind(s string) (AdvisoryKind, error) {
	for k := AdvisoryPolicyWouldDeny; k <= AdvisoryScheduleFiresSkipped; k++ {
		if k.String() == s {
			return k, nil
		}
	}
	return 0, &AdvisoryKindError{Text: s}
}

// MarshalText encodes the wire name.
func (k AdvisoryKind) MarshalText() ([]byte, error) {
	if k < AdvisoryPolicyWouldDeny || k > AdvisoryScheduleFiresSkipped {
		return nil, &AdvisoryKindError{Text: k.String()}
	}
	return []byte(k.String()), nil
}

// UnmarshalText decodes a wire name.
func (k *AdvisoryKind) UnmarshalText(text []byte) error {
	parsed, err := ParseAdvisoryKind(string(text))
	if err != nil {
		return err
	}
	*k = parsed
	return nil
}

// Redacted is a call's input as a compact JSON document with every sensitive
// member already removed by the generated Redact. The zero value is no input.
//
// Begin stores an input longer than Options.MaxBytes/16 bytes as the marker
// {"$truncated":<length of the input in bytes>}, so one large body cannot push
// every other event out of the ring. No modeled input has a member named
// "$truncated": Smithy member names are identifiers.
type Redacted struct{ doc string }

// inputShare bounds a stored input to MaxBytes/inputShare bytes; see Redacted.
const inputShare = 16

// RedactedError reports a document that is not JSON.
type RedactedError struct {
	Err error
}

func (e *RedactedError) Error() string {
	return "journal: a redacted input must be one JSON value: " + e.Err.Error()
}

func (e *RedactedError) Unwrap() error { return e.Err }

// NewRedacted returns doc, compacted and with '<', '>', '&', U+2028 and
// U+2029 escaped in strings, as a Redacted input. That is the form
// json.Marshal writes, so encoding and decoding the document returns it
// unchanged. An empty doc is the zero value.
func NewRedacted(doc []byte) (Redacted, error) {
	if len(doc) == 0 {
		return Redacted{}, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc); err != nil {
		return Redacted{}, &RedactedError{Err: err}
	}
	var escaped bytes.Buffer
	json.HTMLEscape(&escaped, compact.Bytes())
	return Redacted{doc: escaped.String()}, nil
}

// JSON returns the document; nil for the zero value.
func (r Redacted) JSON() []byte {
	if r.doc == "" {
		return nil
	}
	return []byte(r.doc)
}

// capped returns r, or the truncation marker in its place when r's document
// is longer than limit bytes.
func (r Redacted) capped(limit int) Redacted {
	if len(r.doc) <= limit {
		return r
	}
	return Redacted{doc: fmt.Sprintf(`{"$truncated":%d}`, len(r.doc))}
}

// Result classifies outcomes across event kinds for the Outcome filter.
type Result uint8

// Result values.
const (
	ResultOK Result = iota + 1
	ResultError
	ResultPending
)

// String returns the wire name: ok, error or pending.
func (r Result) String() string {
	switch r {
	case ResultOK:
		return "ok"
	case ResultError:
		return "error"
	case ResultPending:
		return "pending"
	default:
		return fmt.Sprintf("Result(%d)", uint8(r))
	}
}

// ResultNameError reports text that names no result.
type ResultNameError struct {
	Text string
}

func (e *ResultNameError) Error() string {
	return fmt.Sprintf("%q names no outcome class; known names are ok, error and pending", e.Text)
}

// ParseResult returns the Result whose wire name is s.
func ParseResult(s string) (Result, error) {
	for r := ResultOK; r <= ResultPending; r++ {
		if r.String() == s {
			return r, nil
		}
	}
	return 0, &ResultNameError{Text: s}
}

// ResultOf classifies d: an API call by its outcome, a delivery as OK when
// delivered or filtered, an invocation as OK when its handler returned, an
// unclaimed request as an error. Other kinds have no result.
func ResultOf(d Detail) (Result, bool) {
	switch d := d.(type) {
	case APICall:
		switch d.Outcome.(type) {
		case Pending:
			return ResultPending, true
		case OK:
			return ResultOK, true
		case Failed, Injected:
			return ResultError, true
		}
		return 0, false
	case Delivery:
		switch d.Outcome.(type) {
		case Delivered, Filtered:
			return ResultOK, true
		case Retrying, DeadLettered, Dropped:
			return ResultError, true
		}
		return 0, false
	case Invocation:
		switch d.Outcome.(type) {
		case InvocationOK:
			return ResultOK, true
		case FunctionError, Timeout, ExitError, OOM, Throttled:
			return ResultError, true
		}
		return 0, false
	case Unclaimed:
		return ResultError, true
	case TimerFired, StateChange, FaultInjected, ClockChanged, Advisory, Gap:
		return 0, false
	}
	return 0, false
}
