package fault_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

var (
	transactWriteItems = ref.OperationID{Service: "dynamodb", Name: "TransactWriteItems"}
	transactGetItems   = ref.OperationID{Service: "dynamodb", Name: "TransactGetItems"}
	executeTransaction = ref.OperationID{Service: "dynamodb", Name: "ExecuteTransaction"}
)

// The documented TransactionConflict reason (smithy-protocol-tests.md
// 12.2, aws-doc).
const conflictReason = `{"Code":"TransactionConflict","Message":"Transaction is ongoing for the item."}`

// fataler is what *testing.T and *rapid.T share.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// generatedStore is a fault store validating against the kernel's view of
// the generated DynamoDB and SQS descriptors, whose error shapes decode
// members (DynamoDB) or do not (SQS).
func generatedStore(t fataler, seed uint64) *fault.Store {
	t.Helper()
	reg, err := registry.New(dynamodbapi.Service(), sqsapi.Service())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return fault.New(&seed, ids.NewSeeded(seed), kernel.NewCatalogue(reg, nil))
}

// members parses a JSON object as rule members, without going through
// fault.Members' own decoder.
func members(t fataler, js string) fault.Members {
	t.Helper()
	d := jsonx.NewDecoder([]byte(js))
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		t.Fatalf("members %s: %v", js, err)
	}
	m, ok := v.(document.Map)
	if !ok {
		t.Fatalf("members %s is a %T, not an object", js, v)
	}
	if m == nil {
		m = document.Map{}
	}
	return fault.Members(m)
}

// errorRule injects shape with members on op.
func errorRule(id string, op ref.OperationID, shape smithy.ShapeID, m fault.Members) fault.Rule {
	return fault.Rule{
		ID:      ref.RuleID(id),
		Match:   fault.Match{Operations: []ref.OperationID{op}},
		Phase:   fault.Request,
		Action:  fault.Error{Shape: shape, Members: m},
		Trigger: fault.Always{},
	}
}

// txCall is an external call of op whose transaction has items items.
func txCall(op ref.OperationID, items int) fault.MatchInput {
	in := call(op)
	in.Touched = nil
	in.Items = items
	return in
}

// canceled returns the TransactionCanceledException a decision fails with.
func canceled(t fataler, d fault.Decision) (*awserr.Error, *dynamodbapi.TransactionCanceledException) {
	t.Helper()
	f, ok := d.(fault.Fail)
	if !ok {
		t.Fatalf("decision = %#v, want Fail", d)
	}
	v, ok := f.Err.Members().(*dynamodbapi.TransactionCanceledException)
	if !ok {
		t.Fatalf("the injected error carries %#v, want *dynamodbapi.TransactionCanceledException", f.Err.Members())
	}
	return f.Err, v
}

func codesOf(reasons []dynamodbapi.CancellationReason) []string {
	out := make([]string, len(reasons))
	for i, r := range reasons {
		if r.Code != nil {
			out[i] = *r.Code
		}
	}
	return out
}

// canceledMessage is the message of a cancelled transaction: the reason
// codes in request order inside AWS's frame (smithy-protocol-tests.md 12.3,
// third-party-recorded; 12.4).
func canceledMessage(codes []string) string {
	return "Transaction cancelled, please refer cancellation reasons for specific reasons [" + strings.Join(codes, ", ") + "]"
}

