package gosdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Research: docs/research/stepfunctions-api.md, stepfunctions-asl.md.

const statesTrustPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"states.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

// routeMachine chooses on $.wait between a one-hour Wait and the end, so a
// test finishes it with one admin clock advance.
const routeMachine = `{"StartAt":"Route","States":{
	"Route":{"Type":"Choice","Choices":[{"Variable":"$.wait","BooleanEquals":true,"Next":"Hold"}],"Default":"Done"},
	"Hold":{"Type":"Wait","Seconds":3600,"Next":"Done"},
	"Done":{"Type":"Pass","Result":{"finished":true},"ResultPath":"$.result","End":true}}}`

// admin calls understudy's admin API (path under /_understudy/v1) and fails
// the test on a non-2xx answer.
func admin(t *testing.T, method, path, body string) []byte {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, endpoint.String()+"/_understudy/v1"+path, rd)
	ok(t, err, "admin request")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	ok(t, err, method+" "+path)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	ok(t, err, "reading "+path)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s = %d: %s", method, path, resp.StatusCode, out)
	}
	return out
}

// advance moves emulator time forward and waits for what came due; the
// clock is put back when the test ends.
func advance(t *testing.T, seconds int) {
	t.Helper()
	t.Cleanup(func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint.String()+"/_understudy/v1/reset", bytes.NewReader([]byte(`{"services":["sts"],"clock":true}`)))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	})
	admin(t, http.MethodPost, "/clock", fmt.Sprintf(`{"op":"advance","by":"%ds"}`, seconds))
	settleEmulator(t)
}

func settleEmulator(t *testing.T) {
	t.Helper()
	var out struct {
		Settled bool `json:"settled"`
	}
	ok(t, json.Unmarshal(admin(t, http.MethodGet, "/settle?timeout=30s", ""), &out), "decoding /settle")
	if !out.Settled {
		t.Fatal("the emulator did not settle within 30s")
	}
}

// statesRole creates a role Step Functions may assume.
func statesRole(t *testing.T) string {
	t.Helper()
	return *createRole(t, name(t, "sfn-role"), statesTrustPolicy).Arn
}

func createMachine(t *testing.T, client *sfn.Client, machineName, definition, role string) string {
	t.Helper()
	out, err := client.CreateStateMachine(t.Context(), &sfn.CreateStateMachineInput{Name: aws.String(machineName), Definition: aws.String(definition), RoleArn: aws.String(role)})
	ok(t, err, "CreateStateMachine "+machineName)
	t.Cleanup(func() {
		_, _ = client.DeleteStateMachine(context.Background(), &sfn.DeleteStateMachineInput{StateMachineArn: out.StateMachineArn})
	})
	return *out.StateMachineArn
}

func describeExecution(t *testing.T, client *sfn.Client, arn string) *sfn.DescribeExecutionOutput {
	t.Helper()
	out, err := client.DescribeExecution(t.Context(), &sfn.DescribeExecutionInput{ExecutionArn: aws.String(arn)})
	ok(t, err, "DescribeExecution")
	return out
}

func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if json.Unmarshal([]byte(got), &g) != nil || json.Unmarshal([]byte(want), &w) != nil {
		return false
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	return bytes.Equal(gb, wb)
}

