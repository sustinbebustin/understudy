package kinesis_test

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/service/kinesis"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// v2Layout writes fields in kinesis.sequence.layout v2-56-digit, the
// switch's value: the decimal value of the 47 hex digits
// 2 | created (9) | index&0xf (1) | counter (16) | 00 | record (9) | index (8) | 2
// (contract 3.1; kinesis.md 3.4).
func v2Layout(f kinesis.V2Fields) string {
	sec := func(s int64) int64 {
		if s < 0 {
			return 0
		}
		return s % (1 << 36)
	}
	h := fmt.Sprintf("2%09x%01x%016x00%09x%08x2", sec(f.ShardCreated), f.ShardIndex&0xf, f.Counter, sec(f.Record), f.ShardIndex)
	n, ok := new(big.Int).SetString(h, 16)
	if !ok {
		panic("bad hex " + h)
	}
	return n.String()
}

// shardInfoGen draws a Kinesis shard: shardId- and 12 digits from 0
// (kinesis.md 3.2), opened at a stream creation instant.
func shardInfoGen() *rapid.Generator[shardlog.ShardInfo] {
	return rapid.Custom(func(t *rapid.T) shardlog.ShardInfo {
		i := rapid.IntRange(0, 999).Draw(t, "index")
		opened := clock.FromWire(time.Unix(rapid.Int64Range(1_400_000_000, 2_000_000_000).Draw(t, "opened"), 0).UTC())
		return shardlog.ShardInfo{ID: fmt.Sprintf("shardId-%012d", i), Index: i, Opened: opened}
	})
}

// The documented numbers decode under the V2 layout as kinesis.md 3.4
// computed them over the aws-doc examples: the Lambda event's record is
// shard 6 at 2018-12-17T22:10:50Z; the failure record's start and end are
// shard 1 at 2019-11-14T00:38:04Z, the start's counter below the end's.
func TestDecodeV2DocumentedNumbers(t *testing.T) {
	t.Parallel()
	ev, ok := kinesis.DecodeV2("49590338271490256608559692538361571095921575989136588898")
	if !ok || ev.ShardIndex != 6 || ev.Record != 1545084650 {
		t.Errorf("Lambda event number decodes to %+v, %v; want shard 6, record 1545084650", ev, ok)
	}
	start, ok1 := kinesis.DecodeV2("49601189658422359378836298521827638475320189012309704722")
	end, ok2 := kinesis.DecodeV2("49601189658422359378836298522902373528957594348623495186")
	if !ok1 || !ok2 || start.ShardIndex != 1 || end.ShardIndex != 1 || start.Record != 1573691884 || end.Record != 1573691884 || start.Counter >= end.Counter {
		t.Errorf("failure record numbers decode to %+v, %+v", start, end)
	}
	for _, s := range []shardlog.Seq{"", "123", "21269319989653637946712965403778482177", "495903382714902566085596925383615710959215759891365888981"} {
		if f, ok := kinesis.DecodeV2(s); ok {
			t.Errorf("DecodeV2(%q) = %+v, true; want false (not 56 digits)", s, f)
		}
	}
}

// V2Sequencer follows the layout: Start is counter 0 at the shard's
// creation second, each Next counts up from the previous number and stamps
// the arrival second; every number has 56 digits and decodes back
// (contract 3.1; kinesis.md 3.4, where such numbers always have 56 digits).
func TestV2SequencerLayout(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		shard := shardInfoGen().Draw(t, "shard")
		var seq kinesis.V2Sequencer
		created := shard.Opened.Wire().Unix()
		start := seq.Start(shard)
		if want := v2Layout(kinesis.V2Fields{ShardCreated: created, ShardIndex: shard.Index, Record: created}); string(start) != want {
			t.Fatalf("Start(%+v) = %s, want %s", shard, start, want)
		}
		prev := shardlog.Seq("")
		at := shard.Opened
		n := rapid.IntRange(1, 20).Draw(t, "records")
		for i := 1; i <= n; i++ {
			at = at.Add(time.Duration(rapid.Int64Range(0, int64(48*time.Hour)).Draw(t, "gap")))
			next := seq.Next(shard, prev, at)
			want := kinesis.V2Fields{ShardCreated: created, ShardIndex: shard.Index, Counter: uint64(i), Record: at.Wire().Unix()}
			if string(next) != v2Layout(want) || len(next) != 56 {
				t.Fatalf("record %d: Next = %s, want %s (56 digits)", i, next, v2Layout(want))
			}
			if got, ok := kinesis.DecodeV2(next); !ok || got != want {
				t.Fatalf("DecodeV2(%s) = %+v, %v; want %+v", next, got, ok, want)
			}
			prev = next
		}
	})
}

// Sequence numbers strictly increase within a shard under both values of
// kinesis.sequence.layout, whatever the arrival instants, even when the
// clock does not move (spec B3 rapid property; kinesis.md 3.4 "Sequence
// numbers increase over time", aws-doc).
func TestSequencersStrictlyIncreasePerShard(t *testing.T) {
	t.Parallel()
	for name, seq := range map[string]shardlog.Sequencer{
		"v2-56-digit": kinesis.V2Sequencer{},
		"decimal-56":  shardlog.DecimalCounter{Digits: 56},
	} {
		rapid.Check(t, func(t *rapid.T) {
			shard := shardInfoGen().Draw(t, "shard")
			prev := seq.Start(shard)
			last := shardlog.Seq("")
			at := shard.Opened
			for range rapid.IntRange(1, 50).Draw(t, "records") {
				at = at.Add(time.Duration(rapid.Int64Range(0, int64(time.Hour)).Draw(t, "gap")))
				next := seq.Next(shard, last, at)
				if next.Compare(prev) <= 0 {
					t.Fatalf("%s: %s does not follow %s", name, next, prev)
				}
				prev, last = next, next
			}
		})
	}
}
