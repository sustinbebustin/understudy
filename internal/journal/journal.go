package journal

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Defaults for the zero values of Options and FileOptions.
const (
	DefaultCapacity     = 100_000
	DefaultMaxBytes     = 128 << 20
	DefaultStreamBuffer = 1024
	DefaultRotateBytes  = 64 << 20
)

// Options configures New.
type Options struct {
	// Capacity is the most events kept in memory; 0 means DefaultCapacity.
	Capacity int
	// MaxBytes bounds the sum of the Estimate of every recorded event kept in
	// memory; calls between Begin and End are not counted. 0 means
	// DefaultMaxBytes. Begin stores a call input longer than MaxBytes/16
	// bytes as a truncation marker (see Redacted). An event still larger
	// than MaxBytes on its own, which takes a MaxBytes smaller than its other
	// text, evicts every older event and then itself: eviction follows
	// recording order, which resumed streams count Gaps by.
	MaxBytes int
	// StreamBuffer is how many events a stream holds for a consumer that is
	// not reading before it starts counting a Gap, which then takes every
	// later event until the consumer reads up to it; 0 means
	// DefaultStreamBuffer. The held events are also bounded to MaxBytes/8
	// estimated bytes, whichever bound comes first; an event larger than that
	// is held only when the stream holds no other.
	StreamBuffer int
	// File, when set, appends every recorded event to an NDJSON file. A
	// writer goroutine writes it, so recording never waits for the disk; when
	// the disk falls MaxBytes estimated bytes behind, the file stops (see
	// FileErr).
	File *FileOptions
	// Clock stamps Wall, fills a zero At and dates Gap events. Required.
	Clock *clock.Clock
}

// FileOptions configures the NDJSON file the journal appends to.
type FileOptions struct {
	// Path is the file; it is created when missing and appended to otherwise.
	Path string
	// RotateBytes is the size past which the file is renamed to Path + ".1",
	// replacing an earlier one, and a new file started; 0 means
	// DefaultRotateBytes.
	RotateBytes int64
}

// OptionsError reports Options New cannot use. Nothing was created.
type OptionsError struct {
	Field  string
	Reason string
}

func (e *OptionsError) Error() string {
	return fmt.Sprintf("journal: option %s %s", e.Field, e.Reason)
}

// Journal is the causal event log: a bounded in-memory ring of recorded events
// with indexes, plus the calls in flight, live streams and an optional file.
// Every method is safe for concurrent use and none blocks on a consumer.
type Journal struct {
	clock        *clock.Clock
	capacity     int
	maxBytes     int
	streamBuffer int
	streamBytes  int // MaxBytes/streamShare

	mu        sync.Mutex
	next      ref.EventID            // the id the next event gets
	byID      map[ref.EventID]*entry // retained and open events
	all       idList                 // the keys of byID
	committed ring                   // recorded events, oldest first
	evicted   evictions              // ids of the latest evicted events, for resuming streams
	bytes     int                    // sum of the committed entries' sizes
	index     indexes
	subs      map[*subscriber]struct{}

	file *sink // set by New and never changed; it has its own lock
}

// entry is one event the journal holds: open between Begin and End, then
// committed with its position in recording order.
type entry struct {
	ev   Event
	seq  uint64 // position in recording order, once committed
	size int    // Estimate(ev), once committed
}

// openCall returns the call e holds between Begin and End. Only an open call
// has a Pending outcome: End refuses to record one.
func (e *entry) openCall() (APICall, bool) {
	call, ok := e.ev.Detail.(APICall)
	if !ok {
		return APICall{}, false
	}
	_, pending := call.Outcome.(Pending)
	return call, pending
}

// New returns an empty journal whose first event gets id 1.
func New(o Options) (*Journal, error) {
	return newJournal(o, openOSFile)
}

// newJournal is New with the function that opens the file, which tests
// replace to simulate a slow or failing disk.
func newJournal(o Options, open opener) (*Journal, error) {
	if o.Clock == nil {
		return nil, &OptionsError{Field: "Clock", Reason: "is required: events are stamped with emulator and wall time"}
	}
	capacity, err := positive("Capacity", o.Capacity, DefaultCapacity)
	if err != nil {
		return nil, err
	}
	maxBytes, err := positive("MaxBytes", o.MaxBytes, DefaultMaxBytes)
	if err != nil {
		return nil, err
	}
	streamBuffer, err := positive("StreamBuffer", o.StreamBuffer, DefaultStreamBuffer)
	if err != nil {
		return nil, err
	}
	j := &Journal{
		clock:        o.Clock,
		capacity:     capacity,
		maxBytes:     maxBytes,
		streamBuffer: streamBuffer,
		streamBytes:  maxBytes / streamShare,
		next:         1,
		byID:         map[ref.EventID]*entry{},
		evicted:      evictions{limit: max(capacity, minRememberedEvictions)},
		index:        newIndexes(),
		subs:         map[*subscriber]struct{}{},
	}
	if o.File != nil {
		if j.file, err = openSink(*o.File, maxBytes, open); err != nil {
			return nil, err
		}
	}
	return j, nil
}

