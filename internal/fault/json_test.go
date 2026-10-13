package fault_test

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func TestRuleDecodesTheDocumentedForm(t *testing.T) {
	const doc = `{"id": "orders-throttle", "phase": "request",
		"match": {"operations": ["sqs.SendMessage"], "resource": "arn:aws:sqs:*:*:orders-*", "origin": "internal"},
		"action": {"kind": "throttle"},
		"trigger": {"kind": "next-n", "n": 3}}`
	var got fault.Rule
	if err := jsonx.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatal(err)
	}
	want := fault.Rule{
		ID: "orders-throttle",
		Match: fault.Match{
			Operations: []ref.OperationID{sendMessage},
			Resource:   fault.NewARNGlob("arn:aws:sqs:*:*:orders-*"),
			Origin:     fault.InternalOnly,
		},
		Phase:   fault.Request,
		Action:  fault.Throttle{},
		Trigger: fault.NextN{N: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded %+v, want %+v", got, want)
	}
}

func TestRuleDecodingIsStrict(t *testing.T) {
	cases := map[string]string{
		"unknown rule member":    `{"phase":"request","action":{"kind":"throttle"},"trigger":{"kind":"always"},"extra":1}`,
		"unknown action member":  `{"phase":"request","action":{"kind":"throttle","shape":"x"},"trigger":{"kind":"always"}}`,
		"unknown match member":   `{"phase":"request","match":{"service":["sqs"]},"action":{"kind":"throttle"},"trigger":{"kind":"always"}}`,
		"unknown action kind":    `{"phase":"request","action":{"kind":"explode"},"trigger":{"kind":"always"}}`,
		"unknown latency member": `{"phase":"request","action":{"kind":"latency","wall":"1s","jitter":"1s"},"trigger":{"kind":"always"}}`,
		"kind without object":    `{"phase":"request","action":"throttle","trigger":{"kind":"always"}}`,
		"unknown phase":          `{"phase":"later","action":{"kind":"throttle"},"trigger":{"kind":"always"}}`,
		"unknown origin":         `{"phase":"request","match":{"origin":"elsewhere"},"action":{"kind":"throttle"},"trigger":{"kind":"always"}}`,
		"operation without dot":  `{"phase":"request","match":{"operations":["SendMessage"]},"action":{"kind":"throttle"},"trigger":{"kind":"always"}}`,
		"unknown mechanism":      `{"phase":"delivery","match":{"mechanisms":["sns-to-ftp"]},"action":{"kind":"drop-delivery"},"trigger":{"kind":"always"}}`,
		"latency not a duration": `{"phase":"request","action":{"kind":"latency","wall":"soon"},"trigger":{"kind":"always"}}`,
	}
	for name, doc := range cases {
		var r fault.Rule
		if err := jsonx.Unmarshal([]byte(doc), &r); err == nil {
			t.Errorf("%s: decoded without error: %+v", name, r)
		}
	}
	var r fault.Rule
	err := jsonx.Unmarshal([]byte(`{"phase":"request","action":{"kind":"explode"},"trigger":{"kind":"always"}}`), &r)
	if ue, ok := errors.AsType[*jsonx.UnionError](err); !ok || ue.Sum != "fault.Action" || !strings.Contains(err.Error(), "drop-delivery") {
		t.Errorf("unknown kind error = %v, want a UnionError listing the known kinds", err)
	}
}

// A rule that does not parse is reported in the rule's own terms: the
// member at fault in dotted form and what it should be, never the
// decoder's Go type names.
func TestRuleParseErrorsNameTheMember(t *testing.T) {
	cases := []struct{ doc, path, reason string }{
		{`{"phase":"request","match":{"operations":"sqs.SendMessage"},"action":{"kind":"throttle"},"trigger":{"kind":"always"}}`, "match.operations", "is a JSON string; expected an array"},
		{`"throttle"`, "", "is a JSON string; expected an object"},
		{`{"phase":"later","action":{"kind":"throttle"},"trigger":{"kind":"always"}}`, "phase", "is invalid: "},
		{`{"phase":"request","extra":1,"action":{"kind":"throttle"},"trigger":{"kind":"always"}}`, "extra", "is not a member a rule has"},
		{`{"phase":`, "", "is not valid JSON"},
	}
	for _, c := range cases {
		var r fault.Rule
		err := r.UnmarshalJSON([]byte(c.doc))
		pe, ok := errors.AsType[*fault.RuleParseError](err)
		if !ok || pe.Path != c.path || !strings.HasPrefix(pe.Reason, c.reason) || strings.Contains(err.Error(), "Go ") || strings.Contains(err.Error(), "ruleJSON") {
			t.Errorf("UnmarshalJSON(%s) = %v; want a RuleParseError at %q that %s, naming no Go type", c.doc, err, c.path, c.reason)
		}
	}
}

