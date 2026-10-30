package lambda_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/kinesis"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
)

// The Kinesis mapping over the real Kinesis service (08b contract 6;
// spec B5, goldens 11-13, 16-19, 21-23). The blind tests'
// fakeKinesis covers goldens 10, 14, 15, 20 and 25.

// realStream is the Kinesis stream these tests map, in the harness's
// account and region (kinesis.md 3.1).
const realStream = "arn:aws:kinesis:us-east-1:000000000000:stream/orders"

// kinesisRecorder keeps each invocation's records as the bytes Lambda
// sent and answers with answer, success when nil.
type kinesisRecorder struct {
	mu      sync.Mutex
	batches [][]json.RawMessage
	answer  func(records []json.RawMessage) runtimeapi.Outcome
}

func (r *kinesisRecorder) handle(_ context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
	var ev struct{ Records []json.RawMessage }
	if err := json.Unmarshal(inv.Payload, &ev); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, ev.Records)
	if r.answer != nil {
		return r.answer(ev.Records), nil
	}
	return runtimeapi.Success{Payload: []byte("null")}, nil
}

func (r *kinesisRecorder) setAnswer(answer func([]json.RawMessage) runtimeapi.Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answer = answer
}

func (r *kinesisRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.batches)
}

// failWith answers every invocation with an unhandled error.
func failWith([]json.RawMessage) runtimeapi.Outcome { return alwaysFail(streamEvent{}) }

// realKinesisHarness is Lambda with its Execution over the real Kinesis
// and SQS services, a stub SNS Publish, and an outbox delivering
// on-failure records.
type realKinesisHarness struct {
	*execHarness
	rec    *kinesisRecorder
	topics *published
}

func runRealKinesis(t *testing.T, f esm.StreamFidelity, fn func(h *realKinesisHarness)) {
	t.Helper()
	box := outbox.New()
	kerneltest.Run(t, func(k *kerneltest.K) {
		if err := box.Bind(k.Kernel, outbox.Options{}); err != nil {
			k.Fatal(err)
		}
		blobs, err := blob.NewMemory(k.TempDir(), 0)
		if err != nil {
			k.Fatal(err)
		}
		defer func() {
			if err := blobs.Close(); err != nil {
				k.Error(err)
			}
		}()
		d := k.Deps(kinesisapi.Service())
		d.Blob = blobs
		kinesis.New(d, kinesis.Options{})
		sqs.New(k.Deps(sqsapi.Service()), sqs.Options{})
		topics := &published{}
		kernel.Bind(k.Kernel.Binder(snsapi.Service()), snsapi.OpPublish, func(_ context.Context, _ *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
			topics.mu.Lock()
			defer topics.mu.Unlock()
			topics.messages = append(topics.messages, *in)
			return &snsapi.PublishOutput{MessageId: new("stub")}, nil
		}, registry.Full{})
		rec := &kinesisRecorder{}
		withExec(k, rec.handle, nil, func(h *execHarness) {
			fn(&realKinesisHarness{execHarness: h, rec: rec, topics: topics})
		}, func(c *execConfig) { c.svc.Streams = f })
	}, kerneltest.Deliver(box), kerneltest.Services(lambdaapi.Service(), logsapi.Service(), sqsapi.Service(), snsapi.Service(), kinesisapi.Service()))
}

// createStream creates the stream orders with shards shards.
func (h *realKinesisHarness) createStream(shards int32) {
	h.Helper()
	call(h.harness, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "orders", ShardCount: new(shards)})
}

// put writes data under key, at the hash key when hash is not empty; the
// service wakes the stream's readers.
func (h *realKinesisHarness) put(key, hash, data string) *kinesisapi.PutRecordOutput {
	h.Helper()
	in := &kinesisapi.PutRecordInput{StreamName: new("orders"), PartitionKey: new(key), Data: []byte(data)}
	if hash != "" {
		in.ExplicitHashKey = new(hash)
	}
	return call(h.harness, kinesisapi.OpPutRecord, in)
}

