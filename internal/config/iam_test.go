package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/config"
)

// iam.enforce is the one IAM enforcement setting (iam-enforcement-surfaces.md
// 12.1), spelled --iam-enforce and UNDERSTUDY_IAM_ENFORCE. This build runs
// only off, the default; every other value, advise and enforce included,
// is refused rather than accepted and ignored (CLAUDE.md invariant 3).
func TestIAMEnforceAcceptsOnlyOff(t *testing.T) {
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
	for _, s := range sources {
		if got := load(t, fill(s.args, "off"), fill(s.env, "off"), fillFiles(s.f, "off")).IAM.Enforce; got != config.EnforceOff {
			t.Errorf("%s off: iam.enforce = %v, want EnforceOff", s.name, got)
		}
		for _, v := range []string{"advise", "enforce", "on", "OFF"} {
			f := fillFiles(s.f, v)
			err := loadErr(t, fill(s.args, v), fill(s.env, v), f)
			reason := `"` + v + `" is not supported: IAM enforcement is not built yet, so this build accepts only off`
			if err.Where != s.where || err.Reason != reason || err.Hint != "remove the setting or set it to off" {
				t.Errorf("%s %q: refused at %q: %q; hint %q; want at %q: %q", s.name, v, err.Where, err.Reason, err.Hint, s.where, reason)
			}
		}
	}
	err := loadErr(t, []string{"--iam-enforce", "enforce"}, nil, files{})
	want := `config: --iam-enforce: "enforce" is not supported: IAM enforcement is not built yet, so this build accepts only off; remove the setting or set it to off`
	if err.Error() != want {
		t.Errorf("rendered refusal = %q, want %q", err.Error(), want)
	}
}

// The flag outranks the variable, which outranks the file, as for every
// setting: a refused value in a lower source is never read when a higher
// one says off.
func TestIAMEnforcePrecedence(t *testing.T) {
	f := files{config.DefaultConfigFile: `{"iam": {"enforce": "enforce"}}`}
	if got := load(t, []string{"--iam-enforce=off"}, []string{"UNDERSTUDY_IAM_ENFORCE=advise"}, f).IAM.Enforce; got != config.EnforceOff {
		t.Errorf("flag off over variable and file: iam.enforce = %v", got)
	}
	if got := load(t, nil, []string{"UNDERSTUDY_IAM_ENFORCE=off"}, f).IAM.Enforce; got != config.EnforceOff {
		t.Errorf("variable off over file: iam.enforce = %v", got)
	}
	if err := loadErr(t, nil, []string{"UNDERSTUDY_IAM_ENFORCE=advise"}, files{config.DefaultConfigFile: `{"iam": {"enforce": "off"}}`}); err.Where != "UNDERSTUDY_IAM_ENFORCE" {
		t.Errorf("variable advise over file off = %v, want refused at the variable", err)
	}
}

// The setting is listed for help and docs/configuration.md right after
// sigv4, the other request-checking mode, beside the file's iam section.
func TestIAMEnforceIsASetting(t *testing.T) {
	settings := config.Settings()
	at := slices.IndexFunc(settings, func(s config.Setting) bool { return s.Key == "iam.enforce" })
	if at < 0 {
		t.Fatal("config.Settings() has no iam.enforce")
	}
	s := settings[at]
	if s.Flag != "iam-enforce" || s.Env != "UNDERSTUDY_IAM_ENFORCE" || s.Value != "MODE" || s.Usage == "" {
		t.Errorf("iam.enforce setting = %+v", s)
	}
	if at == 0 || settings[at-1].Key != "sigv4" {
		t.Errorf("iam.enforce follows %q, want sigv4", settings[max(at-1, 0)].Key)
	}
}