func TestAMissingTriggerIsReportedByAdd(t *testing.T) {
	var r fault.Rule
	if err := jsonx.Unmarshal([]byte(`{"phase":"request","match":{"services":["sqs"]},"action":{"kind":"throttle"}}`), &r); err != nil {
		t.Fatal(err)
	}
	if _, err := newStore(1).Add(r); err == nil || err.Field != "trigger" {
		t.Errorf("Add error = %v, want one on trigger", err)
	}
}

func drawShape(t *rapid.T) smithy.ShapeID {
	return smithy.ShapeID("com.amazonaws.sqs#" + rapid.StringMatching(`[A-Z][A-Za-z]{0,12}`).Draw(t, "shape"))
}

func drawAction(t *rapid.T) fault.Action {
	switch rapid.IntRange(0, 5).Draw(t, "action") {
	case 0:
		return fault.Error{Shape: drawShape(t), Message: rapid.String().Draw(t, "message")}
	case 1:
		return fault.Throttle{}
	case 2:
		return fault.Latency{Wall: time.Duration(rapid.Int64Range(1, int64(time.Hour)).Draw(t, "wall"))}
	case 3:
		return fault.AfterCommit{Shape: drawShape(t)}
	case 4:
		return fault.StatusOKError{Shape: drawShape(t)}
	default:
		return fault.DropDelivery{}
	}
}

func drawTrigger(t *rapid.T) fault.Trigger {
	switch rapid.IntRange(0, 2).Draw(t, "trigger") {
	case 0:
		return fault.Always{}
	case 1:
		return fault.NextN{N: rapid.IntRange(1, 1000).Draw(t, "n")}
	default:
		return fault.Probability{P: rapid.Float64Range(0, 1).Draw(t, "p")}
	}
}

// nilIfEmpty keeps a drawn empty slice nil, as decoding an omitted member
// leaves it.
func nilIfEmpty[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return s
}

func drawRule(t *rapid.T) fault.Rule {
	op := rapid.Custom(func(t *rapid.T) ref.OperationID {
		return ref.OperationID{
			Service: ref.ServiceID(rapid.StringMatching(`[a-z][a-z0-9-]{0,8}`).Draw(t, "service")),
			Name:    rapid.StringMatching(`[A-Z][A-Za-z]{0,12}`).Draw(t, "name"),
		}
	})
	account := rapid.Custom(func(t *rapid.T) aws.AccountID {
		a, err := aws.ParseAccountID(rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"))
		if err != nil {
			t.Fatal(err)
		}
		return a
	})
	region := rapid.Custom(func(t *rapid.T) aws.Region {
		r, err := aws.KnownRegions().Parse(rapid.SampledFrom([]string{"us-east-1", "eu-west-1", "ap-southeast-2"}).Draw(t, "region"))
		if err != nil {
			t.Fatal(err)
		}
		return r
	})
	return fault.Rule{
		ID: ref.RuleID(rapid.StringMatching(`([A-Za-z0-9._-]{1,20})?`).Draw(t, "id")),
		Match: fault.Match{
			Services:   nilIfEmpty(rapid.SliceOfN(rapid.Custom(func(t *rapid.T) ref.ServiceID { return op.Draw(t, "svc").Service }), 0, 3).Draw(t, "services")),
			Operations: nilIfEmpty(rapid.SliceOfN(op, 0, 3).Draw(t, "operations")),
			Mechanisms: nilIfEmpty(rapid.SliceOfN(rapid.SampledFrom([]delivery.Mechanism{delivery.SNSToSQS, delivery.S3ToLambda, delivery.EventsToSNS}), 0, 3).Draw(t, "mechanisms")),
			Resource:   fault.NewARNGlob(rapid.SampledFrom([]string{"", "*", "arn:aws:sqs:*:*:orders-?"}).Draw(t, "resource")),
			Accounts:   nilIfEmpty(rapid.SliceOfN(account, 0, 2).Draw(t, "accounts")),
			Regions:    nilIfEmpty(rapid.SliceOfN(region, 0, 2).Draw(t, "regions")),
			Origin:     rapid.SampledFrom([]fault.OriginMatch{fault.Any, fault.ExternalOnly, fault.InternalOnly}).Draw(t, "origin"),
		},
		Phase:   rapid.SampledFrom([]fault.Phase{fault.Request, fault.Delivery}).Draw(t, "phase"),
		Action:  drawAction(t),
		Trigger: drawTrigger(t),
	}
}

func TestRuleJSONRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := drawRule(t)
		data, err := jsonx.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var got fault.Rule
		if err := jsonx.Unmarshal(data, &got); err != nil {
			t.Fatalf("decoding %s: %v", data, err)
		}
		if !reflect.DeepEqual(got, r) {
			t.Fatalf("round trip of %s gave %+v, want %+v", data, got, r)
		}
	})
}