// putAll writes data in one PutRecords call under one key, so the records
// share a shard and keep their order.
func (h *realKinesisHarness) putAll(data ...string) *kinesisapi.PutRecordsOutput {
	h.Helper()
	in := &kinesisapi.PutRecordsInput{StreamName: new("orders")}
	for _, d := range data {
		in.Records = append(in.Records, kinesisapi.PutRecordsRequestEntry{Data: []byte(d), PartitionKey: new("k")})
	}
	return call(h.harness, kinesisapi.OpPutRecords, in)
}

// mapInput is in from orders to worker, TRIM_HORIZON unless in says
// otherwise.
func mapInput(in lambdaapi.CreateEventSourceMappingInput) *lambdaapi.CreateEventSourceMappingInput {
	in.FunctionName, in.EventSourceArn = "worker", new(realStream)
	if in.StartingPosition == nil {
		in.StartingPosition = new(lambdaapi.EventSourcePosition("TRIM_HORIZON"))
	}
	return &in
}

// mapStream creates the function worker and a mapping from orders to it.
func (h *realKinesisHarness) mapStream(in lambdaapi.CreateEventSourceMappingInput) *lambdaapi.EventSourceMappingConfiguration {
	h.Helper()
	h.createFunction("worker")
	return call(h.harness, lambdaapi.OpCreateEventSourceMapping, mapInput(in))
}

func (h *realKinesisHarness) start() {
	h.Helper()
	h.x.StartPollers()
	h.quiesce()
}

// kinesisEvent is the members of a delivered Kinesis record a test reads.
type kinesisEvent struct {
	Kinesis struct {
		PartitionKey                string      `json:"partitionKey"`
		SequenceNumber              string      `json:"sequenceNumber"`
		Data                        string      `json:"data"`
		ApproximateArrivalTimestamp json.Number `json:"approximateArrivalTimestamp"`
	} `json:"kinesis"`
	EventID        string `json:"eventID"`
	EventSourceARN string `json:"eventSourceARN"`
}

func decodeEvent(tb testing.TB, raw json.RawMessage) kinesisEvent {
	tb.Helper()
	var ev kinesisEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		tb.Fatal(err)
	}
	return ev
}

// dataOf is the decoded data of a delivered record.
func dataOf(tb testing.TB, raw json.RawMessage) string {
	tb.Helper()
	b, err := base64.StdEncoding.DecodeString(decodeEvent(tb, raw).Kinesis.Data)
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

// invocations returns each invocation's records' data.
func (h *realKinesisHarness) invocations() [][]string {
	h.Helper()
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	out := make([][]string, 0, len(h.rec.batches))
	for _, batch := range h.rec.batches {
		data := make([]string, 0, len(batch))
		for _, raw := range batch {
			data = append(data, dataOf(h, raw))
		}
		out = append(out, data)
	}
	return out
}

// data returns every delivered record's data, in order.
func (h *realKinesisHarness) data() []string {
	h.Helper()
	var out []string
	for _, batch := range h.invocations() {
		out = append(out, batch...)
	}
	return out
}

// kinesisFailure is an on-failure record in the Kinesis form
// (lambda-stream-sources.md 5.6).
type kinesisFailure struct {
	RequestContext struct {
		Condition              string `json:"condition"`
		ApproximateInvokeCount int    `json:"approximateInvokeCount"`
	} `json:"requestContext"`
	ResponseContext *struct {
		FunctionError *string `json:"functionError"`
	} `json:"responseContext"`
	Info *struct {
		ShardID   string `json:"shardId"`
		Start     string `json:"startSequenceNumber"`
		End       string `json:"endSequenceNumber"`
		BatchSize int    `json:"batchSize"`
		StreamARN string `json:"streamArn"`
	} `json:"KinesisBatchInfo"`
}

func decodeFailure(tb testing.TB, body string) kinesisFailure {
	tb.Helper()
	var f kinesisFailure
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		tb.Fatalf("on-failure body %s: %v", body, err)
	}
	if f.Info == nil {
		tb.Fatalf("on-failure body %s has no KinesisBatchInfo", body)
	}
	return f
}

