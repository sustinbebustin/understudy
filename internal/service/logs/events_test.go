package logs_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/logs"
)

const day = 24 * time.Hour

func (h *harness) get(in *logsapi.GetLogEventsInput) *logsapi.GetLogEventsOutput {
	h.Helper()
	return must(h, logsapi.OpGetLogEvents, in)
}

// all reads a stream from the head in one page.
func (h *harness) all(group, stream string) []string {
	h.Helper()
	return messages(h.get(&logsapi.GetLogEventsInput{LogGroupName: new(group), LogStreamName: stream, StartFromHead: new(true)}).Events)
}

func TestPutLogEventsStoresEventsInTimestampOrder(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		now := h.now()
		first := h.put("g", "s", event(now-10, "b"), event(now, "d"))
		if val(first.NextSequenceToken) == "<nil>" || len(*first.NextSequenceToken) != 56 {
			h.Errorf("nextSequenceToken = %q, want 56 digits", val(first.NextSequenceToken))
		}
		if first.RejectedLogEventsInfo != nil {
			h.Errorf("rejectedLogEventsInfo = %+v for a valid batch", first.RejectedLogEventsInfo)
		}
		// A later batch may hold earlier events; reads follow timestamps. The
		// sequence token sent is ignored (secrets-logs-events-alarms.md 3.3).
		must(h, logsapi.OpPutLogEvents, &logsapi.PutLogEventsInput{
			LogGroupName: "g", LogStreamName: "s", SequenceToken: new("stale"),
			LogEvents: []logsapi.InputLogEvent{event(now-20, "a"), event(now-5, "c")},
		})
		if got := h.all("g", "s"); strings.Join(got, "") != "abcd" {
			h.Errorf("events = %v, want a b c d", got)
		}
		s := must(h, logsapi.OpDescribeLogStreams, &logsapi.DescribeLogStreamsInput{LogGroupName: new("g")}).LogStreams[0]
		if *s.FirstEventTimestamp != now-20 || *s.LastEventTimestamp != now || *s.LastIngestionTime != now {
			h.Errorf("stream times = first %d last %d ingestion %d", *s.FirstEventTimestamp, *s.LastEventTimestamp, *s.LastIngestionTime)
		}
	})
}

func TestPutLogEventsBatchRules(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		now := h.now()
		// AWS's recorded input: the order check fires although the second
		// event is also outside a window.
		_, err := call(h, logsapi.OpPutLogEvents, &logsapi.PutLogEventsInput{
			LogGroupName: "g", LogStreamName: "s", LogEvents: []logsapi.InputLogEvent{event(now, "a"), event(now-int64(2*day/time.Millisecond), "b")},
		})
		wantError(h.T, err, "InvalidParameterException", "Log events in a single PutLogEvents request must be in chronological order.")

		_, err = call(h, logsapi.OpPutLogEvents, &logsapi.PutLogEventsInput{
			LogGroupName: "g", LogStreamName: "s", LogEvents: []logsapi.InputLogEvent{event(now-int64(25*time.Hour/time.Millisecond), "a"), event(now, "b")},
		})
		wantError(h.T, err, "InvalidParameterException", "The batch of log events in a single PutLogEvents request cannot span more than 24 hours.")

		big := strings.Repeat("x", 1_048_576-26)
		_, err = call(h, logsapi.OpPutLogEvents, &logsapi.PutLogEventsInput{
			LogGroupName: "g", LogStreamName: "s", LogEvents: []logsapi.InputLogEvent{event(now, big), event(now, "y")},
		})
		wantError(h.T, err, "InvalidParameterException", "Upload too large: 1048603 bytes exceeds limit of 1048576")
		h.put("g", "s", event(now, big))

		_, err = call(h, logsapi.OpPutLogEvents, &logsapi.PutLogEventsInput{LogGroupName: "g", LogStreamName: "missing", LogEvents: []logsapi.InputLogEvent{event(now, "a")}})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log stream does not exist.")
		_, err = call(h, logsapi.OpPutLogEvents, &logsapi.PutLogEventsInput{LogGroupName: "missing", LogStreamName: "s", LogEvents: []logsapi.InputLogEvent{event(now, "a")}})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log group does not exist.")
	})
}

