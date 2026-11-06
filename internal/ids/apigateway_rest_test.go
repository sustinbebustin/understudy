package ids_test

import (
	"fmt"
	"regexp"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/ids"
)

var (
	// rootResourceID is a REST API root resource id: 10 lower-case
	// alphanumerics, as svzr2028x8 and 3kzxbg5sa2 (apigateway-rest.md 3.10,
	// documentation examples; inferred, APIGW1-02).
	rootResourceID = regexp.MustCompile(`^[a-z0-9]{10}$`)
	// resourceID is a child resource id: 6 lower-case alphanumerics, as
	// 6sxz2j, rjkmth, xdsvhp, x3o5ih (apigateway-rest.md 3.10; inferred,
	// APIGW1-02).
	resourceID = regexp.MustCompile(`^[a-z0-9]{6}$`)
)

// restDraws are the REST resource minting calls in one order.
func restDraws(src *ids.Source) []string {
	return []string{string(src.APIGatewayRootResourceID()), string(src.APIGatewayResourceID())}
}

// A seeded source repeats its REST resource ids (ADR 0018).
func TestSeededRESTResourceIDsRepeat(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Uint64().Draw(t, "seed")
		n := rapid.IntRange(1, 20).Draw(t, "rounds")
		a, b := ids.NewSeeded(seed), ids.NewSeeded(seed)
		for i := range n {
			x, y := restDraws(a), restDraws(b)
			if fmt.Sprint(x) != fmt.Sprint(y) {
				t.Fatalf("round %d with seed %d: %v and %v", i, seed, x, y)
			}
		}
	})
}

// Root and child resource ids have the documentation examples' shapes
// (apigateway-rest.md 3.10).
func TestRESTResourceIDShapes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := ids.NewSeeded(rapid.Uint64().Draw(t, "seed"))
		if id := src.APIGatewayRootResourceID(); !rootResourceID.MatchString(string(id)) {
			t.Fatalf("root resource id %q, want 10 of [a-z0-9]", id)
		}
		if id := src.APIGatewayResourceID(); !resourceID.MatchString(string(id)) {
			t.Fatalf("resource id %q, want 6 of [a-z0-9]", id)
		}
	})
}

// A thousand resource ids from one source are distinct.
func TestRESTResourceIDsAreDistinct(t *testing.T) {
	src := ids.NewSeeded(1)
	seen := map[ids.APIGatewayChildID]int{}
	for i := range 1000 {
		id := src.APIGatewayResourceID()
		if j, dup := seen[id]; dup {
			t.Fatalf("draw %d repeats draw %d: %q", i, j, id)
		}
		seen[id] = i
	}
}

// Resource ids draw from the child stream, never the API id stream, so
// the execute-api namespace stays one stream (ADR 0018).
func TestRESTResourceIDsDrawTheChildStream(t *testing.T) {
	src := ids.NewSeeded(7)
	src.APIGatewayRootResourceID()
	src.APIGatewayResourceID()
	pos, err := src.Positions()
	if err != nil {
		t.Fatal(err)
	}
	if pos[ids.KindAPIGatewayChildID] == 0 || pos[ids.KindAPIGatewayAPIID] != 0 {
		t.Errorf("after two resource ids: positions api %d, child %d; want api 0, child > 0", pos[ids.KindAPIGatewayAPIID], pos[ids.KindAPIGatewayChildID])
	}
}