// failures returns the on-failure records the queue url holds.
func (h *realKinesisHarness) failures(url string) []kinesisFailure {
	h.Helper()
	messages := h.receiveNow(url)
	out := make([]kinesisFailure, 0, len(messages))
	for _, m := range messages {
		out = append(out, decodeFailure(h, deref(m.Body)))
	}
	return out
}

// onFailureTo is a DestinationConfig sending on-failure records to dest.
func onFailureTo(dest string) *lambdaapi.DestinationConfig {
	return &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dest)}}
}

// mappingChanges are the journal's state changes of the mapping arn, as
// From->To.
func (h *realKinesisHarness) mappingChanges(arn string) []string {
	var out []string
	events := h.Journal.Query(journal.Filter{Kind: journal.KindStateChange}).Events
	for i := range events {
		if sc, ok := events[i].Detail.(journal.StateChange); ok && sc.Resource.String() == arn {
			out = append(out, string(sc.From)+"->"+string(sc.To))
		}
	}
	return out
}

// The mapping reads every shard of a real stream through the Kinesis
// service as the execution role: one record put to each end of the hash
// space (kinesis.md 3.3) reaches the function, each with its shard in
// eventID, the sequence number PutRecord answered, its partition key, and
// its arrival in epoch seconds with milliseconds (kinesis.md 8.1, 13.8
// item 2; 08b contract 6.2).
func TestKinesisMappingReadsEveryShardOfTheService(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.createStream(2)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		at := h.Clock.Now().Wire()
		low := h.put("a", "0", "low")
		high := h.put("b", "340282366920938463463374607431768211455", "high")
		h.quiesce()
		if low.ShardId == high.ShardId {
			h.Fatalf("both records went to %s, want one per shard", low.ShardId)
		}
		var got []kinesisEvent
		h.rec.mu.Lock()
		for _, batch := range h.rec.batches {
			for _, raw := range batch {
				got = append(got, decodeEvent(h, raw))
			}
		}
		h.rec.mu.Unlock()
		if len(got) != 2 {
			h.Fatalf("delivered %d records, want 2", len(got))
		}
		want := map[string]*kinesisapi.PutRecordOutput{"a": low, "b": high}
		ms := at.UnixMilli()
		stamp := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%03d", ms/1000, ms%1000), "0"), ".")
		for _, ev := range got {
			put := want[ev.Kinesis.PartitionKey]
			if put == nil {
				h.Fatalf("delivered a record keyed %q", ev.Kinesis.PartitionKey)
			}
			if ev.EventID != put.ShardId+":"+put.SequenceNumber || ev.Kinesis.SequenceNumber != put.SequenceNumber || ev.EventSourceARN != realStream {
				h.Errorf("record %+v, want shard %s and sequence %s of %s", ev, put.ShardId, put.SequenceNumber, realStream)
			}
			if string(ev.Kinesis.ApproximateArrivalTimestamp) != stamp {
				h.Errorf("approximateArrivalTimestamp %s, want the put's instant %s", ev.Kinesis.ApproximateArrivalTimestamp, stamp)
			}
		}
	})
}

// Three PutRecords calls of ten records under TRIM_HORIZON and BatchSize
// 10 give three invocations of ten, in put order (third-party-recorded,
// golden 11: LS-K:L564-L1097; lambda-stream-sources.md 4.2).
func TestKinesisMappingBatchesInPutOrder(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.createStream(1)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(10))})
		h.start()
		want := make([][]string, 0, 3)
		for b := range 3 {
			batch := make([]string, 0, 10)
			for i := range 10 {
				batch = append(batch, strconv.Itoa(b*10+i))
			}
			h.putAll(batch...)
			want = append(want, batch)
		}
		h.quiesce()
		if got := h.invocations(); !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
	})
}

