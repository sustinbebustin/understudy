package lambda

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// dynamoSource reads a DynamoDB stream for the poller through the DynamoDB
// Streams operations, as the mapping's execution role
// (lambda-stream-sources.md 12.5).
type dynamoSource struct {
	k      *kernel.Kernel
	parent *kernel.Call
	target kernel.Target
	stream aws.ARN
}

var _ esm.StreamSource = dynamoSource{}

// Retention is a DynamoDB stream's fixed 24 hours (dynamodb-streams.md
// 5.5).
func (dynamoSource) Retention(context.Context) (time.Duration, error) {
	return esm.DynamoDBRetention, nil
}

// Shards lists the stream's shard ids through DescribeStream.
func (d dynamoSource) Shards(ctx context.Context) ([]string, error) {
	out, err := kernel.Invoke(ctx, d.k, d.parent, dynamodbstreamsapi.OpDescribeStream, d.target, &dynamodbstreamsapi.DescribeStreamInput{StreamArn: d.stream.String()})
	if err != nil {
		return nil, d.readError("DescribeStream", err, nil, "")
	}
	var ids []string
	if out.StreamDescription != nil {
		for _, sh := range out.StreamDescription.Shards {
			if sh.ShardId != nil {
				ids = append(ids, *sh.ShardId)
			}
		}
	}
	return ids, nil
}

// iteratorType is the GetShardIterator type and number of a position.
func iteratorType(p shardlog.Position) (dynamodbstreamsapi.ShardIteratorType, *string, error) {
	switch p := p.(type) {
	case shardlog.TrimHorizon:
		return dynamodbstreamsapi.ShardIteratorTypeTrimHorizon, nil, nil
	case shardlog.Latest:
		return dynamodbstreamsapi.ShardIteratorTypeLatest, nil, nil
	case shardlog.At:
		return dynamodbstreamsapi.ShardIteratorTypeAtSequenceNumber, new(string(p.Seq)), nil
	case shardlog.After:
		return dynamodbstreamsapi.ShardIteratorTypeAfterSequenceNumber, new(string(p.Seq)), nil
	case shardlog.AtTime:
	}
	return "", nil, fmt.Errorf("a DynamoDB stream cannot be read from %#v: the pinned model has no AT_TIMESTAMP iterator", p)
}

// Read takes an iterator at from and returns one GetRecords page of up to
// limit records.
func (d dynamoSource) Read(ctx context.Context, shard string, from shardlog.Position, limit int) (esm.StreamPage, error) {
	typ, seq, perr := iteratorType(from)
	if perr != nil {
		return esm.StreamPage{}, perr
	}
	it, err := kernel.Invoke(ctx, d.k, d.parent, dynamodbstreamsapi.OpGetShardIterator, d.target, &dynamodbstreamsapi.GetShardIteratorInput{
		StreamArn: d.stream.String(), ShardId: shard, ShardIteratorType: typ, SequenceNumber: seq,
	})
	if err != nil {
		return esm.StreamPage{}, d.readError("GetShardIterator", err, from, shard)
	}
	if it.ShardIterator == nil {
		return esm.StreamPage{}, fmt.Errorf("GetShardIterator of %s shard %s answered no iterator", d.stream, shard)
	}
	out, err := kernel.Invoke(ctx, d.k, d.parent, dynamodbstreamsapi.OpGetRecords, d.target, &dynamodbstreamsapi.GetRecordsInput{
		ShardIterator: *it.ShardIterator, Limit: new(int32(min(limit, getRecordsLimit))), //nolint:gosec // bounded by getRecordsLimit
	})
	if err != nil {
		return esm.StreamPage{}, d.readError("GetRecords", err, from, shard)
	}
	page := esm.StreamPage{Next: from, Closed: out.NextShardIterator == nil}
	for i := range out.Records {
		r, cerr := lambdaRecord(&out.Records[i], d.stream)
		if cerr != nil {
			return esm.StreamPage{}, cerr
		}
		page.Records = append(page.Records, r)
		page.Next = shardlog.After{Seq: r.Seq}
	}
	return page, nil
}

// getRecordsLimit is GetRecords' largest Limit (dynamodb-streams.md 6.4).
const getRecordsLimit = 1000

