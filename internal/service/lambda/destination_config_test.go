package lambda_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
)

// runLookups runs fn against the Lambda control plane in a kernel whose
// SQS, SNS, EventBridge and S3 lookups are stubbed (stubDestinationLookups)
// and recorded in seen.
func runLookups(t *testing.T, fn func(h *harness, seen *lookups)) {
	t.Helper()
	seen := &lookups{}
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
		stubDestinationLookups(k, seen)
		fn(&harness{K: k, svc: lambda.New(deps, lambda.Options{})}, seen)
	}, kerneltest.Services(lambdaapi.Service(), sqsapi.Service(), snsapi.Service(), eventbridgeapi.Service(), s3api.Service()))
}

// putDestinations is a Put of fn's event-invoke configuration with the
// destinations given ("" for none).
func putDestinations(onSuccess, onFailure string) *lambdaapi.PutFunctionEventInvokeConfigInput {
	in := &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", DestinationConfig: &lambdaapi.DestinationConfig{}}
	if onSuccess != "" {
		in.DestinationConfig.OnSuccess = &lambdaapi.OnSuccess{Destination: new(onSuccess)}
	}
	if onFailure != "" {
		in.DestinationConfig.OnFailure = &lambdaapi.OnFailure{Destination: new(onFailure)}
	}
	return in
}

// existenceChecked fails the test unless the switch
// lambda.destination.config-existence-check says a queue, topic or bucket
// is looked up and a bus is not (aws-recorded, lambda-destinations-layers.md
// 2.3: LDL-08).
func existenceChecked(h *harness) {
	h.Helper()
	if v := h.switchValue("lambda.destination.config-existence-check"); v != "queue-topic-bucket" {
		h.Fatalf("lambda.destination.config-existence-check = %q, a value this test does not know", v)
	}
}

// A destination that does not exist is refused, and nothing is stored: a
// queue or topic with the text AWS answers for a missing destination
// function (lambda-api.md 7.7; aws-recorded, LDL-08 01 and 02), a bucket
// with S3's ListBucket refusal (LDL-08 04). Each destination is looked up
// in its own service, as Lambda on its own behalf over the destination
// mechanism (11.1: the execution role's permission is not checked here).
func TestEventInvokeConfigRefusesADestinationThatDoesNotExist(t *testing.T) {
	cases := []struct {
		name   string
		in     *lambdaapi.PutFunctionEventInvokeConfigInput
		text   string
		lookup string
	}{
		{"queue", putDestinations("arn:aws:sqs:us-east-1:000000000000:missing-queue", ""), "The destination ARN arn:aws:sqs:us-east-1:000000000000:missing-queue is invalid.", "GetQueueUrl missing-queue 000000000000"},
		{"topic", putDestinations("arn:aws:sns:us-east-1:000000000000:missing-topic", ""), "The destination ARN arn:aws:sns:us-east-1:000000000000:missing-topic is invalid.", "GetTopicAttributes arn:aws:sns:us-east-1:000000000000:missing-topic"},
		{"bucket", putDestinations("", "arn:aws:s3:::missing-bucket"), "Error occurred while ListBucket. S3 Error Code: 404 Not Found. S3 Error Message: Not Found", "HeadBucket missing-bucket"},
		{"another account's queue", putDestinations("", "arn:aws:sqs:us-east-1:111111111111:missing-queue"), "The destination ARN arn:aws:sqs:us-east-1:111111111111:missing-queue is invalid.", "GetQueueUrl missing-queue 111111111111"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runLookups(t, func(h *harness, seen *lookups) {
				t := h.T
				existenceChecked(h)
				h.createFunction("fn")
				err := fail(h, lambdaapi.OpPutFunctionEventInvokeConfig, c.in)
				errorIs(t, err, "InvalidParameterValueException", c.text)
				if err.ID.Status != 400 {
					t.Errorf("status = %d, want 400", err.ID.Status)
				}
				got := seen.all()
				if len(got) != 1 || got[0].op+" "+got[0].in != c.lookup {
					t.Fatalf("lookups = %+v, want one: %s", got, c.lookup)
				}
				if got[0].principal != (identity.ServicePrincipal{Name: "lambda.amazonaws.com"}) || got[0].origin != (ref.Internal{Via: ref.LambdaDestination}) {
					t.Errorf("lookup by %v from %v, want lambda.amazonaws.com over the Lambda destination mechanism", got[0].principal, got[0].origin)
				}
				errorIs(t, fail(h, lambdaapi.OpGetFunctionEventInvokeConfig, &lambdaapi.GetFunctionEventInvokeConfigInput{FunctionName: "fn"}),
					"ResourceNotFoundException", "The function arn:aws:lambda:us-east-1:000000000000:function:fn:$LATEST doesn't have an EventInvokeConfig")
			})
		})
	}
}