// LATEST starts after the records the stream holds when the mapping is
// created (lambda.esm-stream.latest-anchor create-instant, inferred,
// ESMS-17), read through ListShards and the shard's records (08b contract
// 6.1).
func TestKinesisMappingLatestSkipsEarlierRecords(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.createStream(1)
		h.put("k", "", "before")
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))})
		h.start()
		h.put("k", "", "after")
		h.quiesce()
		if got := h.data(); !slices.Equal(got, []string{"after"}) {
			h.Errorf("delivered %v, want [after]", got)
		}
	})
}

// With MaximumRetryAttempts 1 a failing batch runs twice, one retry delay
// apart, and then sends one on-failure record to SQS in the Kinesis form,
// KinesisBatchInfo naming the shard, the sequence range and the stream,
// approximateInvokeCount 2 (third-party-recorded, golden 12:
// LS-K:L1326-L1362, count LS-K:L1334; lambda-stream-sources.md 5.2, 5.6).
func TestKinesisMappingOnFailureToSQS(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.rec.setAnswer(failWith)
		h.createStream(1)
		url, dlq := h.queue("dlq", nil)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{MaximumRetryAttempts: new(int32(1)), DestinationConfig: onFailureTo(dlq)})
		h.start()
		put := h.put("k", "", "x")
		h.quiesce()
		h.advancePolled(esm.KinesisRetryDelay)
		got := h.failures(url)
		if h.rec.count() != 2 || len(got) != 1 {
			h.Fatalf("%d invocations and %d on-failure records, want 2 and 1", h.rec.count(), len(got))
		}
		f := got[0]
		if f.RequestContext.Condition != "RetryAttemptsExhausted" || f.RequestContext.ApproximateInvokeCount != 2 ||
			f.ResponseContext == nil || derefAny(f.ResponseContext.FunctionError) != "Unhandled" ||
			f.Info.ShardID != put.ShardId || f.Info.Start != put.SequenceNumber || f.Info.End != put.SequenceNumber ||
			f.Info.BatchSize != 1 || f.Info.StreamARN != realStream {
			h.Errorf("on-failure record %+v %+v", f.RequestContext, *f.Info)
		}
	})
}

// The SNS destination gets the same record as the message, with no
// subject (third-party-recorded, golden 16: LS-K:L1620-L1653;
// lambda-stream-sources.md 5.6).
func TestKinesisMappingOnFailureToSNS(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.rec.setAnswer(failWith)
		h.createStream(1)
		topic := "arn:aws:sns:us-east-1:000000000000:failures"
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{MaximumRetryAttempts: new(int32(0)), DestinationConfig: onFailureTo(topic)})
		h.start()
		put := h.put("k", "", "x")
		h.quiesce()
		h.topics.mu.Lock()
		messages := slices.Clone(h.topics.messages)
		h.topics.mu.Unlock()
		if len(messages) != 1 {
			h.Fatalf("%d published messages, want 1", len(messages))
		}
		if messages[0].Subject != nil || deref(messages[0].TopicArn) != topic {
			h.Errorf("Publish to %s with subject %v, want %s and none", deref(messages[0].TopicArn), derefAny(messages[0].Subject), topic)
		}
		if f := decodeFailure(h, messages[0].Message); f.Info.Start != put.SequenceNumber || f.RequestContext.ApproximateInvokeCount != 1 {
			h.Errorf("on-failure record %+v %+v", f.RequestContext, *f.Info)
		}
	})
}

