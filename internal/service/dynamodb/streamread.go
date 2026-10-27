package dynamodb

import (
	"context"
	"errors"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/shardlog"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The read side of the stream log: DescribeStream, GetShardIterator and
// GetRecords (dynamodb-streams.md 11.4; contract 5.4, 5.5).

// describeStreamMax is DescribeStream's largest Limit ("The upper limit is
// 100", dynamodb-streams.md 6.2).
const describeStreamMax = 100

// iteratorTTL is a shard iterator's lifetime: "A shard iterator expires 15
// minutes after it is returned to the requester" (dynamodb-streams.md 4.4,
// aws-doc), each NextShardIterator from its own issue (inferred, DDBS-04).
const iteratorTTL = 15 * time.Minute

// getRecordsMax is GetRecords' largest Limit and its default
// (dynamodb-streams.md 6.4).
const getRecordsMax = 1000

// pageBytes is the most record data one GetRecords page holds: "up to 1 MB
// of data", counted in SizeBytes (dynamodb-streams.md 6.4, aws-doc).
const pageBytes = 1 << 20

// Texts of the read errors. The not-found and validation texts are
// DynamoDB Local's, observed (dynamodb-streams.md 6.5; AWS's are
// UNVERIFIED, DDBS-02); the expired text is the model's documentation of
// ExpiredIteratorException (3.3; inferred, DDBS-04).
const (
	textSequenceNotAllowed = "One or more parameter values were invalid: SequenceNumber should not be passed for this iterator type"
	textInvalidSequence    = "One or more parameter values were invalid: Invalid SequenceNumber"
	textStreamMissing      = "Stream does not exist"
	textShardMissing       = "Shard does not exist"
	textInvalidIterator    = "Invalid ShardIterator"
	textExpiredIterator    = "The provided iterator exceeds the maximum age allowed."
	textTrimmed            = "The data you are trying to access has been trimmed."
)

// streamByARN is the stream of scope whose ARN is arn.
func (st *State) streamByARN(scope aws.Scope, arn string) (Stream, bool) {
	for _, s := range st.Streams {
		if s.arn(scope) == arn {
			return s, true
		}
	}
	return Stream{}, false
}

// stream finds the stream named arn in scope.
func (s *Service) stream(scope aws.Scope, arn string) (Stream, bool) {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return Stream{}, false
	}
	type found struct {
		st Stream
		ok bool
	}
	f := store.Read(cell, func(st *State) found {
		x, ok := st.streamByARN(scope, arn)
		return found{x, ok}
	})
	return f.st, f.ok
}

func (s *Streams) describeStream(ctx context.Context, c *kernel.Call, in *dynamodbstreamsapi.DescribeStreamInput) (*dynamodbstreamsapi.DescribeStreamOutput, *awserr.Error) {
	if err := s.svc.serving(ctx, c); err != nil {
		return nil, err
	}
	if in.Limit != nil && *in.Limit > describeStreamMax {
		// DynamoDB Local's text, observed (dynamodb-streams.md 6.5; DDBS-02).
		return nil, dynamodbstreamsapi.ErrValidationException("One or more parameter values were invalid: Limit cannot be greater than the maximum: 100")
	}
	scope := c.Scope()
	cell, ok := s.svc.cells.Lookup(scope)
	if !ok {
		return nil, errStreamNotFound(in.StreamArn)
	}
	type described struct {
		out *dynamodbstreamsapi.StreamDescription
		ok  bool
	}
	d := store.Read(cell, func(st *State) described {
		x, ok := st.streamByARN(scope, in.StreamArn)
		if !ok {
			return described{}
		}
		return described{out: x.description(scope, in), ok: true}
	})
	if !d.ok {
		return nil, errStreamNotFound(in.StreamArn)
	}
	return &dynamodbstreamsapi.DescribeStreamOutput{StreamDescription: d.out}, nil
}

