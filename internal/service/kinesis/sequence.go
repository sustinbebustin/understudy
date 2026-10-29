package kinesis

import (
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// V2Sequencer is kinesis.sequence.layout v2-56-digit: the decimal value of
// 47 hex digits that encode the shard's creation second and index, a
// per-shard counter and the record's arrival second (kinesis.md 3.4).
type V2Sequencer struct{}

var _ shardlog.Sequencer = V2Sequencer{}

// Start is the shard's StartingSequenceNumber. Not implemented yet: it
// returns "".
func (V2Sequencer) Start(shardlog.ShardInfo) shardlog.Seq { return "" }

// Next is the number of a record appended at after prev. Not
// implemented yet: it returns "".
func (V2Sequencer) Next(shardlog.ShardInfo, shardlog.Seq, clock.Instant) shardlog.Seq { return "" }

// V2Fields are the fields of a V2 sequence number.
type V2Fields struct {
	// ShardCreated is the shard's creation, Unix seconds.
	ShardCreated int64
	ShardIndex   int
	Counter      uint64
	// Record is the record's arrival, Unix seconds.
	Record int64
}

// DecodeV2 reads a V2 sequence number; false when s is not one. Not
// implemented yet: it returns false.
func DecodeV2(shardlog.Seq) (V2Fields, bool) { return V2Fields{}, false }
