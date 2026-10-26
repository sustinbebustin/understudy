package ids

import (
	"regexp"
	"strings"
	"testing"
)

// Every character of [a-z0-9] turns up in API Gateway ids: the rejection
// sampling neither drops a character nor leaves one out of reach, as
// taking a byte mod 36 over a truncated range could.
func TestAPIGatewayCharsReachEveryCharacter(t *testing.T) {
	src := NewSeeded(11)
	seen := map[rune]bool{}
	for range 200 {
		for _, r := range string(src.APIGatewayAPIID()) + string(src.APIGatewayRouteID()) {
			seen[r] = true
		}
	}
	for _, r := range apigatewayAlphabet {
		if !seen[r] {
			t.Errorf("character %q never drawn in 200 API ids and 200 route ids", r)
		}
	}
	if len(seen) != len(apigatewayAlphabet) {
		t.Errorf("drew %d distinct characters, want the %d of %q", len(seen), len(apigatewayAlphabet), apigatewayAlphabet)
	}
}

// A random source, the default without --seed, mints the shapes a seeded
// one does (apigateway-http.md 2.5).
func TestRandomSourceAPIGatewayShapes(t *testing.T) {
	src := NewRandom()
	for name, c := range map[string]struct {
		id   string
		want *regexp.Regexp
	}{
		"api":         {string(src.APIGatewayAPIID()), regexp.MustCompile(`^[a-z0-9]{10}$`)},
		"route":       {string(src.APIGatewayRouteID()), regexp.MustCompile(`^[a-z0-9]{7}$`)},
		"integration": {string(src.APIGatewayIntegrationID()), regexp.MustCompile(`^[a-z0-9]{7}$`)},
		"deployment":  {string(src.APIGatewayDeploymentID()), regexp.MustCompile(`^[a-z0-9]{6}$`)},
	} {
		if !c.want.MatchString(c.id) {
			t.Errorf("%s id %q, want %s", name, c.id, c.want)
		}
	}
	if got := APIGatewayRequestIDFor(src.RequestID()); len(got) != 16 || !strings.HasSuffix(string(got), "=") {
		t.Errorf("APIGatewayRequestIDFor = %q, want 16 characters ending in =", got)
	}
}
