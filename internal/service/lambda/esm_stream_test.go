package lambda_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// streamARN is a DynamoDB stream in the harness's account and region, in
// the documented form (dynamodb-streams.md 4.1).
const streamARN = "arn:aws:dynamodb:us-east-1:000000000000:table/Orders/stream/2000-01-01T00:00:00.000"

const streamShard = "shardId-00000000946684800000-1a2b3c4d"

// fakeStream stands in for the DynamoDB Streams service: it binds the four
// operations Lambda reads a stream through, keeps one shard of records and
// the principal of every call, and wakes shardlog.WaitKey on each append as
// the stream log does.
type fakeStream struct {
	k *kerneltest.K

	mu      sync.Mutex
	records []fakeRecord
	// trimmed is how many leading records are gone.
	trimmed int
	gone    bool
	calls   []string // "<op> <principal>"
}

type fakeRecord struct {
	seq     string
	arrival clock.Instant
	id      string
}

func newFakeStream(k *kerneltest.K) *fakeStream {
	s := &fakeStream{k: k}
	b := k.Kernel.Binder(dynamodbstreamsapi.Service())
	kernel.Bind(b, dynamodbstreamsapi.OpDescribeStream, s.describe, registry.Full{})
	kernel.Bind(b, dynamodbstreamsapi.OpGetShardIterator, s.iterator, registry.Full{})
	kernel.Bind(b, dynamodbstreamsapi.OpGetRecords, s.getRecords, registry.Full{})
	kernel.Bind(b, dynamodbstreamsapi.OpListStreams, func(context.Context, *kernel.Call, *dynamodbstreamsapi.ListStreamsInput) (*dynamodbstreamsapi.ListStreamsOutput, *awserr.Error) {
		return &dynamodbstreamsapi.ListStreamsOutput{Streams: []dynamodbstreamsapi.Stream{}}, nil
	}, registry.Full{})
	return s
}

func (s *fakeStream) note(op string, c *kernel.Call) {
	s.calls = append(s.calls, op+" "+identity.Render(c.Principal()))
}

// seqAt is the n-th record's number: 21 digits above 10^20.
func seqAt(n int) string {
	return new(big.Int).Add(new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil), big.NewInt(int64(n+1))).String()
}

// add appends records with ids at instant at and wakes the stream's readers.
func (s *fakeStream) add(at clock.Instant, ids ...string) {
	s.mu.Lock()
	for _, id := range ids {
		s.records = append(s.records, fakeRecord{seq: seqAt(len(s.records)), arrival: at, id: id})
	}
	s.mu.Unlock()
	s.k.Kernel.Waiters().Wake(shardlog.WaitKey(mustARN(s.k, streamARN)))
}

// trim drops the records that arrived at or before through.
func (s *fakeStream) trim(through clock.Instant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.trimmed < len(s.records) && s.records[s.trimmed].arrival.Compare(through) <= 0 {
		s.trimmed++
	}
}

func (s *fakeStream) setGone() {
	s.mu.Lock()
	s.gone = true
	s.mu.Unlock()
	s.k.Kernel.Waiters().Wake(shardlog.WaitKey(mustARN(s.k, streamARN)))
}

func (s *fakeStream) describe(_ context.Context, c *kernel.Call, in *dynamodbstreamsapi.DescribeStreamInput) (*dynamodbstreamsapi.DescribeStreamOutput, *awserr.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.note("DescribeStream", c)
	if in.StreamArn != streamARN || s.gone {
		return nil, dynamodbstreamsapi.ErrResourceNotFoundException("Requested resource not found: Stream: " + in.StreamArn + " not found")
	}
	status, view := dynamodbstreamsapi.StreamStatus("ENABLED"), dynamodbstreamsapi.StreamViewType("NEW_IMAGE")
	return &dynamodbstreamsapi.DescribeStreamOutput{StreamDescription: &dynamodbstreamsapi.StreamDescription{
		StreamArn: new(streamARN), StreamStatus: &status, StreamViewType: &view, TableName: new("Orders"),
		Shards: []dynamodbstreamsapi.Shard{{ShardId: new(streamShard), SequenceNumberRange: &dynamodbstreamsapi.SequenceNumberRange{StartingSequenceNumber: new(seqAt(-1))}}},
	}}, nil
}

func (s *fakeStream) iterator(_ context.Context, c *kernel.Call, in *dynamodbstreamsapi.GetShardIteratorInput) (*dynamodbstreamsapi.GetShardIteratorOutput, *awserr.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.note("GetShardIterator", c)
	if in.StreamArn != streamARN || s.gone {
		return nil, dynamodbstreamsapi.ErrResourceNotFoundException("Stream does not exist")
	}
	if in.ShardId != streamShard {
		return nil, dynamodbstreamsapi.ErrResourceNotFoundException("Shard does not exist")
	}
	idx := s.trimmed
	switch in.ShardIteratorType {
	case dynamodbstreamsapi.ShardIteratorTypeTrimHorizon:
	case dynamodbstreamsapi.ShardIteratorTypeLatest:
		idx = len(s.records)
	case dynamodbstreamsapi.ShardIteratorTypeAtSequenceNumber, dynamodbstreamsapi.ShardIteratorTypeAfterSequenceNumber:
		if in.SequenceNumber == nil {
			return nil, dynamodbstreamsapi.ErrValidationException("One or more parameter values were invalid: Invalid SequenceNumber")
		}
		want := shardlog.Seq(*in.SequenceNumber)
		idx = len(s.records)
		for i, r := range s.records {
			cmp := shardlog.Seq(r.seq).Compare(want)
			if cmp > 0 || (cmp == 0 && in.ShardIteratorType == "AT_SEQUENCE_NUMBER") {
				idx = i
				break
			}
		}
		// dynamodb-streams.md 6.3, 11.1 item 5: a number at or below the trim
		// point (AT_), or below it (AFTER_), is trimmed.
		if s.trimmed > 0 {
			cmp := want.Compare(shardlog.Seq(s.records[s.trimmed-1].seq))
			if cmp < 0 || (cmp == 0 && in.ShardIteratorType == dynamodbstreamsapi.ShardIteratorTypeAtSequenceNumber) {
				return nil, dynamodbstreamsapi.ErrTrimmedDataAccessException("The data you are trying to access has been trimmed.")
			}
		}
	}
	return &dynamodbstreamsapi.GetShardIteratorOutput{ShardIterator: new(streamARN + "|1|" + strconv.Itoa(idx))}, nil
}

