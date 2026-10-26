package events_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// bubbleTB hides *testing.T from rapid, which would otherwise call
// T.Deadline, forbidden inside a synctest bubble.
type bubbleTB struct{ *testing.T }

// policyDoc is a bus Policy string, decoded with its member order.
type policyDoc struct {
	keys       []string
	version    string
	statements []policyStatement
}

// policyStatement is one statement of a bus policy, with its member order.
type policyStatement struct {
	keys      []string
	Sid       string          `json:"Sid"`
	Effect    string          `json:"Effect"`
	Principal json.RawMessage `json:"Principal"`
	Action    json.RawMessage `json:"Action"`
	Resource  json.RawMessage `json:"Resource"`
	Condition json.RawMessage `json:"Condition"`
}

// memberOrder returns the member names of the JSON object data in order.
func memberOrder(t interface{ Fatalf(string, ...any) }, data []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("%s is not a JSON object", data)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("%s: member name %v", data, tok)
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
	}
	return keys
}

func compact(t interface{ Fatalf(string, ...any) }, raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return b.String()
}

// parsePolicy decodes a bus Policy string: a JSON string holding a policy
// document (eventbridge-buses.md 4.2).
func parsePolicy(t interface{ Fatalf(string, ...any) }, text string) policyDoc {
	var raw struct {
		Version   string            `json:"Version"`
		Statement []json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		t.Fatalf("Policy %s: %v", text, err)
	}
	doc := policyDoc{keys: memberOrder(t, []byte(text)), version: raw.Version}
	for _, s := range raw.Statement {
		var st policyStatement
		if err := json.Unmarshal(s, &st); err != nil {
			t.Fatalf("statement %s: %v", s, err)
		}
		st.keys = memberOrder(t, s)
		doc.statements = append(doc.statements, st)
	}
	return doc
}

// policyOf is the Policy DescribeEventBus answers for bus, nil when it has
// none.
func (h *harness) policyOf(bus *string) *policyDoc {
	h.Helper()
	d := h.describeBus(bus)
	if d.Policy == nil {
		return nil
	}
	doc := parsePolicy(h, *d.Policy)
	return &doc
}

func (h *harness) permit(bus *string, sid, principal string) {
	h.Helper()
	must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{
		EventBusName: bus, StatementId: new(sid), Action: new("events:PutEvents"), Principal: new(principal),
	})
}

// bySid indexes statements by Sid.
func bySid(doc *policyDoc) map[string]policyStatement {
	out := map[string]policyStatement{}
	if doc != nil {
		for i := range doc.statements {
			out[doc.statements[i].Sid] = doc.statements[i]
		}
	}
	return out
}

// PutPermission with a statement id adds a statement in the recorded form:
// members Sid, Effect, Principal, Action, Resource in that order, Effect
// Allow, a 12-digit Principal as {"AWS": "arn:aws:iam::<account>:root"} and
// "*" as the string "*", Action and Resource as strings, Resource the bus
// ARN, under a document of Version 2012-10-17 with no Id
// (eventbridge-buses.md 4.2, third-party-recorded
// localstack@8b9a79f tests/aws/services/events/test_events.snapshot.json:L897-L933, L974-L980).
// A repeated statement id leaves one statement under it (L939-L987) holding
// the new content (TF-ACC permission_test.go L79-L90). The default bus
// takes a policy too (L1020-L1150) and then reports its times (L1029-L1033).
func TestPutPermissionStatements(t *testing.T) {
	for _, bus := range []string{"custom", "default"} {
		t.Run(bus, func(t *testing.T) {
			run(t, func(h *harness) {
				name := new(bus)
				if bus == "default" {
					name = nil
				} else {
					h.createBus(bus)
				}
				h.permit(name, "s1", "111111111111")
				h.permit(name, "s2", "*")
				h.permit(name, "s3", "222222222222")
				h.permit(name, "s3", "*")
				doc := h.policyOf(name)
				if doc == nil {
					t.Fatal("DescribeEventBus answered no Policy after PutPermission")
				}
				if !slices.Equal(doc.keys, []string{"Version", "Statement"}) || doc.version != "2012-10-17" {
					t.Errorf("Policy members %v, Version %q; want Version 2012-10-17 then Statement, no Id", doc.keys, doc.version)
				}
				got := bySid(doc)
				if len(doc.statements) != 3 || len(got) != 3 {
					t.Fatalf("statements = %+v, want s1, s2 and s3 once each", doc.statements)
				}
				principals := map[string]string{"s1": `{"AWS":"arn:aws:iam::111111111111:root"}`, "s2": `"*"`, "s3": `"*"`}
				for sid, principal := range principals {
					s := got[sid]
					if !slices.Equal(s.keys, []string{"Sid", "Effect", "Principal", "Action", "Resource"}) {
						t.Errorf("%s members = %v, want Sid, Effect, Principal, Action, Resource", sid, s.keys)
					}
					if s.Effect != "Allow" || compact(h, s.Principal) != principal || compact(h, s.Action) != `"events:PutEvents"` ||
						compact(h, s.Resource) != `"`+busPrefix+bus+`"` {
						t.Errorf("%s = %+v (principal %s, action %s, resource %s), want Allow %s events:PutEvents on %s",
							sid, s, s.Principal, s.Action, s.Resource, principal, busPrefix+bus)
					}
				}
				d := h.describeBus(name)
				if d.CreationTime == nil || d.LastModifiedTime == nil || deref(d.Arn) != busPrefix+bus || deref(d.Name) != bus {
					t.Errorf("DescribeEventBus = %+v, want its name, ARN and times beside the Policy", d)
				}
			})
		})
	}
}

