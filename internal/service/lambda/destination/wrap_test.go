package destination_test

import (
	"bytes"
	"crypto/md5" //nolint:gosec // the recorded MD5OfBody is the MD5 of the body; the test recomputes it
	"encoding/hex"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"uuid"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/destination"
)

func mustARN(tb testing.TB, s string) aws.ARN {
	tb.Helper()
	a, err := aws.ParseARN(s)
	if err != nil {
		tb.Fatal(err)
	}
	return a
}

// sender is destination-fn's: its unqualified ARN, the record's qualified
// functionArn, its execution role and the Invoke call's trace header.
func sender(tb testing.TB) destination.Sender {
	tb.Helper()
	return destination.Sender{
		Function:  mustARN(tb, "arn:aws:lambda:us-east-1:111111111111:function:destination-fn"),
		Qualified: goldenQualified,
		Role:      mustARN(tb, "arn:aws:iam::111111111111:role/service-role/destination-fn-role"),
		Trace:     "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1",
	}
}

// Each destination ARN form Lambda delivers to
// (lambda-destinations-layers.md 2.2) parses to its destination: a
// function ARN's qualifier, when it has one, is kept apart from the
// unqualified ARN.
func TestParseReadsEveryDestinationForm(t *testing.T) {
	cases := []struct {
		arn  string
		want destination.Destination
	}{
		{"arn:aws:sqs:us-east-1:111111111111:orders", destination.Queue{ARN: mustARN(t, "arn:aws:sqs:us-east-1:111111111111:orders")}},
		{"arn:aws:sqs:us-east-1:111111111111:orders.fifo", destination.Queue{ARN: mustARN(t, "arn:aws:sqs:us-east-1:111111111111:orders.fifo")}},
		{"arn:aws-cn:sqs:cn-north-1:111111111111:orders", destination.Queue{ARN: mustARN(t, "arn:aws-cn:sqs:cn-north-1:111111111111:orders")}},
		{"arn:aws:sns:us-east-1:111111111111:alerts", destination.Topic{ARN: mustARN(t, "arn:aws:sns:us-east-1:111111111111:alerts")}},
		{"arn:aws:sns:us-east-1:111111111111:alerts.fifo", destination.Topic{ARN: mustARN(t, "arn:aws:sns:us-east-1:111111111111:alerts.fifo")}},
		{"arn:aws:lambda:us-east-1:111111111111:function:worker", destination.Function{ARN: mustARN(t, "arn:aws:lambda:us-east-1:111111111111:function:worker")}},
		{"arn:aws:lambda:us-east-1:111111111111:function:worker:live", destination.Function{ARN: mustARN(t, "arn:aws:lambda:us-east-1:111111111111:function:worker"), Qualifier: "live"}},
		{"arn:aws:lambda:us-east-1:111111111111:function:worker:3", destination.Function{ARN: mustARN(t, "arn:aws:lambda:us-east-1:111111111111:function:worker"), Qualifier: "3"}},
		{"arn:aws:lambda:us-east-1:111111111111:function:worker:$LATEST", destination.Function{ARN: mustARN(t, "arn:aws:lambda:us-east-1:111111111111:function:worker"), Qualifier: "$LATEST"}},
		{"arn:aws:events:us-east-1:111111111111:event-bus/default", destination.Bus{ARN: mustARN(t, "arn:aws:events:us-east-1:111111111111:event-bus/default")}},
		{"arn:aws:events:us-east-1:111111111111:event-bus/orders", destination.Bus{ARN: mustARN(t, "arn:aws:events:us-east-1:111111111111:event-bus/orders")}},
		{"arn:aws:s3:::audit-records", destination.Bucket{Name: "audit-records"}},
	}
	for _, c := range cases {
		got, err := destination.Parse(c.arn)
		if err != nil {
			t.Errorf("Parse(%q) = %v", c.arn, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) = %#v, want %#v", c.arn, got, c.want)
		}
	}
}