// Rejected events are reported by index and the rest stored: too old is an
// exclusive end index, too new an inclusive start index
// (secrets-logs-events-alarms.md 3.3).
func TestPutLogEventsReportsRejectedEvents(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		now := h.now()
		ms := func(d time.Duration) int64 { return int64(d / time.Millisecond) }
		out := h.put("g", "s",
			event(now-ms(30*day), "old"),
			event(now-ms(15*day), "old"),
			event(now, "kept"),
			event(now+ms(3*time.Hour), "new"),
		)
		info := out.RejectedLogEventsInfo
		switch {
		case info == nil:
			h.Fatal("no rejectedLogEventsInfo")
		case info.TooOldLogEventEndIndex == nil || *info.TooOldLogEventEndIndex != 2:
			h.Errorf("tooOldLogEventEndIndex = %v, want 2", info.TooOldLogEventEndIndex)
		case info.TooNewLogEventStartIndex == nil || *info.TooNewLogEventStartIndex != 3:
			h.Errorf("tooNewLogEventStartIndex = %v, want 3", info.TooNewLogEventStartIndex)
		case info.ExpiredLogEventEndIndex != nil:
			h.Errorf("expiredLogEventEndIndex = %d without a retention policy", *info.ExpiredLogEventEndIndex)
		}
		if got := h.all("g", "s"); !slices.Equal(got, []string{"kept"}) {
			h.Errorf("stored = %v, want only the valid event", got)
		}

		must(h, logsapi.OpPutRetentionPolicy, &logsapi.PutRetentionPolicyInput{LogGroupName: "g", RetentionInDays: 1})
		out = h.put("g", "s", event(now-ms(2*day), "expired"), event(now, "kept"))
		if info := out.RejectedLogEventsInfo; info == nil || info.ExpiredLogEventEndIndex == nil || *info.ExpiredLogEventEndIndex != 1 {
			h.Errorf("rejectedLogEventsInfo = %+v, want expiredLogEventEndIndex 1", info)
		}
	})
}

// The 2-hour and 14-day windows judge a timestamp that containers stamp with
// wall time, so an event inside them by either clock is stored (G4).
func TestWindowsAcceptAnEventValidByEitherClock(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		wall := h.now()
		h.advance(30 * day)
		emu := h.now()
		// Valid against the emulator clock, 30 days ahead of wall time.
		if out := h.put("g", "s", event(emu, "emulator")); out.RejectedLogEventsInfo != nil {
			h.Errorf("an event at emulator now was rejected: %+v", out.RejectedLogEventsInfo)
		}
		// Valid against wall time, 30 days behind the emulator clock.
		if out := h.put("g", "s", event(wall, "wall")); out.RejectedLogEventsInfo != nil {
			h.Errorf("an event at wall now was rejected: %+v", out.RejectedLogEventsInfo)
		}
		// Too new by both.
		if out := h.put("g", "s", event(emu+int64(3*time.Hour/time.Millisecond), "x")); out.RejectedLogEventsInfo == nil {
			h.Error("an event 3 hours past both clocks was stored")
		}
		// Between the two windows: too new by wall time, too old by the
		// emulator clock, so valid by neither.
		out := h.put("g", "s", event(wall+int64(10*day/time.Millisecond), "gap"))
		if info := out.RejectedLogEventsInfo; info == nil || info.TooOldLogEventEndIndex == nil || *info.TooOldLogEventEndIndex != 1 {
			h.Errorf("an event 10 days past wall time, 20 days before the emulator clock: rejected = %+v, want tooOldLogEventEndIndex 1", out.RejectedLogEventsInfo)
		}
		if got := h.all("g", "s"); !slices.Equal(got, []string{"wall", "emulator"}) {
			h.Errorf("stored = %v", got)
		}
	})
}

