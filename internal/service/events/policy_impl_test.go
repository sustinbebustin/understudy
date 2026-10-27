package events_test

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/events"
)

// sids is the statement Sids of bus's Policy in policy order.
func (h *harness) sids(bus *string) []string {
	h.Helper()
	var out []string
	if doc := h.policyOf(bus); doc != nil {
		for i := range doc.statements {
			out = append(out, doc.statements[i].Sid)
		}
	}
	return out
}

// PutPermission refuses a request before it looks the bus up: a Policy
// with a statement member, a statement form missing StatementId, Action
// or Principal, and a Policy that is not a JSON object whose Statement is
// an object or a list of objects with distinct string Sids. The order and
// texts are understudy's choice (eventbridge-buses.md 4.1; EBB-06 records
// AWS's). A refused call stores nothing.
func TestPutPermissionRequestChecks(t *testing.T) {
	const (
		withStatement = "Parameter(s) Policy cannot be specified together with StatementId, Action, Principal or Condition."
		incomplete    = "Parameter(s) Action, Principal and StatementId must be specified when Policy is not."
		invalid       = "Parameter Policy is not valid."
	)
	doc := `{"Statement":{"Sid":"a","Effect":"Allow","Principal":"*","Action":"events:PutEvents","Resource":"*"}}`
	for _, tt := range []struct {
		name string
		in   eventbridgeapi.PutPermissionInput
		want string
	}{
		{"Policy with Condition", eventbridgeapi.PutPermissionInput{
			Policy:    new(doc),
			Condition: &eventbridgeapi.Condition{Type: "StringEquals", Key: "aws:PrincipalOrgID", Value: "o-1"},
		}, withStatement},
		{"Policy with Action", eventbridgeapi.PutPermissionInput{Policy: new(doc), Action: new("events:PutEvents")}, withStatement},
		{"Policy with StatementId, invalid Policy", eventbridgeapi.PutPermissionInput{Policy: new("{"), StatementId: new("s")}, withStatement},
		{"no Principal", eventbridgeapi.PutPermissionInput{StatementId: new("s"), Action: new("events:PutEvents")}, incomplete},
		{"no StatementId", eventbridgeapi.PutPermissionInput{Action: new("events:PutEvents"), Principal: new("*")}, incomplete},
		{"nothing", eventbridgeapi.PutPermissionInput{}, incomplete},
		{"not JSON", eventbridgeapi.PutPermissionInput{Policy: new("{")}, invalid},
		{"empty", eventbridgeapi.PutPermissionInput{Policy: new("")}, invalid},
		{"a list", eventbridgeapi.PutPermissionInput{Policy: new(`[]`)}, invalid},
		{"no Statement", eventbridgeapi.PutPermissionInput{Policy: new(`{"Version":"2012-10-17"}`)}, invalid},
		{"Statement a string", eventbridgeapi.PutPermissionInput{Policy: new(`{"Statement":"s"}`)}, invalid},
		{"Statement list of a number", eventbridgeapi.PutPermissionInput{Policy: new(`{"Statement":[{"Sid":"a"},1]}`)}, invalid},
		{"two statements with one Sid", eventbridgeapi.PutPermissionInput{Policy: new(`{"Statement":[{"Sid":"a"},{"Sid":"a"}]}`)}, invalid},
		{"Sid a number", eventbridgeapi.PutPermissionInput{Policy: new(`{"Statement":[{"Sid":1}]}`)}, invalid},
		{"trailing text", eventbridgeapi.PutPermissionInput{Policy: new(`{"Statement":[]} x`)}, invalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run(t, func(h *harness) {
				for _, bus := range []*string{nil, new("missing")} {
					in := tt.in
					in.EventBusName = bus
					_, err := call(h, eventbridgeapi.OpPutPermission, &in)
					wantError(t, err, "ValidationException", tt.want)
				}
				if d := h.describeBus(nil); d.Policy != nil {
					t.Errorf("default bus Policy after refused calls = %s, want none", *d.Policy)
				}
				if got := h.svc.Resources(h.Scope); len(got) != 0 {
					t.Errorf("Resources after refused calls = %v, want none: a refused call stores nothing", got)
				}
			})
		})
	}
}