// Anything else names no destination an asynchronous invocation delivers
// to: the kafka:// branch of DestinationArn serves Kafka event source
// mappings only (lambda-destinations-layers.md 2.2), and other services,
// other resources of these services and malformed text are not
// destinations.
func TestParseRefusesWhatNamesNoDestination(t *testing.T) {
	for _, arn := range []string{
		"",
		"kafka://broker.example.test:9092",
		"orders",
		"arn:aws:dynamodb:us-east-1:111111111111:table/orders",
		"arn:aws:kinesis:us-east-1:111111111111:stream/orders",
		"arn:aws:events:us-east-1:111111111111:rule/nightly",
		"arn:aws:events:us-east-1:111111111111:event-bus/",
		"arn:aws:lambda:us-east-1:111111111111:layer:shared:1",
		"arn:aws:lambda:us-east-1:111111111111:function:",
		"arn:aws:s3:::audit-records/prefix/key",
		"arn:aws:s3:::",
		"arn:aws:sqs:us-east-1:111111111111:",
	} {
		_, err := destination.Parse(arn)
		pe, ok := errors.AsType[*destination.ParseError](err)
		if !ok {
			t.Errorf("Parse(%q) = %v, want a *ParseError", arn, err)
			continue
		}
		if pe.ARN != arn || pe.Reason == "" {
			t.Errorf("Parse(%q) = %+v, want the ARN and a reason", arn, pe)
		}
	}
}

// FIFO queue and topic names end in .fifo
// (https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/FIFO-queues-understanding-logic.html,
// https://docs.aws.amazon.com/sns/latest/dg/fifo-topic-code-examples.html);
// Lambda does not deliver to either (lambda-destinations-layers.md 2.2, 4.6).
func TestFIFOQueuesAndTopicsAreKnownByName(t *testing.T) {
	for arn, want := range map[string]bool{
		"arn:aws:sqs:us-east-1:111111111111:orders.fifo": true,
		"arn:aws:sqs:us-east-1:111111111111:orders":      false,
		"arn:aws:sqs:us-east-1:111111111111:fifo-orders": false,
	} {
		if got := (destination.Queue{ARN: mustARN(t, arn)}).FIFO(); got != want {
			t.Errorf("Queue %s FIFO() = %t, want %t", arn, got, want)
		}
	}
	for arn, want := range map[string]bool{
		"arn:aws:sns:us-east-1:111111111111:alerts.fifo": true,
		"arn:aws:sns:us-east-1:111111111111:alerts":      false,
	} {
		if got := (destination.Topic{ARN: mustARN(t, arn)}).FIFO(); got != want {
			t.Errorf("Topic %s FIFO() = %t, want %t", arn, got, want)
		}
	}
}

// The S3 key follows the documented convention
// aws/lambda/async/<function-name>/YYYY/MM/DD/YYYY-MM-DDTHH.MM.SS-<Random UUID>
// (lambda-destinations-layers.md 4.5). The zone and UUID form are
// unrecorded (LDL-07): understudy writes the instant in UTC and the UUID
// in RFC 9562's lowercase form (11.3.5).
func TestObjectKeyFollowsTheDocumentedConvention(t *testing.T) {
	id := uuid.MustParse("0F0E0D0C-0B0A-4908-8706-050403020100")
	cases := []struct {
		at   time.Time
		want string
	}{
		{goldenAt, "aws/lambda/async/destination-fn/2026/10/26/2026-10-26T09.05.07-0f0e0d0c-0b0a-4908-8706-050403020100"},
		// 05:00 on the 27th at UTC+5:30 is 23:30 on the 26th in UTC.
		{time.Date(2026, 10, 27, 5, 0, 0, 0, time.FixedZone("UTC+5:30", 5*3600+1800)), "aws/lambda/async/destination-fn/2026/10/26/2026-10-26T23.30.00-0f0e0d0c-0b0a-4908-8706-050403020100"},
	}
	for _, c := range cases {
		if got := destination.ObjectKey("destination-fn", c.at, id); got != c.want {
			t.Errorf("ObjectKey(destination-fn, %v) = %q, want %q", c.at, got, c.want)
		}
	}
}

