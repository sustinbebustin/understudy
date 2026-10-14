package sqs_test

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
	"github.com/sustinbebustin/understudy/internal/store"
)

func op[In, Out any](tb testing.TB, name string) kernel.Op[In, Out] {
	tb.Helper()
	d, ok := sqsapi.Service().Operation(name)
	if !ok {
		tb.Fatalf("sqsapi has no %s", name)
	}
	return kernel.NewOp[In, Out](d)
}

// harness is a kernel with the SQS service and typed shortcuts for its
// operations.
type harness struct {
	*kerneltest.K
	svc *sqs.Service
}

func run(t *testing.T, fn func(h *harness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := sqs.New(k.Deps(sqsapi.Service()), sqs.Options{})
		fn(&harness{K: k, svc: svc})
	}, kerneltest.Services(sqsapi.Service()))
}

func (h *harness) create(in *sqsapi.CreateQueueInput) (*sqsapi.CreateQueueOutput, *awserr.Error) {
	return h.Invoke(h.Context(), op[sqsapi.CreateQueueInput, sqsapi.CreateQueueOutput](h, "CreateQueue"), in)
}

func (h *harness) getURL(in *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
	return h.Invoke(h.Context(), op[sqsapi.GetQueueUrlInput, sqsapi.GetQueueUrlOutput](h, "GetQueueUrl"), in)
}

func (h *harness) send(in *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
	return h.Invoke(h.Context(), op[sqsapi.SendMessageInput, sqsapi.SendMessageOutput](h, "SendMessage"), in)
}

func (h *harness) receive(in *sqsapi.ReceiveMessageInput) (*sqsapi.ReceiveMessageOutput, *awserr.Error) {
	return h.Invoke(h.Context(), op[sqsapi.ReceiveMessageInput, sqsapi.ReceiveMessageOutput](h, "ReceiveMessage"), in)
}

func (h *harness) remove(in *sqsapi.DeleteMessageInput) *awserr.Error {
	_, err := h.Invoke(h.Context(), op[sqsapi.DeleteMessageInput, sqsapi.DeleteMessageOutput](h, "DeleteMessage"), in)
	return err
}

// queue creates a queue and returns its URL.
func (h *harness) queue(name string, attrs map[sqsapi.QueueAttributeName]string) string {
	h.Helper()
	out, err := h.create(&sqsapi.CreateQueueInput{QueueName: name, Attributes: attrs})
	if err != nil {
		h.Fatalf("CreateQueue(%s) = %s: %s", name, err.ID.QueryCode, err.Message)
	}
	if out.QueueUrl == nil {
		h.Fatalf("CreateQueue(%s) returned no QueueUrl", name)
	}
	return *out.QueueUrl
}

// val reads an optional output member as "<nil>" when it is absent, so a
// missing member fails a comparison instead of panicking the test.
func val(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func (h *harness) mustSend(url, body string) *sqsapi.SendMessageOutput {
	h.Helper()
	out, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: body})
	if err != nil {
		h.Fatalf("SendMessage = %s: %s", err.ID.QueryCode, err.Message)
	}
	return out
}

func (h *harness) mustReceive(in *sqsapi.ReceiveMessageInput) []sqsapi.Message {
	h.Helper()
	out, err := h.receive(in)
	if err != nil {
		h.Fatalf("ReceiveMessage = %s: %s", err.ID.QueryCode, err.Message)
	}
	return out.Messages
}

func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

func wantError(t *testing.T, err *awserr.Error, code, message string) {
	t.Helper()
	switch {
	case err == nil:
		t.Fatalf("no error, want %s %q", code, message)
	case err.ID.QueryCode != code || err.Message != message:
		t.Fatalf("error = %s %q\nwant  %s %q", err.ID.QueryCode, err.Message, code, message)
	}
}

func TestCreateQueueURLEchoesTheHostAndIsIdempotent(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("orders", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "40"})
		if url != "http://localhost:4566/000000000000/orders" {
			h.Errorf("QueueUrl = %q", url)
		}
		// No attributes, or a subset equal to the current values, return the
		// same URL (sqs.md 5.1).
		if again := h.queue("orders", nil); again != url {
			h.Errorf("CreateQueue again = %q, want %q", again, url)
		}
		if again := h.queue("orders", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "40", "DelaySeconds": "0"}); again != url {
			h.Errorf("CreateQueue with equal attributes = %q, want %q", again, url)
		}
		_, err := h.create(&sqsapi.CreateQueueInput{QueueName: "orders", Attributes: map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "41", "DelaySeconds": "5"}})
		wantError(h.T, err, "QueueAlreadyExists", "A queue already exists with the same name and a different value for attribute DelaySeconds")
		if err.ID.JSONType != "com.amazonaws.sqs#QueueNameExists" {
			h.Errorf("__type = %s", err.ID.JSONType)
		}
	})
}