func TestMembersDecodeAsGivenInTheRuleForm(t *testing.T) {
	doc := `{"phase": "request", "match": {"operations": ["dynamodb.TransactWriteItems"]},
		"action": {"kind": "error", "shape": "TransactionCanceledException",
		 "members": {"CancellationReasons": [` + conflictReason + `]}},
		"trigger": {"kind": "always"}}`
	var r fault.Rule
	if err := jsonx.Unmarshal([]byte(doc), &r); err != nil {
		t.Fatal(err)
	}
	a, ok := r.Action.(fault.Error)
	want := members(t, `{"CancellationReasons":[`+conflictReason+`]}`)
	if !ok || a.Shape != "TransactionCanceledException" || !reflect.DeepEqual(a.Members, want) {
		t.Errorf("decoded action %#v, want an error action with members %#v", r.Action, want)
	}
	var none fault.Rule
	if err := jsonx.Unmarshal([]byte(strings.Replace(doc, `{"CancellationReasons": [`+conflictReason+`]}`, "null", 1)), &none); err != nil {
		t.Fatal(err)
	}
	if a, ok := none.Action.(fault.Error); !ok || a.Members != nil {
		t.Errorf(`"members": null decoded as %#v, want no members`, none.Action)
	}
}

// members is a JSON object of distinct member names; anything else is
// refused naming action.members.
func TestMembersMustBeAnObjectOfDistinctNames(t *testing.T) {
	rule := func(m string) string {
		return `{"phase":"request","match":{"operations":["dynamodb.TransactWriteItems"]},"action":{"kind":"error","shape":"TransactionCanceledException","members":` +
			m + `},"trigger":{"kind":"always"}}`
	}
	for _, c := range []struct{ members, reason string }{
		{`[` + conflictReason + `]`, "is a JSON array; expected an object"},
		{`"CancellationReasons"`, "is a JSON string; expected an object"},
		{`{"CancellationReasons":[` + conflictReason + `],"CancellationReasons":[]}`, `is invalid: duplicate member "CancellationReasons"`},
	} {
		var r fault.Rule
		err := r.UnmarshalJSON([]byte(rule(c.members)))
		pe, ok := errors.AsType[*fault.RuleParseError](err)
		if !ok || pe.Path != "action.members" || pe.Reason != c.reason {
			t.Errorf("members %s: %v; want a RuleParseError at action.members that %s", c.members, err, c.reason)
		}
	}
}

// Members are written compact, in the order given, and a rule without them
// writes no members at all.
func TestMembersEncodeCompactInOrder(t *testing.T) {
	m := members(t, `{ "Message" : "m", "CancellationReasons" : [ {"Code": "None"}, `+conflictReason+` ] }`)
	got, err := m.MarshalJSON()
	if want := `{"Message":"m","CancellationReasons":[{"Code":"None"},` + conflictReason + `]}`; err != nil || string(got) != want {
		t.Errorf("MarshalJSON = %s, %v; want %s", got, err, want)
	}
	with, err := errorRule("tx", transactWriteItems, "TransactionCanceledException", m).MarshalJSON()
	if err != nil || !strings.Contains(string(with), `"members":{"Message":"m","CancellationReasons":`) {
		t.Errorf("rule with members = %s, %v; want the members in its action", with, err)
	}
	without, err := errorRule("q", sendMessage, "QueueDoesNotExist", nil).MarshalJSON()
	if err != nil || strings.Contains(string(without), "members") {
		t.Errorf("rule without members = %s, %v; want no members", without, err)
	}
}

// drawMembers draws a members object of distinct names whose values are
// any JSON but null.
func drawMembers(t *rapid.T) fault.Members {
	var value func(depth int, label string) document.Value
	value = func(depth int, label string) document.Value {
		kinds := 3
		if depth > 0 {
			kinds = 5
		}
		switch rapid.IntRange(0, kinds-1).Draw(t, label) {
		case 0:
			return document.String(rapid.StringMatching(`[A-Za-z .]{0,8}`).Draw(t, label+".s"))
		case 1:
			return document.Number(strconv.Itoa(rapid.IntRange(-1000, 1000).Draw(t, label+".n")))
		case 2:
			return document.Bool(rapid.Bool().Draw(t, label+".b"))
		case 3:
			n := rapid.IntRange(0, 3).Draw(t, label+".len")
			l := make(document.List, 0, n)
			for i := range n {
				l = append(l, value(depth-1, label+"["+strconv.Itoa(i)+"]"))
			}
			return l
		default:
			names := rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Z][a-z]{0,5}`), 0, 3, func(s string) string { return s }).Draw(t, label+".names")
			m := make(document.Map, 0, len(names))
			for _, k := range names {
				m = append(m, document.Entry{Key: k, Value: value(depth-1, label+"."+k)})
			}
			return m
		}
	}
	names := rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Z][a-z]{0,5}`), 1, 3, func(s string) string { return s }).Draw(t, "names")
	m := make(document.Map, 0, len(names))
	for _, k := range names {
		m = append(m, document.Entry{Key: k, Value: value(2, k)})
	}
	return fault.Members(m)
}