// For any function name, instant and UUID, the key's date parts are the
// instant's in UTC, the same in the path and in the name, and the key ends
// with the UUID (lambda-destinations-layers.md 4.5, 11.3.5).
func TestObjectKeyProperties(t *testing.T) {
	shape := regexp.MustCompile(`^aws/lambda/async/([A-Za-z0-9_-]+)/(\d{4})/(\d{2})/(\d{2})/(\d{4})-(\d{2})-(\d{2})T(\d{2})\.(\d{2})\.(\d{2})-([0-9a-f-]{36})$`)
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.StringMatching(`[A-Za-z0-9_-]{1,64}`).Draw(t, "name")
		at := time.Unix(rapid.Int64Range(0, 4_102_444_800).Draw(t, "seconds"), rapid.Int64Range(0, 999_999_999).Draw(t, "nanos")).
			In(rapid.SampledFrom([]*time.Location{time.UTC, time.FixedZone("UTC-11", -11*3600), time.FixedZone("UTC+14", 14*3600)}).Draw(t, "zone"))
		var id uuid.UUID
		copy(id[:], rapid.SliceOfN(rapid.Byte(), 16, 16).Draw(t, "uuid"))
		key := destination.ObjectKey(name, at, id)
		m := shape.FindStringSubmatch(key)
		if m == nil {
			t.Fatalf("ObjectKey = %q, not the documented shape", key)
		}
		u := at.UTC()
		if m[1] != name || m[2] != m[5] || m[3] != m[6] || m[4] != m[7] ||
			m[2]+m[3]+m[4]+m[8]+m[9]+m[10] != u.Format("20060102150405") || m[11] != id.String() {
			t.Fatalf("ObjectKey(%q, %v, %s) = %q, want %s's UTC date parts and the UUID", name, at, id, key, at)
		}
	})
}

// wrapped is To's request with what every destination shares checked:
// it comes from the unqualified function, has no dead-letter queue, and
// runs as the execution role carrying a destination record
// (lambda-destinations-layers.md 2.5, 11.3.2).
func wrapped(t *testing.T, d destination.Destination, o destination.Outgoing) delivery.Request {
	t.Helper()
	s := sender(t)
	req, err := destination.To(d, s, o)
	if err != nil {
		t.Fatalf("To(%#v) = %v", d, err)
	}
	if req.Source != s.Function || req.DLQ != nil {
		t.Errorf("To(%#v): Source %s, DLQ %v; want the unqualified function and no DLQ", d, req.Source, req.DLQ)
	}
	if req.Sender == nil || *req.Sender != (delivery.LambdaSender{Role: s.Role, Carries: delivery.DestinationRecord}) {
		t.Errorf("To(%#v): Sender = %+v, want the execution role carrying a destination record", d, req.Sender)
	}
	return req
}

// The record is the SQS message body, with no message attributes, and the
// message carries an AWSTraceHeader (lambda-destinations-layers.md 4.1:
// test_lambda_destinations.snapshot.json:L87-L117 and L362-L368, whose
// value is redacted, so understudy sends the Invoke call's own, 11.3.5).
func TestToQueueSendsTheRecordAsTheBody(t *testing.T) {
	s := sender(t)
	queue := mustARN(t, "arn:aws:sqs:us-east-1:111111111111:orders")
	body := golden(t, "success-sqs")
	req := wrapped(t, destination.Queue{ARN: queue}, destination.Outgoing{Condition: destination.Success, Body: body})
	want := delivery.SQSQueue{ARN: queue, Body: string(body), TraceHeader: s.Trace}
	if req.Mechanism != delivery.LambdaToSQS || !reflect.DeepEqual(req.Target, want) {
		t.Errorf("To queue = %s %#v, want lambda-to-sqs %#v", req.Mechanism, req.Target, want)
	}
}

// The record is the published Message (lambda-destinations-layers.md 4.2).
// A subject and attributes are unrecorded (LDL-05), so none is sent
// (11.3.5).
func TestToTopicPublishesTheRecord(t *testing.T) {
	topic := mustARN(t, "arn:aws:sns:us-east-1:111111111111:alerts")
	body := golden(t, "retries-exhausted")
	req := wrapped(t, destination.Topic{ARN: topic}, destination.Outgoing{Condition: destination.RetriesExhausted, Body: body})
	want := delivery.SNSTopic{ARN: topic, Message: string(body)}
	if req.Mechanism != delivery.LambdaToSNS || !reflect.DeepEqual(req.Target, want) {
		t.Errorf("To topic = %s %#v, want lambda-to-sns %#v", req.Mechanism, req.Target, want)
	}
}