func TestCreateQueueRefusesBadNamesAndAttributes(t *testing.T) {
	run(t, func(h *harness) {
		nameText := func(name string) string {
			return "Value " + name + " for parameter QueueName is invalid. Reason: Can only include alphanumeric characters, hyphens, or underscores. 1 to 80 in length."
		}
		for _, tt := range []struct {
			in            sqsapi.CreateQueueInput
			code, message string
		}{
			{sqsapi.CreateQueueInput{QueueName: "bad name"}, "InvalidParameterValue", nameText("bad name")},
			{sqsapi.CreateQueueInput{QueueName: strings.Repeat("q", 81)}, "InvalidParameterValue", nameText(strings.Repeat("q", 81))},
			{sqsapi.CreateQueueInput{QueueName: "standard.fifo"}, "InvalidParameterValue", nameText("standard.fifo")},
			{sqsapi.CreateQueueInput{QueueName: "nofifo", Attributes: map[sqsapi.QueueAttributeName]string{"FifoQueue": "true"}}, "InvalidParameterValue", nameText("nofifo")},
			{sqsapi.CreateQueueInput{QueueName: "q.fifo", Attributes: map[sqsapi.QueueAttributeName]string{"FifoQueue": "true", "FifoThroughputLimit": "perMessageGroupId"}}, "InvalidAttributeValue", "Invalid value for the parameter FifoThroughputLimit."},
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"FifoQueue": "false"}}, "InvalidAttributeName", "Unknown Attribute FifoQueue."},
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"Foobar": "1"}}, "InvalidAttributeName", "Unknown Attribute Foobar."},
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"DeduplicationScope": "queue"}}, "InvalidAttributeName", "You can specify the DeduplicationScope only when FifoQueue is set to true."},
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"FifoThroughputLimit": "perQueue"}}, "InvalidAttributeName", "You can specify the FifoThroughputLimit only when FifoQueue is set to true."},
			// LocalStack's AWS recording of 2024-05-15
			// (invalid-attribute-content-based-deduplication).
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"ContentBasedDeduplication": "false"}}, "InvalidAttributeName", "Unknown Attribute ContentBasedDeduplication."},
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"DelaySeconds": "901"}}, "InvalidAttributeValue", "Invalid value for the parameter DelaySeconds."},
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"KmsMasterKeyId": "alias/k", "SqsManagedSseEnabled": "true"}}, "InvalidAttributeName", "You can use one type of server-side encryption (SSE) at one time. You can either enable KMS SSE or SQS SSE."},
			{redrive(`{"maxReceiveCount":3}`), "InvalidParameterValue", "The required parameter 'deadLetterTargetArn' is missing"},
			{redrive(`{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dlq"}`), "InvalidParameterValue", "The required parameter 'maxReceiveCount' is missing"},
			{redrive(`{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dlq","maxReceiveCount":"many"}`), "InvalidParameterValue", `Value {"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dlq","maxReceiveCount":"many"} for parameter RedrivePolicy is invalid. Reason: Invalid value for maxReceiveCount: many, valid values are from 1 to 1000 both inclusive.`},
			{redrive(`{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dlq","maxReceiveCount":1001}`), "InvalidParameterValue", `Value {"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dlq","maxReceiveCount":1001} for parameter RedrivePolicy is invalid. Reason: Invalid value for maxReceiveCount: 1001, valid values are from 1 to 1000 both inclusive.`},
			{redrive(`not json`), "InvalidAttributeValue", "Invalid value for the parameter RedrivePolicy."},
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"Policy": "{"}}, "InvalidAttributeValue", "Invalid value for the parameter Policy."},
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"Policy": "null"}}, "InvalidAttributeValue", "Invalid value for the parameter Policy."},
			// sqs.md 4: a Policy holds at most 8,192 bytes and 20 statements.
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"Policy": `{"Id":"` + strings.Repeat("x", 8192) + `"}`}}, "InvalidAttributeValue", "Invalid value for the parameter Policy. Reason: the policy is 8201 bytes; at most 8192 are allowed."},
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"Policy": policyOf(21)}}, "InvalidAttributeValue", "Invalid value for the parameter Policy. Reason: the policy has 21 statements; at most 20 are allowed."},
			{sqsapi.CreateQueueInput{QueueName: "q", Attributes: map[sqsapi.QueueAttributeName]string{"RedriveAllowPolicy": `{"redrivePermission":"sometimes"}`}}, "InvalidAttributeValue", "Invalid value for the parameter RedriveAllowPolicy."},
		} {
			_, err := h.create(&tt.in)
			wantError(h.T, err, tt.code, tt.message)
		}
		// A target that does not exist is refused; a numeric string
		// maxReceiveCount is accepted, and "" means no policy (sqs.md 4,
		// 5.1 and 9.1).
		in := redrive(`{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dlq","maxReceiveCount":3}`)
		_, err := h.create(&in)
		wantError(h.T, err, "InvalidParameterValue", `Value {"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dlq","maxReceiveCount":3} for parameter RedrivePolicy is invalid. Reason: Dead letter target does not exist.`)
		h.queue("dlq", nil)
		for i, policy := range []string{`{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dlq","maxReceiveCount":"42"}`, ""} {
			in := redrive(policy)
			in.QueueName += strconv.Itoa(i)
			if _, err := h.create(&in); err != nil {
				h.Errorf("RedrivePolicy %q = %s: %s", policy, err.ID.QueryCode, err.Message)
			}
		}
		if _, err := h.create(&sqsapi.CreateQueueInput{QueueName: "policed", Attributes: map[sqsapi.QueueAttributeName]string{"Policy": policyOf(20)}}); err != nil {
			h.Errorf("a Policy of 20 statements = %s: %s", err.ID.QueryCode, err.Message)
		}
	})
}

func redrive(policy string) sqsapi.CreateQueueInput {
	return sqsapi.CreateQueueInput{QueueName: "redriven", Attributes: map[sqsapi.QueueAttributeName]string{"RedrivePolicy": policy}}
}

// policyOf is a queue policy of n statements.
func policyOf(n int) string {
	statements := make([]string, n)
	for i := range statements {
		statements[i] = `{"Sid":"s` + strconv.Itoa(i) + `","Effect":"Allow","Principal":"*","Action":"SQS:SendMessage"}`
	}
	return `{"Version":"2012-10-17","Statement":[` + strings.Join(statements, ",") + `]}`
}

// throttleAll injects a throttle into every SQS call whose touched queue
// matches resource; the empty glob matches every call.
func (h *harness) throttleAll(resource string) {
	h.Helper()
	if _, err := h.Faults.Add(fault.Rule{
		Match:   fault.Match{Services: []ref.ServiceID{"sqs"}, Resource: fault.NewARNGlob(resource)},
		Phase:   fault.Request,
		Action:  fault.Throttle{},
		Trigger: fault.Always{},
	}); err != nil {
		h.Fatal(err)
	}
}

