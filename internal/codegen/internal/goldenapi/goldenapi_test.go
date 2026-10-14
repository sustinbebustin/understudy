// The files here are the generator's golden output: the fixture model in
// internal/codegen/testdata/golden, generated with every member kind, an
// enum, an intEnum, a union, errors with awsQueryError, resource-bound and
// internal operations, a lookaround pattern with its hand validator and
// patternEcho, and the Op handles and Binder of bind_gen.go, compiled
// against internal/kernel.
//
// TestGoldenPackage in internal/codegen fails when generation drifts from
// them; go test ./internal/codegen -run TestGoldenPackage -update rewrites
// them. The tests in this file show the emitted code behaves. The package
// comment is the generated one in service_gen.go.

package goldenapi

import (
	"math/big"
	"reflect"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/codegen/serdetest"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// TestOperations: the closure reaches the resource-bound GetThing (a
// lifecycle read) and ListParts (a collection operation of a nested
// resource) as well as the service's own and the overlay's internal Sweep;
// ordinals follow name order.
func TestOperations(t *testing.T) {
	if _, err := registry.New(Service()); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(Service().Operations))
	for i, op := range Service().Operations {
		names = append(names, op.ID.Name)
		if op.Ordinal != i {
			t.Errorf("%s has ordinal %d at index %d", op.ID.Name, op.Ordinal, i)
		}
	}
	if want := []string{"GetThing", "ListParts", "Ping", "PutKinds", "Sweep"}; !slices.Equal(names, want) {
		t.Fatalf("operations = %v, want %v", names, want)
	}
	sweep, _ := Service().Operation("Sweep")
	if sweep.Kind != registry.Internal || sweep.IAM != nil {
		t.Errorf("Sweep = kind %v, IAM %v; want an internal operation with no IAM action", sweep.Kind, sweep.IAM)
	}
	put, _ := Service().Operation("PutKinds")
	if put.Kind != registry.Modeled || put.SuccessCode != 201 || put.MaxRequestBytes != 1024 || !put.Idempotent {
		t.Errorf("PutKinds = %+v", put)
	}
	// The Service Reference lists golden:TagThing first; the same-name
	// action is primary and the others keep the table's order.
	if got, want := iamStrings(put.IAM), []string{"golden:PutKinds", "golden:TagThing", "iam:PassRole"}; !slices.Equal(got, want) {
		t.Errorf("PutKinds IAM = %v, want %v", got, want)
	}
	parts, _ := Service().Operation("ListParts")
	if got, want := iamStrings(parts.IAM), []string{"golden:ListThingParts"}; !slices.Equal(got, want) {
		t.Errorf("ListParts IAM = %v, want the renamed action %v", got, want)
	}
	want := []awserr.Identity{IdentityKindsFault(), IdentityThrottled(), IdentityInvalidInput()}
	if !slices.Equal(put.Errors, want) {
		t.Errorf("PutKinds errors = %v, want modeled errors then the validation identity", put.Errors)
	}
	ping, _ := Service().Operation("Ping")
	if !ping.Readonly || ping.Input.Type != smithy.TypeUnit || ping.Output.Type != smithy.TypeUnit || ping.IAM != nil {
		t.Errorf("Ping = %+v; want a readonly unit operation the Service Reference authorizes with no action", ping)
	}
}

func iamStrings(acts []registry.IAMAction) []string {
	out := make([]string, len(acts))
	for i, a := range acts {
		out[i] = a.String()
	}
	return out
}

// TestOpHandles: every operation, the internal Sweep included, has a typed
// handle on its own descriptor (ARCHITECTURE.md 3.2), so NewBinder's
// methods bind the operation the service lists.
func TestOpHandles(t *testing.T) {
	handles := map[string]*registry.Operation{
		"GetThing":  OpGetThing.Descriptor(),
		"ListParts": OpListParts.Descriptor(),
		"Ping":      OpPing.Descriptor(),
		"PutKinds":  OpPutKinds.Descriptor(),
		"Sweep":     OpSweep.Descriptor(),
	}
	for _, op := range Service().Operations {
		if handles[op.ID.Name] != op {
			t.Errorf("Op%s does not hold the descriptor Service lists", op.ID.Name)
		}
	}
	if len(handles) != len(Service().Operations) {
		t.Errorf("%d handles for %d operations", len(handles), len(Service().Operations))
	}
}