// The record is the payload of an invoke of the destination function and
// its qualifier, when the configured ARN has one
// (lambda-destinations-layers.md 4.3). The invocation type is the switch
// lambda.destination.lambda-invocation-type's, checked by
// TestToFollowsTheLambdaSwitches.
func TestToFunctionInvokesWithTheRecord(t *testing.T) {
	worker := mustARN(t, "arn:aws:lambda:us-east-1:111111111111:function:worker")
	body := golden(t, "success-sqs")
	for _, qualifier := range []string{"", "live", "3"} {
		req := wrapped(t, destination.Function{ARN: worker, Qualifier: qualifier}, destination.Outgoing{Condition: destination.Success, Body: body})
		got, ok := req.Target.(delivery.LambdaFunction)
		if req.Mechanism != delivery.LambdaToLambda || !ok || got.ARN != worker || got.Qualifier != qualifier || !bytes.Equal(got.Event, body) {
			t.Errorf("To function with qualifier %q = %s %#v, want lambda-to-lambda of worker:%s with the record", qualifier, req.Mechanism, req.Target, qualifier)
		}
	}
}

// One PutEvents entry: source "lambda", the condition's detail-type, the
// record as the detail, and the resources the bus ARN then the qualified
// function ARN (lambda-destinations-layers.md 4.4:
// test_lambda_destinations.snapshot.json:L576-L585, validated 2025-11-24;
// the order is the switch lambda.destination.eventbridge-resources-order,
// labelled third-party-recorded).
func TestToBusPutsOneEvent(t *testing.T) {
	s := sender(t)
	bus := mustARN(t, "arn:aws:events:us-east-1:111111111111:event-bus/orders")
	for cond, detailType := range map[destination.Condition]string{
		destination.Success:                 "Lambda Function Invocation Result - Success",
		destination.RetriesExhausted:        "Lambda Function Invocation Result - Failure",
		destination.EventAgeExceeded:        "Lambda Function Invocation Result - Failure",
		destination.ZeroReservedConcurrency: "Lambda Function Invocation Result - Failure",
	} {
		body := golden(t, "zero-reserved-concurrency")
		req := wrapped(t, destination.Bus{ARN: bus}, destination.Outgoing{Condition: cond, Body: body})
		want := delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{{
			Source: "lambda", DetailType: detailType, Detail: string(body), Resources: []string{bus.String(), s.Qualified},
		}}}
		if req.Mechanism != delivery.LambdaToEvents || !reflect.DeepEqual(req.Target, want) {
			t.Errorf("To bus for %d = %s %#v, want lambda-to-events %#v", cond, req.Mechanism, req.Target, want)
		}
	}
}

// The record is an object at the key the caller computed
// (lambda-destinations-layers.md 4.5).
func TestToBucketPutsTheRecordAtTheKey(t *testing.T) {
	body := golden(t, "event-age-exceeded")
	key := "aws/lambda/async/destination-fn/2026/10/26/2026-10-26T09.05.07-0f0e0d0c-0b0a-4908-8706-050403020100"
	req := wrapped(t, destination.Bucket{Name: "audit-records"}, destination.Outgoing{Condition: destination.EventAgeExceeded, Body: body, ObjectKey: key})
	want := delivery.S3Object{Bucket: "audit-records", Key: key, Body: body}
	if req.Mechanism != delivery.LambdaToS3 || !reflect.DeepEqual(req.Target, want) {
		t.Errorf("To bucket = %s %#v, want lambda-to-s3 %#v", req.Mechanism, req.Target, want)
	}
}

// A FIFO queue or topic is wrapped like any other: the delivery, not the
// wrapping, fails it (lambda-destinations-layers.md 2.2, 11.3.6).
func TestToWrapsFIFODestinationsLikeAnyOther(t *testing.T) {
	body := golden(t, "success-sqs")
	queue := mustARN(t, "arn:aws:sqs:us-east-1:111111111111:orders.fifo")
	if req := wrapped(t, destination.Queue{ARN: queue}, destination.Outgoing{Condition: destination.Success, Body: body}); req.Mechanism != delivery.LambdaToSQS {
		t.Errorf("To a FIFO queue = %s, want lambda-to-sqs", req.Mechanism)
	}
	topic := mustARN(t, "arn:aws:sns:us-east-1:111111111111:alerts.fifo")
	if req := wrapped(t, destination.Topic{ARN: topic}, destination.Outgoing{Condition: destination.Success, Body: body}); req.Mechanism != delivery.LambdaToSNS {
		t.Errorf("To a FIFO topic = %s, want lambda-to-sns", req.Mechanism)
	}
}