func (s *fakeStream) getRecords(_ context.Context, c *kernel.Call, in *dynamodbstreamsapi.GetRecordsInput) (*dynamodbstreamsapi.GetRecordsOutput, *awserr.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.note("GetRecords", c)
	_, n, found := strings.Cut(in.ShardIterator, "|1|")
	idx, err := strconv.Atoi(n)
	if !found || err != nil {
		return nil, dynamodbstreamsapi.ErrValidationException("Invalid ShardIterator")
	}
	if s.gone {
		return nil, dynamodbstreamsapi.ErrResourceNotFoundException("Stream does not exist")
	}
	if idx < s.trimmed {
		return nil, dynamodbstreamsapi.ErrTrimmedDataAccessException("The data you are trying to access has been trimmed.")
	}
	limit := 1000
	if in.Limit != nil {
		limit = int(*in.Limit)
	}
	end := min(idx+limit, len(s.records))
	out := &dynamodbstreamsapi.GetRecordsOutput{Records: []dynamodbstreamsapi.Record{}, NextShardIterator: new(streamARN + "|1|" + strconv.Itoa(end))}
	for _, r := range s.records[idx:end] {
		out.Records = append(out.Records, r.wire())
	}
	return out, nil
}

// wire is the record as GetRecords answers it (dynamodb-streams.md 7.1,
// 7.3: the creation time floored to the second).
func (r fakeRecord) wire() dynamodbstreamsapi.Record {
	at := r.arrival.Wire().Truncate(time.Second)
	view, event := dynamodbstreamsapi.StreamViewType("NEW_IMAGE"), dynamodbstreamsapi.OperationType("INSERT")
	key := map[string]dynamodbstreamsapi.AttributeValue{"id": &dynamodbstreamsapi.AttributeValueMemberS{Value: r.id}}
	return dynamodbstreamsapi.Record{
		EventID: new(fmt.Sprintf("%032x", r.seq)[:32]), EventName: &event, EventVersion: new("1.1"), EventSource: new("aws:dynamodb"), AwsRegion: new("us-east-1"),
		Dynamodb: &dynamodbstreamsapi.StreamRecord{
			ApproximateCreationDateTime: &at, Keys: key, NewImage: key, SequenceNumber: new(r.seq), SizeBytes: new(int64(3 + len(r.id))), StreamViewType: &view,
		},
	}
}

// callers returns the principals of the calls of op.
func (s *fakeStream) callers(op string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if rest, ok := strings.CutPrefix(c, op+" "); ok {
			out = append(out, rest)
		}
	}
	return out
}

func mustARN(tb interface{ Fatal(...any) }, s string) aws.ARN {
	a, err := aws.ParseARN(s)
	if err != nil {
		tb.Fatal(err)
	}
	return a
}

// streamEvent is the event a stream mapping invokes a function with.
type streamEvent struct {
	Records []map[string]json.RawMessage
}

// streamRecorder keeps each invocation's event and answers through answer,
// or with a null response when answer is nil.
type streamRecorder struct {
	mu     sync.Mutex
	events []streamEvent
	answer func(ev streamEvent) runtimeapi.Outcome
}

func (r *streamRecorder) handle(_ context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
	var ev streamEvent
	if err := json.Unmarshal(inv.Payload, &ev); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.events = append(r.events, ev)
	answer := r.answer
	r.mu.Unlock()
	if answer == nil {
		return runtimeapi.Success{Payload: []byte("null")}, nil
	}
	return answer(ev), nil
}

func (r *streamRecorder) setAnswer(f func(ev streamEvent) runtimeapi.Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answer = f
}

// ids returns the item ids of each invocation's records.
func (r *streamRecorder) ids() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.events))
	for i, ev := range r.events {
		for _, rec := range ev.Records {
			var d struct{ Keys map[string]struct{ S string } }
			if err := json.Unmarshal(rec["dynamodb"], &d); err != nil {
				out[i] = append(out[i], "<undecodable: "+err.Error()+">")
				continue
			}
			out[i] = append(out[i], d.Keys["id"].S)
		}
	}
	return out
}

// first returns the first invocation's first record.
func (r *streamRecorder) first() map[string]json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[0].Records[0]
}

func (r *streamRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func seqOf(rec map[string]json.RawMessage) string {
	var d struct{ SequenceNumber string }
	if err := json.Unmarshal(rec["dynamodb"], &d); err != nil {
		return "<undecodable: " + err.Error() + ">"
	}
	return d.SequenceNumber
}

// published is what the stub SNS Publish received.
type published struct {
	mu       sync.Mutex
	messages []snsapi.PublishInput
}

// streamHarness is Lambda with its Execution, the real SQS service, an
// outbox delivering on-failure records, a stub SNS Publish and a fake
// stream.
type streamHarness struct {
	*execHarness
	stream *fakeStream
	rec    *streamRecorder
	topics *published
	queues *sqs.Service
}

// runStream runs fn with the stream poller's switches f and any further
// options; the pollers are started by fn (start), so a test can act before
// the first read.
func runStream(t *testing.T, f esm.StreamFidelity, fn func(h *streamHarness), opts ...execOptions) {
	t.Helper()
	box := outbox.New()
	kerneltest.Run(t, func(k *kerneltest.K) {
		if err := box.Bind(k.Kernel, outbox.Options{}); err != nil {
			k.Fatal(err)
		}
		queues := sqs.New(k.Deps(sqsapi.Service()), sqs.Options{})
		topics := &published{}
		kernel.Bind(k.Kernel.Binder(snsapi.Service()), snsapi.OpPublish, func(_ context.Context, _ *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
			topics.mu.Lock()
			defer topics.mu.Unlock()
			topics.messages = append(topics.messages, *in)
			return &snsapi.PublishOutput{MessageId: new("stub")}, nil
		}, registry.Full{})
		stream := newFakeStream(k)
		rec := &streamRecorder{}
		withExec(k, rec.handle, nil, func(h *execHarness) {
			fn(&streamHarness{execHarness: h, stream: stream, rec: rec, topics: topics, queues: queues})
		}, append([]execOptions{func(c *execConfig) { c.svc.Streams = f }}, opts...)...)
	}, kerneltest.Deliver(box), kerneltest.Services(lambdaapi.Service(), logsapi.Service(), sqsapi.Service(), snsapi.Service(), dynamodbstreamsapi.Service()))
}

func (h *streamHarness) start() {
	h.Helper()
	h.x.StartPollers()
	h.quiesce()
}

// mapStream creates the function worker and a mapping from the stream to
// it, TRIM_HORIZON unless in says otherwise.
func (h *streamHarness) mapStream(in lambdaapi.CreateEventSourceMappingInput) *lambdaapi.EventSourceMappingConfiguration {
	h.Helper()
	h.createFunction("worker")
	return call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(in))
}