// A rule with members reads back as itself from its JSON form, the shared
// form of the admin API, the config file and snapshots.
func TestRuleWithMembersJSONRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := drawMembers(t)
		r := errorRule(rapid.StringMatching(`[a-z]{1,8}`).Draw(t, "id"), transactWriteItems, "TransactionCanceledException", m)
		r.Action = fault.Error{Shape: "TransactionCanceledException", Message: rapid.StringMatching(`[a-z ]{0,8}`).Draw(t, "message"), Members: m}
		data, err := jsonx.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var got fault.Rule
		if err := jsonx.Unmarshal(data, &got); err != nil {
			t.Fatalf("decoding %s: %v", data, err)
		}
		if !reflect.DeepEqual(got, r) {
			t.Fatalf("round trip of %s gave %#v, want %#v", data, got, r)
		}
	})
}

// A TransactionCanceledException rule is accepted on each operation that
// sends the error (smithy-protocol-tests.md 12.1), and its error is not one
// the SDKs retry: "If that request fails, AWS SDKs do not retry the
// request" (12.2, aws-doc), so it draws no retry warning.
func TestTransactionCanceledRulesAreAccepted(t *testing.T) {
	s := generatedStore(t, 1)
	for _, op := range []ref.OperationID{transactWriteItems, transactGetItems, executeTransaction} {
		r := errorRule("tx-"+strings.ToLower(op.Name), op, "TransactionCanceledException", members(t, `{"CancellationReasons":[`+conflictReason+`]}`))
		if _, err := s.Add(r); err != nil {
			t.Errorf("%s: Add = %v, want the rule accepted", op.Name, err)
			continue
		}
		if w, ok := s.RetryableWarning(r); ok {
			t.Errorf("%s: RetryableWarning = %q, want none", op.Name, w)
		}
	}
}

// Each item past the rule's last reason gets exactly {"Code":"None"}, so the
// error carries one reason per request item, in item order
// (smithy-protocol-tests.md 12.2, 14.9). The message lists the codes
// (12.4), and the error has TransactionCanceledException's identity with
// the rule as its origin.
func TestCancellationReasonsArePaddedToTheItems(t *testing.T) {
	s := generatedStore(t, 1)
	if _, err := s.Add(errorRule("tx", transactWriteItems, "TransactionCanceledException",
		members(t, `{"CancellationReasons":[{"Code":"None"},`+conflictReason+`]}`))); err != nil {
		t.Fatal(err)
	}
	e, v := canceled(t, s.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 3)))
	msg := canceledMessage([]string{"None", "TransactionConflict", "None"})
	if e.ID != dynamodbapi.IdentityTransactionCanceledException() || e.Message != msg || e.Origin != (awserr.Injected{Rule: "tx"}) {
		t.Errorf("error = %s %q origin %#v; want TransactionCanceledException %q injected by tx", e.ID.Shape, e.Message, e.Origin, msg)
	}
	want := []dynamodbapi.CancellationReason{
		{Code: new("None")},
		{Code: new("TransactionConflict"), Message: new("Transaction is ongoing for the item.")},
		{Code: new("None")},
	}
	if !reflect.DeepEqual(v.CancellationReasons, want) {
		t.Errorf("reasons = %+v, want %+v", v.CancellationReasons, want)
	}
	if v.Message == nil || *v.Message != msg {
		t.Errorf("the value's Message = %v, want %q", v.Message, msg)
	}
}

