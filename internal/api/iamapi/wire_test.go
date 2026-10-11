package iamapi_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
)

// policyChars are the characters IAM's policy document charset allows:
// tab, newline, carriage return and U+0020 to U+00FF, with the ones a form
// or a percent decoder treats specially drawn often.
func policyChars() *rapid.Generator[rune] {
	return rapid.OneOf(
		rapid.RuneFrom([]rune{'\t', '\n', '\r', ' ', '+', '%', '&', '=', '<', '"', '\\'}),
		rapid.Rune().Filter(func(r rune) bool { return r >= 0x20 && r <= 0xFF }),
	)
}

var policyElement = regexp.MustCompile(`<PolicyDocument>([^<]*)</PolicyDocument>`)

// A policy document crosses the wire unchanged in both directions: the form
// body of PutRolePolicy decodes to the text the client sent, and the
// percent-encoded document of GetRolePolicy reads back as that text through
// url.QueryUnescape, the decoder Terraform uses (iam-sts-kms.md 3.3).
func TestPolicyDocumentsCrossTheWireUnchanged(t *testing.T) {
	codec := awsquery.Codec{}
	rapid.Check(t, func(t *rapid.T) {
		doc := rapid.StringOfN(policyChars(), 1, 40, -1).Draw(t, "doc")

		form := url.Values{"Action": {"PutRolePolicy"}, "Version": {"2010-05-08"}, "RoleName": {"r"}, "PolicyName": {"p"}, "PolicyDocument": {doc}}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		var in iamapi.PutRolePolicyInput
		if err := codec.Decode(req, iamapi.OpPutRolePolicy.Descriptor(), &in); err != nil {
			t.Fatalf("Decode: %s %s", err.ID.QueryCode, err.Message)
		}
		if in.PolicyDocument != doc {
			t.Fatalf("decoded PolicyDocument = %q, sent %q", in.PolicyDocument, doc)
		}

		out := &iamapi.GetRolePolicyOutput{RoleName: "r", PolicyName: "p", PolicyDocument: doc}
		resp, err := codec.Encode(iamapi.OpGetRolePolicy.Descriptor(), "req", out, http.StatusOK)
		if err != nil {
			t.Fatalf("Encode: %s %s", err.ID.QueryCode, err.Message)
		}
		m := policyElement.FindSubmatch(resp.Body)
		if m == nil {
			t.Fatalf("body %s has no PolicyDocument element", resp.Body)
		}
		got, uerr := url.QueryUnescape(string(m[1]))
		if uerr != nil || got != doc {
			t.Fatalf("QueryUnescape(%s) = %q, %v; want %q", m[1], got, uerr, doc)
		}
	})
}