// A Condition renders in the IAM form Terraform parses back,
// {"StringEquals": {"aws:PrincipalOrgID": "<org>"}}, beside string Action
// and Resource members (eventbridge-buses.md 4.2, 8.4, 10.6.18).
func TestPutPermissionCondition(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("org")
		must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{
			EventBusName: new("org"), StatementId: new("org-wide"), Action: new("events:PutEvents"), Principal: new("*"),
			Condition: &eventbridgeapi.Condition{Type: "StringEquals", Key: "aws:PrincipalOrgID", Value: "o-1234567890"},
		})
		s, ok := bySid(h.policyOf(new("org")))["org-wide"]
		if !ok {
			t.Fatal("no org-wide statement")
		}
		if compact(h, s.Condition) != `{"StringEquals":{"aws:PrincipalOrgID":"o-1234567890"}}` || compact(h, s.Action) != `"events:PutEvents"` ||
			compact(h, s.Resource) != `"`+busPrefix+`org"` || compact(h, s.Principal) != `"*"` {
			t.Errorf("statement = %+v (condition %s)", s, s.Condition)
		}
	})
}

// PutPermission with a Policy replaces the whole policy: afterwards only
// the document's statements remain, stored as given, whatever action,
// principal and resource they name (eventbridge-buses.md 4.1,
// third-party-recorded test_events.snapshot.json:L994-L1012). A Policy sent
// with a statement member is refused, since the Policy is sent "instead of
// using the StatementId, Action, Principal, or Condition parameters"
// (aws-doc API_PutPermission; the error text is unrecorded, EBB-06), and
// the policy is left as it was.
func TestPutPermissionPolicyReplaces(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("p")
		h.permit(new("p"), "s1", "111111111111")
		const document = `{"Version":"2012-10-17","Statement":[{"Sid":"listing","Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"events:ListRules","Resource":"*"}]}`
		must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{EventBusName: new("p"), Policy: new(document)})
		doc := h.policyOf(new("p"))
		if doc == nil || len(doc.statements) != 1 {
			t.Fatalf("Policy = %+v, want the document's one statement", doc)
		}
		if s := doc.statements[0]; s.Sid != "listing" || s.Effect != "Allow" || compact(h, s.Principal) != `{"Service":"events.amazonaws.com"}` ||
			compact(h, s.Action) != `"events:ListRules"` || compact(h, s.Resource) != `"*"` {
			t.Errorf("statement = %+v, want the document's statement as given", s)
		}

		_, err := call(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{
			EventBusName: new("p"), Policy: new(document), StatementId: new("s9"), Action: new("events:PutEvents"), Principal: new("*"),
		})
		if !refused(err) {
			t.Errorf("PutPermission with Policy and StatementId = %v, want a client error", err)
		}
		if got := slices.Collect(maps.Keys(bySid(h.policyOf(new("p"))))); !slices.Equal(got, []string{"listing"}) {
			t.Errorf("statements after the refused call = %v, want listing alone", got)
		}
	})
}

// PutPermission on a bus that does not exist is not found
// (ResourceNotFoundException "Event bus <name> does not exist.",
// eventbridge-buses.md 4.1, third-party-recorded
// test_events.snapshot.json:L1152-L1165).
func TestPutPermissionMissingBus(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{
			EventBusName: new("missing"), StatementId: new("s"), Action: new("events:PutEvents"), Principal: new("111111111111"),
		})
		wantError(t, err, "ResourceNotFoundException", "Event bus missing does not exist.")
	})
}