// documentedCodes are the cancellation codes the API reference lists
// (smithy-protocol-tests.md 12.2).
var documentedCodes = []string{"None", "ConditionalCheckFailed", "ItemCollectionSizeLimitExceeded", "TransactionConflict", "ProvisionedThroughputExceeded", "ThrottlingError", "ValidationError"}

// A rule giving k reasons matches a call of n items only when n >= k, and
// then answers exactly n reasons: the rule's k in order, then None for each
// item after them (smithy-protocol-tests.md 14.9).
func TestPaddingProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		k := rapid.IntRange(1, 5).Draw(t, "k")
		codes := make([]string, k)
		parts := make([]string, k)
		failing := rapid.IntRange(0, k-1).Draw(t, "failing")
		for i := range codes {
			codes[i] = rapid.SampledFrom(documentedCodes).Draw(t, "code"+strconv.Itoa(i))
			if i == failing && codes[i] == "None" {
				codes[i] = "TransactionConflict"
			}
			parts[i] = `{"Code":"` + codes[i] + `"}`
		}
		s := generatedStore(t, 1)
		if _, err := s.Add(errorRule("tx", transactWriteItems, "TransactionCanceledException",
			members(t, `{"CancellationReasons":[`+strings.Join(parts, ",")+`]}`))); err != nil {
			t.Fatal(err)
		}
		n := rapid.IntRange(0, 8).Draw(t, "items")
		d := s.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, n))
		if n < k {
			if _, ok := d.(fault.None); !ok {
				t.Fatalf("%d reasons on a %d-item call = %#v, want no match", k, n, d)
			}
			return
		}
		e, v := canceled(t, d)
		want := append(append([]string{}, codes...), make([]string, n-k)...)
		for i := k; i < n; i++ {
			want[i] = "None"
		}
		if got := codesOf(v.CancellationReasons); !reflect.DeepEqual(got, want) {
			t.Fatalf("codes = %v, want %v", got, want)
		}
		for i := k; i < n; i++ {
			if r := v.CancellationReasons[i]; r.Message != nil || r.Item != nil {
				t.Fatalf("padded reason %d = %+v, want only Code None (smithy-protocol-tests.md 12.2)", i, r)
			}
		}
		if e.Message != canceledMessage(want) {
			t.Fatalf("message = %q, want %q", e.Message, canceledMessage(want))
		}
	})
}

// A call the rule does not match because it has too few items spends no
// hit of a NextN budget and draws nothing from a Probability stream.
func TestTooFewItemsSpendNothing(t *testing.T) {
	two := members(t, `{"CancellationReasons":[{"Code":"None"},`+conflictReason+`]}`)
	s := generatedStore(t, 1)
	r := errorRule("tx", transactWriteItems, "TransactionCanceledException", two)
	r.Trigger = fault.NextN{N: 1}
	if _, err := s.Add(r); err != nil {
		t.Fatal(err)
	}
	if d := s.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 1)); d != (fault.None{}) {
		t.Fatalf("a 1-item call = %#v, want no match", d)
	}
	if got := s.List(); len(got) != 1 || got[0].Hits != 0 {
		t.Fatalf("after a 1-item call the rules are %+v, want tx with no hit", got)
	}
	if _, ok := s.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 2)).(fault.Fail); !ok {
		t.Fatal("a 2-item call did not fail")
	}

	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Uint64().Draw(t, "seed")
		p := r
		p.Trigger = fault.Probability{P: 0.5}
		a, b := generatedStore(t, seed), generatedStore(t, seed)
		for _, st := range []*fault.Store{a, b} {
			if _, err := st.Add(p); err != nil {
				t.Fatal(err)
			}
		}
		for i := range 20 {
			for range rapid.IntRange(0, 2).Draw(t, "short"+strconv.Itoa(i)) {
				a.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 1))
			}
			_, x := a.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 2)).(fault.Fail)
			_, y := b.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 2)).(fault.Fail)
			if x != y {
				t.Fatalf("call %d: with 1-item calls between, the rule fired %t; without, %t", i, x, y)
			}
		}
	})
}