// To refuses what Lambda never sends: a success to S3, which takes
// on-failure records only (lambda-destinations-layers.md 2.2), an S3
// record without its key or a key for any other destination, an
// undeclared condition and an empty record.
func TestToRefusesWhatLambdaNeverSends(t *testing.T) {
	s := sender(t)
	body := golden(t, "success-sqs")
	queue := destination.Queue{ARN: mustARN(t, "arn:aws:sqs:us-east-1:111111111111:orders")}
	bucket := destination.Bucket{Name: "audit-records"}
	const key = "aws/lambda/async/destination-fn/2026/10/26/2026-10-26T09.05.07-0f0e0d0c-0b0a-4908-8706-050403020100"
	cases := map[string]struct {
		d destination.Destination
		o destination.Outgoing
	}{
		"a success to S3":           {bucket, destination.Outgoing{Condition: destination.Success, Body: body, ObjectKey: key}},
		"an S3 record without key":  {bucket, destination.Outgoing{Condition: destination.RetriesExhausted, Body: body}},
		"a queue record with a key": {queue, destination.Outgoing{Condition: destination.Success, Body: body, ObjectKey: key}},
		"an undeclared condition":   {queue, destination.Outgoing{Condition: 0, Body: body}},
		"an empty record":           {queue, destination.Outgoing{Condition: destination.Success}},
	}
	for name, c := range cases {
		if _, err := destination.To(c.d, s, c.o); !isWrapError(err) {
			t.Errorf("%s: To = %v, want a *WrapError", name, err)
		}
	}
}

func isWrapError(err error) bool {
	we, ok := errors.AsType[*destination.WrapError](err)
	return ok && we.Reason != ""
}

// lambdaSwitches is the Lambda service's fidelity switches by name. A test
// of a behavior no AWS evidence fixes reads understudy's choice from its
// switch rather than repeating it.
func lambdaSwitches(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory("", 0)
		if err != nil {
			k.Fatal(err)
		}
		defer func() {
			if err := blobs.Close(); err != nil {
				k.Error(err)
			}
		}()
		deps := k.Deps(lambdaapi.Service())
		deps.Blob = blobs
		for _, e := range lambda.New(deps, lambda.Options{}).Switches() {
			out[e.Name] = e.Value
		}
	}, kerneltest.Services(lambdaapi.Service()))
	return out
}

// switchValue is the value of the Lambda switch name.
func switchValue(t *testing.T, switches map[string]string, name string) string {
	t.Helper()
	v, ok := switches[name]
	if !ok {
		t.Fatalf("the Lambda service has no switch %s (lambda-destinations-layers.md 11.2)", name)
	}
	return v
}

// The behaviors of To that rest on inference follow their switches
// (lambda-destinations-layers.md 11.2): lambda.destination.delivery-retry
// names the retry policy of every destination's delivery,
// lambda.destination.lambda-invocation-type how a destination function
// is invoked, and lambda.destination.sns-oversize whether a record over
// SNS's 256 KB limit goes out whole (and SNS refuses it) or loses its
// payload.
func TestToFollowsTheLambdaSwitches(t *testing.T) {
	switches := lambdaSwitches(t)
	var policy delivery.RetryPolicy
	switch v := switchValue(t, switches, "lambda.destination.delivery-retry"); v {
	case "none":
		policy = delivery.None{}
	default:
		t.Fatalf("lambda.destination.delivery-retry = %q, a value this test does not know", v)
	}
	invocation, err := delivery.ParseInvocationType(switchValue(t, switches, "lambda.destination.lambda-invocation-type"))
	if err != nil {
		t.Fatal(err)
	}
	body := golden(t, "success-sqs")
	o := destination.Outgoing{Condition: destination.RetriesExhausted, Body: body}
	for _, d := range []destination.Destination{
		destination.Queue{ARN: mustARN(t, "arn:aws:sqs:us-east-1:111111111111:orders")},
		destination.Topic{ARN: mustARN(t, "arn:aws:sns:us-east-1:111111111111:alerts")},
		destination.Function{ARN: mustARN(t, "arn:aws:lambda:us-east-1:111111111111:function:worker")},
		destination.Bus{ARN: mustARN(t, "arn:aws:events:us-east-1:111111111111:event-bus/default")},
		destination.Bucket{Name: "audit-records"},
	} {
		oo := o
		if _, ok := d.(destination.Bucket); ok {
			oo.ObjectKey = "aws/lambda/async/destination-fn/2026/10/26/2026-10-26T09.05.07-0f0e0d0c-0b0a-4908-8706-050403020100"
		}
		req := wrapped(t, d, oo)
		if !reflect.DeepEqual(req.Policy, policy) {
			t.Errorf("To %#v: Policy = %#v, want %#v (lambda.destination.delivery-retry)", d, req.Policy, policy)
		}
		if fn, ok := req.Target.(delivery.LambdaFunction); ok && fn.Invocation != invocation {
			t.Errorf("To a function: Invocation = %s, want %s (lambda.destination.lambda-invocation-type)", fn.Invocation, invocation)
		}
	}
	switch v := switchValue(t, switches, "lambda.destination.sns-oversize"); v {
	case "delivery-failure":
		// 300 KiB, over SNS's 256 KB (lambda-destinations-layers.md 4.2).
		big := append([]byte(`{"pad":"`), append([]byte(strings.Repeat("x", 300<<10)), `"}`...)...)
		topic := mustARN(t, "arn:aws:sns:us-east-1:111111111111:alerts")
		req := wrapped(t, destination.Topic{ARN: topic}, destination.Outgoing{Condition: destination.RetriesExhausted, Body: big})
		if got, ok := req.Target.(delivery.SNSTopic); !ok || got.Message != string(big) {
			t.Errorf("To a topic with a 300 KiB record changed the message; under delivery-failure it goes out whole")
		}
	default:
		t.Fatalf("lambda.destination.sns-oversize = %q, a value this test does not know", v)
	}
}

