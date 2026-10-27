package shardlog

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/clock"
)

// Seq is a sequence number: decimal digits without a leading zero, or "0".
// String order and numeric order disagree for numbers of different lengths
// (dynamodb-streams.md 4.3), so compare with Compare.
type Seq string

// Compare orders s and o numerically, by length and then by bytes, and
// returns -1, 0 or +1.
func (s Seq) Compare(o Seq) int {
	switch {
	case len(s) < len(o):
		return -1
	case len(s) > len(o):
		return 1
	}
	return strings.Compare(string(s), string(o))
}

// maxSeqDigits is the longest sequence number the Kinesis model allows
// (kinesis.md 3.4).
const maxSeqDigits = 129

// ParseSeq accepts 1 to 129 decimal digits without a leading zero, or "0"
// (kinesis.md 3.4); anything else is a *SeqError.
func ParseSeq(s string) (Seq, error) {
	if s == "" || len(s) > maxSeqDigits || (s[0] == '0' && len(s) > 1) {
		return "", &SeqError{Text: s}
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return "", &SeqError{Text: s}
		}
	}
	return Seq(s), nil
}

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

// Start returns 10^(Digits-1), "1" when Digits is below 1.
func (c DecimalCounter) Start(ShardInfo) Seq {
	return Seq("1" + strings.Repeat("0", max(c.Digits-1, 0)))
}

// Next returns the next record's number.
func (c DecimalCounter) Next(shard ShardInfo, prev Seq, _ clock.Instant) Seq {
	if prev == "" {
		if c.FirstAtStart {
			return c.Start(shard)
		}
		return increment(c.Start(shard))
	}
	return increment(prev)
}

// increment returns s+1 for a decimal s.
func increment(s Seq) Seq {
	b := []byte(s)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < '9' {
			b[i]++
			return Seq(b)
		}
		b[i] = '0'
	}
	return Seq("1" + string(b))
}
