package lambda

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// kinesisSource reads a Kinesis data stream for the poller and the control
// plane through ListShards, GetShardIterator, GetRecords and
// DescribeStreamSummary, as the mapping's execution role (08b contract
// 6.2).
type kinesisSource struct {
	k      *kernel.Kernel
	parent *kernel.Call
	target kernel.Target
	stream aws.ARN
	// role is the execution role, each record's invokeIdentityArn.
	role aws.ARN
}

var _ esm.StreamSource = kinesisSource{}

// kinesisGetRecordsLimit is GetRecords' largest Limit (kinesis.md 5.6).
const kinesisGetRecordsLimit = 10000

// isKinesisStream reports whether a names a Kinesis data stream:
// arn:<partition>:kinesis:<region>:<account>:stream/<name>, the name
// without a further "/" (kinesis.md 3.1).
func isKinesisStream(a aws.ARN) bool {
	name, ok := strings.CutPrefix(a.Resource, "stream/")
	return a.Service == "kinesis" && ok && name != "" && !strings.Contains(name, "/")
}

// isKinesisConsumer reports whether a names a Kinesis stream consumer:
// stream/<name>/consumer/<consumer>:<timestamp> (kinesis.md 3.6).
func isKinesisConsumer(a aws.ARN) bool {
	rest, ok := strings.CutPrefix(a.Resource, "stream/")
	if a.Service != "kinesis" || !ok {
		return false
	}
	name, consumer, ok := strings.Cut(rest, "/consumer/")
	return ok && name != "" && consumer != ""
}

// Shards lists the stream's shard ids through ListShards, following
// NextToken; a token is sent without the stream, which ListShards
// refuses alongside one (kinesis.md 4.6).
func (s kinesisSource) Shards(ctx context.Context) ([]string, error) {
	in := &kinesisapi.ListShardsInput{StreamARN: new(s.stream.String())}
	var ids []string
	for {
		out, err := kernel.Invoke(ctx, s.k, s.parent, kinesisapi.OpListShards, s.target, in)
		if err != nil {
			return nil, s.readError("ListShards", err, nil, "")
		}
		for _, sh := range out.Shards {
			ids = append(ids, sh.ShardId)
		}
		if out.NextToken == nil {
			return ids, nil
		}
		in = &kinesisapi.ListShardsInput{NextToken: out.NextToken}
	}
}

// Retention is the stream's current RetentionPeriodHours, read through
// DescribeStreamSummary (08b contract 5).
func (s kinesisSource) Retention(ctx context.Context) (time.Duration, error) {
	out, err := kernel.Invoke(ctx, s.k, s.parent, kinesisapi.OpDescribeStreamSummary, s.target, &kinesisapi.DescribeStreamSummaryInput{StreamARN: new(s.stream.String())})
	if err != nil {
		return 0, s.readError("DescribeStreamSummary", err, nil, "")
	}
	return time.Duration(out.StreamDescriptionSummary.RetentionPeriodHours) * time.Hour, nil
}

// Read takes an iterator at from and returns one GetRecords page of up to
// limit records. A Kinesis shard never closes in slice 1, so a page is
// Closed only when GetRecords answers no NextShardIterator.
func (s kinesisSource) Read(ctx context.Context, shard string, from shardlog.Position, limit int) (esm.StreamPage, error) {
	in := &kinesisapi.GetShardIteratorInput{StreamARN: new(s.stream.String()), ShardId: shard}
	switch p := from.(type) {
	case shardlog.TrimHorizon:
		in.ShardIteratorType = kinesisapi.ShardIteratorTypeTrimHorizon
	case shardlog.Latest:
		in.ShardIteratorType = kinesisapi.ShardIteratorTypeLatest
	case shardlog.At:
		in.ShardIteratorType, in.StartingSequenceNumber = kinesisapi.ShardIteratorTypeAtSequenceNumber, new(string(p.Seq))
	case shardlog.After:
		in.ShardIteratorType, in.StartingSequenceNumber = kinesisapi.ShardIteratorTypeAfterSequenceNumber, new(string(p.Seq))
	case shardlog.AtTime:
		in.ShardIteratorType, in.Timestamp = kinesisapi.ShardIteratorTypeAtTimestamp, new(p.At.Wire())
	default:
		return esm.StreamPage{}, fmt.Errorf("a Kinesis stream cannot be read from %#v", from)
	}
	it, err := kernel.Invoke(ctx, s.k, s.parent, kinesisapi.OpGetShardIterator, s.target, in)
	if err != nil {
		return esm.StreamPage{}, s.readError("GetShardIterator", err, from, shard)
	}
	if it.ShardIterator == nil {
		return esm.StreamPage{}, fmt.Errorf("GetShardIterator of %s shard %s answered no iterator", s.stream, shard)
	}
	out, err := kernel.Invoke(ctx, s.k, s.parent, kinesisapi.OpGetRecords, s.target, &kinesisapi.GetRecordsInput{
		ShardIterator: *it.ShardIterator, Limit: new(int32(min(limit, kinesisGetRecordsLimit))), //nolint:gosec // bounded by kinesisGetRecordsLimit
	})
	if err != nil {
		return esm.StreamPage{}, s.readError("GetRecords", err, nil, shard)
	}
	page := esm.StreamPage{Next: from, Closed: out.NextShardIterator == nil}
	for i := range out.Records {
		r, cerr := s.record(&out.Records[i], shard)
		if cerr != nil {
			return esm.StreamPage{}, cerr
		}
		page.Records = append(page.Records, r)
		page.Next = shardlog.After{Seq: r.Seq}
	}
	return page, nil
}