// A bus policy holds at most 10,240 characters (aws-doc
// https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-quota.html,
// eventbridge-buses.md 7, 9): a PutPermission that would exceed it answers
// PolicyLengthExceededException, which the model declares for
// PutPermission (eventbridge-buses.md 2.3; its text is unrecorded, EBB-06),
// and changes nothing.
func TestPutPermissionPolicyLength(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("long")
		h.permit(new("long"), "keep", "111111111111")
		long := `{"Version":"2012-10-17","Statement":[{"Sid":"big","Effect":"Allow","Principal":"*","Action":"events:PutEvents","Resource":"` +
			busPrefix + "long" + `","Condition":{"StringEquals":{"aws:PrincipalOrgID":"` + strings.Repeat("o", 10240) + `"}}}]}`
		_, err := call(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{EventBusName: new("long"), Policy: new(long)})
		if err == nil || err.ID.JSONType != "PolicyLengthExceededException" {
			t.Errorf("PutPermission of an 11 KB policy = %v, want PolicyLengthExceededException", err)
		}
		if got := slices.Collect(maps.Keys(bySid(h.policyOf(new("long"))))); !slices.Equal(got, []string{"keep"}) {
			t.Errorf("statements after the refused call = %v, want keep alone", got)
		}
	})
}

// RemovePermission removes one statement and leaves the rest
// (third-party-recorded test_events.snapshot.json:L1176-L1199);
// RemoveAllPermissions leaves a bus whose DescribeEventBus has no Policy
// (L1206-L1215); an unknown statement id is not found, with or without a
// policy, on the default or a custom bus (ResourceNotFoundException
// "Statement with the provided id does not exist.", L1269-L1328;
// eventbridge-buses.md 4.3).
func TestRemovePermission(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("r")
		for _, bus := range []*string{nil, new("r")} {
			_, err := call(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: bus, StatementId: new("nope")})
			wantError(t, err, "ResourceNotFoundException", "Statement with the provided id does not exist.")
		}
		h.permit(new("r"), "s1", "111111111111")
		h.permit(new("r"), "s2", "*")
		_, err := call(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("r"), StatementId: new("nope")})
		wantError(t, err, "ResourceNotFoundException", "Statement with the provided id does not exist.")

		must(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("r"), StatementId: new("s1")})
		if got := slices.Collect(maps.Keys(bySid(h.policyOf(new("r"))))); !slices.Equal(got, []string{"s2"}) {
			t.Errorf("statements after removing s1 = %v, want s2", got)
		}
		must(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("r"), RemoveAllPermissions: true})
		if d := h.describeBus(new("r")); d.Policy != nil {
			t.Errorf("Policy after RemoveAllPermissions = %s, want none", *d.Policy)
		}
		if b := h.listBuses(new("r")); len(b) != 1 || b[0].Policy != nil {
			t.Errorf("ListEventBuses after RemoveAllPermissions = %+v, want no Policy", b)
		}
	})
}

// What removing a bus's last statement leaves, and what RemoveAllPermissions
// answers on a bus with no policy, are unrecorded (eventbridge-buses.md
// 4.3, EBB-06); understudy follows events.bus-policy.remove-last-statement
// and events.bus-policy.remove-all-without-policy.
func TestRemovePermissionUnrecordedCases(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("r")
		h.permit(new("r"), "only", "111111111111")
		must(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("r"), StatementId: new("only")})
		doc := h.policyOf(new("r"))
		switch v := h.switchValue("events.bus-policy.remove-last-statement"); v {
		case "policy-removed":
			if doc != nil {
				t.Errorf("Policy after removing the last statement = %+v, want none", doc)
			}
		case "empty-statement":
			if doc == nil || len(doc.statements) != 0 {
				t.Errorf("Policy after removing the last statement = %+v, want an empty Statement list", doc)
			}
		default:
			t.Fatalf("no expectation for events.bus-policy.remove-last-statement = %q", v)
		}

		h.createBus("bare")
		_, err := call(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("bare"), RemoveAllPermissions: true})
		switch v := h.switchValue("events.bus-policy.remove-all-without-policy"); v {
		case "succeed":
			if err != nil {
				t.Errorf("RemoveAllPermissions without a policy = %v, want success", err)
			}
		default:
			if err == nil || err.ID.JSONType != v {
				t.Errorf("RemoveAllPermissions without a policy = %v, want %s", err, v)
			}
		}
	})
}

