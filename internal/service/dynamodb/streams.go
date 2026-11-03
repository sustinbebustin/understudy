package dynamodb

import (
	"cmp"
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/shardlog"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Streams is Amazon DynamoDB Streams, a second service on the signing name
// dynamodb (ADR 0019) served from the DynamoDB service's stream log
// (dynamodb-streams.md 11.1, ADR 0014 amendment of 2026-10-26). It holds
// no state of its own.
type Streams struct {
	svc *Service
}

// The Streams service is a Stateful with nothing to save.
var _ kernel.Stateful = (*Streams)(nil)

// constraintTexts is the clause of each Streams note whose operation takes
// a Limit or an enum: the generated Coral text quotes the value, AWS's
// omits it (aws-recorded, dynamodb-streams.md 6.5: DDBS-02).
const constraintTexts = "a constraint violation's text quotes the value (\"Value '0' at 'limit'\"), where AWS's omits it (\"Value at 'limit'\")"

// describeStreamNote is DescribeStream's coverage note, the differences
// DDBS-05 recorded (dynamodb-streams.md 5.2, 5.6, 6.5).
const describeStreamNote = "aws-differs: understudy: a stream has one shard, which never splits or rolls over; AWS opened a new on-demand table's stream with 4 shards and closes shards as it replaces them; " +
	"StreamStatus is ENABLED or DISABLED at once, where AWS reports ENABLING with no shards for about 7 seconds and DISABLING for about 8; " + constraintTexts +
	" (dynamodb-streams.md 5.2, 5.6, 6.5)"

// getShardIteratorNote is GetShardIterator's coverage note
// (dynamodb-streams.md 3.5, 6.5).
const getShardIteratorNote = "AT_TIMESTAMP is refused: the pinned model (api-models-aws 1cfdfb4) predates it, so the enum text lists the four other types where AWS's lists five; " +
	constraintTexts + " (dynamodb-streams.md 3.5, 6.5)"

// getRecordsNote is GetRecords' coverage note, the differences
// DDBS-04 and DDBS-10 recorded (dynamodb-streams.md 3.3, 5.5, 6.4, 7.3).
const getRecordsNote = "aws-differs: understudy: more than two concurrent readers of a shard are never throttled; inject LimitExceededException with a fault rule; " +
	"ApproximateCreationDateTime is written as a plain epoch-seconds number, where AWS writes the exponent form (1.793723928E9); " +
	"ExpiredIteratorException carries the __type namespace com.amazonaws.dynamodb.v20120810, where AWS's is com.amazonaws.dynamodb.streams.v20120810; " +
	constraintTexts + "; the trimmed-iterator text is UNVERIFIED (dynamodb-streams.md 3.3, 5.5, 6.5, 7.3)"

// NewStreams binds the DynamoDB Streams operations understudy serves,
// reading svc's log.
func NewStreams(d kernel.ServiceDeps, svc *Service) *Streams {
	s := &Streams{svc: svc}
	b := dynamodbstreamsapi.NewBinder(d.Binder)
	// Every ListStreams answer DDBS-03, DDBS-08 and DDBS-17 recorded is
	// AWS's (dynamodb-streams.md 6.1); its Limit and TableName constraint
	// texts are the generated ones (6.5).
	b.ListStreams(s.listStreams, notes(constraintTexts+" (dynamodb-streams.md 6.5)"))
	b.DescribeStream(s.describeStream, notes(describeStreamNote))
	b.GetShardIterator(s.getShardIterator, notes(getShardIteratorNote))
	b.GetRecords(s.getRecords, notes(getRecordsNote))
	declareStreamsAccess(d.Binder)
	return s
}

// declareStreamsAccess states what each Streams call needs from IAM: its
// own action and no other (dynamodb:ReadDataForReplication, also in the
// generated lists of DescribeStream, GetShardIterator and GetRecords,
// authorizes global-table replication, not a caller's read), on the stream
// it addresses; ListStreams addresses no stream, so it is asked on "*"
// (spec enforce matrix row 1). GetRecords names its stream only in its
// iterator's prefix, which is read before the handler verifies the token.
func declareStreamsAccess(b *kernel.Binder) {
	kernel.Declare(b, dynamodbstreamsapi.OpListStreams, kernel.WithoutSecondaryActions())
	kernel.Declare(b, dynamodbstreamsapi.OpDescribeStream, kernel.WithoutSecondaryActions(),
		kernel.WithResources(func(_ *kernel.Call, in *dynamodbstreamsapi.DescribeStreamInput) []aws.ARN {
			return streamResource(in.StreamArn)
		}))
	kernel.Declare(b, dynamodbstreamsapi.OpGetShardIterator, kernel.WithoutSecondaryActions(),
		kernel.WithResources(func(_ *kernel.Call, in *dynamodbstreamsapi.GetShardIteratorInput) []aws.ARN {
			return streamResource(in.StreamArn)
		}))
	kernel.Declare(b, dynamodbstreamsapi.OpGetRecords, kernel.WithoutSecondaryActions(),
		kernel.WithResources(func(_ *kernel.Call, in *dynamodbstreamsapi.GetRecordsInput) []aws.ARN {
			stream, ok := shardlog.ClaimedStream([]byte(in.ShardIterator))
			if !ok || stream.Service != "dynamodb" {
				return nil
			}
			return []aws.ARN{stream}
		}))
}

// streamResource is the stream a Streams input's StreamArn names, nothing
// when it is not a DynamoDB ARN: the handler then answers it as usual.
func streamResource(arn string) []aws.ARN {
	stream, err := aws.ParseARN(arn)
	if err != nil || stream.Service != "dynamodb" {
		return nil
	}
	return []aws.ARN{stream}
}

// Name returns "dynamodbstreams".
func (*Streams) Name() ref.ServiceID { return "dynamodbstreams" }

// SchemaVersion is 1: the service has no state, so its sections never
// change.
func (*Streams) SchemaVersion() int { return 1 }

// Export returns no section: the stream log is in DynamoDB's.
func (*Streams) Export() ([]store.Section, error) { return nil, nil }

// Import accepts the no sections Export writes.
func (*Streams) Import([]store.Section) error { return nil }

// Reset has nothing to drop.
func (*Streams) Reset(kernel.ResetScope) {}

// Rearm has no timers to arm: DynamoDB arms the log's.
func (*Streams) Rearm(clock.Instant) {}

// Resources lists nothing: DynamoDB lists the tables that hold streams.
func (*Streams) Resources(aws.Scope) []kernel.Resource { return nil }

// Scopes returns DynamoDB's scopes, where the streams are.
func (s *Streams) Scopes() []aws.Scope { return s.svc.Scopes() }

// Switches lists the stream switches in effect.
func (s *Streams) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		fidelity.Switch[string]{
			Name:    "dynamodb.streams.sequence-number",
			Value:   s.svc.streamSeq.String(),
			Label:   evidence.Inferred,
			Ref:     "docs/research/dynamodb-streams.md#43-sequence-numbers",
			Record:  "DDBS-09",
			Ops:     []string{"dynamodbstreams.DescribeStream", "dynamodbstreams.GetRecords"},
			Settles: fidelity.NoScenario{Reason: "recorded in dynamodb/DDBS-09: AWS numbers records with 23 digits above a 21-digit StartingSequenceNumber, which neither value does"},
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "dynamodb.streams.iterator-reuse",
			Value:   s.svc.iteratorReuse.String(),
			Label:   evidence.AWSRecorded,
			Ref:     "docs/research/dynamodb-streams.md#44-shard-iterators",
			Record:  "DDBS-04",
			Ops:     []string{"dynamodbstreams.GetRecords"},
			Settles: fidelity.NoScenario{Reason: "dynamodb/DDBS-04/02-reuse.json is recorded; the replayer runs without DynamoDB"},
		}.Entry(),
	}
}

