package admin_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/admin"
)

// tceRule injects TransactionCanceledException on TransactWriteItems with
// members.
func tceRule(members json.RawMessage) admin.FaultRule {
	return admin.FaultRule{
		Phase:   admin.RequestPhase,
		Match:   admin.FaultMatch{Operations: []string{"dynamodb.TransactWriteItems"}},
		Action:  admin.InjectError{Shape: "TransactionCanceledException", Members: members},
		Trigger: admin.Always{},
	}
}

func compact(t interface {
	Helper()
	Fatalf(string, ...any)
}, data []byte,
) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, data); err != nil {
		t.Fatalf("compacting %s: %v", data, err)
	}
	return b.String()
}

// drawObject draws a JSON object of the kind a rule's members are: names
// to strings, numbers, booleans, arrays and objects.
func drawObject(t *rapid.T, depth int, label string) map[string]any {
	names := rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Z][a-z]{0,5}`), 1, 3, func(s string) string { return s }).Draw(t, label+".names")
	out := make(map[string]any, len(names))
	for _, n := range names {
		kinds := 3
		if depth > 0 {
			kinds = 5
		}
		switch rapid.IntRange(0, kinds-1).Draw(t, label+"."+n) {
		case 0:
			out[n] = rapid.StringMatching(`[A-Za-z .]{0,8}`).Draw(t, label+"."+n+".s")
		case 1:
			out[n] = rapid.IntRange(-1000, 1000).Draw(t, label+"."+n+".n")
		case 2:
			out[n] = rapid.Bool().Draw(t, label+"."+n+".b")
		case 3:
			out[n] = []any{drawObject(t, depth-1, label+"."+n+"[0]")}
		default:
			out[n] = drawObject(t, depth-1, label+"."+n)
		}
	}
	return out
}

// An error action's members travel as the JSON object given: written under
// "members" in the action, and read back as the same object.
func TestInjectErrorMembersRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		raw, err := json.Marshal(drawObject(t, 2, "members"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(tceRule(raw))
		if err != nil {
			t.Fatalf("encoding a rule with members %s: %v", raw, err)
		}
		var wire struct {
			Action struct {
				Members json.RawMessage `json:"members"`
			} `json:"action"`
		}
		if err := json.Unmarshal(data, &wire); err != nil || compact(t, wire.Action.Members) != compact(t, raw) {
			t.Fatalf("encoded %s, %v; want action.members %s", data, err, raw)
		}
		var got admin.FaultRule
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decoding %s: %v", data, err)
		}
		a, ok := got.Action.(admin.InjectError)
		if !ok || a.Shape != "TransactionCanceledException" || compact(t, a.Members) != compact(t, raw) {
			t.Fatalf("decoded %#v, want the shape and members %s", got.Action, raw)
		}
	})
}

// An error action without members writes none.
func TestInjectErrorWithoutMembersWritesNone(t *testing.T) {
	data, err := json.Marshal(tceRule(nil))
	if err != nil || strings.Contains(string(data), "members") {
		t.Errorf("encoded %s, %v; want no members", data, err)
	}
}

// members must be a JSON object, and only an error action takes it.
func TestMembersDecodeRefusals(t *testing.T) {
	rule := func(action string) string {
		return `{"phase":"request","match":{"operations":["dynamodb.TransactWriteItems"]},"action":` + action + `,"trigger":{"kind":"always"}}`
	}
	for _, c := range []struct {
		action, reason string
		prefix         bool
	}{
		{`{"kind":"error","shape":"TransactionCanceledException","members":[{"Code":"TransactionConflict"}]}`, "is a JSON array; members is an object of the error shape's members", false},
		{`{"kind":"error","shape":"TransactionCanceledException","members":"CancellationReasons"}`, "is a JSON string; members is an object of the error shape's members", false},
		{`{"kind":"after-commit","shape":"TransactionCanceledException","members":{"CancellationReasons":[]}}`, `is not a member of action "after-commit", which takes `, true},
		{`{"kind":"status-ok-error","shape":"InternalError","members":{"Code":"x"}}`, `is not a member of action "status-ok-error", which takes `, true},
		{`{"kind":"throttle","members":{}}`, `is not a member of action "throttle", which takes `, true},
	} {
		var r admin.FaultRule
		err := json.Unmarshal([]byte(rule(c.action)), &r)
		form, ok := errors.AsType[*admin.FaultFormError](err)
		switch {
		case !ok || form.Field != "action.members":
			t.Errorf("decoding action %s = %v; want a FaultFormError on action.members", c.action, err)
		case c.prefix && !strings.HasPrefix(form.Reason, c.reason):
			t.Errorf("decoding action %s: reason %q, want it to start %q", c.action, form.Reason, c.reason)
		case !c.prefix && form.Reason != c.reason:
			t.Errorf("decoding action %s: reason %q, want %q", c.action, form.Reason, c.reason)
		}
	}
}

// Members that are not a JSON object do not encode: the error names
// action.members.
func TestMembersEncodeRefusals(t *testing.T) {
	for _, raw := range []string{`[{"Code":"TransactionConflict"}]`, `"x"`, `{"CancellationReasons":`} {
		_, err := tceRule(json.RawMessage(raw)).MarshalJSON()
		form, ok := errors.AsType[*admin.FaultFormError](err)
		if !ok || form.Field != "action.members" || !strings.HasPrefix(form.Reason, "is not a JSON object: ") {
			t.Errorf("encoding members %s = %v; want a FaultFormError on action.members that is not a JSON object", raw, err)
		}
	}
}
