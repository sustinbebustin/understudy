package lambda_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The function and destinations of the tests below, all in the test
// account and region.
const (
	fnUnqualified = "arn:aws:lambda:us-east-1:000000000000:function:fn"
	successQueue  = "arn:aws:sqs:us-east-1:000000000000:on-success"
	failureQueue  = "arn:aws:sqs:us-east-1:000000000000:on-failure"
	dlqA          = "arn:aws:sqs:us-east-1:000000000000:fn-dlq"
	dlqB          = "arn:aws:sqs:us-east-1:000000000000:fn-dlq-b"
	alertsTopic   = "arn:aws:sns:us-east-1:000000000000:alerts"
	auditorARN    = "arn:aws:lambda:us-east-1:000000000000:function:auditor"
	auditBus      = "arn:aws:events:us-east-1:000000000000:event-bus/audit"
	auditBucket   = "arn:aws:s3:::audit-records"
)

// sink is the kernel's DeliverSink in place of the outbox: it keeps every
// delivery Tx.Deliver produces.
type sink struct {
	mu  sync.Mutex
	got []kernel.Delivery
}

func (s *sink) Deliver(d kernel.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, d)
	return nil
}

// lambda returns the deliveries of asynchronous invocation records and dead
// letters, in order.
func (s *sink) lambda() []kernel.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []kernel.Delivery
	for i := range s.got {
		if s.got[i].Request.Mechanism.Via() == ref.LambdaDestination {
			out = append(out, s.got[i])
		}
	}
	return out
}

// destHarness is an execHarness whose kernel hands deliveries to sink and
// stubs the lookups that find a destination: a queue, topic, bus or bucket
// whose name begins "missing" is not found.
type destHarness struct {
	*execHarness
	sink *sink
}

func runDestinations(t *testing.T, handler pooltest.Handler, fn func(h *destHarness)) {
	t.Helper()
	s := &sink{}
	kerneltest.Run(t, func(k *kerneltest.K) {
		stubDestinationLookups(k, nil)
		withExec(k, handler, nil, func(h *execHarness) { fn(&destHarness{execHarness: h, sink: s}) })
	}, kerneltest.Services(lambdaapi.Service(), logsapi.Service(), sqsapi.Service(), snsapi.Service(), eventbridgeapi.Service(), s3api.Service()),
		kerneltest.Deliver(s))
}

// lookup is one destination lookup a stub received.
type lookup struct {
	op        string
	in        string
	principal identity.Principal
	origin    ref.Origin
}

// lookups records the destination lookups the stubs answered.
type lookups struct {
	mu  sync.Mutex
	got []lookup
}

func (l *lookups) add(op, in string, c *kernel.Call) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, lookup{op: op, in: in, principal: c.Principal(), origin: c.Origin()})
}

func (l *lookups) all() []lookup {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]lookup(nil), l.got...)
}

// stubDestinationLookups answers the calls that find a destination with
// each service's own not-found error for a name that begins "missing", an
// internal error for one that begins "broken", and success otherwise.
func stubDestinationLookups(k *kerneltest.K, seen *lookups) {
	k.Stub(sqsapi.OpGetQueueUrl, func(_ context.Context, c *kernel.Call, in *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
		owner := ""
		if in.QueueOwnerAWSAccountId != nil {
			owner = *in.QueueOwnerAWSAccountId
		}
		seen.add("GetQueueUrl", in.QueueName+" "+owner, c)
		switch {
		case strings.HasPrefix(in.QueueName, "missing"):
			return nil, sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
		case strings.HasPrefix(in.QueueName, "broken"):
			return nil, awserr.Internalf(context.DeadlineExceeded)
		}
		return &sqsapi.GetQueueUrlOutput{QueueUrl: new("http://localhost:4566/000000000000/" + in.QueueName)}, nil
	})
	k.Stub(snsapi.OpGetTopicAttributes, func(_ context.Context, c *kernel.Call, in *snsapi.GetTopicAttributesInput) (*snsapi.GetTopicAttributesOutput, *awserr.Error) {
		seen.add("GetTopicAttributes", in.TopicArn, c)
		if strings.Contains(in.TopicArn, ":missing") {
			return nil, snsapi.ErrNotFoundException("Topic does not exist")
		}
		return &snsapi.GetTopicAttributesOutput{Attributes: map[string]string{"TopicArn": in.TopicArn}}, nil
	})
	k.Stub(eventbridgeapi.OpDescribeEventBus, func(_ context.Context, c *kernel.Call, in *eventbridgeapi.DescribeEventBusInput) (*eventbridgeapi.DescribeEventBusOutput, *awserr.Error) {
		name := ""
		if in.Name != nil {
			name = *in.Name
		}
		seen.add("DescribeEventBus", name, c)
		if strings.Contains(name, "/missing") {
			return nil, eventbridgeapi.ErrResourceNotFoundException("Event bus " + name + " does not exist.")
		}
		return &eventbridgeapi.DescribeEventBusOutput{Arn: new(name)}, nil
	})
	k.Stub(s3api.OpHeadBucket, func(_ context.Context, c *kernel.Call, in *s3api.HeadBucketInput) (*s3api.HeadBucketOutput, *awserr.Error) {
		seen.add("HeadBucket", in.Bucket, c)
		if strings.HasPrefix(in.Bucket, "missing") {
			return nil, s3api.ErrNotFound("Not Found")
		}
		return &s3api.HeadBucketOutput{}, nil
	})
}