// listStreamsMax is ListStreams' largest Limit and its default ("The upper
// limit is 100", dynamodb-streams.md 6.1).
const listStreamsMax = 100

// listStreams lists the streams of the caller's account and region from
// the log, newest label first and ties by ARN (the aws-doc example's
// order, dynamodb-streams.md 6.1): every stream, or those of the table
// TableName names exactly. A page ends after Limit streams;
// LastEvaluatedStreamArn names its last only when more follow, and
// ExclusiveStartStreamArn continues after it (an ARN not listed starts
// from the first).
func (s *Streams) listStreams(ctx context.Context, c *kernel.Call, in *dynamodbstreamsapi.ListStreamsInput) (*dynamodbstreamsapi.ListStreamsOutput, *awserr.Error) {
	if err := s.svc.serving(ctx, c); err != nil {
		return nil, err
	}
	limit := listStreamsMax
	if in.Limit != nil {
		if *in.Limit > listStreamsMax {
			// DynamoDB Local's text, observed (dynamodb-streams.md 6.1, 6.5; DDBS-02).
			return nil, dynamodbstreamsapi.ErrValidationException("One or more parameter values were invalid: Limit cannot be greater than the maximum allowed size: 100")
		}
		limit = int(*in.Limit)
	}
	scope := c.Scope()
	// The rows are values copied under the cell's lock: a Stream shares its
	// log with the state (ADR 0005).
	type listed struct {
		table, label, arn string
		at                clock.Instant
	}
	var rows []listed
	if _, ok := s.svc.cells.Lookup(scope); ok {
		var err *awserr.Error
		rows, err = streamsWrite(c, s.svc, scope, func(st *State, _ clock.Instant) ([]listed, bool, *awserr.Error) {
			var out []listed
			for _, l := range st.Streams {
				if in.TableName == nil || l.Table == *in.TableName {
					out = append(out, listed{table: l.Table, label: l.label(), arn: l.arn(scope), at: l.Label})
				}
			}
			return out, false, nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(rows, func(a, b listed) int {
		return cmp.Or(b.at.Compare(a.at), cmp.Compare(a.arn, b.arn))
	})
	if in.ExclusiveStartStreamArn != nil {
		if i := slices.IndexFunc(rows, func(r listed) bool { return r.arn == *in.ExclusiveStartStreamArn }); i >= 0 {
			rows = rows[i+1:]
		}
	}
	out := &dynamodbstreamsapi.ListStreamsOutput{Streams: []dynamodbstreamsapi.Stream{}}
	for _, r := range rows[:min(limit, len(rows))] {
		out.Streams = append(out.Streams, dynamodbstreamsapi.Stream{StreamArn: new(r.arn), TableName: new(r.table), StreamLabel: new(r.label)})
	}
	// A full page names its last stream, though none follows: AWS's last
	// page under Limit 1 does (aws-recorded, dynamodb-streams.md 6.1: DDBS-08).
	if len(rows) >= limit {
		out.LastEvaluatedStreamArn = new(rows[limit-1].arn)
	}
	return out, nil
}
