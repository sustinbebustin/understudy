package ssm_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// A version selector reads that version by name or by ARN, echoing the
// bare name and the selector with its colon (ssm-parameters.md 6.3); a
// version that does not exist, version 0 included, is
// ParameterVersionNotFound naming the parameter and the version, never the
// latest (6.1). A selector on a missing parameter is ParameterNotFound with
// the empty message (2.2).
func TestGetParameterVersionSelector(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/vs/a", "v1")
		h.overwrite("/vs/a", "v2")
		for _, ask := range []string{"/vs/a:1", arnPrefix + "vs/a:1"} {
			got := h.mustGet(ask, false)
			if val(got.Name) != "/vs/a" || val(got.Selector) != ":1" || val(got.Value) != "v1" || got.Version != 1 || val(got.ARN) != arnPrefix+"vs/a" {
				h.Errorf("GetParameter %s = Name %q Selector %q Value %q Version %d ARN %q", ask, val(got.Name), val(got.Selector), val(got.Value), got.Version, val(got.ARN))
			}
		}
		for _, c := range []struct{ ask, names string }{{"/vs/a:9", "9"}, {"/vs/a:0", "0"}, {"/vs/a:nolabel", "nolabel"}} {
			err := h.get(c.ask)
			wantCode(h.T, "GetParameter "+c.ask, err, ssmapi.IdentityParameterVersionNotFound())
			wantOwnMessage(h.T, "GetParameter "+c.ask, err, "", "/vs/a", c.names)
		}
		wantError(h.T, "GetParameter /vs/missing:1", h.get("/vs/missing:1"), ssmapi.IdentityParameterNotFound(), "")
		wantCode(h.T, "GetParameter /vs/a:1:2", h.get("/vs/a:1:2"), ssmapi.IdentityValidationException())
	})
}

// GetParameters echoes each selector beside the name, sorts by the echoed
// name, and decrypts each SecureString when asked (ssm-parameters.md 6.3,
// 7.3, 5.3).
func TestGetParametersSelectorsAndDecryption(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/gs/b", "b1")
		h.overwrite("/gs/b", "b2")
		h.putSecure("/gs/a", "secret", nil)
		out := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/gs/b:1", "/gs/a"}, WithDecryption: new(true)})
		if got := names(out.Parameters, paramName); !slices.Equal(got, []string{"/gs/a", "/gs/b"}) {
			h.Fatalf("Parameters = %v, want /gs/a /gs/b", got)
		}
		a, b := out.Parameters[0], out.Parameters[1]
		if val(a.Value) != "secret" || a.Selector != nil {
			h.Errorf("/gs/a = Value %q Selector %v, want the plaintext and no Selector", val(a.Value), a.Selector)
		}
		if val(b.Value) != "b1" || val(b.Selector) != ":1" || b.Version != 1 {
			h.Errorf("/gs/b:1 = Value %q Selector %q Version %d, want b1 :1 1", val(b.Value), val(b.Selector), b.Version)
		}
		sealed := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/gs/a"}})
		if v := val(sealed.Parameters[0].Value); v == "secret" || strings.Contains(v, "secret") {
			h.Error("GetParameters without decryption returned the plaintext")
		}
	})
}

// No name found answers Parameters [] and every name in InvalidParameters,
// both present (ssm-parameters.md 7.10). A reserved name anywhere in the
// request makes the whole call 501 (spec "Scope").
func TestGetParametersNoneFound(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/nf/a", "v")
		out := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/nf/x", "/nf/y"}})
		if out.Parameters == nil || len(out.Parameters) != 0 || !slices.Equal(out.InvalidParameters, []string{"/nf/x", "/nf/y"}) {
			h.Errorf("GetParameters of missing names = Parameters %#v InvalidParameters %v", out.Parameters, out.InvalidParameters)
		}
		_, err := call(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/nf/a", "/aws/service/x:1"}})
		wantNotImplemented(h.T, "GetParameters with a public parameter", err, "/aws/service/")
	})
}
