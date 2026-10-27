package dynamodb

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Streams is Amazon DynamoDB Streams, a second service on the signing name
// dynamodb (ADR 0019) served from the DynamoDB service's stream log
// (dynamodb-streams.md 11.1, ADR 0014 amendment of 2026-10-26). It holds
// no state of its own. It is not wired yet, and its operations answer 501:
// not implemented yet.
type Streams struct {
	svc *Service
}

// The Streams service is a Stateful with nothing to save.
var _ kernel.Stateful = (*Streams)(nil)

// NewStreams binds the four DynamoDB Streams operations, reading svc's log.
func NewStreams(d kernel.ServiceDeps, svc *Service) *Streams {
	s := &Streams{svc: svc}
	b := dynamodbstreamsapi.NewBinder(d.Binder)
	const note = "not implemented yet"
	b.ListStreams(streamsNotImplemented[dynamodbstreamsapi.ListStreamsInput, dynamodbstreamsapi.ListStreamsOutput], notes(note))
	b.DescribeStream(streamsNotImplemented[dynamodbstreamsapi.DescribeStreamInput, dynamodbstreamsapi.DescribeStreamOutput], notes(note))
	b.GetShardIterator(streamsNotImplemented[dynamodbstreamsapi.GetShardIteratorInput, dynamodbstreamsapi.GetShardIteratorOutput], notes(note))
	b.GetRecords(streamsNotImplemented[dynamodbstreamsapi.GetRecordsInput, dynamodbstreamsapi.GetRecordsOutput], notes(note))
	return s
}

// streamsNotImplemented answers every Streams operation until it is built.
//
//nolint:unparam // not implemented yet: no Streams operation answers anything but 501
func streamsNotImplemented[In, Out any](context.Context, *kernel.Call, *In) (*Out, *awserr.Error) {
	return nil, dynamodbstreamsapi.ErrNotImplemented("understudy does not serve DynamoDB Streams yet: not implemented yet")
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

// Switches lists the stream switches. Not implemented yet: none.
func (*Streams) Switches() []fidelity.Entry { return nil }
