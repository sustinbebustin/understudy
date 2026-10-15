package awsquery_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The generated SQS package and the codec meet here: legacy Query clients
// (sqs.md 2.3) decode through the generated Deserialize methods and the
// model's retained xmlName and xmlFlattened traits, and answer under the
// legacy namespace.

func sqsRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	return newRegistry(t, sqsapi.Service())
}

func claimSQS(t *testing.T, r *http.Request) *registry.Operation {
	t.Helper()
	claim := awsquery.Codec{}.Claim(r, sqsRegistry(t))
	got, ok := claim.(awsquery.Claimed)
	if !ok {
		t.Fatalf("Claim = %#v, want Claimed", claim)
	}
	return got.Op
}

// The Query example of the SQS API reference's SendMessage page
// (API_SendMessage.html, Example 2, fetched 2026-10-10): its queue URL and
// its body verbatim. The example has no Version parameter.
const (
	sendMessageDocQueue = "http://sqs.us-east-1.amazonaws.com/177715257436/MyQueue/"
	sendMessageDocForm  = "Action=SendMessage&MessageBody=This+is+a+test+message" +
		"&MessageAttribute.1.Name=my_attribute_name_1&MessageAttribute.1.Value.StringValue=my_attribute_value_1&MessageAttribute.1.Value.DataType=String" +
		"&MessageAttribute.2.Name=my_attribute_name_2&MessageAttribute.2.Value.StringValue=my_attribute_value_2&MessageAttribute.2.Value.DataType=String"
)

// sendMessageForm is the documented body plus Version=2012-11-05, which
// the Smithy awsQuery protocol and sqs.md 2.3 require and the example
// leaves out.
const sendMessageForm = sendMessageDocForm + "&Version=2012-11-05"

// The documented body as sent, without Version, names no service: the
// codec finds the service by Version alone and declines it. Confirming a
// Version-less request against the SigV4 signing name would be the edge's
// job, and it does not.
func TestSQSDocumentedFormWithoutVersionIsDeclined(t *testing.T) {
	t.Parallel()
	claim := awsquery.Codec{}.Claim(formPost(sendMessageDocQueue, sendMessageDocForm), sqsRegistry(t))
	d, ok := claim.(awsquery.Declined)
	if !ok || !strings.Contains(d.Reason, `Version "" names no service`) {
		t.Errorf("Claim = %#v, want Declined naming the empty Version", claim)
	}
}

// sqs.md 2.3: a legacy SendMessage posted to the queue URL decodes with
// the queue from the path and the flattened MessageAttribute.N map, and
// its answer is the documented SendMessageResponse under the legacy
// namespace.
func TestSQSLegacySendMessageExchange(t *testing.T) {
	t.Parallel()
	r := formPost(sendMessageDocQueue, sendMessageForm)
	op := claimSQS(t, r)
	if op.ID.Name != "SendMessage" {
		t.Fatalf("claimed %s", op.ID)
	}
	var in sqsapi.SendMessageInput
	if aerr := (awsquery.Codec{}).Decode(r, op, &in); aerr != nil {
		t.Fatalf("Decode = %s %q", aerr.ID.Shape, aerr.Message)
	}
	if in.QueueUrl != "http://sqs.us-east-1.amazonaws.com/177715257436/MyQueue" || in.MessageBody != "This is a test message" {
		t.Errorf("QueueUrl, MessageBody = %q, %q", in.QueueUrl, in.MessageBody)
	}
	a2, ok := in.MessageAttributes["my_attribute_name_2"]
	if len(in.MessageAttributes) != 2 || !ok || a2.DataType != "String" || a2.StringValue == nil || *a2.StringValue != "my_attribute_value_2" {
		t.Errorf("MessageAttributes = %+v", in.MessageAttributes)
	}
	if v := in.Validate(); len(v) != 0 {
		t.Errorf("Validate = %v", v)
	}

	// The values of the API reference's Query example response. Two
	// differences from the documented bytes: the generated Serialize writes
	// the members in model order, MD5s before MessageId, where the example
	// lists MessageId first, and the codec sends no <?xml version="1.0"?>
	// declaration, which the example starts with. XML parsers read neither.
	md5Body, md5Attrs, id, docRequestID := "fafb00f5732ab283681e124bf8747ed1", "c48838208d2b4e14e3ca0093a8443f09", "374cec7b-d0c8-4a2e-ad0b-67be763cf97e", "7fe4446e-b452-53f7-8f85-181e06f2dd99"
	resp, aerr := awsquery.Codec{}.Encode(op, docRequestID, &sqsapi.SendMessageOutput{MD5OfMessageBody: &md5Body, MD5OfMessageAttributes: &md5Attrs, MessageId: &id}, http.StatusOK)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	checkResponse(t, resp, golden{
		status: 200,
		header: http.Header{"X-Amzn-Requestid": {docRequestID}},
		body: `<SendMessageResponse xmlns="http://queue.amazonaws.com/doc/2012-11-05/"><SendMessageResult>` +
			`<MD5OfMessageBody>fafb00f5732ab283681e124bf8747ed1</MD5OfMessageBody>` +
			`<MD5OfMessageAttributes>c48838208d2b4e14e3ca0093a8443f09</MD5OfMessageAttributes>` +
			`<MessageId>374cec7b-d0c8-4a2e-ad0b-67be763cf97e</MessageId>` +
			`</SendMessageResult><ResponseMetadata><RequestId>7fe4446e-b452-53f7-8f85-181e06f2dd99</RequestId></ResponseMetadata></SendMessageResponse>`,
	})
}