// configure puts fn's event-invoke configuration with retries and
// the destinations given ("" for none).
func (h *destHarness) configure(qualifier string, retries int32, onSuccess, onFailure string) {
	h.Helper()
	in := &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", MaximumRetryAttempts: new(retries), DestinationConfig: &lambdaapi.DestinationConfig{}}
	if qualifier != "" {
		in.Qualifier = new(qualifier)
	}
	if onSuccess != "" {
		in.DestinationConfig.OnSuccess = &lambdaapi.OnSuccess{Destination: new(onSuccess)}
	}
	if onFailure != "" {
		in.DestinationConfig.OnFailure = &lambdaapi.OnFailure{Destination: new(onFailure)}
	}
	call(h.harness, lambdaapi.OpPutFunctionEventInvokeConfig, in)
}

// switchValue is the value of the Lambda fidelity switch name: a test of a
// behavior no AWS evidence fixes reads understudy's choice from the switch
// rather than repeating it.
func (h *harness) switchValue(name string) string {
	h.Helper()
	for _, e := range h.svc.Switches() {
		if e.Name == name {
			return e.Value
		}
	}
	h.Fatalf("Switches() lists no %s (lambda-destinations-layers.md 11.2)", name)
	return ""
}

// record is an invocation record decoded with its payloads as written.
type record struct {
	Version        string `json:"version"`
	Timestamp      string `json:"timestamp"`
	RequestContext struct {
		RequestID              string `json:"requestId"`
		FunctionArn            string `json:"functionArn"`
		Condition              string `json:"condition"`
		ApproximateInvokeCount int    `json:"approximateInvokeCount"`
	} `json:"requestContext"`
	RequestPayload  json.RawMessage `json:"requestPayload"`
	ResponseContext *struct {
		StatusCode      int     `json:"statusCode"`
		ExecutedVersion string  `json:"executedVersion"`
		FunctionError   *string `json:"functionError"`
	} `json:"responseContext"`
	ResponsePayload json.RawMessage `json:"responsePayload"`
}

// body is the record or event a delivery carries, whatever its target.
func carried(tb testing.TB, d kernel.Delivery) []byte {
	tb.Helper()
	switch t := d.Request.Target.(type) {
	case delivery.SQSQueue:
		return []byte(t.Body)
	case delivery.SNSTopic:
		return []byte(t.Message)
	case delivery.LambdaFunction:
		return t.Event
	case delivery.EventBus:
		if len(t.Entries) != 1 {
			tb.Fatalf("event bus delivery with %d entries, want 1", len(t.Entries))
		}
		return []byte(t.Entries[0].Detail)
	case delivery.S3Object:
		return t.Body
	case delivery.HTTPEndpoint:
	}
	tb.Fatalf("delivery target %T carries no record", d.Request.Target)
	return nil
}

// recordOf decodes the record d carries.
func recordOf(tb testing.TB, d kernel.Delivery) record {
	tb.Helper()
	var r record
	if err := json.Unmarshal(carried(tb, d), &r); err != nil {
		tb.Fatalf("delivery body %s is not a record: %v", carried(tb, d), err)
	}
	return r
}

// timestampOf is the record timestamp of the instant d was accepted at:
// the record is built in the write that settles the event
// (lambda-destinations-layers.md 3.1, 11.3.3, 11.4).
func timestampOf(d kernel.Delivery) string {
	return d.Accepted.Wire().UTC().Format("2006-01-02T15:04:05.000Z")
}

// fromFn checks what every delivery of fn's records and dead letters
// shares: it comes from the unqualified function as its execution role
// (lambda-destinations-layers.md 2.5, 11.3.2), with no dead-letter queue
// of its own, carrying carries, under the retry policy the switch
// lambda.destination.delivery-retry names for a record; a dead letter is
// one attempt, since Lambda deletes an event its dead-letter queue does not
// take (5.4).
func (h *destHarness) fromFn(d kernel.Delivery, carries delivery.LambdaPayload) {
	h.Helper()
	r := d.Request
	if r.Source.String() != fnUnqualified || r.DLQ != nil {
		h.Errorf("delivery from %s with DLQ %v, want from %s with none", r.Source, r.DLQ, fnUnqualified)
	}
	if r.Sender == nil || r.Sender.Role.String() != role || r.Sender.Carries != carries {
		h.Errorf("delivery Sender = %+v, want %s carrying %s", r.Sender, role, carries)
	}
	if carries == delivery.DestinationRecord && h.switchValue("lambda.destination.delivery-retry") != "none" {
		h.Fatalf("lambda.destination.delivery-retry = %q, a value this test does not know", h.switchValue("lambda.destination.delivery-retry"))
	}
	if _, ok := r.Policy.(delivery.None); !ok {
		h.Errorf("delivery Policy = %#v, want one attempt", r.Policy)
	}
}