// A Choice/Wait/Pass machine end to end: create, describe with the
// recorded defaults (stepfunctions-api.md 5.2), validate OK and FAIL with
// the recorded diagnostic (5.6), start, finish the Wait with an admin clock
// advance, describe, history (stepfunctions-asl.md 11.2 rules 1 and 2),
// list, stop a second execution (stepfunctions-api.md 6.4), update with a
// new revisionId (5.3), delete.
func TestSFNStateMachineLifecycle(t *testing.T) {
	ctx := t.Context()
	client := sfn.NewFromConfig(awsCfg)
	role := statesRole(t)

	valid, err := client.ValidateStateMachineDefinition(ctx, &sfn.ValidateStateMachineDefinitionInput{Definition: aws.String(routeMachine)})
	ok(t, err, "ValidateStateMachineDefinition")
	if valid.Result != types.ValidateStateMachineDefinitionResultCodeOk || len(valid.Diagnostics) != 0 {
		t.Errorf("validate = %+v, want OK with no diagnostics", valid)
	}
	invalid, err := client.ValidateStateMachineDefinition(ctx, &sfn.ValidateStateMachineDefinitionInput{Definition: aws.String("{}")})
	ok(t, err, "ValidateStateMachineDefinition {}")
	if invalid.Result != types.ValidateStateMachineDefinitionResultCodeFail || len(invalid.Diagnostics) == 0 ||
		aws.ToString(invalid.Diagnostics[0].Code) != "SCHEMA_VALIDATION_FAILED" ||
		aws.ToString(invalid.Diagnostics[0].Message) != "These fields are required: [States, StartAt]" {
		t.Errorf("validate {} = %+v, want FAIL with the recorded diagnostic", invalid)
	}

	machineName := name(t, "sfn-route")
	machine := createMachine(t, client, machineName, routeMachine, role)
	d, err := client.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{StateMachineArn: aws.String(machine)})
	ok(t, err, "DescribeStateMachine")
	if aws.ToString(d.Definition) != routeMachine || d.Status != types.StateMachineStatusActive || d.Type != types.StateMachineTypeStandard ||
		d.LoggingConfiguration == nil || d.LoggingConfiguration.Level != types.LogLevelOff ||
		d.TracingConfiguration == nil || d.TracingConfiguration.Enabled ||
		d.EncryptionConfiguration == nil || d.EncryptionConfiguration.Type != types.EncryptionTypeAwsOwnedKey || d.RevisionId != nil {
		t.Errorf("DescribeStateMachine = %+v, want the recorded defaults", d)
	}

	started, err := client.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(machine), Name: aws.String("waits"), Input: aws.String(`{"wait":true}`)})
	ok(t, err, "StartExecution")
	settleEmulator(t)
	if got := describeExecution(t, client, *started.ExecutionArn); got.Status != types.ExecutionStatusRunning {
		t.Fatalf("before the advance: %s, want RUNNING", got.Status)
	}
	advance(t, 3600)
	done := describeExecution(t, client, *started.ExecutionArn)
	if done.Status != types.ExecutionStatusSucceeded || !sameJSON(t, aws.ToString(done.Output), `{"wait":true,"result":{"finished":true}}`) {
		t.Fatalf("after the advance: %s %s, want SUCCEEDED", done.Status, aws.ToString(done.Output))
	}
	history, err := client.GetExecutionHistory(ctx, &sfn.GetExecutionHistoryInput{ExecutionArn: started.ExecutionArn})
	ok(t, err, "GetExecutionHistory")
	ev := history.Events
	if len(ev) < 3 || ev[0].Type != types.HistoryEventTypeExecutionStarted || ev[0].Id != 1 || ev[0].PreviousEventId != 0 ||
		ev[1].Type != types.HistoryEventTypeChoiceStateEntered || ev[1].PreviousEventId != 0 ||
		ev[len(ev)-1].Type != types.HistoryEventTypeExecutionSucceeded {
		t.Errorf("history = %+v", ev)
	}

	held, err := client.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(machine), Name: aws.String("stopped"), Input: aws.String(`{"wait":true}`)})
	ok(t, err, "StartExecution stopped")
	settleEmulator(t)
	_, err = client.StopExecution(ctx, &sfn.StopExecutionInput{ExecutionArn: held.ExecutionArn})
	ok(t, err, "StopExecution")
	settleEmulator(t)
	if got := describeExecution(t, client, *held.ExecutionArn); got.Status != types.ExecutionStatusAborted {
		t.Errorf("stopped execution = %s, want ABORTED", got.Status)
	}

	listed, err := client.ListExecutions(ctx, &sfn.ListExecutionsInput{StateMachineArn: aws.String(machine)})
	ok(t, err, "ListExecutions")
	if len(listed.Executions) != 2 {
		t.Errorf("ListExecutions = %d executions, want 2", len(listed.Executions))
	}
	machines, err := client.ListStateMachines(ctx, &sfn.ListStateMachinesInput{})
	ok(t, err, "ListStateMachines")
	found := false
	for _, m := range machines.StateMachines {
		found = found || aws.ToString(m.StateMachineArn) == machine
	}
	if !found {
		t.Errorf("ListStateMachines does not list %s", machine)
	}

	updated, err := client.UpdateStateMachine(ctx, &sfn.UpdateStateMachineInput{StateMachineArn: aws.String(machine), Definition: aws.String(strings.Replace(routeMachine, `"Seconds":3600`, `"Seconds":60`, 1))})
	ok(t, err, "UpdateStateMachine")
	if aws.ToString(updated.RevisionId) == "" {
		t.Errorf("UpdateStateMachine = %+v, want a revisionId", updated)
	}

	_, err = client.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: aws.String(machine)})
	ok(t, err, "DeleteStateMachine")
	settleEmulator(t)
	_, err = client.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{StateMachineArn: aws.String(machine)})
	errAs[*types.StateMachineDoesNotExist](t, err, "DescribeStateMachine after delete")
	wantMessage(t, err, "DescribeStateMachine after delete", "State Machine Does Not Exist: '"+machine+"'")
}

// disableHostPrefix keeps StartSyncExecution on the configured endpoint:
// the SDK otherwise sends it to sync-<host> (stepfunctions-api.md 3.3).
func disableHostPrefix(stack *middleware.Stack) error {
	return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("DisableHostPrefix",
		func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
			return next.HandleInitialize(smithyhttp.DisableEndpointHostPrefix(ctx, true), in)
		}), middleware.Before)
}