// Destinations that exist are stored and echoed, a FIFO topic among them
// (whether AWS refuses one at configuration is unrecorded,
// lambda-destinations-layers.md 2.2). A bus is not looked up: AWS accepts
// a missing one (aws-recorded, LDL-08 03).
func TestEventInvokeConfigTakesDestinationsThatExist(t *testing.T) {
	runLookups(t, func(h *harness, seen *lookups) {
		t := h.T
		existenceChecked(h)
		h.createFunction("fn")
		for _, c := range []struct{ onSuccess, onFailure string }{
			{successQueue, auditBucket},
			{alertsTopic, auditBus},
			{"arn:aws:sns:us-east-1:000000000000:alerts.fifo", "arn:aws:events:us-east-1:000000000000:event-bus/missing-bus"},
		} {
			out := call(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations(c.onSuccess, c.onFailure))
			if out.DestinationConfig == nil || out.DestinationConfig.OnSuccess == nil || out.DestinationConfig.OnFailure == nil ||
				deref(out.DestinationConfig.OnSuccess.Destination) != c.onSuccess || deref(out.DestinationConfig.OnFailure.Destination) != c.onFailure {
				t.Errorf("Put %s, %s = %+v, want both stored", c.onSuccess, c.onFailure, out.DestinationConfig)
			}
		}
		if n := len(seen.all()); n != 4 {
			t.Errorf("%d lookups, want one per destination that is not a bus", n)
		}
	})
}

// A FIFO queue is refused at configuration in the words of a destination
// config Lambda cannot take, before any lookup (aws-recorded,
// lambda-destinations-layers.md 2.2: LDL-08 05).
func TestEventInvokeConfigRefusesAFIFOQueue(t *testing.T) {
	runLookups(t, func(h *harness, seen *lookups) {
		t := h.T
		existenceChecked(h)
		h.createFunction("fn")
		const fifo = "arn:aws:sqs:us-east-1:000000000000:records.fifo"
		errorIs(t, fail(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations("", fifo)),
			"InvalidParameterValueException", "The provided destination config DestinationConfig(onSuccess=null, onFailure=OnFailure(destination="+fifo+")) is invalid.")
		if n := len(seen.all()); n != 0 {
			t.Errorf("%d lookups for a FIFO queue, want none", n)
		}
	})
}

// UpdateFunctionEventInvokeConfig with one side of DestinationConfig keeps
// the other side stored (aws-recorded, lambda-destinations-layers.md 2.1:
// LDL-08 07 and 08).
func TestUpdateEventInvokeConfigMergesDestinationsPerSide(t *testing.T) {
	runLookups(t, func(h *harness, _ *lookups) {
		t := h.T
		h.createFunction("fn")
		call(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations(successQueue, auditBucket))
		out := call(h, lambdaapi.OpUpdateFunctionEventInvokeConfig, &lambdaapi.UpdateFunctionEventInvokeConfigInput{
			FunctionName: "fn", DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(alertsTopic)}},
		})
		if out.DestinationConfig == nil || out.DestinationConfig.OnSuccess == nil || deref(out.DestinationConfig.OnSuccess.Destination) != successQueue ||
			out.DestinationConfig.OnFailure == nil || deref(out.DestinationConfig.OnFailure.Destination) != alertsTopic {
			t.Errorf("Update with OnFailure only = %+v, want OnSuccess %s kept and OnFailure %s", out.DestinationConfig, successQueue, alertsTopic)
		}
	})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// A lookup that fails for another reason than the destination's absence
