package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/server"
)

// serverConfig carries iam.enforce onto server.Config
// (iam-enforcement-surfaces.md 12.1), off by default.
func TestServerConfigMapsIAMEnforce(t *testing.T) {
	cases := []struct {
		args []string
		want server.IAMEnforce
	}{
		{nil, server.IAMEnforceOff},
		{[]string{"--iam-enforce", "off"}, server.IAMEnforceOff},
		{[]string{"--iam-enforce", "advise"}, server.IAMEnforceAdvise},
		{[]string{"--iam-enforce", "enforce"}, server.IAMEnforceEnforce},
	}
	for _, c := range cases {
		s, err := serverConfig(loadFile(t, c.args, `{}`), "test")
		if err != nil {
			t.Errorf("%q: serverConfig = %v", c.args, err)
			continue
		}
		if s.IAMEnforce != c.want {
			t.Errorf("%q: IAMEnforce = %v, want %v", c.args, s.IAMEnforce, c.want)
		}
	}
}

// Under enforce a role IAM does not hold is denied at authorization
// whatever iam.assumeRole says, and the iam.assumeRole line of serve --help
// says so by naming iam.enforce (iam-evaluation.md 3.5).
func TestAssumeRoleUsageNamesEnforcement(t *testing.T) {
	settings := config.Settings()
	at := slices.IndexFunc(settings, func(s config.Setting) bool { return s.Key == "iam.assumeRole" })
	if at < 0 {
		t.Fatal("config.Settings() has no iam.assumeRole")
	}
	if usage := settings[at].Usage; !strings.Contains(usage, "iam.enforce") {
		t.Errorf("iam.assumeRole usage %q does not say what iam.enforce does to it", usage)
	}
}
