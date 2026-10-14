package logs

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Time rules of PutLogEvents (secrets-logs-events-alarms.md 3.3) and the
// page limits of GetLogEvents and FilterLogEvents (3.4, 3.5).
const (
	futureWindow  = 2 * time.Hour
	pastWindow    = 14 * 24 * time.Hour
	maxBatchSpan  = 24 * time.Hour
	maxPageEvents = 10_000
	maxPageBytes  = 1_048_576
)

// bindEvents binds the event data plane.
func (s *Service) bindEvents(b logsapi.Binder) {
	b.PutLogEvents(s.putLogEvents,
		registry.Partial{Note: "entity is accepted and ignored; sequenceToken is ignored, as AWS documents"},
		kernel.WithCheck(checkPutLogEvents),
		streamNamed(func(in *logsapi.PutLogEventsInput) (string, string) { return in.LogGroupName, in.LogStreamName }))
	b.PutLogEventsInternal(s.putLogEventsInternal, registry.Full{},
		kernel.WithCheck(checkPutLogEventsInternal),
		streamNamed(func(in *logsapi.PutLogEventsInternalInput) (string, string) { return in.LogGroupName, in.LogStreamName }))
	b.GetLogEvents(s.getLogEvents,
		registry.Partial{Note: "unmask is ignored: no data protection policies"},
		kernel.WithCheck(checkGetLogEvents),
		groupIdentified(
			func(in *logsapi.GetLogEventsInput) (*string, *string) { return in.LogGroupName, in.LogGroupIdentifier },
			func(ref groupRef, in *logsapi.GetLogEventsInput) aws.ARN { return ref.streamARN(in.LogStreamName) }))
	b.FilterLogEvents(s.filterLogEvents,
		registry.Partial{Note: "unmask is ignored: no data protection policies; logStreamNames that name no stream are skipped"},
		kernel.WithCheck(checkFilterLogEvents),
		groupIdentified(
			func(in *logsapi.FilterLogEventsInput) (*string, *string) {
				return in.LogGroupName, in.LogGroupIdentifier
			},
			func(ref groupRef, _ *logsapi.FilterLogEventsInput) aws.ARN { return ref.arn() }))
}

// admission decides which events of a batch PutLogEvents stores.
//
//sumtype:decl
type admission interface{ isAdmission() }

// clientWindows is a client's PutLogEvents: an event is stored when it is
// inside the 2-hour and 14-day windows of the wall clock or of the
// emulator clock, since containers stamp lines with wall time
// (lambda-runtime-contract.md 3), and
// not older than the group's retention on the emulator clock.
type clientWindows struct{ wall clock.Instant }

// internalFeed is PutLogEventsInternal: the emulator's own lines, stamped
// on its clock, are all stored.
type internalFeed struct{}

func (clientWindows) isAdmission() {}
func (internalFeed) isAdmission()  {}

// verdict is what admission makes of one event.
type verdict uint8

const (
	accepted verdict = iota
	tooOld
	tooNew
	expiredEvent
)

// judge classifies one event at the commit instant now.
func judge(a admission, g *Group, ts int64, now clock.Instant) verdict {
	w, ok := a.(clientWindows)
	if !ok {
		return accepted
	}
	at := instantOf(ts)
	inWindow := func(ref clock.Instant) bool {
		return !at.Before(ref.Add(-pastWindow)) && !ref.Add(futureWindow).Before(at)
	}
	// Each clock's windows are checked on their own: an event between the
	// two windows of diverged clocks is valid by neither. A rejection is
	// classified by the emulator clock.
	valid := inWindow(now) || inWindow(w.wall)
	switch {
	case !valid && now.Add(futureWindow).Before(at):
		return tooNew
	case !valid:
		return tooOld
	case expired(g, ts, now):
		return expiredEvent
	}
	return accepted
}

// putResult is what one ingest reports.
type putResult struct {
	token    string
	rejected *logsapi.RejectedLogEventsInfo
}

// putLogEvents stores a client's batch.
func (s *Service) putLogEvents(_ context.Context, c *kernel.Call, in *logsapi.PutLogEventsInput) (*logsapi.PutLogEventsOutput, *awserr.Error) {
	wall := clock.FromWire(s.k.Clock().Wall())
	r, err := s.ingest(c, named(c, in.LogGroupName), in.LogStreamName, in.LogEvents, clientWindows{wall: wall})
	if err != nil {
		return nil, err
	}
	return &logsapi.PutLogEventsOutput{NextSequenceToken: new(r.token), RejectedLogEventsInfo: r.rejected}, nil
}