// The refusals: a JSONata definition is InvalidDefinition with code
// UNSUPPORTED_BY_UNDERSTUDY (stepfunctions-asl.md 10.4), an EXPRESS machine
// and StartSyncExecution answer 501 (stepfunctions-api.md 13.1, Item 7
// minimum).
func TestSFNRefusals(t *testing.T) {
	ctx := t.Context()
	client := sfn.NewFromConfig(awsCfg)
	role := statesRole(t)

	_, err := client.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String(name(t, "sfn-jsonata")), RoleArn: aws.String(role),
		Definition: aws.String(`{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`),
	})
	errAs[*types.InvalidDefinition](t, err, "CreateStateMachine JSONata")
	wantMessageContains(t, err, "CreateStateMachine JSONata", "Invalid State Machine Definition: 'UNSUPPORTED_BY_UNDERSTUDY: ")

	_, err = client.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String(name(t, "sfn-express")), RoleArn: aws.String(role),
		Definition: aws.String(routeMachine), Type: types.StateMachineTypeExpress,
	})
	apiErr(t, err, "CreateStateMachine EXPRESS", "NotImplemented")
	wantStatus(t, err, "CreateStateMachine EXPRESS", 501)

	machine := createMachine(t, client, name(t, "sfn-sync"), routeMachine, role)
	_, err = client.StartSyncExecution(ctx, &sfn.StartSyncExecutionInput{StateMachineArn: aws.String(machine)}, func(o *sfn.Options) {
		o.APIOptions = append(o.APIOptions, disableHostPrefix)
	})
	apiErr(t, err, "StartSyncExecution", "NotImplemented")
	wantStatus(t, err, "StartSyncExecution", 501)
}

// A Lambda task through lambda:invoke with Retry and Catch: the function
// raises, the retry runs after its interval on the emulator clock, the
// catcher puts the error output at its ResultPath, named by the function's
// errorType with the error payload as cause (stepfunctions-asl.md 8.1-8.4).
// A function ARN Task returns the function's output (stepfunctions-api.md 9.2).
func TestSFNLambdaTaskRetryAndCatch(t *testing.T) {
	ctx := t.Context()
	client := sfn.NewFromConfig(awsCfg)
	failing := deployFunction(t, name(t, "sfn-fail"), "handler.fail", nil)
	echoing := deployFunction(t, name(t, "sfn-echo"), "handler.echo", nil)

	roleName := name(t, "sfn-task-role")
	role := *createRole(t, roleName, statesTrustPolicy).Arn
	iamClient := iam.NewFromConfig(awsCfg)
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeFunction","Resource":[%q,%q]}]}`, *failing.FunctionArn, *echoing.FunctionArn)
	_, err := iamClient.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("invoke"), PolicyDocument: aws.String(policy)})
	ok(t, err, "PutRolePolicy")
	t.Cleanup(func() {
		_, _ = iamClient.DeleteRolePolicy(context.Background(), &iam.DeleteRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("invoke")})
	})

	definition := fmt.Sprintf(`{"StartAt":"Call","States":{
		"Call":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":%q,"Payload.$":"$"},
			"Retry":[{"ErrorEquals":["States.ALL"],"IntervalSeconds":1,"MaxAttempts":1}],
			"Catch":[{"ErrorEquals":["States.ALL"],"ResultPath":"$.error","Next":"Echo"}],"Next":"Echo"},
		"Echo":{"Type":"Task","Resource":%q,"End":true}}}`, *failing.FunctionArn, *echoing.FunctionArn)
	machine := createMachine(t, client, name(t, "sfn-lambda"), definition, role)
	started, err := client.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(machine), Input: aws.String(`{"k":"v"}`)})
	ok(t, err, "StartExecution")
	settleEmulator(t)
	advance(t, 2)
	d := describeExecution(t, client, *started.ExecutionArn)
	if d.Status != types.ExecutionStatusSucceeded {
		t.Fatalf("execution = %s %s %s, want SUCCEEDED", d.Status, aws.ToString(d.Error), aws.ToString(d.Cause))
	}
	var out struct {
		K     string `json:"k"`
		Error struct {
			Error string `json:"Error"`
			Cause string `json:"Cause"`
		} `json:"error"`
	}
	ok(t, json.Unmarshal([]byte(aws.ToString(d.Output)), &out), "decoding the output")
	var cause struct {
		ErrorMessage string `json:"errorMessage"`
		ErrorType    string `json:"errorType"`
	}
	if out.K != "v" || out.Error.Error != "Exception" || json.Unmarshal([]byte(out.Error.Cause), &cause) != nil || cause.ErrorMessage != "Handler fails" || cause.ErrorType != "Exception" {
		t.Errorf("output = %s, want the input with the caught Exception and its payload as cause", aws.ToString(d.Output))
	}
	history, err := client.GetExecutionHistory(ctx, &sfn.GetExecutionHistoryInput{ExecutionArn: started.ExecutionArn, MaxResults: 1000})
	ok(t, err, "GetExecutionHistory")
	scheduled := 0
	for _, e := range history.Events {
		if e.Type == types.HistoryEventTypeTaskScheduled {
			scheduled++
		}
	}
	if scheduled != 2 {
		t.Errorf("TaskScheduled events = %d, want the try and one retry (stepfunctions-asl.md 11.2 rule 7)", scheduled)
	}
}
