package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// receiveOne waits for one message in q and returns it with its
// attributes.
func receiveOne(t *testing.T, q queue, what string) sqstypes.Message {
	t.Helper()
	var got sqstypes.Message
	eventually(t, what, func() (bool, string) {
		out, err := env.sqs.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(q.url),
			MaxNumberOfMessages:         10,
			MessageAttributeNames:       []string{"All"},
			MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
		})
		if err != nil {
			t.Fatalf("sqs ReceiveMessage %s: %v", q.url, err)
		}
		if len(out.Messages) == 0 {
			return false, q.url + " is empty"
		}
		if len(out.Messages) > 1 {
			t.Fatalf("%s holds %d messages, want one", q.url, len(out.Messages))
		}
		got = out.Messages[0]
		return true, ""
	})
	return got
}

// An asynchronous invocation whose function fails, with
// MaximumRetryAttempts 0, sends one invocation record to the on-failure
// queue: RetriesExhausted, count 1, statusCode 200 and functionError
// Unhandled, the event as requestPayload and the error object as
// responsePayload, as the record's body with no message attributes
// (lambda-destinations-layers.md 3.4, 4.1: test_lambda_destinations.snapshot.json:L150-L175).
// The function's dead-letter queue gets the event as sent, byte for byte,
// with ErrorCode Number 200, ErrorMessage the error's errorMessage and
// RequestID the record's request id (finding 1, 5.2:
// test_lambda_destinations.snapshot.json:L243-L273): both receive a failed
// event (finding 7).
func TestLambdaAsyncFailureReachesOnFailureAndTheDeadLetterQueue(t *testing.T) {
	p := pipe(t)
	ctx := t.Context()
	onFailure, err := createQueue(ctx, env, "e2e-async-on-failure-"+env.runID, nil)
	if err != nil {
		t.Fatalf("create the on-failure queue: %v", err)
	}
	dlq, err := createQueue(ctx, env, "e2e-async-dlq-"+env.runID, nil)
	if err != nil {
		t.Fatalf("create the dead-letter queue: %v", err)
	}
	name := "e2e-async-" + env.runID
	created, err := env.lambda.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName:     aws.String(name),
		Role:             aws.String(p.roleARN),
		Runtime:          lambdatypes.RuntimePython313,
		Handler:          aws.String("handler.handler"),
		Code:             &lambdatypes.FunctionCode{ZipFile: env.artifact.pyZip},
		Timeout:          aws.Int32(functionTimeout),
		MemorySize:       aws.Int32(256),
		Architectures:    []lambdatypes.Architecture{architecture()},
		DeadLetterConfig: &lambdatypes.DeadLetterConfig{TargetArn: aws.String(dlq.arn)},
	})
	if err != nil {
		t.Fatalf("lambda CreateFunction: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		if _, err := env.lambda.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)}); err != nil {
			t.Errorf("lambda DeleteFunction: %v", err)
		}
		for _, q := range []queue{onFailure, dlq} {
			if _, err := env.sqs.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(q.url)}); err != nil {
				t.Errorf("sqs DeleteQueue %s: %v", q.url, err)
			}
		}
	})
	if err := lambda.NewFunctionActiveV2Waiter(env.lambda).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(name)}, 5*time.Minute); err != nil {
		t.Fatalf("%s did not become Active: %v", name, err)
	}
	if _, err := env.lambda.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{
		FunctionName:         aws.String(name),
		MaximumRetryAttempts: aws.Int32(0),
		DestinationConfig:    &lambdatypes.DestinationConfig{OnFailure: &lambdatypes.OnFailure{Destination: aws.String(onFailure.arn)}},
	}); err != nil {
		t.Fatalf("lambda PutFunctionEventInvokeConfig: %v", err)
	}

	// The Python worker reads event["Records"], so an event without them
	// raises: a function error.
	event := `{"id": "` + newID(t) + `", "note": "no Records here"}`
	inv, err := env.lambda.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(name), InvocationType: lambdatypes.InvocationTypeEvent, Payload: []byte(event)})
	if err != nil {
		t.Fatalf("lambda Invoke: %v", err)
	}
	if inv.StatusCode != http.StatusAccepted {
		t.Fatalf("Event Invoke status %d, want 202", inv.StatusCode)
	}

	msg := receiveOne(t, onFailure, "the on-failure queue holds the record")
	var rec struct {
		Version        string `json:"version"`
		Timestamp      string `json:"timestamp"`
		RequestContext struct {
			RequestID              string `json:"requestId"`
			FunctionArn            string `json:"functionArn"`
			Condition              string `json:"condition"`
			ApproximateInvokeCount int    `json:"approximateInvokeCount"`
		} `json:"requestContext"`
		RequestPayload  json.RawMessage `json:"requestPayload"`
		ResponseContext struct {
			StatusCode      int    `json:"statusCode"`
			ExecutedVersion string `json:"executedVersion"`
			FunctionError   string `json:"functionError"`
		} `json:"responseContext"`
		ResponsePayload struct {
			ErrorMessage string `json:"errorMessage"`
			ErrorType    string `json:"errorType"`
		} `json:"responsePayload"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(msg.Body)), &rec); err != nil {
		t.Fatalf("on-failure message body %q is not a record: %v", aws.ToString(msg.Body), err)
	}
	rc := rec.RequestContext
	if rec.Version != "1.0" || rc.Condition != "RetriesExhausted" || rc.ApproximateInvokeCount != 1 || rc.FunctionArn != aws.ToString(created.FunctionArn)+":$LATEST" || rc.RequestID == "" {
		t.Errorf("record version %q, requestContext %+v; want 1.0, RetriesExhausted, count 1, %s:$LATEST and a request id",
			rec.Version, rc, aws.ToString(created.FunctionArn))
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", rec.Timestamp); err != nil {
		t.Errorf("timestamp %q, want ISO 8601 UTC with milliseconds: %v", rec.Timestamp, err)
	}
	var sent, embedded any
	if json.Unmarshal([]byte(event), &sent) != nil || json.Unmarshal(rec.RequestPayload, &embedded) != nil || !reflect.DeepEqual(sent, embedded) {
		t.Errorf("requestPayload %s, want the event %s as a JSON value", rec.RequestPayload, event)
	}
	if r := rec.ResponseContext; r.StatusCode != http.StatusOK || r.ExecutedVersion != "$LATEST" || r.FunctionError != "Unhandled" {
		t.Errorf("responseContext %+v, want statusCode 200, $LATEST, functionError Unhandled", r)
	}
	if rec.ResponsePayload.ErrorMessage == "" || rec.ResponsePayload.ErrorType == "" {
		t.Errorf("responsePayload %+v, want the runtime's error object", rec.ResponsePayload)
	}
	if len(msg.MessageAttributes) != 0 {
		t.Errorf("record message attributes %v, want none", msg.MessageAttributes)
	}

	dead := receiveOne(t, dlq, "the dead-letter queue holds the event")
	if aws.ToString(dead.Body) != event {
		t.Errorf("dead letter body %q, want the event as sent, %q", aws.ToString(dead.Body), event)
	}
	attrs := map[string]string{}
	for k, v := range dead.MessageAttributes {
		attrs[k] = aws.ToString(v.DataType) + " " + aws.ToString(v.StringValue)
	}
	want := map[string]string{
		"ErrorCode":    "Number 200",
		"ErrorMessage": "String " + rec.ResponsePayload.ErrorMessage,
		"RequestID":    "String " + rc.RequestID,
	}
	if len(attrs) != len(want) {
		t.Errorf("dead letter attributes %v, want %v", attrs, want)
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("dead letter attribute %s = %q, want %q", k, attrs[k], v)
		}
	}
}
