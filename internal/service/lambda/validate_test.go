package lambda_test

import (
	"archive/zip"
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// recordedRuntimes is the runtime enum of the recorded creation errors
// (lambda-api.md 4.2, recorded 2026-01-12).
const recordedRuntimes = "[nodejs20.x, python3.14, provided.al2023, python3.12, python3.13, nodejs24.x, nodejs22.x, java25, python3.10, python3.11, java21, ruby3.3, ruby3.4, ruby3.2, python3.8, python3.9, java17, nodejs16.x, dotnet10, dotnet8, java11, dotnet6, nodejs18.x, provided.al2, java8.al2]"

// golden is one recorded validation answer: the error's shape, HTTP
// status, message and whether it carries Lambda's Type: User member.
type golden struct {
	name    string
	shape   string
	status  int
	message string
	user    bool
}

func (g golden) check(t *testing.T, err *awserr.Error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error, want %s", g.name, g.shape)
	}
	if err.ID.Shape.Name() != g.shape || err.ID.Status != g.status || err.Message != g.message {
		t.Errorf("%s:\n got %s %d %q\nwant %s %d %q", g.name, err.ID.Shape.Name(), err.ID.Status, err.Message, g.shape, g.status, g.message)
	}
	hasUser := false
	for _, f := range err.Fields {
		hasUser = hasUser || (f.Name == "Type" && f.Value == "User")
	}
	if hasUser != g.user {
		t.Errorf("%s: Type User = %t, want %t", g.name, hasUser, g.user)
	}
}