// errStreamNotFound is DescribeStream's answer for a stream that does not
// exist: the recorded text (third-party-recorded, STREAMS-SNAP:L90-L104;
// dynamodb-streams.md 6.2).
func errStreamNotFound(arn string) *awserr.Error {
	return dynamodbstreamsapi.ErrResourceNotFoundException("Requested resource not found: Stream: " + arn + " not found")
}

// description is s's StreamDescription for in, read under the cell's lock.
// The one shard is listed unless ExclusiveStartShardId names it or the
// filter asks for its children, which it never has; an
// ExclusiveStartShardId naming no shard lists every shard (DynamoDB Local's
// answers, observed, dynamodb-streams.md 5.6).
func (s Stream) description(scope aws.Scope, in *dynamodbstreamsapi.DescribeStreamInput) *dynamodbstreamsapi.StreamDescription {
	status := dynamodbstreamsapi.StreamStatusEnabled
	if !s.Enabled {
		status = dynamodbstreamsapi.StreamStatusDisabled
	}
	view := dynamodbstreamsapi.StreamViewType(s.ViewType)
	created := s.Label.Wire()
	shard := s.log.shard
	r := &dynamodbstreamsapi.SequenceNumberRange{}
	switch rg := shard.Range().(type) {
	case shardlog.Open:
		r.StartingSequenceNumber = new(string(rg.Start))
	case shardlog.Closed:
		r.StartingSequenceNumber, r.EndingSequenceNumber = new(string(rg.Start)), new(string(rg.End))
	}
	id := shard.Info().ID
	shards := []dynamodbstreamsapi.Shard{{ShardId: new(id), SequenceNumberRange: r}}
	children := in.ShardFilter != nil && in.ShardFilter.Type != nil && *in.ShardFilter.Type == dynamodbstreamsapi.ShardFilterTypeChildShards
	if children || (in.ExclusiveStartShardId != nil && *in.ExclusiveStartShardId == id) {
		shards = []dynamodbstreamsapi.Shard{}
	}
	return &dynamodbstreamsapi.StreamDescription{
		StreamArn: new(s.arn(scope)), StreamLabel: new(s.label()), StreamStatus: &status, StreamViewType: &view,
		CreationRequestDateTime: &created, TableName: new(s.Table), KeySchema: s.log.keys, Shards: shards,
	}
}

// position is the shard position GetShardIterator's type and number name,
// checked in DynamoDB Local's order (dynamodb-streams.md 6.3, 6.5).
func position(in *dynamodbstreamsapi.GetShardIteratorInput) (shardlog.Position, *awserr.Error) {
	switch in.ShardIteratorType {
	case dynamodbstreamsapi.ShardIteratorTypeTrimHorizon, dynamodbstreamsapi.ShardIteratorTypeLatest:
		if in.SequenceNumber != nil {
			return nil, dynamodbstreamsapi.ErrValidationException(textSequenceNotAllowed)
		}
		if in.ShardIteratorType == dynamodbstreamsapi.ShardIteratorTypeLatest {
			return shardlog.Latest{}, nil
		}
		return shardlog.TrimHorizon{}, nil
	case dynamodbstreamsapi.ShardIteratorTypeAtSequenceNumber, dynamodbstreamsapi.ShardIteratorTypeAfterSequenceNumber:
	}
	if in.SequenceNumber == nil {
		return nil, dynamodbstreamsapi.ErrValidationException(textInvalidSequence)
	}
	seq, err := shardlog.ParseSeq(*in.SequenceNumber)
	if err != nil {
		return nil, dynamodbstreamsapi.ErrValidationException(textInvalidSequence)
	}
	if in.ShardIteratorType == dynamodbstreamsapi.ShardIteratorTypeAtSequenceNumber {
		return shardlog.At{Seq: seq}, nil
	}
	return shardlog.After{Seq: seq}, nil
}

