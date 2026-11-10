package lambda_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// kinesisARN is a Kinesis stream in the harness's account and region, in
// the documented form (kinesis.md 3.1).
const kinesisARN = "arn:aws:kinesis:us-east-1:000000000000:stream/clicks"

// kinesisShard is a shard id in the documented form (kinesis.md 3.2).
const kinesisShard = "shardId-000000000000"

// fakeKinesis stands in for the Kinesis service: it binds the four
// operations Lambda reads a stream through (ListShards, GetShardIterator,
// GetRecords; DescribeStreamSummary for existence), keeps one shard of
// records and the principal of every call, and wakes shardlog.WaitKey on
// each put as the service does.
type fakeKinesis struct {
	k *kerneltest.K

	mu      sync.Mutex
	records []kinesisapi.Record
	calls   []string // "<op> <principal>"
}

func newFakeKinesis(k *kerneltest.K) *fakeKinesis {
	s := &fakeKinesis{k: k}
	b := k.Kernel.Binder(kinesisapi.Service())
	kernel.Bind(b, kinesisapi.OpDescribeStreamSummary, s.summary, registry.Full{})
	kernel.Bind(b, kinesisapi.OpListShards, s.listShards, registry.Full{})
	kernel.Bind(b, kinesisapi.OpGetShardIterator, s.iterator, registry.Full{})
	kernel.Bind(b, kinesisapi.OpGetRecords, s.getRecords, registry.Full{})
	return s
}

func (s *fakeKinesis) note(op string, c *kernel.Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, op+" "+identity.Render(c.Principal()))
}

func (s *fakeKinesis) callers(op string) []string {
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

// add appends a record and wakes the stream's readers.
func (s *fakeKinesis) add(seq, key string, data []byte, at time.Time) {
	s.mu.Lock()
	s.records = append(s.records, kinesisapi.Record{SequenceNumber: seq, PartitionKey: new(key), Data: data, ApproximateArrivalTimestamp: &at})
	s.mu.Unlock()
	s.k.Kernel.Waiters().Wake(shardlog.WaitKey(mustARN(s.k, kinesisARN)))
}

func notFoundKinesis() *awserr.Error {
	return kinesisapi.ErrResourceNotFoundException("Stream nope under account 000000000000 not found.")
}

func (s *fakeKinesis) known(name, arn *string) bool {
	return (arn != nil && *arn == kinesisARN) || (name != nil && *name == "clicks")
}

func (s *fakeKinesis) summary(_ context.Context, c *kernel.Call, in *kinesisapi.DescribeStreamSummaryInput) (*kinesisapi.DescribeStreamSummaryOutput, *awserr.Error) {
	s.note("DescribeStreamSummary", c)
	if !s.known(in.StreamName, in.StreamARN) {
		return nil, notFoundKinesis()
	}
	return &kinesisapi.DescribeStreamSummaryOutput{StreamDescriptionSummary: kinesisapi.StreamDescriptionSummary{
		StreamName: "clicks", StreamARN: kinesisARN, StreamStatus: "ACTIVE", RetentionPeriodHours: 24, OpenShardCount: 1,
		StreamCreationTimestamp: s.k.Clock.Now().Wire(), // required member of StreamDescriptionSummary (kinesis.md 4.4)
		EnhancedMonitoring:      []kinesisapi.EnhancedMetrics{{ShardLevelMetrics: []kinesisapi.MetricsName{}}},
	}}, nil
}

func (s *fakeKinesis) listShards(_ context.Context, c *kernel.Call, in *kinesisapi.ListShardsInput) (*kinesisapi.ListShardsOutput, *awserr.Error) {
	s.note("ListShards", c)
	if !s.known(in.StreamName, in.StreamARN) {
		return nil, notFoundKinesis()
	}
	return &kinesisapi.ListShardsOutput{Shards: []kinesisapi.Shard{{
		ShardId:             kinesisShard,
		HashKeyRange:        kinesisapi.HashKeyRange{StartingHashKey: "0", EndingHashKey: "340282366920938463463374607431768211455"},
		SequenceNumberRange: kinesisapi.SequenceNumberRange{StartingSequenceNumber: "1"},
	}}}, nil
}

func (s *fakeKinesis) iterator(_ context.Context, c *kernel.Call, in *kinesisapi.GetShardIteratorInput) (*kinesisapi.GetShardIteratorOutput, *awserr.Error) {
	s.note("GetShardIterator", c)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.known(in.StreamName, in.StreamARN) {
		return nil, notFoundKinesis()
	}
	idx := 0
	switch in.ShardIteratorType {
	case kinesisapi.ShardIteratorTypeTrimHorizon:
	case kinesisapi.ShardIteratorTypeLatest:
		idx = len(s.records)
	case kinesisapi.ShardIteratorTypeAtSequenceNumber, kinesisapi.ShardIteratorTypeAfterSequenceNumber:
		want := shardlog.Seq(deref(in.StartingSequenceNumber))
		idx = len(s.records)
		for i, r := range s.records {
			cmp := shardlog.Seq(r.SequenceNumber).Compare(want)
			if cmp > 0 || (cmp == 0 && in.ShardIteratorType == kinesisapi.ShardIteratorTypeAtSequenceNumber) {
				idx = i
				break
			}
		}
	case kinesisapi.ShardIteratorTypeAtTimestamp:
		return nil, kinesisapi.ErrInvalidArgumentException("the fake does not serve AT_TIMESTAMP")
	}
	return &kinesisapi.GetShardIteratorOutput{ShardIterator: new(strconv.Itoa(idx))}, nil
}

func (s *fakeKinesis) getRecords(_ context.Context, c *kernel.Call, in *kinesisapi.GetRecordsInput) (*kinesisapi.GetRecordsOutput, *awserr.Error) {
	s.note("GetRecords", c)
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := strconv.Atoi(in.ShardIterator)
	if err != nil || idx > len(s.records) {
		return nil, kinesisapi.ErrInvalidArgumentException("Invalid ShardIterator.")
	}
	end := len(s.records)
	if in.Limit != nil {
		end = min(end, idx+int(*in.Limit))
	}
	return &kinesisapi.GetRecordsOutput{Records: append([]kinesisapi.Record{}, s.records[idx:end]...), NextShardIterator: new(strconv.Itoa(end)), MillisBehindLatest: new(int64(0))}, nil
}

// rawRecorder keeps each invocation's records as the bytes Lambda sent.
type rawRecorder struct {
	mu      sync.Mutex
	records [][]json.RawMessage
	fail    bool
}

func (r *rawRecorder) handle(_ context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
	var ev struct{ Records []json.RawMessage }
	if err := json.Unmarshal(inv.Payload, &ev); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, ev.Records)
	if r.fail {
		return alwaysFail(streamEvent{}), nil
	}
	return runtimeapi.Success{Payload: []byte("null")}, nil
}

