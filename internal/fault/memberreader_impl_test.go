package fault_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonprotocol"
)

// parsed reads js as Members through their own decoder.
func parsed(t *testing.T, js string) fault.Members {
	t.Helper()
	var m fault.Members
	if err := m.UnmarshalJSON([]byte(js)); err != nil {
		t.Fatalf("members %s: %v", js, err)
	}
	return m
}

// The reader takes each scalar in the awsJson1_0 form a rule's members
// use: numbers as JSON numbers, integers whole (1e2 is 100), blobs as
// base64, timestamps as epoch seconds, to the nanosecond.
func TestReaderReadsTheAWSJSONForm(t *testing.T) {
	var v jsonprotocol.KitchenSink
	_, err := fault.DecodeMembers(&v, parsed(t, `{"Integer":1e2,"Long":-9007199254740993,"Boolean":true,"Double":1.5,"Float":0.25,
		"Blob":"aGk=","Timestamp":-1.5,"String":"s","ListOfStrings":["a"],"MapOfStrings":{"k":"v"},
		"SimpleStruct":{"Value":"x"},"RecursiveList":[{"Integer":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := jsonprotocol.KitchenSink{
		Integer: new(int32(100)), Long: new(int64(-9007199254740993)), Boolean: new(true), Double: new(1.5), Float: new(float32(0.25)),
		Blob: []byte("hi"), Timestamp: new(time.Unix(-2, 500_000_000).UTC()), String: new("s"), ListOfStrings: []string{"a"},
		MapOfStrings: map[string]string{"k": "v"}, SimpleStruct: &jsonprotocol.SimpleStruct{Value: new("x")},
		RecursiveList: []jsonprotocol.KitchenSink{{Integer: new(int32(1))}},
	}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("decoded %+v\nwant %+v", v, want)
	}
}

// A value outside its shape is refused at its path, saying what to give.
func TestReaderRefusals(t *testing.T) {
	for _, c := range []struct{ members, at, reason string }{
		{`{"Integer":1.5}`, "action.members.Integer", "is 1.5, which is not a whole number; smithy.api#Integer is an integer, a whole number from -2147483648 to 2147483647"},
		{`{"Integer":2147483648}`, "action.members.Integer", "is 2147483648, out of range; smithy.api#Integer is an integer, a whole number from -2147483648 to 2147483647"},
		{`{"Timestamp":"2026-10-26T00:00:00Z"}`, "action.members.Timestamp", "is a JSON string; smithy.api#Timestamp is a timestamp, so give a JSON number"},
		{`{"RecursiveList":[{"Blob":"!"}]}`, "action.members.RecursiveList.0.Blob", "is not base64: "},
		{`{"String":null}`, "action.members.String", "is a JSON null; smithy.api#String is a string and takes no null here"},
		{`{"SimpleStruct":{"Value":"x","Other":1}}`, "action.members.SimpleStruct", `aws.protocoltests.json#SimpleStruct has no member "Other"; its members are Value`},
		{`{"EmptyStruct":{"x":1}}`, "action.members.EmptyStruct", `aws.protocoltests.json#EmptyStruct has no member "x"; it has no members`},
	} {
		var v jsonprotocol.KitchenSink
		_, err := fault.DecodeMembers(&v, parsed(t, c.members))
		me, ok := errors.AsType[*fault.MemberError](err)
		if !ok || me.At != c.at || !strings.HasPrefix(me.Reason, c.reason) {
			t.Errorf("members %s: %v; want a refusal at %s starting %q", c.members, err, c.at, c.reason)
		}
	}
}

// Only a @sparse collection holds a null, and it reads as nil.
func TestReaderSparseNulls(t *testing.T) {
	var v jsonprotocol.SparseNullsOperationInputOutput
	if _, err := fault.DecodeMembers(&v, parsed(t, `{"sparseStringList":[null,"a"],"sparseStringMap":{"k":null}}`)); err != nil {
		t.Fatal(err)
	}
	want := jsonprotocol.SparseNullsOperationInputOutput{SparseStringList: []*string{nil, new("a")}, SparseStringMap: map[string]*string{"k": nil}}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("decoded %+v, want %+v", v, want)
	}
}

// A duplicate name inside the members is refused at any depth, naming
// where: by Members' own decoder, and through the rule form at the object
// that holds it.
func TestNestedDuplicateNames(t *testing.T) {
	var m fault.Members
	err := m.UnmarshalJSON([]byte(`{"CancellationReasons":[{"Code":"None","Code":"None"}]}`))
	if err == nil || err.Error() != `fault: duplicate member "Code" in CancellationReasons.0` {
		t.Errorf("UnmarshalJSON = %v; want the duplicate Code in CancellationReasons.0", err)
	}
	var r fault.Rule
	err = r.UnmarshalJSON([]byte(`{"phase":"request","match":{"operations":["dynamodb.TransactWriteItems"]},` +
		`"action":{"kind":"error","shape":"TransactionCanceledException","members":{"CancellationReasons":[{"Code":"None","Code":"None"}]}},"trigger":{"kind":"always"}}`))
	pe, ok := errors.AsType[*fault.RuleParseError](err)
	if !ok || pe.Path != "action.members.CancellationReasons.0" || pe.Reason != `is invalid: duplicate member "Code"` {
		t.Errorf("UnmarshalJSON = %v; want a RuleParseError at action.members.CancellationReasons.0", err)
	}
}

// Members built in Go skip their JSON decoder, so Add refuses a name given
// twice itself: the store would check the first CancellationReasons and the
// generated decoder keep the second, injecting reasons that were never
// checked against the items (smithy-protocol-tests.md 14.14).
func TestAddRefusesNamesGivenTwice(t *testing.T) {
	reason := func(code string) document.Map { return document.Map{{Key: "Code", Value: document.String(code)}} }
	key := func(s string) document.Map { return document.Map{{Key: "S", Value: document.String(s)}} }
	withItem := append(reason("ConditionalCheckFailed"), document.Entry{Key: "Item", Value: document.Map{{Key: "pk", Value: key("a")}, {Key: "pk", Value: key("b")}}})
	for _, c := range []struct {
		name    string
		members fault.Members
		at      string
		reason  string
	}{
		{
			"member",
			fault.Members{{Key: "CancellationReasons", Value: document.List{reason("TransactionConflict")}}, {Key: "CancellationReasons", Value: document.List{reason("None")}}},
			"action.members", `has the member "CancellationReasons" twice; give each member once`,
		},
		{
			"map key",
			fault.Members{{Key: "CancellationReasons", Value: document.List{withItem}}},
			"action.members.CancellationReasons.0.Item", `has the key "pk" twice; give each key once`,
		},
	} {
		_, err := generatedStore(t, 1).Add(errorRule("r", transactWriteItems, "TransactionCanceledException", c.members))
		if err == nil || err.Field != c.at || err.Reason != c.reason {
			t.Errorf("%s: Add = %v; want a refusal at %s: %s", c.name, err, c.at, c.reason)
		}
	}
}