// The ReportBatchItemFailures matrix, one record per batch, as recorded
// for Kinesis with MaximumRetryAttempts 2 (third-party-recorded, golden
// 18: LS-K:L1892-L3025; lambda-stream-sources.md 5.5): the success
// responses invoke once, the malformed ones fail the whole batch to
// exhaustion (three invocations) with functionError null, and an
// exception with "Unhandled".
func TestKinesisMappingReportBatchItemFailuresMatrix(t *testing.T) {
	unhandled := "Unhandled"
	for _, c := range []struct {
		name     string
		response string // "" for an exception
		success  bool
		fnError  *string
	}{
		{"bare return", `null`, true, nil},
		{"empty list", `[]`, true, nil},
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
			runRealKinesis(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *realKinesisHarness) {
				h.rec.setAnswer(func(records []json.RawMessage) runtimeapi.Outcome {
					if c.response == "" {
						return failWith(records)
					}
					return runtimeapi.Success{Payload: []byte(c.response)}
				})
				h.createStream(1)
				url, dlq := h.queue("dlq", nil)
				h.mapStream(lambdaapi.CreateEventSourceMappingInput{
					BatchSize: new(int32(1)), MaximumRetryAttempts: new(int32(2)),
					FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"},
					DestinationConfig:     onFailureTo(dlq),
				})
				h.start()
				h.put("k", "", "a")
				h.quiesce()
				got := h.failures(url)
				if c.success {
					if n := h.rec.count(); n != 1 || len(got) != 0 {
						h.Errorf("%d invocations, %d on-failure records; want 1 and none", n, len(got))
					}
					return
				}
				if n := h.rec.count(); n != 3 || len(got) != 1 {
					h.Fatalf("%d invocations, %d on-failure records; want 3 and 1", n, len(got))
				}
				f := got[0]
				if f.RequestContext.ApproximateInvokeCount != 3 || f.ResponseContext == nil || derefAny(f.ResponseContext.FunctionError) != derefAny(c.fnError) {
					h.Errorf("on-failure record %+v, want count 3 and functionError %v", f.RequestContext, derefAny(c.fnError))
				}
			})
		})
	}
}

// A partial failure checkpoints just before the reported record: with
// BatchSize 3, MaximumRetryAttempts 3 and the sixth of six records always
// reported, records 1 to 5 are delivered once and the destination gets
// batchSize 1 with the sixth record's number as start and end,
// approximateInvokeCount 4 and functionError null (third-party-recorded,
// golden 17: LS-K:L1656-L1891; lambda-stream-sources.md 5.5).
func TestKinesisMappingPartialFailure(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *realKinesisHarness) {
		h.createStream(1)
		url, dlq := h.queue("dlq", nil)
		out := h.putAll("1", "2", "3", "4", "5", "6")
		sixth := out.Records[5].SequenceNumber
		h.rec.setAnswer(func(records []json.RawMessage) runtimeapi.Outcome {
			for _, raw := range records {
				if decodeEvent(h, raw).Kinesis.SequenceNumber == deref(sixth) {
					return runtimeapi.Success{Payload: []byte(`{"batchItemFailures":[{"itemIdentifier":"` + deref(sixth) + `"}]}`)}
				}
			}
			return runtimeapi.Success{Payload: []byte("null")}
		})
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			BatchSize: new(int32(3)), MaximumRetryAttempts: new(int32(3)),
			FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"},
			DestinationConfig:     onFailureTo(dlq),
		})
		h.start()
		counts := map[string]int{}
		for _, d := range h.data() {
			counts[d]++
		}
		for _, d := range []string{"1", "2", "3", "4", "5"} {
			if counts[d] != 1 {
				h.Errorf("record %s delivered %d times, want once", d, counts[d])
			}
		}
		got := h.failures(url)
		if len(got) != 1 {
			h.Fatalf("%d on-failure records, want 1", len(got))
		}
		f := got[0]
		if f.Info.BatchSize != 1 || f.Info.Start != deref(sixth) || f.Info.End != deref(sixth) || f.RequestContext.ApproximateInvokeCount != 4 ||
			f.ResponseContext == nil || f.ResponseContext.FunctionError != nil {
			h.Errorf("on-failure record %+v %+v, want the sixth record alone, count 4, functionError null", f.RequestContext, *f.Info)
		}
	})
}

