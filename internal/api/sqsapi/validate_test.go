package sqsapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/codegen/serdetest"
)

// TestCreateQueueRejectsALongInvalidQueueName: SQS queue names are 1 to 80
// characters of [A-Za-z0-9_-] with an optional .fifo suffix (sqs.md 3); a
// name breaking both rules reports the pattern before the length, AWS's
// order within a member (error-matrix.md 4).
func TestCreateQueueRejectsALongInvalidQueueName(t *testing.T) {
	name := strings.Repeat("q!", 128) + "q" // 257 characters
	in := &CreateQueueInput{QueueName: name}
	got := in.Validate()
	want := []awserr.Violation{
		{Path: awserr.Path{"queueName"}, Value: awserr.RenderedText{Text: name}, Constraint: awserr.Pattern{Echo: `[a-zA-Z0-9_-]+(\.fifo)?`}},
		{Path: awserr.Path{"queueName"}, Value: awserr.RenderedText{Text: name}, Constraint: awserr.LengthMax{Max: 80}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate = %#v\nwant %#v", got, want)
	}
	err := in.ValidationError(got)
	if err.ID != IdentityInvalidParameterValue() {
		t.Fatalf("identity = %+v, want InvalidParameterValue", err.ID)
	}
	wantMsg := "Value " + name + " for parameter QueueName is invalid. Reason: Can only include alphanumeric characters, hyphens, or underscores. 1 to 80 in length."
	if err.Message != wantMsg {
		t.Fatalf("message = %q\nwant %q", err.Message, wantMsg)
	}
}

func TestCreateQueueAcceptsStandardAndFIFONames(t *testing.T) {
	for _, name := range []string{"q", strings.Repeat("a", 80), "orders-1_x.fifo", strings.Repeat("b", 75) + ".fifo"} {
		if vs := (&CreateQueueInput{QueueName: name}).Validate(); len(vs) != 0 {
			t.Errorf("%q: %v", name, vs)
		}
	}
}

// TestSendMessageRejectsADelayAbove900: DelaySeconds is 0 to 900
// (API_SendMessage), reported in SQS's own text under InvalidParameterValue.
func TestSendMessageRejectsADelayAbove900(t *testing.T) {
	in := &SendMessageInput{QueueUrl: "u", MessageBody: "b", DelaySeconds: new(int32(901))}
	got := in.Validate()
	want := []awserr.Violation{{Path: awserr.Path{"delaySeconds"}, Value: awserr.RenderedText{Text: "901"}, Constraint: awserr.RangeMax{Max: "900"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate = %#v, want %#v", got, want)
	}
	err := in.ValidationError(got)
	if err.ID.QueryCode != "InvalidParameterValue" || err.ID.JSONType != "com.amazonaws.sqs#InvalidParameterValueException" {
		t.Fatalf("identity = %+v", err.ID)
	}
	if err.Message != "Value 901 for parameter DelaySeconds is invalid. Reason: Must be >= 0 and <= 900, if provided." {
		t.Fatalf("message = %q", err.Message)
	}
	if _, ok := err.Origin.(awserr.Validation); !ok {
		t.Fatalf("origin = %T, want Validation", err.Origin)
	}
}

// TestSendMessageViolationsFollowMemberOrder: violations come member by
// member in model order, and the first picks the code: a missing QueueUrl
// is SQS's MissingParameter although DelaySeconds also fails.
func TestSendMessageViolationsFollowMemberOrder(t *testing.T) {
	r := serdetest.NewReplayer([]serdetest.Event{
		{Op: serdetest.OpWriteStruct, Schema: &schemaSendMessageInput},
		{Op: serdetest.OpWriteString, Member: &memberSendMessageInput_MessageGroupId, Value: ""},
		{Op: serdetest.OpWriteInt32, Member: &memberSendMessageInput_DelaySeconds, Value: int32(901)},
		{Op: serdetest.OpWriteString, Member: &memberSendMessageInput_MessageBody, Value: "b"},
		{Op: serdetest.OpCloseStruct},
	})
	var in SendMessageInput
	if err := in.Deserialize(r); err != nil {
		t.Fatal(err)
	}
	got := in.Validate()
	paths := make([]string, len(got))
	for i, v := range got {
		paths[i] = v.Path.String()
	}
	if want := []string{"queueUrl", "delaySeconds", "messageGroupId", "messageGroupId"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("violation paths = %v, want %v", paths, want)
	}
	if _, ok := got[0].Constraint.(awserr.Required); !ok {
		t.Fatalf("first constraint = %T, want Required", got[0].Constraint)
	}
	err := in.ValidationError(got)
	if err.ID != IdentityMissingParameter() || err.Message != "The request must contain the parameter QueueUrl." {
		t.Fatalf("error = %+v %q", err.ID, err.Message)
	}
}

// TestRecordedSQSParameterTexts are the AWS-recorded rows of sqs.md 2.4.
func TestRecordedSQSParameterTexts(t *testing.T) {
	tests := []struct {
		name string
		in   interface {
			Validate() []awserr.Violation
			ValidationError([]awserr.Violation) *awserr.Error
		}
		id   awserr.Identity
		want string
	}{
		{
			"MaxNumberOfMessages=11", &ReceiveMessageInput{QueueUrl: "u", MaxNumberOfMessages: new(int32(11))}, IdentityInvalidParameterValue(),
			"Value 11 for parameter MaxNumberOfMessages is invalid. Reason: Must be between 1 and 10, if provided.",
		},
		{
			"WaitTimeSeconds=21", &ReceiveMessageInput{QueueUrl: "u", WaitTimeSeconds: new(int32(21))}, IdentityInvalidParameterValue(),
			"Value 21 for parameter WaitTimeSeconds is invalid. Reason: Must be >= 0 and <= 20, if provided.",
		},
		{
			"WaitTimeSeconds=-1", &ReceiveMessageInput{QueueUrl: "u", WaitTimeSeconds: new(int32(-1))}, IdentityInvalidParameterValue(),
			"Value -1 for parameter WaitTimeSeconds is invalid. Reason: Must be >= 0 and <= 20, if provided.",
		},
		{
			"MessageGroupId with a space", &SendMessageInput{QueueUrl: "u", MessageBody: "b", MessageGroupId: new("group 123")}, IdentityInvalidParameterValue(),
			"Value group 123 for parameter MessageGroupId is invalid. Reason: MessageGroupId can only include alphanumeric and punctuation characters. 1 to 128 in length.",
		},
		{
			"MessageDeduplicationId of 129", &SendMessageInput{QueueUrl: "u", MessageBody: "b", MessageDeduplicationId: new(strings.Repeat("d", 129))}, IdentityInvalidParameterValue(),
			"Value " + strings.Repeat("d", 129) + " for parameter MessageDeduplicationId is invalid. Reason: MessageDeduplicationId can only include alphanumeric and punctuation characters. 1 to 128 in length.",
		},
		{
			// An empty body is missing, not too short (sqs.md 6.1).
			"empty MessageBody", &SendMessageInput{QueueUrl: "u", MessageBody: ""}, IdentityMissingParameter(),
			"The request must contain the parameter MessageBody.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vs := tt.in.Validate()
			if len(vs) == 0 {
				t.Fatal("no violation")
			}
			err := tt.in.ValidationError(vs)
			if err.ID != tt.id || err.Message != tt.want {
				t.Fatalf("got %s %q\nwant %s %q", err.ID.QueryCode, err.Message, tt.id.QueryCode, tt.want)
			}
		})
	}
}

// TestBatchEntriesKeepAnEmptyBody: a batch entry's failures are per entry
// (sqs.md 6.1), so an empty entry body is the handler's to report.
func TestBatchEntriesKeepAnEmptyBody(t *testing.T) {
	in := &SendMessageBatchInput{QueueUrl: "u", Entries: []SendMessageBatchRequestEntry{{Id: "a", MessageBody: ""}}}
	if vs := in.Validate(); len(vs) != 0 {
		t.Fatalf("Validate = %v, want none", vs)
	}
}

// TestGoBuiltInputsNeedTheirRequiredCollections: internal callers build
// inputs in Go, with no Deserialize to record absence; a nil required list
// is still missing, an empty one is present.
func TestGoBuiltInputsNeedTheirRequiredCollections(t *testing.T) {
	in := &AddPermissionInput{QueueUrl: "u", Label: "l"}
	want := []awserr.Violation{
		{Path: awserr.Path{"aWSAccountIds"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}},
		{Path: awserr.Path{"actions"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}},
	}
	if got := in.Validate(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate = %v\nwant %v", got, want)
	}
	in.AWSAccountIds, in.Actions = []string{}, []string{}
	if got := in.Validate(); len(got) != 0 {
		t.Fatalf("Validate = %v, want none for present empty lists", got)
	}
}

// TestEnumsAreLeftToTheHandler: SQS answers an unknown attribute name with
// InvalidAttributeName from the handler (sqs.md 4), so the generated
// validator must not pre-empt it with an enum violation.
func TestEnumsAreLeftToTheHandler(t *testing.T) {
	in := &GetQueueAttributesInput{QueueUrl: "u", AttributeNames: []QueueAttributeName{"Foobar"}}
	if vs := in.Validate(); len(vs) != 0 {
		t.Fatalf("Validate = %v, want none", vs)
	}
}

func TestHoldVisibilityChecksItsMode(t *testing.T) {
	in := &HoldVisibilityInput{QueueUrl: "u", ReceiptHandles: []string{"h"}, Mode: "PAUSE"}
	got := in.Validate()
	want := []awserr.Violation{{Path: awserr.Path{"mode"}, Value: awserr.RenderedText{Text: "PAUSE"}, Constraint: awserr.Enum{Values: []string{"PIN", "RELEASE"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate = %#v, want %#v", got, want)
	}
}

func TestNoViolationsIsNoError(t *testing.T) {
	if err := (&SendMessageInput{}).ValidationError(nil); err != nil {
		t.Fatalf("ValidationError(nil) = %+v", err)
	}
}