// putLogEventsInternal stores the emulator's own lines (Lambda function
// logs) without the client time windows.
func (s *Service) putLogEventsInternal(_ context.Context, c *kernel.Call, in *logsapi.PutLogEventsInternalInput) (*logsapi.PutLogEventsInternalOutput, *awserr.Error) {
	if _, err := s.ingest(c, named(c, in.LogGroupName), in.LogStreamName, in.LogEvents, internalFeed{}); err != nil {
		return nil, err
	}
	return &logsapi.PutLogEventsInternalOutput{}, nil
}

// ingest stores the admitted events of a chronological batch in a stream:
// order and size were checked at step 7; here each event is judged, the
// 24-hour span of the valid ones is checked, and the stored ones are merged
// into the stream by position.
func (s *Service) ingest(c *kernel.Call, ref groupRef, stream string, batch []logsapi.InputLogEvent, a admission) (putResult, *awserr.Error) {
	if !s.exists(ref) {
		return putResult{}, errGroupNotFound()
	}
	return kernel.Write(c, s.cells, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (putResult, *awserr.Error) {
		g, ok := st.Groups[ref.name]
		if !ok {
			return putResult{}, errGroupNotFound()
		}
		str, ok := g.Streams[stream]
		if !ok {
			return putResult{}, errStreamNotFound()
		}
		kept, rejected, err := admit(a, &g, batch, now)
		if err != nil {
			return putResult{}, err
		}
		if len(kept) > 0 {
			fresh := make([]Event, len(kept))
			for i, e := range kept {
				st.LastSeq++
				fresh[i] = Event{Timestamp: e.Timestamp, Message: e.Message, Ingestion: millis(now), Seq: st.LastSeq}
			}
			str.Events = merge(str.Events, fresh)
			str.LastIngestion = now
			str.LastSeq = st.LastSeq
			g.Streams[stream] = str
			st.Groups[ref.name] = g
			s.armExpiry(tx, ref, &g)
		} else {
			tx.Unchanged()
		}
		return putResult{token: sequenceToken(str.LastSeq), rejected: rejected}, nil
	})
}

// admit judges every event and returns the stored ones with the
// rejectedLogEventsInfo AWS reports: the exclusive end of the too-old and
// expired prefixes and the inclusive start of the too-new suffix
// (RejectedLogEventsInfo documentation); nil when nothing was rejected.
// Those fields hold only when the wall and emulator clocks are within 14
// days 2 hours of each other: further apart, an event one clock rejects
// can lie between two stored ones, AWS's index fields cannot express that,
// and the reported indices can cover stored events.
//
// The 24-hour span rule covers every event inside the time windows, stored
// or expired by retention, as the PutLogEvents documentation words it; a
// batch whose valid events span more fails whole.
func admit(a admission, g *Group, batch []logsapi.InputLogEvent, now clock.Instant) ([]logsapi.InputLogEvent, *logsapi.RejectedLogEventsInfo, *awserr.Error) {
	var (
		kept     []logsapi.InputLogEvent
		info     logsapi.RejectedLogEventsInfo
		rejected bool
		// first and last bound the batch's valid events, which are in
		// chronological order; seen is false while there is none.
		first, last int64
		seen        bool
	)
	valid := func(ts int64) {
		if !seen {
			first, seen = ts, true
		}
		last = ts
	}
	for i, e := range batch {
		switch judge(a, g, e.Timestamp, now) {
		case accepted:
			valid(e.Timestamp)
			kept = append(kept, e)
		case tooOld:
			info.TooOldLogEventEndIndex, rejected = new(int32(i+1)), true
		case expiredEvent:
			valid(e.Timestamp)
			info.ExpiredLogEventEndIndex, rejected = new(int32(i+1)), true
		case tooNew:
			if info.TooNewLogEventStartIndex == nil {
				info.TooNewLogEventStartIndex, rejected = new(int32(i)), true
			}
		}
	}
	if seen && time.Duration(last-first)*time.Millisecond > maxBatchSpan {
		return nil, nil, errSpan()
	}
	if !rejected {
		return kept, nil, nil
	}
	return kept, &info, nil
}

// merge returns a new slice of the stream's events and the fresh ones, in
// position order; fresh events are in order and newer than every stored
// one by sequence.
func merge(stored, fresh []Event) []Event {
	out := make([]Event, 0, len(stored)+len(fresh))
	i, j := 0, 0
	for i < len(stored) && j < len(fresh) {
		if stored[i].pos().compare(fresh[j].pos()) <= 0 {
			out = append(out, stored[i])
			i++
		} else {
			out = append(out, fresh[j])
			j++
		}
	}
	out = append(out, stored[i:]...)
	return append(out, fresh[j:]...)
}

// sequenceToken is the nextSequenceToken PutLogEvents returns: AWS ignores
// the token it is sent back, so it only has to be non-empty; it carries the
// stream's latest sequence number in AWS's 56-digit form.
func sequenceToken(seq uint64) string { return fmt.Sprintf("%056d", seq) }

