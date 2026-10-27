package ssm

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// splitSelector cuts the selector of ssm-parameters.md 6.3 off a name or
// an ARN: all digits is a version, anything else a label, and an ARN's
// selector follows its resource. The refusals (an empty selector, a second
// colon, a version past int64) are understudy's. UNVERIFIED (SSM-17),
// ssm-parameters.md 6.3.
func TestSplitSelector(t *testing.T) {
	const arn = "arn:aws:ssm:us-east-1:000000000000:parameter/a/b"
	for _, c := range []struct {
		raw, base string
		sel       selector
	}{
		{"/a/b", "/a/b", noSelector{}},
		{" /a/b:3 ", "/a/b", versionSelector{n: 3}},
		{"/a/b:0", "/a/b", versionSelector{n: 0}},
		{"/a/b:prod", "/a/b", labelSelector{label: "prod"}},
		{"/a/b:1a", "/a/b", labelSelector{label: "1a"}},
		{arn, arn, noSelector{}},
		{arn + ":2", arn, versionSelector{n: 2}},
		{arn + ":live", arn, labelSelector{label: "live"}},
		{"arn:aws:ssm", "arn:aws:ssm", noSelector{}},
	} {
		base, sel, err := splitSelector(c.raw)
		if err != nil {
			t.Errorf("splitSelector(%q) = %s %q", c.raw, err.ID.Shape, err.Message)
			continue
		}
		if base != c.base || sel != c.sel {
			t.Errorf("splitSelector(%q) = %q %#v, want %q %#v", c.raw, base, sel, c.base, c.sel)
		}
	}
	for _, raw := range []string{"/a/b:", "/a/b:1:2", "/a/b:99999999999999999999", arn + ":1:2"} {
		_, _, err := splitSelector(raw)
		if err == nil || err.ID.Shape != ssmapi.IdentityValidationException().Shape {
			t.Errorf("splitSelector(%q) = %v, want ssm ValidationException", raw, err)
		}
	}
}

// pick resolves a selector against the retained versions and the label
// index (ssm-parameters.md 6.1-6.3): a label reads the version it names, a
// number a retained version, and neither falls back to the latest.
func TestPickReadsTheSelectedVersion(t *testing.T) {
	p := Parameter{
		Name:     "/p",
		Versions: []Version{{Number: 4}, {Number: 5}, {Number: 6}},
		Labels:   map[string]int64{"prod": 5, "gone": 2},
	}
	for _, c := range []struct {
		sel  selector
		want int64
		ok   bool
	}{
		{noSelector{}, 6, true},
		{versionSelector{n: 4}, 4, true},
		{versionSelector{n: 3}, 0, false},
		{labelSelector{label: "prod"}, 5, true},
		{labelSelector{label: "gone"}, 0, false},
		{labelSelector{label: "none"}, 0, false},
	} {
		v, ok := p.pick(c.sel)
		if diff := cmp.Diff([]any{c.want, c.ok}, []any{v.Number, ok}); diff != "" {
			t.Errorf("pick(%#v) (-want +got):\n%s", c.sel, diff)
		}
	}
}