func (h *streamHarness) streamInput(in lambdaapi.CreateEventSourceMappingInput) *lambdaapi.CreateEventSourceMappingInput {
	in.FunctionName, in.EventSourceArn = "worker", new(streamARN)
	if in.StartingPosition == nil {
		in.StartingPosition = new(lambdaapi.EventSourcePosition("TRIM_HORIZON"))
	}
	return &in
}

func (h *streamHarness) get(uuid string) *lambdaapi.EventSourceMappingConfiguration {
	h.Helper()
	return call(h.harness, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: uuid})
}

func derefAny[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// ---- control plane (lambda-stream-sources.md 3, 12.4) ----.

// A stream mapping created with only StartingPosition carries every stream
// field, defaulted, as recorded (third-party-recorded LS-D:L246-L271 and
// LS-K:L5-L29; lambda-stream-sources.md 3.5), and none of the fields 3.5
// lists as absent; Get after creation reads Enabled with the same fields.
// The recorded transient state needs lambda.stateTransitions = clock
// (ARCHITECTURE.md 9); under final, the default, Create answers Enabled.
func TestStreamMappingCreateShape(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		got := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		check := func(when string, m *lambdaapi.EventSourceMappingConfiguration, state string) {
			h.Helper()
			want := map[string]any{
				"BatchSize": int32(100), "BisectBatchOnFunctionError": false, "EventSourceArn": streamARN,
				"LastProcessingResult": "No records processed", "MaximumBatchingWindowInSeconds": int32(0),
				"MaximumRecordAgeInSeconds": int32(-1), "MaximumRetryAttempts": int32(-1), "ParallelizationFactor": int32(1),
				"StartingPosition": lambdaapi.EventSourcePosition("TRIM_HORIZON"), "State": state,
				"StateTransitionReason": "User action", "TumblingWindowInSeconds": int32(0),
			}
			have := map[string]any{
				"BatchSize": derefAny(m.BatchSize), "BisectBatchOnFunctionError": derefAny(m.BisectBatchOnFunctionError), "EventSourceArn": derefAny(m.EventSourceArn),
				"LastProcessingResult": derefAny(m.LastProcessingResult), "MaximumBatchingWindowInSeconds": derefAny(m.MaximumBatchingWindowInSeconds),
				"MaximumRecordAgeInSeconds": derefAny(m.MaximumRecordAgeInSeconds), "MaximumRetryAttempts": derefAny(m.MaximumRetryAttempts), "ParallelizationFactor": derefAny(m.ParallelizationFactor),
				"StartingPosition": derefAny(m.StartingPosition), "State": derefAny(m.State),
				"StateTransitionReason": derefAny(m.StateTransitionReason), "TumblingWindowInSeconds": derefAny(m.TumblingWindowInSeconds),
			}
			for k, v := range want {
				if have[k] != v {
					h.Errorf("%s: %s = %v, want %v", when, k, have[k], v)
				}
			}
			if m.DestinationConfig == nil || m.DestinationConfig.OnFailure == nil || m.DestinationConfig.OnFailure.Destination != nil || m.DestinationConfig.OnSuccess != nil {
				h.Errorf("%s: DestinationConfig = %+v, want {\"OnFailure\": {}}", when, m.DestinationConfig)
			}
			if m.FunctionResponseTypes == nil || len(m.FunctionResponseTypes) != 0 {
				h.Errorf("%s: FunctionResponseTypes = %#v, want []", when, m.FunctionResponseTypes)
			}
			if m.UUID == nil || m.FunctionArn == nil || m.EventSourceMappingArn == nil || m.LastModified == nil {
				h.Errorf("%s: UUID, FunctionArn, EventSourceMappingArn or LastModified missing: %+v", when, m)
			}
			if m.FilterCriteria != nil || m.ScalingConfig != nil || m.ProvisionedPollerConfig != nil || m.MetricsConfig != nil || m.KMSKeyArn != nil ||
				m.StartingPositionTimestamp != nil || m.Queues != nil || m.Topics != nil || m.SourceAccessConfigurations != nil || m.SelfManagedEventSource != nil {
				h.Errorf("%s: a member 3.5 lists as absent is present: %+v", when, m)
			}
		}
		check("Create", got, "Creating")
		h.advancePolled(settleDelay)
		g := h.get(*got.UUID)
		check("Get", g, "Enabled")
	}, transitionsClock)
}

// An SQS mapping renders exactly as before: no StartingPosition and no
// stream member, so Terraform SQS plans stay empty (lambda-events.md 1.4).
func TestSQSMappingRendersNoStreamField(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		_, arn := h.queue("orders", nil)
		h.createFunction("worker")
		m := call(h.harness, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{FunctionName: "worker", EventSourceArn: new(arn)})
		if m.StartingPosition != nil || m.ParallelizationFactor != nil || m.MaximumRetryAttempts != nil || m.MaximumRecordAgeInSeconds != nil ||
			m.BisectBatchOnFunctionError != nil || m.TumblingWindowInSeconds != nil || m.DestinationConfig != nil {
			h.Errorf("SQS mapping carries a stream member: %+v", m)
		}
	})
}

// An SQS mapping refuses each member of the stream type with AWS's text
// (aws-recorded, lambda-stream-sources.md 3.4: ESMS-03 01-07).
func TestSQSMappingRefusesStreamMembers(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		_, arn := h.queue("orders", nil)
		h.createFunction("worker")
		for _, tc := range []struct {
			in   lambdaapi.CreateEventSourceMappingInput
			text string
		}{
			{lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))}, "StartingPosition is not valid for SQS event sources."},
			{lambdaapi.CreateEventSourceMappingInput{ParallelizationFactor: new(int32(2))}, "Unsupported ParallelizationFactor parameter for given event source mapping type."},
			{lambdaapi.CreateEventSourceMappingInput{BisectBatchOnFunctionError: new(true)}, "Unsupported BisectBatchOnFunctionError parameter for given event source mapping type."},
			{lambdaapi.CreateEventSourceMappingInput{MaximumRetryAttempts: new(int32(2))}, "Unsupported MaximumRetryAttempts parameter for given event source mapping type."},
			{lambdaapi.CreateEventSourceMappingInput{MaximumRecordAgeInSeconds: new(int32(60))}, "Unsupported MaximumRecordAgeInSeconds parameter for given event source mapping type."},
			{lambdaapi.CreateEventSourceMappingInput{TumblingWindowInSeconds: new(int32(60))}, "Unsupported TumblingWindowInSeconds parameter for given event source mapping type."},
			{
				lambdaapi.CreateEventSourceMappingInput{DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(arn)}}},
				"Unsupported DestinationConfig parameter for given event source mapping type.",
			},
		} {
			tc.in.FunctionName, tc.in.EventSourceArn = "worker", new(arn)
			errorIs(h, fail(h.harness, lambdaapi.OpCreateEventSourceMapping, &tc.in), "InvalidParameterValueException", tc.text)
		}
	})
}

