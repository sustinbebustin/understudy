package sfn_test

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
)

func (h *harness) legacyTaskMachine(name, fn string) string {
	h.Helper()
	return h.create(name, fmt.Sprintf(`{"StartAt":"T","States":{"T":{"Type":"Task","Resource":%q,"End":true}}}`, fn))
}

func (h *harness) invokeTaskMachine(name, fn string) string {
	h.Helper()
	return h.create(name, fmt.Sprintf(`{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":%q,"Payload.$":"$"},"End":true}}}`, fn))
}

// A function ARN Task invokes the function synchronously, as the execution
// role, with the effective input; its payload is the task result
// (stepfunctions-api.md 9.2 legacy row; invoked AsExecutionRole{Role}).
func TestFunctionArnTask(t *testing.T) {
	run(t, func(h *harness) {
		fn := h.functionArn("worker")
		h.answer(func(_ *kernel.Call, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: []byte(`{"result":"ok"}`), ExecutedVersion: new("$LATEST")}, nil
		})
		arn := h.start(h.legacyTaskMachine("m", fn), "x", new(`{"in":1}`))
		calls := h.invoked()
		if len(calls) != 1 {
			h.Fatalf("Invoke calls = %d, want 1", len(calls))
		}
		in := calls[0].in
		if in.FunctionName != fn || !sameJSON(h, string(in.Payload), `{"in":1}`) {
			h.Errorf("Invoke input = %s %s, want the function with the effective input", in.FunctionName, in.Payload)
		}
		if in.InvocationType != nil && *in.InvocationType != lambdaapi.InvocationTypeRequestResponse {
			h.Errorf("InvocationType = %s, want RequestResponse", *in.InvocationType)
		}
		role, ok := calls[0].mode.(kernel.AsExecutionRole)
		if !ok || role.Role.String() != h.roleArn() {
			h.Errorf("invoked as %#v, want AsExecutionRole of %s", calls[0].mode, h.roleArn())
		}
		d := h.describeExecution(arn)
		if d.Status != sfnapi.ExecutionStatusSucceeded || !sameJSON(h, val(d.Output), `{"result":"ok"}`) {
			h.Fatalf("DescribeExecution = %+v, want the function's output", d)
		}
		want := "ExecutionStarted 1<-0\nTaskStateEntered 2<-0\nLambdaFunctionScheduled 3<-2\nLambdaFunctionStarted 4<-3\nLambdaFunctionSucceeded 5<-4\nTaskStateExited 6<-5\nExecutionSucceeded 7<-6"
		if got := shape(h.history(arn)); got != want {
			h.Errorf("history:\n%s\nwant (stepfunctions-asl.md 11.3 legacy Lambda row):\n%s", got, want)
		}
	})
}

// The recorded header names of a lambda:invoke result's SdkHttpMetadata
// (aws-recorded localstack@8b9a79f
// v2/services/test_lambda_task_service.snapshot.json:L67-L114).
var recordedHeaders = []string{
	"Connection", "Content-Length", "Content-Type", "Date", "X-Amz-Executed-Version",
	"X-Amzn-Trace-Id", "x-amzn-Remapped-Content-Length", "x-amzn-RequestId",
}

// lambda:invoke returns {ExecutedVersion, Payload, SdkHttpMetadata,
// SdkResponseMetadata, StatusCode} (stepfunctions-api.md 9.2; aws-doc
// dg/connect-lambda) with exactly the recorded header names, the recorded
// constants, and the values InvokeOutput carries.
func TestLambdaInvokeResult(t *testing.T) {
	run(t, func(h *harness) {
		fn := h.functionArn("worker")
		payload := []byte(`{"r":1}`)
		h.answer(func(_ *kernel.Call, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: payload, ExecutedVersion: new("$LATEST")}, nil
		})
		arn := h.start(h.invokeTaskMachine("m", fn), "x", new(`{"in":1}`))
		if calls := h.invoked(); len(calls) != 1 || calls[0].in.FunctionName != fn || !sameJSON(h, string(calls[0].in.Payload), `{"in":1}`) {
			h.Fatalf("Invoke calls = %+v, want one with FunctionName and the Payload", calls)
		}
		d := h.describeExecution(arn)
		if d.Status != sfnapi.ExecutionStatusSucceeded {
			h.Fatalf("DescribeExecution = %+v", d)
		}
		out := decode(h, val(d.Output))
		if got := mapKeys(out); !slices.Equal(got, []string{"ExecutedVersion", "Payload", "SdkHttpMetadata", "SdkResponseMetadata", "StatusCode"}) {
			h.Fatalf("result keys = %v (%s)", got, val(d.Output))
		}
		if !equal(at(out, "ExecutedVersion"), document.String("$LATEST")) || !equal(at(out, "Payload"), decode(h, `{"r":1}`)) || !equal(at(out, "StatusCode"), document.Number("200")) {
			h.Errorf("result = %s", val(d.Output))
		}
		meta := at(out, "SdkHttpMetadata")
		if got := mapKeys(meta); !slices.Equal(got, []string{"AllHttpHeaders", "HttpHeaders", "HttpStatusCode"}) {
			h.Fatalf("SdkHttpMetadata keys = %v", got)
		}
		headers := at(meta, "HttpHeaders")
		if got := mapKeys(headers); !slices.Equal(got, recordedHeaders) {
			h.Errorf("HttpHeaders keys = %v, want %v", got, recordedHeaders)
		}
		if got := mapKeys(at(meta, "AllHttpHeaders")); !slices.Equal(got, recordedHeaders) {
			h.Errorf("AllHttpHeaders keys = %v, want %v", got, recordedHeaders)
		}
		for name, want := range map[string]string{
			"Content-Type":                   "application/json",
			"Connection":                     "keep-alive",
			"x-amzn-Remapped-Content-Length": "0",
			"X-Amz-Executed-Version":         "$LATEST",
			"Content-Length":                 strconv.Itoa(len(payload)),
		} {
			if got := at(headers, name); !equal(got, document.String(want)) {
				h.Errorf("HttpHeaders %s = %v, want %q", name, got, want)
			}
			if got := at(at(meta, "AllHttpHeaders"), name); !equal(got, document.List{document.String(want)}) {
				h.Errorf("AllHttpHeaders %s = %v, want [%q]", name, got, want)
			}
		}
		if !equal(at(meta, "HttpStatusCode"), document.Number("200")) {
			h.Errorf("HttpStatusCode = %v", at(meta, "HttpStatusCode"))
		}
		reqID := at(headers, "x-amzn-RequestId")
		if s, ok := reqID.(document.String); !ok || s == "" || !equal(at(at(out, "SdkResponseMetadata"), "RequestId"), reqID) {
			h.Errorf("x-amzn-RequestId %v and SdkResponseMetadata %v, want one request id in both", reqID, at(out, "SdkResponseMetadata"))
		}
	})
}