// The message of an injected error with members: the members' own message
// member, else action.message, else AWS's cancellation text built from the
// codes, else the text naming the rule (smithy-protocol-tests.md 14.9).
func TestInjectedMessagePrecedence(t *testing.T) {
	reasons := `"CancellationReasons":[` + conflictReason + `]`
	s := generatedStore(t, 1)
	for _, c := range []struct {
		id, message, members, want string
	}{
		{"own", "", `{"Message":"from the members",` + reasons + `}`, "from the members"},
		{"action", "from the action", `{` + reasons + `}`, "from the action"},
		{"codes", "", `{` + reasons + `}`, canceledMessage([]string{"TransactionConflict"})},
	} {
		r := errorRule(c.id, transactWriteItems, "TransactionCanceledException", members(t, c.members))
		r.Action = fault.Error{Shape: "TransactionCanceledException", Message: c.message, Members: members(t, c.members)}
		if _, err := s.Add(r); err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		e, v := canceled(t, s.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 1)))
		if e.Message != c.want || v.Message == nil || *v.Message != c.want {
			t.Errorf("%s: message %q, value message %v; want %q in both", c.id, e.Message, v.Message, c.want)
		}
		s.Remove(r.ID)
	}

	// A shape whose members carry no message keeps the message naming the
	// rule; ThrottlingReasons is one of the members a rule can set
	// (smithy-protocol-tests.md 11.5, 14.9; com.amazonaws.dynamodb#ThrottlingReason
	// has members reason and resource).
	r := errorRule("ptee", putItem, "ProvisionedThroughputExceededException",
		members(t, `{"ThrottlingReasons":[{"reason":"TableWriteProvisionedThroughputExceeded","resource":"arn:aws:dynamodb:us-east-1:000000000000:table/Orders"}]}`))
	if _, err := s.Add(r); err != nil {
		t.Fatal(err)
	}
	f, ok := s.Evaluate(fault.BeforeHandler, call(putItem)).(fault.Fail)
	if !ok {
		t.Fatal("PutItem did not fail")
	}
	v, ok := f.Err.Members().(*dynamodbapi.ProvisionedThroughputExceededException)
	if !ok || len(v.ThrottlingReasons) != 1 || v.ThrottlingReasons[0].Reason == nil || *v.ThrottlingReasons[0].Reason != "TableWriteProvisionedThroughputExceeded" {
		t.Fatalf("members = %#v, want the throttling reason given", f.Err.Members())
	}
	if want := "Injected by understudy fault rule ptee."; f.Err.Message != want {
		t.Errorf("message = %q, want %q", f.Err.Message, want)
	}
}

// Every other rule ignores the item count.
func TestOtherRulesIgnoreTheItemCount(t *testing.T) {
	s := generatedStore(t, 1)
	if _, err := s.Add(errorRule("idem", transactWriteItems, "IdempotentParameterMismatchException", nil)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 0)).(fault.Fail); !ok {
		t.Error("a rule without members did not match a call with no item count")
	}
}