func TestService(t *testing.T) {
	svc := Service()
	if svc.SigningName != "golden" || svc.IAMPrefix != "golden" || svc.JSONTargetPrefix != "Golden" || svc.QueryVersion != "2026-10-09" || !svc.QueryCompatible {
		t.Fatalf("service = %+v", svc)
	}
	if want := []ref.ProtocolID{ref.AWSJSON10, ref.AWSQuery}; !slices.Equal(svc.Protocols, want) {
		t.Fatalf("protocols = %v, want %v (the model's, then extraProtocols)", svc.Protocols, want)
	}
	if want := (registry.QueryErrorEnvelope{Detail: true}); svc.QueryErrorEnvelope != want {
		t.Fatalf("QueryErrorEnvelope = %+v, want %+v (dev.understudy#queryErrorEnvelope)", svc.QueryErrorEnvelope, want)
	}
}

// TestIdentities: __type is namespaced by errorNamespace for awsJson1_0,
// jsonType renames, the Query code and query-mode status come from
// awsQueryError, the message key from the member's own spelling.
func TestIdentities(t *testing.T) {
	tests := []struct {
		got, want awserr.Identity
	}{
		{IdentityKindsFault(), awserr.Identity{
			Shape: "com.example.golden#KindsFault", JSONType: "com.example.golden.v1#KindsFault", QueryCode: "Kinds.Fault",
			Status: 409, QueryModeStatus: 404, Fault: awserr.Sender, MessageKey: awserr.MessageUpper,
		}},
		{IdentityThrottled(), awserr.Identity{
			Shape: "com.example.golden#Throttled", JSONType: "com.example.golden.v1#Throttled", QueryCode: "Throttled",
			Status: 503, Fault: awserr.Receiver, MessageKey: awserr.NoMessage, Retryable: true,
		}},
		{IdentityInvalidInput(), awserr.Identity{
			Shape: "com.example.golden#InvalidInput", JSONType: "com.example.golden.v1#InvalidInputException", QueryCode: "InvalidInput",
			Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
		}},
		{IdentityUnknownOperationException(), awserr.Identity{
			Shape: "com.amazon.coral.service#UnknownOperationException", JSONType: "com.amazon.coral.service#UnknownOperationException",
			QueryCode: "UnknownOperationException", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
		}},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("identity = %+v\nwant %+v", tt.got, tt.want)
		}
	}
	if ThrottleError() != IdentityThrottled() || MalformedRequestError() != IdentitySerializationException() ||
		Service().MalformedRequestError != IdentitySerializationException() {
		t.Error("service-level identities do not resolve to their shapes")
	}
	if _, ok := UnknownOperationError(awserr.QueryFamily); ok {
		t.Error("the overlay names no Query identity, yet one resolved")
	}
	if got := ErrKindsFault("boom").MessageFor(ref.AWSQuery); got != "Kinds failed for this wsdl version." {
		t.Errorf("Query message = %q", got)
	}
	if got := ErrKindsFault("boom").MessageFor(ref.AWSJSON10); got != "boom" {
		t.Errorf("JSON message = %q", got)
	}
	if _, ok := ErrUnknownOperationException("x").Origin.(awserr.Generic); !ok {
		t.Error("an error outside the service namespace is not Generic")
	}
}

func validInput() *PutKindsInput {
	return &PutKindsInput{Name: "abc", Nested: Nested{Code: "a-b"}}
}