// onlyDelivery returns the one record or dead letter delivered.
func (h *destHarness) onlyDelivery() kernel.Delivery {
	h.Helper()
	got := h.sink.lambda()
	if len(got) != 1 {
		h.Fatalf("deliveries = %+v, want one", got)
	}
	return got[0]
}

// queueTarget is d's SQS target, which must go to queue.
func queueTarget(tb testing.TB, d kernel.Delivery, queue string) delivery.SQSQueue {
	tb.Helper()
	q, ok := d.Request.Target.(delivery.SQSQueue)
	if d.Request.Mechanism != delivery.LambdaToSQS || !ok || q.ARN.String() != queue {
		tb.Fatalf("delivery = %s %#v, want lambda-to-sqs to %s", d.Request.Mechanism, d.Request.Target, queue)
	}
	return q
}

// A success with an on-success destination sends one record: version 1.0,
// the settle instant, the Invoke's request id, the unqualified invoke's
// functionArn qualified with $LATEST, condition Success, count 1, the
// event and the response embedded as JSON, statusCode 200, executedVersion
// $LATEST and no functionError (lambda-destinations-layers.md 3.4:
// test_lambda_destinations.snapshot.json:L87-L114), as the SQS body (4.1).
func TestAsyncSuccessSendsARecordToOnSuccess(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createFunction("fn")
		h.configure("", 0, successQueue, failureQueue)
		h.invoke(fnEvent(`{"n": 1}`))
		h.settle()
		d := h.onlyDelivery()
		h.fromFn(d, delivery.DestinationRecord)
		queueTarget(t, d, successQueue)
		r := recordOf(t, d)
		calls := s.invoked()
		if len(calls) != 1 {
			t.Fatalf("%d attempts, want 1", len(calls))
		}
		if r.Version != "1.0" || r.Timestamp != timestampOf(d) {
			t.Errorf("version %q timestamp %q, want 1.0 and %s", r.Version, r.Timestamp, timestampOf(d))
		}
		rc := r.RequestContext
		if rc.RequestID != string(calls[0].RequestID) || rc.FunctionArn != fnUnqualified+":$LATEST" || rc.Condition != "Success" || rc.ApproximateInvokeCount != 1 {
			t.Errorf("requestContext = %+v, want request %s, %s:$LATEST, Success, count 1", rc, calls[0].RequestID, fnUnqualified)
		}
		if string(r.RequestPayload) != `{"n":1}` {
			t.Errorf("requestPayload = %s, want the event compacted", r.RequestPayload)
		}
		if r.ResponseContext == nil || r.ResponseContext.StatusCode != http.StatusOK || r.ResponseContext.ExecutedVersion != "$LATEST" || r.ResponseContext.FunctionError != nil {
			t.Errorf("responseContext = %+v, want statusCode 200, $LATEST and no functionError", r.ResponseContext)
		}
		if string(r.ResponsePayload) != `{"n":1}` {
			t.Errorf("responsePayload = %s, want the echoed event", r.ResponsePayload)
		}
	})
}

// A function error with MaximumRetryAttempts 0 sends RetriesExhausted with
// count 1, functionError Unhandled and the error object as the response
// (lambda-destinations-layers.md 3.4: test_lambda_destinations.snapshot.json:L150-L175),
// to the on-failure destination alone.
func TestAsyncFunctionErrorWithoutRetriesSendsRetriesExhausted(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createFunction("fn")
		h.configure("", 0, successQueue, failureQueue)
		h.invoke(fnEvent(`{"action": "fail"}`))
		h.settle()
		h.advance(10 * time.Minute)
		d := h.onlyDelivery()
		h.fromFn(d, delivery.DestinationRecord)
		queueTarget(t, d, failureQueue)
		r := recordOf(t, d)
		if rc := r.RequestContext; rc.Condition != "RetriesExhausted" || rc.ApproximateInvokeCount != 1 || rc.FunctionArn != fnUnqualified+":$LATEST" {
			t.Errorf("requestContext = %+v, want RetriesExhausted, count 1", rc)
		}
		if r.ResponseContext == nil || r.ResponseContext.StatusCode != http.StatusOK || r.ResponseContext.FunctionError == nil || *r.ResponseContext.FunctionError != "Unhandled" {
			t.Errorf("responseContext = %+v, want statusCode 200 and functionError Unhandled", r.ResponseContext)
		}
		if string(r.ResponsePayload) != `{"errorMessage":"boom","errorType":"Error"}` {
			t.Errorf("responsePayload = %s, want the error object the runtime posted", r.ResponsePayload)
		}
		if string(r.RequestPayload) != `{"action":"fail"}` {
			t.Errorf("requestPayload = %s, want the event", r.RequestPayload)
		}
	})
}

