package fault_test

import (
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// A ConditionalCheckFailedException rule may set members other than Item:
// the members' own message wins, and the value carries it.
// The model spells that member message, so Message is no member of it.
func TestConditionalCheckWithoutItem(t *testing.T) {
	s := generatedStore(t, 1)
	_, err := s.Add(errorRule("ccf", putItem, "ConditionalCheckFailedException", members(t, `{"Message":"m"}`)))
	if want := `com.amazonaws.dynamodb#ConditionalCheckFailedException has no member "Message"; its members are message and Item`; err == nil || err.Reason != want {
		t.Errorf("Add with Message = %v; want %q", err, want)
	}
	if _, err := s.Add(errorRule("ccf", putItem, "ConditionalCheckFailedException", members(t, `{"message":"from the members"}`))); err != nil {
		t.Fatal(err)
	}
	f, ok := s.Evaluate(fault.BeforeHandler, call(putItem)).(fault.Fail)
	if !ok {
		t.Fatal("PutItem did not fail")
	}
	v, ok := f.Err.Members().(*dynamodbapi.ConditionalCheckFailedException)
	if !ok || f.Err.ID != dynamodbapi.IdentityConditionalCheckFailedException() || f.Err.Message != "from the members" || v.Message == nil || *v.Message != "from the members" {
		t.Errorf("injected %s %q carrying %#v; want ConditionalCheckFailedException with the members' message", f.Err.ID.Shape, f.Err.Message, f.Err.Members())
	}
	if f.Err.Origin != (awserr.Injected{Rule: "ccf"}) {
		t.Errorf("origin %#v, want the rule", f.Err.Origin)
	}
}

// The export keeps a rule's members in the order given, while every other
// object is sorted as before.
func TestExportKeepsTheMembersOrder(t *testing.T) {
	s := generatedStore(t, 1)
	if _, err := s.Add(errorRule("tx", transactWriteItems, "TransactionCanceledException",
		members(t, `{"CancellationReasons":[{"Message":"Transaction is ongoing for the item.","Code":"TransactionConflict"}],"Message":"m"}`))); err != nil {
		t.Fatal(err)
	}
	data, err := s.Export()
	want := `{"rules":[{"rule":{"action":{"kind":"error","members":{"CancellationReasons":[{"Message":"Transaction is ongoing for the item.","Code":"TransactionConflict"}],"Message":"m"},` +
		`"shape":"TransactionCanceledException"},"id":"tx","match":{"operations":["dynamodb.TransactWriteItems"]},"phase":"request","trigger":{"kind":"always"}}}],"version":2}`
	if err != nil || string(data) != want {
		t.Errorf("Export = %s, %v\nwant %s", data, err, want)
	}
}

// Version 1 rules carry no members, so a version-1 document whose rule
// sets them is refused as corrupt; the same rule imports under version 2.
func TestVersionOneRuleWithMembers(t *testing.T) {
	doc := func(version string) []byte {
		return []byte(`{"rules":[{"rule":{"action":{"kind":"error","members":{"CancellationReasons":[` + conflictReason + `]},"shape":"TransactionCanceledException"},` +
			`"id":"tx","match":{"operations":["dynamodb.TransactWriteItems"]},"phase":"request","trigger":{"kind":"always"}}}],"version":` + version + `}`)
	}
	s := generatedStore(t, 1)
	ie, ok := errors.AsType[*fault.ImportError](s.Import(doc("1")))
	if want := "it sets action.members, which version 1 rules never carry; the document is corrupt or was not written by Export"; !ok || ie.Index != 0 || ie.Rule != "tx" || ie.Reason != want {
		t.Errorf("Import of version 1 = %v; want rule 0 (tx) refused: %s", ie, want)
	}
	if err := s.Import(doc("2")); err != nil {
		t.Errorf("Import of version 2 = %v", err)
	}
}

// swappedDecoders is a catalogue whose decoder for every shape builds a
// ConditionalCheckFailedException: generated code out of step with the
// catalogue.
type swappedDecoders struct{ fault.Catalogue }

func (swappedDecoders) NewMembers(ref.OperationID, smithy.ShapeID) (awserr.DecodableMembers, bool) {
	return &dynamodbapi.ConditionalCheckFailedException{}, true
}

// A decoder of another identity than the shape the rule resolved is an
// understudy bug, reported with both identities, not as a missing decoder.
func TestDecoderOfAnotherIdentity(t *testing.T) {
	reg, err := registry.New(dynamodbapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	seed := uint64(1)
	s := fault.New(&seed, ids.NewSeeded(seed), swappedDecoders{kernel.NewCatalogue(reg)})
	_, rerr := s.Add(errorRule("tx", transactWriteItems, "TransactionCanceledException", members(t, `{"CancellationReasons":[`+conflictReason+`]}`)))
	want := "the generated decoder of com.amazonaws.dynamodb#TransactionCanceledException builds com.amazonaws.dynamodb#ConditionalCheckFailedException " +
		"(__type com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException) where dynamodb.TransactWriteItems sends " +
		"com.amazonaws.dynamodb#TransactionCanceledException (__type com.amazonaws.dynamodb.v20120810#TransactionCanceledException); " +
		"the catalogue and the generated code disagree, which is an understudy bug, not a fault of the rule, so report it"
	if rerr == nil || rerr.Field != "action.members" || rerr.Reason != want {
		t.Errorf("Add = %v; want a refusal at action.members: %s", rerr, want)
	}
}

// A rule matching several transactional operations checks its members
// against each, and every one of them pads the reasons to its own call.
func TestOneRuleOnEveryTransactionalOperation(t *testing.T) {
	s := generatedStore(t, 1)
	r := errorRule("tx", transactWriteItems, "TransactionCanceledException", members(t, `{"CancellationReasons":[`+conflictReason+`]}`))
	r.Match.Operations = append(r.Match.Operations, transactGetItems, executeTransaction)
	if _, err := s.Add(r); err != nil {
		t.Fatal(err)
	}
	for _, op := range r.Match.Operations {
		_, v := canceled(t, s.Evaluate(fault.BeforeHandler, txCall(op, 2)))
		if got := codesOf(v.CancellationReasons); len(got) != 2 || got[0] != "TransactionConflict" || got[1] != "None" {
			t.Errorf("%s: codes %v, want [TransactionConflict None]", op.Name, got)
		}
	}
}