// getLogEvents reads one page of a stream's events in [startTime, endTime).
func (s *Service) getLogEvents(_ context.Context, c *kernel.Call, in *logsapi.GetLogEventsInput) (*logsapi.GetLogEventsOutput, *awserr.Error) {
	ref, err := resolveGroup(c, in.LogGroupName, in.LogGroupIdentifier)
	if err != nil {
		return nil, err
	}
	w := window{lo: 0, hi: math.MaxInt64}
	if in.StartTime != nil {
		w.lo = *in.StartTime
	}
	if in.EndTime != nil {
		w.hi = *in.EndTime
	}
	now := c.Admitted()
	l := eventListing(ref, in.LogStreamName)
	from := issuedCursor{dir: backward, at: position{ts: w.hi}, issued: now}
	if in.StartFromHead != nil && *in.StartFromHead {
		from.cursor = cursor{dir: forward, at: position{ts: w.lo}}
	}
	if in.NextToken != nil {
		parsed, ok := parseCursor(*in.NextToken, l, now)
		if !ok {
			return nil, errInvalidToken()
		}
		from = parsed
	}
	limit := maxPageEvents
	if in.Limit != nil {
		limit = int(*in.Limit)
	}
	// read is the page, or why the group yields none.
	type read struct {
		out *logsapi.GetLogEventsOutput
		err *awserr.Error
	}
	r, ok := group(s, ref, func(g *Group) read {
		if g.Class == string(logsapi.LogGroupClassInfrequentAccess) {
			return read{err: errInfrequentAccess("GetLogEvents")}
		}
		st, ok := g.Streams[in.LogStreamName]
		if !ok {
			return read{err: errStreamNotFound()}
		}
		return read{out: streamPage(st.Events, w, l, from, now, limit)}
	})
	switch {
	case !ok:
		return nil, errGroupNotFound()
	case r.err != nil:
		return nil, r.err
	}
	return r.out, nil
}

// window is the time range GetLogEvents reads: lo inclusive, hi exclusive.
type window struct{ lo, hi int64 }

// streamPage cuts one GetLogEvents page from events in position order. A
// page ends at limit events or maxPageBytes of messages. Its forward token
// names the gap after its newest event and its backward token the gap
// before its oldest, both tokens of l issued at now. An empty page hands back the token
// it was read from, issue time included, which is how a client knows it
// reached the end; an empty backward page instead points its forward token
// at the window's start, since nothing in the window lies before the
// cursor, so following it (the default read of an empty stream, say)
// returns the events that arrive later.
func streamPage(events []Event, w window, l listing, from issuedCursor, now clock.Instant, limit int) *logsapi.GetLogEventsOutput {
	cur := from.cursor
	a := firstAt(events, position{ts: w.lo})
	b := firstAt(events, position{ts: w.hi})
	var lo, hi int
	switch cur.dir {
	case forward:
		lo = max(a, firstAt(events, cur.at))
		hi = lo
		for size := 0; hi < b && hi-lo < limit && fits(&size, &events[hi]); hi++ {
		}
	case backward:
		hi = min(b, firstAt(events, cur.at))
		hi = max(hi, a)
		lo = hi
		for size := 0; lo > a && hi-lo < limit && fits(&size, &events[lo-1]); lo-- {
		}
	}
	out := &logsapi.GetLogEventsOutput{Events: make([]logsapi.OutputLogEvent, 0, hi-lo)}
	for i := lo; i < hi; i++ {
		e := &events[i]
		out.Events = append(out.Events, logsapi.OutputLogEvent{Timestamp: new(e.Timestamp), Message: new(e.Message), IngestionTime: new(e.Ingestion)})
	}
	if lo == hi {
		ahead := cur.at
		if cur.dir == backward {
			ahead = position{ts: w.lo}
		}
		out.NextForwardToken = new(cursor{dir: forward, at: ahead}.token(l, from.issued))
		out.NextBackwardToken = new(cursor{dir: backward, at: cur.at}.token(l, from.issued))
		return out
	}
	out.NextForwardToken = new(cursor{dir: forward, at: events[hi-1].pos().next()}.token(l, now))
	out.NextBackwardToken = new(cursor{dir: backward, at: events[lo].pos()}.token(l, now))
	return out
}

// firstAt is the index of the first event at or after p.
func firstAt(events []Event, p position) int {
	i, _ := slices.BinarySearchFunc(events, p, func(e Event, p position) int { return e.pos().compare(p) })
	return i
}

// fits adds e to a page of size bytes when the page has room for it; the
// first event of a page always fits.
func fits(size *int, e *Event) bool {
	n := len(e.Message) + eventOverhead
	if *size > 0 && *size+n > maxPageBytes {
		return false
	}
	*size += n
	return true
}
