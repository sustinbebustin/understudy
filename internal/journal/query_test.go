package journal

import (
	"cmp"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// model is a brute-force journal: every held event in a slice, evicted by
// rescanning. The property tests compare the real journal against it.
type model struct {
	capacity, maxBytes int
	next               ref.EventID
	open               map[ref.EventID]Event
	committed          []Event // recording order
}

func (m *model) held(id ref.EventID) (Event, bool) {
	if e, ok := m.open[id]; ok {
		return e, true
	}
	for i := range m.committed {
		if m.committed[i].ID == id {
			return m.committed[i], true
		}
	}
	return Event{}, false
}

// stamp applies the journal's documented stamping rules.
func (m *model) stamp(e Event) Event {
	e.ID = m.next
	m.next++
	if e.Cause.Trace == 0 {
		e.Cause.Trace = e.ID
		if p, ok := m.held(e.Cause.Parent); e.Cause.Parent != 0 && ok {
			e.Cause.Trace = p.Cause.Trace
		}
	}
	if e.At.IsZero() {
		e.At = epoch
	}
	e.Wall = wall0
	e.Links = nilIfEmpty(e.Links)
	if u, ok := e.Detail.(Unclaimed); ok {
		u.Reasons = nilIfEmpty(u.Reasons)
		e.Detail = u
	}
	return e
}

// touch returns have extended with the ARNs of add it lacks, in order, but
// never the zero ARN; nil when that leaves none.
func touch(have, add []aws.ARN) []aws.ARN {
	out := slices.Clip(nilIfEmpty(have))
	for _, a := range add {
		if a != (aws.ARN{}) && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func (m *model) commit(e Event) {
	m.committed = append(m.committed, e)
	for {
		total := 0
		for i := range m.committed {
			total += Estimate(m.committed[i])
		}
		if len(m.committed) == 0 || (len(m.committed) <= m.capacity && total <= m.maxBytes) {
			return
		}
		m.committed = m.committed[1:]
	}
}

// expect returns the held events f selects, by brute force, in id order.
func (m *model) expect(f Filter) []Event {
	var out []Event
	for id := range m.open {
		if oracle(f, m.open[id]) {
			out = append(out, m.open[id])
		}
	}
	for i := range m.committed {
		if oracle(f, m.committed[i]) {
			out = append(out, m.committed[i])
		}
	}
	slices.SortFunc(out, func(a, b Event) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// oracle restates the Filter semantics without the package's helpers.
func oracle(f Filter, e Event) bool {
	call, isCall := e.Detail.(APICall)
	var about []aws.ARN
	var result string
	switch d := e.Detail.(type) {
	case APICall:
		about = d.Resources
		switch d.Outcome.(type) {
		case Pending:
			result = "pending"
		case OK:
			result = "ok"
		case Failed, Injected:
			result = "error"
		}
	case Delivery:
		about = []aws.ARN{d.From, d.To}
		switch o := d.Outcome.(type) {
		case Delivered, Filtered:
			result = "ok"
		case DeadLettered:
			about = append(about, o.DLQ)
			result = "error"
		case Retrying, Dropped:
			result = "error"
		}
	case Invocation:
		about = []aws.ARN{d.Function}
		result = "error"
		if _, ok := d.Outcome.(InvocationOK); ok {
			result = "ok"
		}
	case StateChange:
		about = []aws.ARN{d.Resource}
	case Advisory:
		about = []aws.ARN{d.Resource}
	case Unclaimed:
		result = "error"
	case TimerFired, FaultInjected, ClockChanged, Gap:
	}
	anyARN := func(pred func(aws.ARN) bool) bool {
		// The zero ARN is no resource.
		return slices.ContainsFunc(about, func(a aws.ARN) bool { return a != aws.ARN{} && pred(a) })
	}
	if f.Service != (Service{}) {
		if isCall && call.Op.Service != f.Service.ID {
			return false
		}
		if !isCall && !anyARN(func(a aws.ARN) bool { return a.Service == f.Service.ARNNamespace }) {
			return false
		}
	}
	if f.ResourceGlob != "" {
		re := globRegexp(f.ResourceGlob)
		if !anyARN(func(a aws.ARN) bool { return re.MatchString(a.String()) }) {
			return false
		}
	}
	return (f.Trace == 0 || e.Cause.Trace == f.Trace) &&
		(f.RequestID == "" || isCall && call.RequestID == f.RequestID) &&
		(f.Operation == ref.OperationID{} || isCall && call.Op == f.Operation) &&
		(f.Principal == "" || isCall && call.Principal == f.Principal) &&
		(f.Kind == 0 || KindOf(e.Detail) == f.Kind) &&
		(f.Outcome == 0 || f.Outcome.String() == result) &&
		(f.EmuRange.From.IsZero() || !e.At.Before(f.EmuRange.From)) &&
		(f.EmuRange.To.IsZero() || e.At.Before(f.EmuRange.To)) &&
		(f.IDRange.From == 0 || e.ID >= f.IDRange.From) &&
		(f.IDRange.To == 0 || e.ID < f.IDRange.To)
}

// genAt draws emulator times from a few minutes, so ranges select some events.
func genAt() *rapid.Generator[clock.Instant] {
	return rapid.Custom(func(t *rapid.T) clock.Instant {
		k := rapid.IntRange(-1, 5).Draw(t, "minute")
		if k < 0 {
			return clock.Instant{}
		}
		return epoch.Add(time.Duration(k) * time.Minute)
	})
}

func genEventID(upTo ref.EventID) *rapid.Generator[ref.EventID] {
	return rapid.Custom(func(t *rapid.T) ref.EventID {
		return ref.EventID(rapid.Uint64Range(0, uint64(upTo)).Draw(t, "id"))
	})
}

// genLinks draws up to two links to issued ids, those below next; none when
// no id was issued.
func genLinks(next ref.EventID) *rapid.Generator[[]ref.EventID] {
	if next <= 1 {
		return rapid.Just[[]ref.EventID](nil)
	}
	link := rapid.Map(rapid.Uint64Range(1, uint64(next-1)), func(id uint64) ref.EventID { return ref.EventID(id) })
	return rapid.SliceOfN(link, 0, 2)
}

func genFilter(next ref.EventID) *rapid.Generator[Filter] {
	return rapid.Custom(func(t *rapid.T) Filter {
		set := func(label string) bool { return rapid.IntRange(0, 3).Draw(t, label) == 0 }
		var f Filter
		if set("trace?") {
			f.Trace = genEventID(next).Draw(t, "trace")
		}
		if set("requestId?") {
			f.RequestID = rapid.SampledFrom(poolRequestIDs[:3]).Draw(t, "requestId")
		}
		if set("service?") {
			f.Service = rapid.SampledFrom(poolServices).Draw(t, "service")
		}
		if set("operation?") {
			f.Operation = ref.OperationID{Service: rapid.SampledFrom(poolServices).Draw(t, "opService").ID, Name: rapid.SampledFrom(poolOperations).Draw(t, "opName")}
		}
		if set("resource?") {
			f.ResourceGlob = rapid.OneOf(
				rapid.Custom(func(t *rapid.T) string { return genARN().Draw(t, "exact").String() }),
				rapid.SampledFrom([]string{
					"arn:aws:sqs:*", "*orders*", "*:billing", "*", "arn:aws:*:us-east-1:000000000000:orders-*",
					"arn:aws:sqs:us-east-1:000000000000:orders-???", "*:?rders", "arn:aws:???:*",
				}),
			).Draw(t, "resource")
		}
		if set("kind?") {
			f.Kind = Kind(rapid.Uint8Range(uint8(KindAPICall), uint8(KindGap)).Draw(t, "kind"))
		}
		if set("outcome?") {
			f.Outcome = Result(rapid.Uint8Range(uint8(ResultOK), uint8(ResultPending)).Draw(t, "outcome"))
		}
		if set("principal?") {
			f.Principal = rapid.SampledFrom(poolPrincipals[:2]).Draw(t, "principal")
		}
		if set("emu?") {
			f.EmuRange = EmuRange{From: genAt().Draw(t, "emuFrom"), To: genAt().Draw(t, "emuTo")}
		}
		if set("ids?") {
			f.IDRange = IDRange{From: genEventID(next+1).Draw(t, "idFrom"), To: genEventID(next+1).Draw(t, "idTo")}
		}
		f.Limit = rapid.IntRange(0, 7).Draw(t, "limit")
		return f
	})
}

// queryAll pages through every result of f.
func queryAll(tb rapid.TB, j *Journal, f Filter) []Event {
	tb.Helper()
	var out []Event
	for pages := 0; ; pages++ {
		p := j.Query(f)
		limit := f.Limit
		if limit == 0 {
			limit = DefaultLimit
		}
		if len(p.Events) > limit {
			tb.Fatalf("page of %d events exceeds Limit %d", len(p.Events), limit)
		}
		out = append(out, p.Events...)
		if p.Next == 0 {
			return out
		}
		if pages > 10_000 {
			tb.Fatalf("Query never ends: cursor %d", p.Next)
		}
		f.Cursor = p.Next
	}
}

func TestQueryMatchesBruteForce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := &model{
			capacity: rapid.IntRange(1, 40).Draw(t, "capacity"),
			maxBytes: rapid.IntRange(300, 12_000).Draw(t, "maxBytes"),
			next:     1,
			open:     map[ref.EventID]Event{},
		}
		j := newTestJournal(t, Options{Capacity: m.capacity, MaxBytes: m.maxBytes})
		header := func(t *rapid.T) Event {
			return Event{
				Cause: ref.Cause{Parent: genEventID(m.next-1).Draw(t, "parent")},
				Links: genLinks(m.next).Draw(t, "links"),
				At:    genAt().Draw(t, "at"),
				Scope: scope(),
			}
		}
		steps := rapid.IntRange(0, 60).Draw(t, "steps")
		for range steps {
			switch rapid.IntRange(0, 2).Draw(t, "op") {
			case 0:
				e := header(t)
				e.Detail = genAppendable().Draw(t, "detail")
				want := m.stamp(e)
				if got := mustAppend(t, j, e); got != want.ID {
					t.Fatalf("Append returned id %d, want %d", got, want.ID)
				}
				m.commit(want)
			case 1:
				e := header(t)
				call := genAPICall(genText(), genOutcome()).Draw(t, "call")
				e.Detail = call
				call.Outcome, call.Latency = Pending{}, 0
				call.Resources = touch(nil, call.Resources)
				if n := len(call.Input.JSON()); n > m.maxBytes/16 {
					marker, err := NewRedacted(fmt.Appendf(nil, `{"$truncated":%d}`, n))
					if err != nil {
						t.Fatal(err)
					}
					call.Input = marker
				}
				want := m.stamp(e)
				want.Detail = call
				if got := mustBegin(t, j, e); got != want.ID {
					t.Fatalf("Begin returned id %d, want %d", got, want.ID)
				}
				m.open[want.ID] = want
			case 2:
				if len(m.open) == 0 {
					continue
				}
				openIDs := slices.Sorted(func(yield func(ref.EventID) bool) {
					for id := range m.open {
						if !yield(id) {
							return
						}
					}
				})
				id := rapid.SampledFrom(openIDs).Draw(t, "end")
				outcome := genEnded().Draw(t, "outcome")
				latency := time.Duration(rapid.Int64Range(0, int64(time.Second)).Draw(t, "latency"))
				touched := rapid.SliceOfN(genARN(), 0, 3).Draw(t, "touched")
				if err := j.End(id, outcome, latency, touched...); err != nil {
					t.Fatalf("End(%d): %v", id, err)
				}
				e := m.open[id]
				call, ok := e.Detail.(APICall)
				if !ok {
					t.Fatalf("model holds a non-call open event %d", id)
				}
				call.Resources = touch(call.Resources, touched)
				call.Outcome, call.Latency = outcome, latency
				e.Detail = call
				delete(m.open, id)
				m.commit(e)
			}
		}
		for i := range 8 {
			f := genFilter(m.next).Draw(t, fmt.Sprintf("filter%d", i))
			got, want := queryAll(t, j, f), m.expect(f)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Query(%+v)\n got ids %v\nwant ids %v", f, eventIDs(got), eventIDs(want))
			}
		}
	})
}

func eventIDs(es []Event) []ref.EventID {
	out := make([]ref.EventID, len(es))
	for i := range es {
		out[i] = es[i].ID
	}
	return out
}

func TestEvictionKeepsNewestEventsWithinBounds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		capacity := rapid.IntRange(1, 30).Draw(t, "capacity")
		maxBytes := rapid.IntRange(1, 10_000).Draw(t, "maxBytes")
		j := newTestJournal(t, Options{Capacity: capacity, MaxBytes: maxBytes})
		n := rapid.IntRange(0, 80).Draw(t, "events")
		appended := make([]Event, 0, n)
		for range n {
			e := Event{Scope: scope(), Detail: genAppendable().Draw(t, "detail")}
			e.ID = mustAppend(t, j, e)
			appended = append(appended, e)
		}
		kept := queryAll(t, j, Filter{Limit: 1 << 30})
		total := 0
		for _, e := range kept {
			total += Estimate(e)
		}
		if total > maxBytes || len(kept) > capacity {
			t.Fatalf("kept %d events of %d bytes; bounds are %d events and %d bytes", len(kept), total, capacity, maxBytes)
		}
		suffix := eventIDs(appended[len(appended)-len(kept):])
		if !slices.Equal(eventIDs(kept), suffix) {
			t.Fatalf("kept ids %v, want the newest %v", eventIDs(kept), suffix)
		}
		// Nothing was evicted that fits: the next older event would break a bound.
		if n := len(appended) - len(kept); n > 0 {
			older := Estimate(appended[n-1])
			if len(kept)+1 <= capacity && total+older <= maxBytes {
				t.Fatalf("event %d was evicted although %d events of %d bytes fit the bounds", appended[n-1].ID, len(kept)+1, total+older)
			}
		}
	})
}

