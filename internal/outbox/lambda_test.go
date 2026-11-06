package outbox_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The journal's metric clauses: a Lambda delivery that fails for good
// counts in DestinationDeliveryFailures when it carries a destination
// record (lambda-destinations-layers.md 4.6) and in DeadLetterErrors,
// the event deleted, when it carries a dead letter (5.4).
const (
	recordClause     = "; Lambda counts it in DestinationDeliveryFailures"
	deadLetterClause = "; Lambda deletes the event and counts it in DeadLetterErrors"
)

// traceHeader is the Invoke call's X-Amzn-Trace-Id.
const traceHeader = "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1"

// workerRole is the execution role of the function worker.
func workerRole() aws.ARN {
	return aws.ARN{Partition: "aws", Service: "iam", Account: "000000000000", Resource: "role/service-role/worker-role"}
}

func worker() aws.ARN { return arn("lambda", "function:worker") }

// fromWorker is a Lambda delivery from worker, run as its execution role,
// carrying carries: no retry and no dead-letter queue of its own.
func fromWorker(m delivery.Mechanism, target delivery.Target, carries delivery.LambdaPayload) delivery.Request {
	return delivery.Request{
		Source: worker(), Mechanism: m, Target: target, Policy: delivery.None{},
		Sender: &delivery.LambdaSender{Role: workerRole(), Carries: carries},
	}
}

// isWorkerRole reports whether p is a session of worker's execution role:
// Lambda delivers records and dead letters as that role
// (lambda-destinations-layers.md 2.5, 11.3.2).
func isWorkerRole(p identity.Principal) bool {
	r, ok := p.(identity.AssumedRole)
	return ok && r.Account == aws.DefaultAccount() && r.Role.Name == "worker-role" && r.Role.Path == "/service-role/"
}

// overLambdaDestination is the origin of a Lambda delivery's target call.
var overLambdaDestination = ref.Internal{Via: ref.LambdaDestination}

// roleUsed reports whether the access record holds action used by worker's
// execution role in the execution-role mode, so the least-privilege policy
// the recorder writes for the role carries it.
func roleUsed(k *kerneltest.K, prefix, action string) bool {
	role := access.Role{Account: aws.DefaultAccount(), Path: "/service-role/", Name: "worker-role"}
	uses := k.Access.Usage(access.Filter{Principal: role, Service: prefix})
	for i := range uses {
		if u := &uses[i]; u.Mode == (access.AsExecutionRole{}) && u.Action == (registry.IAMAction{Prefix: prefix, Name: action}) {
			return true
		}
	}
	return false
}

// dropped returns the reason of the only Delivery event, which must be
// Dropped.
func dropped(k *kerneltest.K) string {
	k.Helper()
	events := deliveries(k)
	if len(events) != 1 {
		k.Fatalf("delivery events = %+v, want one", events)
	}
	d, ok := detail[journal.Delivery](k, events[0]).Outcome.(journal.Dropped)
	if !ok {
		k.Fatalf("outcome = %#v, want Dropped", detail[journal.Delivery](k, events[0]).Outcome)
	}
	return d.Reason
}

