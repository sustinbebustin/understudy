package config_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/config"
)

// dockerLabels adds labels to every container and network understudy
// creates, so a Testcontainers reaper selecting its session labels also
// removes understudy's children. Its value is a JSON object of string
// values; keys under understudy.dev/ are understudy's own and refused
// (testcontainers.md 9.3, 8.6).

const (
	labelsFlag = "--docker-labels"
	labelsEnv  = "UNDERSTUDY_DOCKER_LABELS"
	labelsKey  = "understudy.json: dockerLabels"
	// labelsFileString is the file key set to a JSON string holding the
	// object's text, the text the flag takes.
	labelsFileString = "understudy.json: dockerLabels (string)"
)

// labelSources sets dockerLabels from one source to text, the JSON object
// text the flag and the variable take.
var labelSources = map[string]func(text string) ([]string, []string, files){
	labelsFlag: func(text string) ([]string, []string, files) {
		return []string{labelsFlag, text}, nil, files{}
	},
	labelsEnv: func(text string) ([]string, []string, files) {
		return nil, []string{labelsEnv + "=" + text}, files{}
	},
	labelsKey: func(text string) ([]string, []string, files) {
		return nil, nil, files{config.DefaultConfigFile: `{"dockerLabels": ` + text + `}`}
	},
	labelsFileString: func(text string) ([]string, []string, files) {
		return nil, nil, files{config.DefaultConfigFile: `{"dockerLabels": ` + jsonString(text) + `}`}
	},
}

