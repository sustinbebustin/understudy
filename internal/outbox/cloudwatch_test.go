package outbox_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// alarmSource is a delivery from the alarm cpu, to be completed.
func alarmSource(k *kerneltest.K) delivery.Request {
	k.Helper()
	return delivery.Request{Source: arn("cloudwatch", "alarm:cpu"), SourceAccount: k.Scope.Account}
}

// An alarm notification is a Publish with the alarm's Subject as
// cloudwatch.amazonaws.com, the principal a topic policy grants
// (cloudwatch-alarms.md 5.3, aws-doc); to a FIFO topic it carries the
// message group and deduplication ids (5.3).
func TestCloudWatchToSNS(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		var publishes recorder[call[snsapi.PublishInput]]
		k.Stub(snsapi.OpPublish, func(_ context.Context, c *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
			publishes.add(call[snsapi.PublishInput]{in: *in, at: c.Admitted(), principal: c.Principal(), origin: c.Origin()})
			id := "m"
			return &snsapi.PublishOutput{MessageId: &id}, nil
		})
		req := alarmSource(k)
		req.Mechanism = delivery.CloudWatchToSNS
		req.Policy = delivery.None{}
		req.Target = delivery.SNSTopic{ARN: topic(), Message: `{"AlarmName":"cpu"}`, Subject: `ALARM: "cpu" in US East (N. Virginia)`}
		cause := source(k)
		enqueue(k, o, req, cause)
		settled(k)
		fifo := req
		fifo.Target = delivery.SNSTopic{ARN: arn("sns", "orders.fifo"), Message: `{"AlarmName":"cpu"}`, Subject: "s", GroupID: "g1", DedupID: "d1"}
		enqueue(k, o, fifo, cause)
		settled(k)

		calls := publishes.all()
		if len(calls) != 2 {
			k.Fatalf("publishes = %+v, want two", calls)
		}
		got := calls[0]
		if got.in.TopicArn == nil || *got.in.TopicArn != topic().String() || got.in.Message != `{"AlarmName":"cpu"}` ||
			got.in.Subject == nil || *got.in.Subject != `ALARM: "cpu" in US East (N. Virginia)` ||
			got.in.MessageGroupId != nil || got.in.MessageDeduplicationId != nil || len(got.in.MessageAttributes) != 0 {
			k.Errorf("Publish input = %+v, want the notification with its subject and no attributes (cwa 5.3)", got.in)
		}
		if got.principal != (identity.ServicePrincipal{Name: "cloudwatch.amazonaws.com"}) || got.origin != (ref.Internal{Via: ref.CloudWatchAlarm}) {
			k.Errorf("Publish by %v from %v, want cloudwatch.amazonaws.com over a CloudWatch alarm", got.principal, got.origin)
		}
		f := calls[1].in
		if f.MessageGroupId == nil || *f.MessageGroupId != "g1" || f.MessageDeduplicationId == nil || *f.MessageDeduplicationId != "d1" {
			k.Errorf("FIFO Publish = %+v, want group g1 and deduplication d1", f)
		}
	})
}

// A Lambda alarm action is an asynchronous invoke of the qualified function
// as lambda.alarms.cloudwatch.amazonaws.com, the principal its resource
// policy names (cloudwatch-alarms.md 6.1, aws-doc).
func TestCloudWatchToLambda(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		var invokes recorder[call[lambdaapi.InvokeInput]]
		k.Stub(lambdaapi.OpInvoke, func(_ context.Context, c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			invokes.add(call[lambdaapi.InvokeInput]{in: *in, at: c.Admitted(), principal: c.Principal(), origin: c.Origin()})
			return &lambdaapi.InvokeOutput{StatusCode: 202}, nil
		})
		event := []byte(`{"source":"aws.cloudwatch"}`)
		req := alarmSource(k)
		req.Mechanism = delivery.CloudWatchToLambda
		req.Policy = delivery.None{}
		req.Target = delivery.LambdaFunction{ARN: arn("lambda", "function:on-alarm"), Qualifier: "prod", Event: event, Invocation: delivery.InvocationEvent}
		enqueue(k, o, req, source(k))
		settled(k)
		calls := invokes.all()
		if len(calls) != 1 {
			k.Fatalf("invokes = %+v, want one", calls)
		}
		got := calls[0]
		if got.in.FunctionName != arn("lambda", "function:on-alarm").String() || got.in.Qualifier == nil || *got.in.Qualifier != "prod" ||
			got.in.InvocationType == nil || *got.in.InvocationType != lambdaapi.InvocationTypeEvent || !bytes.Equal(got.in.Payload, event) {
			k.Errorf("Invoke input = %+v, want an Event invoke of on-alarm:prod with the event", got.in)
		}
		if got.principal != (identity.ServicePrincipal{Name: "lambda.alarms.cloudwatch.amazonaws.com"}) || got.origin != (ref.Internal{Via: ref.CloudWatchAlarm}) {
			k.Errorf("Invoke by %v from %v, want lambda.alarms.cloudwatch.amazonaws.com over a CloudWatch alarm", got.principal, got.origin)
		}
	})
}

// An alarm notification the topic refuses, for a missing topic or a policy
// that does not allow CloudWatch, is journaled and dropped after one
// attempt: CloudWatch "doesn't ... detect any ... Amazon SNS errors"
// (cloudwatch-alarms.md 5.3, aws-doc) and no retry schedule is invented
// (12.4). In enforce mode a denial ends the same way, as
// cloudwatch.alarm.actions.denied (inferred, CWA-23) says.
func TestCloudWatchToSNSRefusalIsDropped(t *testing.T) {
	for name, refusal := range map[string]*awserr.Error{
		"missing topic": snsapi.ErrNotFoundException("Topic does not exist"),
		"denied":        {ID: snsapi.DenialError(), Message: "denied", Origin: awserr.Denial{}},
	} {
		t.Run(name, func(t *testing.T) {
			run(t, func(k *kerneltest.K, o *outbox.Outbox) {
				var publishes recorder[call[snsapi.PublishInput]]
				k.Stub(snsapi.OpPublish, func(_ context.Context, c *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
					publishes.add(call[snsapi.PublishInput]{in: *in, at: c.Admitted(), principal: c.Principal(), origin: c.Origin()})
					return nil, refusal
				})
				req := alarmSource(k)
				req.Mechanism = delivery.CloudWatchToSNS
				req.Policy = delivery.None{}
				req.Target = delivery.SNSTopic{ARN: topic(), Message: "{}", Subject: "s"}
				enqueue(k, o, req, source(k))
				settled(k)
				advance(k, time.Hour)
				if n := len(publishes.all()); n != 1 {
					k.Errorf("attempts = %d, want one", n)
				}
				events := deliveries(k)
				if len(events) != 1 {
					k.Fatalf("delivery events = %+v, want one", events)
				}
				if got := detail[journal.Delivery](k, events[0]).Outcome; !sameOutcome(got, journal.Dropped{}) {
					k.Errorf("delivery outcome = %#v, want dropped", got)
				}
			})
		})
	}
}
