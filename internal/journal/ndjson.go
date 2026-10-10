package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/netip"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// EncodeNDJSON writes e as one JSON line.
func EncodeNDJSON(w io.Writer, e Event) error {
	b, err := e.MarshalJSON()
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// DecodeError reports an NDJSON line that is not a journal event, such as a
// record torn by a crash mid-write. The lines around it are unaffected.
type DecodeError struct {
	Line int // 1-based
	Err  error
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("journal: NDJSON line %d is not a journal event: %v; the other lines are read as usual", e.Line, e.Err)
}

func (e *DecodeError) Unwrap() error { return e.Err }

// DecodeNDJSON yields the events of r, one per line, in order; blank lines
// are skipped. A line that is not an event yields a *DecodeError and decoding
// goes on with the next line, so one bad record costs only itself. An error
// reading r is yielded last.
func DecodeNDJSON(r io.Reader) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		br := bufio.NewReader(r)
		for n := 1; ; n++ {
			line, readErr := br.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				var e Event
				if err := e.UnmarshalJSON(line); err != nil {
					if !yield(Event{}, &DecodeError{Line: n, Err: err}) {
						return
					}
				} else if !yield(e, nil) {
					return
				}
			}
			switch {
			case errors.Is(readErr, io.EOF):
				return
			case readErr != nil:
				yield(Event{}, fmt.Errorf("journal: reading NDJSON line %d: %w", n, readErr))
				return
			}
		}
	}
}

// EncodingError reports an event that has no wire form: a nil sum value, an
// enum outside its declared values, an ARN whose text would not read back
// as it (an *aws.ARNError or *ARNTextError), or an instant outside years 0
// to 9999.
type EncodingError struct {
	ID  ref.EventID
	Err error
}

func (e *EncodingError) Error() string {
	return fmt.Sprintf("journal: event %d cannot be encoded: %v", e.ID, e.Err)
}

func (e *EncodingError) Unwrap() error { return e.Err }

// missingError names a nil sum value an event needs.
type missingError struct {
	What string
}

func (e *missingError) Error() string { return e.What + " is nil" }