// pythonError is the recorded event and error of the DLQ test: the event
// as json.dumps sent it, with its space, whose MD5 is the recorded
// MD5OfBody (lambda-destinations-layers.md finding 1:
// test_lambda_destinations.snapshot.json:L249).
const (
	pythonEvent    = `{"raise_error": 1}`
	pythonEventMD5 = "ba08269cbd0ce515889bfba6593731e1"
)

// attributes is attrs by name.
func attributes(attrs []delivery.MessageAttribute) map[string]delivery.AttributeValue {
	out := map[string]delivery.AttributeValue{}
	for _, a := range attrs {
		out[a.Name] = a.Value
	}
	return out
}

// dead is DeadLetter's request with what every dead letter shares checked:
// it comes from the unqualified function, runs as the execution role
// carrying a dead letter, has no dead-letter queue of its own, and makes
// one attempt, since Lambda deletes an event its dead-letter queue does
// not take (lambda-destinations-layers.md 5.4).
func dead(t *testing.T, target string, cond destination.Condition, inv destination.Invocation) delivery.Request {
	t.Helper()
	s := sender(t)
	req, err := destination.DeadLetter(mustARN(t, target), s, goldenRequestID, cond, inv, []byte(pythonEvent))
	if err != nil {
		t.Fatalf("DeadLetter(%s) = %v", target, err)
	}
	if req.Source != s.Function || req.DLQ != nil || !reflect.DeepEqual(req.Policy, delivery.None{}) {
		t.Errorf("DeadLetter(%s): Source %s, DLQ %v, Policy %#v; want the unqualified function, no DLQ, one attempt", target, req.Source, req.DLQ, req.Policy)
	}
	if req.Sender == nil || *req.Sender != (delivery.LambdaSender{Role: s.Role, Carries: delivery.DeadLetter}) {
		t.Errorf("DeadLetter(%s): Sender = %+v, want the execution role carrying a dead letter", target, req.Sender)
	}
	return req
}

// ranWithPythonError is the recorded function error: it ran, failed with
// the Python exception record, and was retried at the default
// (test_lambda_destinations.snapshot.json:L243-L273).
func ranWithPythonError() destination.Invocation {
	return destination.Ran{Attempts: 3, Response: destination.Response{ExecutedVersion: "$LATEST", FunctionError: "Unhandled", Payload: []byte(raiseErrorResponse)}}
}

