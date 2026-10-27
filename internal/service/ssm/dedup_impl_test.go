package ssm_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// UNVERIFIED (SSM-18), ssm-parameters.md 7.3: GetParameters answers a
// parameter named twice once, and /r, r and r's ARN name one parameter
// (3.2), so a call naming all three returns one entry, echoing the first
// spelling (switch ssm.name.root-slash-echo). The same parameter with
// different selectors is answered per selector.
func TestGetParametersAnswersOneParameterOnce(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/r", "v1")
		h.overwrite("/r", "v2")
		out := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/r", "r", arnPrefix + "r", "r:1"}})
		if got := names(out.Parameters, paramName); !slices.Equal(got, []string{"/r", "r"}) {
			h.Fatalf("Parameters = %v, want /r (latest) and r (:1)", got)
		}
		if out.Parameters[0].Selector != nil || val(out.Parameters[1].Selector) != ":1" {
			h.Errorf("Selectors = %v %q, want none and :1", out.Parameters[0].Selector, val(out.Parameters[1].Selector))
		}
		if len(out.InvalidParameters) != 0 {
			h.Errorf("InvalidParameters = %v, want []", out.InvalidParameters)
		}
	})
}

// UNVERIFIED (SSM-27), ssm-parameters.md 7.7: DeleteParameters naming one
// parameter as /r and r deletes it once, lists the first spelling in
// DeletedParameters and lists nothing as invalid, since the parameter
// existed when the request arrived.
func TestDeleteParametersDeletesOneParameterOnce(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/r", "v")
		out := must(h, ssmapi.OpDeleteParameters, &ssmapi.DeleteParametersInput{Names: []string{"/r", "r"}})
		if !slices.Equal(out.DeletedParameters, []string{"/r"}) || len(out.InvalidParameters) != 0 {
			h.Errorf("DeleteParameters /r r = Deleted %v Invalid %v, want [/r] []", out.DeletedParameters, out.InvalidParameters)
		}
	})
}