// Refusals of members name the member at fault and say how to fix the
// rule; they are understudy's own admin texts, two of them from
// smithy-protocol-tests.md 14.12, and none lists known shapes.
func TestMembersRefusals(t *testing.T) {
	tce := smithy.ShapeID("TransactionCanceledException")
	reasons101 := strings.TrimSuffix(strings.Repeat(conflictReason+",", 101), ",")
	queueDoesNotExist := sqsapi.IdentityQueueDoesNotExist().Shape
	for _, c := range []struct {
		name   string
		rule   fault.Rule
		field  string
		reason string
		prefix bool
	}{
		{
			"empty members", errorRule("r", transactWriteItems, tce, members(t, `{}`)),
			"action.members", "is empty; leave it out, or set members of com.amazonaws.dynamodb#TransactionCanceledException", false,
		},
		{
			"message twice", func() fault.Rule {
				m := members(t, `{"Message":"m","CancellationReasons":[`+conflictReason+`]}`)
				r := errorRule("r", transactWriteItems, tce, m)
				r.Action = fault.Error{Shape: tce, Message: "also m", Members: m}
				return r
			}(),
			"action.members.Message", "repeats action.message; give the message in one of them", false,
		},
		{
			"no generated decoder", errorRule("r", sendMessage, "QueueDoesNotExist", members(t, `{"QueueUrl":"u"}`)),
			"action.members", string(queueDoesNotExist) + " has no generated decoder, so a fault rule cannot set its members; leave action.members out", false,
		},
		{
			"unknown member", errorRule("r", transactWriteItems, tce, members(t, `{"CancellationReasons":[`+conflictReason+`],"Reasons":[]}`)),
			"action.members", `com.amazonaws.dynamodb#TransactionCanceledException has no member "Reasons"; its members are Message and CancellationReasons`, false,
		},
		{
			"unknown nested member", errorRule("r", transactWriteItems, tce, members(t, `{"CancellationReasons":[{"Code":"TransactionConflict","Reason":"x"}]}`)),
			"action.members.CancellationReasons.0", `com.amazonaws.dynamodb#CancellationReason has no member "Reason"; its members are Item, Code and Message`, false,
		},
		{
			"object for a list", errorRule("r", transactWriteItems, tce, members(t, `{"CancellationReasons":`+conflictReason+`}`)),
			"action.members.CancellationReasons", "is a JSON object; ", true,
		},
		{
			"number for a string", errorRule("r", transactWriteItems, tce, members(t, `{"CancellationReasons":[{"Code":7}]}`)),
			"action.members.CancellationReasons.0.Code", "is a JSON number; ", true,
		},
		{
			"null in a dense list", errorRule("r", transactWriteItems, tce, members(t, `{"CancellationReasons":[null]}`)),
			"action.members.CancellationReasons.0", "is ", true,
		},
		{
			"union with two members", errorRule("r", transactWriteItems, tce, members(t, `{"CancellationReasons":[{"Code":"ConditionalCheckFailed","Item":{"pk":{"S":"a","N":"1"}}}]}`)),
			"action.members.CancellationReasons.0.Item.pk", "is ", true,
		},
		{
			"bad base64", errorRule("r", transactWriteItems, tce, members(t, `{"CancellationReasons":[{"Code":"ConditionalCheckFailed","Item":{"pk":{"B":"!!!"}}}]}`)),
			"action.members.CancellationReasons.0.Item.pk.B", "is ", true,
		},
		{
			"no reasons", errorRule("r", transactWriteItems, tce, nil),
			"action.members", `com.amazonaws.dynamodb#TransactionCanceledException always carries CancellationReasons on AWS, one per item of the transaction in item order; ` +
				`set them in action.members, such as {"CancellationReasons": [{"Code": "TransactionConflict", "Message": "Transaction is ongoing for the item."}]}`, false,
		},
		{
			"message without reasons", errorRule("r", transactGetItems, tce, members(t, `{"Message":"m"}`)),
			"action.members", `com.amazonaws.dynamodb#TransactionCanceledException always carries CancellationReasons on AWS, one per item of the transaction in item order; ` +
				`set them in action.members, such as {"CancellationReasons": [{"Code": "TransactionConflict", "Message": "Transaction is ongoing for the item."}]}`, false,
		},
		{
			// smithy-protocol-tests.md 14.12; CancellationReasonList holds 1
			// to 100 (12.1).
			"zero reasons", errorRule("r", transactWriteItems, tce, members(t, `{"CancellationReasons":[]}`)),
			"action.members.CancellationReasons", "has 0 entries; com.amazonaws.dynamodb#CancellationReasonList holds 1 to 100; give at least one reason", false,
		},
		{
			"101 reasons", errorRule("r", transactWriteItems, tce, members(t, `{"CancellationReasons":[`+reasons101+`]}`)),
			"action.members.CancellationReasons", "has 101 entries; com.amazonaws.dynamodb#CancellationReasonList holds 1 to 100; give at most 100 reasons, one per item of the transaction", false,
		},
		{
			"reason without a code", errorRule("r", transactWriteItems, tce, members(t, `{"CancellationReasons":[`+conflictReason+`,{"Message":"m"}]}`)),
			"action.members.CancellationReasons.1", "has no Code; every cancellation reason carries one, None for an item that did not fail", false,
		},
		{
			"every code None", errorRule("r", executeTransaction, tce, members(t, `{"CancellationReasons":[{"Code":"None"},{"Code":"None"}]}`)),
			"action.members.CancellationReasons", "names no failing item, since every Code is None; AWS cancels a transaction only for an item that fails, " +
				"so give one reason a Code such as TransactionConflict or ConditionalCheckFailed", false,
		},
		{
			// AWS spells the key Message when Item is present
			// (testdata/parity/dynamodb/R04/05-put-ccf-all-old.json).
			"conditional check with Item", errorRule("r", putItem, "ConditionalCheckFailedException", members(t, `{"Item":{"pk":{"S":"a"}}}`)),
			"action.members.Item", "com.amazonaws.dynamodb#ConditionalCheckFailedException carries Item only under ReturnValuesOnConditionCheckFailure ALL_OLD, " +
				"when AWS also spells its message key Message (testdata/parity/dynamodb/R04/05-put-ccf-all-old.json), which an injected error does not do; leave Item out", false,
		},
		{
			"after commit",
			fault.Rule{
				Match: fault.Match{Operations: []ref.OperationID{transactWriteItems}}, Phase: fault.Request,
				Action: fault.AfterCommit{Shape: tce}, Trigger: fault.Always{},
			},
			"action.shape", "com.amazonaws.dynamodb#TransactionCanceledException means the transaction changed nothing, so AWS never sends it after a commit; " +
				"inject it with an error action whose members set CancellationReasons", false,
		},
	} {
		_, err := generatedStore(t, 1).Add(c.rule)
		switch {
		case err == nil:
			t.Errorf("%s: Add accepted the rule; want a refusal on %s", c.name, c.field)
		case err.Field != c.field || len(err.Known) != 0:
			t.Errorf("%s: refused on %s with known %v (%s); want %s and no known shapes", c.name, err.Field, err.Known, err.Reason, c.field)
		case c.prefix && !strings.HasPrefix(err.Reason, c.reason):
			t.Errorf("%s: reason %q, want it to start %q", c.name, err.Reason, c.reason)
		case !c.prefix && err.Reason != c.reason:
			t.Errorf("%s: reason\n %q\nwant\n %q", c.name, err.Reason, c.reason)
		}
	}
}