// Statements without a Sid, or with distinct ones, are accepted from a
// Policy document, and a Statement that is one object is one statement
// (the policy grammar allows both; eventbridge-buses.md 4.1).
// DescribeEventBus and ListEventBuses return the document as sent, until a
// statement call rebuilds the policy from its statements in order, compact
// and without the document's other members (eventbridge-buses.md 4.2,
// 10.2.5).
func TestPolicyDocumentIsReturnedVerbatimUntilAStatementCall(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("doc")
		const document = "{\n  \"Version\": \"2012-10-17\",\n  \"Id\": \"kept\",\n" +
			"  \"Statement\": {\"Effect\": \"Allow\", \"Principal\": \"*\", \"Action\": \"events:PutEvents\", \"Resource\": \"*\"}\n}"
		must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{EventBusName: new("doc"), Policy: new(document)})
		if d := h.describeBus(new("doc")); deref(d.Policy) != document {
			t.Errorf("DescribeEventBus Policy = %q, want the document as sent", deref(d.Policy))
		}
		if b := h.listBuses(new("doc")); len(b) != 1 || deref(b[0].Policy) != document {
			t.Errorf("ListEventBuses = %+v, want the document as sent", b)
		}

		h.permit(new("doc"), "added", "*")
		const want = `{"Version":"2012-10-17","Statement":[` +
			`{"Effect":"Allow","Principal":"*","Action":"events:PutEvents","Resource":"*"},` +
			`{"Sid":"added","Effect":"Allow","Principal":"*","Action":"events:PutEvents","Resource":"` + busPrefix + `doc"}]}`
		if d := h.describeBus(new("doc")); deref(d.Policy) != want {
			t.Errorf("Policy after PutPermission = %s, want %s", deref(d.Policy), want)
		}
		if b := h.listBuses(new("doc")); len(b) != 1 || deref(b[0].Policy) != want {
			t.Errorf("ListEventBuses = %+v, want the rebuilt policy", b)
		}
	})
}

// A repeated StatementId replaces its statement where it stands, a new one
// is appended, and a removal keeps the others in order (eventbridge-buses.md
// 10.2.5: a list in insertion order, upserted by Sid).
func TestPolicyStatementsKeepTheirOrder(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("o")
		for _, sid := range []string{"a", "b", "c"} {
			h.permit(new("o"), sid, "111111111111")
		}
		h.permit(new("o"), "a", "*")
		if got := h.sids(new("o")); !slices.Equal(got, []string{"a", "b", "c"}) {
			t.Errorf("Sids after replacing a = %v, want a, b, c", got)
		}
		if s := bySid(h.policyOf(new("o")))["a"]; compact(h, s.Principal) != `"*"` {
			t.Errorf("a's Principal = %s, want the replacement's *", s.Principal)
		}
		must(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("o"), StatementId: new("b")})
		if got := h.sids(new("o")); !slices.Equal(got, []string{"a", "c"}) {
			t.Errorf("Sids after removing b = %v, want a, c", got)
		}
	})
}

// A policy of exactly 10,240 characters is stored; one more character is
// PolicyLengthExceededException, with understudy's text (the eb-quota.html
// limit, eventbridge-buses.md 4.1; the text is the PutPermission page's
// sentence, EBB-06), from either form, and the policy stays as it was.
func TestPutPermissionPolicyLengthBoundary(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("edge")
		sized := func(n int) string {
			head := `{"Version":"2012-10-17","Statement":[{"Sid":"pad","Effect":"Allow","Principal":"*","Action":"events:PutEvents","Resource":"`
			tail := `"}]}`
			return head + strings.Repeat("r", n-len(head)-len(tail)) + tail
		}
		must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{EventBusName: new("edge"), Policy: new(sized(10240))})
		if d := h.describeBus(new("edge")); len(deref(d.Policy)) != 10240 {
			t.Fatalf("Policy holds %d characters, want the 10240 sent", len(deref(d.Policy)))
		}
		_, err := call(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{EventBusName: new("edge"), Policy: new(sized(10241))})
		wantError(t, err, "PolicyLengthExceededException", "The permission policy on the event bus cannot exceed 10 KB in size.")
		_, err = call(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{
			EventBusName: new("edge"), StatementId: new("more"), Action: new("events:PutEvents"), Principal: new("*"),
		})
		wantError(t, err, "PolicyLengthExceededException", "The permission policy on the event bus cannot exceed 10 KB in size.")
		if d := h.describeBus(new("edge")); deref(d.Policy) != sized(10240) {
			t.Errorf("Policy after the refused calls holds %d characters, want the stored 10240", len(deref(d.Policy)))
		}
	})
}

