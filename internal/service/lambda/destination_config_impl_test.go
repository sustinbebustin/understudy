package lambda_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/events"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/s3"
	"github.com/sustinbebustin/understudy/internal/service/sns"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
)

// The existence check asks the destination services themselves, not the
// stubs of destination_config_test.go: a queue, topic, bus and bucket that
// were created are taken, the default bus among them, a missing bus too,
// and a missing queue, topic or bucket is refused in AWS's words
// (aws-recorded, lambda-destinations-layers.md 2.3: LDL-08 01 to 04;
// lambda-api.md 7.7).
func TestEventInvokeConfigFindsDestinationsInTheirServices(t *testing.T) {
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
		sqs.New(k.Deps(sqsapi.Service()), sqs.Options{})
		sns.New(k.Deps(snsapi.Service()), sns.Options{})
		events.New(k.Deps(eventbridgeapi.Service()), events.Options{})
		s3Deps := k.Deps(s3api.Service())
		s3Deps.Blob = blobs
		s3.New(s3Deps, s3.Options{})
		h := &harness{K: k, svc: lambda.New(deps, lambda.Options{})}
		t := h.T
		existenceChecked(h)
		h.createFunction("fn")
		call(h, sqsapi.OpCreateQueue, &sqsapi.CreateQueueInput{QueueName: "on-success"})
		call(h, snsapi.OpCreateTopic, &snsapi.CreateTopicInput{Name: "alerts"})
		call(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: "audit"})
		call(h, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "audit-records"})

		const defaultBus = "arn:aws:events:us-east-1:000000000000:event-bus/default"
		for _, c := range []struct{ onSuccess, onFailure string }{
			{successQueue, auditBucket},
			{alertsTopic, auditBus},
			{defaultBus, ""},
			{"arn:aws:events:us-east-1:000000000000:event-bus/missing-bus", ""},
		} {
			out := call(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations(c.onSuccess, c.onFailure))
			if deref(out.DestinationConfig.OnSuccess.Destination) != c.onSuccess || deref(out.DestinationConfig.OnFailure.Destination) != c.onFailure {
				t.Errorf("Put %s, %s = %+v, want both stored", c.onSuccess, c.onFailure, out.DestinationConfig)
			}
		}
		for _, missing := range []struct{ arn, text string }{
			{"arn:aws:sqs:us-east-1:000000000000:missing-queue", "The destination ARN arn:aws:sqs:us-east-1:000000000000:missing-queue is invalid."},
			{"arn:aws:sns:us-east-1:000000000000:missing-topic", "The destination ARN arn:aws:sns:us-east-1:000000000000:missing-topic is invalid."},
			{"arn:aws:s3:::missing-bucket", "Error occurred while ListBucket. S3 Error Code: 404 Not Found. S3 Error Message: Not Found"},
		} {
			errorIs(t, fail(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations("", missing.arn)), "InvalidParameterValueException", missing.text)
		}
	}, kerneltest.Services(lambdaapi.Service(), sqsapi.Service(), snsapi.Service(), eventbridgeapi.Service(), s3api.Service()))
}

// A function of the same name in another account is not the function
// itself, which AWS refuses in other words (lambda-api.md 7.7): it is
// looked up in its own account, where it does not exist.
func TestEventInvokeConfigFindsADestinationFunctionInItsOwnAccount(t *testing.T) {
	runLookups(t, func(h *harness, _ *lookups) {
		h.createFunction("fn")
		const elsewhere = "arn:aws:lambda:us-east-1:111111111111:function:fn"
		errorIs(h.T, fail(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations(elsewhere, "")),
			"InvalidParameterValueException", "The destination ARN "+elsewhere+" is invalid.")
		errorIs(h.T, fail(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations(fnUnqualified+":live", "")),
			"InvalidParameterValueException", "You can't specify the function as a destination for itself.")
	})
}

// A missing destination function on success is refused before the
// failure side's queue is looked up, as OnSuccess is checked before
// OnFailure whatever the destinations' kinds.
func TestEventInvokeConfigChecksAFunctionBeforeTheNextSide(t *testing.T) {
	runLookups(t, func(h *harness, seen *lookups) {
		existenceChecked(h)
		h.createFunction("fn")
		const missingFn = "arn:aws:lambda:us-east-1:000000000000:function:missing"
		errorIs(h.T, fail(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations(missingFn, "arn:aws:sqs:us-east-1:000000000000:missing-queue")),
			"InvalidParameterValueException", "The destination ARN "+missingFn+" is invalid.")
		if got := seen.all(); len(got) != 0 {
			h.Errorf("lookups = %+v, want none once OnSuccess is refused", got)
		}
	})
}
