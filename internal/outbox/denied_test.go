package outbox_test

import (
	"context"
	"maps"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
)

// A delivery the target's policy refuses (an attempt answered with an
// authorization denial, awserr.Denial) is not retried: "Amazon SNS doesn't
// retry the message delivery that fails because of a client-side error"
// and discards it "unless a dead-letter queue is attached"
// (iam-enforcement-surfaces.md 8.2, aws-doc); for "missing permissions to a
// target ... no retry attempts will happen ... EventBridge sends these
// events directly to the target DLQ" (8.4, aws-doc). The attempt is
// journaled dead-lettered or dropped (12.4).

// denied is the answer a target gives a delivery its policy refuses.
func denied(string) *awserr.Error {
	return &awserr.Error{ID: sqsapi.DenialError(), Message: "denied", Origin: awserr.Denial{}}
}

func TestADeniedDeliveryIsNotRetried(t *testing.T) {
	cases := []struct {
		name   string
		req    func() delivery.Request
		dlq    bool
		letter journal.DeliveryOutcome
	}{
		{"SNS without a dead-letter queue", func() delivery.Request { return toQueue("refused", delivery.SNSManaged{}) }, false, journal.Dropped{}},
		{"SNS with a dead-letter queue", func() delivery.Request {
			r := toQueue("refused", delivery.SNSManaged{})
			dlq := arn("sqs", "dlq")
			r.DLQ = &dlq
			return r
		}, true, journal.DeadLettered{DLQ: arn("sqs", "dlq")}},
		{"EventBridge with a dead-letter queue", func() delivery.Request {
			dlq := arn("sqs", "dlq")
			return delivery.Request{
				Source: arn("events", "rule/nightly"), Mechanism: delivery.EventsToSQS,
				Target: delivery.SQSQueue{ARN: arn("sqs", "refused"), Body: `{"version":"0","detail":{}}`},
				Policy: delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 185}, DLQ: &dlq,
			}
		}, true, journal.DeadLettered{DLQ: arn("sqs", "dlq")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run(t, func(k *kerneltest.K, o *outbox.Outbox) {
				q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
					if in.QueueUrl[len(in.QueueUrl)-len("refused"):] == "refused" {
						return denied("")
					}
					return nil
				})
				enqueue(k, o, c.req(), source(k))
				settled(k)
				advance(k, time.Hour)

				attempts, letters := 0, 0
				for _, s := range q.sent() {
					switch s.queue {
					case "refused":
						attempts++
					case "dlq":
						letters++
					}
				}
				wantLetters := 0
				if c.dlq {
					wantLetters = 1
				}
				if attempts != 1 || letters != wantLetters {
					k.Errorf("attempts %d, dead letters %d; want one attempt and %d dead letters", attempts, letters, wantLetters)
				}
				events := deliveries(k)
				if len(events) != 1 {
					k.Fatalf("delivery events = %+v, want one", events)
				}
				if got := detail[journal.Delivery](k, events[0]).Outcome; !sameOutcome(got, c.letter) {
					k.Errorf("delivery outcome = %#v, want %#v", got, c.letter)
				}
			})
		})
	}
}

// sdkRequestID is the request id an SDK client suffix carries.
var sdkRequestID = regexp.MustCompile(`Request ID: [0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12};`)