// The wire form: one object per event with the detail under "detail" and its
// variant named by "kind"; nested sums are objects tagged by "kind";
// durations are integer nanoseconds; instants and wall times RFC 3339 with
// nanoseconds; ARNs strings. Empty slices are omitted and decode as nil.
type (
	wireEvent struct {
		ID     ref.EventID     `json:"id"`
		Cause  wireCause       `json:"cause"`
		Links  []ref.EventID   `json:"links,omitempty"`
		At     clock.Instant   `json:"at"`
		Wall   time.Time       `json:"wall"`
		Scope  wireScope       `json:"scope"`
		Kind   Kind            `json:"kind"`
		Detail json.RawMessage `json:"detail"`
	}
	wireCause struct {
		Parent ref.EventID `json:"parent,omitempty"`
		Trace  ref.EventID `json:"trace,omitempty"`
	}
	wireScope struct {
		Partition aws.Partition `json:"partition"`
		Account   aws.AccountID `json:"account"`
		Region    aws.Region    `json:"region"`
	}
	wireAPICall struct {
		Service     ref.ServiceID   `json:"service"`
		Operation   string          `json:"operation"`
		RequestID   ids.RequestID   `json:"requestId,omitempty"`
		Origin      wireOrigin      `json:"origin"`
		Principal   string          `json:"principal,omitempty"`
		Mode        string          `json:"mode,omitempty"`
		Resources   []string        `json:"resources,omitempty"`
		Outcome     wireOutcome     `json:"outcome"`
		Latency     time.Duration   `json:"latency,omitempty"`
		TraceHeader string          `json:"traceHeader,omitempty"`
		Input       json.RawMessage `json:"input,omitempty"`
	}
	wireOrigin struct {
		Kind       string `json:"kind"`
		Protocol   string `json:"protocol,omitempty"`
		Listener   string `json:"listener,omitempty"`
		RemoteAddr string `json:"remoteAddr,omitempty"`
		UserAgent  string `json:"userAgent,omitempty"`
		Via        string `json:"via,omitempty"`
	}
	wireOutcome struct {
		Kind     string        `json:"kind"`
		Identity *wireIdentity `json:"identity,omitempty"`
		Status   int           `json:"status,omitempty"`
		Message  string        `json:"message,omitempty"`
		Rule     ref.RuleID    `json:"rule,omitempty"`
	}
	wireIdentity struct {
		Shape string `json:"shape"`
	}
	wireDelivery struct {
		From      string             `json:"from"`
		To        string             `json:"to"`
		Mechanism delivery.Mechanism `json:"mechanism"`
		Attempt   int                `json:"attempt"`
		Outcome   wireDeliveryResult `json:"outcome"`
	}
	wireDeliveryResult struct {
		Kind   string         `json:"kind"`
		Rule   string         `json:"rule,omitempty"`
		Next   *clock.Instant `json:"next,omitempty"`
		DLQ    string         `json:"dlq,omitempty"`
		Reason string         `json:"reason,omitempty"`
	}
	wireInvocation struct {
		Function string               `json:"function"`
		Version  string               `json:"version,omitempty"`
		Env      string               `json:"env,omitempty"`
		Cold     bool                 `json:"cold,omitempty"`
		Init     time.Duration        `json:"init,omitempty"`
		Duration time.Duration        `json:"duration,omitempty"`
		Outcome  wireInvocationResult `json:"outcome"`
	}
	wireInvocationResult struct {
		Kind string `json:"kind"`
		Type string `json:"type,omitempty"`
		Code int    `json:"code,omitempty"`
	}
	wireTimerFired struct {
		Kind clock.TimerKind `json:"timerKind"`
		ID   string          `json:"timerId"`
	}
	wireStateChange struct {
		Resource string        `json:"resource"`
		From     ResourceState `json:"from"`
		To       ResourceState `json:"to"`
	}
	wireFaultInjected struct {
		Rule ref.RuleID `json:"rule"`
		Hit  int        `json:"hit"`
		Of   int        `json:"of,omitempty"`
	}
	wireClockChanged struct {
		From wireMode `json:"from"`
		To   wireMode `json:"to"`
	}
	wireMode struct {
		Kind       string         `json:"kind"`
		Offset     time.Duration  `json:"offset,omitempty"`
		At         *clock.Instant `json:"at,omitempty"`
		Factor     float64        `json:"factor,omitempty"`
		AnchorWall *time.Time     `json:"anchorWall,omitempty"`
		AnchorEmu  *clock.Instant `json:"anchorEmu,omitempty"`
	}
	wireAdvisory struct {
		Advisory AdvisoryKind `json:"advisory"`
		Resource string       `json:"resource"`
		Text     string       `json:"text"`
	}
	wireUnclaimed struct {
		Method  string   `json:"method"`
		Host    string   `json:"host"`
		Path    string   `json:"path"`
		Reasons []string `json:"reasons,omitempty"`
	}
	wireGap struct {
		Missed uint64 `json:"missed"`
	}
)

// MarshalJSON encodes e in the journal's wire form, the form of every NDJSON
// line and admin API response.
func (e Event) MarshalJSON() ([]byte, error) {
	fail := func(err error) ([]byte, error) { return nil, &EncodingError{ID: e.ID, Err: err} }
	detail, err := encodeDetail(e.Detail)
	if err != nil {
		return fail(err)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fail(err)
	}
	b, err := json.Marshal(wireEvent{
		ID:     e.ID,
		Cause:  wireCause{Parent: e.Cause.Parent, Trace: e.Cause.Trace},
		Links:  e.Links,
		At:     e.At,
		Wall:   e.Wall,
		Scope:  wireScope{Partition: e.Scope.Partition, Account: e.Scope.Account, Region: e.Scope.Region},
		Kind:   KindOf(e.Detail),
		Detail: raw,
	})
	if err != nil {
		return fail(err)
	}
	return b, nil
}