// RemoveAllPermissions ignores a StatementId; on a bus without a policy it
// is not found with understudy's text (events.bus-policy.remove-all-without-policy;
// eventbridge-buses.md 4.3, EBB-06). Without RemoveAllPermissions, no
// StatementId is not found as an unknown one is (eventbridge-buses.md 4.3).
// A missing bus is not found (eventbridge-buses.md 4.1, third-party-recorded
// test_events.snapshot.json:L1152-L1165).
func TestRemovePermissionForms(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("r")
		h.permit(new("r"), "s1", "111111111111")
		_, err := call(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("r")})
		wantError(t, err, "ResourceNotFoundException", "Statement with the provided id does not exist.")
		must(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("r"), StatementId: new("nope"), RemoveAllPermissions: true})
		if d := h.describeBus(new("r")); d.Policy != nil {
			t.Errorf("Policy after RemoveAllPermissions = %s, want none", *d.Policy)
		}
		for bus, name := range map[string]*string{"r": new("r"), "default": nil} {
			_, err := call(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: name, RemoveAllPermissions: true})
			wantError(t, err, "ResourceNotFoundException", "Event bus "+bus+" does not have a policy.")
		}
		_, err = call(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("missing"), StatementId: new("s1")})
		wantError(t, err, "ResourceNotFoundException", "Event bus missing does not exist.")
	})
}

// PutPermission and RemovePermission move a bus's LastModifiedTime and keep
// its CreationTime; a refused call moves neither. On the default bus the
// first PutPermission stores it created at the scope's first use
// (events.default-bus.creation-time; eventbridge-buses.md 10.2.2).
func TestPermissionCallsMoveLastModifiedTime(t *testing.T) {
	run(t, func(h *harness) {
		created := h.Clock.Now().Wire()
		h.createBus("m")
		times := func(bus *string) (time.Time, time.Time) {
			t.Helper()
			d := h.describeBus(bus)
			return *d.CreationTime, *d.LastModifiedTime
		}
		h.advance(time.Minute)
		permitted := h.Clock.Now().Wire()
		h.permit(new("m"), "s1", "*")
		if c, m := times(new("m")); !c.Equal(created) || !m.Equal(permitted) {
			t.Errorf("after PutPermission: times %v, %v; want %v, %v", c, m, created, permitted)
		}
		h.advance(time.Minute)
		_, err := call(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("m"), StatementId: new("nope")})
		if err == nil {
			t.Fatal("removing an unknown statement succeeded")
		}
		if _, m := times(new("m")); !m.Equal(permitted) {
			t.Errorf("after a refused RemovePermission: LastModifiedTime %v, want %v", m, permitted)
		}
		removedAt := h.Clock.Now().Wire()
		must(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("m"), StatementId: new("s1")})
		if c, m := times(new("m")); !c.Equal(created) || !m.Equal(removedAt) {
			t.Errorf("after RemovePermission: times %v, %v; want %v, %v", c, m, created, removedAt)
		}

		h.advance(time.Minute)
		defaultAt := h.Clock.Now().Wire()
		h.permit(nil, "d", "*")
		if c, m := times(nil); !c.Equal(created) || !m.Equal(defaultAt) {
			t.Errorf("default bus after PutPermission: times %v, %v; want the scope's first use %v and %v", c, m, created, defaultAt)
		}
	})
}

// A Condition's members are JSON-escaped in the generated statement, so
// the Policy stays a document Terraform can parse (eventbridge-buses.md
// 10.6.18) whatever the Value holds; the model constrains none of Type,
// Key and Value.
func TestPutPermissionConditionIsEscaped(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("esc")
		value := `o-"quoted"\back` + " "
		must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{
			EventBusName: new("esc"), StatementId: new("e"), Action: new("events:PutEvents"), Principal: new("*"),
			Condition: &eventbridgeapi.Condition{Type: "StringEquals", Key: "aws:PrincipalOrgID", Value: value},
		})
		s := bySid(h.policyOf(new("esc")))["e"]
		var cond map[string]map[string]string
		if err := json.Unmarshal(s.Condition, &cond); err != nil || cond["StringEquals"]["aws:PrincipalOrgID"] != value {
			t.Errorf("Condition = %s (%v), want the value %q back", s.Condition, err, value)
		}
	})
}

// Deleting a bus deletes its policy: a bus recreated under the name has
// none (eventbridge-buses.md 3.5, 10.2.6).
func TestDeletedBusLosesItsPolicy(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("gone")
		h.permit(new("gone"), "s1", "*")
		must(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "gone"})
		h.createBus("gone")
		if d := h.describeBus(new("gone")); d.Policy != nil {
			t.Errorf("recreated bus Policy = %s, want none", *d.Policy)
		}
	})
}