// An EventBridge dead letter of a delivery its target refused carries
// ERROR_CODE NO_PERMISSIONS, ERROR_MESSAGE, RULE_ARN and TARGET_ARN only,
// no RETRY_ATTEMPTS or EXHAUSTED_RETRY_CONDITION, the message being the
// target's denial with the AWS SDK for Java v1 client suffix EventBridge's
// client appends (iam-enforcement-surfaces.md 8.4; aws-recorded,
// testdata/parity/sqs/IAM-ENF/22-dlq.json for Lambda, 22-dlq-sa.json for
// SQS). A function that no longer exists dead-letters the same way, with
// Lambda's denial naming it, since no policy can admit EventBridge to it
// (22-dlq-gone.json). Request ids, account, Region and names are
// normalized.
func TestAnEventBridgeRefusalDeadLettersTheTargetsDenial(t *testing.T) {
	const (
		fnDenial = "User: events.amazonaws.com is not authorized to perform: lambda:InvokeFunction on resource: arn:aws:lambda:us-east-1:000000000000:function:fn" +
			" because no resource-based policy allows the lambda:InvokeFunction action"
		lambdaSuffix = " (Service: AWSLambdaInternal; Status Code: 403; Error Code: AccessDeniedException; Request ID: <request-id>; Proxy: null)"
		queueDenial  = "User: events.amazonaws.com is not authorized to perform: sqs:sendmessage on resource: arn:aws:sqs:us-east-1:000000000000:eq" +
			" because no resource-based policy allows the sqs:sendmessage action"
	)
	fn, queue := arn("lambda", "function:fn"), arn("sqs", "eq")
	toFunction := delivery.LambdaFunction{ARN: fn, Event: []byte(`{}`), Invocation: delivery.InvocationEvent}
	for _, c := range []struct {
		name      string
		mechanism delivery.Mechanism
		target    delivery.Target
		targetARN aws.ARN
		answer    *awserr.Error
		message   string
	}{
		{
			"a function's policy refuses", delivery.EventsToLambda, toFunction, fn,
			refusal(lambdaapi.DenialError(), fnDenial), fnDenial + lambdaSuffix,
		},
		{
			"the function is gone", delivery.EventsToLambda, toFunction, fn,
			lambdaapi.ErrResourceNotFoundException("Function not found: " + fn.String()), fnDenial + lambdaSuffix,
		},
		{
			"a queue's policy refuses", delivery.EventsToSQS,
			delivery.SQSQueue{ARN: queue, Body: `{}`},
			queue,
			refusal(sqsapi.DenialError(), queueDenial),
			queueDenial + " (Service: AmazonSQS; Status Code: 403; Error Code: AccessDenied; Request ID: <request-id>; Proxy: null)",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			run(t, func(k *kerneltest.K, o *outbox.Outbox) {
				k.Stub(lambdaapi.OpInvoke, func(context.Context, *kernel.Call, *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
					return nil, c.answer
				})
				q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
					if strings.HasSuffix(in.QueueUrl, "/eq") {
						return c.answer
					}
					return nil
				})
				rule, dlq := arn("events", "rule/r"), arn("sqs", "dlq")
				enqueue(k, o, delivery.Request{
					Source: rule, Mechanism: c.mechanism, Target: c.target,
					Policy: delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 185}, DLQ: &dlq,
				}, source(k))
				settled(k)
				advance(k, time.Hour)

				var letters []map[string]string
				for _, s := range q.sent() {
					if s.queue != "dlq" {
						continue
					}
					attrs := map[string]string{}
					for name, v := range s.in.MessageAttributes {
						if v.DataType != "String" || v.StringValue == nil {
							k.Errorf("attribute %s = %+v, want a String", name, v)
							continue
						}
						attrs[name] = sdkRequestID.ReplaceAllString(*v.StringValue, "Request ID: <request-id>;")
					}
					letters = append(letters, attrs)
				}
				want := map[string]string{
					"ERROR_CODE":    "NO_PERMISSIONS",
					"ERROR_MESSAGE": c.message,
					"RULE_ARN":      rule.String(),
					"TARGET_ARN":    c.targetARN.String(),
				}
				if len(letters) != 1 || !maps.Equal(letters[0], want) {
					k.Errorf("dead letters = %v, want one with exactly %v", letters, want)
				}
			})
		})
	}
}

// refusal is a target's denial of a delivery as its service answers it.
func refusal(id awserr.Identity, message string) *awserr.Error {
	return &awserr.Error{ID: id, Message: message, Origin: awserr.Denial{}}
}

// sameOutcome compares the variant, and a dead letter's queue.
func sameOutcome(got, want journal.DeliveryOutcome) bool {
	switch w := want.(type) {
	case journal.DeadLettered:
		g, ok := got.(journal.DeadLettered)
		return ok && g.DLQ == w.DLQ
	case journal.Dropped:
		_, ok := got.(journal.Dropped)
		return ok
	case journal.Delivered, journal.Filtered, journal.Refused, journal.Retrying:
	}
	return false
}
