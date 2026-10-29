package esm

import (
	"bytes"
	"encoding/json"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// Batch is the records of one stream invocation and the size of the event
// they make, counted over {"Records":[...]} as the function receives it
// against MaxPayload ("The payload size reaches 6 MB",
// lambda-stream-sources.md 4.2; counted over the serialized event,
// inferred, ESMS-18).
type Batch[R any] struct {
	records []R
	encoded [][]byte
	// encodedSize is the sum of the encoded records' lengths.
	encodedSize int
}

// Add appends r and reports true, or false, leaving the batch unchanged,
// when r would take the event past MaxPayload; the first record always
// fits, so a batch makes progress whatever its size. r is encoded with
// json.Marshal.
func (b *Batch[R]) Add(r R) (bool, error) {
	enc, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	if len(b.records) > 0 && sizeOf(len(b.records)+1, b.encodedSize+len(enc)) > MaxPayload {
		return false, nil
	}
	b.records = append(b.records, r)
	b.encoded = append(b.encoded, enc)
	b.encodedSize += len(enc)
	return true, nil
}

// Len returns the number of records.
func (b *Batch[R]) Len() int { return len(b.records) }

// Size returns the length of Event.
func (b *Batch[R]) Size() int { return sizeOf(len(b.records), b.encodedSize) }

// Records returns the records in order. The caller must not change them.
func (b *Batch[R]) Records() []R { return b.records }

// Event returns the invocation's payload: {"Records":[...]}.
func (b *Batch[R]) Event() []byte {
	out := make([]byte, 0, b.Size())
	out = append(out, eventOpen...)
	out = append(out, bytes.Join(b.encoded, []byte(","))...)
	return append(out, eventClose...)
}

// FailedBatch is what an on-failure record reports
// (lambda-stream-sources.md 5.6).
type FailedBatch struct {
	// RequestID is the last invocation's request id, or a new Lambda
	// request id for records discarded before any invocation.
	RequestID   string
	FunctionARN string
	Condition   FailCondition
	// InvokeCount is approximateInvokeCount.
	InvokeCount int
	// Timestamp is when the batch was discarded.
	Timestamp clock.Instant
	Stream    aws.ARN
	Shard     string
	// First and Last are the batch's first and last records; their Seq and
	// Arrival are read.
	First, Last StreamRecord
	// Size is batchSize.
	Size int
}

// FailCondition is why a batch was discarded.
//
//sumtype:decl
type FailCondition interface{ isFailCondition() }

// RetryAttemptsExhausted is a batch whose retries ran out; it carries the
// last invocation's response context.
type RetryAttemptsExhausted struct {
	StatusCode      int
	ExecutedVersion string
	// FunctionError is the X-Amz-Function-Error value; "" renders null, as
	// after a reported partial failure.
	FunctionError string
}

// RecordAgeExceeded is a record older than MaximumRecordAgeInSeconds; its
// record has no response context.
type RecordAgeExceeded struct{}

func (RetryAttemptsExhausted) isFailCondition() {}
func (RecordAgeExceeded) isFailCondition()      {}

// FailureRecord renders b as kind's on-failure record: compact JSON in the
// documented key order (lambda-stream-sources.md 12.6 item 1, 5.6). The
// timestamp is UTC with milliseconds; a DynamoDB stream's arrival times are
// floored to the second, as ApproximateCreationDateTime is
// (dynamodb-streams.md 7.3). An SQS mapping has no on-failure record: nil.
func FailureRecord(kind EventSourceKind, b FailedBatch) []byte {
	switch kind.(type) {
	case SQS:
		return nil
	case DynamoDBStream:
		return ddbFailureRecord(b)
	case KinesisStream:
		// Not implemented yet: KinesisBatchInfo with millisecond arrival
		// times (lambda-stream-sources.md 5.6).
		return nil
	}
	return nil
}

// failureTimestamp is the on-failure record's timestamp layout; arrivalTime
// is a DynamoDB batch info's arrival layout.
const (
	failureTimestamp = "2006-01-02T15:04:05.000Z"
	arrivalTime      = "2006-01-02T15:04:05Z"
)

type failureRequestContext struct {
	RequestID              string `json:"requestId"`
	FunctionARN            string `json:"functionArn"`
	Condition              string `json:"condition"`
	ApproximateInvokeCount int    `json:"approximateInvokeCount"`
}

type failureResponseContext struct {
	StatusCode      int    `json:"statusCode"`
	ExecutedVersion string `json:"executedVersion"`
	// FunctionError is null after a reported partial failure (recorded
	// LS-D:L2265).
	FunctionError *string `json:"functionError"`
}

type ddbStreamBatchInfo struct {
	ShardID                         string `json:"shardId"`
	StartSequenceNumber             string `json:"startSequenceNumber"`
	EndSequenceNumber               string `json:"endSequenceNumber"`
	ApproximateArrivalOfFirstRecord string `json:"approximateArrivalOfFirstRecord"`
	ApproximateArrivalOfLastRecord  string `json:"approximateArrivalOfLastRecord"`
	BatchSize                       int    `json:"batchSize"`
	StreamARN                       string `json:"streamArn"`
}

type ddbFailure struct {
	RequestContext  failureRequestContext   `json:"requestContext"`
	ResponseContext *failureResponseContext `json:"responseContext,omitempty"`
	Version         string                  `json:"version"`
	Timestamp       string                  `json:"timestamp"`
	BatchInfo       ddbStreamBatchInfo      `json:"DDBStreamBatchInfo"`
}

func ddbFailureRecord(b FailedBatch) []byte {
	rec := ddbFailure{
		RequestContext: failureRequestContext{
			RequestID:              b.RequestID,
			FunctionARN:            b.FunctionARN,
			ApproximateInvokeCount: b.InvokeCount,
		},
		Version:   "1.0",
		Timestamp: b.Timestamp.Wire().UTC().Format(failureTimestamp),
		BatchInfo: ddbStreamBatchInfo{
			ShardID:                         b.Shard,
			StartSequenceNumber:             string(b.First.Seq),
			EndSequenceNumber:               string(b.Last.Seq),
			ApproximateArrivalOfFirstRecord: b.First.Arrival.Wire().UTC().Format(arrivalTime),
			ApproximateArrivalOfLastRecord:  b.Last.Arrival.Wire().UTC().Format(arrivalTime),
			BatchSize:                       b.Size,
			StreamARN:                       b.Stream.String(),
		},
	}
	switch c := b.Condition.(type) {
	case RetryAttemptsExhausted:
		rec.RequestContext.Condition = "RetryAttemptsExhausted"
		resp := failureResponseContext{StatusCode: c.StatusCode, ExecutedVersion: c.ExecutedVersion}
		if c.FunctionError != "" {
			resp.FunctionError = &c.FunctionError
		}
		rec.ResponseContext = &resp
	case RecordAgeExceeded:
		rec.RequestContext.Condition = "RecordAgeExceeded"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// The record holds only strings, ints and a *string, which always
	// encode; an error here is unreachable.
	if err := enc.Encode(rec); err != nil {
		return nil
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
