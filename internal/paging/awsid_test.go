package paging_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/paging"
)

func self(s string) string { return s }

// The token is AWS's, as recorded in testdata/parity/apigatewayv2/APIGW2-03
// (14-get-apis-page.json): for a page ending at id 9ackjmy7v7,
// "aWQ9UzlhY2tqbXk3djc%3D". The next page starts after that id, whether the
// token comes back as sent or escaped once more, and a token that is not one
// is refused.
func TestAWSIDKeysetRecordedToken(t *testing.T) {
	items := []string{"zz", "9ackjmy7v7", "0a"}
	const recorded = "aWQ9UzlhY2tqbXk3djc%3D"
	first, ok := paging.AWSIDKeyset(items, self, 2, nil)
	if !ok || !slices.Equal(first.Items, []string{"0a", "9ackjmy7v7"}) || first.Next == nil || *first.Next != recorded || !first.Truncated {
		t.Fatalf("first page = %+v (ok %v), want [0a 9ackjmy7v7] with token %q", first, ok, recorded)
	}
	for _, in := range []string{recorded, "aWQ9UzlhY2tqbXk3djc="} {
		rest, ok := paging.AWSIDKeyset(items, self, 2, &in)
		if !ok || !slices.Equal(rest.Items, []string{"zz"}) || rest.Next != nil || rest.Truncated {
			t.Errorf("page after %q = %+v (ok %v), want [zz] and no token", in, rest, ok)
		}
	}
	for _, bad := range []string{"not a token!", "MQ"} {
		if _, ok := paging.AWSIDKeyset(items, self, 2, &bad); ok {
			t.Errorf("token %q was accepted", bad)
		}
	}
}