func injected(err *awserr.Error) bool {
	if err == nil {
		return false
	}
	_, ok := err.Origin.(awserr.Injected)
	return ok
}

// A Request rule scoped by resource throttles the queue it names before the
// handler runs, and leaves other queues alone.
func TestFaultRulesMatchTheQueueACallNames(t *testing.T) {
	run(t, func(h *harness) {
		orders := h.queue("orders-1", nil)
		billing := h.queue("billing", nil)
		h.throttleAll("arn:aws:sqs:*:*:orders-*")
		if _, err := h.send(&sqsapi.SendMessageInput{QueueUrl: orders, MessageBody: "m"}); !injected(err) {
			h.Errorf("SendMessage to orders-1 = %v, want the injected throttle", err)
		}
		if _, err := h.getURL(&sqsapi.GetQueueUrlInput{QueueName: "orders-1"}); !injected(err) {
			h.Errorf("GetQueueUrl of orders-1 = %v, want the injected throttle", err)
		}
		h.mustSend(billing, "m")
		if msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: billing}); len(msgs) != 1 {
			h.Errorf("billing received %d messages, want 1", len(msgs))
		}
	})
}

// SQS's limit texts precede an injected error, as generated constraints do.
func TestLimitChecksPrecedeInjectedFaults(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("limits", nil)
		h.throttleAll("")
		// testdata/parity/sqs/R11/02-receive-visibility-43201.json
		_, err := h.receive(&sqsapi.ReceiveMessageInput{QueueUrl: url, VisibilityTimeout: new(int32(43201))})
		wantError(h.T, err, "InvalidParameterValue", "Value 43201 for parameter VisibilityTimeout is invalid. Reason: Must be between 0 and 43200, if provided.")
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "a\x01b"})
		if err == nil || err.ID.QueryCode != "InvalidMessageContents" {
			h.Errorf("a control character under a throttle = %v, want InvalidMessageContents", err)
		}
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageAttributes: map[string]sqsapi.MessageAttributeValue{"k": {DataType: "String"}}})
		wantError(h.T, err, "InvalidParameterValue", "Message (user) attribute 'k' must contain a non-empty value of type 'String'.")
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageAttributes: map[string]sqsapi.MessageAttributeValue{"k": {DataType: "Date", StringValue: new("x")}}})
		wantError(h.T, err, "InvalidParameterValue", "The message attribute 'k' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String.")
		// testdata/parity/sqs/R11/x-send-attribute-name-257.json
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageAttributes: map[string]sqsapi.MessageAttributeValue{strings.Repeat("a", 257): {DataType: "String", StringValue: new("v")}}})
		wantError(h.T, err, "InvalidParameterValue", "Message (user) attribute name must be shorter than 256 bytes.")
		// testdata/parity/sqs/R11/x-send-datatype-257.json
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageAttributes: map[string]sqsapi.MessageAttributeValue{"k": {DataType: "String." + strings.Repeat("a", 250), StringValue: new("v")}}})
		wantError(h.T, err, "InvalidParameterValue", "Message (user) attribute type must be shorter than 256 bytes.")
		if _, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b"}); !injected(err) {
			h.Errorf("a valid send under a throttle = %v, want the injected throttle", err)
		}
	})
}

func TestGetQueueURLFindsQueuesAcrossAccounts(t *testing.T) {
	run(t, func(h *harness) {
		h.Scope.Account = mustAccount(h.T, "111111111111")
		url := h.queue("shared", nil)
		h.Scope.Account = aws.DefaultAccount()
		_, err := h.getURL(&sqsapi.GetQueueUrlInput{QueueName: "shared"})
		wantError(h.T, err, "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist.")
		out, err := h.getURL(&sqsapi.GetQueueUrlInput{QueueName: "shared", QueueOwnerAWSAccountId: new("111111111111")})
		if err != nil || val(out.QueueUrl) != url {
			h.Fatalf("cross-account GetQueueUrl = %v, %v; want %s", out, err, url)
		}
		// The URL's path names the account, so other accounts reach it.
		h.mustSend(url, "hello")
	})
}

// A call to a missing queue answers NonExistentQueue and leaves no state
// behind for the account its URL names.
func TestCallsToAMissingQueueLeaveNoState(t *testing.T) {
	run(t, func(h *harness) {
		url := "http://localhost:4566/111111111111/missing"
		const code, message = "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist."
		_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "m"})
		wantError(h.T, err, code, message)
		_, err = h.receive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		wantError(h.T, err, code, message)
		err = h.remove(&sqsapi.DeleteMessageInput{QueueUrl: url, ReceiptHandle: "handle"})
		wantError(h.T, err, code, message)
		if scopes := h.svc.Scopes(); len(scopes) != 0 {
			h.Errorf("scopes after calls to a missing queue = %v, want none", scopes)
		}
	})
}

// A QueueUrl that is neither a queue URL nor a queue name answers
// InvalidAddress (404) on every queue operation, not NonExistentQueue.
func TestMalformedQueueURLsAnswerInvalidAddress(t *testing.T) {
	run(t, func(h *harness) {
		for _, url := range []string{"http://127.0.0.1:4566/", "http://localhost:4566/queue/q1", "http://localhost/000000000000/a/b", "not a/name"} {
			want := "The address " + url + " is not valid for this endpoint."
			_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "m"})
			wantError(h.T, err, "InvalidAddress", want)
			if err.ID.Status != http.StatusNotFound {
				h.Errorf("SendMessage(%q) status = %d, want 404", url, err.ID.Status)
			}
			_, err = h.Invoke(h.Context(), op[sqsapi.GetQueueAttributesInput, sqsapi.GetQueueAttributesOutput](h, "GetQueueAttributes"), &sqsapi.GetQueueAttributesInput{QueueUrl: url})
			wantError(h.T, err, "InvalidAddress", want)
		}
	})
}

