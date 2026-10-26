package policycatalogue_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

// Embedded decompresses the embedded policies.json.gz and serves every
// policy of the reviewable policies.json beside it, in its order, so the
// catalogue the binary carries is the one under review.
func TestEmbeddedServesTheWholeCatalogue(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(dataDir, awsmanagedpolicies.PoliciesFile))
	if err != nil {
		t.Fatal(err)
	}
	want, err := policycatalogue.Parse(data)
	if err != nil {
		t.Fatalf("Parse(%s) = %v", awsmanagedpolicies.PoliciesFile, err)
	}
	// An empty catalogue would make the comparison below vacuous.
	if want.Len() == 0 {
		t.Fatalf("%s holds no policy although %s records a run; restore %s with git checkout", awsmanagedpolicies.PoliciesFile, awsmanagedpolicies.SourceFile, dataDir)
	}
	got, err := policycatalogue.Embedded()
	if err != nil {
		t.Fatalf("Embedded = %v", err)
	}
	if got.Len() != want.Len() {
		t.Fatalf("Embedded serves %d policies, %s holds %d; re-run 'go run ./internal/cmd/managedpolicies -pack'", got.Len(), awsmanagedpolicies.PoliciesFile, want.Len())
	}
	if diff := cmp.Diff(slices.Collect(want.All()), slices.Collect(got.All())); diff != "" {
		t.Errorf("Embedded (-%s +embedded):\n%s", awsmanagedpolicies.PoliciesFile, diff)
	}
	again, err := policycatalogue.Embedded()
	if err != nil || again != got {
		t.Errorf("a second Embedded = %p, %v; want the first catalogue %p", again, err, got)
	}
}