func TestExportImportResumesBudgets(t *testing.T) {
	a := newStore(1)
	id := mustAdd(t, a, throttleSend(fault.NextN{N: 3}))
	a.Evaluate(fault.BeforeHandler, call(sendMessage))
	data, err := a.Export()
	if err != nil {
		t.Fatal(err)
	}
	b := newStore(1)
	if err := b.Import(data); err != nil {
		t.Fatal(err)
	}
	if got := b.List(); len(got) != 1 || got[0].Hits != 1 || got[0].Rule.ID != id {
		t.Fatalf("imported List = %+v, want %s with one hit", got, id)
	}
	for hit := 2; hit <= 3; hit++ {
		if f, ok := b.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Fail); !ok || f.Hit != hit || f.Of != 3 {
			t.Errorf("call %d after import: %#v, want hit %d of 3", hit, f, hit)
		}
	}
	if got := b.List(); len(got) != 0 {
		t.Errorf("List after the budget = %+v, want empty", got)
	}
}

func TestExportImportResumesProbabilityStreams(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Uint64().Draw(t, "seed")
		before := rapid.IntRange(0, 50).Draw(t, "before")
		a := newStore(seed)
		mustAdd(t, a, probabilityRule("send", sendMessage, 0.5))
		mustAdd(t, a, probabilityRule("receive", receiveMessage, 0.3))
		hits(a, sendMessage, before)
		hits(a, receiveMessage, before/2)
		data, err := a.Export()
		if err != nil {
			t.Fatal(err)
		}
		b := newStore(seed)
		if err := b.Import(data); err != nil {
			t.Fatal(err)
		}
		again, err := b.Export()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, again) {
			t.Fatalf("Export after Import differs:\n%s\n%s", data, again)
		}
		for _, op := range []ref.OperationID{sendMessage, receiveMessage} {
			if x, y := hits(a, op, 40), hits(b, op, 40); fmt.Sprint(x) != fmt.Sprint(y) {
				t.Fatalf("%s after import: %v, original store: %v", op, y, x)
			}
		}
	})
}

