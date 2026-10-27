package shardlog

import (
	"errors"
	"fmt"

	"github.com/sustinbebustin/understudy/internal/clock"
)

// errNotImplemented is what the parts of the log that are not built yet
// answer.
var errNotImplemented = errors.New("shardlog: not implemented yet")

// Seq is a sequence number: decimal digits without a leading zero, or "0".
// String order and numeric order disagree for numbers of different lengths
// (dynamodb-streams.md 4.3), so compare with Compare.
type Seq string

// Compare orders s and o numerically, by length and then by bytes, and
// returns -1, 0 or +1. Not implemented yet: it answers 0.
func (s Seq) Compare(_ Seq) int { return 0 }

// ParseSeq accepts 1 to 129 decimal digits without a leading zero, or "0"
// (kinesis.md 3.4); anything else is a *SeqError. Not implemented yet.
func ParseSeq(_ string) (Seq, error) { return "", errNotImplemented }

// SeqError is text that is not a sequence number.
type SeqError struct{ Text string }

func (e *SeqError) Error() string {
	return fmt.Sprintf("%q is not a sequence number: it must be decimal digits without a leading zero", e.Text)
}

// ShardInfo is what a Sequencer may read about a shard.
type ShardInfo struct {
	ID string `json:"id"`
	// Index is the shard's position in its stream: 0 for DynamoDB's single
	// shard.
	Index  int           `json:"index"`
	Opened clock.Instant `json:"opened"`
}

// Sequencer mints the sequence numbers of a shard, one implementation per
// format; which one a service uses is its sequence-number switch.
type Sequencer interface {
	// Start is the shard's StartingSequenceNumber.
	Start(shard ShardInfo) Seq
	// Next is the number of a record appended at at; prev is the shard's
	// last record's number, "" for its first record.
	Next(shard ShardInfo, prev Seq, at clock.Instant) Seq
}

// DecimalCounter is a per-shard decimal counter: Start is 10^(Digits-1),
// the first record is Start+1, or Start when FirstAtStart, and each next
// record is prev+1, growing past Digits digits rather than wrapping.
// dynamodb.streams.sequence-number decimal-21 is DecimalCounter{Digits: 21};
// start-equals-first, DynamoDB Local's form (dynamodb-streams.md 8.3), adds
// FirstAtStart.
type DecimalCounter struct {
	Digits       int
	FirstAtStart bool
}

// Start returns the shard's starting number. Not implemented yet: it
// answers "".
func (c DecimalCounter) Start(ShardInfo) Seq { return "" }

// Next returns the next record's number. Not implemented yet: it answers
// "".
func (c DecimalCounter) Next(_ ShardInfo, _ Seq, _ clock.Instant) Seq { return "" }