func mustAccount(t *testing.T, s string) aws.AccountID {
	t.Helper()
	a, err := aws.ParseAccountID(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// sqs.md 6.3: digests AWS recorded.
func TestSendMessageDigestsMatchAWS(t *testing.T) {
	str := func(v string) sqsapi.MessageAttributeValue {
		return sqsapi.MessageAttributeValue{DataType: "String", StringValue: &v}
	}
	cases := []struct {
		attrs map[string]sqsapi.MessageAttributeValue
		want  string
	}{
		{map[string]sqsapi.MessageAttributeValue{"k": str("x")}, "a45daa70926828a2f0a1db3418e6feb4"},
		{map[string]sqsapi.MessageAttributeValue{"MyAttribute": str("foobar")}, "adb59cd4678ea5a855436b949cd07ab6"},
		{map[string]sqsapi.MessageAttributeValue{"General": str("Kenobi"), "Hello": str("There"), "Help.Me": str("Me")}, "4c360f3fdafd970e05fae2f149d997f5"},
		{map[string]sqsapi.MessageAttributeValue{"Foo": str("Bar")}, "ae7155c6026991b6d54b11589678bf9c"},
		{map[string]sqsapi.MessageAttributeValue{"timestamp": {DataType: "Number", StringValue: new("1493147359900")}}, "235c5c510d26fb653d073faed50ae77c"},
		{map[string]sqsapi.MessageAttributeValue{"attr1": {DataType: "Binary", BinaryValue: []byte("traceparent\x1e00-774062d6c37081a5a0b9b5b88e30627c-2d2482211f6489da-01")}}, "8cbe4db156db8a94db8b801b7addb984"},
	}
	run(t, func(h *harness) {
		url := h.queue("digests", nil)
		out := h.mustSend(url, "message")
		if val(out.MD5OfMessageBody) != "78e731027d8fd50ed642340b7c9a63b3" || out.MD5OfMessageAttributes != nil || out.MD5OfMessageSystemAttributes != nil {
			h.Errorf("SendMessage(message) = %+v", out)
		}
		for _, tt := range cases {
			out, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageAttributes: tt.attrs})
			if err != nil || val(out.MD5OfMessageAttributes) != tt.want {
				h.Errorf("SendMessage(%v) = %v, %v; want attribute MD5 %s", tt.attrs, out, err, tt.want)
			}
		}
		trace := "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1"
		out, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageSystemAttributes: map[sqsapi.MessageSystemAttributeNameForSends]sqsapi.MessageSystemAttributeValue{
			"AWSTraceHeader": {DataType: "String", StringValue: &trace},
		}})
		if err != nil || val(out.MD5OfMessageSystemAttributes) != "5ae4d5d7636402d80f4eb6d213245a88" {
			h.Errorf("system attribute digest = %v, %v", out, err)
		}
		// AWSTraceHeader taken from the X-Amzn-Trace-Id header is kept on the
		// message but sent no system attribute, so it has no digest.
		c := h.Kernel.NewCall(kernel.ExternalCall{
			RequestID: h.Kernel.IDs().RequestID(),
			Operation: sqsapi.OpSendMessage.Descriptor().ID,
			Scope:     h.Scope,
			Principal: identity.Root{Account: h.Scope.Account},
			Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
			Admitted:  h.Clock.Now(),
			Endpoint:  h.Endpoint,
			Header:    http.Header{"X-Amzn-Trace-Id": {trace}},
		})
		out, err = kerneltest.Dispatch[sqsapi.SendMessageInput, sqsapi.SendMessageOutput](h.Context(), h.K, c, &sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b"})
		if err != nil || out.MD5OfMessageSystemAttributes != nil {
			h.Errorf("header-only trace: SendMessage = %+v, %v; want no MD5OfMessageSystemAttributes", out, err)
		}
		all := []sqsapi.MessageSystemAttributeName{"All"}
		msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, MaxNumberOfMessages: new(int32(10)), MessageSystemAttributeNames: all})
		if len(msgs) == 0 {
			h.Fatal("ReceiveMessage returned no messages")
		}
		if last := msgs[len(msgs)-1]; last.Attributes["AWSTraceHeader"] != trace {
			h.Errorf("header-only trace: AWSTraceHeader = %q, want %q", last.Attributes["AWSTraceHeader"], trace)
		}
	})
}

// sqs.md 6.1: MessageGroupId on a standard queue is accepted and follows the
// FIFO character rule (test_send_message_to_standard_queue_with_invalid_message_group_id).
func TestSendMessageGroupIDOnAStandardQueue(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("fair", nil)
		for _, bad := range []string{"", "group 123", strings.Repeat("g", 129), "é"} {
			_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageGroupId: &bad})
			wantError(h.T, err, "InvalidParameterValue", "Value "+bad+" for parameter MessageGroupId is invalid. Reason: MessageGroupId can only include alphanumeric and punctuation characters. 1 to 128 in length.")
		}
		good := strings.Repeat("g", 127) + "~"
		if _, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageGroupId: &good}); err != nil {
			h.Fatalf("a 128-character group id: %s", err.Message)
		}
		msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, MessageSystemAttributeNames: []sqsapi.MessageSystemAttributeName{"MessageGroupId"}})
		if len(msgs) != 1 || msgs[0].Attributes["MessageGroupId"] != good {
			h.Errorf("received = %+v, want the group id as a system attribute", msgs)
		}
	})
}

