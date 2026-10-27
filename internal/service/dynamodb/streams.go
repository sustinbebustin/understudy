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

// listStreamsNote is ListStreams' coverage note: the answers DynamoDB
// Local gives where AWS's are unrecorded (dynamodb-streams.md 6.1, 6.5;
// DDBS-03, DDBS-08), and that a deleted table's stream is never deleted.
const listStreamsNote = "a TableName naming no table returns an empty list, a disabled stream stays listed, and LastEvaluatedStreamArn is set only when more streams follow; " +
	"AWS's answers to all three, and the Limit text (DynamoDB Local's), are UNVERIFIED (dynamodb-streams.md 6.1, 6.5); " +
	"aws-differs: understudy: a deleted table's stream stays listed, where AWS deletes it 24 hours after its table (dynamodb-streams.md 5.4)"

// describeStreamNote is DescribeStream's coverage note (dynamodb-streams.md
// 6.5, 11.4).
const describeStreamNote = "aws-differs: understudy: a stream has one shard, which never splits or rolls over; AWS opens a shard per table partition and closes shards as it replaces them; " +
	"StreamStatus is ENABLED or DISABLED at once, never ENABLING or DISABLING; the Limit text is DynamoDB Local's and UNVERIFIED (dynamodb-streams.md 6.5)"

// getShardIteratorNote is GetShardIterator's coverage note: the texts
// DynamoDB Local gives (dynamodb-streams.md 6.3, 6.5; DDBS-02), and the
// retention the log does not apply yet (5.4, 5.5).
const getShardIteratorNote = "AT_TIMESTAMP is refused: the pinned model (api-models-aws 1cfdfb4) predates it; the validation and not-found texts are DynamoDB Local's; AWS's forms are UNVERIFIED (dynamodb-streams.md 6.5); " +
	"aws-differs: understudy: records are never trimmed, where AWS trims them 24 hours after they arrive, and a deleted table's stream stays readable, where AWS deletes it 24 hours after its table (dynamodb-streams.md 5.4, 5.5)"

// getRecordsNote is GetRecords' coverage note (dynamodb-streams.md 4.4,
// 5.3, 6.4, 7.3, 7.4, 7.6, 11.1 items 2-3).
const getRecordsNote = "aws-differs: understudy: more than two concurrent readers of a shard are never throttled; inject LimitExceededException with a fault rule; " +
	"when drains of DynamoDB Local's stream fail for several writes in a row, the records of all of them carry the first failed write's instant (dynamodb-streams.md 11.1 item 3); " +
	"the last page of a closed shard omits NextShardIterator, a Limit above 1000 answers ValidationException as DynamoDB Local does, ApproximateCreationDateTime is written as a plain epoch-seconds number, " +
	"a NextShardIterator expires 15 minutes after its own issue, SizeBytes is DynamoDB Local's (a byte above the documented item-size rule for some fractional numbers), a DeleteItem of an absent key emits no record, " +
	"and the malformed and expired iterator texts are DynamoDB Local's or understudy's; AWS's forms are UNVERIFIED (dynamodb-streams.md 4.4, 5.3, 6.4, 7.3, 7.4, 7.6)"

// NewStreams binds the DynamoDB Streams operations understudy serves,
// reading svc's log.
func NewStreams(d kernel.ServiceDeps, svc *Service) *Streams {
	s := &Streams{svc: svc}
	b := dynamodbstreamsapi.NewBinder(d.Binder)
	b.ListStreams(s.listStreams, notes(listStreamsNote))
	b.DescribeStream(s.describeStream, notes(describeStreamNote))
	b.GetShardIterator(s.getShardIterator, notes(getShardIteratorNote))
	b.GetRecords(s.getRecords, notes(getRecordsNote))
	return s
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

// Switches lists the stream switches in effect. Only
// dynamodb.streams.sequence-number is built; iterator-reuse is not yet, and
// an iterator reads again until it expires, its default.
func (s *Streams) Switches() []fidelity.Entry {
	return []fidelity.Entry{fidelity.Switch[string]{
		Name:    "dynamodb.streams.sequence-number",
		Value:   s.svc.streamSeq.String(),
		Label:   evidence.Inferred,
		Ref:     "docs/research/dynamodb-streams.md#43-sequence-numbers",
		Record:  "DDBS-09",
		Ops:     []string{"dynamodbstreams.DescribeStream", "dynamodbstreams.GetRecords"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: DDBS-09 is queued"},
	}.Entry()}
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
	all := s.svc.streams(scope)
	if in.TableName != nil {
		all = slices.DeleteFunc(all, func(st Stream) bool { return st.Table != *in.TableName })
	}
	type listed struct {
		Stream
		arn string
	}
	rows := make([]listed, len(all))
	for i, st := range all {
		rows[i] = listed{Stream: st, arn: st.arn(scope)}
	}
	slices.SortFunc(rows, func(a, b listed) int {
		return cmp.Or(b.Label.Compare(a.Label), cmp.Compare(a.arn, b.arn))
	})
	if in.ExclusiveStartStreamArn != nil {
		if i := slices.IndexFunc(rows, func(r listed) bool { return r.arn == *in.ExclusiveStartStreamArn }); i >= 0 {
			rows = rows[i+1:]
		}
	}
	out := &dynamodbstreamsapi.ListStreamsOutput{Streams: []dynamodbstreamsapi.Stream{}}
	for _, r := range rows[:min(limit, len(rows))] {
		out.Streams = append(out.Streams, dynamodbstreamsapi.Stream{StreamArn: new(r.arn), TableName: new(r.Table), StreamLabel: new(r.label())})
	}
	if len(rows) > limit {
		out.LastEvaluatedStreamArn = new(rows[limit-1].arn)
	}
	return out, nil
}
