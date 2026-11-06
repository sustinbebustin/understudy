package cli_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// serve refuses an iam.enforce text that is not off, advise or enforce
// before it listens, as a usage error naming the setting and what to do.
// enforce itself is accepted (iam-enforcement-surfaces.md 12.1).
func TestServeRefusesAnUnknownIAMEnforceMode(t *testing.T) {
	cases := []struct {
		args, env []string
		want      string
	}{
		{[]string{"serve", "--iam-enforce", "on"}, nil, `--iam-enforce: "on" is not off, advise or enforce; set iam.enforce to off, advise or enforce`},
		{[]string{"serve"}, []string{"UNDERSTUDY_IAM_ENFORCE=Advise"}, `UNDERSTUDY_IAM_ENFORCE: "Advise" is not off, advise or enforce; set iam.enforce to off, advise or enforce`},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), closedEndpoint(t), c.env, c.args...)
		if r.code != admin.ExitUsage || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%q with %q = %d, stderr %q; want %d and %q", c.args, c.env, r.code, r.stderr, admin.ExitUsage, c.want)
		}
	}
}