// sqs.md 6.1: the size limit counts the body and every attribute's name,
// type and value; exactly at the limit passes.
func TestSendMessageSizeLimit(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("small", map[sqsapi.QueueAttributeName]string{"MaximumMessageSize": "1024"})
		x := "x"
		attrs := map[string]sqsapi.MessageAttributeValue{"k": {DataType: "String", StringValue: &x}}
		atLimit := strings.Repeat("b", 1024-len("k")-len("String")-len("x"))
		if _, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: atLimit, MessageAttributes: attrs}); err != nil {
			h.Fatalf("a message at the limit: %s", err.Message)
		}
		_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: atLimit + "b", MessageAttributes: attrs})
		wantError(h.T, err, "InvalidParameterValue", "One or more parameters are invalid. Reason: Message must be shorter than 1024 bytes.")
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "a\x01b"})
		if err == nil || err.ID.QueryCode != "InvalidMessageContents" {
			h.Errorf("a control character = %v, want InvalidMessageContents", err)
		}
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: "http://localhost:4566/000000000000/missing", MessageBody: "b"})
		wantError(h.T, err, "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist.")
	})
}

// A queue's own MaximumMessageSize does not apply to messages from Amazon
// SNS (https://docs.aws.amazon.com/sns/latest/dg/large-message-payloads.html):
// a delivery larger than the queue's limit is enqueued, while a client's
// send of the same body is refused.
func TestSNSDeliveriesSkipTheQueuesSizeLimit(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("small", map[sqsapi.QueueAttributeName]string{"MaximumMessageSize": "2048"})
		body := strings.Repeat("b", 4096)
		_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: body})
		wantError(h.T, err, "InvalidParameterValue", "One or more parameters are invalid. Reason: Message must be shorter than 2048 bytes.")
		send := op[sqsapi.SendMessageInput, sqsapi.SendMessageOutput](h, "SendMessage")
		parent := h.Call(send.Descriptor())
		delivery := kernel.Target{Scope: h.Scope, As: kernel.AsService{Name: "sns.amazonaws.com"}, Via: ref.SNSDelivery}
		if _, err := kernel.Invoke(h.Context(), h.Kernel, parent, send, delivery, &sqsapi.SendMessageInput{QueueUrl: url, MessageBody: body}); err != nil {
			h.Fatalf("an SNS delivery of %d bytes to a queue limited to 2048 = %s: %s", len(body), err.ID.QueryCode, err.Message)
		}
		// SQS's absolute ceiling, 1 MiB, still bounds a delivery.
		over := strings.Repeat("b", 1<<20+1)
		_, err = kernel.Invoke(h.Context(), h.Kernel, parent, send, delivery, &sqsapi.SendMessageInput{QueueUrl: url, MessageBody: over})
		wantError(h.T, err, "InvalidParameterValue", "One or more parameters are invalid. Reason: Message must be shorter than 1048576 bytes.")
		if msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url}); len(msgs) != 1 || val(msgs[0].Body) != body {
			h.Errorf("ReceiveMessage = %d messages, want the SNS delivery", len(msgs))
		}
	})
}

func TestLongPollOnAFrozenClockEndsAtTheWallCap(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("idle", nil)
		start := time.Now()
		emuStart := h.Clock.Now()
		msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, WaitTimeSeconds: new(int32(20))})
		if len(msgs) != 0 {
			h.Fatalf("messages = %v, want none", msgs)
		}
		if elapsed := time.Since(start); elapsed != 20*time.Second {
			h.Errorf("the long poll took %s of wall time, want 20s", elapsed)
		}
		if h.Clock.Now() != emuStart {
			h.Errorf("the frozen clock moved to %s", h.Clock.Now())
		}
	})
}

func TestSendMessageWakesAParkedReceive(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("wake", nil)
		got := make(chan []sqsapi.Message, 1)
		start := time.Now()
		go func() {
			got <- h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, WaitTimeSeconds: new(int32(20))})
		}()
		synctest.Wait()
		h.mustSend(url, "ping")
		msgs := <-got
		if len(msgs) != 1 || val(msgs[0].Body) != "ping" {
			h.Fatalf("messages = %+v, want ping", msgs)
		}
		if time.Since(start) != 0 {
			h.Errorf("the receive waited %s; the send should wake it at once", time.Since(start))
		}
	})
}

func TestVisibilityExpiryRedeliversUnderAdvance(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("visible", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "30"})
		h.mustSend(url, "work")
		count := []sqsapi.MessageSystemAttributeName{"ApproximateReceiveCount"}
		first := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, MessageSystemAttributeNames: count})
		if len(first) != 1 || first[0].Attributes["ApproximateReceiveCount"] != "1" {
			h.Fatalf("first receive = %+v", first)
		}
		if again := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url}); len(again) != 0 {
			h.Fatalf("an in-flight message was received again: %+v", again)
		}
		// A long poll parked on the in-flight message wakes when its
		// visibility timeout ends on the emulator clock.
		got := make(chan []sqsapi.Message, 1)
		go func() {
			got <- h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, WaitTimeSeconds: new(int32(20)), MessageSystemAttributeNames: count})
		}()
		synctest.Wait()
		h.advance(30 * time.Second)
		second := <-got
		if len(second) != 1 || *second[0].MessageId != *first[0].MessageId || second[0].Attributes["ApproximateReceiveCount"] != "2" {
			h.Fatalf("redelivery = %+v", second)
		}
		if *second[0].ReceiptHandle == *first[0].ReceiptHandle {
			h.Error("a redelivery reused the receipt handle")
		}
	})
}

func TestDelayedMessagesBecomeVisibleOnTheClock(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("delayed", map[sqsapi.QueueAttributeName]string{"DelaySeconds": "10"})
		h.mustSend(url, "later")
		if msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url}); len(msgs) != 0 {
			h.Fatalf("a delayed message was received at once: %+v", msgs)
		}
		h.advance(10 * time.Second)
		if msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url}); len(msgs) != 1 {
			h.Fatalf("after the delay: %+v", msgs)
		}
		// An explicit per-message delay overrides the queue's.
		if _, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "now", DelaySeconds: new(int32(0))}); err != nil {
			h.Fatal(err.Message)
		}
		if msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url}); len(msgs) != 1 || val(msgs[0].Body) != "now" {
			h.Fatalf("DelaySeconds=0: %+v", msgs)
		}
	})
}