// UnmarshalJSON decodes the wire form strictly: unknown members, unknown kinds
// and malformed ARNs are errors.
func (e *Event) UnmarshalJSON(b []byte) error {
	var w wireEvent
	if err := strict(b, &w); err != nil {
		return err
	}
	d, err := decodeDetail(w.Kind, w.Detail)
	if err != nil {
		return fmt.Errorf("%s detail: %w", w.Kind, err)
	}
	*e = Event{
		ID:     w.ID,
		Cause:  ref.Cause{Parent: w.Cause.Parent, Trace: w.Cause.Trace},
		Links:  w.Links,
		At:     w.At,
		Wall:   w.Wall,
		Scope:  aws.Scope{Partition: w.Scope.Partition, Account: w.Scope.Account, Region: w.Scope.Region},
		Detail: d,
	}
	return nil
}

// strict decodes exactly one JSON value into v, rejecting unknown members.
func strict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// encodeDetail returns the wire form of d. It refuses every value json.Marshal
// would refuse in it, and every ARN that would not decode back to itself, so
// the journal can check an event before recording it without encoding it.
func encodeDetail(d Detail) (any, error) {
	for _, a := range resourcesOf(d) {
		if err := checkARN(a); err != nil {
			return nil, err
		}
	}
	switch d := d.(type) {
	case APICall:
		return encodeAPICall(d)
	case Delivery:
		if _, err := d.Mechanism.MarshalText(); err != nil {
			return nil, err
		}
		out, err := encodeDeliveryResult(d.Outcome)
		return wireDelivery{From: arnText(d.From), To: arnText(d.To), Mechanism: d.Mechanism, Attempt: d.Attempt, Outcome: out}, err
	case Invocation:
		out, err := encodeInvocationResult(d.Outcome)
		return wireInvocation{
			Function: arnText(d.Function), Version: d.Version, Env: d.Env, Cold: d.Cold,
			Init: d.Init, Duration: d.Duration, Outcome: out,
		}, err
	case TimerFired:
		return wireTimerFired{Kind: d.Key.Kind, ID: d.Key.ID}, nil
	case StateChange:
		return wireStateChange{Resource: arnText(d.Resource), From: d.From, To: d.To}, nil
	case FaultInjected:
		return wireFaultInjected(d), nil
	case ClockChanged:
		from, err := encodeMode(d.From)
		if err != nil {
			return nil, err
		}
		to, err := encodeMode(d.To)
		return wireClockChanged{From: from, To: to}, err
	case Advisory:
		if _, err := d.Kind.MarshalText(); err != nil {
			return nil, err
		}
		return wireAdvisory{Advisory: d.Kind, Resource: arnText(d.Resource), Text: d.Text}, nil
	case Unclaimed:
		return wireUnclaimed(d), nil
	case Gap:
		return wireGap(d), nil
	}
	return nil, &missingError{What: "Detail"}
}

func encodeAPICall(d APICall) (any, error) {
	origin, err := encodeOrigin(d.Origin)
	if err != nil {
		return nil, err
	}
	out, err := encodeOutcome(d.Outcome)
	if err != nil {
		return nil, err
	}
	var resources []string
	for _, a := range d.Resources {
		resources = append(resources, arnText(a))
	}
	return wireAPICall{
		Service: d.Op.Service, Operation: d.Op.Name, RequestID: d.RequestID, Origin: origin,
		Principal: d.Principal, Mode: d.Mode, Resources: resources, Outcome: out,
		Latency: d.Latency, TraceHeader: d.TraceHeader, Input: d.Input.JSON(),
	}, nil
}

func encodeOrigin(o ref.Origin) (wireOrigin, error) {
	switch o := o.(type) {
	case ref.External:
		listener, err := o.Listener.MarshalText()
		if err != nil {
			return wireOrigin{}, err
		}
		w := wireOrigin{Kind: "external", Protocol: string(o.Protocol), Listener: string(listener), UserAgent: o.UserAgent}
		if o.RemoteAddr.IsValid() {
			w.RemoteAddr = o.RemoteAddr.String()
		}
		return w, nil
	case ref.Internal:
		via, err := o.Via.MarshalText()
		if err != nil {
			return wireOrigin{}, err
		}
		return wireOrigin{Kind: "internal", Via: string(via)}, nil
	}
	return wireOrigin{}, &missingError{What: "APICall.Origin"}
}

