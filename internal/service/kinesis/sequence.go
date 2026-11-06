package kinesis

import (
	"fmt"
	"math/big"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// V2Sequencer is kinesis.sequence.layout v2-56-digit: the decimal value of
// 47 hex digits that encode the shard's creation second and index, a
// per-shard counter and the record's arrival second (kinesis.md 3.4):
//
//	2 | created (9) | index & 0xf (1) | counter (16) | 00 | record (9) | index (8) | 2
//
// The 00 byte is what every documented number decodes to. The counter
// precedes the record second, so numbers strictly increase within a shard
// whatever the clock does.
type V2Sequencer struct{}

var _ shardlog.Sequencer = V2Sequencer{}

// v2Digits and v2HexDigits are the decimal and hex lengths of a V2 number.
const (
	v2Digits    = 56
	v2HexDigits = 47
)

// v2SecondsMod is 16^9: a second field holds nine hex digits.
const v2SecondsMod = 1 << 36

// Start is the shard's StartingSequenceNumber: counter 0, stamped with the
// shard's creation second.
func (V2Sequencer) Start(shard shardlog.ShardInfo) shardlog.Seq {
	created := shard.Opened.Wire().Unix()
	return encodeV2(V2Fields{ShardCreated: created, ShardIndex: shard.Index, Record: created})
}

// Next is the number of a record appended at after prev: prev's counter
// plus one (1 for the shard's first record), stamped with at's second.
func (V2Sequencer) Next(shard shardlog.ShardInfo, prev shardlog.Seq, at clock.Instant) shardlog.Seq {
	var counter uint64 = 1
	if f, ok := DecodeV2(prev); ok {
		counter = f.Counter + 1
	}
	return encodeV2(V2Fields{ShardCreated: shard.Opened.Wire().Unix(), ShardIndex: shard.Index, Counter: counter, Record: at.Wire().Unix()})
}

// V2Fields are the fields of a V2 sequence number.
type V2Fields struct {
	// ShardCreated is the shard's creation, Unix seconds.
	ShardCreated int64
	ShardIndex   int
	Counter      uint64
	// Record is the record's arrival, Unix seconds.
	Record int64
}

// v2Seconds is a second as its nine hex digits hold it: below 0 is 0,
// above 16^9-1 wraps.
func v2Seconds(s int64) int64 {
	if s < 0 {
		return 0
	}
	return s % v2SecondsMod
}

func encodeV2(f V2Fields) shardlog.Seq {
	h := fmt.Sprintf("2%09x%01x%016x00%09x%08x2", v2Seconds(f.ShardCreated), f.ShardIndex&0xf, f.Counter, v2Seconds(f.Record), f.ShardIndex)
	n, _ := new(big.Int).SetString(h, 16)
	return shardlog.Seq(n.String())
}

// DecodeV2 reads a V2 sequence number; false when s is not one: not 56
// decimal digits, or digits that do not follow the layout.
func DecodeV2(s shardlog.Seq) (V2Fields, bool) {
	if len(s) != v2Digits || s[0] == '0' {
		return V2Fields{}, false
	}
	n, ok := new(big.Int).SetString(string(s), 10)
	if !ok || n.Sign() < 0 {
		return V2Fields{}, false
	}
	h := n.Text(16)
	if len(h) != v2HexDigits || h[0] != '2' || h[46] != '2' || h[27:29] != "00" {
		return V2Fields{}, false
	}
	field := func(from, to int) uint64 {
		v, err := strconv.ParseUint(h[from:to], 16, 64)
		if err != nil {
			ok = false
		}
		return v
	}
	created, low, counter, record, index := field(1, 10), field(10, 11), field(11, 27), field(29, 38), field(38, 46)
	if !ok || index&0xf != low {
		return V2Fields{}, false
	}
	return V2Fields{ShardCreated: int64(created), ShardIndex: int(index), Counter: counter, Record: int64(record)}, true //nolint:gosec // nine hex digits fit int64 and eight fit int
}