// A message waits out its delay on an sqs.delay timer, which is what
// `clock advance` names when the delay ends, and a received message waits
// out its visibility timeout on an sqs.visibility timer.
func TestDelayedMessageWaitsOnItsOwnTimer(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("delayed", nil)
		if _, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "later", DelaySeconds: new(int32(10))}); err != nil {
			h.Fatal(err.Message)
		}
		if d, v := armedTimers(h, sqs.TimerDelay), visibilityTimers(h); d != 1 || v != 0 {
			h.Fatalf("timers of a delayed message: %d delay, %d visibility; want 1 delay", d, v)
		}
		adv, err := h.Clock.Advance(10 * time.Second)
		if err != nil {
			h.Fatal(err)
		}
		if len(adv.Fired) != 1 || adv.Fired[0].Kind != sqs.TimerDelay {
			h.Fatalf("advance past the delay fired %+v, want one %s timer", adv.Fired, sqs.TimerDelay)
		}
		if msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, VisibilityTimeout: new(int32(30))}); len(msgs) != 1 {
			h.Fatalf("after the delay: %+v", msgs)
		}
		if d, v := armedTimers(h, sqs.TimerDelay), visibilityTimers(h); d != 0 || v != 1 {
			h.Fatalf("timers of a received message: %d delay, %d visibility; want 1 visibility", d, v)
		}
	})
}

func visibilityTimers(h *harness) int {
	return armedTimers(h, sqs.TimerVisibility)
}

// armedTimers counts the armed timers of kind.
func armedTimers(h *harness, kind clock.TimerKind) int {
	n := 0
	for _, a := range h.Kernel.Timers().Scheduler().Snapshot() {
		if a.Key.Kind == kind {
			n++
		}
	}
	return n
}

// DeleteMessage disarms the visibility timer in the same commit, so no
// ghost timer outlives the message (ARCHITECTURE.md 3.5, effects order).
func TestDeleteMessageLeavesNoGhostTimer(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("tidy", nil)
		h.mustSend(url, "done")
		msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		if visibilityTimers(h) != 1 {
			h.Fatalf("visibility timers after a receive = %d, want 1", visibilityTimers(h))
		}
		if len(msgs) != 1 {
			h.Fatalf("ReceiveMessage = %+v, want one message", msgs)
		}
		if err := h.remove(&sqsapi.DeleteMessageInput{QueueUrl: url, ReceiptHandle: val(msgs[0].ReceiptHandle)}); err != nil {
			h.Fatal(err.Message)
		}
		if n := visibilityTimers(h); n != 0 {
			h.Fatalf("visibility timers after the delete = %d, want 0", n)
		}
		h.advance(time.Hour)
		if msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url}); len(msgs) != 0 {
			h.Fatalf("a deleted message came back: %+v", msgs)
		}
	})
}

func TestDeleteMessageReceiptHandles(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("handles", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "5"})
		h.mustSend(url, "m")
		msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		if len(msgs) != 1 {
			h.Fatalf("ReceiveMessage = %+v, want one message", msgs)
		}
		handle := val(msgs[0].ReceiptHandle)
		err := h.remove(&sqsapi.DeleteMessageInput{QueueUrl: url, ReceiptHandle: "garbage"})
		wantError(h.T, err, "ReceiptHandleIsInvalid", `The input receipt handle "garbage" is not a valid receipt handle.`)
		if err.ID.Status != 404 {
			h.Errorf("status = %d, want the model's 404", err.ID.Status)
		}
		// The queue is resolved before the handle is read.
		err = h.remove(&sqsapi.DeleteMessageInput{QueueUrl: "http://localhost:4566/000000000000/missing", ReceiptHandle: "garbage"})
		wantError(h.T, err, "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist.")
		// A handle whose visibility expired still deletes the message when
		// nobody received it since (sqs.md 7.2).
		h.advance(10 * time.Second)
		if err := h.remove(&sqsapi.DeleteMessageInput{QueueUrl: url, ReceiptHandle: handle}); err != nil {
			h.Fatal(err.Message)
		}
		if err := h.remove(&sqsapi.DeleteMessageInput{QueueUrl: url, ReceiptHandle: handle}); err != nil {
			h.Fatalf("deleting a deleted message again = %s, want success", err.Message)
		}
		if msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url}); len(msgs) != 0 {
			h.Fatalf("messages after the delete: %+v", msgs)
		}
	})
}

func TestRetentionDropsOldMessages(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("short", map[sqsapi.QueueAttributeName]string{"MessageRetentionPeriod": "60"})
		h.mustSend(url, "old")
		h.advance(time.Minute)
		if msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url}); len(msgs) != 0 {
			h.Fatalf("a message past its retention was received: %+v", msgs)
		}
	})
}

func TestReceiveSelectsAttributesAndDigestsWhatItReturns(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("attrs", nil)
		v := func(s string) sqsapi.MessageAttributeValue {
			return sqsapi.MessageAttributeValue{DataType: "String", StringValue: &s}
		}
		_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageAttributes: map[string]sqsapi.MessageAttributeValue{
			"General": v("Kenobi"), "Hello": v("There"), "Help.Me": v("Me"),
		}})
		if err != nil {
			h.Fatal(err.Message)
		}
		msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{
			QueueUrl: url, MessageAttributeNames: []string{"Hel.*"},
			AttributeNames: []sqsapi.QueueAttributeName{"All"},
		})
		if len(msgs) == 0 {
			h.Fatal("ReceiveMessage returned no messages")
		}
		m := msgs[0]
		if len(m.MessageAttributes) != 2 || val(m.MD5OfMessageAttributes) != "83fee93c1bcd8b9a5a923ffacdc636c7" {
			h.Errorf("Hel.* selected %v with digest %v", m.MessageAttributes, m.MD5OfMessageAttributes)
		}
		if m.Attributes["SenderId"] != "000000000000" || m.Attributes["ApproximateReceiveCount"] != "1" || m.Attributes["SentTimestamp"] == "" {
			h.Errorf("system attributes = %v", m.Attributes)
		}
	})
}

