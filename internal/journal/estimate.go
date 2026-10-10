package journal

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Per-event bookkeeping the estimate charges on top of the event's own
// strings and slices: the Event and entry structs, the map and list slots
// that index it, and the boxed Detail.
const (
	eventOverhead = 320
	idCost        = 8  // one event id in Links or an index list
	arnCost       = 96 // an aws.ARN's five string headers plus its index slot
	stringCost    = 16 // a string header
)

// Estimate returns the approximate memory, in bytes, the journal spends to
// hold e. Eviction keeps the sum over retained events at or below
// Options.MaxBytes.
func Estimate(e Event) int {
	return eventOverhead + idCost*len(e.Links) + detailSize(e.Detail)
}

func detailSize(d Detail) int {
	switch d := d.(type) {
	case APICall:
		return strs(string(d.Op.Service), d.Op.Name, string(d.RequestID), d.Principal, d.Mode, d.TraceHeader, d.Input.doc) +
			originSize(d.Origin) + arns(d.Resources...) + outcomeSize(d.Outcome)
	case Delivery:
		n := arns(d.From, d.To)
		switch o := d.Outcome.(type) {
		case Filtered:
			n += strs(o.Rule)
		case DeadLettered:
			n += arns(o.DLQ)
		case Dropped:
			n += strs(o.Reason)
		case Delivered, Retrying:
		}
		return n
	case Invocation:
		n := arns(d.Function) + strs(d.Version, d.Env)
		if fe, ok := d.Outcome.(FunctionError); ok {
			n += strs(fe.Type)
		}
		return n
	case TimerFired:
		return strs(string(d.Key.Kind), d.Key.ID)
	case StateChange:
		return arns(d.Resource) + strs(string(d.From), string(d.To))
	case FaultInjected:
		return strs(string(d.Rule))
	case ClockChanged:
		return 0
	case Advisory:
		return arns(d.Resource) + strs(d.Text)
	case Unclaimed:
		return strs(d.Method, d.Host, d.Path) + strs(d.Reasons...)
	case Gap:
		return 0
	}
	return 0
}

func originSize(o ref.Origin) int {
	switch o := o.(type) {
	case ref.External:
		return strs(string(o.Protocol), o.UserAgent)
	case ref.Internal:
		return 0
	}
	return 0
}

func outcomeSize(o Outcome) int {
	switch o := o.(type) {
	case Failed:
		return strs(o.Identity.Shape, o.Message)
	case Injected:
		return strs(o.Identity.Shape, string(o.Rule))
	case Pending, OK:
		return 0
	}
	return 0
}

func strs(ss ...string) int {
	n := 0
	for _, s := range ss {
		n += stringCost + len(s)
	}
	return n
}

func arns(as ...aws.ARN) int {
	n := 0
	for _, a := range as {
		n += arnCost + len(a.Partition) + len(a.Service) + len(a.Region) + len(a.Account) + len(a.Resource)
	}
	return n
}
