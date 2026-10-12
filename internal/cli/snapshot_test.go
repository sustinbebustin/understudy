package cli_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// snapshot diff names the queue a second save added, and --normalize still
// sees it while two saves of one state compare identical.
func TestSnapshotDiff(t *testing.T) {
	inst := start(t)
	dir := t.TempDir()
	before, after := filepath.Join(dir, "before.tar"), filepath.Join(dir, "after.tar")
	if r := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "save", before); r.code != admin.ExitOK {
		t.Fatalf("save: %d %s", r.code, r.stderr)
	}
	createQueue(t, inst.Endpoint(), "added")
	if r := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "save", after); r.code != admin.ExitOK {
		t.Fatalf("save: %d %s", r.code, r.stderr)
	}
	r := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "diff", before, after, "--normalize", "--output", "json")
	var diff struct {
		Identical   bool `json:"identical"`
		Differences []struct {
			Entry, After string
		} `json:"differences"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &diff); err != nil {
		t.Fatalf("diff = %d %q: %v", r.code, r.stdout, err)
	}
	if diff.Identical || !strings.Contains(r.stdout, "added") {
		t.Errorf("diff before after = %s, want the added queue", r.stdout)
	}
	text := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "diff", after, after)
	if text.code != admin.ExitOK || text.stdout != "the archives are identical\n" {
		t.Errorf("diff after = %d %q", text.code, text.stdout)
	}
}
