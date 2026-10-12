package sqsapi

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

func TestServiceDescriptorIsAccepted(t *testing.T) {
	r, err := registry.New(Service())
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := r.ByJSONTarget("AmazonSQS")
	if !ok || svc.ID != "sqs" {
		t.Fatalf("ByJSONTarget(AmazonSQS) = %v, %v", svc, ok)
	}
	op, ok := r.ByQueryAction("SendMessage", "2012-11-05")
	if !ok || op != &descSendMessage {
		t.Fatalf("ByQueryAction(SendMessage) = %v, %v", op, ok)
	}
	if !svc.QueryCompatible || svc.XMLNamespace != "http://queue.amazonaws.com/doc/2012-11-05/" {
		t.Fatalf("service = %+v", svc)
	}
	if want := []ref.ProtocolID{ref.AWSJSON10, ref.AWSQuery}; len(svc.Protocols) != 2 || svc.Protocols[0] != want[0] || svc.Protocols[1] != want[1] {
		t.Fatalf("protocols = %v, want %v", svc.Protocols, want)
	}
}

// TestOperations: the model's 23 operations plus the internal HoldVisibility
// (ADR 0006) and MessageCauses (ADR 0007), whose kind keeps the edge from
// claiming them.
func TestOperations(t *testing.T) {
	ops := Service().Operations
	if len(ops) != 25 {
		t.Fatalf("%d operations, want 25", len(ops))
	}
	for _, name := range []string{"HoldVisibility", "MessageCauses"} {
		op, ok := Service().Operation(name)
		if !ok || op.Kind != registry.Internal || op.IAM != nil {
			t.Fatalf("%s = %+v", name, op)
		}
	}
	send, _ := Service().Operation("SendMessage")
	if send.Kind != registry.Modeled || send.MaxRequestBytes != 8<<20 || send.SuccessCode != 200 {
		t.Fatalf("SendMessage = %+v", send)
	}
	if len(send.IAM) != 1 || send.IAM[0].String() != "sqs:SendMessage" {
		t.Fatalf("SendMessage IAM = %v", send.IAM)
	}
}

// TestIdentities pins the wire identity of the errors the research
// recorded (sqs.md 2.2, 2.4).
func TestIdentities(t *testing.T) {
	tests := []struct {
		name string
		got  awserr.Identity
		want awserr.Identity
	}{
		{"QueueDoesNotExist", IdentityQueueDoesNotExist(), awserr.Identity{
			Shape: "com.amazonaws.sqs#QueueDoesNotExist", JSONType: "com.amazonaws.sqs#QueueDoesNotExist",
			QueryCode: "AWS.SimpleQueueService.NonExistentQueue", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
		}},
		{"PurgeQueueInProgress is 403", IdentityPurgeQueueInProgress(), awserr.Identity{
			Shape: "com.amazonaws.sqs#PurgeQueueInProgress", JSONType: "com.amazonaws.sqs#PurgeQueueInProgress",
			QueryCode: "AWS.SimpleQueueService.PurgeQueueInProgress", Status: 403, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
		}},
		{"MissingParameter", IdentityMissingParameter(), awserr.Identity{
			Shape: "com.amazonaws.sqs#MissingParameter", JSONType: "com.amazonaws.sqs#MissingRequiredParameterException",
			QueryCode: "MissingParameter", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
		}},
		{"RequestThrottled is the throttle identity, so retryable", IdentityRequestThrottled(), awserr.Identity{
			Shape: "com.amazonaws.sqs#RequestThrottled", JSONType: "com.amazonaws.sqs#RequestThrottled",
			QueryCode: "RequestThrottled", Status: 403, Fault: awserr.Sender, MessageKey: awserr.MessageLower, Retryable: true,
		}},
		{"MessageNotInflight has no message", IdentityMessageNotInflight(), awserr.Identity{
			Shape: "com.amazonaws.sqs#MessageNotInflight", JSONType: "com.amazonaws.sqs#MessageNotInflight",
			QueryCode: "AWS.SimpleQueueService.MessageNotInflight", Status: 400, Fault: awserr.Sender, MessageKey: awserr.NoMessage,
		}},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %+v\nwant %+v", tt.name, tt.got, tt.want)
		}
	}
}

// TestGenericIdentitiesMatchTheCatalogue: overlays/common.json and the
// awserr catalogue describe the same protocol-level errors; they must not
// drift apart.
func TestGenericIdentitiesMatchTheCatalogue(t *testing.T) {
	if got, ok := UnknownOperationError(awserr.JSONFamily); !ok || got != awserr.UnknownOperationException() {
		t.Errorf("JSON unknown operation = %+v, want %+v", got, awserr.UnknownOperationException())
	}
	if got, ok := UnknownOperationError(awserr.QueryFamily); !ok || got != awserr.InvalidAction() {
		t.Errorf("Query unknown operation = %+v, want %+v", got, awserr.InvalidAction())
	}
	if got := ThrottleError(); got != IdentityRequestThrottled() || got.Status != 403 {
		t.Errorf("ThrottleError = %+v", got)
	}
}

func TestQueueDoesNotExistHasItsQueryText(t *testing.T) {
	err := ErrQueueDoesNotExist("The specified queue does not exist.")
	if got := err.MessageFor(ref.AWSJSON10); got != "The specified queue does not exist." {
		t.Errorf("JSON message = %q", got)
	}
	if got := err.MessageFor(ref.AWSQuery); got != "The specified queue does not exist for this wsdl version." {
		t.Errorf("Query message = %q", got)
	}
	if _, ok := err.Origin.(awserr.Modeled); !ok {
		t.Errorf("origin = %T", err.Origin)
	}
}

// TestOperationErrorsIncludeValidationIdentities: fault rules validate
// against Operation.Errors, so the codes validation can answer are there.
func TestOperationErrorsIncludeValidationIdentities(t *testing.T) {
	send, _ := Service().Operation("SendMessage")
	have := map[string]bool{}
	for _, id := range send.Errors {
		have[id.Shape.Name()] = true
	}
	for _, name := range []string{"InvalidMessageContents", "QueueDoesNotExist", "InvalidParameterValue", "MissingParameter"} {
		if !have[name] {
			t.Errorf("SendMessage errors lack %s: %v", name, send.Errors)
		}
	}
}

func TestRedactReturnsInputsWithoutSensitiveMembersUnchanged(t *testing.T) {
	in := &SendMessageInput{QueueUrl: "u", MessageBody: "b"}
	if in.Redact() != in {
		t.Fatal("SendMessageInput has no sensitive member; Redact should return it")
	}
}
