package shardlog

import (
	"fmt"
	"slices"

	"github.com/sustinbebustin/understudy/internal/clock"
)

// Position is where a read starts.
//
//sumtype:decl
type Position interface{ isPosition() }

// TrimHorizon is the oldest untrimmed record.
type TrimHorizon struct{}

// Latest is just after the newest record.
type Latest struct{}

// At is the record numbered Seq, or the first after it.
type At struct {
	Seq Seq `json:"seq"`
}

// After is the first record numbered above Seq.
type After struct {
	Seq Seq `json:"seq"`
}

// AtTime is the first record whose Arrival is at or after At.
type AtTime struct {
	At clock.Instant `json:"at"`
}

func (TrimHorizon) isPosition() {}
func (Latest) isPosition()      {}
func (At) isPosition()          {}
func (After) isPosition()       {}
func (AtTime) isPosition()      {}

// Entry is one record of a shard.
type Entry[R any] struct {
	Seq     Seq           `json:"seq"`
	Arrival clock.Instant `json:"arrival"`
	Value   R             `json:"value"`
}

// SeqRange is a shard's SequenceNumberRange.
//
//sumtype:decl
type SeqRange interface{ isSeqRange() }

// Open is a shard records can still be appended to.
type Open struct{ Start Seq }

// Closed is a closed shard: End is its last record's number, or Start when
// it never held one.
type Closed struct{ Start, End Seq }

func (Open) isSeqRange()   {}
func (Closed) isSeqRange() {}

// Shard is one shard's append-only log. It is not safe for concurrent use:
// its owner serializes the calls.
type Shard[R any] struct {
	info ShardInfo
	seq  Sequencer
	st   ShardState[R]
}

// NewShard returns an empty open shard whose numbers seq mints.
func NewShard[R any](info ShardInfo, seq Sequencer) *Shard[R] {
	return &Shard[R]{info: info, seq: seq, st: ShardState[R]{Info: info, Start: seq.Start(info)}}
}

// Info returns what the shard was made with.
func (s *Shard[R]) Info() ShardInfo { return s.info }

func (s *Shard[R]) closed() bool { return s.st.End != "" }

// Append adds v as arriving at at, numbered by the shard's Sequencer; a
// closed shard refuses it with *ClosedError.
func (s *Shard[R]) Append(at clock.Instant, v R) (Entry[R], error) {
	if s.closed() {
		return Entry[R]{}, &ClosedError{Shard: s.info.ID}
	}
	e := Entry[R]{Seq: s.seq.Next(s.info, s.st.Last, at), Arrival: at, Value: v}
	s.st.Entries = append(s.st.Entries, e)
	s.st.Last = e.Seq
	return e, nil
}

// Resolve turns Latest and AtTime into the position they name now:
// Latest is After the last appended number, or TrimHorizon when nothing was
// ever appended; AtTime is At the first record arriving at or after its
// instant, TrimHorizon when that is at or before the oldest untrimmed
// record, and Latest's resolution when no record qualifies. TrimHorizon, At
// and After come back unchanged.
func (s *Shard[R]) Resolve(p Position) Position {
	switch p := p.(type) {
	case Latest:
		if s.st.Last == "" {
			return TrimHorizon{}
		}
		return After{Seq: s.st.Last}
	case AtTime:
		es := s.st.Entries
		if len(es) > 0 && p.At.Compare(es[0].Arrival) <= 0 {
			return TrimHorizon{}
		}
		for _, e := range es {
			if e.Arrival.Compare(p.At) >= 0 {
				return At{Seq: e.Seq}
			}
		}
		return s.Resolve(Latest{})
	case TrimHorizon, At, After:
	}
	return p
}

// rest returns the untrimmed entries a read from the resolved position p
// starts with, or a *TrimmedError.
func (s *Shard[R]) rest(p Position) ([]Entry[R], error) {
	es := s.st.Entries
	var first func(Entry[R]) bool
	switch p := p.(type) {
	case TrimHorizon:
		return es, nil
	case At:
		if s.st.Trimmed != "" && p.Seq.Compare(s.st.Trimmed) <= 0 {
			return nil, &TrimmedError{Shard: s.info.ID, From: p}
		}
		first = func(e Entry[R]) bool { return e.Seq.Compare(p.Seq) >= 0 }
	case After:
		if s.st.Trimmed != "" && p.Seq.Compare(s.st.Trimmed) < 0 {
			return nil, &TrimmedError{Shard: s.info.ID, From: p}
		}
		first = func(e Entry[R]) bool { return e.Seq.Compare(p.Seq) > 0 }
	case Latest, AtTime:
		// Resolve never returns these.
		return nil, nil
	}
	i := slices.IndexFunc(es, first)
	if i < 0 {
		return nil, nil
	}
	return es[i:], nil
}