// where is the spelling a refusal from source names (config.Error.Where).
func where(source string) string {
	if source == labelsFileString {
		return labelsKey
	}
	return source
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// The file key is the flag name in camelCase (config.Setting); the
// variable and the file key are testcontainers.md 9.3's; the value is a
// JSON object, and help lists the setting beside --docker, the Engine the
// labels apply to.
func TestDockerLabelsSettingRow(t *testing.T) {
	settings := config.Settings()
	i := slices.IndexFunc(settings, func(s config.Setting) bool { return s.Key == "dockerLabels" })
	if i < 0 {
		t.Fatalf("Settings() has no dockerLabels row (testcontainers.md 9.3)")
	}
	s := settings[i]
	if s.Flag != "docker-labels" || s.Env != labelsEnv || s.Value != "JSON" {
		t.Errorf("dockerLabels row = %+v, want flag docker-labels, variable %s, value JSON", s, labelsEnv)
	}
	if i == 0 || settings[i-1].Key != "docker" {
		t.Errorf("dockerLabels is at %d, want it directly after docker", i)
	}
	for _, want := range []string{"understudy.dev/", "org.testcontainers"} {
		if !strings.Contains(s.Usage, want) {
			t.Errorf("dockerLabels usage %q does not mention %s", s.Usage, want)
		}
	}
}

func TestDockerLabelsFromEachSource(t *testing.T) {
	const text = `{"org.testcontainers.session-id": "6f1b8c2e-1d7a-4c39-9a51-0d2f3b4c5e6f", "com.example/team": "payments"}`
	want := map[string]string{"org.testcontainers.session-id": "6f1b8c2e-1d7a-4c39-9a51-0d2f3b4c5e6f", "com.example/team": "payments"}
	for name, source := range labelSources {
		args, env, f := source(text)
		if got := load(t, args, env, f).DockerLabels; !maps.Equal(got, want) {
			t.Errorf("%s: DockerLabels = %v, want %v", name, got, want)
		}
	}
}

// No source leaves the setting unset, an empty variable is unset as every
// variable is, and an empty object sets no label.
func TestDockerLabelsUnsetAndEmpty(t *testing.T) {
	if got := load(t, nil, nil, files{}).DockerLabels; got != nil {
		t.Errorf("DockerLabels with no source = %#v, want nil", got)
	}
	if got := load(t, nil, []string{labelsEnv + "="}, files{}).DockerLabels; got != nil {
		t.Errorf("DockerLabels with an empty %s = %#v, want nil", labelsEnv, got)
	}
	file := files{config.DefaultConfigFile: `{"dockerLabels": {"com.example/team": "payments"}}`}
	if got := load(t, nil, []string{labelsEnv + "="}, file).DockerLabels; !maps.Equal(got, map[string]string{"com.example/team": "payments"}) {
		t.Errorf("DockerLabels with an empty %s and a file = %v, want the file's", labelsEnv, got)
	}
	for name, source := range labelSources {
		args, env, f := source(`{}`)
		if got := load(t, args, env, f).DockerLabels; len(got) != 0 {
			t.Errorf("%s {}: DockerLabels = %v, want none", name, got)
		}
	}
}

// The highest source wins whole: flag over variable over file, never a
// merge of their maps (docs/configuration.md, precedence).
func TestDockerLabelsPrecedenceReplacesWhole(t *testing.T) {
	file := files{config.DefaultConfigFile: `{"dockerLabels": {"c": "3"}}`}
	env := []string{labelsEnv + `={"b": "2"}`}
	args := []string{labelsFlag, `{"a": "1"}`}
	cases := []struct {
		name string
		args []string
		env  []string
		want map[string]string
	}{
		{"flag over variable and file", args, env, map[string]string{"a": "1"}},
		{"variable over file", nil, env, map[string]string{"b": "2"}},
		{"file", nil, nil, map[string]string{"c": "3"}},
	}
	for _, c := range cases {
		if got := load(t, c.args, c.env, file).DockerLabels; !maps.Equal(got, c.want) {
			t.Errorf("%s: DockerLabels = %v, want %v", c.name, got, c.want)
		}
	}
}

// A key given twice keeps its last value, as encoding/json decodes an
// object, and keys under org.testcontainers, the setting's purpose, are
// accepted.
func TestDockerLabelsAcceptedKeys(t *testing.T) {
	cases := map[string]map[string]string{
		`{"a": "1", "a": "2"}`: {"a": "2"},
		`{"org.testcontainers": "true", "org.testcontainers.sessionId": "s", "org.testcontainers.reap": "true"}`: {
			"org.testcontainers": "true", "org.testcontainers.sessionId": "s", "org.testcontainers.reap": "true",
		},
		// The reserved prefix is understudy.dev/ with its slash, matched
		// case-sensitively (testcontainers.md 9.3).
		`{"understudy.dev": "x", "understudy.devx/a": "x", "Understudy.dev/a": "x", "io.understudy.dev/a": "x"}`: {
			"understudy.dev": "x", "understudy.devx/a": "x", "Understudy.dev/a": "x", "io.understudy.dev/a": "x",
		},
	}
	for text, want := range cases {
		for name, source := range labelSources {
			args, env, f := source(text)
			if got := load(t, args, env, f).DockerLabels; !maps.Equal(got, want) {
				t.Errorf("%s %s: DockerLabels = %v, want %v", name, text, got, want)
			}
		}
	}
}

// Each refusal names the setting as its source spells it, says what is
// wrong and how to write it. The reserved-key text is testcontainers.md
// 9.3's.
func TestDockerLabelsRefusals(t *testing.T) {
	const (
		shapeHint    = `write {"com.example.team": "payments"}`
		emptyHint    = `name every label, as in {"com.example.team": "payments"}`
		reservedHint = `choose a key outside understudy.dev/`
	)
	shape := func(text string) string {
		return strconv.Quote(text) + " is not a JSON object of label keys to string values"
	}
	reservedReason := func(key string) string {
		return fmt.Sprintf("label key %q is reserved for understudy's own labels", key)
	}
	notString := func(key string) (string, string) {
		return fmt.Sprintf("label %q has a value that is not a string", key), fmt.Sprintf(`quote it, as in {%q: "1"}`, key)
	}
	type refusal struct{ text, reason, hint string }
	cases := make([]refusal, 0, 20)
	for _, text := range []string{`[1]`, `nope`, `{"a": "1"`, `"x"`, `1`, `true`, `null`, `[{"a": "1"}]`} {
		cases = append(cases, refusal{text, shape(text), shapeHint})
	}
	cases = append(cases,
		refusal{`{"": "x"}`, "has an empty label key", emptyHint},
		// The empty key sorts first, so it is the first failure.
		refusal{`{"understudy.dev/x": "1", "": "1"}`, "has an empty label key", emptyHint},
		refusal{`{"understudy.dev/managed": "false"}`, reservedReason("understudy.dev/managed"), reservedHint},
		refusal{`{"understudy.dev/": "x"}`, reservedReason("understudy.dev/"), reservedHint},
		// Keys are walked in sorted order and the first failure is named.
		refusal{`{"understudy.dev/b": "1", "understudy.dev/a": "1"}`, reservedReason("understudy.dev/a"), reservedHint},
		// A reserved key is refused for its key before its value is read.
		refusal{`{"understudy.dev/x": 1}`, reservedReason("understudy.dev/x"), reservedHint},
	)
	for _, value := range []string{`1`, `true`, `null`, `{"b": "c"}`, `["x"]`} {
		reason, hint := notString("a")
		cases = append(cases, refusal{`{"a": ` + value + `}`, reason, hint})
	}
	reason, hint := notString("a")
	cases = append(cases, refusal{`{"b": 1, "a": false}`, reason, hint})

	for _, c := range cases {
		for name, source := range labelSources {
			if name == labelsKey && !json.Valid([]byte(c.text)) {
				// Malformed text as the key's value makes the whole file
				// malformed, which the file's own refusal covers.
				continue
			}
			args, env, f := source(c.text)
			_, err := config.Load(args, env, f.read)
			if err == nil {
				t.Errorf("%s %s: Load succeeded, want it refused", name, c.text)
				continue
			}
			wantReason := c.reason
			if strings.HasPrefix(name, "understudy.json") && strings.HasSuffix(c.reason, " is not a JSON object of label keys to string values") {
				// How the file's value is quoted is the file's own; only the
				// shape it needs is fixed.
				if err.Where != where(name) || err.Hint != c.hint || !strings.HasSuffix(err.Reason, " is not a JSON object of label keys to string values") {
					t.Errorf("%s %s: refused at %q: %q, hint %q; want at %q: <value> is not a JSON object..., hint %q", name, c.text, err.Where, err.Reason, err.Hint, where(name), c.hint)
				}
				continue
			}
			if err.Where != where(name) || err.Reason != wantReason || err.Hint != c.hint {
				t.Errorf("%s %s: refused at %q: %q, hint %q; want at %q: %q, hint %q", name, c.text, err.Where, err.Reason, err.Hint, where(name), wantReason, c.hint)
			}
		}
	}

	_, err := config.Load(nil, []string{labelsEnv + `={"understudy.dev/managed": "false"}`}, files{}.read)
	const rendered = `config: UNDERSTUDY_DOCKER_LABELS: label key "understudy.dev/managed" is reserved for understudy's own labels; choose a key outside understudy.dev/`
	if err == nil || err.Error() != rendered {
		t.Errorf("reserved key by variable = %v, want %q (testcontainers.md 9.3)", err, rendered)
	}
}

// Any value of the file key other than an object or a string holding one
// is refused like a bad text.
func TestDockerLabelsFileValueShapes(t *testing.T) {
	for _, value := range []string{`5`, `true`, `false`, `null`, `[]`, `["a"]`, `"[1]"`, `"nope"`, `""`} {
		f := files{config.DefaultConfigFile: `{"dockerLabels": ` + value + `}`}
		_, err := config.Load(nil, nil, f.read)
		if err == nil || err.Where != labelsKey || !strings.HasSuffix(err.Reason, " is not a JSON object of label keys to string values") ||
			err.Hint != `write {"com.example.team": "payments"}` {
			t.Errorf("dockerLabels %s = %v, want it refused at %s as not a JSON object", value, err, labelsKey)
		}
	}
}

// reservedFree draws a label key outside understudy.dev/.
var reservedFree = rapid.String().Filter(func(k string) bool { return k != "" && !strings.HasPrefix(k, "understudy.dev/") })

// Whatever source carries a valid label object, the configuration holds
// exactly its labels.
func TestDockerLabelsRoundTripFromAnySource(t *testing.T) {
	names := slices.Sorted(maps.Keys(labelSources))
	rapid.Check(t, func(t *rapid.T) {
		labels := rapid.MapOf(reservedFree, rapid.String()).Draw(t, "labels")
		text, err := json.Marshal(labels)
		if err != nil {
			t.Fatal(err)
		}
		name := rapid.SampledFrom(names).Draw(t, "source")
		args, env, f := labelSources[name](string(text))
		cfg, cerr := config.Load(args, env, f.read)
		if cerr != nil {
			t.Fatalf("%s %s: %v", name, text, cerr)
		}
		if len(labels) == 0 && len(cfg.DockerLabels) == 0 {
			return
		}
		if !maps.Equal(cfg.DockerLabels, labels) {
			t.Fatalf("%s %s: DockerLabels = %v, want %v", name, text, cfg.DockerLabels, labels)
		}
	})
}

// A valid object with any one key under understudy.dev/ added is refused
// naming that key.
func TestDockerLabelsRefuseAnyReservedKey(t *testing.T) {
	names := slices.Sorted(maps.Keys(labelSources))
	rapid.Check(t, func(t *rapid.T) {
		labels := rapid.MapOf(reservedFree, rapid.String()).Draw(t, "labels")
		key := "understudy.dev/" + rapid.StringMatching(`[a-z0-9.\-]{0,10}`).Draw(t, "suffix")
		labels[key] = rapid.String().Draw(t, "value")
		text, err := json.Marshal(labels)
		if err != nil {
			t.Fatal(err)
		}
		name := rapid.SampledFrom(names).Draw(t, "source")
		args, env, f := labelSources[name](string(text))
		_, cerr := config.Load(args, env, f.read)
		want := fmt.Sprintf("label key %q is reserved for understudy's own labels", key)
		if cerr == nil || cerr.Where != where(name) || cerr.Reason != want {
			t.Fatalf("%s %s = %v, want it refused at %s: %s", name, text, cerr, where(name), want)
		}
	})
}