func (s *Streams) getShardIterator(ctx context.Context, c *kernel.Call, in *dynamodbstreamsapi.GetShardIteratorInput) (*dynamodbstreamsapi.GetShardIteratorOutput, *awserr.Error) {
	if err := s.svc.serving(ctx, c); err != nil {
		return nil, err
	}
	pos, aerr := position(in)
	if aerr != nil {
		return nil, aerr
	}
	scope := c.Scope()
	if _, ok := s.svc.stream(scope, in.StreamArn); !ok {
		return nil, dynamodbstreamsapi.ErrResourceNotFoundException(textStreamMissing)
	}
	return kernel.Write(c, s.svc.cells, scope, func(st *State, now clock.Instant, _ *kernel.Tx) (*dynamodbstreamsapi.GetShardIteratorOutput, *awserr.Error) {
		x, ok := st.streamByARN(scope, in.StreamArn)
		if !ok {
			return nil, dynamodbstreamsapi.ErrResourceNotFoundException(textStreamMissing)
		}
		shard := x.log.shard
		if in.ShardId != shard.Info().ID {
			return nil, dynamodbstreamsapi.ErrResourceNotFoundException(textShardMissing)
		}
		pos = shard.Resolve(pos)
		if _, _, err := shard.Read(pos, 1, 1, recordSize); err != nil {
			return nil, readError(err)
		}
		arn, err := aws.ParseARN(in.StreamArn)
		if err != nil {
			return nil, awserr.Internalf(err)
		}
		token := st.codec(s.svc).Encode(shardlog.Iterator{Stream: arn, Created: x.Label, Shard: shard.Info().ID, Pos: pos, Issued: now})
		return &dynamodbstreamsapi.GetShardIteratorOutput{ShardIterator: new(string(token))}, nil
	})
}

// codec is the scope's iterator codec, its key drawn on first use from the
// seeded id streams (contract 3).
func (st *State) codec(s *Service) shardlog.Codec {
	if st.iteratorKey == nil {
		st.iteratorKey = s.kernel.IDs().StreamIteratorKey()
	}
	return shardlog.NewCodec(st.iteratorKey, shardlog.ARNPrefixed{})
}

func recordSize(r streamRecord) int { return int(r.SizeBytes) }

// readError is a shard read's refusal as GetShardIterator and GetRecords
// answer it.
func readError(err error) *awserr.Error {
	if _, ok := errors.AsType[*shardlog.TrimmedError](err); ok {
		return dynamodbstreamsapi.ErrTrimmedDataAccessException(textTrimmed)
	}
	return awserr.Internalf(err)
}

func (s *Streams) getRecords(ctx context.Context, c *kernel.Call, in *dynamodbstreamsapi.GetRecordsInput) (*dynamodbstreamsapi.GetRecordsOutput, *awserr.Error) {
	if err := s.svc.serving(ctx, c); err != nil {
		return nil, err
	}
	limit := getRecordsMax
	if in.Limit != nil {
		if *in.Limit > getRecordsMax {
			// DynamoDB Local's text, observed (dynamodb-streams.md 6.5; DDBS-02).
			return nil, dynamodbstreamsapi.ErrValidationException("One or more parameter values were invalid: Limit should be less than or equal to 1000")
		}
		limit = int(*in.Limit)
	}
	scope := c.Scope()
	if _, ok := s.svc.cells.Lookup(scope); !ok {
		// No iterator was ever issued in the scope.
		return nil, dynamodbstreamsapi.ErrValidationException(textInvalidIterator)
	}
	// A write that changes nothing, for the commit instant: an iterator's
	// age is judged on the clock as it reads, as GetShardIterator issues
	// it on the clock as it writes.
	return kernel.Write(c, s.svc.cells, scope, func(st *State, now clock.Instant, tx *kernel.Tx) (*dynamodbstreamsapi.GetRecordsOutput, *awserr.Error) {
		tx.Unchanged()
		return st.records(scope, in.ShardIterator, limit, now)
	})
}