func (r *rawRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

type kinesisHarness struct {
	*execHarness
	stream *fakeKinesis
	rec    *rawRecorder
}

func runKinesis(t *testing.T, fn func(h *kinesisHarness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		stream := newFakeKinesis(k)
		rec := &rawRecorder{}
		withExec(k, rec.handle, nil, func(h *execHarness) {
			fn(&kinesisHarness{execHarness: h, stream: stream, rec: rec})
		})
	}, kerneltest.Services(lambdaapi.Service(), logsapi.Service(), kinesisapi.Service()))
}

func (h *kinesisHarness) kinesisInput(in lambdaapi.CreateEventSourceMappingInput) *lambdaapi.CreateEventSourceMappingInput {
	in.FunctionName = "worker"
	if in.EventSourceArn == nil {
		in.EventSourceArn = new(kinesisARN)
	}
	if in.StartingPosition == nil {
		in.StartingPosition = new(lambdaapi.EventSourcePosition("TRIM_HORIZON"))
	}
	return &in
}

// A Kinesis mapping created with only StartingPosition LATEST carries
// every stream field defaulted as recorded (third-party-recorded,
// LS-K:L5-L29, lambda-stream-sources.md 3.5), Enabled under
// lambda.stateTransitions final; existence is checked through
// DescribeStreamSummary as the execution role.
func TestKinesisMappingCreateShape(t *testing.T) {
	runKinesis(t, func(h *kinesisHarness) {
		h.createFunction("worker")
		m := call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.kinesisInput(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))}))
		want := map[string]any{
			"BatchSize": int32(100), "BisectBatchOnFunctionError": false, "EventSourceArn": kinesisARN,
			"LastProcessingResult": "No records processed", "MaximumBatchingWindowInSeconds": int32(0),
			"MaximumRecordAgeInSeconds": int32(-1), "MaximumRetryAttempts": int32(-1), "ParallelizationFactor": int32(1),
			"StartingPosition": lambdaapi.EventSourcePosition("LATEST"), "State": "Enabled",
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
				h.Errorf("%s = %v, want %v", k, have[k], v)
			}
		}
		if m.DestinationConfig == nil || m.DestinationConfig.OnFailure == nil || m.DestinationConfig.OnFailure.Destination != nil {
			h.Errorf("DestinationConfig = %+v, want {\"OnFailure\": {}}", m.DestinationConfig)
		}
		if m.FunctionResponseTypes == nil || len(m.FunctionResponseTypes) != 0 {
			h.Errorf("FunctionResponseTypes = %#v, want []", m.FunctionResponseTypes)
		}
		if m.FilterCriteria != nil || m.ScalingConfig != nil || m.StartingPositionTimestamp != nil || m.Queues != nil || m.Topics != nil {
			h.Errorf("a member 3.5 lists as absent is present: %+v", m)
		}
		described := h.stream.callers("DescribeStreamSummary")
		if len(described) == 0 || !strings.Contains(described[0], "role/lambda-role") {
			h.Errorf("DescribeStreamSummary callers = %v, want the execution role", described)
		}
		got := call(h.harness, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *m.UUID})
		if derefAny(got.EventSourceArn) != kinesisARN || derefAny(got.StartingPosition) != lambdaapi.EventSourcePosition("LATEST") {
			h.Errorf("Get = %+v", got)
		}
	})
}

