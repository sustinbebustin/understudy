package journal

import (
	"context"
	"sync"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// Stream delivers the events matching f in recording order on the returned
// channel until ctx ends, then closes it. Limit and Cursor of f are ignored.
//
// after resumes a stream: when it is the last event a previous stream
// delivered, the new one replays every retained event recorded after it, then
// continues live, so nothing is lost or repeated. When after was evicted the
// stream first delivers Gap{Missed: n}, n being the evicted events recorded
// after it, matching f or not, then replays everything retained; no Gap
// comes when none were. The journal remembers where the latest
// Options.Capacity (at least 4096) evicted events were recorded; for an older
// id, or one skipped by Seek, it no longer knows, and n counts every event
// evicted so far. When after is 0, open, or not issued yet, the stream starts
// live. Replay reads the ring a batch at a time rather than copying it, a batch
// being at most 64 events and MaxBytes/8 estimated bytes, so a consumer that
// stops reading pins at most one batch of it; events evicted before the
// replay reaches them arrive as one Gap{Missed: n} in their place.
//
// Recording never waits for a consumer: each stream buffers up to
// Options.StreamBuffer events and MaxBytes/8 estimated bytes, and the events
// it has to drop while full, with every event recorded after them until the
// consumer reads up to that point, reach the consumer as one Gap{Missed: n}
// in their place. A single event larger than MaxBytes/8 is buffered or
// batched only alone. A consumer that stops reading thus holds at most about
// MaxBytes/4 per stream, plus the event being handed to it.
func (j *Journal) Stream(ctx context.Context, f Filter, after ref.EventID) <-chan Event {
	out := make(chan Event)
	s := &subscriber{filter: f, limit: j.streamBuffer, maxBytes: j.streamBytes, wake: make(chan struct{}, 1)}
	j.mu.Lock()
	r := j.replayFrom(after)
	j.subs[s] = struct{}{}
	j.mu.Unlock()
	go j.pump(ctx, s, r, out)
	return out
}

// replay is the part of a resumed stream recorded before it subscribed: the
// committed entries numbered from up to end, after missed evicted events.
// Recorded events from end on reach the stream through its live queue.
type replay struct {
	from, end uint64
	missed    uint64
}

// replayFrom returns what a stream resuming after the event after replays.
// Called with mu held.
func (j *Journal) replayFrom(after ref.EventID) replay {
	end := j.committed.next
	if after == 0 || after >= j.next {
		return replay{from: end, end: end}
	}
	if e, ok := j.byID[after]; ok {
		if _, open := e.openCall(); open {
			return replay{from: end, end: end}
		}
		return replay{from: e.seq + 1, end: end}
	}
	// after was evicted. Ids do not follow recording order, so count by
	// eviction order, which does.
	n, known := j.evicted.after(after)
	if !known {
		n = j.evicted.total
	}
	return replay{from: j.committed.oldest(), end: end, missed: n}
}

// replayBatch is how many events a replaying stream takes per lock. With the
// byte bound Journal.streamBytes it bounds what a consumer that stops reading
// pins in memory once the ring has evicted those events.
const replayBatch = 64

// streamShare bounds what a stream holds, its live queue and its replay
// batch each, to MaxBytes/streamShare estimated bytes.
const streamShare = 8

// nextBatch returns the next events of r, at most replayBatch of them and
// streamBytes estimated bytes, or the next one alone when it is larger, and
// advances r past them. Entries evicted since r was last advanced are added
// to r.missed. It takes mu.
func (j *Journal) nextBatch(r *replay) []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	if oldest := j.committed.oldest(); r.from < oldest {
		skipped := min(oldest, r.end) - r.from
		r.missed += skipped
		r.from += skipped
	}
	var out []Event
	held := 0
	for _, e := range j.committed.from(r.from) {
		if e.seq >= r.end || len(out) == replayBatch || (len(out) > 0 && held+e.size > j.streamBytes) {
			break
		}
		out = append(out, e.ev)
		held += e.size
		r.from = e.seq + 1
	}
	return out
}

// sendReplay sends the events of r matching s, a batch at a time, each run
// of events evicted before their turn as one Gap in their place. It reports
// false when send did.
func (j *Journal) sendReplay(s *subscriber, r replay, send func(Event) bool) bool {
	for {
		batch := j.nextBatch(&r)
		if r.missed > 0 {
			if !send(j.gap(r.missed)) {
				return false
			}
			r.missed = 0
		}
		if len(batch) == 0 {
			return true
		}
		for i := range batch {
			if s.filter.matches(batch[i]) && !send(batch[i]) {
				return false
			}
		}
	}
}

// pump feeds out: the replay first, then the live queue. It ends with ctx
// and unsubscribes.
func (j *Journal) pump(ctx context.Context, s *subscriber, r replay, out chan<- Event) {
	defer close(out)
	defer func() {
		j.mu.Lock()
		delete(j.subs, s)
		j.mu.Unlock()
	}()
	send := func(e Event) bool {
		select {
		case out <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !j.sendReplay(s, r, send) {
		return
	}
	for {
		it, ok := s.next()
		if !ok {
			select {
			case <-s.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		e := it.event
		if it.missed > 0 {
			e = j.gap(it.missed)
		}
		if !send(e) {
			return
		}
	}
}

// gap returns the Gap event for n missed events, dated now.
func (j *Journal) gap(n uint64) Event {
	return Event{At: j.clock.Now(), Wall: j.clock.Wall().Round(0).UTC(), Detail: Gap{Missed: n}}
}

// subscriber is one stream's queue, bounded in events and in estimated bytes.
type subscriber struct {
	filter   Filter
	limit    int           // most queued items
	maxBytes int           // most estimated bytes of queued events, unless one event alone exceeds it
	wake     chan struct{} // holds a token while the queue may be non-empty

	mu    sync.Mutex
	queue []item
	bytes int // sum of the queued events' sizes
}

// item is a queued event and its Estimate, or a run of dropped events when
// missed > 0.
type item struct {
	event  Event
	size   int
	missed uint64
}

// offer queues e, whose Estimate is size, when it matches, never blocking: a
// full queue counts it in a trailing gap item instead. The queue is full at
// limit items, or when e would take it past maxBytes; an empty queue takes
// e whatever its size, so a consumer that reads still receives it. Once a
// gap item trails the queue, every later event joins it until the consumer
// reads up to it, so a stalled consumer gets exactly one Gap however the
// event sizes vary.
func (s *subscriber) offer(e Event, size int) {
	if !s.filter.matches(e) {
		return
	}
	s.mu.Lock()
	switch n := len(s.queue); {
	case n > 0 && s.queue[n-1].missed > 0:
		s.queue[n-1].missed++
	case n < s.limit && (s.bytes == 0 || s.bytes+size <= s.maxBytes):
		s.queue = append(s.queue, item{event: e, size: size})
		s.bytes += size
	default:
		s.queue = append(s.queue, item{missed: 1})
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// next pops the oldest queued item.
func (s *subscriber) next() (item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return item{}, false
	}
	it := s.queue[0]
	s.queue[0] = item{}
	s.queue = s.queue[1:]
	s.bytes -= it.size
	return it, true
}