// records reads the page token names, as of now: up to limit records and
// pageBytes of SizeBytes, the first record always, and a NextShardIterator
// unless the page exhausts a closed shard (dynamodb-streams.md 6.4).
func (st *State) records(scope aws.Scope, token string, limit int, now clock.Instant) (*dynamodbstreamsapi.GetRecordsOutput, *awserr.Error) {
	if st.iteratorKey == nil {
		return nil, dynamodbstreamsapi.ErrValidationException(textInvalidIterator)
	}
	codec := shardlog.NewCodec(st.iteratorKey, shardlog.ARNPrefixed{})
	it, err := codec.Decode([]byte(token), now, iteratorTTL)
	if err != nil {
		if _, expired := errors.AsType[*shardlog.ExpiredError](err); expired {
			return nil, dynamodbstreamsapi.ErrExpiredIteratorException(textExpiredIterator)
		}
		return nil, dynamodbstreamsapi.ErrValidationException(textInvalidIterator)
	}
	x, ok := st.streamByARN(scope, it.Stream.String())
	if !ok || x.Label.Compare(it.Created) != 0 || x.log.shard.Info().ID != it.Shard {
		return nil, dynamodbstreamsapi.ErrResourceNotFoundException(textStreamMissing)
	}
	shard := x.log.shard
	entries, next, err := shard.Read(it.Pos, limit, pageBytes, recordSize)
	if err != nil {
		return nil, readError(err)
	}
	out := &dynamodbstreamsapi.GetRecordsOutput{Records: make([]dynamodbstreamsapi.Record, 0, len(entries))}
	for _, e := range entries {
		r, err := x.record(scope, e)
		if err != nil {
			return nil, awserr.Internalf(err)
		}
		out.Records = append(out.Records, r)
	}
	if !shard.Exhausted(next) {
		it.Pos, it.Issued = next, now
		out.NextShardIterator = new(string(codec.Encode(it)))
	}
	return out, nil
}

// record is e as GetRecords answers it (dynamodb-streams.md 7.1): the
// images Local wrote, the stream's region and view type, the arrival
// floored to the second (7.3), eventVersion 1.1 (third-party-recorded,
// DDB-SNAP:L761).
func (s Stream) record(scope aws.Scope, e shardlog.Entry[streamRecord]) (dynamodbstreamsapi.Record, error) {
	v := e.Value
	d := &dynamodbstreamsapi.StreamRecord{SequenceNumber: new(string(e.Seq)), SizeBytes: new(v.SizeBytes)}
	at := e.Arrival.Wire().UTC().Truncate(time.Second)
	view := dynamodbstreamsapi.StreamViewType(s.ViewType)
	d.ApproximateCreationDateTime, d.StreamViewType = &at, &view
	var err error
	if d.Keys, err = v.Keys.typed(); err != nil {
		return dynamodbstreamsapi.Record{}, err
	}
	if v.NewImage != nil {
		if d.NewImage, err = v.NewImage.typed(); err != nil {
			return dynamodbstreamsapi.Record{}, err
		}
	}
	if v.OldImage != nil {
		if d.OldImage, err = v.OldImage.typed(); err != nil {
			return dynamodbstreamsapi.Record{}, err
		}
	}
	name := dynamodbstreamsapi.OperationType(v.EventName)
	r := dynamodbstreamsapi.Record{
		EventID: new(v.EventID), EventName: &name, EventVersion: new("1.1"), EventSource: new("aws:dynamodb"),
		AwsRegion: new(scope.Region.String()), Dynamodb: d,
	}
	if v.TTL {
		// A TTL delete's userIdentity (dynamodb-streams.md 7.5, aws-doc;
		// GetRecords casing per the model).
		r.UserIdentity = &dynamodbstreamsapi.Identity{PrincipalId: new("dynamodb.amazonaws.com"), Type: new("Service")}
	}
	return r, nil
}