// A record that passes MaximumRecordAgeInSeconds while its batch is
// retried is discarded with RecordAgeExceeded, batchSize 1 and no
// responseContext (third-party-recorded, golden 21: LS-K:L3208-L3239).
// Named waiver (spec golden 21): AWS's approximateInvokeCount 8 folds in
// its polling start and retry delay; asserted here is that the count
// equals the invocations made and is above 1.
func TestKinesisMappingRecordAgeWhileRetrying(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.rec.setAnswer(failWith)
		h.createStream(1)
		url, dlq := h.queue("dlq", nil)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{MaximumRecordAgeInSeconds: new(int32(60)), DestinationConfig: onFailureTo(dlq)})
		h.start()
		h.put("k", "", "x")
		h.quiesce()
		for range 3 {
			h.advancePolled(esm.KinesisRetryDelay)
		}
		h.advancePolled(time.Minute)
		got := h.failures(url)
		if len(got) != 1 {
			h.Fatalf("%d on-failure records, want 1", len(got))
		}
		f := got[0]
		if f.RequestContext.Condition != "RecordAgeExceeded" || f.ResponseContext != nil || f.Info.BatchSize != 1 {
			h.Errorf("on-failure record %+v %+v, want RecordAgeExceeded, batchSize 1, no responseContext", f.RequestContext, *f.Info)
		}
		if n := h.rec.count(); f.RequestContext.ApproximateInvokeCount != n || n <= 1 {
			h.Errorf("approximateInvokeCount %d, want the %d invocations made, above 1", f.RequestContext.ApproximateInvokeCount, n)
		}
	})
}

// A record already older than MaximumRecordAgeInSeconds when it is read
// is discarded without invocation, approximateInvokeCount 1
// (third-party-recorded, golden 22: LS-K:L3273-L3304); among five fresh
// records it goes alone, batchSize 1, and the fresh ones are invoked
// (third-party-recorded, golden 23: LS-K:L3338-L3451;
// lambda-stream-sources.md 5.3).
func TestKinesisMappingRecordAgeBeforeRead(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.createStream(1)
		url, dlq := h.queue("dlq", nil)
		old := h.put("k", "", "old")
		h.advancePolled(81 * time.Second)
		h.putAll("1", "2", "3", "4", "5")
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			BatchSize: new(int32(10)), MaximumRetryAttempts: new(int32(0)), MaximumRecordAgeInSeconds: new(int32(60)), DestinationConfig: onFailureTo(dlq),
		})
		h.start()
		if got := h.invocations(); !slices.EqualFunc(got, [][]string{{"1", "2", "3", "4", "5"}}, slices.Equal) {
			h.Errorf("invocations %v, want the five fresh records once", got)
		}
		got := h.failures(url)
		if len(got) != 1 {
			h.Fatalf("%d on-failure records, want 1", len(got))
		}
		f := got[0]
		if f.RequestContext.Condition != "RecordAgeExceeded" || f.RequestContext.ApproximateInvokeCount != 1 || f.ResponseContext != nil ||
			f.Info.BatchSize != 1 || f.Info.Start != old.SequenceNumber || f.Info.End != old.SequenceNumber {
			h.Errorf("on-failure record %+v %+v, want the old record alone, RecordAgeExceeded, count 1", f.RequestContext, *f.Info)
		}
	})
}