// TestValidateReportsAWSOrder: member by member in model order; within a
// member, pattern then maximum then minimum length (error-matrix.md 4);
// open enums accept anything, closed ones list the modeled values.
func TestValidateReportsAWSOrder(t *testing.T) {
	in := validInput()
	in.Name = "ABCDEFGHIJ"
	in.Count = new(int32(0))
	in.Score = new(0.25)
	in.Data = []byte("12345")
	in.Color = new(Color("purple"))
	in.Level = new(Level(5))
	in.Tags = []string{"a", "b", "c"}
	in.Nested.Code = "-a"
	got := in.Validate()
	text := func(s string) awserr.Rendered { return awserr.RenderedText{Text: s} }
	want := []awserr.Violation{
		{Path: awserr.Path{"name"}, Value: text("ABCDEFGHIJ"), Constraint: awserr.Pattern{Echo: "[a-z]+"}},
		{Path: awserr.Path{"name"}, Value: text("ABCDEFGHIJ"), Constraint: awserr.LengthMax{Max: 8}},
		{Path: awserr.Path{"count"}, Value: text("0"), Constraint: awserr.RangeMin{Min: "1"}},
		{Path: awserr.Path{"score"}, Value: text("0.25"), Constraint: awserr.RangeMin{Min: "0.5"}},
		{Path: awserr.Path{"data"}, Value: awserr.RenderedOmitted{}, Constraint: awserr.LengthMax{Max: 4}},
		{Path: awserr.Path{"level"}, Value: text("5"), Constraint: awserr.Enum{Values: []string{"1", "9"}}},
		{Path: awserr.Path{"tags"}, Value: text("[a, b, c]"), Constraint: awserr.LengthMax{Max: 2}},
		// Code's patternEcho overrides the trimmed default "(?!-)[a-z-]+".
		{Path: awserr.Path{"nested", "code"}, Value: text("-a"), Constraint: awserr.Pattern{Echo: "^(?!-)[a-z-]+$"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate =\n%v\nwant\n%v", got, want)
	}
}

// TestLookaroundValidator: the hand validator stands in for ^(?!-)[a-z-]+$.
func TestLookaroundValidator(t *testing.T) {
	for code, ok := range map[string]bool{"a": true, "a-b": true, "a-": true, "-a": false, "": false, "A": false} {
		in := validInput()
		in.Nested.Code = code
		if got := len(in.Validate()) == 0; got != ok {
			t.Errorf("code %q valid = %v, want %v", code, got, ok)
		}
	}
}

// TestValidationErrorPicksTheRuleOfTheFirstViolation: a member rule with a
// hand-text style beats the service's Coral rule; without it the Coral
// message lists every violation under the code.
func TestValidationErrorPicksTheRuleOfTheFirstViolation(t *testing.T) {
	in := validInput()
	in.Name = "ABC"
	in.Count = new(int32(11))
	err := in.ValidationError(in.Validate())
	if err.ID != IdentityInvalidInput() || err.Message != "Value ABC for parameter Name is invalid. Reason: Names are 1 to 8 lowercase letters." {
		t.Fatalf("error = %s %q", err.ID.QueryCode, err.Message)
	}
	in = validInput()
	in.Count = new(int32(11))
	in.Score = new(0.0)
	err = in.ValidationError(in.Validate())
	want := "2 validation errors detected: Value '11' at 'count' failed to satisfy constraint: Member must have value less than or equal to 10; " +
		"Value '0.0' at 'score' failed to satisfy constraint: Member must have value greater than or equal to 0.5"
	if err.ID != IdentityInvalidInput() || err.Message != want {
		t.Fatalf("error = %s %q\nwant %q", err.ID.QueryCode, err.Message, want)
	}
	if _, ok := err.Origin.(awserr.Validation); !ok {
		t.Fatalf("origin = %T, want Validation", err.Origin)
	}
}

// TestAListElementRuleNamesItsCollection: a validationError on a list's
// member applies to every element and names the structure member that
// holds the list.
func TestAListElementRuleNamesItsCollection(t *testing.T) {
	in := validInput()
	in.Tags = []string{"a", ""}
	vs := in.Validate()
	want := []awserr.Violation{{Path: awserr.Path{"tags", "2", "member"}, Value: awserr.RenderedText{Text: ""}, Constraint: awserr.LengthMin{Min: 1}}}
	if !reflect.DeepEqual(vs, want) {
		t.Fatalf("Validate = %v, want %v", vs, want)
	}
	err := in.ValidationError(vs)
	if err.ID != IdentityInvalidInput() || err.Message != "Value  for parameter Tags is invalid. Reason: Tags are not empty." {
		t.Fatalf("error = %s %q", err.ID.QueryCode, err.Message)
	}
}

// TestSensitiveValuesNeverReachTheMessage: a violation of a sensitive member
// omits its value ("Value at"), as the sensitive trait requires of exception
// messages, whether the member holds the secret or a map or list does, so
// neither the client nor the journal sees it.
func TestSensitiveValuesNeverReachTheMessage(t *testing.T) {
	const secret = "hunter2!x"
	in := validInput()
	in.Secret = new(secret)
	in.Vault = map[string]string{"db": secret}
	in.Hints = []*string{nil, new(secret)}
	err := in.ValidationError(in.Validate())
	entry := func(path string) string {
		return "Value at '" + path + "' failed to satisfy constraint: Member must satisfy regular expression pattern: [^!]*; " +
			"Value at '" + path + "' failed to satisfy constraint: Member must have length less than or equal to 8"
	}
	want := "6 validation errors detected: " + entry("secret") + "; " + entry("vault.db.member") + "; " + entry("hints.2.member")
	if err.Message != want {
		t.Fatalf("message = %q\nwant %q", err.Message, want)
	}
}

// TestGoBuiltInputsNeedTheirRequiredCollections: an internal operation's
// input is built in Go, never decoded; a nil required list is missing all
// the same, an empty one is present.
func TestGoBuiltInputsNeedTheirRequiredCollections(t *testing.T) {
	in := &SweepInput{Before: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)}
	want := []awserr.Violation{{Path: awserr.Path{"ids"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}}}
	if got := in.Validate(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate = %v, want %v", got, want)
	}
	in.Ids = []string{}
	if got := in.Validate(); len(got) != 0 {
		t.Fatalf("Validate = %v, want none for a present empty list", got)
	}
}

// TestAbsentRequiredMembersAndDefaults: a decoded input that lacks its
// required members reports them as null; defaults fill absent members.
func TestAbsentRequiredMembersAndDefaults(t *testing.T) {
	r := serdetest.NewReplayer([]serdetest.Event{{Op: serdetest.OpWriteStruct, Schema: &schemaPutKindsInput}, {Op: serdetest.OpCloseStruct}})
	var in PutKindsInput
	if err := in.Deserialize(r); err != nil {
		t.Fatal(err)
	}
	if in.Retries != 3 || in.Total != 0 {
		t.Fatalf("defaults: Retries %d, Total %d; want 3 and 0", in.Retries, in.Total)
	}
	want := []awserr.Violation{
		{Path: awserr.Path{"name"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}},
		{Path: awserr.Path{"nested"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}},
	}
	if got := in.Validate(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate = %v, want %v", got, want)
	}
}

// TestEveryMemberKindDeserializes replays one value of every member kind
// into the input.
func TestEveryMemberKindDeserializes(t *testing.T) {
	at := time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)
	one := int32(1)
	events := []serdetest.Event{
		{Op: serdetest.OpWriteStruct, Schema: &schemaPutKindsInput},
		{Op: serdetest.OpWriteString, Member: &memberPutKindsInput_Name, Value: "abc"},
		{Op: serdetest.OpWriteBool, Member: &memberPutKindsInput_Flag, Value: true},
		{Op: serdetest.OpWriteInt8, Member: &memberPutKindsInput_Tiny, Value: int8(-1)},
		{Op: serdetest.OpWriteInt16, Member: &memberPutKindsInput_Small, Value: int16(300)},
		{Op: serdetest.OpWriteInt32, Member: &memberPutKindsInput_Count, Value: int32(2)},
		{Op: serdetest.OpWriteInt64, Member: &memberPutKindsInput_Total, Value: int64(1) << 40},
		{Op: serdetest.OpWriteFloat32, Member: &memberPutKindsInput_Ratio, Value: float32(0.5)},
		{Op: serdetest.OpWriteFloat64, Member: &memberPutKindsInput_Score, Value: 2.5},
		{Op: serdetest.OpWriteBigInt, Member: &memberPutKindsInput_Huge, Value: big.NewInt(7)},
		{Op: serdetest.OpWriteBigDecimal, Member: &memberPutKindsInput_Exact, Value: smithy.BigDecimal("1.10")},
		{Op: serdetest.OpWriteTimestamp, Member: &memberPutKindsInput_At, Value: at},
		{Op: serdetest.OpWriteBlob, Member: &memberPutKindsInput_Data, Value: []byte{1, 2}},
		{Op: serdetest.OpWriteDocument, Member: &memberPutKindsInput_Doc, Value: document.Value(document.Bool(true))},
		{Op: serdetest.OpWriteString, Member: &memberPutKindsInput_Color, Value: "dark-blue"},
		{Op: serdetest.OpWriteInt32, Member: &memberPutKindsInput_Level, Value: int32(9)},
		{Op: serdetest.OpWriteList, Member: &memberPutKindsInput_Tags, N: 1},
		{Op: serdetest.OpWriteString, Member: &memberTagList_member, Value: "t"},
		{Op: serdetest.OpCloseList},
		{Op: serdetest.OpWriteList, Member: &memberPutKindsInput_Sparse, N: 2},
		{Op: serdetest.OpWriteNull, Member: &memberSparseCounts_member},
		{Op: serdetest.OpWriteInt32, Member: &memberSparseCounts_member, Value: int32(1)},
		{Op: serdetest.OpCloseList},
		{Op: serdetest.OpWriteMap, Member: &memberPutKindsInput_Labels, N: 1},
		{Op: serdetest.OpWriteMapKey, Value: "k"},
		{Op: serdetest.OpWriteString, Member: &memberLabelMap_value, Value: "v"},
		{Op: serdetest.OpCloseMap},
		{Op: serdetest.OpWriteStruct, Member: &memberPutKindsInput_Nested, Schema: &schemaNested},
		{Op: serdetest.OpWriteString, Member: &memberNested_Code, Value: "a-b"},
		{Op: serdetest.OpCloseStruct},
		{Op: serdetest.OpWriteUnion, Member: &memberPutKindsInput_Choice, Schema: &schemaChoice},
		{Op: serdetest.OpWriteString, Member: &memberChoice_Text, Value: "hi"},
		{Op: serdetest.OpCloseUnion},
		{Op: serdetest.OpWriteString, Member: &memberPutKindsInput_Secret, Value: "s3cret"},
		{Op: serdetest.OpCloseStruct},
	}
	r := serdetest.NewReplayer(events)
	var got PutKindsInput
	if err := got.Deserialize(r); err != nil {
		t.Fatal(err)
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
	want := PutKindsInput{
		Name: "abc", Flag: new(true), Tiny: new(int8(-1)), Small: new(int16(300)), Count: new(int32(2)),
		Total: 1 << 40, Retries: 3, Ratio: new(float32(0.5)), Score: new(2.5), Huge: big.NewInt(7),
		Exact: new(smithy.BigDecimal("1.10")), At: &at, Data: []byte{1, 2}, Doc: document.Bool(true),
		Color: new(ColorDarkBlue), Level: new(LevelHigh), Tags: []string{"t"}, Sparse: []*int32{nil, &one},
		Labels: map[string]string{"k": "v"}, Nested: Nested{Code: "a-b"}, Choice: ChoiceMemberText{Value: "hi"},
		Secret: new("s3cret"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Deserialize =\n%+v\nwant\n%+v", got, want)
	}
	if vs := got.Validate(); len(vs) != 0 {
		t.Fatalf("a complete input has violations %v", vs)
	}
}

// TestOutputSerializesInModelOrder: alwaysEmit makes Count a plain value
// written even at zero; emptyCollection emit writes Items though nil.
func TestOutputSerializesInModelOrder(t *testing.T) {
	var rec serdetest.Recorder
	(&PutKindsOutput{Id: "i"}).Serialize(&rec)
	want := []string{
		"WriteStruct <root> com.example.golden#PutKindsResponse",
		"WriteString Id i",
		"WriteInt32 Count 0",
		"WriteList Items 0",
		"CloseList",
		"CloseStruct",
	}
	if got := rec.Strings(); !slices.Equal(got, want) {
		t.Fatalf("recorded %q\nwant %q", got, want)
	}
}

// TestChoiceRoundTrips: Choice travels both ways, so every variant,
// including the unit member, serializes and deserializes back.
func TestChoiceRoundTrips(t *testing.T) {
	member := &memberPutKindsOutput_Choice
	rapid.Check(t, func(t *rapid.T) {
		var c Choice
		switch rapid.IntRange(0, 2).Draw(t, "variant") {
		case 0:
			c = ChoiceMemberText{Value: rapid.String().Draw(t, "text")}
		case 1:
			c = ChoiceMemberNumber{Value: rapid.Int32().Draw(t, "number")}
		default:
			c = ChoiceMemberEmpty{}
		}
		var rec serdetest.Recorder
		serializeChoice(&rec, member, c)
		r := serdetest.NewReplayer(rec.Events)
		got, err := deserializeChoice(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Done(); err != nil {
			t.Fatal(err)
		}
		if got != c {
			t.Fatalf("round trip = %#v, want %#v", got, c)
		}
	})
}

// TestRedactMasksSensitiveMembersInACopy: the journal stores the copy, where
// a sensitive string the client sent reads "[redacted]", alone, as a map
// value or as a sparse list element, and one it left out stays absent; the
// handler's value keeps its secret.
func TestRedactMasksSensitiveMembersInACopy(t *testing.T) {
	in := validInput()
	in.Secret = new("s3cret")
	in.Vault = map[string]string{"db": "pw"}
	in.Hints = []*string{nil, new("h")}
	red := in.Redact()
	if red == in || red.Secret == nil || *red.Secret != "[redacted]" || red.Name != "abc" {
		t.Fatalf("Redact = %+v", red)
	}
	if want := map[string]string{"db": "[redacted]"}; !reflect.DeepEqual(red.Vault, want) {
		t.Fatalf("Redact Vault = %v, want %v", red.Vault, want)
	}
	if len(red.Hints) != 2 || red.Hints[0] != nil || red.Hints[1] == nil || *red.Hints[1] != "[redacted]" {
		t.Fatalf("Redact Hints = %v, want [nil [redacted]]", red.Hints)
	}
	if *in.Secret != "s3cret" || in.Vault["db"] != "pw" || *in.Hints[1] != "h" {
		t.Fatal("Redact changed its receiver")
	}
	if absent := validInput().Redact(); absent.Secret != nil || absent.Vault != nil || absent.Hints != nil {
		t.Fatalf("Redact of absent secrets = %+v, want them absent", absent)
	}
	out := (&PutKindsOutput{Id: "i", Secret: new("x")}).Redact()
	if out.Secret == nil || *out.Secret != "[redacted]" {
		t.Fatal("output secret survived Redact")
	}
	ping := &PingOutput{}
	if ping.Redact() != ping {
		t.Fatal("a shape with nothing sensitive is returned as is")
	}
}

func TestEnumValues(t *testing.T) {
	if !ColorRed.Known() || Color("RED").Known() || !LevelLow.Known() || Level(2).Known() {
		t.Fatal("Known disagrees with the modeled values")
	}
	if got := string(ColorRed) + "," + string(ColorDarkBlue); got != "red,dark-blue" {
		t.Fatalf("wire values = %s, want the enumValue strings", got)
	}
	if !slices.Equal(ColorRed.Values(), []Color{ColorRed, ColorDarkBlue}) {
		t.Fatal("Values is not in model order")
	}
}
