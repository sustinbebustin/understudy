package journal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func TestBeginOpensACallThatEndRecords(t *testing.T) {
	j := newTestJournal(t, Options{})
	queue, dlq := arn("sqs", "orders"), arn("sqs", "orders-dlq")
	e := callEvent(0)
	call, ok := e.Detail.(APICall)
	if !ok {
		t.Fatal("callEvent is not an APICall")
	}
	call.Resources = []aws.ARN{{}, queue} // the zero ARN is no resource and is dropped
	call.Outcome = OK{}                   // Begin replaces whatever outcome the caller set
	e.Detail = call
	id := mustBegin(t, j, e)

	open := j.Query(Filter{Outcome: ResultPending})
	if len(open.Events) != 1 || open.Events[0].ID != id {
		t.Fatalf("pending calls = %v, want the open call %d", eventIDs(open.Events), id)
	}
	got := open.Events[0]
	if got.At != epoch || got.Wall != wall0 || got.Cause.Trace != id {
		t.Errorf("open call stamped At %v, Wall %v, Trace %d; want %v, %v, its own id", got.At, got.Wall, got.Cause.Trace, epoch, wall0)
	}

	failed := Failed{Identity: ErrorIdentity{Shape: "com.amazonaws.sqs#QueueDoesNotExist"}, Status: 400, Message: "The specified queue does not exist."}
	if err := j.End(id, failed, 2*time.Millisecond, dlq, aws.ARN{}, queue, dlq); err != nil {
		t.Fatalf("End: %v", err)
	}
	page := j.Query(Filter{ResourceGlob: dlq.String()})
	if len(page.Events) != 1 {
		t.Fatalf("Query by a touched ARN found %d events, want the ended call", len(page.Events))
	}
	ended, ok := page.Events[0].Detail.(APICall)
	if !ok {
		t.Fatalf("ended event is %s, want an APICall", detailName(page.Events[0].Detail))
	}
	if !reflect.DeepEqual(ended.Resources, []aws.ARN{queue, dlq}) || ended.Outcome != failed || ended.Latency != 2*time.Millisecond {
		t.Errorf("ended call = %+v; want resources [queue dlq], the Failed outcome and 2ms", ended)
	}
	if n := len(j.Query(Filter{Outcome: ResultPending}).Events); n != 0 {
		t.Errorf("%d calls still pending after End", n)
	}
}

func TestEndRefusesWhatItCannotApply(t *testing.T) {
	j := newTestJournal(t, Options{})
	id := mustBegin(t, j, callEvent(0))
	colonRegion := arn("sqs", "orders")
	colonRegion.Region = "us-east-1:x"
	cases := []struct {
		name    string
		id      ref.EventID
		outcome Outcome
		touched []aws.ARN
		reason  EndReason
	}{
		{"nil outcome", id, nil, nil, NoOutcome},
		{"pending outcome", id, Pending{}, nil, NoOutcome},
		{"unknown id", 999, OK{}, nil, NotOpen},
		{"touched ARN without service", id, OK{}, []aws.ARN{{Partition: "aws", Resource: "q"}}, UnreadableTouched},
		{"touched ARN with a colon in the region", id, OK{}, []aws.ARN{arn("sqs", "orders"), colonRegion}, UnreadableTouched},
	}
	for _, c := range cases {
		var endErr *EndError
		if err := j.End(c.id, c.outcome, 0, c.touched...); !errors.As(err, &endErr) || endErr.Reason != c.reason {
			t.Errorf("%s: End error = %v, want reason %d", c.name, err, c.reason)
		}
	}
	if n := len(j.Query(Filter{Outcome: ResultPending}).Events); n != 1 {
		t.Fatalf("%d calls pending after refused Ends, want the call still open", n)
	}
	if err := j.End(id, OK{}, 0); err != nil {
		t.Fatalf("End after refusals: %v", err)
	}
	var endErr *EndError
	if err := j.End(id, OK{}, 0); !errors.As(err, &endErr) || endErr.Reason != NotOpen {
		t.Errorf("second End error = %v, want NotOpen", err)
	}
	appended := mustAppend(t, j, timerEvent())
	if err := j.End(appended, OK{}, 0); !errors.As(err, &endErr) || endErr.Reason != NotOpen {
		t.Errorf("End of an appended event error = %v, want NotOpen", err)
	}
}

func TestBeginRefusesANonCall(t *testing.T) {
	// A kernel that Begins a delivery learns at once, not at End.
	j := newTestJournal(t, Options{})
	var recErr *RecordError
	if id, err := j.Begin(timerEvent()); !errors.As(err, &recErr) || recErr.Reason != NotACall || recErr.Kind != KindTimerFired || id != 0 {
		t.Fatalf("Begin(timer) = %d, %v; want a NotACall *RecordError for a timer-fired event", id, err)
	}
	if next := j.NextID(); next != 1 {
		t.Errorf("NextID after the refusal = %d, want 1", next)
	}
}