// TestCreateFunctionValidationRows: every validation row of lambda-api.md
// 4.1 to 4.3 answers with the recorded identity and text.
func TestCreateFunctionValidationRows(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("exists")
		rows := []struct {
			golden
			edit func(in *lambdaapi.CreateFunctionInput)
		}{
			{
				golden{
					"role pattern", "ValidationException", 400,
					`1 validation error detected: Value 'r1' at 'role' failed to satisfy constraint: Member must satisfy regular expression pattern: arn:(aws[a-zA-Z-]*)?:iam::\d{12}:role/?[a-zA-Z_0-9+=,.@\-_/]+`, false,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.Role = "r1" },
			},
			{
				golden{
					"unknown runtime", "InvalidParameterValueException", 400,
					"Value non-existent-runtime at 'runtime' failed to satisfy constraint: Member must satisfy enum value set: " + recordedRuntimes + " or be a valid ARN", true,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.Runtime = new(lambdaapi.Runtime("non-existent-runtime")) },
			},
			{
				golden{
					"uppercase runtime", "InvalidParameterValueException", 400,
					"Value PYTHON3.9 at 'runtime' failed to satisfy constraint: Member must satisfy enum value set: " + recordedRuntimes + " or be a valid ARN", true,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.Runtime = new(lambdaapi.Runtime("PYTHON3.9")) },
			},
			{
				golden{
					"deprecated runtime", "InvalidParameterValueException", 400,
					"The runtime parameter of go1.x is no longer supported for creating or updating AWS Lambda functions. We recommend you use a supported runtime while creating or updating functions.", true,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.Runtime = new(lambdaapi.RuntimeGo1x) },
			},
			{
				golden{
					"two architectures", "ValidationException", 400,
					"1 validation error detected: Value '[x86_64, arm64]' at 'architectures' failed to satisfy constraint: Member must have length less than or equal to 1", false,
				},
				func(in *lambdaapi.CreateFunctionInput) {
					in.Architectures = []lambdaapi.Architecture{lambdaapi.ArchitectureX8664, lambdaapi.ArchitectureArm64}
				},
			},
			{
				golden{
					"uppercase architecture", "ValidationException", 400,
					"1 validation error detected: Value '[X86_64]' at 'architectures' failed to satisfy constraint: Member must satisfy constraint: [Member must satisfy enum value set: [x86_64, arm64], Member must not be null]", false,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.Architectures = []lambdaapi.Architecture{"X86_64"} },
			},
			{
				golden{
					"name over 64", "InvalidParameterValueException", 400,
					"1 validation error detected: Value '" + strings.Repeat("a", 65) + "' at 'functionName' failed to satisfy constraint: Member must have length less than or equal to 64", true,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.FunctionName = strings.Repeat("a", 65) },
			},
			{
				golden{
					"timeout over 900", "ValidationException", 400,
					"1 validation error detected: Value '901' at 'timeout' failed to satisfy constraint: Member must have value less than or equal to 900", false,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.Timeout = new(int32(901)) },
			},
			{
				golden{
					"invalid zip", "InvalidParameterValueException", 400,
					"Could not unzip uploaded file. Please check your file, then try to upload again.", true,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.Code.ZipFile = []byte("not a zip") },
			},
			{
				golden{
					"unzipped over the limit", "InvalidParameterValueException", 400,
					"Unzipped size must be smaller than 262144000 bytes", true,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.Code.ZipFile = claimedZip(t, 262_144_001) },
			},
			{
				golden{
					"zipped over the limit", "RequestEntityTooLargeException", 413,
					"Zipped size must be smaller than 52428800 bytes", false,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.Code.ZipFile = make([]byte, 52_428_801) },
			},
			{
				golden{
					"environment over 4 KB", "InvalidParameterValueException", 400,
					`Lambda was unable to configure your environment variables because the environment variables you have provided exceeded the 4KB limit. String measured: {"BIG":"` + strings.Repeat("x", 4096) + `"}`, true,
				},
				func(in *lambdaapi.CreateFunctionInput) {
					in.Environment = &lambdaapi.Environment{Variables: map[string]string{"BIG": strings.Repeat("x", 4096)}}
				},
			},
			{
				golden{
					"reserved environment keys", "InvalidParameterValueException", 400,
					"Lambda was unable to configure your environment variables because the environment variables you have provided contains reserved keys that are currently not supported for modification. Reserved keys used in this request: AWS_LAMBDA_FUNCTION_NAME,AWS_REGION", true,
				},
				func(in *lambdaapi.CreateFunctionInput) {
					in.Environment = &lambdaapi.Environment{Variables: map[string]string{"AWS_REGION": "x", "AWS_LAMBDA_FUNCTION_NAME": "x", "MINE": "x"}}
				},
			},
			{
				golden{
					"memory over 10240", "ValidationException", 400,
					"1 validation error detected: Value '10241' at 'memorySize' failed to satisfy constraint: Member must have value less than or equal to 10240", false,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.MemorySize = new(int32(10241)) },
			},
			{
				golden{
					"ephemeral storage over 10240", "ValidationException", 400,
					"1 validation error detected: Value '10241' at 'ephemeralStorage.size' failed to satisfy constraint: Member must have value less than or equal to 10240", false,
				},
				func(in *lambdaapi.CreateFunctionInput) {
					in.EphemeralStorage = &lambdaapi.EphemeralStorage{Size: 10241}
				},
			},
			{
				golden{"image with a runtime", "InvalidParameterValueException", 400, "Please don't provide Handler or Runtime or Layer when the intended function PackageType is Image.", true},
				func(in *lambdaapi.CreateFunctionInput) {
					asImage(in)
					in.Runtime = new(lambdaapi.RuntimeProvidedal2023)
				},
			},
			{
				golden{"image with a handler", "InvalidParameterValueException", 400, "Please don't provide Handler or Runtime or Layer when the intended function PackageType is Image.", true},
				func(in *lambdaapi.CreateFunctionInput) {
					asImage(in)
					in.Handler = new("bootstrap")
				},
			},
			{
				golden{"image with a layer", "InvalidParameterValueException", 400, "Please don't provide Handler or Runtime or Layer when the intended function PackageType is Image.", true},
				func(in *lambdaapi.CreateFunctionInput) {
					asImage(in)
					in.Layers = []string{"arn:aws:lambda:us-east-1:000000000000:layer:l:1"}
				},
			},
			{
				golden{"zip without a runtime", "InvalidParameterValueException", 400, "Runtime and Handler are mandatory parameters for functions created with deployment packages.", true},
				func(in *lambdaapi.CreateFunctionInput) { in.Runtime = nil },
			},
			{
				golden{"zip without a handler", "InvalidParameterValueException", 400, "Runtime and Handler are mandatory parameters for functions created with deployment packages.", true},
				func(in *lambdaapi.CreateFunctionInput) { in.Handler = nil },
			},
			{
				golden{
					"handler pattern", "ValidationException", 400,
					`1 validation error detected: Value 'my handler' at 'handler' failed to satisfy constraint: Member must satisfy regular expression pattern: [^\s]+`, false,
				},
				func(in *lambdaapi.CreateFunctionInput) { in.Handler = new("my handler") },
			},
			{
				golden{"six layers", "InvalidParameterValueException", 400, "Cannot reference more than 5 layers.", true},
				func(in *lambdaapi.CreateFunctionInput) {
					for i := range 6 {
						in.Layers = append(in.Layers, "arn:aws:lambda:us-east-1:000000000000:layer:l:"+string(rune('1'+i)))
					}
				},
			},
			{
				golden{"tag limit", "InvalidParameterValueException", 400, "Number of tags exceeds resource tag limit.", true},
				func(in *lambdaapi.CreateFunctionInput) {
					in.Tags = map[string]string{}
					for i := range 51 {
						in.Tags["k"+strings.Repeat("x", i)] = "v"
					}
				},
			},
			{
				golden{"duplicate", "ResourceConflictException", 409, "Function already exist: exists", true},
				func(in *lambdaapi.CreateFunctionInput) { in.FunctionName = "exists" },
			},
		}
		for _, row := range rows {
			in := zipFunction(h, "fn")
			row.edit(in)
			row.check(t, fail(h, lambdaapi.OpCreateFunction, in))
		}
	})
}

