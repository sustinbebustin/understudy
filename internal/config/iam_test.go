package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/config"
)

// iam.enforce is the one IAM enforcement setting (iam-enforcement-surfaces.md
// 12.1), spelled --iam-enforce and UNDERSTUDY_IAM_ENFORCE: off (the
// default), advise or enforce, the spec's settled spelling. Every other
// text, other cases included, is refused rather than read as one of them
// (CLAUDE.md invariant 3). The 5a version of this test refused advise and
// enforce; 5b runs them (contract 9.1).
func TestIAMEnforceModes(t *testing.T) {
	if got := load(t, nil, nil, files{}).IAM.Enforce; got != config.EnforceOff {
		t.Errorf("default iam.enforce = %v, want EnforceOff", got)
	}
	sources := []struct {
		name  string
		args  []string
		env   []string
		f     files
		where string
	}{
		{"flag", []string{"--iam-enforce", "%s"}, nil, files{}, "--iam-enforce"},
		{"variable", nil, []string{"UNDERSTUDY_IAM_ENFORCE=%s"}, files{}, "UNDERSTUDY_IAM_ENFORCE"},
		{"file", nil, nil, files{config.DefaultConfigFile: `{"iam": {"enforce": "%s"}}`}, config.DefaultConfigFile + ": iam.enforce"},
	}
	// fill puts the value v where a source's text holds %s.
	fill := func(xs []string, v string) []string {
		out := make([]string, len(xs))
		for i, x := range xs {
			out[i] = strings.ReplaceAll(x, "%s", v)
		}
		return out
	}
	fillFiles := func(fs files, v string) files {
		out := files{}
		for k, text := range fs {
			out[k] = strings.ReplaceAll(text, "%s", v)
		}
		return out
	}
	modes := map[string]config.Enforcement{"off": config.EnforceOff, "advise": config.EnforceAdvise, "enforce": config.EnforceEnforce}
	for _, s := range sources {
		for text, want := range modes {
			if got := load(t, fill(s.args, text), fill(s.env, text), fillFiles(s.f, text)).IAM.Enforce; got != want {
				t.Errorf("%s %s: iam.enforce = %v, want %v", s.name, text, got, want)
			}
		}
		for _, v := range []string{"on", "report", "OFF", "Enforce", "advisory"} {
			err := loadErr(t, fill(s.args, v), fill(s.env, v), fillFiles(s.f, v))
			reason := `"` + v + `" is not off, advise or enforce`
			if err.Where != s.where || err.Reason != reason || err.Hint != "set iam.enforce to off, advise or enforce" {
				t.Errorf("%s %q: refused at %q: %q; hint %q; want at %q: %q", s.name, v, err.Where, err.Reason, err.Hint, s.where, reason)
			}
		}
	}
	err := loadErr(t, []string{"--iam-enforce", "on"}, nil, files{})
	want := `config: --iam-enforce: "on" is not off, advise or enforce; set iam.enforce to off, advise or enforce`
	if err.Error() != want {
		t.Errorf("rendered refusal = %q, want %q", err.Error(), want)
	}
}

// The flag outranks the variable, which outranks the file, as for every
// setting.
func TestIAMEnforcePrecedence(t *testing.T) {
	f := files{config.DefaultConfigFile: `{"iam": {"enforce": "enforce"}}`}
	if got := load(t, []string{"--iam-enforce=off"}, []string{"UNDERSTUDY_IAM_ENFORCE=advise"}, f).IAM.Enforce; got != config.EnforceOff {
		t.Errorf("flag off over variable and file: iam.enforce = %v", got)
	}
	if got := load(t, nil, []string{"UNDERSTUDY_IAM_ENFORCE=advise"}, f).IAM.Enforce; got != config.EnforceAdvise {
		t.Errorf("variable advise over file enforce: iam.enforce = %v", got)
	}
	if got := load(t, nil, nil, f).IAM.Enforce; got != config.EnforceEnforce {
		t.Errorf("file enforce: iam.enforce = %v", got)
	}
	if err := loadErr(t, nil, []string{"UNDERSTUDY_IAM_ENFORCE=on"}, files{config.DefaultConfigFile: `{"iam": {"enforce": "off"}}`}); err.Where != "UNDERSTUDY_IAM_ENFORCE" {
		t.Errorf("variable on over file off = %v, want refused at the variable", err)
	}
}

// The setting is listed for help and docs/configuration.md right after
// sigv4, the other request-checking mode, beside the file's iam section,
// with a usage line that says what each mode does (contract 9.1).
func TestIAMEnforceIsASetting(t *testing.T) {
	settings := config.Settings()
	at := slices.IndexFunc(settings, func(s config.Setting) bool { return s.Key == "iam.enforce" })
	if at < 0 {
		t.Fatal("config.Settings() has no iam.enforce")
	}
	s := settings[at]
	if s.Flag != "iam-enforce" || s.Env != "UNDERSTUDY_IAM_ENFORCE" || s.Value != "MODE" {
		t.Errorf("iam.enforce setting = %+v", s)
	}
	const usage = "IAM enforcement: off (default) records which actions each principal uses and enforces nothing; " +
		"advise evaluates every call and journals an advisory for each denial AWS would issue, answering as off does; " +
		"enforce answers those denials"
	if s.Usage != usage {
		t.Errorf("iam.enforce usage =\n%q\nwant\n%q", s.Usage, usage)
	}
	if at == 0 || settings[at-1].Key != "sigv4" {
		t.Errorf("iam.enforce follows %q, want sigv4", settings[max(at-1, 0)].Key)
	}
}