// The store keeps its own copy of the members: changing the caller's value
// after Add, or a listed rule's, never reaches the active rule.
func TestMembersAreCopiedDeeply(t *testing.T) {
	const js = `{"CancellationReasons":[` + conflictReason + `]}`
	m := members(t, js)
	s := generatedStore(t, 1)
	if _, err := s.Add(errorRule("tx", transactWriteItems, "TransactionCanceledException", m)); err != nil {
		t.Fatal(err)
	}
	mutate := func(m fault.Members) {
		reasons, ok := m[0].Value.(document.List)
		if !ok {
			t.Fatalf("CancellationReasons is a %T", m[0].Value)
		}
		reason, ok := reasons[0].(document.Map)
		if !ok {
			t.Fatalf("a reason is a %T", reasons[0])
		}
		reason[0].Value = document.String("Mutated")
	}
	listed := func() fault.Members {
		got := s.List()
		if len(got) != 1 {
			t.Fatalf("List = %+v, want one rule", got)
		}
		a, ok := got[0].Rule.Action.(fault.Error)
		if !ok {
			t.Fatalf("action %#v", got[0].Rule.Action)
		}
		return a.Members
	}
	mutate(m)
	if got := listed(); !reflect.DeepEqual(got, members(t, js)) {
		t.Errorf("after changing the added members the rule holds %#v", got)
	}
	mutate(listed())
	if got := listed(); !reflect.DeepEqual(got, members(t, js)) {
		t.Errorf("after changing the listed members the rule holds %#v", got)
	}
	_, v := canceled(t, s.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 1)))
	if got := codesOf(v.CancellationReasons); !reflect.DeepEqual(got, []string{"TransactionConflict"}) {
		t.Errorf("injected codes = %v, want [TransactionConflict]", got)
	}
}