// An Update of an unknown UUID answers ResourceNotFoundException
// (aws-recorded, lambda-events.md 1.1), also when it carries a member of
// the stream type, which only a known SQS mapping refuses.
func TestUpdateOfAnUnknownMappingWithAStreamMemberIsNotFound(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		errorIs(h, fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID: "00000000-0000-4000-8000-000000000000", MaximumRetryAttempts: new(int32(1)),
		}), "ResourceNotFoundException", "The resource you requested does not exist.")
	})
}

// A FIFO queue is refused as a stream mapping's on-failure destination
// (aws-recorded, lambda-stream-sources.md 3.7: ESMS-05 04).
func TestStreamMappingRefusesAFIFOOnFailure(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.createFunction("worker")
		errorIs(h, fail(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{
			DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new("arn:aws:sqs:us-east-1:000000000000:failures.fifo")}},
		})), "InvalidParameterValueException", "FIFO-type Amazon SQS queues aren't supported as a destination.")
	})
}

// Rows 1 to 6 of lambda-stream-sources.md 3.3, byte for byte
// (third-party-recorded: LS-API:L12333-L12366 rows 1-3, LS-D:L4478-L4494
// row 4, LS-D:L557-L568 row 5, LS-D:L458-L490 row 6), StartingPosition
// checked before the stream lookup (12.4 item 3).
func TestStreamMappingRefusals(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.createFunction("worker")
		missing := "arn:aws:dynamodb:us-east-1:000000000000:table/Nope/stream/2000-01-01T00:00:00.000"
		create := func(in lambdaapi.CreateEventSourceMappingInput) *awserr.Error {
			h.Helper()
			in.FunctionName = "worker"
			if in.EventSourceArn == nil {
				in.EventSourceArn = new(streamARN)
			}
			return fail(h.harness, lambdaapi.OpCreateEventSourceMapping, &in)
		}
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{EventSourceArn: new(missing)}), "InvalidParameterValueException",
			"1 validation error detected: Value null at 'startingPosition' failed to satisfy constraint: Member must not be null.")
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("invalid"))}), "ValidationException",
			"1 validation error detected: Value 'invalid' at 'startingPosition' failed to satisfy constraint: Member must satisfy enum value set: [LATEST, AT_TIMESTAMP, TRIM_HORIZON]")
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("AT_TIMESTAMP"))}), "InvalidParameterValueException",
			"Unsupported starting position for arn type: "+streamARN)
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{EventSourceArn: new(missing), StartingPosition: new(lambdaapi.EventSourcePosition("TRIM_HORIZON"))}), "InvalidParameterValueException",
			"Stream not found: "+missing)
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{
			StartingPosition: new(lambdaapi.EventSourcePosition("LATEST")),
			FilterCriteria:   &lambdaapi.FilterCriteria{Filters: []lambdaapi.Filter{{Pattern: new("single-string")}}},
		}), "InvalidParameterValueException",
			"Invalid filter pattern definition.")
		first := call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{}))
		dup := create(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("TRIM_HORIZON"))})
		errorIs(h, dup, "ResourceConflictException", `The event source arn (" `+streamARN+` ") and function (" worker ") provided mapping already exists. Please update or delete the existing mapping with UUID `+*first.UUID)
		if dup.ID.Status != 409 {
			h.Errorf("duplicate status %d, want 409", dup.ID.Status)
		}
	})
}

// Settings understudy does not build answer 501 on a stream, never stored
// without effect (CLAUDE.md invariant 3): ParallelizationFactor
// above 1, BisectBatchOnFunctionError, TumblingWindowInSeconds above 0, a
// FilterCriteria pattern that compiles, an S3 on-failure destination, and a
// source of another service, whose text names what is supported.
func TestStreamMappingNotImplemented(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.createFunction("worker")
		for name, in := range map[string]lambdaapi.CreateEventSourceMappingInput{
			"ParallelizationFactor 2":    {ParallelizationFactor: new(int32(2))},
			"BisectBatchOnFunctionError": {BisectBatchOnFunctionError: new(true)},
			"TumblingWindowInSeconds 1":  {TumblingWindowInSeconds: new(int32(1))},
			"a filter that compiles":     {FilterCriteria: &lambdaapi.FilterCriteria{Filters: []lambdaapi.Filter{{Pattern: new(`{"eventName":["INSERT"]}`)}}}},
			"S3 on-failure":              {DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new("arn:aws:s3:::failures")}}},
		} {
			err := fail(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(in))
			if err.ID.Shape.Name() != "NotImplemented" || err.ID.Status != 501 {
				h.Errorf("%s: %s %d %q, want 501 NotImplemented", name, err.ID.Shape.Name(), err.ID.Status, err.Message)
			}
		}
		cluster := "arn:aws:kafka:us-east-1:000000000000:cluster/orders/0123abcd-0123-4567-89ab-0123456789ab-1"
		errorIs(h, fail(h.harness, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{FunctionName: "worker", EventSourceArn: new(cluster), StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))}),
			"NotImplemented", "understudy implements event source mappings for Amazon SQS queues, DynamoDB streams and Kinesis data streams only; "+cluster+" is none of them. Run 'understudy coverage --service lambda' for what is supported.")
	})
}

// MaximumRecordAgeInSeconds 0 to 59 is refused with an IPVE stating the
// rule ("values less than 60 and greater than -1 ... are not allowed",
// smithy member doc, lambda-stream-sources.md 2.2; the text is inferred,
// ESMS-01, a clause of the CreateEventSourceMapping note); OnSuccess
// answers the recorded SQS text, inferred to apply (12.4 item 9).
func TestStreamMappingValidation(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.createFunction("worker")
		errorIs(h, fail(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{MaximumRecordAgeInSeconds: new(int32(30))})),
			"InvalidParameterValueException", "MaximumRecordAgeInSeconds must be -1 or between 60 and 604800.")
		errorIs(h, fail(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{
			DestinationConfig: &lambdaapi.DestinationConfig{OnSuccess: &lambdaapi.OnSuccess{Destination: new("arn:aws:sqs:us-east-1:000000000000:done")}},
		})), "InvalidParameterValueException", "Unsupported DestinationConfig parameter for given event source mapping type.")
	})
}

