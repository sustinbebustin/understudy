package ref_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The default filesystems of macOS and Windows ignore letter case, so a
// clone there holds only one of two tracked paths that differ only in case
// and starts with a dirty working tree.

func TestTrackedPathsDifferInMoreThanCase(t *testing.T) {
	root, _ := moduleRoot(t)
	cmd := exec.CommandContext(t.Context(), "git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files in %s: %v; this test reads the tracked paths, so run it in a git checkout with git on PATH", root, err)
	}
	paths := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	for _, c := range caseCollisions(paths) {
		t.Error(c)
	}
}

func TestCaseCollisionsNamesEveryPath(t *testing.T) {
	paths := []string{
		"testdata/a/bool-TRUE.json",
		"testdata/a/bool-True.json",
		"testdata/a/bool-1.json",
		"docs/README.md",
		"docs/readme.md",
		"docs/ReadMe.md",
		"Docs/other.md",
		"docs/unique.md",
	}
	want := []string{
		"docs/README.md, docs/ReadMe.md and docs/readme.md differ only in letter case; a case-insensitive filesystem (the macOS and Windows default) checks out only one of them, so rename all but one",
		"testdata/a/bool-TRUE.json and testdata/a/bool-True.json differ only in letter case; a case-insensitive filesystem (the macOS and Windows default) checks out only one of them, so rename all but one",
	}
	if got := caseCollisions(paths); !slices.Equal(got, want) {
		t.Errorf("caseCollisions =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// caseCollisions returns one message, sorted, per set of paths that are
// equal once letter case is folded. Paths that share only a directory
// spelled in two cases are not reported: their files merge into one
// directory on checkout and none is lost.
func caseCollisions(paths []string) []string {
	byFolded := map[string][]string{}
	for _, p := range paths {
		k := strings.ToLower(p)
		byFolded[k] = append(byFolded[k], p)
	}
	var msgs []string
	for _, group := range byFolded {
		if len(group) < 2 {
			continue
		}
		slices.Sort(group)
		names := strings.Join(group[:len(group)-1], ", ") + " and " + group[len(group)-1]
		msgs = append(msgs, names+" differ only in letter case; a case-insensitive filesystem (the macOS and Windows default) checks out only one of them, so rename all but one")
	}
	slices.Sort(msgs)
	return msgs
}