// Read returns up to limit entries from Resolve(from), stopping before the
// entry that would take the sum of size over the values past maxBytes; the
// first entry is always returned when one exists. next is After the last
// returned number, or Resolve(from) when nothing was returned. A limit or
// maxBytes below 1 reads nothing. A position behind the trim point is a
// *TrimmedError.
func (s *Shard[R]) Read(from Position, limit, maxBytes int, size func(R) int) (entries []Entry[R], next Position, err error) {
	p := s.Resolve(from)
	if limit < 1 || maxBytes < 1 {
		return nil, p, nil
	}
	rest, err := s.rest(p)
	if err != nil {
		return nil, nil, err
	}
	sum := 0
	for _, e := range rest {
		n := size(e.Value)
		if len(entries) == limit || (len(entries) > 0 && sum+n > maxBytes) {
			break
		}
		sum += n
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, p, nil
	}
	return entries, After{Seq: entries[len(entries)-1].Seq}, nil
}

// Trim drops every entry whose Arrival is at or before through.
func (s *Shard[R]) Trim(through clock.Instant) {
	n := 0
	for n < len(s.st.Entries) && s.st.Entries[n].Arrival.Compare(through) <= 0 {
		n++
	}
	if n == 0 {
		return
	}
	s.st.Trimmed = s.st.Entries[n-1].Seq
	s.st.Entries = slices.Clone(s.st.Entries[n:])
}

// Close ends the shard at at; a second Close changes nothing.
func (s *Shard[R]) Close(at clock.Instant) {
	if s.closed() {
		return
	}
	s.st.End = s.st.Last
	if s.st.End == "" {
		s.st.End = s.st.Start
	}
	s.st.Closed = at
}

// Range is Open until Close, then Closed.
func (s *Shard[R]) Range() SeqRange {
	if s.closed() {
		return Closed{Start: s.st.Start, End: s.st.End}
	}
	return Open{Start: s.st.Start}
}

// Exhausted reports that the shard is closed and a read from p can return
// nothing more.
func (s *Shard[R]) Exhausted(p Position) bool {
	if !s.closed() {
		return false
	}
	rest, err := s.rest(s.Resolve(p))
	return err == nil && len(rest) == 0
}

// State returns the shard for a snapshot.
func (s *Shard[R]) State() ShardState[R] {
	st := s.st
	st.Entries = slices.Clone(s.st.Entries)
	return st
}

// RestoreShard rebuilds a shard State returned.
func RestoreShard[R any](st ShardState[R], seq Sequencer) *Shard[R] {
	st.Entries = slices.Clone(st.Entries)
	return &Shard[R]{info: st.Info, seq: seq, st: st}
}

// ShardState is a shard as a snapshot holds it.
type ShardState[R any] struct {
	Info  ShardInfo `json:"info"`
	Start Seq       `json:"start"`
	// Last is the last appended number, "" when none was.
	Last Seq `json:"last,omitempty"`
	// Trimmed is the highest trimmed number, "" when none was.
	Trimmed Seq `json:"trimmed,omitempty"`
	// End and Closed are set once the shard is closed.
	End     Seq           `json:"end,omitempty"`
	Closed  clock.Instant `json:"closed,omitzero"`
	Entries []Entry[R]    `json:"entries"`
}

// ClosedError is an Append to a closed shard.
type ClosedError struct{ Shard string }

func (e *ClosedError) Error() string {
	return "shard " + e.Shard + " is closed: no record can be appended"
}

// TrimmedError is a read from a position whose next record was trimmed.
type TrimmedError struct {
	Shard string
	From  Position
}

func (e *TrimmedError) Error() string {
	return "shard " + e.Shard + ": the records from " + positionText(e.From) + " on have been trimmed"
}

// positionText names p by the shard iterator type that asks for it.
func positionText(p Position) string {
	switch p := p.(type) {
	case TrimHorizon:
		return "TRIM_HORIZON"
	case Latest:
		return "LATEST"
	case At:
		return "AT_SEQUENCE_NUMBER " + string(p.Seq)
	case After:
		return "AFTER_SEQUENCE_NUMBER " + string(p.Seq)
	case AtTime:
		return "AT_TIMESTAMP " + p.At.String()
	}
	return fmt.Sprintf("%#v", p)
}