// The on-failure destination is echoed as set, cleared by an empty
// Destination, and kept by an Update without DestinationConfig
// (lambda-stream-sources.md 3.7 aws-doc "supply an empty string"; 12.4 item
// 8; recorded echo LS-K:L1301-L1305); members of the SQS type are ignored
// and not echoed (12.4 item 9, inferred, ESMS-03); FunctionResponseTypes
// echoes ["ReportBatchItemFailures"] (LS-K:L1670-L1672); disabling answers
// Disabling with "User action" (LS-D:L173-L197), which needs
// lambda.stateTransitions = clock (ARCHITECTURE.md 9).
func TestStreamMappingUpdate(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		_, dlq := h.queue("dlq", nil)
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			DestinationConfig:     &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}},
			ScalingConfig:         &lambdaapi.ScalingConfig{MaximumConcurrency: new(int32(5))},
			FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"},
		})
		if d := m.DestinationConfig; d == nil || d.OnFailure == nil || derefAny(d.OnFailure.Destination) != dlq {
			h.Errorf("DestinationConfig = %+v, want OnFailure %s", d, dlq)
		}
		if m.ScalingConfig != nil {
			h.Errorf("ScalingConfig = %+v on a stream mapping, want none", m.ScalingConfig)
		}
		if !slices.Equal(m.FunctionResponseTypes, []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"}) {
			h.Errorf("FunctionResponseTypes = %v", m.FunctionResponseTypes)
		}
		h.advancePolled(settleDelay)
		up := call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, BatchSize: new(int32(10))})
		if d := up.DestinationConfig; d == nil || d.OnFailure == nil || derefAny(d.OnFailure.Destination) != dlq {
			h.Errorf("after an Update without DestinationConfig = %+v, want %s kept", d, dlq)
		}
		h.advancePolled(settleDelay)
		up = call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID:              *m.UUID,
			DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new("")}},
		})
		if d := up.DestinationConfig; d == nil || d.OnFailure == nil || d.OnFailure.Destination != nil {
			h.Errorf("after clearing = %+v, want {\"OnFailure\": {}}", d)
		}
		h.advancePolled(settleDelay)
		up = call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(false)})
		if derefAny(up.State) != "Disabling" || derefAny(up.StateTransitionReason) != "User action" {
			h.Errorf("disable = %v, %v; want Disabling, User action", derefAny(up.State), derefAny(up.StateTransitionReason))
		}
	}, transitionsClock)
}

// Create checks the stream through DescribeStream as the mapping's
// execution role (lambda-stream-sources.md 12.4 item 6), and the poller
// reads through GetShardIterator and GetRecords as that role too (12.5).
func TestStreamCallsRunAsTheExecutionRole(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.stream.add(h.Clock.Now(), "a")
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		const assumed = "arn:aws:sts::000000000000:assumed-role/lambda-role/"
		for _, op := range []string{"DescribeStream", "GetShardIterator", "GetRecords"} {
			callers := h.stream.callers(op)
			if len(callers) == 0 {
				h.Errorf("no %s call", op)
			}
			for _, c := range callers {
				if !strings.HasPrefix(c, assumed) {
					h.Errorf("%s called as %s, want the execution role", op, c)
				}
			}
		}
	})
}

// ---- poller (lambda-stream-sources.md 4, 5, 7, 12.5) ----.

// The thin slice: a record reaches the function as a one-record event in
// the Lambda form (dynamodb-streams.md 7.8: eventSourceARN added,
// eventVersion 1.1 recorded ESM-SNAP:L73, ApproximateCreationDateTime an
// epoch-seconds number with 10 integral digits ESM-TEST:L190), and
// LastProcessingResult becomes OK after it (LS-K:L3066, LS-D:L184).
func TestStreamRecordReachesTheFunction(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		h.advancePolled(1500 * time.Millisecond)
		at := h.Clock.Now()
		h.stream.add(at, "a")
		h.quiesce()
		if got := h.rec.ids(); !slices.EqualFunc(got, [][]string{{"a"}}, slices.Equal) {
			h.Fatalf("invocations %v, want one of record a", got)
		}
		rec := h.rec.first()
		str := func(k string) string {
			var s string
			if err := json.Unmarshal(rec[k], &s); err != nil {
				h.Errorf("record member %s = %s: %v", k, rec[k], err)
			}
			return s
		}
		if str("eventName") != "INSERT" || str("eventVersion") != "1.1" || str("eventSource") != "aws:dynamodb" || str("awsRegion") != "us-east-1" || str("eventSourceARN") != streamARN || str("eventID") == "" {
			h.Errorf("record = %s", fmt.Sprint(rec))
		}
		var d struct {
			ApproximateCreationDateTime json.Number
			SequenceNumber              string
			StreamViewType              string
		}
		if err := json.Unmarshal(rec["dynamodb"], &d); err != nil {
			h.Fatal(err)
		}
		if n, ok := new(big.Rat).SetString(d.ApproximateCreationDateTime.String()); !ok || n.Cmp(big.NewRat(at.Wire().Unix(), 1)) != 0 {
			h.Errorf("ApproximateCreationDateTime = %s, want %d", d.ApproximateCreationDateTime, at.Wire().Unix())
		}
		if d.SequenceNumber != seqAt(0) || d.StreamViewType != "NEW_IMAGE" {
			h.Errorf("dynamodb = %+v", d)
		}
		if _, has := rec["userIdentity"]; has {
			h.Errorf("an ordinary write's record carries userIdentity")
		}
		if got := derefAny(h.get(*m.UUID).LastProcessingResult); got != "OK" {
			h.Errorf("LastProcessingResult = %v, want OK", got)
		}
	})
}

// Under TRIM_HORIZON the records written before the mapping existed are
// delivered (recorded LS-K.py:L385-L399, lambda-stream-sources.md 3.2), in
// sequence order, batched up to BatchSize (4.2, 4.3).
func TestStreamTrimHorizonBatchesInOrder(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.stream.add(h.Clock.Now(), "a", "b", "c", "d", "e")
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(2))})
		h.start()
		if got, want := h.rec.ids(), [][]string{{"a", "b"}, {"c", "d"}, {"e"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
	})
}