// Kinesis mapping refusals: rows 1, 2, 4 and 5 of lambda-stream-sources.md
// 3.3 byte for byte (third-party-recorded: LS-API:L18250-L18270 rows 1-2;
// LS-K:L3160-L3175 row 4; LS-K:L1478-L1489 row 5), a second function on
// the stream allowed (LS-K:L1547-L1586); AT_TIMESTAMP and a consumer ARN
// answer 501 naming what is not implemented (12.1 item 2), and an unbuilt
// setting's 501 names a Kinesis mapping.
func TestKinesisMappingRefusals(t *testing.T) {
	runKinesis(t, func(h *kinesisHarness) {
		h.createFunction("worker")
		h.createFunction("other")
		create := func(in lambdaapi.CreateEventSourceMappingInput) *awserr.Error {
			h.Helper()
			in.FunctionName = "worker"
			if in.EventSourceArn == nil {
				in.EventSourceArn = new(kinesisARN)
			}
			return fail(h.harness, lambdaapi.OpCreateEventSourceMapping, &in)
		}
		missing := "arn:aws:kinesis:us-east-1:000000000000:stream/nope"
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{EventSourceArn: new(missing)}), "InvalidParameterValueException",
			"1 validation error detected: Value null at 'startingPosition' failed to satisfy constraint: Member must not be null.")
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("invalid"))}), "ValidationException",
			"1 validation error detected: Value 'invalid' at 'startingPosition' failed to satisfy constraint: Member must satisfy enum value set: [LATEST, AT_TIMESTAMP, TRIM_HORIZON]")
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("AT_TIMESTAMP"))}), "NotImplemented",
			"understudy does not implement StartingPosition AT_TIMESTAMP on a Kinesis stream event source mapping. Run 'understudy coverage --service lambda' for what is supported.")
		consumer := kinesisARN + "/consumer/con1:1540591608" // the documented consumer ARN form (kinesis.md 3.6)
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{EventSourceArn: new(consumer)}), "NotImplemented",
			"understudy does not implement Kinesis stream consumers (enhanced fan-out); "+consumer+" is a consumer ARN. Map the stream ARN instead. Run 'understudy coverage --service lambda' for what is supported.")
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{EventSourceArn: new(missing), StartingPosition: new(lambdaapi.EventSourcePosition("TRIM_HORIZON"))}), "InvalidParameterValueException",
			"Stream not found: "+missing)
		errorIs(h, create(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("TRIM_HORIZON")), ParallelizationFactor: new(int32(2))}), "NotImplemented",
			"understudy does not implement ParallelizationFactor above 1 on a Kinesis stream event source mapping. Run 'understudy coverage --service lambda' for what is supported.")
		first := call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.kinesisInput(lambdaapi.CreateEventSourceMappingInput{}))
		dup := create(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("TRIM_HORIZON"))})
		errorIs(h, dup, "ResourceConflictException", `The event source arn (" `+kinesisARN+` ") and function (" worker ") provided mapping already exists. Please update or delete the existing mapping with UUID `+*first.UUID)
		if dup.ID.Status != 409 {
			h.Errorf("duplicate status %d, want 409", dup.ID.Status)
		}
		second := h.kinesisInput(lambdaapi.CreateEventSourceMappingInput{})
		second.FunctionName = "other"
		call(h.harness, lambdaapi.OpCreateEventSourceMapping, second)
	})
}

