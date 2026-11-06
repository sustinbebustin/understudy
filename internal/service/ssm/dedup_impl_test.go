package ssm_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// GetParameters answers each spelling asked once, with its own Name
// (switch ssm.name.root-slash-echo): /r and r of one root parameter are two
// entries (ssm-parameters.md 3.2, 7.3; SSM-07 step 03), and a spelling
// given twice is one (SSM-18 step 01). UNVERIFIED (SSM-07): r's ARN is a
// third entry, named r. The same parameter with a selector is answered per
// selector.
func TestGetParametersAnswersEachSpellingOnce(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/r", "v1")
		h.overwrite("/r", "v2")
		out := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/r", "r", "/r", arnPrefix + "r", "r:1"}})
		if got := names(out.Parameters, paramName); !slices.Equal(got, []string{"/r", "r", "r", "r"}) {
			h.Fatalf("Parameters = %v, want /r, r, r (ARN) and r (:1)", got)
		}
		selectors := make([]string, 0, len(out.Parameters))
		for _, p := range out.Parameters {
			selectors = append(selectors, val(p.Selector))
		}
		if !slices.Equal(selectors, []string{"", "", "", ":1"}) {
			h.Errorf("Selectors = %q, want only the last :1", selectors)
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
