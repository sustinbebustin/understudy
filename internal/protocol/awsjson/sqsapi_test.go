package awsjson_test

import (
	"net/http"
	"testing"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The generated SQS package and the codec meet here: the claim, the
// generated Deserialize and Serialize methods, and the generated error
// identities produce the documented wire exchange.

func sqsRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.New(sqsapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func claimSQS(t *testing.T, target string, body []byte) (*registry.Operation, awsjson.Codec) {
	t.Helper()
	codec := awsjson.Codec{Version: awsjson.V10}
	claim := codec.Claim(request(target, "application/x-amz-json-1.0", body), sqsRegistry(t))
	got, ok := claim.(awsjson.Claimed)
	if !ok {
		t.Fatalf("Claim(%s) = %#v, want Claimed", target, claim)
	}
	return got.Op, codec
}

// sqs.md 2.1: the documented CreateQueue exchange, request and response.
func TestSQSCreateQueueExchange(t *testing.T) {
	body := []byte(`{"QueueName":"MyQueue","Attributes":{"VisibilityTimeout":"40"},"tags":{"QueueType":"Production"}}`)
	op, codec := claimSQS(t, "AmazonSQS.CreateQueue", body)
	var in sqsapi.CreateQueueInput
	if aerr := codec.Decode(request("AmazonSQS.CreateQueue", "application/x-amz-json-1.0", body), op, &in); aerr != nil {
		t.Fatalf("Decode = %s %q", aerr.ID.Shape, aerr.Message)
	}
	if in.QueueName != "MyQueue" || in.Attributes["VisibilityTimeout"] != "40" || in.Tags["QueueType"] != "Production" {
		t.Errorf("Decode = %+v", in)
	}
	if v := in.Validate(); len(v) != 0 {
		t.Errorf("Validate = %v, want no violations", v)
	}

	url := "https://sqs.us-east-1.amazonaws.com/177715257436/MyQueue"
	resp, aerr := codec.Encode(op, requestID, &sqsapi.CreateQueueOutput{QueueUrl: &url}, 200)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	checkResponse(t, resp, golden{
		status: 200,
		header: headers("application/x-amz-json-1.0"),
		body:   `{"QueueUrl":"https://sqs.us-east-1.amazonaws.com/177715257436/MyQueue"}`,
	})
}

// sqs.md 2.1: an operation without output answers 200 with an empty body
// and the JSON Content-Type.
func TestSQSDeleteMessageEmptyResponse(t *testing.T) {
	op, codec := claimSQS(t, "AmazonSQS.DeleteMessage", nil)
	resp, aerr := codec.Encode(op, requestID, &sqsapi.DeleteMessageOutput{}, 200)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	checkResponse(t, resp, golden{status: 200, header: headers("application/x-amz-json-1.0"), body: ``})
}

// wire-json-query.md 6.2 and sqs.md 2.2: the generated QueueDoesNotExist
// renders the AWS documentation's body over JSON, with the JSON message
// rather than the Query-only "for this wsdl version" text, and the
// query-compatible header boto3 and Go read the code from.
func TestSQSQueueDoesNotExistFromGeneratedIdentity(t *testing.T) {
	codec := awsjson.Codec{Version: awsjson.V10}
	e := sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
	want := golden{
		status: 400,
		header: headers("application/x-amz-json-1.0", "X-Amzn-Query-Error", "AWS.SimpleQueueService.NonExistentQueue;Sender"),
		body:   `{"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"The specified queue does not exist."}`,
	}
	// The header is sent whether or not the client asked for query mode.
	for _, r := range []*http.Request{newRequest(), newRequest("X-Amzn-Query-Mode", "true")} {
		checkResponse(t, codec.EncodeError(r, sqsapi.Service(), requestID, e), want)
	}
}

// SQS answers a message body with invalid UTF-8 or a lone surrogate with
// InvalidMessageContents, a validation outcome: decoding keeps the bytes,
// and utf8.ValidString is the flag the validator reads.
func TestSQSSendMessageKeepsInvalidUTF8(t *testing.T) {
	body := []byte("{\"QueueUrl\":\"q\",\"MessageBody\":\"a\xffb\",\"MessageGroupId\":\"\\udc00\"}")
	op, codec := claimSQS(t, "AmazonSQS.SendMessage", body)
	var in sqsapi.SendMessageInput
	if aerr := codec.Decode(request("AmazonSQS.SendMessage", "application/x-amz-json-1.0", body), op, &in); aerr != nil {
		t.Fatalf("Decode = %s %q", aerr.ID.Shape, aerr.Message)
	}
	if in.MessageBody != "a\xffb" || utf8.ValidString(in.MessageBody) {
		t.Errorf("MessageBody = %q, want the bytes a\\xffb, flagged invalid", in.MessageBody)
	}
	if in.MessageGroupId == nil || *in.MessageGroupId != "\xed\xb0\x80" || utf8.ValidString(*in.MessageGroupId) {
		t.Errorf("MessageGroupId = %v, want the lone surrogate's bytes, flagged invalid", in.MessageGroupId)
	}
}
