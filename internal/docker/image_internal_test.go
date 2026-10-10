package docker

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func referenceParts(t *rapid.T) (repo, tag string) {
	component := rapid.StringMatching(`[a-z0-9]+([._-][a-z0-9]+)*`)
	registry := rapid.OneOf(rapid.Just(""), rapid.StringMatching(`[a-z0-9]+(\.[a-z0-9]+)*(:[0-9]{2,5})?/`)).Draw(t, "registry")
	path := rapid.SliceOfN(component, 1, 3).Draw(t, "path")
	repo = registry + strings.Join(path, "/")
	return repo, rapid.StringMatching(`[A-Za-z0-9_][A-Za-z0-9_.-]{0,20}`).Draw(t, "tag")
}

func TestSplitReferenceSeparatesTheTag(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		repo, tag := referenceParts(t)
		if from, got := splitReference(repo + ":" + tag); from != repo || got != tag {
			t.Fatalf("splitReference(%q) = %q, %q; want %q, %q", repo+":"+tag, from, got, repo, tag)
		}
		if from, got := splitReference(repo); from != repo || got != "latest" {
			t.Fatalf("splitReference(%q) = %q, %q; want the repository with latest", repo, from, got)
		}
	})
}

func TestSplitReferenceSendsDigestsWhole(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		repo, _ := referenceParts(t)
		ref := repo + "@sha256:" + rapid.StringMatching(`[0-9a-f]{64}`).Draw(t, "digest")
		if from, tag := splitReference(ref); from != ref || tag != "" {
			t.Fatalf("splitReference(%q) = %q, %q; want the whole reference and no tag", ref, from, tag)
		}
	})
}
