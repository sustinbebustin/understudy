package kinesis

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// State is the Kinesis state of one account and region (kinesis.md 13.5).
// Streams are values, replaced on change and never mutated in place, so a
// value read out of a cell never aliases the cell.
type State struct {
	// Streams are by name.
	Streams map[string]Stream `json:"streams"`
}

func newState(aws.Scope) State { return State{Streams: map[string]Stream{}} }

// IsZero reports a scope without streams, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Streams) == 0 }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// Stream is one data stream. Status transitions are final, so a stored
// stream is ACTIVE (contract 0).
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
	// Shards are in index order; shard i is shardId-<i, 12 digits>.
	Shards []Shard `json:"shards"`
}

// Shard is one shard of a stream: its hash key range. Shards never split
// or merge in slice 1, so every shard is open.
type Shard struct {
	Range HashRange `json:"range"`
}

// shardID is shardId- and the 12-digit zero-padded index (kinesis.md 3.2).
func shardID(i int) string { return fmt.Sprintf("shardId-%012d", i) }

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