// The dead letter of a function error is the event byte for byte, with
// ErrorCode Number "200", ErrorMessage the error's errorMessage and
// RequestID the request id (lambda-destinations-layers.md finding 1, 5.2:
// test_lambda_destinations.snapshot.json:L243-L273, validated 2025-11-24).
func TestDeadLetterToAQueueIsTheEventAsSent(t *testing.T) {
	const queue = "arn:aws:sqs:us-east-1:111111111111:destination-fn-dlq"
	req := dead(t, queue, destination.RetriesExhausted, ranWithPythonError())
	got, ok := req.Target.(delivery.SQSQueue)
	if req.Mechanism != delivery.LambdaToSQS || !ok || got.ARN != mustARN(t, queue) {
		t.Fatalf("DeadLetter to a queue = %s %#v, want lambda-to-sqs to %s", req.Mechanism, req.Target, queue)
	}
	sum := md5.Sum([]byte(got.Body)) //nolint:gosec // MD5OfBody is defined as the MD5 of the body
	if got.Body != pythonEvent || hex.EncodeToString(sum[:]) != pythonEventMD5 {
		t.Errorf("dead letter body = %q (MD5 %x), want the event as sent, MD5 %s", got.Body, sum, pythonEventMD5)
	}
	want := map[string]delivery.AttributeValue{
		"ErrorCode":    delivery.NumberAttribute{Value: "200"},
		"ErrorMessage": delivery.StringAttribute{Value: "Test exception (this is intentional)"},
		"RequestID":    delivery.StringAttribute{Value: string(goldenRequestID)},
	}
	if a := attributes(got.Attributes); len(got.Attributes) != len(want) || !reflect.DeepEqual(a, want) {
		t.Errorf("dead letter attributes = %#v, want %#v", got.Attributes, want)
	}
	if got.GroupID != "" || got.DedupID != "" {
		t.Errorf("dead letter group %q, dedup %q; want none: Lambda sends to standard queues only (lambda-destinations-layers.md 5.1)", got.GroupID, got.DedupID)
	}
}

// A dead-letter topic gets the event as-is with the same additional
// information in attributes: the guide says so of the dead-letter queue
// or topic alike (lambda-destinations-layers.md 5.2,
// https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html).
func TestDeadLetterToATopicIsTheEventAsSent(t *testing.T) {
	const topic = "arn:aws:sns:us-east-1:111111111111:destination-fn-dlq"
	req := dead(t, topic, destination.RetriesExhausted, ranWithPythonError())
	got, ok := req.Target.(delivery.SNSTopic)
	if req.Mechanism != delivery.LambdaToSNS || !ok || got.ARN != mustARN(t, topic) || got.Message != pythonEvent || got.Subject != "" {
		t.Fatalf("DeadLetter to a topic = %s %#v, want lambda-to-sns of the event as sent", req.Mechanism, req.Target)
	}
	want := map[string]delivery.AttributeValue{
		"ErrorCode":    delivery.NumberAttribute{Value: "200"},
		"ErrorMessage": delivery.StringAttribute{Value: "Test exception (this is intentional)"},
		"RequestID":    delivery.StringAttribute{Value: string(goldenRequestID)},
	}
	if a := attributes(got.Attributes); len(got.Attributes) != len(want) || !reflect.DeepEqual(a, want) {
		t.Errorf("dead letter attributes = %#v, want %#v", got.Attributes, want)
	}
}

// An event that never ran has a request id but no function error, so no
// ErrorCode. Under zero reserved concurrency AWS adds its own ErrorMessage,
// "isconfigured" as sent (aws-recorded, lambda-destinations-layers.md 5.2:
// LDL-04); what it sends for an event that aged out unrun is unrecorded,
// so understudy invents no ErrorMessage there. RequestID is documented
// for every dead letter (5.2).
func TestDeadLetterOfAnEventThatNeverRan(t *testing.T) {
	requestID := delivery.StringAttribute{Value: string(goldenRequestID)}
	for _, c := range []struct {
		cond destination.Condition
		want map[string]delivery.AttributeValue
	}{
		{destination.ZeroReservedConcurrency, map[string]delivery.AttributeValue{
			"ErrorMessage": delivery.StringAttribute{Value: "Function not invoked. Function isconfigured with zero reserved concurrent executions"},
			"RequestID":    requestID,
		}},
		{destination.EventAgeExceeded, map[string]delivery.AttributeValue{"RequestID": requestID}},
	} {
		req := dead(t, "arn:aws:sqs:us-east-1:111111111111:destination-fn-dlq", c.cond, destination.NeverRan{})
		got, ok := req.Target.(delivery.SQSQueue)
		if !ok {
			t.Fatalf("DeadLetter to a queue = %#v, want an SQSQueue", req.Target)
		}
		if a := attributes(got.Attributes); len(got.Attributes) != len(c.want) || !reflect.DeepEqual(a, c.want) {
			t.Errorf("%s dead letter attributes = %#v, want %#v", c.cond, got.Attributes, c.want)
		}
		if got.Body != pythonEvent {
			t.Errorf("%s dead letter body = %q, want the event as sent", c.cond, got.Body)
		}
	}
}

