package journal

import (
	"fmt"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Event is one journal record. The journal assigns ID and Wall; the caller
// supplies the rest and hands over the slices in Detail, which it must not
// modify afterwards. An Event the journal hands out is never modified again,
// so its slices may be shared. Events returned by Query, Trace and Stream
// share their Links, Resources and Reasons with the journal, its streams and
// its file writer; treat them as read-only, and clone a slice before sorting
// or editing it.
type Event struct {
	ID     ref.EventID // monotonic per journal; preserved across snapshot load
	Cause  ref.Cause
	Links  []ref.EventID // related, not parent: the other messages in an ESM batch
	At     clock.Instant // emulator time of the event (a call's admission instant)
	Wall   time.Time     // wall time the journal recorded the event, UTC
	Scope  aws.Scope
	Detail Detail
}

// Detail is the kind-specific payload of a journal event.
//
//sumtype:decl
type Detail interface{ isDetail() }

// APICall is one operation invocation, external or internal. The kernel is
// its only writer, through Begin and End.
type APICall struct {
	Op        ref.OperationID
	RequestID ids.RequestID
	Origin    ref.Origin
	Principal string    // rendered principal ARN
	Mode      string    // caller | execution-role | service:<name>, for internal calls
	Resources []aws.ARN // the ARNs the call touched, in first-touch order; never the zero ARN
	Outcome   Outcome
	Latency   time.Duration // wall
	// TraceHeader is the X-Amzn-Trace-Id the request carried, or inherited
	// from its parent call (kernel Call.Trace); empty when there was none. It
	// is stored for consumers, never used for causality.
	TraceHeader string
	Input       Redacted
}

// Delivery is one attempt to deliver a message across services.
type Delivery struct {
	From, To  aws.ARN
	Mechanism delivery.Mechanism
	Attempt   int
	Outcome   DeliveryOutcome
}

// Invocation is one Lambda function invocation in an execution environment.
type Invocation struct {
	Function       aws.ARN
	Version, Env   string
	Cold           bool
	Init, Duration time.Duration // wall
	Outcome        InvocationOutcome
}

// TimerFired is a scheduler timer reaching its due instant.
type TimerFired struct{ Key clock.TimerKey }

// StateChange is a resource moving between states of its state machine.
type StateChange struct {
	Resource aws.ARN
	From, To ResourceState
}

// FaultInjected is a fault rule matching a call: hit Hit of Of for a NextN
// trigger, Of 0 otherwise.
type FaultInjected struct {
	Rule    ref.RuleID
	Hit, Of int
}

// ClockChanged is the emulator clock changing mode.
type ClockChanged struct{ From, To clock.Mode }

// Advisory is something the emulator did that AWS would not have, or that a
// user would want to know about, such as a delivery a stored resource policy
// would deny.
type Advisory struct {
	Kind     AdvisoryKind
	Resource aws.ARN
	Text     string
}

// Unclaimed is a request no route claimed; Reasons says why each claim rule
// declined. The edge is its only writer.
type Unclaimed struct {
	Method, Host, Path string
	Reasons            []string
}

// Gap tells a stream consumer that Missed events were not delivered to it,
// because it read too slowly or resumed after events were evicted. Missed
// counts differently by cause: events dropped live while the stream's buffer
// was full count only when they matched the stream's filter, while events
// evicted before a resume or replay reached them count whether they matched
// or not, since the journal no longer holds them to check. Gaps exist only on
// streams; the journal never stores one it creates.
type Gap struct{ Missed uint64 }

func (APICall) isDetail()       {}
func (Delivery) isDetail()      {}
func (Invocation) isDetail()    {}
func (TimerFired) isDetail()    {}
func (StateChange) isDetail()   {}
func (FaultInjected) isDetail() {}
func (ClockChanged) isDetail()  {}
func (Advisory) isDetail()      {}
func (Unclaimed) isDetail()     {}
func (Gap) isDetail()           {}

// Kind names a Detail variant; queries filter on it.
type Kind uint8

// Kind values, one per Detail variant.
const (
	KindAPICall Kind = iota + 1
	KindDelivery
	KindInvocation
	KindTimerFired
	KindStateChange
	KindFaultInjected
	KindClockChanged
	KindAdvisory
	KindUnclaimed
	KindGap
)

// KindOf returns the Kind of d; a nil Detail has kind 0.
func KindOf(d Detail) Kind {
	switch d.(type) {
	case APICall:
		return KindAPICall
	case Delivery:
		return KindDelivery
	case Invocation:
		return KindInvocation
	case TimerFired:
		return KindTimerFired
	case StateChange:
		return KindStateChange
	case FaultInjected:
		return KindFaultInjected
	case ClockChanged:
		return KindClockChanged
	case Advisory:
		return KindAdvisory
	case Unclaimed:
		return KindUnclaimed
	case Gap:
		return KindGap
	}
	return 0
}

// String returns the wire name, such as "api-call".
func (k Kind) String() string {
	switch k {
	case KindAPICall:
		return "api-call"
	case KindDelivery:
		return "delivery"
	case KindInvocation:
		return "invocation"
	case KindTimerFired:
		return "timer-fired"
	case KindStateChange:
		return "state-change"
	case KindFaultInjected:
		return "fault-injected"
	case KindClockChanged:
		return "clock-changed"
	case KindAdvisory:
		return "advisory"
	case KindUnclaimed:
		return "unclaimed"
	case KindGap:
		return "gap"
	default:
		return fmt.Sprintf("Kind(%d)", uint8(k))
	}
}

// KindError reports text that names no event kind.
type KindError struct {
	Text string
}

func (e *KindError) Error() string {
	return fmt.Sprintf("%q names no journal event kind; known names are api-call, delivery, invocation, timer-fired, state-change, fault-injected, clock-changed, advisory, unclaimed and gap", e.Text)
}

// ParseKind returns the Kind whose wire name is s.
func ParseKind(s string) (Kind, error) {
	for k := KindAPICall; k <= KindGap; k++ {
		if k.String() == s {
			return k, nil
		}
	}
	return 0, &KindError{Text: s}
}

// MarshalText encodes the wire name.
func (k Kind) MarshalText() ([]byte, error) {
	if k < KindAPICall || k > KindGap {
		return nil, &KindError{Text: k.String()}
	}
	return []byte(k.String()), nil
}

// UnmarshalText decodes a wire name.
func (k *Kind) UnmarshalText(text []byte) error {
	parsed, err := ParseKind(string(text))
	if err != nil {
		return err
	}
	*k = parsed
	return nil
}

// resourcesOf returns the ARNs an event is about: the ARNs an API call
// touched, both ends and the dead-letter queue of a delivery, the function of
// an invocation, the resource of a state change or advisory. A zero ARN is
// no resource, so it is left out.
func resourcesOf(d Detail) []aws.ARN {
	var all []aws.ARN
	switch d := d.(type) {
	case APICall:
		all = d.Resources
	case Delivery:
		all = []aws.ARN{d.From, d.To}
		if dl, ok := d.Outcome.(DeadLettered); ok {
			all = append(all, dl.DLQ)
		}
	case Invocation:
		all = []aws.ARN{d.Function}
	case StateChange:
		all = []aws.ARN{d.Resource}
	case Advisory:
		all = []aws.ARN{d.Resource}
	case TimerFired, FaultInjected, ClockChanged, Unclaimed, Gap:
	}
	if !slices.Contains(all, aws.ARN{}) {
		return all
	}
	return slices.DeleteFunc(slices.Clone(all), func(a aws.ARN) bool { return a == aws.ARN{} })
}