// sqs.md 2.3: the same request as a GET over TLS, trailing slash included;
// the queue URL keeps the scheme the client used.
func TestSQSLegacyGetRequest(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "https://sqs.us-east-1.amazonaws.com/177715257436/MyQueue/?"+sendMessageForm, http.NoBody)
	op := claimSQS(t, r)
	var in sqsapi.SendMessageInput
	if aerr := (awsquery.Codec{}).Decode(r, op, &in); aerr != nil {
		t.Fatalf("Decode = %s %q", aerr.ID.Shape, aerr.Message)
	}
	if in.QueueUrl != "https://sqs.us-east-1.amazonaws.com/177715257436/MyQueue" || len(in.MessageAttributes) != 2 {
		t.Errorf("Decode = %+v", in)
	}
}

// sqs.md 2.3: a QueueUrl parameter overrides the queue named by the path.
func TestSQSQueueUrlParameterWins(t *testing.T) {
	t.Parallel()
	r := formPost("http://localhost/123456789012/PathQueue", "Action=DeleteQueue&Version=2012-11-05&QueueUrl=http%3A%2F%2Flocalhost%2F123456789012%2FParamQueue")
	op := claimSQS(t, r)
	var in sqsapi.DeleteQueueInput
	if aerr := (awsquery.Codec{}).Decode(r, op, &in); aerr != nil {
		t.Fatal(aerr.AsError())
	}
	if in.QueueUrl != "http://localhost/123456789012/ParamQueue" {
		t.Errorf("QueueUrl = %q", in.QueueUrl)
	}
}

// sqs.md 2.3: CreateQueue and ListQueues are account-scoped, so on a queue
// path they are the unknown action InvalidAction; GetQueueUrl is accepted
// there.
func TestSQSAccountActionsOnAQueuePath(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"CreateQueue", "ListQueues"} {
		claim := awsquery.Codec{}.Claim(formPost("http://localhost/123456789012/MyQueue", "Action="+action+"&Version=2012-11-05&QueueName=q"), sqsRegistry(t))
		if u, ok := claim.(awsquery.UnknownOperation); !ok || u.Operation != action || u.Service.ID != "sqs" {
			t.Errorf("Claim(%s) = %#v, want UnknownOperation", action, claim)
		}
	}
	claimSQS(t, formPost("http://localhost/123456789012/MyQueue", "Action=GetQueueUrl&Version=2012-11-05&QueueName=MyQueue"))
	claimSQS(t, formPost("http://localhost/", "Action=CreateQueue&Version=2012-11-05&QueueName=q"))
}