// asImage turns a zip function's input into an image function's, without
// runtime or handler.
func asImage(in *lambdaapi.CreateFunctionInput) {
	in.PackageType = new(lambdaapi.PackageTypeImage)
	in.Code = lambdaapi.FunctionCode{ImageUri: new("local/fn:latest")}
	in.Runtime, in.Handler = nil, nil
}

// claimedZip is a zip whose single stored entry claims an uncompressed
// size of n bytes; Lambda's limit reads the claim.
func claimedZip(tb testing.TB, n uint64) []byte {
	tb.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.CreateRaw(&zip.FileHeader{Name: "big", Method: zip.Store, CompressedSize64: 1, UncompressedSize64: n})
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := f.Write([]byte{0}); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

// TestUpdateFunctionCodePackageTypeRows: the package type cannot change
// (lambda-api.md 4.4).
func TestUpdateFunctionCodePackageTypeRows(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		golden{"image uri on zip", "InvalidParameterValueException", 400, "Please don't provide ImageUri when updating a function with packageType Zip.", true}.
			check(t, fail(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ImageUri: new("repo:tag")}))
	})
}

// TestMappingValidationRows: every validation row of lambda-events.md 1.3
// answers with the recorded identity and text, destinations checked before
// the function.
func TestMappingValidationRows(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		long := zipFunction(h, "slow")
		long.Timeout = new(int32(900))
		call(h, lambdaapi.OpCreateFunction, long)
		queue := h.queue("q")
		call(h, sqsapi.OpCreateQueue, &sqsapi.CreateQueueInput{QueueName: "q.fifo", Attributes: map[sqsapi.QueueAttributeName]string{sqsapi.QueueAttributeNameFifoQueue: "true"}})
		fifo := "arn:aws:sqs:us-east-1:000000000000:q.fifo"
		existing := call(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(queue)})
		rows := []struct {
			golden
			in lambdaapi.CreateEventSourceMappingInput
		}{
			{
				golden{"no event source", "InvalidParameterValueException", 400, "Unrecognized event source.", true},
				lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn"},
			},
			{
				golden{"batch window", "InvalidParameterValueException", 400, "Maximum batch window in seconds must be greater than 0 if maximum batch size is greater than 10", true},
				lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(queue), BatchSize: new(int32(11))},
			},
			{
				golden{"FIFO batch size over 10", "InvalidParameterValueException", 400, "Batch size 11 is above 10, the maximum for an SQS FIFO queue.", true},
				lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(fifo), BatchSize: new(int32(11)), MaximumBatchingWindowInSeconds: new(int32(1))},
			},
			{
				golden{"scaling and provisioned pollers", "InvalidParameterValueException", 400, "ScalingConfig and ProvisionedPollerConfig cannot both be set on an event source mapping.", true},
				lambdaapi.CreateEventSourceMappingInput{
					FunctionName: "fn", EventSourceArn: new(queue),
					ScalingConfig: &lambdaapi.ScalingConfig{MaximumConcurrency: new(int32(2))}, ProvisionedPollerConfig: &lambdaapi.ProvisionedPollerConfig{MinimumPollers: new(int32(1))},
				},
			},
			{
				golden{"destination before function", "InvalidParameterValueException", 400, "Unsupported DestinationConfig parameter for given event source mapping type.", true},
				lambdaapi.CreateEventSourceMappingInput{
					FunctionName: "doesnotexist", EventSourceArn: new(queue),
					DestinationConfig: &lambdaapi.DestinationConfig{OnSuccess: &lambdaapi.OnSuccess{Destination: new(queue)}},
				},
			},
			{
				golden{"missing function", "InvalidParameterValueException", 400, "Function does not exist", true},
				lambdaapi.CreateEventSourceMappingInput{FunctionName: "doesnotexist", EventSourceArn: new(queue)},
			},
			{
				golden{
					"missing queue", "InvalidParameterValueException", 400,
					"Error occurred while ReceiveMessage. SQS Error Code: AWS.SimpleQueueService.NonExistentQueue. SQS Error Message: The specified queue does not exist.", true,
				},
				lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new("arn:aws:sqs:us-east-1:000000000000:nope")},
			},
			{
				golden{"visibility below timeout", "InvalidParameterValueException", 400, "Queue visibility timeout: 30 seconds is less than Function timeout: 900 seconds", true},
				lambdaapi.CreateEventSourceMappingInput{FunctionName: "slow", EventSourceArn: new(queue)},
			},
			{
				golden{
					"duplicate", "ResourceConflictException", 409,
					`An event source mapping with SQS arn (" ` + queue + ` ") and function (" fn ") already exists. Please update or delete the existing mapping with UUID ` + *existing.UUID, true,
				},
				lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(queue)},
			},
			{
				golden{
					"starting position enum", "ValidationException", 400,
					"1 validation error detected: Value 'invalid' at 'startingPosition' failed to satisfy constraint: Member must satisfy enum value set: [LATEST, AT_TIMESTAMP, TRIM_HORIZON]", false,
				},
				lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(queue), StartingPosition: new(lambdaapi.EventSourcePosition("invalid"))},
			},
		}
		for _, row := range rows {
			row.check(t, fail(h, lambdaapi.OpCreateEventSourceMapping, &row.in))
		}
		for _, pattern := range []*string{nil, new("simple string"), new(`{"eventSource":"aws:sqs"}`), new(`{"eventSource":[]}`)} {
			in := lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(queue), FilterCriteria: &lambdaapi.FilterCriteria{Filters: []lambdaapi.Filter{{Pattern: pattern}}}}
			golden{"filter", "InvalidParameterValueException", 400, "Invalid filter pattern definition.", true}.check(t, fail(h, lambdaapi.OpCreateEventSourceMapping, &in))
		}
		golden{"batch window on update", "InvalidParameterValueException", 400, "Maximum batch window in seconds must be greater than 0 if maximum batch size is greater than 10", true}.
			check(t, fail(h, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *existing.UUID, BatchSize: new(int32(100))}))
	})
}