// lambda.esm-stream.latest-anchor (inferred, ESMS-17): under
// create-instant, LATEST is resolved before Create answers, so a record
// written after Create and before the reader's first read is delivered and
// one written before Create is not; under first-read it is resolved when
// the reader first reads, so neither is.
func TestStreamLatestAnchorSwitch(t *testing.T) {
	for _, anchor := range []esm.LatestAnchor{esm.AnchorCreateInstant, esm.AnchorFirstRead} {
		t.Run(anchor.String(), func(t *testing.T) {
			runStream(t, esm.StreamFidelity{LatestAnchor: anchor}, func(h *streamHarness) {
				h.stream.add(h.Clock.Now(), "before")
				h.mapStream(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))})
				h.stream.add(h.Clock.Now(), "after")
				h.start()
				h.stream.add(h.Clock.Now(), "later")
				h.quiesce()
				var got []string
				for _, ids := range h.rec.ids() {
					got = append(got, ids...)
				}
				want := []string{"after", "later"}
				if anchor == esm.AnchorFirstRead {
					want = []string{"later"}
				}
				if !slices.Equal(got, want) {
					h.Errorf("delivered %v, want %v", got, want)
				}
			})
		})
	}
}

// lambda.esm-stream.retry-delay (inferred, ESMS-06): under poll-interval a
// failed batch is retried esm.DynamoDBRetryDelay of emulator time after the
// failed invocation ends (the documented polling rate, aws-doc
// lambda-stream-sources.md 4.1), with the same records (5.1, inferred
// retry-batch); under immediate it is retried at once.
func TestStreamRetryDelaySwitch(t *testing.T) {
	for _, delay := range []esm.RetryDelay{esm.RetryAfterPollInterval, esm.RetryImmediately} {
		t.Run(delay.String(), func(t *testing.T) {
			runStream(t, esm.StreamFidelity{RetryDelay: delay}, func(h *streamHarness) {
				failures := 1
				h.rec.setAnswer(func(streamEvent) runtimeapi.Outcome {
					if failures > 0 {
						failures--
						return runtimeapi.FunctionError{Type: "Error", Payload: []byte(`{"errorMessage":"boom"}`)}
					}
					return runtimeapi.Success{Payload: []byte("null")}
				})
				h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
				h.start()
				h.stream.add(h.Clock.Now(), "a")
				h.quiesce()
				if delay == esm.RetryImmediately {
					if n := h.rec.count(); n != 2 {
						h.Errorf("immediate: %d invocations, want the failure and its retry", n)
					}
					return
				}
				if n := h.rec.count(); n != 1 {
					h.Fatalf("%d invocations before the delay, want 1", n)
				}
				h.advancePolled(esm.DynamoDBRetryDelay - time.Nanosecond)
				if n := h.rec.count(); n != 1 {
					h.Errorf("%d invocations 1ns before the delay ends, want 1", n)
				}
				h.advancePolled(time.Nanosecond)
				if got, want := h.rec.ids(), [][]string{{"a"}, {"a"}}; !slices.EqualFunc(got, want, slices.Equal) {
					h.Errorf("invocations %v, want %v: the same record retried", got, want)
				}
			})
		})
	}
}

// failureBody is an on-failure record as delivered (lambda-stream-sources.md
// 5.6).
type failureBody struct {
	RequestContext struct {
		RequestID              string `json:"requestId"`
		FunctionArn            string `json:"functionArn"`
		Condition              string `json:"condition"`
		ApproximateInvokeCount int    `json:"approximateInvokeCount"`
	} `json:"requestContext"`
	ResponseContext *struct {
		StatusCode      int     `json:"statusCode"`
		ExecutedVersion string  `json:"executedVersion"`
		FunctionError   *string `json:"functionError"`
	} `json:"responseContext"`
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
	Info      struct {
		ShardID      string `json:"shardId"`
		Start        string `json:"startSequenceNumber"`
		End          string `json:"endSequenceNumber"`
		FirstArrival string `json:"approximateArrivalOfFirstRecord"`
		LastArrival  string `json:"approximateArrivalOfLastRecord"`
		BatchSize    int    `json:"batchSize"`
		StreamArn    string `json:"streamArn"`
	} `json:"DDBStreamBatchInfo"`
}

func (h *streamHarness) failures(url string) []failureBody {
	h.Helper()
	messages := h.receiveNow(url)
	out := make([]failureBody, 0, len(messages))
	for _, m := range messages {
		var b failureBody
		if err := json.Unmarshal([]byte(*m.Body), &b); err != nil {
			h.Fatalf("on-failure body %s: %v", *m.Body, err)
		}
		out = append(out, b)
	}
	return out
}

func alwaysFail(streamEvent) runtimeapi.Outcome {
	return runtimeapi.FunctionError{Type: "Error", Payload: []byte(`{"errorMessage":"boom"}`)}
}

// After MaximumRetryAttempts + 1 failed invocations the batch is discarded
// with one RetryAttemptsExhausted record to the SQS destination, its
// approximateInvokeCount the invocations made, functionError "Unhandled"
// after an exception, and the reader moves past the batch
// (third-party-recorded: LS-D:L304-L457 with MaximumRetryAttempts 1 giving
// approximateInvokeCount 2, LS-D:L427; lambda-stream-sources.md 5.2, 5.6).
func TestStreamRetryAttemptsExhaustedToSQS(t *testing.T) {
	runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
		url, dlq := h.queue("dlq", nil)
		h.rec.setAnswer(alwaysFail)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			MaximumRetryAttempts: new(int32(1)),
			DestinationConfig:    &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}},
		})
		h.start()
		at := h.Clock.Now()
		h.stream.add(at, "a", "b")
		h.quiesce()
		if n := h.rec.count(); n != 2 {
			h.Errorf("%d invocations, want MaximumRetryAttempts + 1 = 2", n)
		}
		got := h.failures(url)
		if len(got) != 1 {
			h.Fatalf("%d on-failure records, want 1", len(got))
		}
		f := got[0]
		if f.RequestContext.Condition != "RetryAttemptsExhausted" || f.RequestContext.ApproximateInvokeCount != 2 || f.Version != "1.0" ||
			f.ResponseContext == nil || f.ResponseContext.StatusCode != http.StatusOK || f.ResponseContext.ExecutedVersion != "$LATEST" ||
			f.ResponseContext.FunctionError == nil || *f.ResponseContext.FunctionError != "Unhandled" {
			h.Errorf("on-failure record %+v", f)
		}
		wantArrival := at.Wire().UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")
		if f.Info.ShardID != streamShard || f.Info.Start != seqAt(0) || f.Info.End != seqAt(1) || f.Info.BatchSize != 2 || f.Info.StreamArn != streamARN ||
			f.Info.FirstArrival != wantArrival || f.Info.LastArrival != wantArrival {
			h.Errorf("DDBStreamBatchInfo %+v", f.Info)
		}
		h.rec.setAnswer(nil)
		h.stream.add(h.Clock.Now(), "c")
		h.quiesce()
		if ids := h.rec.ids(); !slices.Equal(ids[len(ids)-1], []string{"c"}) {
			h.Errorf("after the discard the reader read %v, want [c]", ids[len(ids)-1])
		}
	})
}