// A Lambda delivery that breaks the rules of Lambda's deliveries is
// refused before anything is queued, naming the member at fault: only
// the Lambda mechanisms run as an execution role, and always do; the
// sender is an IAM role carrying a declared payload; a dead letter goes
// to a queue or topic only (lambda-destinations-layers.md 5.1); and
// Lambda neither retries nor dead-letters its own deliveries (4.6, 5.4).
func TestEnqueueChecksLambdaDeliveries(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		queue := delivery.SQSQueue{ARN: arn("sqs", "records"), Body: "{}"}
		dlq := arn("sqs", "elsewhere")
		withSender := func(r delivery.Request, s *delivery.LambdaSender) delivery.Request { r.Sender = s; return r }
		withPolicy := func(r delivery.Request, p delivery.RetryPolicy) delivery.Request { r.Policy = p; return r }
		withDLQ := func(r delivery.Request) delivery.Request { r.DLQ = &dlq; return r }
		cases := []struct {
			name   string
			req    delivery.Request
			field  string
			reason string
		}{
			{
				"a Lambda delivery without a sender", withSender(fromWorker(delivery.LambdaToSQS, queue, delivery.DestinationRecord), nil),
				"Sender", "is nil, but a lambda-to-sqs delivery runs as the function's execution role",
			},
			{
				"another delivery with a sender", withSender(toQueue("q", delivery.SNSManaged{}), &delivery.LambdaSender{Role: workerRole(), Carries: delivery.DestinationRecord}),
				"Sender", "is set, but only the Lambda mechanisms run as an execution role",
			},
			{
				"a sender that is a user", withSender(fromWorker(delivery.LambdaToSQS, queue, delivery.DestinationRecord),
					&delivery.LambdaSender{Role: aws.ARN{Partition: "aws", Service: "iam", Account: "000000000000", Resource: "user/alice"}, Carries: delivery.DestinationRecord}),
				"Sender.Role", "is not an IAM role",
			},
			{
				"a sender that is a function", withSender(fromWorker(delivery.LambdaToSQS, queue, delivery.DestinationRecord),
					&delivery.LambdaSender{Role: worker(), Carries: delivery.DestinationRecord}),
				"Sender.Role", "is not an IAM role",
			},
			{"an undeclared payload", fromWorker(delivery.LambdaToSQS, queue, 0), "Sender.Carries", "is not a declared Lambda delivery payload"},
			{
				"a dead letter to a function", fromWorker(delivery.LambdaToLambda,
					delivery.LambdaFunction{ARN: arn("lambda", "function:other"), Event: []byte("{}"), Invocation: delivery.InvocationEvent}, delivery.DeadLetter),
				"Sender.Carries", "dead letter",
			},
			{"a dead-letter queue", withDLQ(fromWorker(delivery.LambdaToSQS, queue, delivery.DestinationRecord)), "DLQ", "Lambda"},
			{"a retry policy", withPolicy(fromWorker(delivery.LambdaToSQS, queue, delivery.DestinationRecord), delivery.SNSManaged{}), "Policy", "None"},
		}
		for _, c := range cases {
			_, err := o.Enqueue(c.req, ref.Cause{}, k.Clock.Now())
			rerr, ok := errors.AsType[*outbox.RequestError](err)
			if !ok || rerr.Field != c.field || !strings.Contains(rerr.Reason, c.reason) {
				k.Errorf("%s: Enqueue = %v, want a *RequestError for %s saying %q", c.name, err, c.field, c.reason)
			}
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources = %v, want nothing queued", r)
		}
	})
}

// An S3 object under lambda-to-s3 and an event bus under lambda-to-events
// are deliveries the outbox takes (lambda-destinations-layers.md 4.4,
// 4.5).
func TestEnqueueAcceptsLambdaObjectAndBusDeliveries(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		for name, req := range map[string]delivery.Request{
			"an object": fromWorker(delivery.LambdaToS3, delivery.S3Object{Bucket: "audit", Key: "aws/lambda/async/worker/k", Body: []byte("{}")}, delivery.DestinationRecord),
			"a bus": fromWorker(delivery.LambdaToEvents, delivery.EventBus{ARN: arn("events", "event-bus/default"), Entries: []delivery.EventEntry{
				{Source: "lambda", DetailType: "Lambda Function Invocation Result - Success", Detail: "{}", Resources: []string{arn("events", "event-bus/default").String(), worker().String() + ":$LATEST"}},
			}}, delivery.DestinationRecord),
		} {
			if _, err := o.Enqueue(req, ref.Cause{}, k.Clock.Now()); err != nil {
				k.Errorf("%s: Enqueue = %v", name, err)
			}
		}
	}, kerneltest.Services(eventbridgeapi.Service(), s3api.Service()))
}

// A record for an SQS destination is one SendMessage as the execution
// role, under the Lambda destination mechanism, with the record as the
// body, no message attributes and the trace header as AWSTraceHeader
// (lambda-destinations-layers.md 4.1, 11.3.5), whose data type is String
// (https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessage.html).
func TestLambdaToSQSSendsAsTheExecutionRole(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		cause := source(k)
		target := delivery.SQSQueue{ARN: arn("sqs", "records"), Body: `{"version":"1.0"}`, TraceHeader: traceHeader}
		enqueue(k, o, fromWorker(delivery.LambdaToSQS, target, delivery.DestinationRecord), cause)
		settled(k)
		got := q.sent()
		if len(got) != 1 {
			k.Fatalf("sent %+v, want one SendMessage", got)
		}
		s := got[0]
		if s.queue != "records" || s.in.MessageBody != target.Body || len(s.in.MessageAttributes) != 0 {
			k.Errorf("SendMessage to %s of %q with attributes %v, want the record alone to records", s.queue, s.in.MessageBody, s.in.MessageAttributes)
		}
		th, ok := s.in.MessageSystemAttributes[sqsapi.MessageSystemAttributeNameForSendsAWSTraceHeader]
		if !ok || th.DataType != "String" || th.StringValue == nil || *th.StringValue != traceHeader || len(s.in.MessageSystemAttributes) != 1 {
			k.Errorf("MessageSystemAttributes = %+v, want AWSTraceHeader String %s alone", s.in.MessageSystemAttributes, traceHeader)
		}
		if !isWorkerRole(s.principal) || s.origin != overLambdaDestination {
			k.Errorf("SendMessage by %v from %v, want worker-role over the Lambda destination mechanism", s.principal, s.origin)
		}
		if d := detail[journal.Delivery](k, deliveries(k)[0]); d.Outcome != (journal.Delivered{}) || d.From != worker() || d.To != target.ARN {
			k.Errorf("delivery = %+v, want Delivered from worker to records", d)
		}
		if !roleUsed(k, "sqs", "SendMessage") {
			k.Errorf("the access record has no sqs:SendMessage by worker-role as its execution role: %+v", k.Access.Usage(access.Filter{Service: "sqs"}))
		}
	})
}