func positive[N int | int64](field string, v, def N) (N, error) {
	switch {
	case v < 0:
		return 0, &OptionsError{Field: field, Reason: fmt.Sprintf("is %d; it must be positive, or 0 for the default %d", v, def)}
	case v == 0:
		return def, nil
	default:
		return v, nil
	}
}

// RecordError reports an event the journal refuses to record. Nothing was
// recorded and no id was used.
type RecordError struct {
	Kind   Kind // the refused event's kind; 0 for a nil Detail
	Reason RecordReason
	Err    error // what was wrong, for NoWireForm and UnissuedID
}

// RecordReason says why an event was refused.
type RecordReason uint8

// RecordReason values.
const (
	// CallNeedsBegin: Append got an APICall. Calls are recorded with Begin
	// and End so they carry an outcome and latency.
	CallNeedsBegin RecordReason = iota + 1
	// GapNotRecordable: the event is a Gap, which exists only on streams.
	GapNotRecordable
	// NoWireForm: the event cannot be encoded, or would not decode back to
	// itself, so no stream or file could carry it: for example a nil Detail,
	// a nil nested outcome or clock mode, an ARN with an empty partition or a
	// colon in its account, or an instant past year 9999.
	NoWireForm
	// UnissuedID: Cause.Parent, Cause.Trace or a link names an id this
	// journal has not issued yet, or a link is 0, which names no event.
	UnissuedID
	// NotACall: Begin got a Detail that is not an APICall. Only calls have a
	// duration to wait for; other kinds are recorded with Append.
	NotACall
	// IDsExhausted: every event id has been issued. The last id,
	// math.MaxUint64, is never issued, so the next id never wraps to 0,
	// which names no event.
	IDsExhausted
)

func (e *RecordError) Error() string {
	switch e.Reason {
	case CallNeedsBegin:
		return "journal: Append refused an api-call event: calls are recorded with Begin and End so they carry an outcome and latency; nothing was recorded"
	case GapNotRecordable:
		return "journal: a gap event cannot be recorded: gaps exist only on streams, to tell a consumer what it missed; nothing was recorded"
	case NoWireForm:
		if e.Kind == 0 {
			return "journal: an event with no Detail cannot be recorded: set Detail to one of the journal's event kinds; nothing was recorded"
		}
		return fmt.Sprintf("journal: the %s event cannot be encoded, so no stream or file could carry it: %v; nothing was recorded", e.Kind, e.Err)
	case UnissuedID:
		return fmt.Sprintf("journal: the %s event refers to an event that does not exist yet: %v; nothing was recorded", e.Kind, e.Err)
	case NotACall:
		return fmt.Sprintf("journal: Begin refused a %s event: only api-call events are opened with Begin and closed with End; record it with Append; nothing was recorded", e.Kind)
	case IDsExhausted:
		return fmt.Sprintf("journal: the %s event cannot get an id: every event id has been issued, most likely after a Seek near the last id; nothing was recorded", e.Kind)
	default:
		return fmt.Sprintf("journal: the %s event was refused", e.Kind)
	}
}

func (e *RecordError) Unwrap() error { return e.Err }

// checkWireForm returns why e has no wire form, or nil. The fields the
// journal stamps are checked when they are stamped.
func checkWireForm(e Event) error {
	err := checkInstant("event instant At", e.At)
	if err == nil {
		_, err = encodeDetail(e.Detail)
	}
	if err != nil {
		return &RecordError{Kind: KindOf(e.Detail), Reason: NoWireForm, Err: err}
	}
	return nil
}