// An SNS destination receives the record as the message, with no subject
// (lambda-stream-sources.md 5.6: no Subject, recorded LS-D:L2099-L2133).
func TestStreamOnFailureToSNS(t *testing.T) {
	runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
		topic := "arn:aws:sns:us-east-1:000000000000:failures"
		h.rec.setAnswer(alwaysFail)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			MaximumRetryAttempts: new(int32(0)),
			DestinationConfig:    &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(topic)}},
		})
		h.start()
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		h.topics.mu.Lock()
		defer h.topics.mu.Unlock()
		if len(h.topics.messages) != 1 {
			h.Fatalf("%d publishes, want 1", len(h.topics.messages))
		}
		p := h.topics.messages[0]
		var b failureBody
		if derefAny(p.TopicArn) != topic || p.Subject != nil || len(p.MessageAttributes) != 0 || json.Unmarshal([]byte(p.Message), &b) != nil ||
			b.RequestContext.Condition != "RetryAttemptsExhausted" || b.RequestContext.ApproximateInvokeCount != 1 {
			h.Errorf("publish %+v", p)
		}
	})
}

// The ReportBatchItemFailures matrix, one record per batch, as recorded for
// DynamoDB with MaximumRetryAttempts 3 (third-party-recorded
// LS-D:L2715-L4308; lambda-stream-sources.md 5.5): the success responses
// invoke once, the malformed ones fail the whole batch to exhaustion with
// functionError null, and an exception with "Unhandled".
func TestStreamReportBatchItemFailuresMatrix(t *testing.T) {
	unhandled := "Unhandled"
	for _, c := range []struct {
		name     string
		response string // "" for an exception
		success  bool
		fnError  *string
	}{
		{"empty list", `[]`, true, nil},
		{"null", `null`, true, nil},
		{"empty object", `{}`, true, nil},
		{"empty batchItemFailures", `{"batchItemFailures":[]}`, true, nil},
		{"null batchItemFailures", `{"batchItemFailures":null}`, true, nil},
		{"empty itemIdentifier", `{"batchItemFailures":[{"itemIdentifier":""}]}`, false, nil},
		{"null itemIdentifier", `{"batchItemFailures":[{"itemIdentifier":null}]}`, false, nil},
		{"bad key", `{"batchItemFailures":[{"foo":123}]}`, false, nil},
		{"bad key null", `{"batchItemFailures":[{"foo":null}]}`, false, nil},
		{"unknown identifier", `{"batchItemFailures":[{"itemIdentifier":123}]}`, false, nil},
		{"exception", "", false, &unhandled},
	} {
		t.Run(c.name, func(t *testing.T) {
			runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
				url, dlq := h.queue("dlq", nil)
				h.rec.setAnswer(func(streamEvent) runtimeapi.Outcome {
					if c.response == "" {
						return alwaysFail(streamEvent{})
					}
					return runtimeapi.Success{Payload: []byte(c.response)}
				})
				h.mapStream(lambdaapi.CreateEventSourceMappingInput{
					BatchSize: new(int32(1)), MaximumRetryAttempts: new(int32(3)),
					FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"},
					DestinationConfig:     &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}},
				})
				h.start()
				h.stream.add(h.Clock.Now(), "a")
				h.quiesce()
				got := h.failures(url)
				if c.success {
					if n := h.rec.count(); n != 1 || len(got) != 0 {
						h.Errorf("%d invocations, %d on-failure records; want 1 and none", n, len(got))
					}
					return
				}
				if n := h.rec.count(); n != 4 || len(got) != 1 {
					h.Fatalf("%d invocations, %d on-failure records; want 4 and 1", n, len(got))
				}
				f := got[0]
				if f.RequestContext.ApproximateInvokeCount != 4 || f.ResponseContext == nil || derefAny(f.ResponseContext.FunctionError) != derefAny(c.fnError) {
					h.Errorf("on-failure record %+v, want count 4 and functionError %v", f, derefAny(c.fnError))
				}
			})
		})
	}
}

// A partial failure checkpoints just before the reported record and
// resends from it alone: with BatchSize 3, MaximumRetryAttempts 3 and the
// sixth of six records always reported, records 1 to 5 are delivered once
// and the destination gets batchSize 1 with the sixth record's number as
// start and end, approximateInvokeCount 4 and functionError null
// (third-party-recorded LS-D:L2135-L2564; lambda-stream-sources.md 5.5).
// lambda.esm-stream.partial-resend-budget (inferred, ESMS-08) decides the
// sixth record's deliveries: restart gives the resend its own four (five in
// all, as the recording's logs show), shared continues the batch's count
// (four in all).
func TestStreamPartialFailureResendBudgetSwitch(t *testing.T) {
	for _, budget := range []esm.ResendBudget{esm.ResendRestart, esm.ResendShared} {
		t.Run(budget.String(), func(t *testing.T) {
			runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately, ResendBudget: budget}, func(h *streamHarness) {
				url, dlq := h.queue("dlq", nil)
				h.stream.add(h.Clock.Now(), "1", "2", "3", "4", "5", "6")
				sixth := seqAt(5)
				h.rec.setAnswer(func(ev streamEvent) runtimeapi.Outcome {
					for _, r := range ev.Records {
						if seqOf(r) == sixth {
							return runtimeapi.Success{Payload: []byte(`{"batchItemFailures":[{"itemIdentifier":"` + sixth + `"}]}`)}
						}
					}
					return runtimeapi.Success{Payload: []byte("null")}
				})
				h.mapStream(lambdaapi.CreateEventSourceMappingInput{
					BatchSize: new(int32(3)), MaximumRetryAttempts: new(int32(3)),
					FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"},
					DestinationConfig:     &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}},
				})
				h.start()
				counts := map[string]int{}
				for _, ids := range h.rec.ids() {
					for _, id := range ids {
						counts[id]++
					}
				}
				for _, id := range []string{"1", "2", "3", "4", "5"} {
					if counts[id] != 1 {
						h.Errorf("record %s delivered %d times, want once", id, counts[id])
					}
				}
				want := 5
				if budget == esm.ResendShared {
					want = 4
				}
				if counts["6"] != want {
					h.Errorf("record 6 delivered %d times, want %d", counts["6"], want)
				}
				got := h.failures(url)
				if len(got) != 1 {
					h.Fatalf("%d on-failure records, want 1", len(got))
				}
				f := got[0]
				if f.Info.BatchSize != 1 || f.Info.Start != sixth || f.Info.End != sixth || f.RequestContext.ApproximateInvokeCount != 4 ||
					f.ResponseContext == nil || f.ResponseContext.FunctionError != nil {
					h.Errorf("on-failure record %+v", f)
				}
			})
		})
	}
}