// fails the call without storing anything: it is not evidence that the
// destination is missing.
func TestEventInvokeConfigLookupFailureStoresNothing(t *testing.T) {
	runLookups(t, func(h *harness, _ *lookups) {
		t := h.T
		existenceChecked(h)
		h.createFunction("fn")
		err := fail(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations("arn:aws:sqs:us-east-1:000000000000:broken-queue", ""))
		if err.ID.Status != 500 {
			t.Errorf("Put with a failing lookup = %d %s %q, want a 500", err.ID.Status, err.ID.Shape.Name(), err.Message)
		}
		errorIs(t, fail(h, lambdaapi.OpGetFunctionEventInvokeConfig, &lambdaapi.GetFunctionEventInvokeConfigInput{FunctionName: "fn"}),
			"ResourceNotFoundException", "The function arn:aws:lambda:us-east-1:000000000000:function:fn:$LATEST doesn't have an EventInvokeConfig")
	})
}

// OnSuccess is checked before OnFailure, and an ARN of a service Lambda
// does not deliver to keeps AWS's recorded text without any lookup
// (lambda-destinations-layers.md 2.2: test_lambda_api.snapshot.json:L4342-L4353).
func TestEventInvokeConfigChecksInOrder(t *testing.T) {
	runLookups(t, func(h *harness, seen *lookups) {
		t := h.T
		existenceChecked(h)
		h.createFunction("fn")
		const missingQueue, missingTopic = "arn:aws:sqs:us-east-1:000000000000:missing-a", "arn:aws:sns:us-east-1:000000000000:missing-b"
		errorIs(t, fail(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations(missingQueue, missingTopic)),
			"InvalidParameterValueException", "The destination ARN "+missingQueue+" is invalid.")
		before := len(seen.all())
		const table = "arn:aws:dynamodb:us-east-1:000000000000:table/orders"
		errorIs(t, fail(h, lambdaapi.OpPutFunctionEventInvokeConfig, putDestinations(table, "")),
			"InvalidParameterValueException", "The provided destination config DestinationConfig(onSuccess=OnSuccess(destination="+table+"), onFailure=null) is invalid.")
		if n := len(seen.all()) - before; n != 0 {
			t.Errorf("%d lookups for a DynamoDB ARN, want none", n)
		}
	})
}

// UpdateFunctionEventInvokeConfig checks its destinations as Put does.
func TestUpdateEventInvokeConfigRefusesADestinationThatDoesNotExist(t *testing.T) {
	runLookups(t, func(h *harness, _ *lookups) {
		t := h.T
		existenceChecked(h)
		h.createFunction("fn")
		call(h, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", MaximumRetryAttempts: new(int32(1))})
		const missing = "arn:aws:sns:us-east-1:000000000000:missing-topic"
		errorIs(t, fail(h, lambdaapi.OpUpdateFunctionEventInvokeConfig, &lambdaapi.UpdateFunctionEventInvokeConfigInput{
			FunctionName: "fn", DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(missing)}},
		}), "InvalidParameterValueException", "The destination ARN "+missing+" is invalid.")
	})
}

