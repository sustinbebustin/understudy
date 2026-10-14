package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
	if strings.Contains(r.stdout, "journalNext") {
		t.Errorf("diff --normalize mentions journalNext, which it masks:\n%s", r.stdout)
	}
	// Without --normalize the manifest's journalNext differs, since the
	// CreateQueue between the saves was journaled, and so do the seeded id
	// streams' positions, since it minted ids; diff says why, once each.
	raw := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "diff", before, after)
	const why = "manifest.json /journalNext is the id the journal assigns next"
	const whyPositions = "manifest.json /positions are the seeded id streams' positions"
	if raw.code != admin.ExitOK || !strings.Contains(raw.stdout, "~ manifest.json /journalNext: ") || !strings.Contains(raw.stdout, "note: "+why) || !strings.Contains(raw.stdout, "--normalize") {
		t.Errorf("diff before after = %d:\n%s\nwant the journalNext difference and %q", raw.code, raw.stdout, "note: "+why)
	}
	if !strings.Contains(raw.stdout, "~ manifest.json /positions/") || strings.Count(raw.stdout, "note: "+whyPositions) != 1 {
		t.Errorf("diff before after under a seed:\n%s\nwant a position difference and one %q", raw.stdout, "note: "+whyPositions)
	}
	rawJSON := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "diff", before, after, "--output", "json")
	var notes struct {
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(rawJSON.stdout), &notes); rawJSON.code != admin.ExitOK || err != nil || len(notes.Notes) != 2 ||
		!strings.HasPrefix(notes.Notes[0], why) || !strings.HasPrefix(notes.Notes[1], whyPositions) {
		t.Errorf("diff before after --output json = %d %q (%v), want a note on journalNext, then one on the positions", rawJSON.code, rawJSON.stdout, err)
	}
	text := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "diff", after, after)
	if text.code != admin.ExitOK || text.stdout != "the archives are identical\n" {
		t.Errorf("diff after = %d %q", text.code, text.stdout)
	}
}

// snapshot save gzips an archive named .gz or .tgz, and refuses a name
// that contradicts --gzip=false rather than write a plain tar under it.
func TestSnapshotSaveGzipsByName(t *testing.T) {
	inst := start(t)
	dir := t.TempDir()
	for _, name := range []string{"state.tar.gz", "state.tgz"} {
		archive := filepath.Join(dir, name)
		if r := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "save", archive); r.code != admin.ExitOK {
			t.Fatalf("save %s: %d %s", name, r.code, r.stderr)
		}
		data, err := os.ReadFile(archive)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
			t.Errorf("snapshot save %s wrote %d bytes starting % x; want gzip", name, len(data), data[:min(4, len(data))])
		}
	}
	plain := filepath.Join(dir, "plain.tar.gz")
	r := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "save", plain, "--gzip=false")
	if _, err := os.Stat(plain); r.code != admin.ExitUsage || !strings.Contains(r.stderr, "--gzip=false") || err == nil {
		t.Errorf("snapshot save plain.tar.gz --gzip=false = %d, stderr %q, file stat %v; want 2 and no file", r.code, r.stderr, err)
	}
}

// snapshot inspect says in words what the archive holds, not bare values.
func TestSnapshotInspectLabelsItsFields(t *testing.T) {
	inst := start(t)
	archive := filepath.Join(t.TempDir(), "state.tar")
	if r := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "save", archive); r.code != admin.ExitOK {
		t.Fatalf("save: %d %s", r.code, r.stderr)
	}
	r := runCLI(t.Context(), inst.Endpoint(), nil, "snapshot", "inspect", archive)
	for _, want := range []string{"\nformat        archive format version ", "\nfault rules   ", "\njournal       not saved\n"} {
		if r.code != admin.ExitOK || !strings.Contains(r.stdout, want) {
			t.Errorf("snapshot inspect = %d, lacks %q:\n%s", r.code, want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "true") || strings.Contains(r.stdout, "false") {
		t.Errorf("snapshot inspect prints a bare boolean:\n%s", r.stdout)
	}
}

// snapshot save to a local file warns when the server cut the state
// without some effects of committed writes, as the path form does, and
// reports the count with --output json.
func TestSnapshotSaveWarnsOfPendingEffects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", admin.Tar)
		w.Header().Set(admin.PendingEffectsHeader, "2")
		if _, err := w.Write([]byte("archive")); err != nil {
			t.Errorf("writing the scripted archive: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	archive := filepath.Join(t.TempDir(), "state.tar")
	r := runCLI(t.Context(), srv.URL, nil, "snapshot", "save", archive)
	if want := "saved " + archive + " (7 bytes)\nwarning: 2 effect(s) of committed writes"; r.code != admin.ExitOK || !strings.HasPrefix(r.stdout, want) {
		t.Errorf("snapshot save with 2 effects pending = %d, stdout %q; want 0 and %q", r.code, r.stdout, want)
	}
	r = runCLI(t.Context(), srv.URL, nil, "snapshot", "save", archive, "--output", "json")
	var info struct {
		Bytes          int64 `json:"bytes"`
		PendingEffects int   `json:"pendingEffects"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &info); err != nil || info.Bytes != 7 || info.PendingEffects != 2 {
		t.Errorf("snapshot save --output json = %q (%v), want 7 bytes and 2 pending effects", r.stdout, err)
	}
}