// A record older than MaximumRecordAgeInSeconds when read is discarded
// without invocation, with its own RecordAgeExceeded record (batchSize 1,
// approximateInvokeCount 1, no responseContext), and the fresh records are
// invoked (third-party-recorded LS-K:L3338-L3451 and LS-K.py:L1259-L1336;
// lambda-stream-sources.md 5.3; age measured from the record's arrival,
// inferred, ESMS-09).
func TestStreamRecordAgeBeforeInvocation(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		url, dlq := h.queue("dlq", nil)
		old := h.Clock.Now()
		h.stream.add(old, "old")
		h.advancePolled(80 * time.Second)
		h.stream.add(h.Clock.Now(), "f1", "f2", "f3", "f4", "f5")
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			BatchSize: new(int32(10)), MaximumRetryAttempts: new(int32(0)), MaximumRecordAgeInSeconds: new(int32(60)),
			DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}},
		})
		h.start()
		if got, want := h.rec.ids(), [][]string{{"f1", "f2", "f3", "f4", "f5"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
		got := h.failures(url)
		if len(got) != 1 {
			h.Fatalf("%d on-failure records, want 1", len(got))
		}
		f := got[0]
		if f.RequestContext.Condition != "RecordAgeExceeded" || f.RequestContext.ApproximateInvokeCount != 1 || f.ResponseContext != nil ||
			f.Info.BatchSize != 1 || f.Info.Start != seqAt(0) || f.Info.End != seqAt(0) {
			h.Errorf("on-failure record %+v", f)
		}
	})
}

// Records written while a mapping is disabled are delivered after it is
// re-enabled, from the checkpoint, none twice ("Lambda keeps track of the
// last record processed and resumes processing from that point when the
// mapping is reenabled", aws-doc ddb-create, lambda-stream-sources.md 7.1).
func TestStreamReEnableResumesAtTheCheckpoint(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(false)})
		h.quiesce()
		h.stream.add(h.Clock.Now(), "b", "c")
		h.quiesce()
		if got := h.rec.ids(); !slices.EqualFunc(got, [][]string{{"a"}}, slices.Equal) {
			h.Fatalf("while disabled: invocations %v, want only [a]", got)
		}
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(true)})
		h.quiesce()
		if got, want := h.rec.ids(), [][]string{{"a"}, {"b", "c"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("after re-enable: invocations %v, want %v", got, want)
		}
	})
}

// stateChanges returns the StateChange events of resource as "from->to".
func stateChanges(h *streamHarness, resource string) []string {
	var out []string
	events := h.Journal.Query(journal.Filter{Kind: journal.KindStateChange}).Events
	for i := range events {
		if sc, ok := events[i].Detail.(journal.StateChange); ok && sc.Resource.String() == resource {
			out = append(out, string(sc.From)+"->"+string(sc.To))
		}
	}
	return out
}

// A stream that is gone stops its reader with a journal entry, and the
// mapping stays Enabled (recorded: the mapping stays Enabled after
// DeleteTable, LS-D:L272-L301; lambda-stream-sources.md 7.3, 12.4 item 12).
func TestStreamGoneStopsTheReader(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		h.stream.setGone()
		h.quiesce()
		if got := stateChanges(h, *m.EventSourceMappingArn); !slices.Contains(got, "Polling->StreamGone") {
			h.Errorf("mapping state changes %v, want Polling->StreamGone", got)
		}
		if got := derefAny(h.get(*m.UUID).State); got != "Enabled" {
			h.Errorf("State = %v, want Enabled", got)
		}
	})
}

// streamRetention is DynamoDB Streams' 24 hours (dynamodb-streams.md 5.5,
// aws-doc).
const streamRetention = 24 * time.Hour

// A checkpoint that falls behind the trim horizon (here: records trimmed
// at retention plus 1 ms while the mapping was disabled) drops the lost
// records without an on-failure record, journals it, and resumes at
// TRIM_HORIZON (aws-doc 5.1 "discards the records and continues", 5.3;
// dropping without an on-failure record is inferred, UNVERIFIED, a clause of
// the CreateEventSourceMapping note).
func TestStreamRetentionResumesAtTrimHorizon(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		url, dlq := h.queue("dlq", nil)
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}},
		})
		h.start()
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(false)})
		h.quiesce()
		lost := h.Clock.Now()
		h.stream.add(lost, "lost")
		h.advancePolled(time.Hour)
		h.stream.add(h.Clock.Now(), "kept")
		h.advancePolled(streamRetention - time.Hour + time.Millisecond)
		h.stream.trim(h.Clock.Now().Add(-streamRetention))
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(true)})
		h.quiesce()
		if got, want := h.rec.ids(), [][]string{{"a"}, {"kept"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
		if got := h.failures(url); len(got) != 0 {
			h.Errorf("%d on-failure records for trimmed records, want none", len(got))
		}
		changes := stateChanges(h, *m.EventSourceMappingArn)
		if !slices.Contains(changes, "Polling->RecordsTrimmed") || !slices.Contains(changes, "RecordsTrimmed->Polling") {
			h.Errorf("mapping state changes %v, want Polling->RecordsTrimmed and back", changes)
		}
	})
}

// Each checkpoint move is journaled on the mapping, from TRIM_HORIZON to
// AFTER <seq> (lambda-stream-sources.md 12.5 item 10).
func TestStreamCheckpointIsJournaled(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		if got := stateChanges(h, *m.EventSourceMappingArn); !slices.Contains(got, "TRIM_HORIZON->AFTER "+seqAt(0)) {
			h.Errorf("mapping state changes %v, want TRIM_HORIZON->AFTER %s", got, seqAt(0))
		}
	})
}

// A save and load keeps the checkpoint: the mapping resumes after the last
// record it processed (lambda-stream-sources.md 12.3 item 4).
func TestStreamCheckpointSurvivesSnapshot(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		s := h.snapshot(h.queues)
		h.load(h.queues, s)
		h.quiesce()
		h.stream.add(h.Clock.Now(), "b")
		h.quiesce()
		if got, want := h.rec.ids(), [][]string{{"a"}, {"b"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v: resumed after a", got, want)
		}
	})
}
