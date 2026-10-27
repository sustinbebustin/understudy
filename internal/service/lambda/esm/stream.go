package esm

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// DynamoDBStream is a DynamoDB stream; the mapping's source is the
// stream's ARN. Mappings on one are not accepted yet: not implemented yet.
type DynamoDBStream struct {
	Position StreamStart    `json:"position"`
	Settings StreamSettings `json:"settings"`
}

func (DynamoDBStream) isEventSourceKind() {}

// StreamStart is a stream mapping's StartingPosition. AT_TIMESTAMP is not
// one: on a DynamoDB stream it is refused (lambda-stream-sources.md 3.3
// row 3).
//
//sumtype:decl
type StreamStart interface{ isStreamStart() }

// TrimHorizon is TRIM_HORIZON: every record the stream still holds.
type TrimHorizon struct{}

// Latest is LATEST: the records written after the mapping is created.
type Latest struct{}

func (TrimHorizon) isStreamStart() {}
func (Latest) isStreamStart()      {}

// ParseStreamStart reads a StartingPosition: TRIM_HORIZON or LATEST, and
// false for anything else. Not implemented yet: it answers false.
func ParseStreamStart(_ string) (StreamStart, bool) { return nil, false }

// StreamStartName is p's wire name. Not implemented yet: it answers "".
func StreamStartName(_ StreamStart) string { return "" }

// StreamSettings are the members only a stream mapping has.
type StreamSettings struct {
	RecordAge RecordAge `json:"recordAge"`
	Retries   Retries   `json:"retries"`
	OnFailure OnFailure `json:"onFailure"`
}

// DefaultStreamSettings are a stream mapping's settings when Create sets
// none (lambda-stream-sources.md 3.5): any age, unlimited retries, no
// destination.
func DefaultStreamSettings() StreamSettings {
	return StreamSettings{RecordAge: AnyAge{}, Retries: Unlimited{}, OnFailure: NoDestination{}}
}

// RecordAge is MaximumRecordAgeInSeconds.
//
//sumtype:decl
type RecordAge interface{ isRecordAge() }

// AnyAge is -1: records are never discarded for their age.
type AnyAge struct{}

// MaxAge discards records older than Seconds, 60 to 604800.
type MaxAge struct {
	Seconds int32 `json:"seconds"`
}

func (AnyAge) isRecordAge() {}
func (MaxAge) isRecordAge() {}

// Retries is MaximumRetryAttempts.
//
//sumtype:decl
type Retries interface{ isRetries() }

// Unlimited is -1: a failing batch is retried until its records expire.
type Unlimited struct{}

// Attempts is 0 to 10000 retries: N+1 invocations at most.
type Attempts struct {
	N int32 `json:"n"`
}

func (Unlimited) isRetries() {}
func (Attempts) isRetries()  {}

// OnFailure is DestinationConfig.OnFailure on a stream mapping.
//
//sumtype:decl
type OnFailure interface{ isOnFailure() }

// NoDestination drops discarded batches' records.
type NoDestination struct{}

// QueueDestination sends each on-failure record to an SQS queue.
type QueueDestination struct {
	ARN aws.ARN `json:"arn"`
}

// TopicDestination publishes each on-failure record to an SNS topic.
type TopicDestination struct {
	ARN aws.ARN `json:"arn"`
}

func (NoDestination) isOnFailure()    {}
func (QueueDestination) isOnFailure() {}
func (TopicDestination) isOnFailure() {}

// StreamSource reads one stream for the poller, through kernel.Invoke as
// the mapping's execution role, one implementation per kind, so the poller
// holds no kind-specific code (lambda-stream-sources.md 12.1 item 4).
type StreamSource interface {
	// Shards lists the stream's shard ids.
	Shards(ctx context.Context) ([]string, error)
	// Read returns up to limit records of shard from from: a
	// *shardlog.TrimmedError when from is behind the trim horizon, a
	// *GoneError when the stream or shard no longer exists.
	Read(ctx context.Context, shard string, from shardlog.Position, limit int) (StreamPage, error)
}

