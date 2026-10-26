package server

import (
	"io"
	"io/fs"
	"net/url"
	"regexp"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

// The served IAM lists exactly the embedded catalogue under Scope AWS, in
// its order, so the catalogue the binary carries is the one it serves.
func TestIAMServesTheEmbeddedCatalogue(t *testing.T) {
	cat, err := policycatalogue.Embedded()
	if err != nil {
		t.Fatalf("policycatalogue.Embedded = %v", err)
	}
	var want []string
	for p := range cat.All() {
		want = append(want, p.ARN)
	}
	// Until the maintainer's first -fetch is committed, policies.json is the
	// empty catalogue and embedded alone. Once SOURCE.json is embedded beside
	// it, an empty catalogue would make the comparison below vacuous.
	if _, err := fs.Stat(awsmanagedpolicies.FS(), awsmanagedpolicies.SourceFile); err == nil && len(want) == 0 {
		t.Fatalf("the embedded catalogue is empty although %s is embedded; restore third_party/aws-managed-policies with git checkout", awsmanagedpolicies.SourceFile)
	}
	srv, err := New(Config{Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	root := auth.Signer{AccessKeyID: keyAccount, SecretAccessKey: "test"}
	arns := regexp.MustCompile(`<Arn>([^<]*)</Arn>`)
	marker := regexp.MustCompile(`<Marker>([^<]*)</Marker>`)
	form := url.Values{"Action": {"ListPolicies"}, "Scope": {"AWS"}, "MaxItems": {"1000"}}
	var got []string
	exhausted := false
	for range 10 {
		body := queryCall(t, srv, "iam", root, form)
		for _, m := range arns.FindAllStringSubmatch(body, -1) {
			got = append(got, m[1])
		}
		next := marker.FindStringSubmatch(body)
		if next == nil {
			exhausted = true
			break
		}
		form.Set("Marker", next[1])
	}
	if !exhausted {
		t.Fatalf("ListPolicies(Scope AWS) still had a Marker after 10 pages of 1000 (%d policies listed); the listing was cut short", len(got))
	}
	if !slices.Equal(got, want) {
		t.Errorf("ListPolicies(Scope AWS) listed %d policies, want the embedded catalogue's %d in its order", len(got), len(want))
	}
}