func TestQueryAfterTheLastPossibleIDIsEmpty(t *testing.T) {
	// The cursor can come from an admin query parameter.
	j := newTestJournal(t, Options{})
	mustAppend(t, j, timerEvent())
	if page := j.Query(Filter{Cursor: math.MaxUint64}); len(page.Events) != 0 || page.Next != 0 {
		t.Fatalf("Query after cursor %d = %v ids, next %d; want an empty last page", uint64(math.MaxUint64), eventIDs(page.Events), page.Next)
	}
}

func TestServiceFilterMatchesOtherKindsByARNNamespace(t *testing.T) {
	// EventBridge's operations carry the id "eventbridge"; its ARNs say "events".
	j := newTestJournal(t, Options{})
	rule, queue := arn("events", "rule/nightly"), arn("sqs", "orders")
	e := callEvent(0)
	call, ok := e.Detail.(APICall)
	if !ok {
		t.Fatal("callEvent is not an APICall")
	}
	call.Op, call.Resources, call.Outcome = ref.OperationID{Service: "eventbridge", Name: "PutRule"}, []aws.ARN{rule}, OK{}
	e.Detail = call
	put := mustRecord(t, j, e)
	deliver := mustAppend(t, j, Event{Scope: scope(), Detail: Delivery{
		From: rule, To: queue, Mechanism: delivery.EventsToSQS, Attempt: 1, Outcome: Delivered{},
	}})
	for _, c := range []struct {
		service Service
		want    []ref.EventID
	}{
		{Service{ID: "eventbridge", ARNNamespace: "events"}, []ref.EventID{put, deliver}},
		{Service{ID: "sqs", ARNNamespace: "sqs"}, []ref.EventID{deliver}},
		{Service{ID: "events", ARNNamespace: "eventbridge"}, nil},
	} {
		if got := eventIDs(j.Query(Filter{Service: c.service}).Events); !slices.Equal(got, c.want) {
			t.Errorf("Query(Service %+v) = %v, want %v", c.service, got, c.want)
		}
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"", "", true},
		{"*", "", true},
		{"*", "arn:aws:sqs:us-east-1:000000000000:orders", true},
		{"arn:aws:sqs:*:*:orders-*", "arn:aws:sqs:us-east-1:000000000000:orders-dlq", true},
		{"arn:aws:sqs:*:*:orders-*", "arn:aws:sqs:us-east-1:000000000000:orders", false},
		{"*orders", "arn:aws:sqs:us-east-1:000000000000:orders", true},
		{"*orders", "arn:aws:sqs:us-east-1:000000000000:orders-dlq", false},
		{"a*b*c", "aXbYbZc", true},
		{"a*b*c", "aXbYbZ", false},
		{"arn:aws:s3:::bucket/*", "arn:aws:s3:::bucket/a/b.txt", true},
		{"exact", "exact", true},
		{"exact", "exactly", false},
		{"arn:aws:sqs:us-east-1:*:orders-?", "arn:aws:sqs:us-east-1:000000000000:orders-1", true},
		{"arn:aws:sqs:us-east-1:*:orders-?", "arn:aws:sqs:us-east-1:000000000000:orders-", false},
		{"arn:aws:sqs:us-east-1:*:orders-?", "arn:aws:sqs:us-east-1:000000000000:orders-12", false},
		{"orders-?", "orders-?", true},
		{"orders-?", "orders-日", true}, // ? is one character, not one byte
		{"*-?", "x-é", true},
		{"orders-??", "orders-日", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

// globRegexp translates a resource glob to an anchored regular expression:
// the independent oracle for globMatch.
func globRegexp(glob string) *regexp.Regexp {
	var re strings.Builder
	re.WriteString(`(?s)\A`)
	for _, r := range glob {
		switch r {
		case '*':
			re.WriteString(".*")
		case '?':
			re.WriteString(".")
		default:
			re.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	re.WriteString(`\z`)
	return regexp.MustCompile(re.String())
}

// genGlobAndARN draws an ARN and a non-empty glob over a small alphabet, so
// matches are common; é and 日 exercise multi-byte characters.
func genGlobAndARN(t *rapid.T) (glob, s string) {
	char := rapid.SampledFrom([]rune{'a', 'b', ':', '/', '-', 'é', '日'})
	s = "arn:aws:sqs:" + string(rapid.SliceOfN(char, 0, 10).Draw(t, "arn"))
	globChar := rapid.OneOf(char, rapid.Just('*'), rapid.Just('?'))
	glob = string(rapid.SliceOfN(globChar, 1, 12).Draw(t, "glob"))
	if rapid.Bool().Draw(t, "anchored") {
		glob = "arn:aws:sqs:" + glob
	}
	return glob, s
}

func TestGlobMatchAgreesWithReferenceMatcher(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		glob, s := genGlobAndARN(t)
		if got, want := globMatch(glob, s), globRegexp(glob).MatchString(s); got != want {
			t.Fatalf("globMatch(%q, %q) = %v, reference says %v", glob, s, got, want)
		}
	})
}

// A user who finds a fault rule's resource glob firing pastes it into
// `understudy journal --resource` to find the calls it hit, so both must
// read a glob the same way.
func TestResourceGlobMatchesWhatAFaultRuleGlobMatches(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		glob, s := genGlobAndARN(t)
		if got, want := globMatch(glob, s), fault.NewARNGlob(glob).Match(s); got != want {
			t.Fatalf("journal glob %q on %q = %v, fault rule glob = %v", glob, s, got, want)
		}
	})
}

func TestQueryByResourceGlobWithQuestionMark(t *testing.T) {
	j := newTestJournal(t, Options{})
	record := func(name string) ref.EventID {
		return mustAppend(t, j, Event{Scope: scope(), Detail: StateChange{Resource: arn("sqs", name)}})
	}
	one, two, twelve := record("orders-1"), record("orders-2"), record("orders-12")
	for _, c := range []struct {
		glob string
		want []ref.EventID
	}{
		{"arn:aws:sqs:us-east-1:*:orders-?", []ref.EventID{one, two}},
		// A ? without a * still makes a glob, not an exact ARN lookup.
		{"arn:aws:sqs:us-east-1:000000000000:orders-?", []ref.EventID{one, two}},
		{"arn:aws:sqs:us-east-1:000000000000:orders-1?", []ref.EventID{twelve}},
		{"arn:aws:sqs:us-east-1:000000000000:orders-??", []ref.EventID{twelve}},
	} {
		if got := eventIDs(j.Query(Filter{ResourceGlob: c.glob}).Events); !slices.Equal(got, c.want) {
			t.Errorf("Query(ResourceGlob %q) = %v, want %v", c.glob, got, c.want)
		}
	}
}