// StreamPage is what one Read returned.
type StreamPage struct {
	Records []StreamRecord
	Next    shardlog.Position
	// Closed is a closed shard whose Next reads nothing more.
	Closed bool
}

// StreamRecord is one stream record as the poller handles it.
type StreamRecord struct {
	Seq     shardlog.Seq
	Arrival clock.Instant
	// Event is the record as the function receives it (dynamodb-streams.md
	// 7.8).
	Event json.RawMessage
	// Cause is the event of the write that appended it; 0 when unknown.
	Cause ref.EventID
}

// MarshalJSON returns the record as the function receives it, so a Batch
// of StreamRecords encodes its event. Not implemented yet: it answers null.
func (r StreamRecord) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// GoneError is a stream or shard that no longer exists.
type GoneError struct {
	Stream aws.ARN
	Reason string
}

func (e *GoneError) Error() string { return "stream " + e.Stream.String() + " is gone: " + e.Reason }

// Checkpoint is where a stream reader resumes, kept per mapping and shard.
type Checkpoint struct {
	Shard string            `json:"shard"`
	Next  shardlog.Position `json:"next"`
}

// DynamoDBRetryDelay is the delay before a failed batch of a DynamoDB
// stream is retried: the documented polling rate of four times a second
// (lambda-stream-sources.md 4.1), used under lambda.esm-stream.retry-delay
// poll-interval.
const DynamoDBRetryDelay = 250 * time.Millisecond

// StreamFidelity holds the stream poller's switch values; the zero value
// is every default.
type StreamFidelity struct {
	LatestAnchor LatestAnchor
	RetryDelay   RetryDelay
	ResendBudget ResendBudget
}

// LatestAnchor is lambda.esm-stream.latest-anchor: when LATEST is resolved.
type LatestAnchor uint8

// LatestAnchor values; the zero value is AnchorCreateInstant.
const (
	// AnchorCreateInstant resolves LATEST before Create answers.
	AnchorCreateInstant LatestAnchor = iota
	// AnchorFirstRead resolves it when the reader first reads.
	AnchorFirstRead
)

func (a LatestAnchor) String() string {
	switch a {
	case AnchorCreateInstant:
		return "create-instant"
	case AnchorFirstRead:
		return "first-read"
	}
	return "LatestAnchor(" + strconv.Itoa(int(a)) + ")"
}

// RetryDelay is lambda.esm-stream.retry-delay: how long after a failed
// invocation its batch is retried.
type RetryDelay uint8

// RetryDelay values; the zero value is RetryAfterPollInterval.
const (
	// RetryAfterPollInterval waits the source's documented polling interval.
	RetryAfterPollInterval RetryDelay = iota
	// RetryImmediately retries at once.
	RetryImmediately
)

func (d RetryDelay) String() string {
	switch d {
	case RetryAfterPollInterval:
		return "poll-interval"
	case RetryImmediately:
		return "immediate"
	}
	return "RetryDelay(" + strconv.Itoa(int(d)) + ")"
}

// ResendBudget is lambda.esm-stream.partial-resend-budget: the attempt
// count of the records a partial batch response sends again.
type ResendBudget uint8

// ResendBudget values; the zero value is ResendRestart.
const (
	// ResendRestart starts the resend's own count.
	ResendRestart ResendBudget = iota
	// ResendShared continues the original batch's count.
	ResendShared
)

func (b ResendBudget) String() string {
	switch b {
	case ResendRestart:
		return "restart"
	case ResendShared:
		return "shared"
	}
	return "ResendBudget(" + strconv.Itoa(int(b)) + ")"
}

// StreamSwitches returns the stream poller's switch entries. Not
// implemented yet: it answers none.
func StreamSwitches(_ StreamFidelity) []fidelity.Entry { return nil }