// readError is a Streams refusal of op as a StreamSource reports it: a
// missing stream or shard is *esm.GoneError, a trimmed position
// *shardlog.TrimmedError, a read iam.enforce refused the execution role
// *deniedReadError, anything else the error itself.
func (d dynamoSource) readError(op string, err *awserr.Error, from shardlog.Position, shard string) error {
	if _, denied := err.Origin.(awserr.Denial); denied {
		return &deniedReadError{Op: op, Err: err}
	}
	switch err.ID {
	case dynamodbstreamsapi.IdentityResourceNotFoundException():
		return &esm.GoneError{Stream: d.stream, Reason: err.Message}
	case dynamodbstreamsapi.IdentityTrimmedDataAccessException():
		return &shardlog.TrimmedError{Shard: shard, From: from}
	}
	return err.AsError()
}

// deniedReadError is a Streams read iam.enforce refused the mapping's
// execution role: Op is the operation, such as "GetRecords", and Err the
// denial it answered.
type deniedReadError struct {
	Op  string
	Err *awserr.Error
}

func (e *deniedReadError) Error() string {
	return "the execution role may not call " + e.Op + " on the stream: " + e.Err.Message
}

// lambdaRecord is r as a stream mapping's function receives it
// (dynamodb-streams.md 7.8): GetRecords' record with
// eventSourceARN added, ApproximateCreationDateTime as whole epoch
// seconds with a fraction, and userIdentity in Lambda's casing. Its Arrival is the
// creation time as answered, which for a mapping's read is the stored
// arrival, not floored.
func lambdaRecord(r *dynamodbstreamsapi.Record, stream aws.ARN) (esm.StreamRecord, error) {
	d := r.Dynamodb
	if d == nil || d.SequenceNumber == nil {
		return esm.StreamRecord{}, fmt.Errorf("a GetRecords record of %s has no sequence number", stream)
	}
	seq, err := shardlog.ParseSeq(*d.SequenceNumber)
	if err != nil {
		return esm.StreamRecord{}, err
	}
	ev := lambdaStreamRecord{
		EventID: smithy.Deref(r.EventID), EventVersion: smithy.Deref(r.EventVersion), EventSource: smithy.Deref(r.EventSource),
		AWSRegion: smithy.Deref(r.AwsRegion), EventSourceARN: stream.String(),
		Dynamodb: lambdaStreamData{SequenceNumber: string(seq), SizeBytes: smithy.Deref(d.SizeBytes)},
	}
	if r.EventName != nil {
		ev.EventName = string(*r.EventName)
	}
	if d.StreamViewType != nil {
		ev.Dynamodb.StreamViewType = string(*d.StreamViewType)
	}
	var arrival clock.Instant
	if d.ApproximateCreationDateTime != nil {
		arrival = clock.FromWire(*d.ApproximateCreationDateTime)
		ev.Dynamodb.ApproximateCreationDateTime = epochSecondsFloat(d.ApproximateCreationDateTime.Unix())
	}
	if ev.Dynamodb.Keys, err = attributeMap(d.Keys); err != nil {
		return esm.StreamRecord{}, err
	}
	if d.NewImage != nil {
		if ev.Dynamodb.NewImage, err = attributeMap(d.NewImage); err != nil {
			return esm.StreamRecord{}, err
		}
	}
	if d.OldImage != nil {
		if ev.Dynamodb.OldImage, err = attributeMap(d.OldImage); err != nil {
			return esm.StreamRecord{}, err
		}
	}
	if u := r.UserIdentity; u != nil {
		ev.UserIdentity = &lambdaIdentity{Type: smithy.Deref(u.Type), PrincipalID: smithy.Deref(u.PrincipalId)}
	}
	event, err := json.Marshal(ev)
	if err != nil {
		return esm.StreamRecord{}, err
	}
	return esm.StreamRecord{Seq: seq, Arrival: arrival, Event: event}, nil
}

// lambdaStreamRecord is a DynamoDB stream record in the event a function
// receives, its members in the documented order (dynamodb-streams.md 7.8).
type lambdaStreamRecord struct {
	EventID        string           `json:"eventID"`
	EventName      string           `json:"eventName"`
	EventVersion   string           `json:"eventVersion"`
	EventSource    string           `json:"eventSource"`
	AWSRegion      string           `json:"awsRegion"`
	Dynamodb       lambdaStreamData `json:"dynamodb"`
	EventSourceARN string           `json:"eventSourceARN"`
	UserIdentity   *lambdaIdentity  `json:"userIdentity,omitempty"`
}

// epochSecondsFloat is whole epoch seconds that marshals as `<seconds>.0`.
type epochSecondsFloat int64

// MarshalJSON writes the seconds with a `.0` fraction.
func (s epochSecondsFloat) MarshalJSON() ([]byte, error) {
	return append(strconv.AppendInt(nil, int64(s), 10), ".0"...), nil
}