// With the default two retries, one and then two minutes apart
// (lambda-api.md 8.6), the record goes out after the third failed attempt
// with count 3 (lambda-destinations-layers.md 3.2: test_lambda_destinations.snapshot.json:L376),
// every attempt under the Invoke's request id (finding 4).
func TestAsyncRetriesExhaustedCountsEveryAttempt(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createFunction("fn")
		call(h.harness, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{
			FunctionName: "fn", DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(failureQueue)}},
		})
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		h.advance(time.Minute)
		if n := len(h.sink.lambda()); n != 0 {
			t.Fatalf("%d deliveries after the second attempt, want none yet", n)
		}
		h.advance(2 * time.Minute)
		d := h.onlyDelivery()
		r := recordOf(t, d)
		calls := s.invoked()
		if len(calls) != 3 || r.RequestContext.Condition != "RetriesExhausted" || r.RequestContext.ApproximateInvokeCount != 3 || r.RequestContext.RequestID != string(calls[0].RequestID) {
			t.Errorf("%d attempts, requestContext %+v; want 3 and RetriesExhausted, count 3, request %s", len(calls), r.RequestContext, calls[0].RequestID)
		}
		if r.Timestamp != timestampOf(d) {
			t.Errorf("timestamp = %s, want the settle instant %s", r.Timestamp, timestampOf(d))
		}
	})
}

// EventAgeExceeded is decided when a failed attempt's next retry would
// fall past the event's maximum age, without waiting: with 60 s the record
// goes out after the first attempt with count 1, with 90 s after the
// second with count 2, each with the last attempt's response half
// (lambda-destinations-layers.md finding 3, 3.3:
// test_lambda_destinations.snapshot.json:L407-L486, validated 2025-11-27).
func TestEventAgeIsDecidedBeforeTheNextAttempt(t *testing.T) {
	for _, c := range []struct {
		age      int32
		attempts int
	}{{60, 1}, {90, 2}} {
		s := newScript()
		runDestinations(t, s.handle, func(h *destHarness) {
			t := h.T
			h.createFunction("fn")
			call(h.harness, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{
				FunctionName: "fn", MaximumRetryAttempts: new(int32(2)), MaximumEventAgeInSeconds: new(c.age),
				DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(failureQueue)}},
			})
			t0 := h.Clock.Now()
			h.invoke(fnEvent(`{"action":"fail"}`))
			h.settle()
			for range c.attempts - 1 {
				h.advance(time.Minute)
			}
			d := h.onlyDelivery()
			r := recordOf(t, d)
			if n := len(s.invoked()); n != c.attempts || r.RequestContext.Condition != "EventAgeExceeded" || r.RequestContext.ApproximateInvokeCount != c.attempts {
				t.Errorf("age %d s: %d attempts, requestContext %+v; want %d and EventAgeExceeded", c.age, n, r.RequestContext, c.attempts)
			}
			if want := t0.Add(time.Duration(c.attempts-1) * time.Minute); d.Accepted != want {
				t.Errorf("age %d s: record accepted at %v, want at the last attempt, %v", c.age, d.Accepted, want)
			}
			if r.ResponseContext == nil || r.ResponseContext.FunctionError == nil || *r.ResponseContext.FunctionError != "Unhandled" || len(r.ResponsePayload) == 0 {
				t.Errorf("age %d s: response half %+v %s, want the last attempt's function error", c.age, r.ResponseContext, r.ResponsePayload)
			}
		})
	}
}

// invokeRequestID is the request id of the last Invoke call.
func (h *destHarness) invokeRequestID() string {
	h.Helper()
	invokes := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: lambdaapi.OpInvoke.Descriptor().ID, Limit: 100}).Events
	if len(invokes) == 0 {
		h.Fatal("no Invoke call was journaled")
	}
	api, ok := invokes[len(invokes)-1].Detail.(journal.APICall)
	if !ok {
		h.Fatalf("Invoke event is %T", invokes[len(invokes)-1].Detail)
	}
	return string(api.RequestID)
}