// A put reaches the function as a one-record event whose record is the
// documented Kinesis record, members in order (aws-doc with-kinesis,
// kinesis.md 8.1 and 13.8 item 2; lambda-stream-sources.md 8.1): base64
// data, approximateArrivalTimestamp in epoch seconds with milliseconds,
// eventID <shardId>:<sequenceNumber>, invokeIdentityArn the execution
// role, the stream's region and ARN, no encryptionType. The stream is read
// through ListShards, GetShardIterator and GetRecords as the execution
// role.
func TestKinesisMappingDeliversTheDocumentedRecord(t *testing.T) {
	runKinesis(t, func(h *kinesisHarness) {
		h.createFunction("worker")
		call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.kinesisInput(lambdaapi.CreateEventSourceMappingInput{}))
		h.x.StartPollers()
		h.quiesce()
		seq := "49590338271490256608559692538361571095921575989136588898"
		h.stream.add(seq, "1", []byte("Hello, this is a test."), time.Unix(1545084650, 987_000_000).UTC())
		h.quiesce()
		if h.rec.count() != 1 || len(h.rec.records[0]) != 1 {
			h.Fatalf("invocations %d, want one with one record", h.rec.count())
		}
		want := `{"kinesis":{"kinesisSchemaVersion":"1.0","partitionKey":"1","sequenceNumber":"` + seq + `","data":"SGVsbG8sIHRoaXMgaXMgYSB0ZXN0Lg==","approximateArrivalTimestamp":1545084650.987},` +
			`"eventSource":"aws:kinesis","eventVersion":"1.0","eventID":"` + kinesisShard + `:` + seq + `","eventName":"aws:kinesis:record",` +
			`"invokeIdentityArn":"` + role + `","awsRegion":"us-east-1","eventSourceARN":"` + kinesisARN + `"}`
		var got bytes.Buffer
		if err := json.Compact(&got, h.rec.records[0][0]); err != nil {
			h.Fatal(err)
		}
		if got.String() != want {
			h.Errorf("record =\n%s\nwant\n%s", got.String(), want)
		}
		for _, op := range []string{"ListShards", "GetShardIterator", "GetRecords"} {
			cs := h.stream.callers(op)
			if len(cs) == 0 || !strings.Contains(cs[0], "role/lambda-role") {
				h.Errorf("%s callers = %v, want the execution role", op, cs)
			}
		}
	})
}

// A failed batch is retried after the kind's poll interval under
// lambda.esm-stream.retry-delay poll-interval, the default (inferred,
// ESMS-06; the value read from esm.PollInterval, never written out): one
// invocation until the delay passes, a second at it.
func TestKinesisMappingRetriesAfterThePollInterval(t *testing.T) {
	runKinesis(t, func(h *kinesisHarness) {
		h.rec.fail = true
		h.createFunction("worker")
		call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.kinesisInput(lambdaapi.CreateEventSourceMappingInput{}))
		h.x.StartPollers()
		h.quiesce()
		h.stream.add("1000", "k", []byte("x"), h.Clock.Now().Wire())
		h.quiesce()
		delay := esm.PollInterval(esm.KinesisStream{})
		if n := h.rec.count(); n != 1 {
			h.Fatalf("invocations after the put = %d, want 1", n)
		}
		h.advancePolled(delay - time.Millisecond)
		if n := h.rec.count(); n != 1 {
			h.Errorf("invocations 1 ms before the retry delay = %d, want 1", n)
		}
		h.advancePolled(time.Millisecond)
		if n := h.rec.count(); n != 2 {
			h.Errorf("invocations at the retry delay = %d, want 2", n)
		}
	})
}