// The reads name the group they address, by name or by ARN, as the call's
// resource, so the journal, fault rules and the access recorder see it:
// GetLogEvents its stream, FilterLogEvents, DescribeLogStreams and
// ListTagsForResource the group.
func TestReadsNameTheirGroupAsTheCallResource(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		const group = "arn:aws:logs:us-east-1:000000000000:log-group:g"
		must(h, logsapi.OpGetLogEvents, &logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s"})
		must(h, logsapi.OpFilterLogEvents, &logsapi.FilterLogEventsInput{LogGroupIdentifier: new(group)})
		must(h, logsapi.OpDescribeLogStreams, &logsapi.DescribeLogStreamsInput{LogGroupName: new("g")})
		must(h, logsapi.OpListTagsForResource, &logsapi.ListTagsForResourceInput{ResourceArn: group})
		for _, c := range []struct {
			op       ref.OperationID
			resource string
		}{
			{logsapi.OpGetLogEvents.Descriptor().ID, group + ":log-stream:s"},
			{logsapi.OpFilterLogEvents.Descriptor().ID, group},
			{logsapi.OpDescribeLogStreams.Descriptor().ID, group},
			{logsapi.OpListTagsForResource.Descriptor().ID, group},
		} {
			if n := len(h.Journal.Query(journal.Filter{Operation: c.op, ResourceGlob: c.resource}).Events); n != 1 {
				t.Errorf("%d %s calls name %s, want 1", n, c.op.Name, c.resource)
			}
		}
	})
}

// Retention expires events on the emulator clock: advancing past an event's
// timestamp plus the retention period removes it.
func TestRetentionExpiresEventsUnderAdvance(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		start := h.now()
		h.put("g", "s", event(start, "first"))
		must(h, logsapi.OpPutRetentionPolicy, &logsapi.PutRetentionPolicyInput{LogGroupName: "g", RetentionInDays: 1})
		h.advance(12 * time.Hour)
		h.put("g", "s", event(h.now(), "second"))

		h.advance(12*time.Hour - time.Millisecond)
		if got := h.all("g", "s"); !slices.Equal(got, []string{"first", "second"}) {
			h.Errorf("just before expiry = %v", got)
		}
		h.advance(time.Millisecond)
		if got := h.all("g", "s"); !slices.Equal(got, []string{"second"}) {
			h.Errorf("at the first expiry = %v", got)
		}
		h.advance(12 * time.Hour)
		if got := h.all("g", "s"); len(got) != 0 {
			h.Errorf("after the second expiry = %v", got)
		}
		if armed := retentionTimers(h); len(armed) != 0 {
			h.Errorf("a group without events keeps retention timers %v", armed)
		}

		// Clearing the policy disarms the timer; nothing expires.
		h.put("g", "s", event(h.now(), "kept"))
		must(h, logsapi.OpDeleteRetentionPolicy, &logsapi.DeleteRetentionPolicyInput{LogGroupName: "g"})
		h.advance(10 * day)
		if got := h.all("g", "s"); !slices.Equal(got, []string{"kept"}) {
			h.Errorf("without retention = %v", got)
		}
	})
}

// A snapshot round trip keeps retention: Rearm rebuilds the timer from the
// stored events.
func TestRearmRebuildsRetentionTimers(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		h.put("g", "s", event(h.now(), "e"))
		must(h, logsapi.OpPutRetentionPolicy, &logsapi.PutRetentionPolicyInput{LogGroupName: "g", RetentionInDays: 3})
		want := retentionTimers(h)
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.Kernel.Timers().Scheduler().Clear()
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		if got := retentionTimers(h); !slices.Equal(got, want) || len(got) != 1 {
			h.Errorf("after Rearm = %v, want %v", got, want)
		}
		h.advance(3 * day)
		if got := h.all("g", "s"); len(got) != 0 {
			h.Errorf("after expiry = %v", got)
		}
	})
}

func retentionTimers(h *harness) []clock.Armed {
	var out []clock.Armed
	for _, a := range h.Kernel.Timers().Scheduler().Snapshot() {
		if a.Key.Kind == logs.TimerRetention {
			out = append(out, a)
		}
	}
	return out
}

