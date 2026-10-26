package ui_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The bun that 'just ui' requires is the one CI's ui job installs, and that
// job runs 'just ui-check', so the build a contributor commits is the build
// CI rebuilds and compares.
func TestBunPinMatchesCI(t *testing.T) {
	justfile, err := os.ReadFile("../../../justfile")
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`(?m)^bun_version := "([^"]+)"$`).FindSubmatch(justfile)
	if pin == nil {
		t.Fatal(`justfile declares no bun_version := "<version>", which 'just ui' checks bun against`)
	}
	want := string(pin[1])

	ci, err := os.ReadFile("../../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	job, ok := ciJob(string(ci), "ui")
	if !ok {
		t.Fatal(".github/workflows/ci.yml has no ui job; add one that installs bun " + want + " and runs 'just ui-check'")
	}
	pins := regexp.MustCompile(`(?m)^\s+bun-version: "([^"]+)"$`).FindAllStringSubmatch(job, -1)
	versions := make([]string, 0, len(pins))
	for _, m := range pins {
		versions = append(versions, m[1])
	}
	if len(versions) != 1 || versions[0] != want {
		t.Errorf("CI's ui job pins bun-version %q, want exactly one pin equal to the justfile's bun_version %q", versions, want)
	}
	if !regexp.MustCompile(`(?m)^\s+- run: just ui-check$`).MatchString(job) {
		t.Errorf("CI's ui job does not run 'just ui-check':\n%s", job)
	}
}

// ciJob returns the lines of the workflow's job named name: from its
// two-space-indented key up to the next job's key.
func ciJob(workflow, name string) (string, bool) {
	var job []string
	in := false
	for line := range strings.SplitSeq(workflow, "\n") {
		key := strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(line, ":")
		switch {
		case key && line == "  "+name+":":
			in = true
		case key && in:
			return strings.Join(job, "\n"), true
		case in:
			job = append(job, line)
		}
	}
	return strings.Join(job, "\n"), in
}