func TestLargeCallInputIsTruncatedSoOlderEventsStay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.ndjson")
	j := newTestJournal(t, Options{MaxBytes: 10_000, File: &FileOptions{Path: path}})
	timers := make([]ref.EventID, 0, 5)
	for range 5 {
		timers = append(timers, mustAppend(t, j, timerEvent()))
	}
	body := fmt.Sprintf(`{"Body":%q}`, strings.Repeat("x", 20_000))
	input, err := NewRedacted([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	e := callEvent(0)
	call, ok := e.Detail.(APICall)
	if !ok {
		t.Fatal("callEvent is not an APICall")
	}
	call.Input = input
	e.Detail = call
	id := mustBegin(t, j, e)
	if err := j.End(id, OK{}, 0); err != nil {
		t.Fatal(err)
	}
	held := j.Query(Filter{})
	if want := append(slices.Clone(timers), id); !slices.Equal(eventIDs(held.Events), want) {
		t.Fatalf("memory holds %v after a %d-byte input, want %v", eventIDs(held.Events), len(body), want)
	}
	stored, ok := held.Events[len(held.Events)-1].Detail.(APICall)
	if want := fmt.Sprintf(`{"$truncated":%d}`, len(body)); !ok || string(stored.Input.JSON()) != want {
		t.Errorf("stored input = %s, want %s", stored.Input.JSON(), want)
	}
	heldEqualsFile(t, j, path)
}

func TestEventLargerThanMaxBytesEvictsEveryOlderEvent(t *testing.T) {
	// Eviction keeps to recording order, so an event that is too large on
	// its own, here an advisory's text, goes last.
	j := newTestJournal(t, Options{MaxBytes: 10_000})
	for range 5 {
		mustAppend(t, j, timerEvent())
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	live := j.Stream(ctx, Filter{}, 0)
	big := mustAppend(t, j, Event{Scope: scope(), Detail: Advisory{
		Kind: AdvisoryPolicyWouldDeny, Resource: arn("sqs", "orders"), Text: strings.Repeat("x", 20_000),
	}})
	if got := receive(t, live, 1); got[0].ID != big {
		t.Errorf("stream delivered %d, want the large event %d", got[0].ID, big)
	}
	if held := j.Query(Filter{}).Events; len(held) != 0 {
		t.Fatalf("memory holds %v, want nothing: the large event and every older one are evicted", eventIDs(held))
	}
	after := mustAppend(t, j, timerEvent())
	if held := eventIDs(j.Query(Filter{}).Events); !slices.Equal(held, []ref.EventID{after}) {
		t.Fatalf("memory holds %v, want the event recorded after, %d", held, after)
	}
}

func TestChildInheritsItsParentsTrace(t *testing.T) {
	j := newTestJournal(t, Options{})
	root := mustAppend(t, j, timerEvent())
	child := mustBegin(t, j, callEvent(root))
	grandchild := mustAppend(t, j, Event{Cause: ref.Cause{Parent: child}, Detail: TimerFired{}})
	page := j.Query(Filter{Trace: root})
	if got := eventIDs(page.Events); !slices.Equal(got, []ref.EventID{root, child, grandchild}) {
		t.Fatalf("trace %d holds %v, want %v", root, got, []ref.EventID{root, child, grandchild})
	}
}

func TestQueryPagesWithCursor(t *testing.T) {
	j := newTestJournal(t, Options{})
	for range 5 {
		mustAppend(t, j, timerEvent())
	}
	p1 := j.Query(Filter{Limit: 2})
	p2 := j.Query(Filter{Limit: 2, Cursor: p1.Next})
	p3 := j.Query(Filter{Limit: 2, Cursor: p2.Next})
	if !slices.Equal(eventIDs(p1.Events), []ref.EventID{1, 2}) || !slices.Equal(eventIDs(p2.Events), []ref.EventID{3, 4}) ||
		!slices.Equal(eventIDs(p3.Events), []ref.EventID{5}) || p3.Next != 0 {
		t.Fatalf("pages = %v %v %v (next %d), want [1 2] [3 4] [5] and no next", eventIDs(p1.Events), eventIDs(p2.Events), eventIDs(p3.Events), p3.Next)
	}
}

func TestSeekKeepsIDsMonotonic(t *testing.T) {
	j := newTestJournal(t, Options{})
	mustAppend(t, j, timerEvent())
	if err := j.Seek(500); err != nil {
		t.Fatalf("Seek(500): %v", err)
	}
	if got := j.NextID(); got != 500 {
		t.Fatalf("NextID = %d, want 500", got)
	}
	if got := mustAppend(t, j, timerEvent()); got != 500 {
		t.Fatalf("Append after Seek = %d, want 500", got)
	}
	var seekErr *SeekError
	if err := j.Seek(10); !errors.As(err, &seekErr) || seekErr.Reason != SeekBelowNext || j.NextID() != 501 {
		t.Fatalf("Seek(10) = %v with NextID %d; want a SeekBelowNext *SeekError and 501", err, j.NextID())
	}
}

func TestIDsNeverWrapToZero(t *testing.T) {
	// A hand-edited snapshot manifest can name any journal.next.
	j := newTestJournal(t, Options{})
	var seekErr *SeekError
	if err := j.Seek(math.MaxUint64); !errors.As(err, &seekErr) || seekErr.Reason != SeekPastLastID || j.NextID() != 1 {
		t.Fatalf("Seek(MaxUint64) = %v with NextID %d; want a SeekPastLastID *SeekError and 1", err, j.NextID())
	}
	if err := j.Seek(math.MaxUint64 - 1); err != nil {
		t.Fatalf("Seek(MaxUint64-1): %v", err)
	}
	if got := mustAppend(t, j, timerEvent()); got != math.MaxUint64-1 {
		t.Fatalf("Append = %d, want %d", got, uint64(math.MaxUint64-1))
	}
	var recErr *RecordError
	if id, err := j.Append(timerEvent()); !errors.As(err, &recErr) || recErr.Reason != IDsExhausted || id != 0 {
		t.Fatalf("Append past the last id = %d, %v; want an IDsExhausted *RecordError", id, err)
	}
	if id, err := j.Begin(callEvent(0)); !errors.As(err, &recErr) || recErr.Reason != IDsExhausted || id != 0 {
		t.Fatalf("Begin past the last id = %d, %v; want an IDsExhausted *RecordError", id, err)
	}
	if next := j.NextID(); next != math.MaxUint64 {
		t.Errorf("NextID = %d, want %d", next, uint64(math.MaxUint64))
	}
}

func TestNewRefusesUnusableOptions(t *testing.T) {
	c, err := clock.New(clock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]Options{
		"no clock":          {},
		"negative capacity": {Clock: c, Capacity: -1},
		"negative bytes":    {Clock: c, MaxBytes: -1},
		"negative buffer":   {Clock: c, StreamBuffer: -1},
		"empty file path":   {Clock: c, File: &FileOptions{}},
		"negative rotation": {Clock: c, File: &FileOptions{Path: filepath.Join(t.TempDir(), "j.ndjson"), RotateBytes: -1}},
	}
	for name, o := range cases {
		var optErr *OptionsError
		if _, err := New(o); !errors.As(err, &optErr) {
			t.Errorf("%s: New error = %v, want *OptionsError", name, err)
		}
	}
}

func decodeFile(t rapid.TB, path string) []Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Event
	for e, err := range DecodeNDJSON(f) {
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		out = append(out, e)
	}
	return out
}

func TestFileReceivesEveryRecordedEventInRecordingOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.ndjson")
	j := newTestJournal(t, Options{Capacity: 1, File: &FileOptions{Path: path}})
	parent := mustBegin(t, j, callEvent(0))
	timer := mustAppend(t, j, timerEvent())
	if err := j.End(parent, OK{}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := decodeFile(t, path)
	if ids := eventIDs(got); !slices.Equal(ids, []ref.EventID{timer, parent}) {
		t.Fatalf("file holds %v, want %d then %d although memory keeps one event", ids, timer, parent)
	}
	if call, ok := got[1].Detail.(APICall); !ok || call.Outcome != (OK{}) {
		t.Errorf("file holds the call as %s, want it ended OK", detailName(got[1].Detail))
	}
}

func TestFileRotatesBySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.ndjson")
	probe := timerEvent() // the size of each line the journal will write
	probe.ID, probe.Cause.Trace, probe.At, probe.Wall = 1, 1, epoch, wall0
	line, err := probe.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	// Room for two lines per file.
	j := newTestJournal(t, Options{File: &FileOptions{Path: path, RotateBytes: int64(2*len(line) + 2)}})
	for range 5 {
		mustAppend(t, j, timerEvent())
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rotated, current := decodeFile(t, path+".1"), decodeFile(t, path)
	if !slices.Equal(eventIDs(rotated), []ref.EventID{3, 4}) || !slices.Equal(eventIDs(current), []ref.EventID{5}) {
		t.Fatalf("rotated file holds %v and current %v; want [3 4] and [5]", eventIDs(rotated), eventIDs(current))
	}
}

func TestRecordedEventsEqualTheirFileRecords(t *testing.T) {
	dir := t.TempDir()
	runs := 0
	rapid.Check(t, func(t *rapid.T) {
		runs++
		path := filepath.Join(dir, fmt.Sprintf("journal-%d.ndjson", runs))
		j := newTestJournal(t, Options{File: &FileOptions{Path: path}})
		for i := range rapid.IntRange(1, 8).Draw(t, "events") {
			e := Event{
				Links:  genLinks(j.NextID()).Draw(t, fmt.Sprintf("links%d", i)),
				At:     genAt().Draw(t, fmt.Sprintf("at%d", i)),
				Scope:  scope(),
				Detail: genDetailOf(genByteText()).Filter(func(d Detail) bool { return KindOf(d) != KindGap }).Draw(t, fmt.Sprintf("detail%d", i)),
			}
			mustRecord(t, j, e)
		}
		heldEqualsFile(t, j, path)
	})
}

// heldEqualsFile closes j and checks that every event it holds equals its
// record in the file at path.
func heldEqualsFile(t rapid.TB, j *Journal, path string) {
	t.Helper()
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	held := map[ref.EventID]Event{}
	events := j.Query(Filter{Limit: 1 << 30}).Events
	open := 0 // calls held but not yet written
	for i := range events {
		held[events[i].ID] = events[i]
		if r, ok := ResultOf(events[i].Detail); ok && r == ResultPending {
			open++
		}
	}
	fromFile := decodeFile(t, path)
	if len(fromFile) != len(events)-open {
		t.Fatalf("the file holds %d events, want the %d recorded ones memory holds", len(fromFile), len(events)-open)
	}
	for i := range fromFile {
		if recorded := held[fromFile[i].ID]; !reflect.DeepEqual(recorded, fromFile[i]) {
			t.Fatalf("event %d differs from its file record\nrecorded %#v\n    file %#v", fromFile[i].ID, recorded, fromFile[i])
		}
	}
}

func TestRecordingStoresTextThatIsNotUTF8AsTheWireCarriesIt(t *testing.T) {
	// The edge records the host and path of a raw request unclaimed; JSON
	// writes each byte that is not UTF-8 as U+FFFD.
	path := filepath.Join(t.TempDir(), "journal.ndjson")
	j := newTestJournal(t, Options{File: &FileOptions{Path: path}})
	unclaimed := mustAppend(t, j, Event{Detail: Unclaimed{Method: "GET", Host: "h\xc3", Path: "/\xff\xfe", Reasons: []string{"no route for \xff"}}})
	mustAppend(t, j, Event{Detail: StateChange{Resource: arn("sqs", "q\xff"), From: "Pending", To: "Active"}})
	call := callEvent(0)
	call.Detail = APICall{
		Op:     ref.OperationID{Service: "sqs", Name: "Get\xff"},
		Origin: ref.External{Protocol: ref.AWSQuery, Listener: ref.ListenerMain, UserAgent: "ua\xff"},
	}
	id := mustBegin(t, j, call)
	if err := j.End(id, Failed{Identity: ErrorIdentity{Shape: "s\xff"}, Status: 400, Message: "queue \xff"}, 0, arn("sqs", "touched\xff")); err != nil {
		t.Fatal(err)
	}
	page := j.Query(Filter{IDRange: IDRange{From: unclaimed, To: unclaimed + 1}})
	if len(page.Events) != 1 {
		t.Fatalf("Query of event %d found %d events, want 1", unclaimed, len(page.Events))
	}
	if u, ok := page.Events[0].Detail.(Unclaimed); !ok || u.Path != "/��" || u.Host != "h�" {
		t.Errorf("unclaimed request recorded as %s; want host h� and path /��", detailName(page.Events[0].Detail))
	}
	heldEqualsFile(t, j, path)
}

func TestRecordedClockChangeEqualsItsFileRecord(t *testing.T) {
	// clock.Scale anchors a scaled clock at time.Now: local, with a
	// monotonic reading.
	path := filepath.Join(t.TempDir(), "journal.ndjson")
	j := newTestJournal(t, Options{File: &FileOptions{Path: path}})
	mustAppend(t, j, Event{Detail: ClockChanged{
		From: clock.Scaled{Factor: 2, AnchorWall: time.Now().In(time.Local), AnchorEmu: epoch},
		To:   clock.Scaled{Factor: 3, AnchorWall: time.Now().In(time.FixedZone("UTC+5", 5*3600)), AnchorEmu: epoch},
	}})
	heldEqualsFile(t, j, path)
}

func TestFileReopenedAfterATornRecordKeepsLaterEventsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.ndjson")
	j := newTestJournal(t, Options{File: &FileOptions{Path: path}})
	first := mustAppend(t, j, timerEvent())
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A crash mid-write leaves the start of a record without its newline.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":2,"cause":{"trace":2},"at":"2026-`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := newTestJournal(t, Options{File: &FileOptions{Path: path}})
	if err := restarted.Seek(3); err != nil {
		t.Fatal(err)
	}
	later := []ref.EventID{mustAppend(t, restarted, timerEvent()), mustAppend(t, restarted, timerEvent())}
	if err := restarted.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var decoded []ref.EventID
	var badLines []int
	for e, err := range DecodeNDJSON(r) {
		var decodeErr *DecodeError
		switch {
		case errors.As(err, &decodeErr):
			badLines = append(badLines, decodeErr.Line)
		case err != nil:
			t.Fatalf("reading %s: %v", path, err)
		default:
			decoded = append(decoded, e.ID)
		}
	}
	if want := append([]ref.EventID{first}, later...); !slices.Equal(decoded, want) || !slices.Equal(badLines, []int{2}) {
		t.Fatalf("decoded %v with bad lines %v; want %v and only the torn line 2 bad", decoded, badLines, want)
	}
}

func TestRecordingRefusesEventsNoConsumerCouldRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.ndjson")
	j := newTestJournal(t, Options{File: &FileOptions{Path: path}})
	call := callEvent(0)
	unissued := timerEvent()
	unissued.Cause.Parent = 7
	foreignTrace := timerEvent()
	foreignTrace.Cause.Trace = 7
	futureLink := timerEvent()
	futureLink.Links = []ref.EventID{999}
	zeroLink := timerEvent()
	zeroLink.Links = []ref.EventID{0}
	selfLink := timerEvent()
	selfLink.Links = []ref.EventID{1} // the id this event would get
	noPartition := aws.ARN{Service: "sqs", Resource: "q"}
	colonAccount := arn("sqs", "orders")
	colonAccount.Account = "0000:1"
	touchingBadARN := Event{Scope: scope(), Detail: APICall{
		Op: ref.OperationID{Service: "sqs", Name: "SendMessage"}, Origin: ref.Internal{Via: ref.SNSDelivery}, Resources: []aws.ARN{colonAccount},
	}}
	pastRFC3339 := clock.FromWire(time.Date(10001, 1, 1, 0, 0, 0, 0, time.UTC))
	farFuture := timerEvent()
	farFuture.At = pastRFC3339
	fn := arn("lambda", "function:worker")
	cases := []struct {
		name   string
		record func() (ref.EventID, error)
		reason RecordReason
	}{
		{"Append of a call", func() (ref.EventID, error) { return j.Append(call) }, CallNeedsBegin},
		{"Append of a gap", func() (ref.EventID, error) { return j.Append(Event{Detail: Gap{Missed: 1}}) }, GapNotRecordable},
		{"Begin of a gap", func() (ref.EventID, error) { return j.Begin(Event{Detail: Gap{Missed: 1}}) }, NotACall},
		{"Begin of a timer", func() (ref.EventID, error) { return j.Begin(timerEvent()) }, NotACall},
		{"nil detail", func() (ref.EventID, error) { return j.Append(Event{}) }, NoWireForm},
		{"delivery without outcome", func() (ref.EventID, error) { return j.Append(Event{Detail: Delivery{Attempt: 1}}) }, NoWireForm},
		{"invocation without outcome", func() (ref.EventID, error) { return j.Append(Event{Detail: Invocation{}}) }, NoWireForm},
		{"clock change without modes", func() (ref.EventID, error) { return j.Append(Event{Detail: ClockChanged{}}) }, NoWireForm},
		{"delivery without mechanism", func() (ref.EventID, error) {
			return j.Append(Event{Detail: Delivery{Attempt: 1, Outcome: Delivered{}}})
		}, NoWireForm},
		{"advisory without kind", func() (ref.EventID, error) { return j.Append(Event{Detail: Advisory{}}) }, NoWireForm},
		{"scaled clock with NaN factor", func() (ref.EventID, error) {
			return j.Append(Event{Detail: ClockChanged{From: clock.Running{}, To: clock.Scaled{Factor: math.NaN()}}})
		}, NoWireForm},
		{"call without origin", func() (ref.EventID, error) { return j.Begin(Event{Detail: APICall{}}) }, NoWireForm},
		{"ARN without partition", func() (ref.EventID, error) {
			return j.Append(Event{Detail: StateChange{Resource: noPartition, From: "Pending", To: "Active"}})
		}, NoWireForm},
		{"ARN with a colon in the account", func() (ref.EventID, error) {
			return j.Append(Event{Detail: Advisory{Kind: AdvisoryPolicyWouldDeny, Resource: colonAccount}})
		}, NoWireForm},
		{"call touching a malformed ARN", func() (ref.EventID, error) { return j.Begin(touchingBadARN) }, NoWireForm},
		{"instant past year 9999", func() (ref.EventID, error) { return j.Append(farFuture) }, NoWireForm},
		{"retry past year 9999", func() (ref.EventID, error) {
			return j.Append(Event{Detail: Delivery{From: fn, To: fn, Mechanism: delivery.SNSToLambda, Attempt: 2, Outcome: Retrying{Next: pastRFC3339}}})
		}, NoWireForm},
		{"frozen clock past year 9999", func() (ref.EventID, error) {
			return j.Append(Event{Detail: ClockChanged{From: clock.Running{}, To: clock.Frozen{At: pastRFC3339}}})
		}, NoWireForm},
		{"scaled clock anchored past year 9999", func() (ref.EventID, error) {
			return j.Append(Event{Detail: ClockChanged{From: clock.Running{}, To: clock.Scaled{Factor: 2, AnchorWall: wall0, AnchorEmu: pastRFC3339}}})
		}, NoWireForm},
		{"unissued parent", func() (ref.EventID, error) { return j.Append(unissued) }, UnissuedID},
		{"unissued trace", func() (ref.EventID, error) { return j.Append(foreignTrace) }, UnissuedID},
		{"call with unissued parent", func() (ref.EventID, error) { return j.Begin(callEvent(7)) }, UnissuedID},
		{"link to an unissued id", func() (ref.EventID, error) { return j.Append(futureLink) }, UnissuedID},
		{"link to event 0", func() (ref.EventID, error) { return j.Append(zeroLink) }, UnissuedID},
		{"link to itself", func() (ref.EventID, error) { return j.Append(selfLink) }, UnissuedID},
	}
	for _, c := range cases {
		var recErr *RecordError
		if id, err := c.record(); !errors.As(err, &recErr) || recErr.Reason != c.reason || id != 0 {
			t.Errorf("%s = %d, %v; want a *RecordError with reason %d", c.name, id, err, c.reason)
		}
	}
	if next := j.NextID(); next != 1 {
		t.Errorf("NextID after refusals = %d, want 1: a refusal uses no id", next)
	}
	if n := len(j.Query(Filter{}).Events); n != 0 {
		t.Errorf("memory holds %d events after refusals, want none", n)
	}
	good := mustAppend(t, j, timerEvent())
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ids := eventIDs(decodeFile(t, path)); !slices.Equal(ids, []ref.EventID{good}) {
		t.Fatalf("file holds %v, want only %d", ids, good)
	}
}

// memoryDisk is a journal file in memory whose writes can stall or fail.
type memoryDisk struct {
	stall    chan struct{} // when not nil, every Write waits for a send, which releases one, or release, which releases all
	failFrom int           // when positive, the first Write that fails

	released sync.Once
	mu       sync.Mutex
	writes   int
	data     bytes.Buffer
}

var errDiskFailed = errors.New("disk failed")

func (d *memoryDisk) Write(p []byte) (int, error) {
	if d.stall != nil {
		<-d.stall
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes++
	if d.failFrom > 0 && d.writes >= d.failFrom {
		return 0, errDiskFailed
	}
	return d.data.Write(p)
}

func (d *memoryDisk) Close() error { return nil }

// release lets every stalled and later Write through; calling it again does
// nothing.
func (d *memoryDisk) release() { d.released.Do(func() { close(d.stall) }) }

// stallingJournal opens a journal on a disk whose every Write waits until
// the test releases it. Cleanup releases the disk and closes the journal,
// so a test that stops early reports its own failure, not a synctest
// deadlock of the blocked writer.
func stallingJournal(t *testing.T, o Options) (*Journal, *memoryDisk) {
	t.Helper()
	disk := &memoryDisk{stall: make(chan struct{})}
	j := newJournalOpening(t, o, wall0, disk.open)
	t.Cleanup(func() {
		disk.release()
		// The test checks Close's error itself when it gets that far.
		_ = j.Close()
	})
	return j, disk
}

func (d *memoryDisk) open(string) (file, int64, error) { return d, 0, nil }

// events returns the events written so far, in file order.
func (d *memoryDisk) events(t *testing.T) []Event {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Event
	for e, err := range DecodeNDJSON(bytes.NewReader(d.data.Bytes())) {
		if err != nil {
			t.Fatalf("decode the written file: %v", err)
		}
		out = append(out, e)
	}
	return out
}

// ids returns the ids of the events written so far, in file order.
func (d *memoryDisk) ids(t *testing.T) []ref.EventID {
	t.Helper()
	return eventIDs(d.events(t))
}

// estimateOf returns the Estimate of the recorded event id, which must still
// be in memory.
func estimateOf(t *testing.T, j *Journal, id ref.EventID) int {
	t.Helper()
	page := j.Query(Filter{IDRange: IDRange{From: id, To: id + 1}})
	if len(page.Events) != 1 {
		t.Fatalf("event %d is not in memory", id)
	}
	return Estimate(page.Events[0])
}

// sumEstimates returns the sum of the Estimate of es.
func sumEstimates(es []Event) int {
	n := 0
	for i := range es {
		n += Estimate(es[i])
	}
	return n
}

const memoryPath = "memory.ndjson"

func TestRecordingDoesNotWaitForAStalledDisk(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		j, disk := stallingJournal(t, Options{File: &FileOptions{Path: memoryPath}})
		var recorded []ref.EventID
		var errs []error
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 3 {
				id, err := j.Begin(callEvent(0))
				errs = append(errs, err, j.End(id, OK{}, time.Millisecond))
				recorded = append(recorded, id)
				id, err = j.Append(timerEvent())
				errs = append(errs, err)
				recorded = append(recorded, id)
			}
		}()
		synctest.Wait() // the writer is stalled in its first Write
		select {
		case <-done:
		default:
			t.Fatal("Begin, End and Append are still blocked while the disk is stalled; recording must never wait for the file")
		}
		if err := errors.Join(errs...); err != nil {
			t.Fatalf("recording while the disk is stalled: %v", err)
		}
		if err := j.FileErr(); err != nil {
			t.Fatalf("FileErr while the disk is stalled = %v, want nil: the disk is slow, not failed", err)
		}
		if n := len(j.Query(Filter{}).Events); n != len(recorded) {
			t.Fatalf("memory holds %d events, want the %d recorded", n, len(recorded))
		}
		disk.release()
		if err := j.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got := disk.ids(t); !slices.Equal(got, recorded) {
			t.Fatalf("file holds %v, want %v in recording order", got, recorded)
		}
	})
}

func TestFileStopsWhenTheDiskFallsTooFarBehind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const maxBytes = 10_000
		j, disk := stallingJournal(t, Options{MaxBytes: maxBytes, File: &FileOptions{Path: memoryPath}})
		var fileErr *FileError
		recorded := 0
		for recorded < maxBytes && !errors.As(j.FileErr(), &fileErr) {
			mustAppend(t, j, timerEvent())
			recorded++
		}
		var full *QueueFullError
		if fileErr == nil || fileErr.Op != FileQueue || fileErr.Path != memoryPath || !errors.As(fileErr, &full) {
			t.Fatalf("FileErr after %d events on a stalled disk = %v, want a queue *FileError for %s wrapping a *QueueFullError", recorded, j.FileErr(), memoryPath)
		}
		refused := estimateOf(t, j, ref.EventID(recorded))
		if full.Event != ref.EventID(recorded) || full.Size != refused || full.Limit != maxBytes {
			t.Fatalf("QueueFullError = %+v, want event %d of %d estimated bytes refused under limit %d", full, recorded, refused, maxBytes)
		}
		mustAppend(t, j, timerEvent())
		disk.release()
		if err := j.Close(); !errors.Is(err, fileErr) {
			t.Fatalf("Close = %v, want the error FileErr reported", err)
		}
		written := disk.events(t)
		want := make([]ref.EventID, recorded-1)
		for i := range want {
			want[i] = ref.EventID(i + 1)
		}
		if got := eventIDs(written); !slices.Equal(got, want) {
			t.Fatalf("file holds %v, want every event before the one the full queue refused, 1 to %d, and nothing after", got, recorded-1)
		}
		// Nothing was written before the refusal, so everything the file holds
		// was waiting when event recorded was refused.
		waiting := sumEstimates(written)
		if waiting > maxBytes || waiting+refused <= maxBytes {
			t.Fatalf("the queue refused event %d of %d estimated bytes with %d waiting; want it refused exactly when it would take the queue past MaxBytes %d", recorded, refused, waiting, maxBytes)
		}
		if full.Waiting != waiting {
			t.Fatalf("QueueFullError.Waiting = %d, want the %d estimated bytes the file holds, all waiting at the refusal", full.Waiting, waiting)
		}
	})
}

func TestFileBoundCountsOnlyEventsNotYetWritten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const maxBytes = 10_000
		j, disk := stallingJournal(t, Options{MaxBytes: maxBytes, File: &FileOptions{Path: memoryPath}})
		first := mustAppend(t, j, timerEvent())
		each := estimateOf(t, j, first)
		synctest.Wait() // the writer is stalled writing the first event alone
		waiting := each
		batch := 0
		for waiting+each <= maxBytes {
			waiting += estimateOf(t, j, mustAppend(t, j, timerEvent()))
			batch++
		}
		if err := j.FileErr(); err != nil {
			t.Fatalf("FileErr with %d estimated bytes waiting = %v, want nil: MaxBytes is %d", waiting, err, maxBytes)
		}
		// Release the first event, then every event of the batch the writer
		// takes next but the last, whose Write stays stalled.
		for range batch {
			disk.stall <- struct{}{}
		}
		synctest.Wait()
		if n := len(disk.ids(t)); n != batch {
			t.Fatalf("the disk wrote %d events, want %d", n, batch)
		}

		waiting = each // the last event of the batch, still being written
		var last ref.EventID
		for waiting+each <= maxBytes {
			last = mustAppend(t, j, timerEvent())
			waiting += estimateOf(t, j, last)
			if err := j.FileErr(); err != nil {
				t.Fatalf("FileErr with %d estimated bytes not yet written = %v, want nil: written events no longer count against MaxBytes %d", waiting, err, maxBytes)
			}
		}
		refused := mustAppend(t, j, timerEvent())
		var fileErr *FileError
		if err := j.FileErr(); !errors.As(err, &fileErr) || fileErr.Op != FileQueue {
			t.Fatalf("FileErr after event %d took the backlog past MaxBytes %d = %v, want a queue *FileError", refused, maxBytes, err)
		}
		var full *QueueFullError
		if !errors.As(fileErr, &full) || full.Event != refused || full.Waiting != waiting || full.Limit != maxBytes {
			t.Fatalf("FileErr = %v, want a *QueueFullError for event %d reporting the %d estimated bytes not yet written and limit %d", fileErr, refused, waiting, maxBytes)
		}
		if full.Waiting+full.Size <= full.Limit {
			t.Fatalf("QueueFullError = %+v: the refused event would have fit", full)
		}

		disk.release()
		if err := j.Close(); !errors.Is(err, fileErr) {
			t.Fatalf("Close = %v, want the error FileErr reported", err)
		}
		want := make([]ref.EventID, last)
		for i := range want {
			want[i] = ref.EventID(i + 1)
		}
		if got := disk.ids(t); !slices.Equal(got, want) {
			t.Fatalf("file holds %v, want every event before the refused %d, 1 to %d, and nothing after", got, refused, last)
		}
	})
}

func TestFileErrReportsAStoppedFileWhileRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		disk := &memoryDisk{failFrom: 2}
		j := newJournalOpening(t, Options{File: &FileOptions{Path: memoryPath}}, wall0, disk.open)
		written := mustAppend(t, j, timerEvent())
		synctest.Wait() // the writer has written it
		if err := j.FileErr(); err != nil {
			t.Fatalf("FileErr of a healthy file = %v, want nil", err)
		}
		mustAppend(t, j, timerEvent())
		synctest.Wait() // the writer has failed to write it
		var fileErr *FileError
		if err := j.FileErr(); !errors.As(err, &fileErr) || fileErr.Op != FileWrite || fileErr.Path != memoryPath || !errors.Is(err, errDiskFailed) {
			t.Fatalf("FileErr after a failed write = %v, want a write *FileError for %s wrapping the disk's error", err, memoryPath)
		}
		mustAppend(t, j, timerEvent())
		if n := len(j.Query(Filter{}).Events); n != 3 {
			t.Fatalf("memory holds %d events, want all 3 although the file stopped", n)
		}
		if err := j.Close(); !errors.Is(err, fileErr) {
			t.Fatalf("Close = %v, want the error FileErr reported", err)
		}
		if ids := disk.ids(t); !slices.Equal(ids, []ref.EventID{written}) {
			t.Fatalf("file holds %v, want only %d, written before it stopped", ids, written)
		}
	})
}

func TestEventRecordedAfterCloseIsReportedMissingFromTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.ndjson")
	j := newTestJournal(t, Options{File: &FileOptions{Path: path}})
	written := mustAppend(t, j, timerEvent())
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := j.FileErr(); err != nil {
		t.Fatalf("FileErr after a clean Close = %v, want nil until an event is dropped", err)
	}
	mustAppend(t, j, timerEvent())
	var fileErr *FileError
	if err := j.FileErr(); !errors.As(err, &fileErr) || fileErr.Op != FileWrite || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("FileErr after recording past Close = %v, want a write *FileError wrapping os.ErrClosed", err)
	}
	if n := len(j.Query(Filter{}).Events); n != 2 {
		t.Errorf("memory holds %d events, want both", n)
	}
	if ids := eventIDs(decodeFile(t, path)); !slices.Equal(ids, []ref.EventID{written}) {
		t.Fatalf("file holds %v, want only %d, written before Close", ids, written)
	}
}

func TestNoFileHasNoFileErr(t *testing.T) {
	j := newTestJournal(t, Options{})
	mustAppend(t, j, timerEvent())
	if err := j.FileErr(); err != nil {
		t.Fatalf("FileErr without a file = %v, want nil", err)
	}
}