// A delivery without a trace header sends no AWSTraceHeader.
func TestLambdaToSQSWithoutATraceHeader(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		enqueue(k, o, fromWorker(delivery.LambdaToSQS, delivery.SQSQueue{ARN: arn("sqs", "records"), Body: "{}"}, delivery.DestinationRecord), source(k))
		settled(k)
		if got := q.sent(); len(got) != 1 || len(got[0].in.MessageSystemAttributes) != 0 {
			k.Errorf("sent %+v, want one SendMessage with no system attributes", got)
		}
	})
}

// A record for an SNS destination is one Publish of the record as the
// execution role (lambda-destinations-layers.md 4.2, 11.3.2).
func TestLambdaToSNSPublishesAsTheExecutionRole(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		var publishes recorder[call[snsapi.PublishInput]]
		k.Stub(snsapi.OpPublish, func(_ context.Context, c *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
			publishes.add(call[snsapi.PublishInput]{in: *in, at: c.Admitted(), principal: c.Principal(), origin: c.Origin()})
			return &snsapi.PublishOutput{MessageId: new("m")}, nil
		})
		target := delivery.SNSTopic{ARN: arn("sns", "alerts"), Message: `{"version":"1.0"}`}
		enqueue(k, o, fromWorker(delivery.LambdaToSNS, target, delivery.DestinationRecord), source(k))
		settled(k)
		got := publishes.all()
		if len(got) != 1 {
			k.Fatalf("publishes = %+v, want one", got)
		}
		p := got[0]
		if p.in.TopicArn == nil || *p.in.TopicArn != target.ARN.String() || p.in.Message != target.Message || p.in.Subject != nil {
			k.Errorf("Publish = %+v, want the record to alerts with no subject", p.in)
		}
		if !isWorkerRole(p.principal) || p.origin != overLambdaDestination {
			k.Errorf("Publish by %v from %v, want worker-role over the Lambda destination mechanism", p.principal, p.origin)
		}
		if !roleUsed(k, "sns", "Publish") {
			k.Error("the access record has no sns:Publish by worker-role as its execution role")
		}
	})
}

// A record for a function destination is one Invoke of the record as the
// execution role (lambda-destinations-layers.md 4.3, 11.3.2).
func TestLambdaToLambdaInvokesAsTheExecutionRole(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		var invokes recorder[call[lambdaapi.InvokeInput]]
		k.Stub(lambdaapi.OpInvoke, func(_ context.Context, c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			invokes.add(call[lambdaapi.InvokeInput]{in: *in, at: c.Admitted(), principal: c.Principal(), origin: c.Origin()})
			return &lambdaapi.InvokeOutput{StatusCode: 202}, nil
		})
		event := []byte(`{"version":"1.0"}`)
		target := delivery.LambdaFunction{ARN: arn("lambda", "function:auditor"), Event: event, Invocation: delivery.InvocationEvent}
		enqueue(k, o, fromWorker(delivery.LambdaToLambda, target, delivery.DestinationRecord), source(k))
		settled(k)
		got := invokes.all()
		if len(got) != 1 {
			k.Fatalf("invokes = %+v, want one", got)
		}
		i := got[0]
		if i.in.FunctionName != target.ARN.String() || i.in.InvocationType == nil || *i.in.InvocationType != lambdaapi.InvocationTypeEvent || !bytes.Equal(i.in.Payload, event) {
			k.Errorf("Invoke = %+v, want an Event invoke of auditor with the record", i.in)
		}
		if !isWorkerRole(i.principal) || i.origin != overLambdaDestination {
			k.Errorf("Invoke by %v from %v, want worker-role over the Lambda destination mechanism", i.principal, i.origin)
		}
		if !roleUsed(k, "lambda", "InvokeFunction") {
			k.Error("the access record has no lambda:InvokeFunction by worker-role as its execution role")
		}
	})
}