// A batch being retried is measured against the stream's own retention,
// read through DescribeStreamSummary (08b contract 6.2): with 48 hours it
// is still retried at 24 hours plus 1 ms, DynamoDB's retention, and
// dropped without an on-failure record at 48 hours plus 1 ms, the reader
// journaling RecordsTrimmed (aws-doc lambda-stream-sources.md 5.1, 5.3;
// spec "Retention").
func TestKinesisMappingRetentionIsTheStreams(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.rec.setAnswer(failWith)
		h.createStream(1)
		call(h.harness, kinesisapi.OpIncreaseStreamRetentionPeriod, &kinesisapi.IncreaseStreamRetentionPeriodInput{StreamName: new("orders"), RetentionPeriodHours: 48})
		url, dlq := h.queue("dlq", nil)
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{DestinationConfig: onFailureTo(dlq)})
		h.start()
		written := h.Clock.Now()
		h.put("k", "", "x")
		h.quiesce()
		if _, err := h.Clock.AdvanceTo(written.Add(esm.DynamoDBRetention + time.Millisecond)); err != nil {
			h.Fatal(err)
		}
		h.quiesce()
		if n := h.rec.count(); n != 2 {
			h.Fatalf("%d invocations at 24 h plus 1 ms, want the failure and its retry", n)
		}
		if _, err := h.Clock.AdvanceTo(written.Add(48*time.Hour + time.Millisecond)); err != nil {
			h.Fatal(err)
		}
		h.quiesce()
		if n := h.rec.count(); n != 2 {
			h.Errorf("%d invocations at 48 h plus 1 ms, want the batch dropped", n)
		}
		changes := h.mappingChanges(*m.EventSourceMappingArn)
		if !slices.Contains(changes, "Polling->RecordsTrimmed") || !slices.Contains(changes, "RecordsTrimmed->Polling") {
			h.Errorf("mapping state changes %v, want Polling->RecordsTrimmed and back", changes)
		}
		if got := h.receiveNow(url); len(got) != 0 {
			h.Errorf("%d on-failure records for a trimmed batch, want none", len(got))
		}
	})
}

// A checkpoint that the stream trims behind (here: the stream's 24 hours
// plus 1 ms pass while the mapping is disabled, kinesis.md 4.7) drops the
// lost records without an on-failure record, journals it, and resumes at
// TRIM_HORIZON: GetShardIterator's refusal of the trimmed checkpoint is
// the trim (08b contract 6.2; aws-doc lambda-stream-sources.md 5.1, 5.3;
// dropping without an on-failure record is inferred, UNVERIFIED, a clause
// of the CreateEventSourceMapping note).
func TestKinesisMappingCheckpointBehindRetentionResumesAtTrimHorizon(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.createStream(1)
		url, dlq := h.queue("dlq", nil)
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{DestinationConfig: onFailureTo(dlq)})
		h.start()
		h.put("k", "", "a")
		h.quiesce()
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(false)})
		h.quiesce()
		h.put("k", "", "lost")
		h.advancePolled(time.Hour)
		h.put("k", "", "kept")
		h.advancePolled(24*time.Hour - time.Hour + time.Millisecond)
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(true)})
		h.quiesce()
		if got, want := h.invocations(), [][]string{{"a"}, {"kept"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
		if got := h.receiveNow(url); len(got) != 0 {
			h.Errorf("%d on-failure records for trimmed records, want none", len(got))
		}
		changes := h.mappingChanges(*m.EventSourceMappingArn)
		if !slices.Contains(changes, "Polling->RecordsTrimmed") || !slices.Contains(changes, "RecordsTrimmed->Polling") {
			h.Errorf("mapping state changes %v, want Polling->RecordsTrimmed and back", changes)
		}
	})
}

// Records put while the mapping is disabled are delivered after it is
// enabled again: the reader resumes at its checkpoint (inferred for
// Kinesis, ESMS-13; lambda-stream-sources.md 7.1; the note's UNVERIFIED
// clause).
func TestKinesisMappingResumesAtTheCheckpointAfterReEnable(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.createStream(1)
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		h.put("k", "", "a")
		h.quiesce()
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(false)})
		h.quiesce()
		h.put("k", "", "while disabled")
		h.quiesce()
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(true)})
		h.quiesce()
		if got := h.data(); !slices.Equal(got, []string{"a", "while disabled"}) {
			h.Errorf("delivered %v, want [a while disabled]", got)
		}
	})
}