// UpdateFunctionConfiguration with DeadLetterConfig {} leaves the
// dead-letter queue in place, as AWS's recorded update response shows
// (lambda-destinations-layers.md finding 2, 11.5: test_lambda_destinations.snapshot.json:L274-L282);
// {"TargetArn": ""} clears it, which Terraform sends to remove a queue and
// then reads back as none (9.2).
func TestDeadLetterConfigWithoutATargetKeepsTheQueue(t *testing.T) {
	runLookups(t, func(h *harness, _ *lookups) {
		t := h.T
		in := zipFunction(h, "fn")
		in.DeadLetterConfig = &lambdaapi.DeadLetterConfig{TargetArn: new(dlqA)}
		call(h, lambdaapi.OpCreateFunction, in)
		h.settle()
		updated := call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", DeadLetterConfig: &lambdaapi.DeadLetterConfig{}})
		if updated.DeadLetterConfig == nil || deref(updated.DeadLetterConfig.TargetArn) != dlqA {
			t.Errorf("update with DeadLetterConfig {} = %+v, want the queue kept", updated.DeadLetterConfig)
		}
		h.settle()
		got := call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"})
		if got.DeadLetterConfig == nil || deref(got.DeadLetterConfig.TargetArn) != dlqA {
			t.Errorf("configuration after DeadLetterConfig {} = %+v, want the queue kept", got.DeadLetterConfig)
		}
		call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", DeadLetterConfig: &lambdaapi.DeadLetterConfig{TargetArn: new("")}})
		h.settle()
		got = call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"})
		if got.DeadLetterConfig != nil && deref(got.DeadLetterConfig.TargetArn) != "" {
			t.Errorf("configuration after TargetArn \"\" = %+v, want no queue", got.DeadLetterConfig)
		}
	})
}

