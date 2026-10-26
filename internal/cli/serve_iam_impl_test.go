package cli_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// serve refuses an iam.enforce mode this build does not run before it
// listens, as a usage error naming the setting and what to do.
func TestServeRefusesIAMEnforcement(t *testing.T) {
	cases := []struct {
		args, env []string
		want      string
	}{
		{[]string{"serve", "--iam-enforce", "enforce"}, nil, `--iam-enforce: "enforce" is not supported: IAM enforcement is not built yet, so this build accepts only off; remove the setting or set it to off`},
		{[]string{"serve"}, []string{"UNDERSTUDY_IAM_ENFORCE=advise"}, `UNDERSTUDY_IAM_ENFORCE: "advise" is not supported: IAM enforcement is not built yet, so this build accepts only off; remove the setting or set it to off`},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), closedEndpoint(t), c.env, c.args...)
		if r.code != admin.ExitUsage || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%q with %q = %d, stderr %q; want %d and %q", c.args, c.env, r.code, r.stderr, admin.ExitUsage, c.want)
		}
	}
}