// A deleted stream stops its reader with a StreamGone entry and the
// mapping stays Enabled (recorded for DynamoDB, lambda-stream-sources.md
// 7.3; the shared reader, 08b contract 6.2).
func TestKinesisMappingStreamGoneStopsTheReader(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.createStream(1)
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		h.put("k", "", "a")
		h.quiesce()
		call(h.harness, kinesisapi.OpDeleteStream, &kinesisapi.DeleteStreamInput{StreamName: new("orders")})
		h.advancePolled(time.Second)
		h.createStream(1)
		h.put("k", "", "b")
		h.quiesce()
		if got := h.data(); !slices.Equal(got, []string{"a"}) {
			h.Errorf("delivered %v, want [a]: a stopped reader reads nothing more", got)
		}
		if changes := h.mappingChanges(*m.EventSourceMappingArn); !slices.Contains(changes, "Polling->StreamGone") {
			h.Errorf("mapping state changes %v, want Polling->StreamGone", changes)
		}
		got := call(h.harness, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *m.UUID})
		if derefAny(got.State) != "Enabled" {
			h.Errorf("State = %v, want Enabled", derefAny(got.State))
		}
	})
}

// Settings a Kinesis mapping does not build answer 501 naming a Kinesis
// mapping rather than being stored without effect (spec "Out of both";
// invariant 3): a FilterCriteria pattern that compiles (refusal, golden
// 13: LS-K:L1365-L1446) and an S3 on-failure destination (refusal, golden
// 19: LS-K:L3114-L3157), on Create and on Update; Update keeps the kind
// and starting position and applies the stream settings (08b contract
// 6.1).
func TestKinesisMappingUnbuiltSettingsAndUpdate(t *testing.T) {
	runRealKinesis(t, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.createStream(1)
		h.createFunction("worker")
		unbuilt := func(setting string) string {
			return "understudy does not implement " + setting + " on a Kinesis stream event source mapping. Run 'understudy coverage --service lambda' for what is supported."
		}
		filter := &lambdaapi.FilterCriteria{Filters: []lambdaapi.Filter{{Pattern: new(`{"data":{"event_type":["function_1"]}}`)}}}
		s3 := onFailureTo("arn:aws:s3:::failures")
		errorIs(h, fail(h.harness, lambdaapi.OpCreateEventSourceMapping, mapInput(lambdaapi.CreateEventSourceMappingInput{FilterCriteria: filter})), "NotImplemented", unbuilt("a FilterCriteria pattern"))
		errorIs(h, fail(h.harness, lambdaapi.OpCreateEventSourceMapping, mapInput(lambdaapi.CreateEventSourceMappingInput{DestinationConfig: s3})), "NotImplemented", unbuilt("an S3 on-failure destination"))

		m := call(h.harness, lambdaapi.OpCreateEventSourceMapping, mapInput(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))}))
		out := call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, MaximumRetryAttempts: new(int32(3)), MaximumRecordAgeInSeconds: new(int32(120))})
		if derefAny(out.StartingPosition) != lambdaapi.EventSourcePosition("LATEST") || derefAny(out.MaximumRetryAttempts) != int32(3) || derefAny(out.MaximumRecordAgeInSeconds) != int32(120) {
			h.Errorf("Update = StartingPosition %v, MaximumRetryAttempts %v, MaximumRecordAgeInSeconds %v; want LATEST, 3, 120",
				derefAny(out.StartingPosition), derefAny(out.MaximumRetryAttempts), derefAny(out.MaximumRecordAgeInSeconds))
		}
		errorIs(h, fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, BisectBatchOnFunctionError: new(true)}), "NotImplemented", unbuilt("BisectBatchOnFunctionError"))
		errorIs(h, fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, FilterCriteria: filter}), "NotImplemented", unbuilt("a FilterCriteria pattern"))
	})
}
