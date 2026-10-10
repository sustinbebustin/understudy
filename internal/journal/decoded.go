package journal

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// asDecoded returns d in the form decoding its wire form produces, so a
// recorded event equals its NDJSON round trip: every string with each byte
// that is not UTF-8 replaced by U+FFFD, as encoding/json writes it, an empty
// Unclaimed.Reasons as nil, and a scaled clock's anchor wall time in UTC
// without a monotonic reading. Redacted input passes through encoding
// unchanged already (see NewRedacted). Slices are copied only when a value
// in them changes.
func asDecoded(d Detail) Detail {
	switch d := d.(type) {
	case APICall:
		return callAsDecoded(d)
	case Delivery:
		d.From, d.To = arnAsDecoded(d.From), arnAsDecoded(d.To)
		switch o := d.Outcome.(type) {
		case Filtered:
			d.Outcome = Filtered{Rule: wireText(o.Rule)}
		case DeadLettered:
			d.Outcome = DeadLettered{DLQ: arnAsDecoded(o.DLQ)}
		case Dropped:
			d.Outcome = Dropped{Reason: wireText(o.Reason)}
		case Delivered, Retrying:
		}
		return d
	case Invocation:
		d.Function = arnAsDecoded(d.Function)
		d.Version, d.Env = wireText(d.Version), wireText(d.Env)
		switch o := d.Outcome.(type) {
		case FunctionError:
			d.Outcome = FunctionError{Type: wireText(o.Type)}
		case InvocationOK, Timeout, ExitError, OOM, Throttled:
		}
		return d
	case TimerFired:
		d.Key = clock.TimerKey{Kind: clock.TimerKind(wireText(string(d.Key.Kind))), ID: wireText(d.Key.ID)}
		return d
	case StateChange:
		d.Resource = arnAsDecoded(d.Resource)
		d.From, d.To = ResourceState(wireText(string(d.From))), ResourceState(wireText(string(d.To)))
		return d
	case FaultInjected:
		d.Rule = ref.RuleID(wireText(string(d.Rule)))
		return d
	case ClockChanged:
		d.From, d.To = modeAsDecoded(d.From), modeAsDecoded(d.To)
		return d
	case Advisory:
		d.Resource, d.Text = arnAsDecoded(d.Resource), wireText(d.Text)
		return d
	case Unclaimed:
		d.Method, d.Host, d.Path = wireText(d.Method), wireText(d.Host), wireText(d.Path)
		d.Reasons = textsAsDecoded(d.Reasons)
		return d
	case Gap:
		return d
	}
	return d
}

// callAsDecoded is asDecoded for an APICall, which Begin keeps as one.
func callAsDecoded(d APICall) APICall {
	d.Op = ref.OperationID{Service: ref.ServiceID(wireText(string(d.Op.Service))), Name: wireText(d.Op.Name)}
	d.RequestID = ids.RequestID(wireText(string(d.RequestID)))
	d.Origin = originAsDecoded(d.Origin)
	d.Principal, d.Mode, d.TraceHeader = wireText(d.Principal), wireText(d.Mode), wireText(d.TraceHeader)
	d.Resources = arnsAsDecoded(d.Resources)
	d.Outcome = outcomeAsDecoded(d.Outcome)
	return d
}

func originAsDecoded(o ref.Origin) ref.Origin {
	switch o := o.(type) {
	case ref.External:
		o.Protocol = ref.ProtocolID(wireText(string(o.Protocol)))
		o.UserAgent = wireText(o.UserAgent)
		return o
	case ref.Internal:
		return o
	}
	return o
}

func outcomeAsDecoded(o Outcome) Outcome {
	switch o := o.(type) {
	case Failed:
		o.Identity.Shape, o.Message = wireText(o.Identity.Shape), wireText(o.Message)
		return o
	case Injected:
		o.Identity.Shape, o.Rule = wireText(o.Identity.Shape), ref.RuleID(wireText(string(o.Rule)))
		return o
	case Pending, OK:
		return o
	}
	return o
}

func modeAsDecoded(m clock.Mode) clock.Mode {
	switch m := m.(type) {
	case clock.Scaled:
		m.AnchorWall = m.AnchorWall.Round(0).UTC()
		return m
	case clock.Running, clock.Frozen:
		return m
	}
	return m
}

func arnAsDecoded(a aws.ARN) aws.ARN {
	return aws.ARN{
		Partition: wireText(a.Partition), Service: wireText(a.Service), Region: wireText(a.Region),
		Account: wireText(a.Account), Resource: wireText(a.Resource),
	}
}

func arnsAsDecoded(as []aws.ARN) []aws.ARN { return mapAsDecoded(as, arnAsDecoded) }

func textsAsDecoded(ss []string) []string { return mapAsDecoded(ss, wireText) }

// mapAsDecoded returns s with f applied to each element: nil when s is empty,
// s itself when f changes nothing, and a copy otherwise, so the caller's
// slice is never written.
func mapAsDecoded[T comparable](s []T, f func(T) T) []T {
	if len(s) == 0 {
		return nil
	}
	var out []T
	for i := range s {
		if d := f(s[i]); d != s[i] {
			if out == nil {
				out = slices.Clone(s)
			}
			out[i] = d
		}
	}
	if out == nil {
		return s
	}
	return out
}

// wireText returns s as JSON carries it: encoding/json writes each byte that
// does not begin a valid UTF-8 sequence as U+FFFD.
func wireText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2*utf8.UTFMax)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}