// A record for an EventBridge destination is one PutEvents entry as the
// execution role (lambda-destinations-layers.md 4.4, 11.3.2).
func TestLambdaToEventsPutsAsTheExecutionRole(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		var puts recorder[call[eventbridgeapi.PutEventsInput]]
		k.Stub(eventbridgeapi.OpPutEvents, func(_ context.Context, c *kernel.Call, in *eventbridgeapi.PutEventsInput) (*eventbridgeapi.PutEventsOutput, *awserr.Error) {
			puts.add(call[eventbridgeapi.PutEventsInput]{in: *in, at: c.Admitted(), principal: c.Principal(), origin: c.Origin()})
			return &eventbridgeapi.PutEventsOutput{Entries: []eventbridgeapi.PutEventsResultEntry{{EventId: new("e-1")}}}, nil
		})
		bus := arn("events", "event-bus/orders")
		entry := delivery.EventEntry{
			Source: "lambda", DetailType: "Lambda Function Invocation Result - Failure", Detail: `{"version":"1.0"}`,
			Resources: []string{bus.String(), worker().String() + ":$LATEST"},
		}
		enqueue(k, o, fromWorker(delivery.LambdaToEvents, delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{entry}}, delivery.DestinationRecord), source(k))
		settled(k)
		got := puts.all()
		if len(got) != 1 || len(got[0].in.Entries) != 1 {
			k.Fatalf("PutEvents calls = %+v, want one of one entry", got)
		}
		e := got[0].in.Entries[0]
		if e.Source == nil || *e.Source != entry.Source || e.DetailType == nil || *e.DetailType != entry.DetailType ||
			e.Detail == nil || *e.Detail != entry.Detail || !slices.Equal(e.Resources, entry.Resources) {
			k.Errorf("entry = %+v, want %+v", e, entry)
		}
		if !isWorkerRole(got[0].principal) || got[0].origin != overLambdaDestination {
			k.Errorf("PutEvents by %v from %v, want worker-role over the Lambda destination mechanism", got[0].principal, got[0].origin)
		}
		if !roleUsed(k, "events", "PutEvents") {
			k.Error("the access record has no events:PutEvents by worker-role as its execution role")
		}
	}, kerneltest.Services(eventbridgeapi.Service(), s3api.Service()))
}

// s3Calls stubs HeadBucket, answering head's error, and PutObject, and
// records each call as "HeadBucket <bucket>" or "PutObject <bucket>/<key>"
// with its principal and origin.
func s3Calls(k *kerneltest.K, head *awserr.Error) *recorder[call[string]] {
	var made recorder[call[string]]
	k.Stub(s3api.OpHeadBucket, func(_ context.Context, c *kernel.Call, in *s3api.HeadBucketInput) (*s3api.HeadBucketOutput, *awserr.Error) {
		made.add(call[string]{in: "HeadBucket " + in.Bucket, principal: c.Principal(), origin: c.Origin()})
		if head != nil {
			return nil, head
		}
		return &s3api.HeadBucketOutput{}, nil
	})
	k.Stub(s3api.OpPutObject, func(_ context.Context, c *kernel.Call, in *s3api.PutObjectInput) (*s3api.PutObjectOutput, *awserr.Error) {
		made.add(call[string]{in: "PutObject " + in.Bucket + "/" + in.Key, principal: c.Principal(), origin: c.Origin()})
		return &s3api.PutObjectOutput{ETag: new(`"etag"`)}, nil
	})
	return &made
}