// The provider answers for another served account's bus by its ARN, gives
// the default bus's policy once one is put, and reports a policy policydoc
// refuses as none, as SQS does, while the bus still exists
// (ARCHITECTURE.md 16).
func TestBusResourcePolicyAcrossScopesAndUnparsedPolicies(t *testing.T) {
	run(t, func(h *harness) {
		h.permit(nil, "d", "111111111111")
		if doc, ok := h.svc.ResourcePolicy(h.mustARN(busPrefix + "default")); !ok || doc == nil || len(doc.Statements) != 1 {
			t.Errorf("ResourcePolicy(default) = %+v, %t; want the one-statement policy", doc, ok)
		}
		h.createBus("odd")
		must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{
			EventBusName: new("odd"), Policy: new(`{"Bogus":1,"Statement":[{"Sid":"x"}]}`),
		})
		if doc, ok := h.svc.ResourcePolicy(h.mustARN(busPrefix + "odd")); !ok || doc != nil {
			t.Errorf("ResourcePolicy(odd) = %+v, %t; want a bus with no policy policydoc reads", doc, ok)
		}

		other := mustAccount(h, "111111111111")
		h.Scope.Account = other
		h.createBus("theirs")
		h.permit(new("theirs"), "s", "*")
		arn := "arn:aws:events:us-east-1:111111111111:event-bus/theirs"
		if doc, ok := h.svc.ResourcePolicy(h.mustARN(arn)); !ok || doc == nil || len(doc.Statements) != 1 {
			t.Errorf("ResourcePolicy(%s) = %+v, %t; want its one-statement policy", arn, doc, ok)
		}
		for _, bad := range []string{"arn:aws:events:us-east-1:111111111111:event-bus/", "arn:aws:events:us-east-1:111111111111:event-bus/a/b"} {
			if doc, ok := h.svc.ResourcePolicy(h.mustARN(bad)); ok || doc != nil {
				t.Errorf("ResourcePolicy(%s) = %+v, %t; want no bus", bad, doc, ok)
			}
		}
	})
}

// IAM evaluation reads a bus's owner and policy: none until PutPermission
// stores one, the stored document once it does, and a document policydoc
// refuses as unparsed rather than as no policy
// (iam-enforcement-surfaces.md 12.10). A bus that does not exist has none.
func TestBusStoredPolicy(t *testing.T) {
	run(t, func(h *harness) {
		def := h.mustARN(busPrefix + "default")
		if got, ok := h.svc.StoredPolicy(def); !ok || got.Owner != h.Scope.Account || got.Policy != (authz.NoPolicy{}) {
			t.Errorf("StoredPolicy(default) before PutPermission = %+v, %t; want the account's bus with no policy", got, ok)
		}
		h.permit(nil, "d", "111111111111")
		if got, ok := h.svc.StoredPolicy(def); !ok {
			t.Errorf("StoredPolicy(default) = %+v, %t; want the bus", got, ok)
		} else if p, parsed := got.Policy.(authz.Parsed); !parsed || len(p.Document.Statements) != 1 {
			t.Errorf("StoredPolicy(default).Policy = %+v; want the one-statement policy", got.Policy)
		}
		h.createBus("odd")
		must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{
			EventBusName: new("odd"), Policy: new(`{"Bogus":1,"Statement":[{"Sid":"x"}]}`),
		})
		if got, ok := h.svc.StoredPolicy(h.mustARN(busPrefix + "odd")); !ok {
			t.Errorf("StoredPolicy(odd) = %+v, %t; want the bus", got, ok)
		} else if _, unparsed := got.Policy.(authz.Unparsed); !unparsed {
			t.Errorf("StoredPolicy(odd).Policy = %+v; want Unparsed", got.Policy)
		}
		if got, ok := h.svc.StoredPolicy(h.mustARN(busPrefix + "absent")); ok {
			t.Errorf("StoredPolicy(absent) = %+v, true; want no bus", got)
		}
	})
}

// A snapshot keeps a Policy document verbatim, and one that fails to load
// for a policy no call could store leaves the policy as it was
// (eventbridge-buses.md 10.2.6).
func TestSnapshotKeepsBusPolicies(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("kept")
		const document = "{ \"Statement\" : [ {\"Sid\":\"s1\"} , {\"Sid\":\"s2\"} ] }"
		must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{EventBusName: new("kept"), Policy: new(document)})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import = %v", err)
		}
		if d := h.describeBus(new("kept")); deref(d.Policy) != document {
			t.Errorf("Policy after the round trip = %q, want %q", deref(d.Policy), document)
		}

		err = h.svc.Import(applyEdits(h, sections, []edit{{regexp.MustCompile(`"sid":"s2"`), `"sid":"s1"`}}))
		if be, ok := errors.AsType[*events.StoredBusError](err); !ok || be.Bus != "kept" {
			t.Fatalf("Import of two statements with one Sid = %v, want a *events.StoredBusError for kept", err)
		}
		if got := h.sids(new("kept")); !slices.Equal(got, []string{"s1", "s2"}) {
			t.Errorf("Sids after the refused import = %v, want s1, s2", got)
		}
	})
}