// With reserved concurrency 0 the event is never run: the record has
// condition ZeroReservedConcurrency, count 0 and no response half at all
// (lambda-destinations-layers.md finding 5: test_lambda.snapshot.json:L2968-L2978).
func TestZeroReservedConcurrencySendsARecordThatNeverRan(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createFunction("fn")
		h.configure("", 2, "", failureQueue)
		call(h.harness, lambdaapi.OpPutFunctionConcurrency, &lambdaapi.PutFunctionConcurrencyInput{FunctionName: "fn", ReservedConcurrentExecutions: 0})
		h.invoke(fnEvent(`{}`))
		h.settle()
		d := h.onlyDelivery()
		queueTarget(t, d, failureQueue)
		raw := carried(t, d)
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatal(err)
		}
		if _, ok := keys["responseContext"]; ok {
			t.Errorf("record %s has a responseContext, want none", raw)
		}
		if _, ok := keys["responsePayload"]; ok {
			t.Errorf("record %s has a responsePayload, want none", raw)
		}
		r := recordOf(t, d)
		if rc := r.RequestContext; rc.Condition != "ZeroReservedConcurrency" || rc.ApproximateInvokeCount != 0 || rc.RequestID != h.invokeRequestID() || rc.FunctionArn != fnUnqualified+":$LATEST" {
			t.Errorf("requestContext = %+v, want ZeroReservedConcurrency, count 0, the Invoke's request id", rc)
		}
		if string(r.RequestPayload) != `{}` || len(s.invoked()) != 0 {
			t.Errorf("requestPayload %s with %d attempts, want {} and none", r.RequestPayload, len(s.invoked()))
		}
	})
}

// createWithDLQ creates fn with dlqA as its dead-letter queue.
func (h *destHarness) createWithDLQ() {
	h.Helper()
	in := zipFunction(h, "fn")
	in.DeadLetterConfig = &lambdaapi.DeadLetterConfig{TargetArn: new(dlqA)}
	call(h.harness, lambdaapi.OpCreateFunction, in)
}

// deadLetterAttributes is the dead letter's attributes by name, as
// DataType and value.
func deadLetterAttributes(attrs []delivery.MessageAttribute) map[string]string {
	out := map[string]string{}
	for _, a := range attrs {
		switch v := a.Value.(type) {
		case delivery.StringAttribute:
			out[a.Name] = "String " + v.Value
		case delivery.NumberAttribute:
			out[a.Name] = "Number " + v.Value
		case delivery.BinaryAttribute:
			out[a.Name] = "Binary " + string(v.Value)
		}
	}
	return out
}

// A failed event goes to the function's dead-letter queue as sent, byte for
// byte, with ErrorCode Number "200", ErrorMessage the error's errorMessage
// and RequestID the request id (lambda-destinations-layers.md finding 1,
// 5.2: test_lambda_destinations.snapshot.json:L243-L273).
func TestDeadLetterQueueReceivesTheEventAsSent(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createWithDLQ()
		call(h.harness, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", MaximumRetryAttempts: new(int32(0))})
		const event = `{"action": "fail",  "raise_error": 1}`
		h.invoke(fnEvent(event))
		h.settle()
		d := h.onlyDelivery()
		h.fromFn(d, delivery.DeadLetter)
		q := queueTarget(t, d, dlqA)
		if q.Body != event {
			t.Errorf("dead letter body = %q, want the event as sent, %q", q.Body, event)
		}
		want := map[string]string{"ErrorCode": "Number 200", "ErrorMessage": "String boom", "RequestID": "String " + string(s.invoked()[0].RequestID)}
		if got := deadLetterAttributes(q.Attributes); len(q.Attributes) != len(want) || !equalMaps(got, want) {
			t.Errorf("dead letter attributes = %v, want %v", got, want)
		}
	})
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// Destinations and a dead-letter queue are used together: a failed event
// goes to both (lambda-destinations-layers.md finding 7, 5.3).
func TestFailedEventReachesBothTheDLQAndOnFailure(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createWithDLQ()
		h.configure("", 0, "", failureQueue)
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		got := h.sink.lambda()
		if len(got) != 2 {
			t.Fatalf("deliveries = %+v, want a record and a dead letter", got)
		}
		carries := map[delivery.LambdaPayload]string{}
		for _, d := range got {
			if d.Request.Sender == nil {
				t.Fatalf("delivery %+v has no sender", d.Request)
			}
			q, ok := d.Request.Target.(delivery.SQSQueue)
			if !ok {
				t.Fatalf("delivery target %#v, want a queue", d.Request.Target)
			}
			carries[d.Request.Sender.Carries] = q.ARN.String()
		}
		if carries[delivery.DestinationRecord] != failureQueue || carries[delivery.DeadLetter] != dlqA {
			t.Errorf("deliveries carry %v, want the record to %s and the dead letter to %s", carries, failureQueue, dlqA)
		}
	})
}

// A published version uses the function's dead-letter queue, $LATEST's,
// not one of its own (lambda-destinations-layers.md 5.1, from
// https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html).
func TestPublishedVersionsUseTheFunctionsDeadLetterQueue(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createWithDLQ()
		call(h.harness, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		call(h.harness, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{
			FunctionName: "fn", DeadLetterConfig: &lambdaapi.DeadLetterConfig{TargetArn: new(dlqB)},
		})
		h.settle()
		call(h.harness, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", Qualifier: new("1"), MaximumRetryAttempts: new(int32(0))})
		in := fnEvent(`{"action":"fail"}`)
		in.Qualifier = new("1")
		h.invoke(in)
		h.settle()
		queueTarget(t, h.onlyDelivery(), dlqB)
	})
}