// lambdaStreamData is a record's dynamodb member in the event.
type lambdaStreamData struct {
	// ApproximateCreationDateTime is whole epoch seconds written with a
	// fraction, so a Python handler reads a float (recorded ESMS-17
	// 09-ddb.json: 1793725107.0; the third-party ESM-TEST:L190 records 10
	// integral digits). The float type is observed; the `.0` spelling,
	// rather than an exponent form, is inferred.
	ApproximateCreationDateTime epochSecondsFloat `json:"ApproximateCreationDateTime"`
	Keys                        map[string]any    `json:"Keys"`
	NewImage                    map[string]any    `json:"NewImage,omitempty"`
	OldImage                    map[string]any    `json:"OldImage,omitempty"`
	SequenceNumber              string            `json:"SequenceNumber"`
	SizeBytes                   int64             `json:"SizeBytes"`
	StreamViewType              string            `json:"StreamViewType"`
}

// lambdaIdentity is a TTL delete's userIdentity in the event's casing
// (dynamodb-streams.md 7.5, 7.8).
type lambdaIdentity struct {
	Type        string `json:"type"`
	PrincipalID string `json:"principalId"`
}

// attributeMap is m in DynamoDB JSON.
func attributeMap(m map[string]dynamodbstreamsapi.AttributeValue) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for k, v := range m {
		j, err := attributeJSON(v)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", k, err)
		}
		out[k] = j
	}
	return out, nil
}

// attributeJSON is v in DynamoDB JSON: {"<type>": <value>}. A variant
// given by pointer, as an in-process caller may build it, reads as its
// value.
func attributeJSON(v dynamodbstreamsapi.AttributeValue) (any, error) {
	if d, ok := derefAttribute(v); ok {
		v = d
	}
	switch x := v.(type) {
	case dynamodbstreamsapi.AttributeValueMemberS:
		return map[string]any{"S": x.Value}, nil
	case dynamodbstreamsapi.AttributeValueMemberN:
		return map[string]any{"N": x.Value}, nil
	case dynamodbstreamsapi.AttributeValueMemberB:
		return map[string]any{"B": x.Value}, nil
	case dynamodbstreamsapi.AttributeValueMemberSS:
		return map[string]any{"SS": x.Value}, nil
	case dynamodbstreamsapi.AttributeValueMemberNS:
		return map[string]any{"NS": x.Value}, nil
	case dynamodbstreamsapi.AttributeValueMemberBS:
		return map[string]any{"BS": x.Value}, nil
	case dynamodbstreamsapi.AttributeValueMemberNULL:
		return map[string]any{"NULL": x.Value}, nil
	case dynamodbstreamsapi.AttributeValueMemberBOOL:
		return map[string]any{"BOOL": x.Value}, nil
	case dynamodbstreamsapi.AttributeValueMemberM:
		m, err := attributeMap(x.Value)
		return map[string]any{"M": m}, err
	case dynamodbstreamsapi.AttributeValueMemberL:
		l := make([]any, len(x.Value))
		for i, e := range x.Value {
			j, err := attributeJSON(e)
			if err != nil {
				return nil, err
			}
			l[i] = j
		}
		return map[string]any{"L": l}, nil
	}
	return nil, fmt.Errorf("%T is not an attribute value", v)
}

// derefAttribute is the value of a variant given by a non-nil pointer.
func derefAttribute(v dynamodbstreamsapi.AttributeValue) (dynamodbstreamsapi.AttributeValue, bool) {
	switch x := v.(type) {
	case *dynamodbstreamsapi.AttributeValueMemberS:
		return derefNonNil(x)
	case *dynamodbstreamsapi.AttributeValueMemberN:
		return derefNonNil(x)
	case *dynamodbstreamsapi.AttributeValueMemberB:
		return derefNonNil(x)
	case *dynamodbstreamsapi.AttributeValueMemberSS:
		return derefNonNil(x)
	case *dynamodbstreamsapi.AttributeValueMemberNS:
		return derefNonNil(x)
	case *dynamodbstreamsapi.AttributeValueMemberBS:
		return derefNonNil(x)
	case *dynamodbstreamsapi.AttributeValueMemberNULL:
		return derefNonNil(x)
	case *dynamodbstreamsapi.AttributeValueMemberBOOL:
		return derefNonNil(x)
	case *dynamodbstreamsapi.AttributeValueMemberM:
		return derefNonNil(x)
	case *dynamodbstreamsapi.AttributeValueMemberL:
		return derefNonNil(x)
	}
	return nil, false
}

func derefNonNil[T dynamodbstreamsapi.AttributeValue](p *T) (dynamodbstreamsapi.AttributeValue, bool) {
	if p == nil {
		return nil, false
	}
	return *p, true
}
