package distribution_test

import (
	"os"
	"regexp"
	"testing"
)

// A repository token in .goreleaser.yaml is rendered by GoReleaser's
// ApplySingleEnvOnly: no template functions, and the whole value must be one
// environment variable, matched by its envOnlyRe (internal/tmpl/tmpl.go,
// v2.19.0). Anything else fails the tagged run's publish step after the
// release and the image are public, and a snapshot never renders it: v0.1.0
// and v0.1.1 both stopped there.
func TestGoReleaserTokensAreASingleEnvironmentVariable(t *testing.T) {
	config, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tokens := regexp.MustCompile(`(?m)^\s*token:\s*(.*?)\s*$`).FindAllSubmatch(config, -1)
	if len(tokens) == 0 {
		t.Fatal("no token: line in .goreleaser.yaml; the cask's tap token is expected")
	}
	quoted := regexp.MustCompile(`^(?:"(.*)"|'(.*)')$`)
	envOnly := regexp.MustCompile(`^{{\s*\.Env\.[^.\s}]+\s*}}$`)
	for _, m := range tokens {
		value := string(m[1])
		if q := quoted.FindStringSubmatch(value); q != nil {
			value = q[1] + q[2]
		}
		if !envOnly.MatchString(value) {
			t.Errorf("token: %s is not a single {{ .Env.NAME }}, which GoReleaser refuses at publish", m[1])
		}
	}
}
