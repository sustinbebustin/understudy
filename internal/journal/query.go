package journal

import (
	"math"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// DefaultLimit is the page size of a Query whose Limit is 0.
const DefaultLimit = 100

// Filter selects events. Zero fields select everything; set fields must all
// match. Fields that only an API call carries (RequestID, Operation,
// Principal) never match other kinds.
type Filter struct {
	Trace     ref.EventID     // events whose Cause.Trace is Trace
	RequestID ids.RequestID   // the API call with this request id
	Service   Service         // events of this service; see Service
	Operation ref.OperationID // API calls of this operation
	// ResourceGlob matches the ARNs an event is about: those an API call
	// touched, both ends and the dead-letter queue of a delivery, the
	// function of an invocation, the resource of a state change or advisory.
	// * matches any run of characters, every other character itself.
	ResourceGlob string
	Kind         Kind
	Outcome      Result // see ResultOf; kinds without a result never match
	Principal    string // an API call's rendered principal ARN
	EmuRange     EmuRange
	IDRange      IDRange
	// Limit is the page size of a Query; 0 means DefaultLimit. Streams
	// ignore it.
	Limit int
	// Cursor is Page.Next of the previous page: a Query returns events with
	// ids above it. Streams ignore it.
	Cursor ref.EventID
}

// Service names a service for Filter.Service by both of its names, which
// differ for some services (EventBridge is "eventbridge" with ARNs under
// "events"): an API call matches when its operation's service is ID, any
// other event when an ARN it is about has the service part ARNNamespace. The
// admin layer fills both from the registry (registry.Service.ID and
// ARNNamespace). The zero Service matches every event; an empty ARNNamespace
// matches no ARN.
type Service struct {
	ID           ref.ServiceID
	ARNNamespace string
}

// matches reports whether d belongs to s.
func (s Service) matches(d Detail) bool {
	if call, ok := d.(APICall); ok {
		return call.Op.Service == s.ID
	}
	if s.ARNNamespace == "" {
		return false
	}
	for _, a := range resourcesOf(d) {
		if a.Service == s.ARNNamespace {
			return true
		}
	}
	return false
}

// EmuRange is the half-open emulator-time interval [From, To); a zero bound
// is open.
type EmuRange struct{ From, To clock.Instant }

// IDRange is the half-open event-id interval [From, To); a zero bound is open.
type IDRange struct{ From, To ref.EventID }

// Page is one page of Query results in ascending id order.
type Page struct {
	Events []Event
	// Next is the Cursor of the next page, or 0 when there is none.
	Next ref.EventID
}

// Query returns the retained and open events matching f in ascending id
// order, at most f.Limit of them, after f.Cursor.
func (j *Journal) Query(f Filter) Page {
	if f.Cursor == math.MaxUint64 {
		return Page{} // no id is above it, and Cursor+1 would wrap to 0
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	var page Page
	for _, id := range j.candidates(f).from(max(f.Cursor+1, f.IDRange.From)) {
		if f.IDRange.To != 0 && id >= f.IDRange.To {
			break
		}
		e := j.byID[id].ev
		if !f.matches(e) {
			continue
		}
		if len(page.Events) == limit {
			page.Next = page.Events[limit-1].ID
			break
		}
		page.Events = append(page.Events, e)
	}
	return page
}

// candidates returns the shortest id list that holds every event f can match.
// Called with mu held.
func (j *Journal) candidates(f Filter) idList {
	best := j.all
	consider := func(l idList) {
		if len(l) < len(best) {
			best = l
		}
	}
	if f.Trace != 0 {
		consider(j.index.trace[f.Trace])
	}
	if f.RequestID != "" {
		consider(j.index.request[f.RequestID])
	}
	if f.Operation != (ref.OperationID{}) {
		consider(j.index.op[f.Operation])
	}
	switch {
	case f.ResourceGlob == "":
	case !strings.Contains(f.ResourceGlob, "*"):
		consider(j.index.resource[f.ResourceGlob])
	default:
		if l, ok := j.globbed(f.ResourceGlob, len(best)); ok {
			consider(l)
		}
	}
	return best
}

// globbed returns the ids of the events about an ARN matching glob, found by
// matching the resource index's keys, the distinct ARNs, rather than every
// event. It gives up, returning false, when they number limit or more, since
// a shorter candidate list is already in hand. Called with mu held.
func (j *Journal) globbed(glob string, limit int) (idList, bool) {
	var lists []idList
	total := 0
	for k, l := range j.index.resource {
		if !globMatch(glob, k) {
			continue
		}
		if total += len(l); total >= limit {
			return nil, false
		}
		lists = append(lists, l)
	}
	out := make(idList, 0, total)
	for _, l := range lists {
		out = append(out, l...)
	}
	slices.Sort(out)
	return slices.Compact(out), true
}

// matches reports whether e passes every set field of f other than Limit and
// Cursor.
func (f Filter) matches(e Event) bool {
	call, isCall := e.Detail.(APICall)
	switch {
	case f.Trace != 0 && e.Cause.Trace != f.Trace,
		f.RequestID != "" && (!isCall || call.RequestID != f.RequestID),
		f.Operation != (ref.OperationID{}) && (!isCall || call.Op != f.Operation),
		f.Principal != "" && (!isCall || call.Principal != f.Principal),
		f.Kind != 0 && KindOf(e.Detail) != f.Kind,
		f.Service != (Service{}) && !f.Service.matches(e.Detail),
		f.ResourceGlob != "" && !hasResource(e.Detail, f.ResourceGlob),
		f.IDRange.From != 0 && e.ID < f.IDRange.From,
		f.IDRange.To != 0 && e.ID >= f.IDRange.To,
		!f.EmuRange.From.IsZero() && e.At.Before(f.EmuRange.From),
		!f.EmuRange.To.IsZero() && !e.At.Before(f.EmuRange.To):
		return false
	}
	if f.Outcome != 0 {
		r, ok := ResultOf(e.Detail)
		return ok && r == f.Outcome
	}
	return true
}

func hasResource(d Detail, glob string) bool {
	for _, a := range resourcesOf(d) {
		if globMatch(glob, a.String()) {
			return true
		}
	}
	return false
}

// globMatch reports whether s matches pattern, where * matches any run of
// characters, including none, and every other byte matches itself.
func globMatch(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0 // the last * seen and the s position it resumes from
	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && pattern[p] == s[i]:
			p++
			i++
		case star >= 0:
			mark++
			p, i = star+1, mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
