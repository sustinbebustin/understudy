package config_test

import (
	"maps"
	"testing"

	"github.com/sustinbebustin/understudy/internal/config"
)

// Every check runs on one key before the next key in sorted order is read,
// so the first key's failure is the one named, whatever the later keys
// hold (testcontainers.md 9.3).
func TestDockerLabelsReportTheFirstKeysFailure(t *testing.T) {
	cases := map[string]string{
		`{"a": 1, "understudy.dev/b": "x"}`:  `label "a" has a value that is not a string`,
		`{"b": "1", "understudy.dev/a": 1}`:  `label key "understudy.dev/a" is reserved for understudy's own labels`,
		`{"z": null, "understudy.dev/": ""}`: `label key "understudy.dev/" is reserved for understudy's own labels`,
	}
	for text, want := range cases {
		_, err := config.Load([]string{"--docker-labels", text}, nil, files{}.read)
		if err == nil || err.Where != "--docker-labels" || err.Reason != want {
			t.Errorf("--docker-labels %s = %v, want refused: %s", text, err, want)
		}
	}
}

// The file's string form is the flag's text, whitespace and all, and the
// object form may span lines; both give the same labels.
func TestDockerLabelsFileForms(t *testing.T) {
	want := map[string]string{"com.example/team": "payments", "org.testcontainers": "true"}
	for _, doc := range []string{
		"{\"dockerLabels\":  \" {\\\"com.example/team\\\": \\\"payments\\\", \\\"org.testcontainers\\\": \\\"true\\\"} \" }",
		"{\n  \"dockerLabels\": {\n    \"com.example/team\": \"payments\",\n    \"org.testcontainers\": \"true\"\n  }\n}\n",
	} {
		if got := load(t, nil, nil, files{config.DefaultConfigFile: doc}).DockerLabels; !maps.Equal(got, want) {
			t.Errorf("file %s: DockerLabels = %v, want %v", doc, got, want)
		}
	}
}

// Client commands load the same settings, so doctor and cleanup refuse a
// reserved key as serve does.
func TestLoadHostRefusesAReservedDockerLabel(t *testing.T) {
	_, err := config.LoadHost(nil, []string{`UNDERSTUDY_DOCKER_LABELS={"understudy.dev/owner": "x"}`}, files{}.read)
	const want = `config: UNDERSTUDY_DOCKER_LABELS: label key "understudy.dev/owner" is reserved for understudy's own labels; choose a key outside understudy.dev/`
	if err == nil || err.Error() != want {
		t.Errorf("LoadHost = %v, want %q", err, want)
	}
}