// A record for an S3 destination is a HeadBucket (IAM action
// s3:ListBucket), then a PutObject, both as the execution role, which
// needs both actions (lambda-destinations-layers.md 2.5, 11.3.2).
func TestLambdaToS3HeadsTheBucketThenPutsTheObject(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		made := s3Calls(k, nil)
		const key = "aws/lambda/async/worker/2026/10/26/2026-10-26T09.05.07-0f0e0d0c-0b0a-4908-8706-050403020100"
		enqueue(k, o, fromWorker(delivery.LambdaToS3, delivery.S3Object{Bucket: "audit", Key: key, Body: []byte(`{"version":"1.0"}`)}, delivery.DestinationRecord), source(k))
		settled(k)
		got := made.all()
		names := make([]string, len(got))
		for i, c := range got {
			names[i] = c.in
			if !isWorkerRole(c.principal) || c.origin != overLambdaDestination {
				k.Errorf("%s by %v from %v, want worker-role over the Lambda destination mechanism", c.in, c.principal, c.origin)
			}
		}
		if want := []string{"HeadBucket audit", "PutObject audit/" + key}; !slices.Equal(names, want) {
			k.Errorf("S3 calls = %v, want %v", names, want)
		}
		if d := detail[journal.Delivery](k, deliveries(k)[0]); d.Outcome != (journal.Delivered{}) {
			k.Errorf("outcome = %#v, want Delivered", d.Outcome)
		}
		if !roleUsed(k, "s3", "ListBucket") || !roleUsed(k, "s3", "PutObject") {
			k.Errorf("the access record lacks s3:ListBucket or s3:PutObject by worker-role: %+v", k.Access.Usage(access.Filter{Service: "s3"}))
		}
	}, kerneltest.Services(eventbridgeapi.Service(), s3api.Service()))
}

// A HeadBucket that fails the attempt with no PutObject, counted in
// DestinationDeliveryFailures and not retried (lambda-destinations-layers.md 4.6).
func TestLambdaToS3WithoutTheBucketPutsNothing(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		made := s3Calls(k, s3api.ErrNoSuchBucket("The specified bucket does not exist"))
		enqueue(k, o, fromWorker(delivery.LambdaToS3, delivery.S3Object{Bucket: "gone", Key: "aws/lambda/async/worker/k", Body: []byte("{}")}, delivery.DestinationRecord), source(k))
		settled(k)
		advance(k, time.Hour)
		if got := made.all(); len(got) != 1 || got[0].in != "HeadBucket gone" {
			k.Errorf("S3 calls = %+v, want the HeadBucket alone", got)
		}
		if reason := dropped(k); !strings.HasSuffix(reason, recordClause) {
			k.Errorf("reason = %q, want it to end %q", reason, recordClause)
		}
	}, kerneltest.Services(eventbridgeapi.Service(), s3api.Service()))
}

// Lambda never delivers to a FIFO queue or topic: the delivery fails with
// no call to the target and counts in its metric
// (lambda-destinations-layers.md 2.2, 4.6, 5.1, 11.3.6).
func TestLambdaDeliveriesToFIFOTargetsFail(t *testing.T) {
	cases := []struct {
		name     string
		req      delivery.Request
		reason   string
		clause   string
		receiver string
	}{
		{
			"a record to a FIFO queue", fromWorker(delivery.LambdaToSQS, delivery.SQSQueue{ARN: arn("sqs", "records.fifo"), Body: "{}"}, delivery.DestinationRecord),
			arn("sqs", "records.fifo").String() + " is a FIFO queue, which Lambda does not deliver to", recordClause, "sqs",
		},
		{
			"a dead letter to a FIFO queue", fromWorker(delivery.LambdaToSQS, delivery.SQSQueue{ARN: arn("sqs", "dlq.fifo"), Body: "{}"}, delivery.DeadLetter),
			arn("sqs", "dlq.fifo").String() + " is a FIFO queue, which Lambda does not deliver to", deadLetterClause, "sqs",
		},
		{
			"a record to a FIFO topic", fromWorker(delivery.LambdaToSNS, delivery.SNSTopic{ARN: arn("sns", "alerts.fifo"), Message: "{}"}, delivery.DestinationRecord),
			arn("sns", "alerts.fifo").String() + " is a FIFO topic, which Lambda does not deliver to", recordClause, "sns",
		},
		{
			"a dead letter to a FIFO topic", fromWorker(delivery.LambdaToSNS, delivery.SNSTopic{ARN: arn("sns", "dlq.fifo"), Message: "{}"}, delivery.DeadLetter),
			arn("sns", "dlq.fifo").String() + " is a FIFO topic, which Lambda does not deliver to", deadLetterClause, "sns",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run(t, func(k *kerneltest.K, o *outbox.Outbox) {
				q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
				var publishes recorder[string]
				k.Stub(snsapi.OpPublish, func(_ context.Context, _ *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
					publishes.add(in.Message)
					return &snsapi.PublishOutput{MessageId: new("m")}, nil
				})
				enqueue(k, o, c.req, source(k))
				settled(k)
				if n := len(q.sent()) + len(publishes.all()); n != 0 {
					k.Errorf("%d calls to the FIFO %s, want none", n, c.receiver)
				}
				reason := dropped(k)
				if !strings.HasPrefix(reason, c.reason) || !strings.HasSuffix(reason, c.clause) {
					k.Errorf("reason = %q, want it to begin %q and end %q", reason, c.reason, c.clause)
				}
			})
		})
	}
}

