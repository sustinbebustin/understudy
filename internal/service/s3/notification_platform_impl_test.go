package s3_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// internalGate is a step 8 Authorizer that allows every client call and
// denies every internal call, recording each internal operation it is
// asked about.
type internalGate struct {
	mu    sync.Mutex
	asked []ref.OperationID
}

func (g *internalGate) Authorize(_ context.Context, c *kernel.Call, _ kernel.AuthzQuery) kernel.Verdict {
	if c.Mode() == nil {
		return kernel.Allowed{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, c.Op().ID)
	return kernel.Denied{Message: "internal calls are denied in this test"}
}

func (g *internalGate) questions() []ref.OperationID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]ref.OperationID(nil), g.asked...)
}

// AWS validates a notification destination against its stored policy
// (iam-enforcement-surfaces.md 8.3, 12.4), not through a call that policy
// grants, so understudy's existence lookup runs as the platform: step 8
// never asks about it. Under an Authorizer that denies every internal
// call, a configuration naming a missing queue, topic and function still
// answers S3's validation failure (AWS-recorded test_multiple_invalid_sqs_arns,
// lambda-does-not-exist; s3.md 13.1), not an InternalError from a denied
// lookup; each lookup is journaled as platform:s3.amazonaws.com and none is
// access-recorded.
func TestDestinationLookupsAreNeverAuthorized(t *testing.T) {
	gate := &internalGate{}
	kerneltest.Run(t, func(k *kerneltest.K) {
		logs := &bytes.Buffer{}
		d := k.Deps(s3api.Service())
		d.Log = slog.New(slog.NewTextHandler(logs, nil))
		h := &harness{K: k, svc: s3.New(d, s3.Options{}), logs: logs}
		k.Stub(sqsapi.OpGetQueueUrl, func(context.Context, *kernel.Call, *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
			return nil, sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
		})
		k.Stub(snsapi.OpGetTopicAttributes, func(context.Context, *kernel.Call, *snsapi.GetTopicAttributesInput) (*snsapi.GetTopicAttributesOutput, *awserr.Error) {
			return nil, snsapi.ErrNotFoundException("Topic does not exist")
		})
		k.Stub(lambdaapi.OpGetFunction, func(_ context.Context, _ *kernel.Call, in *lambdaapi.GetFunctionInput) (*lambdaapi.GetFunctionOutput, *awserr.Error) {
			return nil, lambdaapi.ErrResourceNotFoundException("Function not found: " + in.FunctionName)
		})
		h.createBucket(notifyBucket)

		_, err := invoke(h, s3api.OpPutBucketNotificationConfiguration, &s3api.PutBucketNotificationConfigurationInput{
			Bucket: notifyBucket,
			NotificationConfiguration: s3api.NotificationConfiguration{
				QueueConfigurations:          []s3api.QueueConfiguration{{QueueArn: notifyQueue, Events: created()}},
				TopicConfigurations:          []s3api.TopicConfiguration{{TopicArn: notifyTopic, Events: []s3api.Event{s3api.EventS3Objectremoved}}},
				LambdaFunctionConfigurations: []s3api.LambdaFunctionConfiguration{{LambdaFunctionArn: notifyFunction, Events: []s3api.Event{s3api.EventS3Objecttagging}}},
			},
		})
		if err == nil || err.ID.QueryCode != "InvalidArgument" || err.Message != "Unable to validate the following destination configurations" {
			k.Fatalf("Put = %v, want the destination validation failure", err)
		}
		// notify.Parse orders the rules topic, queue, function.
		wantFields(k.T, err, map[string]string{
			"ArgumentName1": notifyTopic, "ArgumentValue1": "The destination topic does not exist",
			"ArgumentName2": notifyQueue, "ArgumentValue2": "The destination queue does not exist",
			"ArgumentName3": notifyFunction + ", null", "ArgumentValue3": "Not authorized to invoke function [" + notifyFunction + "]",
		})
		if asked := gate.questions(); len(asked) != 0 {
			k.Errorf("step 8 was asked about %v, want no destination lookup", asked)
		}

		for _, op := range []ref.OperationID{sqsapi.OpGetQueueUrl.Descriptor().ID, snsapi.OpGetTopicAttributes.Descriptor().ID, lambdaapi.OpGetFunction.Descriptor().ID} {
			events := k.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: op}).Events
			if len(events) != 1 {
				k.Errorf("journal holds %d %s calls, want one lookup", len(events), op)
				continue
			}
			call, isCall := events[0].Detail.(journal.APICall)
			if !isCall || call.Mode != "platform:s3.amazonaws.com" || call.Principal != "s3.amazonaws.com" || call.Origin != (ref.Internal{Via: ref.S3Notification}) {
				k.Errorf("%s was journaled as %+v, want an S3 notification lookup as the platform principal s3.amazonaws.com", op, events[0].Detail)
			}
		}
		if uses := k.Access.Usage(access.Filter{Principal: access.ServicePrincipal{Name: "s3.amazonaws.com"}}); len(uses) != 0 {
			k.Errorf("the lookups were access-recorded as %+v, want nothing", uses)
		}
	}, kerneltest.Services(s3api.Service(), sqsapi.Service(), snsapi.Service(), lambdaapi.Service()), kerneltest.Authorizer(gate))
}