// Begin opens the APICall event of a call at admission and returns its id,
// which the call's internal calls and effects carry as their Cause.Parent.
// The event is visible to Query and Trace with a Pending outcome until End
// records it; streams and the file see it once, ended. Resources drops the
// zero ARN and repeats, and an Input longer than MaxBytes/16 bytes is stored
// as a truncation marker (see Redacted). It refuses with a *RecordError a
// Detail that is not an APICall, a call that cannot be encoded, and a cause
// or link naming an id not issued yet.
func (j *Journal) Begin(partial Event) (ref.EventID, error) {
	call, ok := partial.Detail.(APICall)
	if !ok {
		return 0, &RecordError{Kind: KindOf(partial.Detail), Reason: NotACall}
	}
	call.Outcome = Pending{}
	call.Latency = 0
	call.Resources = mergeARNs(nil, arnsAsDecoded(call.Resources))
	call.Input = call.Input.capped(j.maxBytes / inputShare)
	call = callAsDecoded(call)
	partial.Detail = call
	if err := checkWireForm(partial); err != nil {
		return 0, err
	}

	now, wall := j.now()
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.checkIDs(partial); err != nil {
		return 0, err
	}
	e := &entry{ev: j.stamp(partial, now, wall)}
	j.hold(e)
	return e.ev.ID, nil
}

// EndError reports an End the journal cannot apply. The journal is unchanged.
type EndError struct {
	ID     ref.EventID
	Reason EndReason
	Err    error // what was wrong, for UnreadableTouched
}

// EndReason says why End was refused.
type EndReason uint8

// EndReason values.
const (
	// NotOpen: no call with the id is between Begin and End; it ended
	// already, or the id never came from Begin.
	NotOpen EndReason = iota + 1
	// NoOutcome: the outcome was nil or Pending.
	NoOutcome
	// UnreadableTouched: a touched ARN would not decode back to itself, as
	// NoWireForm describes.
	UnreadableTouched
)

func (e *EndError) Error() string {
	switch e.Reason {
	case NotOpen:
		return fmt.Sprintf("journal: End(%d): no call with this event id is open; it already ended or never began", e.ID)
	case NoOutcome:
		return fmt.Sprintf("journal: End(%d): the outcome must be OK, Failed or Injected; the call stays open", e.ID)
	case UnreadableTouched:
		return fmt.Sprintf("journal: End(%d): a touched ARN cannot be journaled: %v; the call stays open", e.ID, e.Err)
	default:
		return fmt.Sprintf("journal: End(%d) refused", e.ID)
	}
}

func (e *EndError) Unwrap() error { return e.Err }

// End records the call opened by Begin with its outcome and wall latency.
// touched are the ARNs the call touched (Call.Touch); they are appended to
// the event's Resources in first-touch order without repeats, leaving out the
// zero ARN, which is no resource. It refuses with an *EndError, leaving the
// call open, an outcome that is not final, an id that is not open and a
// touched ARN no consumer could read back.
func (j *Journal) End(id ref.EventID, outcome Outcome, latency time.Duration, touched ...aws.ARN) error {
	switch outcome.(type) {
	case nil, Pending:
		return &EndError{ID: id, Reason: NoOutcome}
	case OK, Failed, Injected:
	}
	outcome, touched = outcomeAsDecoded(outcome), arnsAsDecoded(touched)
	for _, a := range touched {
		if err := checkARN(a); err != nil {
			return &EndError{ID: id, Reason: UnreadableTouched, Err: err}
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	e, ok := j.byID[id]
	if !ok {
		return &EndError{ID: id, Reason: NotOpen}
	}
	call, open := e.openCall()
	if !open {
		return &EndError{ID: id, Reason: NotOpen}
	}
	before := len(call.Resources)
	call.Resources = mergeARNs(call.Resources, touched)
	for _, a := range call.Resources[before:] {
		k := a.String()
		j.index.resource[k] = j.index.resource[k].insert(id)
	}
	call.Outcome = outcome
	call.Latency = latency
	e.ev.Detail = call
	j.commit(e)
	return nil
}

// Append records an event that has no duration (deliveries, invocations,
// timers, state changes, advisories, unclaimed requests) and returns its id.
// It refuses with a *RecordError an APICall, a Gap, an event that cannot be
// encoded and a cause or link naming an id not issued yet.
func (j *Journal) Append(e Event) (ref.EventID, error) {
	switch e.Detail.(type) {
	case APICall:
		return 0, &RecordError{Kind: KindAPICall, Reason: CallNeedsBegin}
	case Gap:
		return 0, &RecordError{Kind: KindGap, Reason: GapNotRecordable}
	case Delivery, Invocation, TimerFired, StateChange, FaultInjected, ClockChanged, Advisory, Unclaimed:
	}
	e.Detail = asDecoded(e.Detail)
	if err := checkWireForm(e); err != nil {
		return 0, err
	}
	now, wall := j.now()
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.checkIDs(e); err != nil {
		return 0, err
	}
	en := &entry{ev: j.stamp(e, now, wall)}
	j.hold(en)
	j.commit(en)
	return en.ev.ID, nil
}

// NextID returns the id the next event will get; the snapshot manifest
// records it as journal.next.
func (j *Journal) NextID() ref.EventID {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.next
}

// SeekError reports a Seek the journal refused. The journal is unchanged.
type SeekError struct {
	Next, Current ref.EventID
	Reason        SeekReason
}

// SeekReason says why Seek was refused.
type SeekReason uint8

// SeekReason values.
const (
	// SeekBelowNext: the id is below NextID, so ids would be reused.
	SeekBelowNext SeekReason = iota + 1
	// SeekPastLastID: the id is math.MaxUint64, which is never issued (see
	// IDsExhausted).
	SeekPastLastID
)

func (e *SeekError) Error() string {
	switch e.Reason {
	case SeekBelowNext:
		return fmt.Sprintf("journal: cannot seek to event id %d: ids up to %d are already issued and ids are never reused; the next id stays %d", e.Next, e.Current-1, e.Current)
	case SeekPastLastID:
		return fmt.Sprintf("journal: cannot seek to event id %d: it is past the last id the journal issues, so no event could be recorded; the next id stays %d (check the snapshot manifest's journal.next)", e.Next, e.Current)
	default:
		return fmt.Sprintf("journal: cannot seek to event id %d; the next id stays %d", e.Next, e.Current)
	}
}

// Seek makes next the id of the next event, so ids stay monotonic across a
// snapshot load. It refuses to move below NextID, and to math.MaxUint64.
func (j *Journal) Seek(next ref.EventID) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case next < j.next:
		return &SeekError{Next: next, Current: j.next, Reason: SeekBelowNext}
	case next == math.MaxUint64:
		return &SeekError{Next: next, Current: j.next, Reason: SeekPastLastID}
	}
	j.next = next
	return nil
}