// sqs.md 2.3: ReceiveMessage with no message answers <ReceiveMessageResult/>
// (SQS omits the empty list), and messages are flattened <Message>
// elements whose attribute maps are flattened Attribute and
// MessageAttribute entries.
func TestSQSReceiveMessageResults(t *testing.T) {
	t.Parallel()
	op := claimSQS(t, formPost("http://localhost/123456789012/q", "Action=ReceiveMessage&Version=2012-11-05"))
	resp, aerr := awsquery.Codec{}.Encode(op, requestID, &sqsapi.ReceiveMessageOutput{}, http.StatusOK)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	checkResponse(t, resp, golden{
		status: 200,
		body: `<ReceiveMessageResponse xmlns="http://queue.amazonaws.com/doc/2012-11-05/"><ReceiveMessageResult/>` +
			`<ResponseMetadata><RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ResponseMetadata></ReceiveMessageResponse>`,
	})

	id, body, value := "m-1", "hello", "v"
	out := &sqsapi.ReceiveMessageOutput{Messages: []sqsapi.Message{{
		MessageId:         &id,
		Body:              &body,
		Attributes:        map[sqsapi.MessageSystemAttributeName]string{"SenderId": "AIDA"},
		MessageAttributes: map[string]sqsapi.MessageAttributeValue{"k": {DataType: "String", StringValue: &value}},
	}}}
	resp, aerr = awsquery.Codec{}.Encode(op, requestID, out, http.StatusOK)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	checkResponse(t, resp, golden{
		status: 200,
		body: `<ReceiveMessageResponse xmlns="http://queue.amazonaws.com/doc/2012-11-05/"><ReceiveMessageResult><Message>` +
			`<MessageId>m-1</MessageId><Body>hello</Body>` +
			`<Attribute><Name>SenderId</Name><Value>AIDA</Value></Attribute>` +
			`<MessageAttribute><Name>k</Name><Value><StringValue>v</StringValue><DataType>String</DataType></Value></MessageAttribute>` +
			`</Message></ReceiveMessageResult>` +
			`<ResponseMetadata><RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ResponseMetadata></ReceiveMessageResponse>`,
	})
}

// AttributeNames and MessageSystemAttributeNames share the key
// AttributeName.N in the model. sqs.md 2.3 treats the key as
// MessageSystemAttributeNames, and the overlay's queryKeyAlias says so:
// it receives the names, in numeric order, and the deprecated
// AttributeNames stays unset.
func TestSQSSharedAttributeNameKey(t *testing.T) {
	t.Parallel()
	r := formPost("http://localhost/123456789012/q", "Action=ReceiveMessage&Version=2012-11-05&AttributeName.10=SenderId&AttributeName.2=All&MaxNumberOfMessages=10")
	op := claimSQS(t, r)
	var in sqsapi.ReceiveMessageInput
	if aerr := (awsquery.Codec{}).Decode(r, op, &in); aerr != nil {
		t.Fatal(aerr.AsError())
	}
	want := []sqsapi.MessageSystemAttributeName{"All", "SenderId"}
	if !slices.Equal(in.MessageSystemAttributeNames, want) || in.AttributeNames != nil {
		t.Errorf("MessageSystemAttributeNames, AttributeNames = %v, %v", in.MessageSystemAttributeNames, in.AttributeNames)
	}
	if in.MaxNumberOfMessages == nil || *in.MaxNumberOfMessages != 10 {
		t.Errorf("MaxNumberOfMessages = %v", in.MaxNumberOfMessages)
	}
}

// sqs.md 2.3 and error-matrix.md 3: over Query, QueueDoesNotExist says
// "for this wsdl version" (messageByProtocol), and SQS declares its
// namespace and sends an empty Detail
// (testdata/parity/sqs/R11/05-sqs-query-nonexistent-queue.json).
func TestSQSQueueDoesNotExistOverQuery(t *testing.T) {
	t.Parallel()
	e := sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
	checkResponse(t, awsquery.Codec{}.EncodeError(sqsapi.Service(), requestID, e), golden{
		status: 400,
		body: `<ErrorResponse xmlns="http://queue.amazonaws.com/doc/2012-11-05/"><Error><Type>Sender</Type><Code>AWS.SimpleQueueService.NonExistentQueue</Code>` +
			`<Message>The specified queue does not exist for this wsdl version.</Message><Detail/></Error>` +
			`<RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ErrorResponse>`,
	})
}