// PutLogEventsInternal stores the emulator's own lines without the client
// windows (ARCHITECTURE.md 14.6).
func TestPutLogEventsInternalBypassesTheWindows(t *testing.T) {
	run(t, func(h *harness) {
		h.group("/aws/lambda/fn")
		h.stream("/aws/lambda/fn", "2000/01/01/[$LATEST]abc")
		parent := h.Call(logsapi.OpCreateLogGroup.Descriptor())
		old := h.now() - int64(20*day/time.Millisecond)
		_, err := kernel.Invoke(h.Context(), h.Kernel, parent, logsapi.OpPutLogEventsInternal,
			kernel.Target{Scope: h.Scope, As: kernel.AsCaller{}, Via: ref.LambdaLogs},
			&logsapi.PutLogEventsInternalInput{LogGroupName: "/aws/lambda/fn", LogStreamName: "2000/01/01/[$LATEST]abc", LogEvents: []logsapi.InputLogEvent{event(old, "START")}})
		if err != nil {
			h.Fatalf("PutLogEventsInternal = %s: %s", err.ID.JSONType, err.Message)
		}
		if got := h.all("/aws/lambda/fn", "2000/01/01/[$LATEST]abc"); !slices.Equal(got, []string{"START"}) {
			h.Errorf("stored = %v", got)
		}
	})
}

func TestGetLogEventsTokens(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		now := h.now()
		h.put("g", "s", event(now-3, "a"), event(now-2, "b"), event(now-1, "c"))

		// The default reads the newest events, in ascending order.
		tail := h.get(&logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s", Limit: new(int32(2))})
		if got := messages(tail.Events); !slices.Equal(got, []string{"b", "c"}) {
			h.Errorf("default page = %v, want the newest two", got)
		}
		for _, tok := range []*string{tail.NextForwardToken, tail.NextBackwardToken} {
			if tok == nil || !strings.HasPrefix(*tok, "f/") && !strings.HasPrefix(*tok, "b/") || len(*tok) != 58 {
				h.Errorf("token = %v, want f/ or b/ and 56 digits", tok)
			}
		}
		// At the end the same token comes back.
		end := h.get(&logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s", NextToken: tail.NextForwardToken, StartFromHead: new(true)})
		if len(end.Events) != 0 || val(end.NextForwardToken) != val(tail.NextForwardToken) {
			h.Errorf("past the end = %v, token %s, want none and %s", messages(end.Events), val(end.NextForwardToken), val(tail.NextForwardToken))
		}
		// endTime is exclusive, startTime inclusive.
		mid := h.get(&logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s", StartTime: new(now - 2), EndTime: new(now - 1), StartFromHead: new(true)})
		if got := messages(mid.Events); !slices.Equal(got, []string{"b"}) {
			h.Errorf("[now-2, now-1) = %v, want b", got)
		}
		_, err := call(h, logsapi.OpGetLogEvents, &logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s", NextToken: new("f/123")})
		wantError(h.T, err, "InvalidParameterException", "The specified nextToken is invalid.")
		_, err = call(h, logsapi.OpGetLogEvents, &logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s", Limit: new(int32(10001))})
		wantError(h.T, err, "InvalidParameterException", "1 validation error detected: Value '10001' at 'limit' failed to satisfy constraint: Member must have value less than or equal to 10000")
		_, err = call(h, logsapi.OpGetLogEvents, &logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "missing"})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log stream does not exist.")
	})
}

// Tailing a stream that starts empty: the forward token of the default read
// returns the events that arrive after it.
func TestGetLogEventsForwardTokenOfAnEmptyStreamTails(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		empty := h.get(&logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s"})
		if len(empty.Events) != 0 {
			h.Fatalf("empty stream page = %v", messages(empty.Events))
		}
		h.put("g", "s", event(h.now(), "first"))
		next := h.get(&logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s", NextToken: empty.NextForwardToken, StartFromHead: new(true)})
		if got := messages(next.Events); !slices.Equal(got, []string{"first"}) {
			h.Errorf("following the empty page's forward token = %v, want [first]", got)
		}
	})
}