// Export writes version 2; Import reads versions 1 and 2 and refuses any
// other, naming the versions it reads.
func TestExportVersions(t *testing.T) {
	s := generatedStore(t, 1)
	if _, err := s.Add(throttleSend(fault.Always{})); err != nil {
		t.Fatal(err)
	}
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != 2 {
		t.Errorf("Export = %s, %v; want version 2", data, err)
	}
	v1 := `{"version":1,"rules":[{"rule":{"id":"a","phase":"request","match":{"operations":["sqs.SendMessage"]},"action":{"kind":"throttle"},"trigger":{"kind":"next-n","n":2}},"hits":1}]}`
	if err := generatedStore(t, 1).Import([]byte(v1)); err != nil {
		t.Errorf("Import of a version 1 document: %v", err)
	}
	err = generatedStore(t, 1).Import([]byte(`{"version":3,"rules":[]}`))
	ie, ok := errors.AsType[*fault.ImportError](err)
	if !ok || ie.Index != -1 || ie.Reason != "version 3 is not one of the versions 1 and 2 this binary reads" {
		t.Errorf("Import of version 3 = %v, want the document refused naming versions 1 and 2", err)
	}
}

// A rule with members survives Export then Import: the same rule, the same
// members, the same budget and the same decisions after.
func TestMembersSurviveExportAndImport(t *testing.T) {
	a := generatedStore(t, 1)
	r := errorRule("tx", transactWriteItems, "TransactionCanceledException", members(t, `{"CancellationReasons":[{"Code":"None"},`+conflictReason+`]}`))
	r.Trigger = fault.NextN{N: 3}
	if _, err := a.Add(r); err != nil {
		t.Fatal(err)
	}
	canceled(t, a.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 2)))
	data, err := a.Export()
	if err != nil {
		t.Fatal(err)
	}
	b := generatedStore(t, 1)
	if err := b.Import(data); err != nil {
		t.Fatalf("Import of %s: %v", data, err)
	}
	if x, y := a.List(), b.List(); !reflect.DeepEqual(x, y) {
		t.Fatalf("imported rules %+v, want %+v", y, x)
	}
	for range 2 {
		ea, va := canceled(t, a.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 3)))
		eb, vb := canceled(t, b.Evaluate(fault.BeforeHandler, txCall(transactWriteItems, 3)))
		if ea.Message != eb.Message || !reflect.DeepEqual(va, vb) {
			t.Fatalf("after import: %q %+v, original store: %q %+v", eb.Message, vb, ea.Message, va)
		}
	}
	if got := b.List(); len(got) != 0 {
		t.Errorf("after the budget the imported store holds %+v", got)
	}
}