// Which configuration an alias invoke uses follows the switch
// lambda.async.qualifier-config, and the record's functionArn the switch
// lambda.destination.record-function-arn (lambda-destinations-layers.md
// 2.4, 3.2, 11.2). executedVersion is the version the alias resolved to
// (https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html, X-Amz-Executed-Version).
func TestAliasInvokesFollowTheQualifierSwitches(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		if v := h.switchValue("lambda.async.qualifier-config"); v != "invoked-qualifier-only" {
			t.Fatalf("lambda.async.qualifier-config = %q, a value this test does not know", v)
		}
		var functionArn string
		switch v := h.switchValue("lambda.destination.record-function-arn"); v {
		case "invoked-qualifier":
			functionArn = fnUnqualified + ":live"
		default:
			t.Fatalf("lambda.destination.record-function-arn = %q, a value this test does not know", v)
		}
		h.createFunction("fn")
		call(h.harness, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		call(h.harness, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: "fn", Name: "live", FunctionVersion: "1"})
		h.configure("", 0, successQueue, "")
		live := fnEvent(`{"n":1}`)
		live.Qualifier = new("live")
		h.invoke(live)
		h.settle()
		if got := h.sink.lambda(); len(got) != 0 {
			t.Fatalf("an alias without its own configuration delivered %+v, want nothing under invoked-qualifier-only", got)
		}
		h.configure("live", 0, failureQueue, "")
		h.invoke(live)
		h.settle()
		d := h.onlyDelivery()
		queueTarget(t, d, failureQueue)
		r := recordOf(t, d)
		if r.RequestContext.FunctionArn != functionArn || r.ResponseContext == nil || r.ResponseContext.ExecutedVersion != "1" {
			t.Errorf("record functionArn %s, responseContext %+v; want %s and executedVersion 1", r.RequestContext.FunctionArn, r.ResponseContext, functionArn)
		}
	})
}

// Only asynchronous invocations produce records
// (lambda-destinations-layers.md 2.4, 11.8).
func TestSynchronousInvokeSendsNoRecord(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createWithDLQ()
		h.configure("", 0, successQueue, failureQueue)
		h.invoke(invokeInput("fn", `{"n":1}`))
		if out := h.invoke(invokeInput("fn", `{"action":"fail"}`)); out.FunctionError == nil {
			t.Fatal("the failing synchronous invoke reported no function error")
		}
		h.settle()
		if got := h.sink.lambda(); len(got) != 0 {
			t.Errorf("synchronous invokes delivered %+v, want nothing", got)
		}
	})
}

// A success goes to the on-success destination and a failure to the
// on-failure one; a side with no destination gets nothing
// (https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html).
func TestOnlyTheConfiguredSideReceives(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createFunction("fn")
		h.configure("", 0, "", failureQueue)
		h.invoke(fnEvent(`{"n":1}`))
		h.settle()
		if got := h.sink.lambda(); len(got) != 0 {
			t.Fatalf("a success with only OnFailure delivered %+v", got)
		}
		h.configure("", 0, successQueue, "")
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		if got := h.sink.lambda(); len(got) != 0 {
			t.Errorf("a failure with only OnSuccess delivered %+v", got)
		}
	})
}

