package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"pgregory.net/rapid"
)

func TestGitBlobSHA1MatchesGitHashObject(t *testing.T) {
	// printf 'hello\n' | git hash-object --stdin
	if got, want := gitBlobSHA1([]byte("hello\n")), "ce013625030ba8dba906f756967f9e9ca394464a"; got != want {
		t.Fatalf("gitBlobSHA1 = %s, want %s", got, want)
	}
	// git hash-object of an empty file.
	if got, want := gitBlobSHA1(nil), "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"; got != want {
		t.Fatalf("gitBlobSHA1(empty) = %s, want %s", got, want)
	}
}

// upstreamFixture is a pinned tree whose files hold their own path as
// content.
func upstreamFixture() map[string][]byte {
	files := map[string][]byte{}
	for _, p := range legalPaths() {
		files[p] = []byte(p + " text\n")
	}
	for _, m := range pinnedModels() {
		files[m.Path] = []byte(`{"smithy":"2.0","shapes":{}}` + m.Dir)
	}
	return files
}

func fixtureFS(t *testing.T, files map[string][]byte) fstest.MapFS {
	t.Helper()
	m := Manifest{Repository: upstreamRepo, Commit: pinnedCommit, Retrieved: "2026-10-08", License: upstreamSPDX}
	fsys := fstest.MapFS{}
	for _, p := range legalPaths() {
		m.Legal = append(m.Legal, record("", p, files[p]))
		fsys[p] = &fstest.MapFile{Data: files[p]}
	}
	for _, md := range pinnedModels() {
		m.Files = append(m.Files, record(md.Dir, md.Path, files[md.Path]))
		fsys[md.Path] = &fstest.MapFile{Data: files[md.Path]}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	fsys[manifestName] = &fstest.MapFile{Data: raw}
	return fsys
}

func TestVerifyAcceptsTheRecordedTree(t *testing.T) {
	if err := verify(fixtureFS(t, upstreamFixture()), "fixture"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyReportsEveryAlteration(t *testing.T) {
	sqs := pinnedModels()[10].Path
	tests := []struct {
		name   string
		mutate func(fstest.MapFS)
		want   []string
	}{
		{
			name: "a changed byte",
			mutate: func(f fstest.MapFS) {
				f[sqs] = &fstest.MapFile{Data: []byte(`{"smithy":"2.0","shapes":{}}sqX`)}
			},
			want: []string{sqs + ": has git blob SHA-1", sqs + ": has SHA-256"},
		},
		{
			name:   "a missing file",
			mutate: func(f fstest.MapFS) { delete(f, "NOTICE") },
			want:   []string{"NOTICE: cannot be read"},
		},
		{
			name: "an unrecorded model",
			mutate: func(f fstest.MapFS) {
				f["models/extra/service/1/extra-1.json"] = &fstest.MapFile{Data: []byte("{}")}
			},
			want: []string{"models/extra/service/1/extra-1.json: is not recorded"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fixtureFS(t, upstreamFixture())
			tt.mutate(fsys)
			err := verify(fsys, "fixture")
			ve, ok := errors.AsType[*VerifyError](err)
			if !ok || len(ve.Problems) == 0 {
				t.Fatalf("verify = %v, want *VerifyError with problems", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error lacks %q:\n%s", w, err)
				}
			}
		})
	}
}

func TestVerifyRejectsAnotherCommit(t *testing.T) {
	fsys := fixtureFS(t, upstreamFixture())
	var m Manifest
	if err := json.Unmarshal(fsys[manifestName].Data, &m); err != nil {
		t.Fatal(err)
	}
	m.Commit = strings.Repeat("0", 40)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	fsys[manifestName] = &fstest.MapFile{Data: raw}
	if err := verify(fsys, "fixture"); err == nil || !strings.Contains(err.Error(), "but the pin is "+pinnedCommit) {
		t.Fatalf("verify = %v, want a pin mismatch", err)
	}
}

// TestVerifyDetectsAnySingleByteChange is the property behind -verify: no
// one-byte edit of a vendored file goes unnoticed.
func TestVerifyDetectsAnySingleByteChange(t *testing.T) {
	files := upstreamFixture()
	paths := make([]string, 0, len(files))
	for _, m := range pinnedModels() {
		paths = append(paths, m.Path)
	}
	base := fixtureFS(t, files)
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.SampledFrom(paths).Draw(t, "path")
		content := append([]byte(nil), files[p]...)
		i := rapid.IntRange(0, len(content)-1).Draw(t, "offset")
		delta := rapid.ByteRange(1, 255).Draw(t, "delta")
		fsys := maps.Clone(base)
		content[i] += delta
		fsys[p] = &fstest.MapFile{Data: content}
		if err := verify(fsys, "fixture"); err == nil {
			t.Fatalf("a change at %s[%d] passed verify", p, i)
		}
	})
}

// fakeGitHub serves a tree and raw files from files; blobSHA overrides the
// tree's SHA for one path.
func fakeGitHub(t *testing.T, files map[string][]byte, truncated bool, blobSHA map[string]string) fetcher {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tree", func(w http.ResponseWriter, _ *http.Request) {
		tr := tree{SHA: pinnedCommit, Truncated: truncated}
		for p, c := range files {
			sha := gitBlobSHA1(c)
			if o, ok := blobSHA[p]; ok {
				sha = o
			}
			tr.Tree = append(tr.Tree, treeEntry{Path: p, Type: "blob", SHA: sha})
		}
		if err := json.NewEncoder(w).Encode(tr); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("GET /raw/{path...}", func(w http.ResponseWriter, r *http.Request) {
		c, ok := files[r.PathValue("path")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write(c); err != nil {
			t.Error(err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return fetcher{
		client:  srv.Client(),
		treeURL: srv.URL + "/tree",
		rawURL:  func(p string) string { return srv.URL + "/raw/" + p },
		today:   func() string { return "2026-10-08" },
	}
}

func TestFetchWritesVerbatimFilesAndAManifestThatVerifies(t *testing.T) {
	files := upstreamFixture()
	dir := t.TempDir()
	if err := fakeGitHub(t, files, false, nil).fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	for p, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	if err := verify(os.DirFS(dir), dir); err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 12 || m.Retrieved != "2026-10-08" || m.License != "Apache-2.0" {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestFetchRefusesABlobThatDoesNotMatchTheTree(t *testing.T) {
	files := upstreamFixture()
	sqs := pinnedModels()[10].Path
	dir := t.TempDir()
	err := fakeGitHub(t, files, false, map[string]string{sqs: strings.Repeat("a", 40)}).fetch(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "git blob SHA-1") {
		t.Fatalf("fetch = %v, want a blob mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(dir, manifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed fetch wrote %s", manifestName)
	}
}

func TestFetchRefusesATruncatedTree(t *testing.T) {
	err := fakeGitHub(t, upstreamFixture(), true, nil).fetch(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("fetch = %v, want a truncation refusal", err)
	}
}

func TestRunNeedsExactlyOneMode(t *testing.T) {
	for _, args := range [][]string{nil, {"-fetch", "-verify"}} {
		if err := run(context.Background(), args, os.Stderr); !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want errUsage", args, err)
		}
	}
}

// TestVendoredModelsVerify is the -verify run over the checked-in tree.
func TestVendoredModelsVerify(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "third_party", "api-models-aws")
	if err := verify(os.DirFS(dir), dir); err != nil {
		t.Fatal(err)
	}
}
