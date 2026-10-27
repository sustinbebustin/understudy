package shardlog

import (
	"fmt"

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
}

// NewShard returns an empty open shard whose numbers seq mints.
func NewShard[R any](info ShardInfo, seq Sequencer) *Shard[R] {
	return &Shard[R]{info: info, seq: seq}
}

// Info returns what the shard was made with.
func (s *Shard[R]) Info() ShardInfo { return s.info }

// Append adds v as arriving at at, numbered by the shard's Sequencer; a
// closed shard refuses it with *ClosedError. Not implemented yet.
func (s *Shard[R]) Append(_ clock.Instant, _ R) (Entry[R], error) {
	return Entry[R]{}, errNotImplemented
}

// Resolve turns Latest and AtTime into the position they name now:
// Latest is After the last appended number, or TrimHorizon when nothing was
// ever appended; AtTime is At the first record arriving at or after its
// instant, TrimHorizon when that is at or before the oldest untrimmed
// record, and Latest's resolution when no record qualifies. TrimHorizon, At
// and After come back unchanged. Not implemented yet: p comes back as it is.
func (s *Shard[R]) Resolve(p Position) Position { return p }

// Read returns up to limit entries from Resolve(from), stopping before the
// entry that would take the sum of size over the values past maxBytes; the
// first entry is always returned when one exists. next is After the last
// returned number, or Resolve(from) when nothing was returned. A limit or
// maxBytes below 1 reads nothing. A position behind the trim point is a
// *TrimmedError. Not implemented yet.
func (s *Shard[R]) Read(from Position, _, _ int, _ func(R) int) (entries []Entry[R], next Position, err error) {
	return nil, from, errNotImplemented
}

// Trim drops every entry whose Arrival is at or before through. Not
// implemented yet: it drops nothing.
func (s *Shard[R]) Trim(_ clock.Instant) {}

// Close ends the shard at at; a second Close changes nothing. Not
// implemented yet: the shard stays open.
func (s *Shard[R]) Close(_ clock.Instant) {}

// Range is Open until Close, then Closed. Not implemented yet: it answers
// Open with no start.
func (s *Shard[R]) Range() SeqRange { return Open{} }

// Exhausted reports that the shard is closed and a read from p can return
// nothing more. Not implemented yet: it answers false.
func (s *Shard[R]) Exhausted(_ Position) bool { return false }

// State returns the shard for a snapshot. Not implemented yet: it holds
// the shard's info only.
func (s *Shard[R]) State() ShardState[R] { return ShardState[R]{Info: s.info} }

// RestoreShard rebuilds a shard State returned. Not implemented yet: the
// shard comes back empty.
func RestoreShard[R any](st ShardState[R], seq Sequencer) *Shard[R] {
	return NewShard[R](st.Info, seq)
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
	return fmt.Sprintf("shard %s: the records from %#v on have been trimmed", e.Shard, e.From)
}