// Every SQS operation, HoldVisibility included, is bound: in full, or
// stored only for the permission operations, which edit the Policy
// attribute nothing enforces.
func TestEveryOperationIsBound(t *testing.T) {
	run(t, func(h *harness) {
		for _, o := range sqsapi.Service().Operations {
			bound, ok := h.Kernel.Bindings().Status(o).(kernel.Bound)
			if !ok {
				h.Errorf("%s is not bound", o.ID.Name)
				continue
			}
			_, stored := bound.Coverage.(registry.StoredOnly)
			if permission := o.ID.Name == "AddPermission" || o.ID.Name == "RemovePermission"; stored != permission {
				h.Errorf("%s is bound %T", o.ID.Name, bound.Coverage)
			}
		}
	})
}

// sqs.md 6.3: the receive-side vectors, digests of the attributes a
// receive selected.
func TestReceiveDigestsMatchAWS(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("subsets", nil)
		v := func(s string) sqsapi.MessageAttributeValue {
			return sqsapi.MessageAttributeValue{DataType: "String", StringValue: &s}
		}
		_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageAttributes: map[string]sqsapi.MessageAttributeValue{
			"General": v("Kenobi"), "Hello": v("There"), "Help.Me": v("Me"),
		}})
		if err != nil {
			h.Fatal(err.Message)
		}
		for _, tt := range []struct {
			names []string
			want  string
		}{
			{[]string{"All"}, "4c360f3fdafd970e05fae2f149d997f5"},
			{[]string{"Hello", "General"}, "fca026605781cb4126a1e9044df24232"},
			{[]string{"Hello"}, "a311262e065454b75da111d535b8dacd"},
			{[]string{"Hello", "Help.Me"}, "83fee93c1bcd8b9a5a923ffacdc636c7"},
			{[]string{"Absent"}, "<nil>"},
		} {
			msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, VisibilityTimeout: new(int32(0)), MessageAttributeNames: tt.names})
			if len(msgs) != 1 || val(msgs[0].MD5OfMessageAttributes) != tt.want {
				h.Errorf("MessageAttributeNames %q: digest %s, want %s", tt.names, val(msgs[0].MD5OfMessageAttributes), tt.want)
			}
		}
	})
}

// The journal records each call with the message's enqueue event as the
// sender's call, so later deliveries can name their cause.
func TestCallsAreJournaledAgainstTheQueue(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("journaled", nil)
		h.mustSend(url, "m")
		page := h.Journal.Query(journal.Filter{})
		var ops []string
		for _, e := range page.Events {
			call, ok := e.Detail.(journal.APICall)
			if !ok {
				continue
			}
			ops = append(ops, call.Op.Name)
			if len(call.Resources) != 1 || call.Resources[0].String() != "arn:aws:sqs:us-east-1:000000000000:journaled" {
				h.Errorf("%s touched %v", call.Op.Name, call.Resources)
			}
		}
		if strings.Join(ops, ",") != "CreateQueue,SendMessage" {
			h.Errorf("journaled calls = %v", ops)
		}
	})
}

// Export, Import and Export again give the same bytes, and Rearm restores
// the timers the state implies.
func TestSnapshotRoundTripAndRearm(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("saved", map[sqsapi.QueueAttributeName]string{"Policy": `{"Version":"2012-10-17"}`, "MessageRetentionPeriod": "120"})
		h.mustSend(url, "in flight")
		h.mustSend(url, "waiting")
		h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		h.queue("dlq", nil)
		src := h.queue("src", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "0", "RedrivePolicy": redrivePolicy("dlq", 1)})
		h.deadLetterMessages(src, 3)
		if _, err := h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "dlq", MaxNumberOfMessagesPerSecond: new(int32(1))}); err != nil {
			h.Fatal(err.Message)
		}
		before := armed(h)
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		again, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		if len(again) != 1 || !bytes.Equal(again[0].Data, sections[0].Data) || again[0].Path != "000000000000/us-east-1.json" {
			h.Errorf("export after import differs:\n%s\n%s", sections[0].Data, again[0].Data)
		}
		if after := armed(h); !maps.Equal(after, before) {
			h.Errorf("timers after Rearm = %v\nwant %v", after, before)
		}
		// The re-armed timers fire: the in-flight message comes back after
		// its visibility timeout, the task moves on, and both messages go
		// at the end of their retention.
		h.advance(30 * time.Second)
		if got := h.receiveBodies(url, 10); len(got) != 2 {
			h.Errorf("after the visibility timeout = %q, want both messages", got)
		}
		if task := h.tasks("dlq", 1)[0]; task.ApproximateNumberOfMessagesMoved != 3 {
			h.Errorf("the re-armed task moved %d, want 3", task.ApproximateNumberOfMessagesMoved)
		}
		h.advance(90 * time.Second)
		res := h.svc.Resources(h.Scope)
		if i := slices.IndexFunc(res, func(r kernel.Resource) bool { return r.ARN.String() == arnPrefix+"saved" }); i < 0 || res[i].Summary != "0 messages" {
			h.Errorf("Resources after retention = %+v", res)
		}
	})
}

