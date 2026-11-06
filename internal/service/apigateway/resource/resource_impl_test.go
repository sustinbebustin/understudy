package resource_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/apigateway/resource"
)

// A greedy part is the last part of a template only (apigateway-rest.md
// 3.2); a template with parts after one never matches, so a stored tree
// that breaks the rule cannot misroute.
func TestGreedyBeforeAnotherPartNeverMatches(t *testing.T) {
	rs := []resource.Resource{{ID: "bad", Path: "/{p+}/b", Methods: []string{http.MethodGet}}}
	for _, m := range []resource.Methods{{}, {ExplicitBeforeAny: true, HeadMatchesGet: true}} {
		for _, path := range []string{"/x", "/x/b", "/x/y/b"} {
			if got, ok := resource.Find(rs, http.MethodGet, path, m); ok {
				t.Errorf("Find(GET %s) = %+v, want no match", path, got)
			}
		}
	}
}