func mapKeys(v document.Value) []string {
	m, ok := v.(document.Map)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, e := range m {
		out = append(out, e.Key)
	}
	slices.Sort(out)
	return out
}

// A missing function fails the task with Lambda.ResourceNotFoundException
// and the AWSLambda cause (aws-recorded localstack@8b9a79f
// v2/error_handling/test_task_service_lambda.snapshot.json:L972-L973,
// stepfunctions-api.md 9.4 row 4); a throttled function is named by switch
// sfn.integration.lambda-error.
func TestLambdaServiceErrors(t *testing.T) {
	run(t, func(h *harness) {
		fn := h.functionArn("gone")
		h.answer(func(_ *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			if strings.HasSuffix(in.FunctionName, ":gone") {
				return nil, lambdaapi.ErrResourceNotFoundException("Function not found: " + in.FunctionName + ":$LATEST")
			}
			return nil, lambdaapi.ErrTooManyRequestsException("Rate exceeded")
		})
		arn := h.start(h.invokeTaskMachine("m", fn), "x", new(`{}`))
		d := h.describeExecution(arn)
		prefix := "Function not found: " + fn + ":$LATEST (Service: AWSLambda; Status Code: 404; Error Code: ResourceNotFoundException; Request ID: "
		if d.Status != sfnapi.ExecutionStatusFailed || val(d.Error) != "Lambda.ResourceNotFoundException" ||
			!strings.HasPrefix(val(d.Cause), prefix) || !strings.HasSuffix(val(d.Cause), "; Proxy: null)") {
			h.Errorf("missing function: %s %q, want Lambda.ResourceNotFoundException with %q...", val(d.Error), val(d.Cause), prefix)
		}
		events := h.history(arn)
		if failed := events[len(events)-2]; failed.Type != "TaskFailed" || failed.TaskFailedEventDetails == nil ||
			val(failed.TaskFailedEventDetails.Error) != "Lambda.ResourceNotFoundException" || failed.TaskFailedEventDetails.Resource != "invoke" || failed.TaskFailedEventDetails.ResourceType != "lambda" {
			h.Errorf("history:\n%s\nwant TaskFailed before ExecutionFailed", shape(events))
		}

		throttled := h.start(h.invokeTaskMachine("t", h.functionArn("busy")), "x", new(`{}`))
		if d := h.describeExecution(throttled); val(d.Error) != sfn.LambdaError().Value.Prefix+"TooManyRequestsException" {
			h.Errorf("throttled: error %q, want %q (switch sfn.integration.lambda-error)", val(d.Error), sfn.LambdaError().Value.Prefix+"TooManyRequestsException")
		}
	})
}

// A function error is named by the payload's errorType with the payload
// as cause, for the function ARN form and lambda:invoke alike
// (stepfunctions-asl.md 8.1; aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L13709, L10459;
// stepfunctions-api.md 9.4 row 5).
func TestLambdaFunctionError(t *testing.T) {
	run(t, func(h *harness) {
		errPayload := `{"errorMessage":"boom","errorType":"Exception","requestId":"r-1","stackTrace":["line"]}`
		h.answer(func(_ *kernel.Call, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			return &lambdaapi.InvokeOutput{StatusCode: 200, FunctionError: new("Unhandled"), Payload: []byte(errPayload), ExecutedVersion: new("$LATEST")}, nil
		})
		for _, machine := range []string{h.legacyTaskMachine("legacy", h.functionArn("f")), h.invokeTaskMachine("invoke", h.functionArn("f"))} {
			d := h.describeExecution(h.start(machine, "x", new(`{}`)))
			if d.Status != sfnapi.ExecutionStatusFailed || val(d.Error) != "Exception" || !sameJSON(h, val(d.Cause), errPayload) {
				h.Errorf("%s: %s %q, want Exception with the error payload as cause", machine, val(d.Error), val(d.Cause))
			}
		}
	})
}