// FileErr returns the first failure of the file, a *FileError naming the
// path and the failed step, or nil while the file is healthy or when there
// is none. The file is written in the background, so a failure shows once
// the writer reaches it. The file stops at its first failure, so a health
// check can report it while the emulator runs; memory, queries and streams
// are unaffected. It never waits for the disk.
func (j *Journal) FileErr() error {
	if j.file == nil {
		return nil
	}
	return j.file.failure()
}

// Close waits until every event queued for the file is written, closes the
// file, if any, and returns its first failure, as FileErr reports it. It
// waits for the disk, but recording goes on meanwhile. Memory, queries and
// streams are unaffected. Events recorded after Close begins are kept in
// memory but not written; the first one makes FileErr report a write
// *FileError wrapping os.ErrClosed.
//
// Close has no deadline of its own: a Write that never returns, as on a hung
// network or FUSE mount, makes it wait forever. A caller with a shutdown
// deadline runs Close in a goroutine and stops waiting for it at the
// deadline; the writer then dies with the process, and the file holds a
// prefix of recording order whose last record may be torn, which the next
// open ends as one unreadable line.
func (j *Journal) Close() error {
	if j.file == nil {
		return nil
	}
	return j.file.close()
}

// checkIDs refuses e when no id is left to give it, when its cause or a link
// names an id not issued yet, which no event can have, and when a link is 0,
// which names no event. Called with mu held.
func (j *Journal) checkIDs(e Event) error {
	if j.next == math.MaxUint64 {
		return &RecordError{Kind: KindOf(e.Detail), Reason: IDsExhausted}
	}
	if e.Cause.Parent >= j.next || e.Cause.Trace >= j.next {
		return &RecordError{Kind: KindOf(e.Detail), Reason: UnissuedID, Err: fmt.Errorf(
			"parent %d and trace %d must each be 0 or an id below the next id %d", e.Cause.Parent, e.Cause.Trace, j.next)}
	}
	for _, l := range e.Links {
		switch {
		case l == 0:
			return &RecordError{Kind: KindOf(e.Detail), Reason: UnissuedID, Err: errors.New(
				"a link is 0, which names no event; a link must be the id of a recorded event")}
		case l >= j.next:
			return &RecordError{Kind: KindOf(e.Detail), Reason: UnissuedID, Err: fmt.Errorf(
				"link %d must be an id below the next id %d", l, j.next)}
		}
	}
	return nil
}

// now reads emulator and wall time for stamp. It is called before mu is
// taken, since the clock has its own lock and the journal's lock is a leaf.
func (j *Journal) now() (clock.Instant, time.Time) {
	return j.clock.Now(), j.clock.Wall().Round(0).UTC()
}