// A dead-letter target that is missing, a FIFO queue or of a service other
// than SQS or SNS is refused by CreateFunction and UpdateFunctionConfiguration
// in AWS's recorded words, and nothing is stored
// (lambda-destinations-layers.md 5.1: testdata/parity/lambda/LDL-08/09-dlq-missing.json,
// 10-dlq-fifo.json, 11-dlq-bus.json; recorded on UpdateFunctionConfiguration,
// CreateFunction's answer inferred to be the same). A FIFO queue and a bus
// are refused before any lookup.
func TestDeadLetterTargetIsCheckedAtConfiguration(t *testing.T) {
	cases := []struct {
		name, target, text string
		lookups            int
	}{
		{
			"missing queue", "arn:aws:sqs:us-east-1:000000000000:missing-dlq",
			"Error occurred while SendMessage. SQS Error Code: AWS.SimpleQueueService.NonExistentQueue. SQS Error Message: The specified queue does not exist.", 1,
		},
		{
			"FIFO queue", "arn:aws:sqs:us-east-1:000000000000:fn-dlq.fifo",
			"Invalid dead letter queue ARN: FIFO SQS queue is not supported for dead letter configuration", 0,
		},
		{
			"event bus", "arn:aws:events:us-east-1:000000000000:event-bus/default",
			"Invalid dead letter queue ARN: The service specified by the TargetArn is not supported for dead letter configuration.", 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name+" on create", func(t *testing.T) {
			runLookups(t, func(h *harness, seen *lookups) {
				t := h.T
				in := zipFunction(h, "fn")
				in.DeadLetterConfig = &lambdaapi.DeadLetterConfig{TargetArn: new(c.target)}
				err := fail(h, lambdaapi.OpCreateFunction, in)
				errorIs(t, err, "InvalidParameterValueException", c.text)
				userFault(t, err)
				if n := len(seen.all()); n != c.lookups {
					t.Errorf("%d lookups, want %d", n, c.lookups)
				}
				errorIs(t, fail(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"}),
					"ResourceNotFoundException", "Function not found: "+fnUnqualified)
			})
		})
		t.Run(c.name+" on update", func(t *testing.T) {
			runLookups(t, func(h *harness, seen *lookups) {
				t := h.T
				in := zipFunction(h, "fn")
				in.DeadLetterConfig = &lambdaapi.DeadLetterConfig{TargetArn: new(dlqA)}
				call(h, lambdaapi.OpCreateFunction, in)
				h.settle()
				before := len(seen.all())
				err := fail(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{
					FunctionName: "fn", DeadLetterConfig: &lambdaapi.DeadLetterConfig{TargetArn: new(c.target)},
				})
				errorIs(t, err, "InvalidParameterValueException", c.text)
				userFault(t, err)
				if n := len(seen.all()) - before; n != c.lookups {
					t.Errorf("%d lookups, want %d", n, c.lookups)
				}
				got := call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"})
				if got.DeadLetterConfig == nil || deref(got.DeadLetterConfig.TargetArn) != dlqA {
					t.Errorf("configuration after the refused update = %+v, want %s kept", got.DeadLetterConfig, dlqA)
				}
			})
		})
	}
}

// A dead-letter topic that does not exist is refused, as a missing queue
// is, after a lookup in SNS. AWS's text for it is unrecorded
// (lambda-destinations-layers.md 5.1), so the expectation reads
// understudy's choice, inferred from the queue's recorded text with SNS's
// NotFound answer, as its own constant rather than from a capture.
func TestDeadLetterTopicThatDoesNotExistIsRefused(t *testing.T) {
	runLookups(t, func(h *harness, seen *lookups) {
		t := h.T
		const missing = "arn:aws:sns:us-east-1:000000000000:missing-topic"
		in := zipFunction(h, "fn")
		in.DeadLetterConfig = &lambdaapi.DeadLetterConfig{TargetArn: new(missing)}
		err := fail(h, lambdaapi.OpCreateFunction, in)
		errorIs(t, err, "InvalidParameterValueException", "Error occurred while Publish. SNS Error Code: NotFound. SNS Error Message: Topic does not exist")
		userFault(t, err)
		got := seen.all()
		if len(got) != 1 || got[0].op+" "+got[0].in != "GetTopicAttributes "+missing {
			t.Errorf("lookups = %+v, want one GetTopicAttributes of %s", got, missing)
		}
	})
}

// userFault fails the test unless err is a 400 carrying Type User, as every
// recorded dead-letter refusal does (testdata/parity/lambda/LDL-08/notes.md).
func userFault(t *testing.T, err *awserr.Error) {
	t.Helper()
	if err.ID.Status != 400 || !slices.Contains(err.Fields, awserr.Field{Name: "Type", Value: "User"}) {
		t.Errorf("refusal = %d with fields %v, want 400 with Type User", err.ID.Status, err.Fields)
	}
}

// The operations this unit binds or changes carry the coverage marks and
// notes of lambda-destinations-layers.md 11.1, verbatim (the CreateFunction
// and UpdateFunctionConfiguration notes keep the TenancyConfig and
// DurableConfig refusals 11.1 predates). The event invoke configuration
// notes lead with the configuration-time role check, recorded for Put and
// inferred for Update (iam-enforcement-surfaces.md 8.5). The CreateFunction
// and UpdateFunctionConfiguration notes end with the iam:PassRole and
// execution role trust checks, the trust check recorded for Create and
// inferred for Update (iam-enforcement-surfaces.md 13.28), then the
// dead-letter target checks, recorded for Update and inferred for Create
// (lambda-destinations-layers.md 5.1).
// AddLayerVersionPermission is partial since another account's ungranted
// use of a version is refused (lambda-destinations-layers.md 7.4).
func TestDestinationAndLayerCoverageMarks(t *testing.T) {
	const (
		trust       = " under iam.enforce=enforce the caller needs iam:PassRole on the execution role, asked with iam:PassedToService lambda.amazonaws.com, then the role's trust policy must let lambda.amazonaws.com assume it"
		trustModes  = ", else InvalidParameterValueException; under advise both are asked and a denial journaled, under off neither"
		trustUpdate = " (inferred: only CreateFunction's trust check is recorded)"
		deadLetter  = "; a dead-letter target must be an existing standard SQS queue or an SNS topic, and under iam.enforce=enforce the execution role must be allowed sqs:SendMessage or sns:Publish on it (advise journals a denial, off asks nothing); the answers for a missing topic and for the role's sns:Publish are inferred"
		layerFound  = "a layer version is found only if it was published to understudy, which never fetches layers from AWS; "
		location    = "Content.Location is a link understudy serves the archive at, not a presigned S3 URL"
		roleChecked = "the execution role's sns:Publish on an SNS destination and sqs:SendMessage on an SQS one are checked when the configuration is stored"
		roleCheck   = ", under iam.enforce=enforce, which refuses it, or advise, which journals the denial; function, event bus and S3 bucket destinations are not checked there (unrecorded); "
		delivery    = "the execution role's permission to reach a destination, and the resource policy of a destination in another account, are evaluated only under iam.enforce=advise, which journals each denial, or enforce, which fails the delivery; a failed delivery is journaled and not retried"
	)
	want := map[string]registry.BindCoverage{
		"PublishLayerVersion":  registry.Partial{Note: "S3ObjectStorageMode REFERENCE is stored and the archive is copied as with COPY; the account code-storage quota is not enforced; layers are never signed"},
		"GetLayerVersion":      registry.Partial{Note: location},
		"GetLayerVersionByArn": registry.Partial{Note: location},
		"ListLayers":           registry.Full{},
		"ListLayerVersions":    registry.Full{},
		"DeleteLayerVersion":   registry.Full{},
		"AddLayerVersionPermission": registry.Partial{
			Note: "a statement for an account, for everyone or for an organization lets another account attach and read the version; " +
				"an organization statement lets every account, since the caller's organization is unknown",
		},
		"RemoveLayerVersionPermission": registry.Full{},
		"GetLayerVersionPolicy":        registry.Full{},
		"CreateFunction": registry.Partial{
			Note: layerFound + "PublishTo, TenancyConfig and DurableConfig are refused; code signing and VPC networking are stored only;" + trust + trustModes + deadLetter +
				" (inferred: only UpdateFunctionConfiguration's dead-letter checks are recorded)",
		},
		"UpdateFunctionConfiguration": registry.Partial{
			Note: layerFound + "DurableConfig is refused; code signing and VPC networking are stored only; when the update passes a Role," + trust + trustUpdate + trustModes + deadLetter,
		},
		"PutFunctionEventInvokeConfig":    registry.Partial{Note: roleChecked + roleCheck + delivery},
		"UpdateFunctionEventInvokeConfig": registry.Partial{Note: roleChecked + " (inferred: only PutFunctionEventInvokeConfig's check is recorded)" + roleCheck + delivery},
	}
	runLookups(t, func(h *harness, _ *lookups) {
		for name, cov := range want {
			coverageIs(h.K, name, cov)
		}
	})
}

// coverageIs checks that lambda.name is bound with cov.
func coverageIs(k *kerneltest.K, name string, cov registry.BindCoverage) {
	k.Helper()
	op, ok := k.Kernel.Registry().Operation(ref.OperationID{Service: "lambda", Name: name})
	if !ok {
		k.Errorf("no operation lambda.%s", name)
		return
	}
	bound, isBound := k.Kernel.Bindings().Status(op).(kernel.Bound)
	if !isBound {
		k.Errorf("lambda.%s is unbound, want %#v", name, cov)
		return
	}
	if bound.Coverage != cov {
		k.Errorf("lambda.%s coverage = %#v\nwant %#v", name, bound.Coverage, cov)
	}
}

// Invoke's note drops the clause that asynchronous invocations deliver to
// no destination or dead-letter queue and keeps the others
// (lambda-destinations-layers.md 11.1).
func TestInvokeCoverageNoLongerDisclaimsDestinations(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		coverageIs(h.K, "Invoke", registry.Partial{Note: "an alias with weighted routing always runs its primary version; " +
			"LoggingConfig reaches the function as AWS_LAMBDA_LOG_FORMAT and AWS_LAMBDA_LOG_LEVEL, but Lambda's own INIT_START, START, END and REPORT lines stay text and SystemLogLevel filters nothing"})
	})
}