// keyShape is the documented S3 key with the instant's UTC date parts and
// a lowercase UUID (lambda-destinations-layers.md 4.5; zone and form per
// 11.3.5 until LDL-07).
var keyShape = regexp.MustCompile(`^aws/lambda/async/fn/(\d{4}/\d{2}/\d{2})/(\d{4}-\d{2}-\d{2}T\d{2}\.\d{2}\.\d{2})-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Each destination kind gets the record its way: an SNS Message (4.2), a
// function invoke as the switch lambda.destination.lambda-invocation-type
// says (4.3), one EventBridge entry from source lambda with the bus then
// the qualified function as resources (4.4:
// test_lambda_destinations.snapshot.json:L576-L585), and an S3 object at the
// documented key for a failure (4.5) (lambda-destinations-layers.md).
func TestRecordsReachEveryDestinationKind(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		invocation, err := delivery.ParseInvocationType(h.switchValue("lambda.destination.lambda-invocation-type"))
		if err != nil {
			t.Fatal(err)
		}
		call(h.harness, lambdaapi.OpCreateFunction, zipFunction(h, "auditor"))
		h.createFunction("fn")
		for _, c := range []struct {
			dest  string
			check func(d kernel.Delivery)
		}{
			{alertsTopic, func(d kernel.Delivery) {
				topic, ok := d.Request.Target.(delivery.SNSTopic)
				if d.Request.Mechanism != delivery.LambdaToSNS || !ok || topic.ARN.String() != alertsTopic || topic.Subject != "" || len(topic.Attributes) != 0 {
					t.Errorf("SNS delivery = %s %#v, want lambda-to-sns of the record alone", d.Request.Mechanism, d.Request.Target)
				}
			}},
			{auditorARN, func(d kernel.Delivery) {
				fn, ok := d.Request.Target.(delivery.LambdaFunction)
				if d.Request.Mechanism != delivery.LambdaToLambda || !ok || fn.ARN.String() != auditorARN || fn.Qualifier != "" || fn.Invocation != invocation {
					t.Errorf("Lambda delivery = %s %#v, want lambda-to-lambda of auditor as %s", d.Request.Mechanism, d.Request.Target, invocation)
				}
			}},
			{auditBus, func(d kernel.Delivery) {
				bus, ok := d.Request.Target.(delivery.EventBus)
				if d.Request.Mechanism != delivery.LambdaToEvents || !ok || bus.ARN.String() != auditBus || len(bus.Entries) != 1 {
					t.Fatalf("EventBridge delivery = %s %#v, want lambda-to-events of one entry to audit", d.Request.Mechanism, d.Request.Target)
				}
				e := bus.Entries[0]
				if e.Source != "lambda" || e.DetailType != "Lambda Function Invocation Result - Success" ||
					len(e.Resources) != 2 || e.Resources[0] != auditBus || e.Resources[1] != fnUnqualified+":$LATEST" || e.Forwarded != nil {
					t.Errorf("entry = %+v, want source lambda, the Success detail-type and resources [bus, function:$LATEST]", e)
				}
			}},
		} {
			h.configure("", 0, c.dest, "")
			before := len(h.sink.lambda())
			h.invoke(fnEvent(`{"n":1}`))
			h.settle()
			got := h.sink.lambda()
			if len(got) != before+1 {
				t.Fatalf("to %s: %d deliveries, want one more", c.dest, len(got)-before)
			}
			d := got[len(got)-1]
			h.fromFn(d, delivery.DestinationRecord)
			if r := recordOf(t, d); r.RequestContext.Condition != "Success" {
				t.Errorf("to %s: condition %s, want Success", c.dest, r.RequestContext.Condition)
			}
			c.check(d)
		}

		h.configure("", 0, "", auditBucket)
		before := len(h.sink.lambda())
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		got := h.sink.lambda()
		if len(got) != before+1 {
			t.Fatalf("to the bucket: %d deliveries, want one more", len(got)-before)
		}
		d := got[len(got)-1]
		h.fromFn(d, delivery.DestinationRecord)
		obj, ok := d.Request.Target.(delivery.S3Object)
		if d.Request.Mechanism != delivery.LambdaToS3 || !ok || obj.Bucket != "audit-records" {
			t.Fatalf("S3 delivery = %s %#v, want lambda-to-s3 into audit-records", d.Request.Mechanism, d.Request.Target)
		}
		m := keyShape.FindStringSubmatch(obj.Key)
		at := d.Accepted.Wire().UTC()
		if len(m) != 3 || m[1] != at.Format("2006/01/02") || m[2] != at.Format("2006-01-02T15.04.05") {
			t.Errorf("object key = %q, want aws/lambda/async/fn/%s/%s-<uuid>", obj.Key, at.Format("2006/01/02"), at.Format("2006-01-02T15.04.05"))
		}
		if r := recordOf(t, d); r.RequestContext.Condition != "RetriesExhausted" {
			t.Errorf("S3 record condition = %s, want RetriesExhausted", r.RequestContext.Condition)
		}
	})
}

// A response that is not JSON leaves responsePayload out of the record,
// which still goes out, and the journal says why: how AWS renders it is
// unrecorded (lambda-destinations-layers.md 11.4, LDL-02).
func TestNonJSONResponseIsLeftOutAndAdvised(t *testing.T) {
	plain := func(_ context.Context, _ runtimeapi.Invocation) (runtimeapi.Outcome, error) {
		return runtimeapi.Success{Payload: []byte("plain text, not JSON")}, nil
	}
	runDestinations(t, plain, func(h *destHarness) {
		t := h.T
		h.createFunction("fn")
		h.configure("", 0, successQueue, "")
		h.invoke(fnEvent(`{}`))
		h.settle()
		d := h.onlyDelivery()
		r := recordOf(t, d)
		if r.ResponseContext == nil || r.ResponsePayload != nil {
			t.Errorf("record %s, want a responseContext and no responsePayload", carried(t, d))
		}
		advisories := h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory, Limit: 10}).Events
		found := false
		for _, e := range advisories {
			if a, ok := e.Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryRecordResponseOmitted {
				found = true
				if a.Resource.String() != fnUnqualified+":$LATEST" || !strings.Contains(a.Text, "is not JSON") {
					t.Errorf("advisory = %+v, want one naming %s:$LATEST and the response that is not JSON", a, fnUnqualified)
				}
			}
		}
		if !found {
			t.Errorf("advisories = %+v, want a record-response-omitted one", advisories)
		}
	})
}

// invokeWithTrace runs in as a client request carrying the X-Amzn-Trace-Id
// header trace.
func (h *destHarness) invokeWithTrace(in *lambdaapi.InvokeInput, trace string) {
	h.Helper()
	c := h.Kernel.NewCall(kernel.ExternalCall{
		RequestID: h.Kernel.IDs().RequestID(),
		Operation: lambdaapi.OpInvoke.Descriptor().ID,
		Scope:     h.Scope,
		Principal: identity.Root{Account: h.Scope.Account},
		Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
		Admitted:  h.Clock.Now(),
		Endpoint:  h.Endpoint,
		Header:    http.Header{"X-Amzn-Trace-Id": {trace}},
	})
	if _, err := kerneltest.Dispatch[lambdaapi.InvokeInput, lambdaapi.InvokeOutput](h.Context(), h.K, c, in); err != nil {
		h.Fatalf("Invoke: %s %s", err.ID.Shape.Name(), err.Message)
	}
}

// An SQS destination's message carries AWSTraceHeader
// (lambda-destinations-layers.md 4.1: test_lambda_destinations.snapshot.json:L362-L368,
// value redacted): the Invoke call's trace header (11.3.5), kept by the
// queued event until it settles.
func TestTheInvokesTraceHeaderReachesTheRecord(t *testing.T) {
	const trace = "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1"
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createFunction("fn")
		h.configure("", 1, "", failureQueue)
		h.invokeWithTrace(fnEvent(`{"action":"fail"}`), trace)
		h.settle()
		h.advance(time.Minute)
		if q := queueTarget(t, h.onlyDelivery(), failureQueue); q.TraceHeader != trace {
			t.Errorf("TraceHeader = %q, want the Invoke's %q", q.TraceHeader, trace)
		}
	})
}

// An event whose function is deleted while it waits for a retry sends no
// record and no dead letter: the configuration naming its destinations went
// with the function (lambda-destinations-layers.md 11.3.4).
func TestFunctionDeletedWhileQueuedSendsNothing(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createWithDLQ()
		h.configure("", 2, successQueue, failureQueue)
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		call(h.harness, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn"})
		h.advance(10 * time.Minute)
		if got := h.sink.lambda(); len(got) != 0 {
			t.Errorf("deliveries = %+v, want none", got)
		}
	})
}

// A queued event keeps what its record needs across a snapshot: after a
// load, its last failure sends RetriesExhausted with every attempt counted
// and the Invoke's request id.
func TestQueuedEventKeepsItsRecordAcrossASnapshot(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createFunction("fn")
		h.configure("", 1, "", failureQueue)
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		q := h.x.AsyncQueue()
		release := h.Kernel.Gate().Exclusive()
		sections, err := q.Export()
		release()
		if err != nil {
			t.Fatal(err)
		}
		if err := h.x.Drain(h.Context(), kernel.ResetScope{}); err != nil {
			t.Fatal(err)
		}
		h.Kernel.Timers().Scheduler().Clear()
		release = h.Kernel.Gate().Exclusive()
		err = q.Import(sections)
		release()
		if err != nil {
			t.Fatal(err)
		}
		q.Rearm(h.Clock.Now())
		h.x.RearmPollers()
		h.advance(time.Minute)
		r := recordOf(t, h.onlyDelivery())
		calls := s.invoked()
		if len(calls) != 2 || r.RequestContext.Condition != "RetriesExhausted" || r.RequestContext.ApproximateInvokeCount != 2 || r.RequestContext.RequestID != string(calls[0].RequestID) {
			t.Errorf("%d attempts, requestContext %+v; want 2 and RetriesExhausted, count 2, request %s", len(calls), r.RequestContext, calls[0].RequestID)
		}
	})
}

// The queued event gained its last response and trace header, so the
// lambda-async section's version is 2: a version 1 section an older binary
// wrote is refused naming both versions rather than migrated (ADR 0009).
func TestAsyncQueueSnapshotRefusesAVersion1Section(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createFunction("fn")
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		q := h.x.AsyncQueue()
		release := h.Kernel.Gate().Exclusive()
		sections, err := q.Export()
		release()
		if err != nil {
			t.Fatal(err)
		}
		if len(sections) == 0 {
			t.Fatal("Export of a queued event wrote no section")
		}
		for i := range sections {
			sections[i].Version = 1
		}
		release = h.Kernel.Gate().Exclusive()
		err = q.Import(sections)
		release()
		vm, ok := errors.AsType[*store.VersionMismatchError](err)
		if !ok {
			t.Fatalf("Import(a version 1 section) = %v, want a *store.VersionMismatchError", err)
		}
		if vm.Section != 1 || vm.Running != 2 || q.SchemaVersion() != 2 {
			t.Errorf("mismatch %+v, schema version %d; want section 1 refused by running 2", vm, q.SchemaVersion())
		}
	})
}
