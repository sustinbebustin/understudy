package kinesis

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/shardlog"
	"github.com/sustinbebustin/understudy/internal/store"
)

// State is the Kinesis state of one account and region (kinesis.md 13.5).
// Streams are values, replaced on change and never mutated in place, so a
// value read out of a cell never aliases the cell, except for its shard
// logs, which every copy shares and only code under the cell's lock reads.
type State struct {
	// Streams are by name.
	Streams map[string]Stream `json:"streams"`
	// TokenKey signs the scope's ListShards tokens and shard iterators:
	// drawn from the seeded id streams by the first CreateStream of the
	// scope, kept in the snapshot.
	TokenKey []byte `json:"tokenKey,omitempty"`
}

func newState(aws.Scope) State { return State{Streams: map[string]Stream{}} }

// IsZero reports a scope without streams, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Streams) == 0 }

// BlobRefs lists one entry per blob reference the state holds: one per
// record.
func (s *State) BlobRefs() []string {
	var out []string
	for _, str := range s.Streams {
		for _, ref := range str.blobRefs() {
			out = append(out, ref.String())
		}
	}
	return out
}

// shardCount is the scope's shards, all open in slice 1 (no resharding),
// counted against shardLimit (aws-doc kinesis.md 4.1).
func (s *State) shardCount() int {
	n := 0
	for _, str := range s.Streams {
		n += len(str.Shards)
	}
	return n
}

// Partitioned finds BlobRefs by a type assertion; this keeps a rename from
// silently dropping the records' data from a snapshot.
var _ store.BlobHolder = (*State)(nil)

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// Stream is one data stream. Status transitions are final, so a stored
// stream is ACTIVE.
type Stream struct {
	Name string `json:"name"`
	// Created is the emulator instant of the CreateStream call; it opens
	// every shard (kinesis.md 13.5 item 1).
	Created clock.Instant         `json:"created"`
	Mode    kinesisapi.StreamMode `json:"mode"`
	// RetentionHours is RetentionPeriodHours, 24 to 8760.
	RetentionHours int32 `json:"retentionHours"`
	// MaxRecordSizeKiB is MaxRecordSizeInKiB, 1024 unless CreateStream set it.
	MaxRecordSizeKiB int32             `json:"maxRecordSizeKiB"`
	Tags             map[string]string `json:"tags,omitempty"`
	// Metrics are the enabled shard-level metrics in MetricsName enum
	// order, never ALL; stored and reported, nothing published
	// (EnableEnhancedMonitoring note).
	Metrics []kinesisapi.MetricsName `json:"metrics,omitempty"`
	// Shards are in index order; shard i is shardId-<i, 12 digits>.
	Shards []Shard `json:"shards"`
}

// blobRefs lists the data of every record of the stream.
func (s Stream) blobRefs() []blob.Ref {
	var out []blob.Ref
	for _, sh := range s.Shards {
		for _, e := range sh.log.State().Entries {
			out = append(out, e.Value.Data)
		}
	}
	return out
}

// Shard is one shard of a stream: its hash key range and its records.
// Shards never split or merge in slice 1, so every shard is open.
type Shard struct {
	Range HashRange
	// Layout is the kinesis.sequence.layout the shard was created under,
	// which numbers its records for as long as it lives, a snapshot load
	// included.
	Layout SequenceLayout
	// log holds the shard's records. Every copy of the Stream shares it,
	// and it is read and changed only under the cell's lock.
	log *shardlog.Shard[Record]
}

// Record is one record of a shard log. Its sequence number
// and arrival instant are the log entry's.
type Record struct {
	PartitionKey string `json:"key"`
	// Data is the record's bytes in the blob store, one reference held by
	// the state; Size is their length.
	Data blob.Ref `json:"data"`
	Size int      `json:"size"`
	// ExplicitHashKey is kept as sent and never returned.
	ExplicitHashKey string `json:"explicitHashKey,omitempty"`
}

// bytes is what a record counts against GetRecords' 10 MiB page and the
// record size limit: its data plus its UTF-8 partition key.
func (r Record) bytes() int { return r.Size + len(r.PartitionKey) }

// newShard is shard i of a stream created at opened, numbered by layout.
func newShard(i int, r HashRange, opened clock.Instant, layout SequenceLayout) Shard {
	info := shardlog.ShardInfo{ID: shardID(i), Index: i, Opened: opened}
	return Shard{Range: r, Layout: layout, log: shardlog.NewShard[Record](info, layout.sequencer())}
}

// savedShard is a Shard as a snapshot holds it.
type savedShard struct {
	Range  HashRange                   `json:"range"`
	Layout SequenceLayout              `json:"layout"`
	Log    shardlog.ShardState[Record] `json:"log"`
}

// MarshalJSONTo writes sh as a savedShard.
func (sh *Shard) MarshalJSONTo(enc *jsontext.Encoder) error {
	return json.MarshalEncode(enc, savedShard{Range: sh.Range, Layout: sh.Layout, Log: sh.log.State()})
}

// UnmarshalJSONFrom reads what MarshalJSONTo writes, with a log of its own.
func (sh *Shard) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var v savedShard
	if err := json.UnmarshalDecode(dec, &v); err != nil {
		return err
	}
	*sh = Shard{Range: v.Range, Layout: v.Layout, log: shardlog.RestoreShard(v.Log, v.Layout.sequencer())}
	return nil
}

// shardID is shardId- and the 12-digit zero-padded index (kinesis.md 3.2).
func shardID(i int) string { return fmt.Sprintf("shardId-%012d", i) }

// openedBy reports whether the stream's shards were open at t, to the
// millisecond: every shard opens when its stream is created (kinesis.md
// 13.5 item 1).
func (s Stream) openedBy(t clock.Instant) bool { return millis(s.Created).Compare(millis(t)) <= 0 }

// millis truncates i to the millisecond, the precision of a wire
// timestamp.
func millis(i clock.Instant) clock.Instant {
	return clock.FromWire(i.Wire().Truncate(time.Millisecond))
}

// streamARN is arn:<partition>:kinesis:<region>:<account>:stream/<name>
// (kinesis.md 3.1).
func streamARN(scope aws.Scope, name string) aws.ARN {
	return aws.ARN{Partition: scope.Partition.String(), Service: "kinesis", Region: scope.Region.String(), Account: scope.Account.String(), Resource: "stream/" + name}
}

// stateCodec is the snapshot codec of State.
func stateCodec() store.Codec {
	c := jsonx.NewCodec()
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}