// Every behavior this unit chose without AWS-recorded evidence is a switch
// with the value, label and settling record of
// lambda-destinations-layers.md 11.2, a ref into that note and no parity
// scenario that settles it: the replayer skips Lambda's REST and Docker
// steps. The 2026-11-03 recordings made eight of them aws-recorded,
// cross-account-policy took LDL-15's refusal and sns-oversize took the
// behavior LDL-05 recorded.
func TestDestinationAndLayerSwitchesNameEveryUnrecordedChoice(t *testing.T) {
	type sw struct {
		value  string
		label  evidence.Label
		record string
		ops    []string
	}
	want := map[string]sw{
		"lambda.async.qualifier-config":                  {"invoked-qualifier-only", evidence.AWSRecorded, "LDL-01", []string{"lambda.Invoke", "lambda.PutFunctionEventInvokeConfig"}},
		"lambda.destination.record-function-arn":         {"invoked-qualifier", evidence.AWSRecorded, "LDL-01", []string{"lambda.Invoke"}},
		"lambda.destination.config-existence-check":      {"queue-topic-bucket", evidence.AWSRecorded, "LDL-08", []string{"lambda.PutFunctionEventInvokeConfig", "lambda.UpdateFunctionEventInvokeConfig"}},
		"lambda.destination.delivery-retry":              {"none", evidence.AWSRecorded, "LDL-09", []string{"lambda.Invoke"}},
		"lambda.destination.sns-oversize":                {"drop-payloads", evidence.AWSRecorded, "LDL-05", []string{"lambda.Invoke"}},
		"lambda.destination.lambda-invocation-type":      {"Event", evidence.AWSRecorded, "LDL-06", []string{"lambda.Invoke"}},
		"lambda.destination.eventbridge-resources-order": {"bus-then-function", evidence.AWSRecorded, "LDL-10", []string{"lambda.Invoke"}},
		"lambda.layers.merge-conflict":                   {"later-overwrites", evidence.AWSRecorded, "LDL-12", []string{"lambda.Invoke"}},
		"lambda.layers.compatibility":                    {"not-enforced", evidence.AWSRecorded, "LDL-13", []string{"lambda.CreateFunction", "lambda.UpdateFunctionConfiguration"}},
		"lambda.layers.cross-account-policy": {"refuse", evidence.AWSRecorded, "LDL-15", []string{
			"lambda.CreateFunction", "lambda.UpdateFunctionConfiguration", "lambda.GetLayerVersionByArn", "lambda.GetLayerVersion",
		}},
	}
	runLookups(t, func(h *harness, _ *lookups) {
		t := h.T
		got := map[string]fidelity.Entry{}
		for _, e := range h.svc.Switches() {
			got[e.Name] = e
		}
		for name, w := range want {
			e, ok := got[name]
			if !ok {
				t.Errorf("Switches() lists no %s", name)
				continue
			}
			if e.Value != w.value || e.Label != w.label || e.Record != w.record || !slices.Equal(e.Ops, w.ops) {
				t.Errorf("%s = %+v, want value %q, label %s, record %s, ops %v", name, e, w.value, w.label, w.record, w.ops)
			}
			if !strings.HasPrefix(e.Ref, "docs/research/lambda-destinations-layers.md#") {
				t.Errorf("%s Ref = %q, want an anchor in docs/research/lambda-destinations-layers.md", name, e.Ref)
			}
			if ns, ok := e.Settles.(fidelity.NoScenario); !ok || !strings.Contains(ns.Reason, w.record) {
				t.Errorf("%s Settles = %#v, want NoScenario naming %s", name, e.Settles, w.record)
			}
		}
	})
}