// stamp assigns e its id and the wall time wall, fills a zero trace from the
// parent when it is held and with e's own id otherwise, and defaults a zero
// At to now. Called with mu held.
func (j *Journal) stamp(e Event, now clock.Instant, wall time.Time) Event {
	e.ID = j.next
	j.next++
	if e.Cause.Trace == 0 {
		e.Cause.Trace = e.ID
		if p, ok := j.byID[e.Cause.Parent]; ok {
			e.Cause.Trace = p.ev.Cause.Trace
		}
	}
	if e.At.IsZero() {
		e.At = now
	}
	e.Wall = wall
	if len(e.Links) == 0 {
		e.Links = nil
	} else {
		e.Links = slices.Clone(e.Links)
	}
	return e
}

// hold makes e findable by id and through the indexes. Called with mu held.
func (j *Journal) hold(e *entry) {
	j.byID[e.ev.ID] = e
	j.all = j.all.insert(e.ev.ID)
	j.index.add(e.ev)
}

// commit records e: it joins the ring, reaches every stream and the file's
// queue, and older events are evicted until the count and byte bounds hold
// again. Called with mu held.
func (j *Journal) commit(e *entry) {
	e.size = Estimate(e.ev)
	j.committed.push(e)
	j.bytes += e.size
	for s := range j.subs {
		s.offer(e.ev, e.size)
	}
	if j.file != nil {
		j.file.enqueue(e.ev, e.size)
	}
	for j.committed.len() > 0 && (j.committed.len() > j.capacity || j.bytes > j.maxBytes) {
		old := j.committed.pop()
		j.evicted.add(old.ev.ID)
		j.bytes -= old.size
		delete(j.byID, old.ev.ID)
		j.all = j.all.remove(old.ev.ID)
		j.index.drop(old.ev)
	}
}

// mergeARNs returns a new slice of have followed by the ARNs of add not
// already present, leaving out the zero ARN, which is no resource; nil when
// that leaves none.
func mergeARNs(have, add []aws.ARN) []aws.ARN {
	var out []aws.ARN
	for _, a := range slices.Concat(have, add) {
		if a != (aws.ARN{}) && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// ring holds committed entries oldest first and numbers them in recording
// order.
type ring struct {
	items []*entry
	start int    // index of the oldest entry in items
	next  uint64 // seq of the next entry pushed
}

func (r *ring) len() int { return len(r.items) - r.start }

// push appends e and numbers it.
func (r *ring) push(e *entry) {
	e.seq = r.next
	r.next++
	r.items = append(r.items, e)
}

// pop removes and returns the oldest entry; the ring must not be empty.
func (r *ring) pop() *entry {
	e := r.items[r.start]
	r.items[r.start] = nil
	r.start++
	if r.start >= 1024 && r.start*2 >= len(r.items) {
		r.items = slices.Clone(r.items[r.start:])
		r.start = 0
	}
	return e
}

// oldest returns the seq of the oldest retained entry, or next when the ring
// is empty. Retained entries are numbered consecutively from it.
func (r *ring) oldest() uint64 {
	if r.len() == 0 {
		return r.next
	}
	return r.items[r.start].seq
}

// from returns the retained entries numbered seq or later, oldest first.
func (r *ring) from(seq uint64) []*entry {
	all := r.items[r.start:]
	i, _ := slices.BinarySearchFunc(all, seq, func(e *entry, s uint64) int { return cmp.Compare(e.seq, s) })
	return all[i:]
}

// minRememberedEvictions is the fewest evicted ids a journal remembers, so a
// small ring still counts exactly what a consumer a few thousand events
// behind missed. 8 bytes each.
const minRememberedEvictions = 4096

// evictions remembers the ids of the latest evicted events in eviction
// order. The ring evicts in recording order, so the ids remembered after an
// id are exactly the evicted events recorded after it.
type evictions struct {
	ids   []ref.EventID // circular once it holds limit ids
	head  int           // index of the oldest id once full
	limit int
	total uint64 // events evicted so far
}

// add remembers id, forgetting the oldest id when full.
func (v *evictions) add(id ref.EventID) {
	v.total++
	if len(v.ids) < v.limit {
		v.ids = append(v.ids, id)
		return
	}
	v.ids[v.head] = id
	v.head = (v.head + 1) % v.limit
}

// after returns how many events were evicted after the event id, or false
// when id is not remembered. It searches newest first, since a resuming
// consumer is usually not far behind.
func (v *evictions) after(id ref.EventID) (uint64, bool) {
	n := len(v.ids)
	var later uint64
	for k := n - 1; k >= 0; k-- {
		if v.ids[(v.head+k)%n] == id {
			return later, true
		}
		later++
	}
	return 0, false
}
