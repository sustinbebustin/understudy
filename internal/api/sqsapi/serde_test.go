package sqsapi

import (
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/codegen/serdetest"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// TestSendMessageOutputSerializesMembersInModelOrder records the calls a
// codec sees: the top-level structure, then each set member in model order,
// absent optional members skipped.
func TestSendMessageOutputSerializesMembersInModelOrder(t *testing.T) {
	out := &SendMessageOutput{
		MD5OfMessageBody: new("fafb00f5732ab283681e124bf8747ed1"),
		MessageId:        new("219f8380-5770-4cc2-8c3e-5c715e145f5e"),
		SequenceNumber:   new("18849496460467696128"),
	}
	var rec serdetest.Recorder
	out.Serialize(&rec)
	want := []string{
		"WriteStruct <root> com.amazonaws.sqs#SendMessageResult",
		"WriteString MD5OfMessageBody fafb00f5732ab283681e124bf8747ed1",
		"WriteString MessageId 219f8380-5770-4cc2-8c3e-5c715e145f5e",
		"WriteString SequenceNumber 18849496460467696128",
		"CloseStruct",
	}
	if got := rec.Strings(); !slices.Equal(got, want) {
		t.Fatalf("Serialize recorded\n%q\nwant\n%q", got, want)
	}
}

// sendMessageWire is the member sequence a client's SendMessage carries,
// with message attributes in wire order (deserializers keep any order).
func sendMessageWire() []serdetest.Event {
	attrs := &memberSendMessageInput_MessageAttributes
	value := &memberMessageBodyAttributeMap_value
	return []serdetest.Event{
		{Op: serdetest.OpWriteStruct, Schema: &schemaSendMessageInput},
		{Op: serdetest.OpWriteString, Member: &memberSendMessageInput_QueueUrl, Value: "http://127.0.0.1:4566/000000000000/q"},
		{Op: serdetest.OpWriteString, Member: &memberSendMessageInput_MessageBody, Value: "hello"},
		{Op: serdetest.OpWriteInt32, Member: &memberSendMessageInput_DelaySeconds, Value: int32(5)},
		{Op: serdetest.OpWriteMap, Member: attrs, N: 1},
		{Op: serdetest.OpWriteMapKey, Value: "k"},
		{Op: serdetest.OpWriteStruct, Member: value, Schema: &schemaMessageAttributeValue},
		{Op: serdetest.OpWriteString, Member: &memberMessageAttributeValue_StringValue, Value: "x"},
		{Op: serdetest.OpWriteString, Member: &memberMessageAttributeValue_DataType, Value: "String"},
		{Op: serdetest.OpCloseStruct},
		{Op: serdetest.OpCloseMap},
		{Op: serdetest.OpWriteString, Member: &memberSendMessageInput_MessageGroupId, Value: "g1"},
		{Op: serdetest.OpCloseStruct},
	}
}

// TestSendMessageInputDeserializesTheWireMemberSequence: SendMessageInput
// travels only inward, so it has Deserialize and no Serialize (role
// pruning, ARCHITECTURE.md 5.4); a recorded client sequence replays into it.
func TestSendMessageInputDeserializesTheWireMemberSequence(t *testing.T) {
	r := serdetest.NewReplayer(sendMessageWire())
	var in SendMessageInput
	if err := in.Deserialize(r); err != nil {
		t.Fatal(err)
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
	if in.QueueUrl != "http://127.0.0.1:4566/000000000000/q" || in.MessageBody != "hello" {
		t.Fatalf("required members = %q, %q", in.QueueUrl, in.MessageBody)
	}
	if in.DelaySeconds == nil || *in.DelaySeconds != 5 {
		t.Fatalf("DelaySeconds = %v, want 5", in.DelaySeconds)
	}
	if in.MessageGroupId == nil || *in.MessageGroupId != "g1" || in.MessageDeduplicationId != nil {
		t.Fatalf("FIFO members = %v, %v", in.MessageGroupId, in.MessageDeduplicationId)
	}
	attr, ok := in.MessageAttributes["k"]
	if !ok || attr.DataType != "String" || attr.StringValue == nil || *attr.StringValue != "x" {
		t.Fatalf("MessageAttributes = %+v", in.MessageAttributes)
	}
	if vs := in.Validate(); len(vs) != 0 {
		t.Fatalf("a complete input has violations %v", vs)
	}
}

func TestDeserializeRejectsAMemberOfAnotherShape(t *testing.T) {
	r := serdetest.NewReplayer([]serdetest.Event{
		{Op: serdetest.OpWriteStruct, Schema: &schemaSendMessageInput},
		{Op: serdetest.OpWriteString, Member: &memberGetQueueUrlInput_QueueName, Value: "q"},
	})
	var in SendMessageInput
	if err := in.Deserialize(r); err == nil {
		t.Fatal("Deserialize accepted a member of GetQueueUrlRequest")
	}
}

// TestMessageAttributesRoundTrip: MessageAttributeValue travels both ways
// (SendMessage in, ReceiveMessage out), so its map serializes and
// deserializes back to the same value for any content.
func TestMessageAttributesRoundTrip(t *testing.T) {
	member := &memberSendMessageInput_MessageAttributes
	rapid.Check(t, func(t *rapid.T) {
		attrs := rapid.MapOf(rapid.String(), rapid.Custom(func(t *rapid.T) MessageAttributeValue {
			v := MessageAttributeValue{DataType: rapid.SampledFrom([]string{"String", "Number", "Binary"}).Draw(t, "type")}
			if rapid.Bool().Draw(t, "hasString") {
				v.StringValue = new(rapid.String().Draw(t, "string"))
			}
			if rapid.Bool().Draw(t, "hasBinary") {
				v.BinaryValue = rapid.SliceOfN(rapid.Byte(), 1, 16).Draw(t, "binary")
			}
			v.StringListValues = rapid.SliceOfN(rapid.String(), 0, 3).Draw(t, "strings")
			if len(v.StringListValues) == 0 {
				v.StringListValues = nil
			}
			return v
		})).Draw(t, "attrs")
		var rec serdetest.Recorder
		serializeMessageBodyAttributeMap(&rec, member, attrs)
		r := serdetest.NewReplayer(rec.Events)
		got, err := deserializeMessageBodyAttributeMap(r, member)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Done(); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(attrs) {
			t.Fatalf("got %d attributes, want %d", len(got), len(attrs))
		}
		for k, want := range attrs {
			if !equalAttribute(got[k], want) {
				t.Fatalf("attribute %q = %+v, want %+v", k, got[k], want)
			}
		}
	})
}

func equalAttribute(a, b MessageAttributeValue) bool {
	return a.DataType == b.DataType &&
		(a.StringValue == nil) == (b.StringValue == nil) && (a.StringValue == nil || *a.StringValue == *b.StringValue) &&
		slices.Equal(a.BinaryValue, b.BinaryValue) &&
		slices.Equal(a.StringListValues, b.StringListValues)
}

// TestUnitOutputWritesAnEmptyStructure: an operation with no modeled output
// still hands the codec a structure, so awsJson writes {} and awsQuery its
// envelope.
func TestUnitOutputWritesAnEmptyStructure(t *testing.T) {
	var rec serdetest.Recorder
	(&DeleteMessageOutput{}).Serialize(&rec)
	want := []string{"WriteStruct <root> smithy.api#Unit", "CloseStruct"}
	if got := rec.Strings(); !slices.Equal(got, want) {
		t.Fatalf("recorded %q, want %q", got, want)
	}
	if descDeleteMessage.Output.Type != smithy.TypeUnit {
		t.Fatalf("DeleteMessage output schema type = %v", descDeleteMessage.Output.Type)
	}
}
