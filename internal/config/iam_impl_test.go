package config_test

import (
	"encoding/json"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/config"
)

// Every text but off, advise and enforce is refused from each source with
// the same reason, so no other spelling is read as a mode
// (iam-enforcement-surfaces.md 12.1).
func TestIAMEnforceRefusesEveryOtherText(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// A variable set to the empty string is unset, so only non-empty
		// text is drawn.
		v := rapid.String().Filter(func(s string) bool { return s != "" && s != "off" && s != "advise" && s != "enforce" }).Draw(t, "value")
		text, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		sources := []struct {
			where string
			args  []string
			env   []string
			f     files
		}{
			{"--iam-enforce", []string{"--iam-enforce=" + v}, nil, files{}},
			{"UNDERSTUDY_IAM_ENFORCE", nil, []string{"UNDERSTUDY_IAM_ENFORCE=" + v}, files{}},
			{config.DefaultConfigFile + ": iam.enforce", nil, nil, files{config.DefaultConfigFile: `{"iam": {"enforce": ` + string(text) + `}}`}},
		}
		for _, s := range sources {
			_, lerr := config.Load(s.args, s.env, s.f.read)
			reason := `"` + v + `" is not off, advise or enforce`
			if lerr == nil || lerr.Where != s.where || lerr.Reason != reason {
				t.Fatalf("%s %q: Load = %v, want refused at %q: %q", s.where, v, lerr, s.where, reason)
			}
		}
	})
}

// The setting's file form is its iam section member. The dotted key at the
// top level is refused with the form it takes, not read as a second
// spelling, and a typo of it there is told that dotted name.
func TestIAMEnforceFileForm(t *testing.T) {
	f := files{config.DefaultConfigFile: `{"iam": {"assumeRole": "lenient", "enforce": "off"}}`}
	cfg := load(t, nil, nil, f)
	if cfg.IAM.Enforce != config.EnforceOff || cfg.IAM.AssumeRole != config.AssumeRoleLenient {
		t.Errorf("iam section = %+v, want EnforceOff beside AssumeRoleLenient", cfg.IAM)
	}

	cases := []struct {
		name, file string
		want       config.Error
	}{
		{
			"top-level dotted key", `{"iam.enforce": "off"}`,
			config.Error{Where: config.DefaultConfigFile + ": iam.enforce", Reason: `is not a top-level key; the setting is "enforce" in the "iam" section`, Hint: `write it as {"iam": {"enforce": ...}}`},
		},
		{
			"typo at the top level", `{"iam.enforc": "off"}`,
			config.Error{Where: config.DefaultConfigFile + ": iam.enforc", Reason: "is not a configuration key", Closest: "iam.enforce", Hint: "fix or remove the key"},
		},
		{
			"typo in the section", `{"iam": {"enforc": "off"}}`,
			config.Error{Where: config.DefaultConfigFile + ": iam.enforc", Reason: "is not a configuration key", Closest: "enforce", Hint: "fix or remove the key"},
		},
		{
			"not a string", `{"iam": {"enforce": false}}`,
			config.Error{Where: config.DefaultConfigFile + ": iam.enforce", Reason: "is a JSON bool; expected a string"},
		},
	}
	for _, c := range cases {
		err := loadErr(t, nil, nil, files{config.DefaultConfigFile: c.file})
		if *err != c.want {
			t.Errorf("%s: %s refused as %+v, want %+v", c.name, c.file, *err, c.want)
		}
	}
}