// A long poll parked on an empty queue returns at once when a snapshot
// load brings the queue a visible message, rather than waiting out its
// WaitTimeSeconds.
func TestRearmWakesALongPollForLoadedMessages(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("loaded", nil)
		h.mustSend(url, "fixture")
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.mustDelete(url, &h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})[0])
		got := make(chan []sqsapi.Message, 1)
		start := time.Now()
		go func() {
			got <- h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, WaitTimeSeconds: new(int32(20))})
		}()
		synctest.Wait()
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		msgs := <-got
		if len(msgs) != 1 || val(msgs[0].Body) != "fixture" {
			h.Fatalf("messages = %+v, want fixture", msgs)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			h.Errorf("the receive waited %s; the load should wake it at once", elapsed)
		}
	})
}

// armed returns the pending timers by key, rendered as text.
func armed(h *harness) map[string]string {
	out := map[string]string{}
	for _, a := range h.Kernel.Timers().Scheduler().Snapshot() {
		out[string(a.Key.Kind)+" "+a.Key.ID] = a.Due.String()
	}
	return out
}

// The URL echoes the host the client used, and the queue it names is
// reached through any host and from another region's caller only in the
// URL's own region (the region comes from the call, sqs.md finding 6).
func TestQueueURLsEchoTheHostAndKeepTheRegion(t *testing.T) {
	run(t, func(h *harness) {
		h.Endpoint.Host = "sqs.us-east-1.localhost:4566"
		url := h.queue("echo", nil)
		if url != "http://sqs.us-east-1.localhost:4566/000000000000/echo" {
			h.Fatalf("QueueUrl = %q", url)
		}
		h.Endpoint.Host = "127.0.0.1:1"
		h.mustSend(url+"/", "via another host")
		h.Scope.Region = mustRegion(h.T, "eu-west-1")
		_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b"})
		wantError(h.T, err, "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist.")
	})
}

// A QueueUrl that is a bare queue name reaches that queue in the caller's
// account and region, as AWS answers it (LocalStack's AWS-validated
// test_posting_to_queue_via_queue_name, sqs.md 3). A name no queue has is
// NonExistentQueue; anything else that is not a /<account>/<name> URL is
// InvalidAddress.
func TestQueueURLMayBeTheQueueName(t *testing.T) {
	run(t, func(h *harness) {
		h.queue("orders", nil)
		h.queue("orders.fifo", map[sqsapi.QueueAttributeName]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"})
		if out := h.mustSend("orders", "Using name instead of URL"); val(out.MD5OfMessageBody) != "86a83f96652a1bfad3891e7d523750cb" {
			h.Errorf("MD5OfMessageBody = %s", val(out.MD5OfMessageBody))
		}
		if msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: "orders"}); len(msgs) != 1 || val(msgs[0].Body) != "Using name instead of URL" {
			h.Errorf("receive by name = %v, want the message sent by name", msgs)
		}
		if _, err := h.send(&sqsapi.SendMessageInput{QueueUrl: "orders.fifo", MessageBody: "m", MessageGroupId: new("g")}); err != nil {
			h.Errorf("SendMessage to orders.fifo by name = %s: %s", err.ID.QueryCode, err.Message)
		}
		const code, message = "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist."
		_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: "missing", MessageBody: "m"})
		wantError(h.T, err, code, message)
		for _, raw := range []string{"orders/", "/orders", "bad name", "orders.fifo.fifo", ".fifo", strings.Repeat("q", 81)} {
			_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: raw, MessageBody: "m"})
			wantError(h.T, err, "InvalidAddress", "The address "+raw+" is not valid for this endpoint.")
		}
		// The name resolves in the caller's account and region only.
		h.Scope.Account = mustAccount(h.T, "111111111111")
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: "orders", MessageBody: "m"})
		wantError(h.T, err, code, message)
		h.Scope.Account = aws.DefaultAccount()
		h.Scope.Region = mustRegion(h.T, "eu-west-1")
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: "orders", MessageBody: "m"})
		wantError(h.T, err, code, message)
	})
}

func mustRegion(t *testing.T, name string) aws.Region {
	t.Helper()
	r, err := aws.KnownRegions().Parse(name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Waking a long poll 1,000 times leaves no registration behind: two
// polls park on one queue and each send wakes both; one takes the message
// and comes back for the next, the other finds nothing and registers again.
// When both end, by a receive and by the client leaving, no waiter remains.
func TestLongPollsLeaveNoWaiters(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("busy", nil)
		key := store.WaitKey("sqs:arn:aws:sqs:us-east-1:000000000000:busy")
		ctx, cancel := context.WithCancel(h.Context())
		defer cancel()
		const sends = 1000
		taken := make(chan int, 2)
		poll := func() {
			n := 0
			for n < sends {
				out, err := h.Invoke(ctx, op[sqsapi.ReceiveMessageInput, sqsapi.ReceiveMessageOutput](h, "ReceiveMessage"),
					&sqsapi.ReceiveMessageInput{QueueUrl: url, WaitTimeSeconds: new(int32(20))})
				if err != nil {
					break
				}
				n += len(out.Messages)
			}
			taken <- n
		}
		go poll()
		go poll()
		for range sends {
			synctest.Wait()
			if n := h.Kernel.Waiters().Len(key); n != 2 {
				h.Fatalf("parked polls before a send = %d, want 2", n)
			}
			h.mustSend(url, "m")
		}
		synctest.Wait()
		cancel()
		if total := <-taken + <-taken; total != sends {
			h.Errorf("messages received = %d, want %d", total, sends)
		}
		// A cancelled wait deregisters on its own goroutine (context.AfterFunc),
		// which may still be pending when the polls have returned.
		synctest.Wait()
		if n := h.Kernel.Waiters().Len(key); n != 0 {
			h.Errorf("waiters left = %d, want 0", n)
		}
	})
}
