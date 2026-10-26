package admin_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// An unknown action kind is refused with the kinds listed, the HTTP
// delivery actions last.
func TestUnknownFaultActionListsTheHTTPKinds(t *testing.T) {
	var r admin.FaultRule
	err := json.Unmarshal([]byte(`{"phase":"delivery","action":{"kind":"http-reset"},"trigger":{"kind":"always"}}`), &r)
	form, ok := errors.AsType[*admin.FaultFormError](err)
	if !ok || form.Field != "action.kind" || !strings.HasSuffix(form.Reason, "drop-delivery, http-status and http-timeout") {
		t.Errorf("decoding an http-reset action = %v, want an action.kind refusal listing http-status and http-timeout", err)
	}
}

// An http-status action without a status decodes as status 0, which the
// server refuses with the range it takes; a status that is no integer is
// refused here.
func TestHTTPStatusActionStatusForms(t *testing.T) {
	var r admin.FaultRule
	if err := json.Unmarshal([]byte(`{"phase":"delivery","action":{"kind":"http-status"},"trigger":{"kind":"always"}}`), &r); err != nil {
		t.Fatalf("decoding an http-status action without a status: %v", err)
	}
	if r.Action != (admin.HTTPStatus{}) {
		t.Errorf("action = %#v, want HTTPStatus{Status: 0}", r.Action)
	}
	err := json.Unmarshal([]byte(`{"phase":"delivery","action":{"kind":"http-status","status":503.5},"trigger":{"kind":"always"}}`), &r)
	if form, ok := errors.AsType[*admin.FaultFormError](err); !ok || form.Field != "action.status" {
		t.Errorf("decoding a fractional status = %v, want a FaultFormError on action.status", err)
	}
}