// Only a standard queue or topic takes dead letters
// (lambda-destinations-layers.md 5.1), and a dead letter needs to know
// whether the function ran.
func TestDeadLetterRefusesOtherTargets(t *testing.T) {
	s := sender(t)
	for _, target := range []string{
		"arn:aws:lambda:us-east-1:111111111111:function:worker",
		"arn:aws:events:us-east-1:111111111111:event-bus/default",
		"arn:aws:s3:::audit-records",
	} {
		if _, err := destination.DeadLetter(mustARN(t, target), s, goldenRequestID, destination.RetriesExhausted, ranWithPythonError(), []byte(pythonEvent)); !isWrapError(err) {
			t.Errorf("DeadLetter(%s) = %v, want a *WrapError", target, err)
		}
	}
	if _, err := destination.DeadLetter(mustARN(t, "arn:aws:sqs:us-east-1:111111111111:dlq"), s, goldenRequestID, destination.RetriesExhausted, nil, []byte(pythonEvent)); !isWrapError(err) {
		t.Errorf("DeadLetter with no invocation = %v, want a *WrapError", err)
	}
}

// ErrorMessage is the error object's errorMessage
// (lambda-destinations-layers.md 5.2: test_lambda_destinations.snapshot.json:L255-L258),
// so for a timeout it is the runtime's timeout text (lambda-api.md 8.3).
// "The first 1 KB of the error message" (5.2) is cut at 1,024 bytes and
// moved back to a UTF-8 character boundary until LDL-03 records the cut
// (11.3.7).
func TestErrorMessage(t *testing.T) {
	cases := []struct {
		name, response, want string
	}{
		{"the Python exception record", raiseErrorResponse, "Test exception (this is intentional)"},
		{
			"a timeout", `{"errorMessage":"RequestId: 0f0e0d0c-0b0a-4908-8706-050403020100 Error: Task timed out after 3.00 seconds"}`,
			"RequestId: 0f0e0d0c-0b0a-4908-8706-050403020100 Error: Task timed out after 3.00 seconds",
		},
		{"a long message", `{"errorMessage":"` + strings.Repeat("a", 3000) + `"}`, strings.Repeat("a", 1024)},
		{"a message cut inside a character", `{"errorMessage":"` + strings.Repeat("a", 1023) + `é and more"}`, strings.Repeat("a", 1023)},
		{"exactly 1 KB", `{"errorMessage":"` + strings.Repeat("b", 1024) + `"}`, strings.Repeat("b", 1024)},
	}
	for _, c := range cases {
		if got := destination.ErrorMessage([]byte(c.response)); got != c.want {
			t.Errorf("%s: ErrorMessage = %q (%d bytes), want %q (%d bytes)", c.name, got, len(got), c.want, len(c.want))
		}
	}
}

// For any errorMessage, ErrorMessage is a prefix of it, valid UTF-8, at
// most 1,024 bytes, and the whole message when that fits
// (lambda-destinations-layers.md 5.2, 11.3.7).
func TestErrorMessageProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		msg := rapid.String().Draw(t, "message")
		got := destination.ErrorMessage([]byte(`{"errorType":"Error","errorMessage":` + jsonString(t, msg) + `}`))
		if !strings.HasPrefix(msg, got) || !utf8.ValidString(got) || len(got) > 1024 {
			t.Fatalf("ErrorMessage of %q = %q, want a valid prefix of at most 1024 bytes", msg, got)
		}
		if len(msg) <= 1024 && got != msg {
			t.Fatalf("ErrorMessage of %q (%d bytes) = %q, want it whole", msg, len(msg), got)
		}
		if len(msg) > 1024 && len(got) < 1024-utf8.UTFMax+1 {
			t.Fatalf("ErrorMessage of a %d-byte message = %d bytes, want the cut within one character of 1024", len(msg), len(got))
		}
	})
}