func encodeOutcome(o Outcome) (wireOutcome, error) {
	switch o := o.(type) {
	case Pending:
		return wireOutcome{Kind: "pending"}, nil
	case OK:
		return wireOutcome{Kind: "ok"}, nil
	case Failed:
		return wireOutcome{Kind: "failed", Identity: &wireIdentity{Shape: o.Identity.Shape}, Status: o.Status, Message: o.Message}, nil
	case Injected:
		return wireOutcome{Kind: "injected", Identity: &wireIdentity{Shape: o.Identity.Shape}, Rule: o.Rule}, nil
	}
	return wireOutcome{}, &missingError{What: "APICall.Outcome"}
}

func encodeDeliveryResult(o DeliveryOutcome) (wireDeliveryResult, error) {
	switch o := o.(type) {
	case Delivered:
		return wireDeliveryResult{Kind: "delivered"}, nil
	case Filtered:
		return wireDeliveryResult{Kind: "filtered", Rule: o.Rule}, nil
	case Retrying:
		if err := checkInstant("retrying delivery's next attempt", o.Next); err != nil {
			return wireDeliveryResult{}, err
		}
		return wireDeliveryResult{Kind: "retrying", Next: &o.Next}, nil
	case DeadLettered:
		return wireDeliveryResult{Kind: "dead-lettered", DLQ: arnText(o.DLQ)}, nil
	case Dropped:
		return wireDeliveryResult{Kind: "dropped", Reason: o.Reason}, nil
	}
	return wireDeliveryResult{}, &missingError{What: "Delivery.Outcome"}
}

func encodeInvocationResult(o InvocationOutcome) (wireInvocationResult, error) {
	switch o := o.(type) {
	case InvocationOK:
		return wireInvocationResult{Kind: "ok"}, nil
	case FunctionError:
		return wireInvocationResult{Kind: "function-error", Type: o.Type}, nil
	case Timeout:
		return wireInvocationResult{Kind: "timeout"}, nil
	case ExitError:
		return wireInvocationResult{Kind: "exit-error", Code: o.Code}, nil
	case OOM:
		return wireInvocationResult{Kind: "oom"}, nil
	case Throttled:
		return wireInvocationResult{Kind: "throttled"}, nil
	}
	return wireInvocationResult{}, &missingError{What: "Invocation.Outcome"}
}

func encodeMode(m clock.Mode) (wireMode, error) {
	switch m := m.(type) {
	case clock.Running:
		return wireMode{Kind: "running", Offset: m.Offset}, nil
	case clock.Frozen:
		if err := checkInstant("frozen clock instant", m.At); err != nil {
			return wireMode{}, err
		}
		return wireMode{Kind: "frozen", At: &m.At}, nil
	case clock.Scaled:
		if !(m.Factor > 0 && m.Factor <= clock.MaxScale) {
			return wireMode{}, fmt.Errorf("scaled clock factor %g is outside (0, %d]", m.Factor, clock.MaxScale)
		}
		if _, err := m.AnchorWall.MarshalText(); err != nil {
			return wireMode{}, fmt.Errorf("scaled clock anchor wall time: %w", err)
		}
		if err := checkInstant("scaled clock anchor instant", m.AnchorEmu); err != nil {
			return wireMode{}, err
		}
		return wireMode{Kind: "scaled", Factor: m.Factor, AnchorWall: &m.AnchorWall, AnchorEmu: &m.AnchorEmu}, nil
	}
	return wireMode{}, &missingError{What: "ClockChanged mode"}
}