// A bus policy acts as an ordered map of statements keyed by Sid under any
// sequence of statement puts and removals: a put upserts, a removal of a
// present Sid removes it, of an absent one is not found and changes
// nothing (third-party-recorded test_events.snapshot.json:L939-L987,
// L1176-L1199, L1269-L1328), and RemoveAllPermissions drops the policy
// (L1206-L1215). The unrecorded empty cases follow their switches.
func TestBusPolicyActsAsAMapOfStatements(t *testing.T) {
	run(t, func(h *harness) {
		lastRemoved := h.switchValue("events.bus-policy.remove-last-statement")
		removeAllBare := h.switchValue("events.bus-policy.remove-all-without-policy")
		accounts := 0
		rapid.Check(bubbleTB{h.T}, func(t *rapid.T) {
			accounts++
			h.Scope.Account = mustAccount(h, fmt.Sprintf("%012d", 500000+accounts))
			h.createBus("b")
			bus := new("b")
			model := map[string]string{}
			// present is whether the bus has a policy at all, which an
			// empty-statement removal can leave with no statements.
			present := false
			for i := range rapid.IntRange(1, 12).Draw(t, "ops") {
				sid := rapid.SampledFrom([]string{"a", "b", "c"}).Draw(t, "sid")
				switch rapid.IntRange(0, 4).Draw(t, "op") {
				case 0, 1:
					principal := rapid.SampledFrom([]string{"111111111111", "222222222222", "*"}).Draw(t, "principal")
					h.permit(bus, sid, principal)
					model[sid] = principal
					present = true
				case 2, 3:
					_, err := call(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: bus, StatementId: new(sid)})
					if _, ok := model[sid]; !ok {
						if err == nil || err.ID.JSONType != "ResourceNotFoundException" || err.Message != "Statement with the provided id does not exist." {
							t.Fatalf("op %d: removing absent %s = %v, want ResourceNotFoundException", i, sid, err)
						}
						break
					}
					if err != nil {
						t.Fatalf("op %d: removing %s = %v", i, sid, err)
					}
					delete(model, sid)
					if len(model) == 0 && lastRemoved == "policy-removed" {
						present = false
					}
				case 4:
					_, err := call(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: bus, RemoveAllPermissions: true})
					switch {
					case present && err != nil:
						t.Fatalf("op %d: RemoveAllPermissions = %v", i, err)
					case !present && removeAllBare == "succeed" && err != nil:
						t.Fatalf("op %d: RemoveAllPermissions without a policy = %v, want success", i, err)
					case !present && removeAllBare != "succeed" && (err == nil || err.ID.JSONType != removeAllBare):
						t.Fatalf("op %d: RemoveAllPermissions without a policy = %v, want %s", i, err, removeAllBare)
					}
					clear(model)
					present = false
				}
				doc := h.policyOf(bus)
				if (doc != nil) != present {
					t.Fatalf("op %d: Policy %+v, want present %t (model %v)", i, doc, present, model)
				}
				got := map[string]string{}
				for sid, s := range bySid(doc) {
					var star string
					var account struct{ AWS string }
					switch {
					case json.Unmarshal(s.Principal, &star) == nil:
						got[sid] = star
					case json.Unmarshal(s.Principal, &account) == nil:
						got[sid] = strings.TrimSuffix(strings.TrimPrefix(account.AWS, "arn:aws:iam::"), ":root")
					default:
						t.Fatalf("op %d: principal %s is neither a string nor {\"AWS\": ...}", i, s.Principal)
					}
				}
				if doc != nil && len(doc.statements) != len(got) {
					t.Fatalf("op %d: %d statements for %d Sids: %+v", i, len(doc.statements), len(got), doc.statements)
				}
				if !maps.Equal(got, model) {
					t.Fatalf("op %d: statements %v, want %v", i, got, model)
				}
			}
		})
	})
}

// The bus policy is the resource policy the delivery access advisory and
// IAM enforcement read (ARCHITECTURE.md 16): a bus that does not exist,
// or an ARN that names no bus, has none to read; the default bus and a
// custom bus without a policy have an absent one; a bus with a policy has
// it parsed.
func TestBusResourcePolicy(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("guarded")
		for _, absent := range []string{busPrefix + "missing", ruleARN + "r", queueARN, "arn:aws:events:us-east-1:111111111111:event-bus/guarded"} {
			if doc, ok := h.svc.ResourcePolicy(h.mustARN(absent)); ok || doc != nil {
				t.Errorf("ResourcePolicy(%s) = %v, %t; want no bus", absent, doc, ok)
			}
		}
		for _, bare := range []string{busPrefix + "default", busPrefix + "guarded"} {
			if doc, ok := h.svc.ResourcePolicy(h.mustARN(bare)); !ok || doc != nil {
				t.Errorf("ResourcePolicy(%s) = %v, %t; want a bus with no policy", bare, doc, ok)
			}
		}
		h.permit(new("guarded"), "s1", "111111111111")
		doc, ok := h.svc.ResourcePolicy(h.mustARN(busPrefix + "guarded"))
		if !ok || doc == nil || len(doc.Statements) != 1 || doc.Version != policydoc.Version20121017 {
			t.Errorf("ResourcePolicy(guarded) = %+v, %t; want the one-statement 2012-10-17 policy", doc, ok)
		}
	})
}