// TestMappingQueueCheckWrapsSQSErrors: an SQS error other than a missing
// queue, here an injected RequestThrottled, reaches the client inside
// Lambda's InvalidParameterValueException with the SQS code and message,
// in the recorded form of the missing queue, never as an SQS error.
func TestMappingQueueCheckWrapsSQSErrors(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		queue := h.queue("q")
		if _, err := h.Faults.Add(fault.Rule{
			Match:   fault.Match{Operations: []ref.OperationID{sqsapi.OpGetQueueAttributes.Descriptor().ID}},
			Phase:   fault.Request,
			Action:  fault.Error{Shape: sqsapi.IdentityRequestThrottled().Shape, Message: "Request is throttled."},
			Trigger: fault.Always{},
		}); err != nil {
			t.Fatal(err)
		}
		golden{
			"throttled queue check", "InvalidParameterValueException", 400,
			"Error occurred while ReceiveMessage. SQS Error Code: RequestThrottled. SQS Error Message: Request is throttled.", true,
		}.check(t, fail(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(queue)}))
	})
}

// TestMappingFiltersAreCappedAtFive: a mapping takes up to five filters,
// the default quota, on create and on update; a sixth is refused and the
// mapping keeps the filters it had.
func TestMappingFiltersAreCappedAtFive(t *testing.T) {
	filters := func(n int) *lambdaapi.FilterCriteria {
		fc := &lambdaapi.FilterCriteria{Filters: []lambdaapi.Filter{}}
		for i := range n {
			fc.Filters = append(fc.Filters, lambdaapi.Filter{Pattern: new(`{"body":{"n":[` + strconv.Itoa(i) + `]}}`)})
		}
		return fc
	}
	tooMany := golden{"six filters", "InvalidParameterValueException", 400, "FilterCriteria has 6 filters, above the quota of 5 per event source mapping. Combine patterns or remove filters.", true}
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		tooMany.check(t, fail(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(h.queue("refused")), FilterCriteria: filters(6)}))
		m := call(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(h.queue("q")), FilterCriteria: filters(5)})
		if n := len(m.FilterCriteria.Filters); n != 5 {
			t.Errorf("created with %d filters, want 5", n)
		}
		tooMany.check(t, fail(h, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, FilterCriteria: filters(6)}))
		got := call(h, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *m.UUID})
		if n := len(got.FilterCriteria.Filters); n != 5 {
			t.Errorf("after the refused update the mapping has %d filters, want its 5", n)
		}
		call(h, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, FilterCriteria: filters(4)})
	})
}