// checkInstant refuses an instant RFC 3339 cannot write, one outside years 0
// to 9999; what names it in the error.
func checkInstant(what string, i clock.Instant) error {
	if _, err := i.MarshalText(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// ARNTextError reports an ARN whose text reads back as a different ARN,
// because a colon inside the partition, service, region or account moves the
// element boundaries.
type ARNTextError struct {
	ARN, ReadBack aws.ARN
}

func (e *ARNTextError) Error() string {
	return fmt.Sprintf("ARN %q reads back with partition %q, service %q, region %q and account %q instead of %q, %q, %q and %q: only the resource may contain a colon",
		e.ARN.String(), e.ReadBack.Partition, e.ReadBack.Service, e.ReadBack.Region, e.ReadBack.Account,
		e.ARN.Partition, e.ARN.Service, e.ARN.Region, e.ARN.Account)
}

// checkARN refuses a non-zero ARN whose text would not decode back to it: one
// with an empty partition, service or resource (an *aws.ARNError), or a colon
// before the resource (an *ARNTextError). The zero ARN is written as "".
func checkARN(a aws.ARN) error {
	if a == (aws.ARN{}) {
		return nil
	}
	back, err := aws.ParseARN(a.String())
	if err != nil {
		return err
	}
	if back != a {
		return &ARNTextError{ARN: a, ReadBack: back}
	}
	return nil
}

// arnText is the wire form of an ARN; the zero ARN is the empty string.
func arnText(a aws.ARN) string {
	if a == (aws.ARN{}) {
		return ""
	}
	return a.String()
}

func parseARNText(s string) (aws.ARN, error) {
	if s == "" {
		return aws.ARN{}, nil
	}
	return aws.ParseARN(s)
}

// decodeDetail decodes the detail of an event of kind k.
func decodeDetail(k Kind, raw json.RawMessage) (Detail, error) {
	switch k {
	case KindAPICall:
		var w wireAPICall
		if err := strict(raw, &w); err != nil {
			return nil, err
		}
		return decodeAPICall(w)
	case KindDelivery:
		var w wireDelivery
		if err := strict(raw, &w); err != nil {
			return nil, err
		}
		return decodeDelivery(w)
	case KindInvocation:
		var w wireInvocation
		if err := strict(raw, &w); err != nil {
			return nil, err
		}
		return decodeInvocation(w)
	case KindTimerFired:
		var w wireTimerFired
		err := strict(raw, &w)
		return TimerFired{Key: clock.TimerKey{Kind: w.Kind, ID: w.ID}}, err
	case KindStateChange:
		var w wireStateChange
		if err := strict(raw, &w); err != nil {
			return nil, err
		}
		a, err := parseARNText(w.Resource)
		return StateChange{Resource: a, From: w.From, To: w.To}, err
	case KindFaultInjected:
		var w wireFaultInjected
		err := strict(raw, &w)
		return FaultInjected(w), err
	case KindClockChanged:
		return decodeClockChanged(raw)
	case KindAdvisory:
		var w wireAdvisory
		if err := strict(raw, &w); err != nil {
			return nil, err
		}
		a, err := parseARNText(w.Resource)
		return Advisory{Kind: w.Advisory, Resource: a, Text: w.Text}, err
	case KindUnclaimed:
		var w wireUnclaimed
		err := strict(raw, &w)
		return Unclaimed(w), err
	case KindGap:
		var w wireGap
		err := strict(raw, &w)
		return Gap(w), err
	}
	return nil, &KindError{Text: k.String()}
}

func decodeAPICall(w wireAPICall) (Detail, error) {
	origin, err := decodeOrigin(w.Origin)
	if err != nil {
		return nil, err
	}
	out, err := decodeOutcome(w.Outcome)
	if err != nil {
		return nil, err
	}
	var resources []aws.ARN
	for _, s := range w.Resources {
		a, err := parseARNText(s)
		if err != nil {
			return nil, err
		}
		resources = append(resources, a)
	}
	input, err := NewRedacted(w.Input)
	if err != nil {
		return nil, err
	}
	return APICall{
		Op:        ref.OperationID{Service: w.Service, Name: w.Operation},
		RequestID: w.RequestID, Origin: origin, Principal: w.Principal, Mode: w.Mode,
		Resources: resources, Outcome: out, Latency: w.Latency, TraceHeader: w.TraceHeader, Input: input,
	}, nil
}

// tagError reports a nested sum whose kind tag names no variant.
type tagError struct {
	Sum, Kind string
}

func (e *tagError) Error() string { return fmt.Sprintf("%s kind %q is unknown", e.Sum, e.Kind) }

func decodeOrigin(w wireOrigin) (ref.Origin, error) {
	switch w.Kind {
	case "external":
		listener, err := ref.ParseListenerKind(w.Listener)
		if err != nil {
			return nil, err
		}
		var addr netip.AddrPort
		if w.RemoteAddr != "" {
			if addr, err = netip.ParseAddrPort(w.RemoteAddr); err != nil {
				return nil, err
			}
		}
		return ref.External{Protocol: ref.ProtocolID(w.Protocol), Listener: listener, RemoteAddr: addr, UserAgent: w.UserAgent}, nil
	case "internal":
		via, err := ref.ParseVia(w.Via)
		if err != nil {
			return nil, err
		}
		return ref.Internal{Via: via}, nil
	}
	return nil, &tagError{Sum: "origin", Kind: w.Kind}
}

func decodeOutcome(w wireOutcome) (Outcome, error) {
	var identity ErrorIdentity
	if w.Identity != nil {
		identity = ErrorIdentity{Shape: w.Identity.Shape}
	}
	switch w.Kind {
	case "pending":
		return Pending{}, nil
	case "ok":
		return OK{}, nil
	case "failed":
		return Failed{Identity: identity, Status: w.Status, Message: w.Message}, nil
	case "injected":
		return Injected{Rule: w.Rule, Identity: identity}, nil
	}
	return nil, &tagError{Sum: "outcome", Kind: w.Kind}
}

func decodeDelivery(w wireDelivery) (Detail, error) {
	from, err := parseARNText(w.From)
	if err != nil {
		return nil, err
	}
	to, err := parseARNText(w.To)
	if err != nil {
		return nil, err
	}
	var out DeliveryOutcome
	switch w.Outcome.Kind {
	case "delivered":
		out = Delivered{}
	case "filtered":
		out = Filtered{Rule: w.Outcome.Rule}
	case "retrying":
		var next clock.Instant
		if w.Outcome.Next != nil {
			next = *w.Outcome.Next
		}
		out = Retrying{Next: next}
	case "dead-lettered":
		dlq, err := parseARNText(w.Outcome.DLQ)
		if err != nil {
			return nil, err
		}
		out = DeadLettered{DLQ: dlq}
	case "dropped":
		out = Dropped{Reason: w.Outcome.Reason}
	default:
		return nil, &tagError{Sum: "delivery outcome", Kind: w.Outcome.Kind}
	}
	return Delivery{From: from, To: to, Mechanism: w.Mechanism, Attempt: w.Attempt, Outcome: out}, nil
}

func decodeInvocation(w wireInvocation) (Detail, error) {
	fn, err := parseARNText(w.Function)
	if err != nil {
		return nil, err
	}
	var out InvocationOutcome
	switch w.Outcome.Kind {
	case "ok":
		out = InvocationOK{}
	case "function-error":
		out = FunctionError{Type: w.Outcome.Type}
	case "timeout":
		out = Timeout{}
	case "exit-error":
		out = ExitError{Code: w.Outcome.Code}
	case "oom":
		out = OOM{}
	case "throttled":
		out = Throttled{}
	default:
		return nil, &tagError{Sum: "invocation outcome", Kind: w.Outcome.Kind}
	}
	return Invocation{
		Function: fn, Version: w.Version, Env: w.Env, Cold: w.Cold,
		Init: w.Init, Duration: w.Duration, Outcome: out,
	}, nil
}

func decodeClockChanged(raw json.RawMessage) (Detail, error) {
	var w wireClockChanged
	if err := strict(raw, &w); err != nil {
		return nil, err
	}
	from, err := decodeMode(w.From)
	if err != nil {
		return nil, err
	}
	to, err := decodeMode(w.To)
	if err != nil {
		return nil, err
	}
	return ClockChanged{From: from, To: to}, nil
}

func decodeMode(w wireMode) (clock.Mode, error) {
	deref := func(i *clock.Instant) clock.Instant {
		if i == nil {
			return clock.Instant{}
		}
		return *i
	}
	switch w.Kind {
	case "running":
		return clock.Running{Offset: w.Offset}, nil
	case "frozen":
		return clock.Frozen{At: deref(w.At)}, nil
	case "scaled":
		var wall time.Time
		if w.AnchorWall != nil {
			wall = *w.AnchorWall
		}
		return clock.Scaled{Factor: w.Factor, AnchorWall: wall, AnchorEmu: deref(w.AnchorEmu)}, nil
	}
	return nil, &tagError{Sum: "clock mode", Kind: w.Kind}
}