func TestExportRefusesPositionsImportWouldRefuse(t *testing.T) {
	// Save and load agree on the limit: Export refuses stream positions whose
	// total Import would refuse, rather than write a snapshot that cannot be
	// loaded. An unseeded store imports positions without replaying them.
	rule := func(id, op string, pos uint64) string {
		return fmt.Sprintf(`{"rule":{"id":%q,"phase":"request","match":{"operations":[%q]},"action":{"kind":"throttle"},"trigger":{"kind":"probability","p":0.5}},"position":%d}`, id, op, pos)
	}
	doc := `{"version":1,"rules":[` + rule("send", "sqs.SendMessage", ids.MaxPosition-1) + "," + rule("receive", "sqs.ReceiveMessage", 1) + `]}`
	s := fault.New(nil, ids.NewRandom(), testCatalogue{})
	if err := s.Import([]byte(doc)); err != nil {
		t.Fatalf("Import of positions totalling the maximum: %v", err)
	}
	atLimit, err := s.Export()
	if err != nil {
		t.Fatalf("Export of positions totalling the maximum: %v", err)
	}
	if err := fault.New(nil, ids.NewRandom(), testCatalogue{}).Import(atLimit); err != nil {
		t.Fatalf("Import of the exported document: %v", err)
	}

	s.Evaluate(fault.BeforeHandler, call(receiveMessage))
	_, err = s.Export()
	if ee, ok := errors.AsType[*fault.ExportError](err); !ok || ee.Rule != "receive" || ee.Position != 2 {
		t.Fatalf("Export past the maximum = %v, want *ExportError for rule receive at position 2", err)
	}
	if got := s.List(); len(got) != 2 {
		t.Errorf("rules after a refused export = %+v, want both still active", got)
	}
}

func TestImportRefusesBadDocumentsAndKeepsTheRules(t *testing.T) {
	spent := `{"version":1,"rules":[{"rule":{"id":"a","phase":"request","match":{"operations":["sqs.SendMessage"]},"action":{"kind":"throttle"},"trigger":{"kind":"next-n","n":2}},"hits":2}]}`
	invalid := `{"version":1,"rules":[{"rule":{"id":"a","phase":"request","match":{"operations":["sqs.SendMessage"]},"action":{"kind":"error","shape":"Nope"},"trigger":{"kind":"always"}}}]}`
	cases := map[string]string{
		"other version":     `{"version":2,"rules":[]}`,
		"unknown member":    `{"version":1,"rules":[],"seed":1}`,
		"spent budget":      spent,
		"invalid rule":      invalid,
		"missing id":        `{"version":1,"rules":[{"rule":{"phase":"request","action":{"kind":"latency","wall":"1s"},"trigger":{"kind":"always"}}}]}`,
		"duplicate id":      `{"version":1,"rules":[{"rule":{"id":"a","phase":"request","action":{"kind":"latency","wall":"1s"},"trigger":{"kind":"always"}}},{"rule":{"id":"a","phase":"request","action":{"kind":"latency","wall":"1s"},"trigger":{"kind":"always"}}}]}`,
		"stray position":    `{"version":1,"rules":[{"rule":{"id":"a","phase":"request","action":{"kind":"latency","wall":"1s"},"trigger":{"kind":"always"}},"position":3}]}`,
		"negative hits":     `{"version":1,"rules":[{"rule":{"id":"a","phase":"request","action":{"kind":"latency","wall":"1s"},"trigger":{"kind":"always"}},"hits":-1}]}`,
		"position too high": `{"version":1,"rules":[{"rule":{"id":"a","phase":"request","match":{"operations":["sqs.SendMessage"]},"action":{"kind":"throttle"},"trigger":{"kind":"probability","p":0.5}},"position":2000000000}]}`,
		"total position too high": `{"version":1,"rules":[` +
			`{"rule":{"id":"a","phase":"request","match":{"operations":["sqs.SendMessage"]},"action":{"kind":"throttle"},"trigger":{"kind":"probability","p":0.5}},"position":1000000000},` +
			`{"rule":{"id":"b","phase":"request","match":{"operations":["sqs.SendMessage"]},"action":{"kind":"throttle"},"trigger":{"kind":"probability","p":0.5}},"position":1000000000}]}`,
	}
	for name, doc := range cases {
		s := newStore(1)
		keep := mustAdd(t, s, throttleSend(fault.Always{}))
		err := s.Import([]byte(doc))
		if ie, ok := errors.AsType[*fault.ImportError](err); !ok || !strings.Contains(ie.Error(), "the active rules are unchanged") {
			t.Errorf("%s: Import error = %v, want *ImportError", name, err)
		}
		if got := s.List(); len(got) != 1 || got[0].Rule.ID != keep {
			t.Errorf("%s: rules after a refused import = %+v", name, got)
		}
	}
	err := newStore(1).Import([]byte(invalid))
	if re, ok := errors.AsType[*fault.RuleError](err); !ok || re.Field != "action.shape" {
		t.Errorf("invalid rule error = %v, want a RuleError cause on action.shape", err)
	}
}