// Lambda makes one attempt at a destination record: a failure is journaled
// with DestinationDeliveryFailures and not retried (switch
// lambda.destination.delivery-retry names the policy the Lambda service
// asks for; here the request's None).
func TestLambdaRecordFailureIsNotRetried(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return throttled("") })
		enqueue(k, o, fromWorker(delivery.LambdaToSQS, delivery.SQSQueue{ARN: arn("sqs", "records"), Body: "{}"}, delivery.DestinationRecord), source(k))
		settled(k)
		advance(k, time.Hour)
		if n := len(q.sent()); n != 1 {
			k.Errorf("%d attempts, want 1", n)
		}
		if reason := dropped(k); !strings.HasSuffix(reason, recordClause) || !strings.Contains(reason, "slow down") {
			k.Errorf("reason = %q, want the target's error then %q", reason, recordClause)
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources = %v, want the delivery gone", r)
		}
	})
}

// A dead letter the queue refuses is lost: Lambda deletes the event and
// counts it in DeadLetterErrors (lambda-destinations-layers.md 5.4).
func TestLambdaDeadLetterFailureCountsDeadLetterErrors(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error {
			return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
		})
		enqueue(k, o, fromWorker(delivery.LambdaToSQS, delivery.SQSQueue{ARN: arn("sqs", "dlq"), Body: `{"raise_error": 1}`}, delivery.DeadLetter), source(k))
		settled(k)
		advance(k, time.Hour)
		if n := len(q.sent()); n != 1 {
			k.Errorf("%d attempts, want 1", n)
		}
		if reason := dropped(k); !strings.HasSuffix(reason, deadLetterClause) || !strings.Contains(reason, "The specified queue does not exist.") {
			k.Errorf("reason = %q, want the queue's error then %q", reason, deadLetterClause)
		}
	})
}

// A target operation understudy does not implement fails the delivery
// with the unimplemented message and the metric clause.
func TestUnimplementedLambdaTargetCountsTheMetric(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		enqueue(k, o, fromWorker(delivery.LambdaToSQS, delivery.SQSQueue{ARN: arn("sqs", "records"), Body: "{}"}, delivery.DestinationRecord), source(k))
		settled(k)
		if reason := dropped(k); !strings.Contains(reason, "does not implement sqs.SendMessage") || !strings.HasSuffix(reason, recordClause) {
			k.Errorf("reason = %q, want the unimplemented operation then %q", reason, recordClause)
		}
	})
}

// AWS authorizes a Lambda delivery as the execution role, not as a
// service principal a resource policy names, so the off-mode advisory
// about a target's resource policy does not apply
// (lambda-destinations-layers.md 2.5, 11.3.2).
func TestLambdaDeliveriesGetNoResourcePolicyAdvisory(t *testing.T) {
	queue := arn("sqs", "locked")
	runWith(t, outbox.Options{Policies: policies{queue: nil}}, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		enqueue(k, o, fromWorker(delivery.LambdaToSQS, delivery.SQSQueue{ARN: queue, Body: "{}"}, delivery.DestinationRecord), source(k))
		settled(k)
		if a := k.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events; len(a) != 0 {
			k.Errorf("advisories = %+v, want none", a)
		}
		if n := len(q.sent()); n != 1 {
			k.Errorf("%d attempts, want the delivery made", n)
		}
	})
}

// The target call of a Lambda delivery continues the trace of the call
// that produced it, like every other delivery (ARCHITECTURE.md 10).
func TestLambdaTargetCallJoinsTheSourceTrace(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		cause := source(k)
		enqueue(k, o, fromWorker(delivery.LambdaToSQS, delivery.SQSQueue{ARN: arn("sqs", "records"), Body: "{}"}, delivery.DestinationRecord), cause)
		settled(k)
		sends := k.Journal.Query(journal.Filter{Operation: ref.OperationID{Service: "sqs", Name: "SendMessage"}, Limit: 10}).Events
		if len(sends) != 1 || sends[0].Cause != cause {
			k.Errorf("SendMessage events = %+v, want one caused by %+v", sends, cause)
		}
	})
}