// TestListRedactsTheFiltersOfAMappingWithAKMSKey: ListEventSourceMappings
// shows FilterCriteria as null for a mapping whose filters a customer key
// encrypts; Get and Delete show them (lambda-events.md 1.2).
func TestListRedactsTheFiltersOfAMappingWithAKMSKey(t *testing.T) {
	const pattern = `{"body":{"kind":["a"]}}`
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		criteria := &lambdaapi.FilterCriteria{Filters: []lambdaapi.Filter{{Pattern: new(pattern)}}}
		keyed := call(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{
			FunctionName: "fn", EventSourceArn: new(h.queue("keyed")), FilterCriteria: criteria,
			KMSKeyArn: new("arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab"),
		})
		plain := call(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(h.queue("plain")), FilterCriteria: criteria})
		for _, row := range call(h, lambdaapi.OpListEventSourceMappings, &lambdaapi.ListEventSourceMappingsInput{}).EventSourceMappings {
			switch *row.UUID {
			case *keyed.UUID:
				if row.FilterCriteria != nil {
					t.Errorf("List shows the keyed mapping's FilterCriteria %+v, want null", row.FilterCriteria)
				}
			case *plain.UUID:
				if row.FilterCriteria == nil || *row.FilterCriteria.Filters[0].Pattern != pattern {
					t.Errorf("List shows the plain mapping's FilterCriteria %+v, want %s", row.FilterCriteria, pattern)
				}
			}
		}
		got := call(h, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *keyed.UUID})
		deleted := call(h, lambdaapi.OpDeleteEventSourceMapping, &lambdaapi.DeleteEventSourceMappingInput{UUID: *keyed.UUID})
		for op, m := range map[string]*lambdaapi.EventSourceMappingConfiguration{"Get": got, "Delete": deleted} {
			if m.FilterCriteria == nil || *m.FilterCriteria.Filters[0].Pattern != pattern {
				t.Errorf("%s shows the keyed mapping's FilterCriteria %+v, want %s", op, m.FilterCriteria, pattern)
			}
		}
	})
}

// queue creates an SQS queue and returns its ARN.
func (h *harness) queue(name string) string {
	h.Helper()
	call(h, sqsapi.OpCreateQueue, &sqsapi.CreateQueueInput{QueueName: name})
	return "arn:aws:sqs:us-east-1:000000000000:" + name
}
