package admin_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// The HTTP delivery actions have the rule file's JSON form: {"kind":
// "http-status", "status": 503} and {"kind": "http-timeout"}.
func TestHTTPFaultActionsJSON(t *testing.T) {
	cases := []struct {
		action admin.FaultAction
		want   string
	}{
		{admin.HTTPStatus{Status: 503}, `"action":{"kind":"http-status","status":503}`},
		{admin.HTTPTimeout{}, `"action":{"kind":"http-timeout"}`},
	}
	for _, c := range cases {
		rule := admin.FaultRule{
			Phase:   admin.DeliveryPhase,
			Match:   admin.FaultMatch{Mechanisms: []string{"sns-to-http"}},
			Action:  c.action,
			Trigger: admin.NextN{N: 1},
		}
		data, err := json.Marshal(rule)
		if err != nil {
			t.Errorf("Marshal(%T): %v", c.action, err)
			continue
		}
		if !strings.Contains(string(data), c.want) {
			t.Errorf("Marshal(%T) = %s, want it to contain %s", c.action, data, c.want)
		}
		var back admin.FaultRule
		if err := json.Unmarshal(data, &back); err != nil || !reflect.DeepEqual(back, rule) {
			t.Errorf("Unmarshal(%s) = %+v, %v; want %+v", data, back, err, rule)
		}
	}
}

// A member the kind does not take is refused at that member, as for the
// other actions.
func TestHTTPFaultActionsRefuseOtherMembers(t *testing.T) {
	for body, field := range map[string]string{
		`{"phase":"delivery","action":{"kind":"http-timeout","status":503},"trigger":{"kind":"always"}}`:            "action.status",
		`{"phase":"delivery","action":{"kind":"http-status","status":503,"wall":"1s"},"trigger":{"kind":"always"}}`: "action.wall",
		`{"phase":"delivery","action":{"kind":"http-status","status":"503"},"trigger":{"kind":"always"}}`:           "action.status",
	} {
		var r admin.FaultRule
		err := json.Unmarshal([]byte(body), &r)
		form, ok := errors.AsType[*admin.FaultFormError](err)
		if !ok || form.Field != field {
			t.Errorf("decoding %s = %v, want a FaultFormError on %s", body, err, field)
		}
	}
}