// readError is a Kinesis refusal of op as a StreamSource reports it: a
// missing stream is *esm.GoneError; GetShardIterator's refusal of a
// sequence number the source read from the shard itself is
// *shardlog.TrimmedError, the only reason it refuses one (08b contract
// 6.2); a read iam.enforce refused the execution role *deniedReadError;
// anything else the error itself. from is nil for an operation that names
// no position.
func (s kinesisSource) readError(op string, err *awserr.Error, from shardlog.Position, shard string) error {
	if _, denied := err.Origin.(awserr.Denial); denied {
		return &deniedReadError{Op: op, Err: err}
	}
	switch err.ID {
	case kinesisapi.IdentityResourceNotFoundException():
		return &esm.GoneError{Stream: s.stream, Reason: err.Message}
	case kinesisapi.IdentityInvalidArgumentException():
		switch from.(type) {
		case shardlog.At, shardlog.After:
			return &shardlog.TrimmedError{Shard: shard, From: from}
		case shardlog.TrimHorizon, shardlog.Latest, shardlog.AtTime, nil:
		}
	}
	return err.AsError()
}

// record is r, read from shard, as a Kinesis mapping's function receives
// it (kinesis.md 8.1, 13.8 item 2; lambda-stream-sources.md 8.1). Its
// Arrival is the record's ApproximateArrivalTimestamp.
func (s kinesisSource) record(r *kinesisapi.Record, shard string) (esm.StreamRecord, error) {
	seq, err := shardlog.ParseSeq(r.SequenceNumber)
	if err != nil {
		return esm.StreamRecord{}, fmt.Errorf("a GetRecords record of %s: %w", s.stream, err)
	}
	if r.ApproximateArrivalTimestamp == nil {
		return esm.StreamRecord{}, fmt.Errorf("a GetRecords record of %s shard %s has no ApproximateArrivalTimestamp", s.stream, shard)
	}
	arrival := clock.FromWire(*r.ApproximateArrivalTimestamp)
	event, err := json.Marshal(kinesisEventRecord{
		Kinesis: kinesisEventData{
			SchemaVersion: "1.0", PartitionKey: deref(r.PartitionKey), SequenceNumber: string(seq),
			Data: r.Data, ApproximateArrivalTimestamp: epochMillis(*r.ApproximateArrivalTimestamp),
		},
		EventSource: "aws:kinesis", EventVersion: "1.0", EventID: shard + ":" + string(seq), EventName: "aws:kinesis:record",
		InvokeIdentityARN: s.role.String(), AWSRegion: s.stream.Region, EventSourceARN: s.stream.String(),
	})
	if err != nil {
		return esm.StreamRecord{}, err
	}
	return esm.StreamRecord{Seq: seq, Arrival: arrival, Event: event}, nil
}

// kinesisEventRecord is a Kinesis record in the event a function receives,
// its members in the documented order and without encryptionType (08b
// contract 6.2).
type kinesisEventRecord struct {
	Kinesis           kinesisEventData `json:"kinesis"`
	EventSource       string           `json:"eventSource"`
	EventVersion      string           `json:"eventVersion"`
	EventID           string           `json:"eventID"`
	EventName         string           `json:"eventName"`
	InvokeIdentityARN string           `json:"invokeIdentityArn"`
	AWSRegion         string           `json:"awsRegion"`
	EventSourceARN    string           `json:"eventSourceARN"`
}

// kinesisEventData is a record's kinesis member; Data encodes as base64.
type kinesisEventData struct {
	SchemaVersion               string      `json:"kinesisSchemaVersion"`
	PartitionKey                string      `json:"partitionKey"`
	SequenceNumber              string      `json:"sequenceNumber"`
	Data                        []byte      `json:"data"`
	ApproximateArrivalTimestamp json.Number `json:"approximateArrivalTimestamp"`
}

// epochMillis is t as epoch seconds with millisecond precision, written as
// the awsJson codec writes a timestamp: trailing zeros trimmed, no
// exponent (08b contract 6.2). A finer instant is truncated.
func epochMillis(t time.Time) json.Number {
	return json.Number(strconv.FormatFloat(float64(t.UnixMilli())/1000, 'f', -1, 64))
}
