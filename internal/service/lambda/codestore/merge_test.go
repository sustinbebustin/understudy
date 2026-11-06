package codestore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
)

// contextTB is a *testing.T or a *rapid.T.
type contextTB interface {
	rapid.TB
	Context() context.Context
}

// put adds a layer archive of entries to s and returns its digest.
func put(t contextTB, s *codestore.Store, entries ...entry) codestore.Digest {
	t.Helper()
	code, err := s.PutZip(t.Context(), bytes.NewReader(buildZip(t, entries...)), codestore.Options{})
	if err != nil {
		t.Fatalf("PutZip: %v", err)
	}
	return code.Sha256
}

// mergeKey is SHA-256 over the digests concatenated in order.
func mergeKey(layers ...codestore.Digest) codestore.Digest {
	h := sha256.New()
	for _, d := range layers {
		h.Write(d[:])
	}
	return codestore.Digest(h.Sum(nil))
}

// files returns every regular file under dir by slash-separated path, with
// its content and permission bits.
func files(t rapid.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("opening %s: %v", dir, err)
	}
	defer root.Close()
	tree := root.FS()
	err = fs.WalkDir(tree, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(tree, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = info.Mode().Perm().String() + " " + string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return out
}

// mergeConflictSwitch is the value of the Lambda switch
// lambda.layers.merge-conflict: which layer wins a path two layers hold is
// unstated, and "later overwrites earlier" is the reading of "merge order"
// (lambda-destinations-layers.md 8.1, 11.2, LDL-12).
func mergeConflictSwitch(t *testing.T) string {
	t.Helper()
	value := ""
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory("", 0)
		if err != nil {
			k.Fatal(err)
		}
		defer func() {
			if err := blobs.Close(); err != nil {
				k.Error(err)
			}
		}()
		deps := k.Deps(lambdaapi.Service())
		deps.Blob = blobs
		for _, e := range lambda.New(deps, lambda.Options{}).Switches() {
			if e.Name == "lambda.layers.merge-conflict" {
				value = e.Value
			}
		}
	}, kerneltest.Services(lambdaapi.Service()))
	if value == "" {
		t.Fatal("the Lambda service has no switch lambda.layers.merge-conflict (lambda-destinations-layers.md 11.2)")
	}
	return value
}

// Lambda merges layer content into /opt in the order the function names the
// layers (lambda-destinations-layers.md 8.1): every layer's files land in
// one tree, with the modes the layers' trees have, and a path two layers
// hold follows the switch lambda.layers.merge-conflict.
func TestMergeFillsOneTreeInLayerOrder(t *testing.T) {
	winner := map[string]string{"later-overwrites": "from b"}[mergeConflictSwitch(t)]
	if winner == "" {
		t.Fatal("lambda.layers.merge-conflict has a value this test does not know")
	}
	s := newStore(t)
	a := put(t, s,
		entry{name: "python/a.py", body: "A = 1\n"},
		entry{name: "shared.txt", body: "from a"},
		entry{name: "bin/tool", body: "#!/bin/sh\n", mode: 0o700},
	)
	b := put(t, s,
		entry{name: "python/b.py", body: "B = 2\n"},
		entry{name: "shared.txt", body: "from b"},
	)
	m, err := s.Merge(t.Context(), []codestore.Digest{a, b})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if m.Key != mergeKey(a, b) {
		t.Errorf("Key = %s, want SHA-256 over the digests in order, %s", m.Key, mergeKey(a, b))
	}
	if !filepath.IsAbs(m.Dir) {
		t.Errorf("Dir = %q, want an absolute path a bind mount can use", m.Dir)
	}
	want := map[string]string{
		"python/a.py": "-rw-r--r-- A = 1\n",
		"python/b.py": "-rw-r--r-- B = 2\n",
		"shared.txt":  "-rw-r--r-- " + winner,
		"bin/tool":    "-rwxr-xr-x #!/bin/sh\n",
	}
	if got := files(t, m.Dir); !maps.Equal(got, want) {
		t.Errorf("merged tree = %v, want %v", got, want)
	}
	if info, err := os.Stat(filepath.Join(m.Dir, "python")); err != nil || info.Mode()&(fs.ModeDir|fs.ModePerm) != fs.ModeDir|0o755 {
		t.Errorf("python/ = %v, %v; want a 0755 directory", info, err)
	}
}

// The same layers in the same order share one tree and one reference
// count; another order is another tree.
func TestMergeIsContentAddressedAndReferenceCounted(t *testing.T) {
	s := newStore(t)
	a := put(t, s, entry{name: "shared.txt", body: "from a"})
	b := put(t, s, entry{name: "shared.txt", body: "from b"})
	first, err := s.Merge(t.Context(), []codestore.Digest{a, b})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Merge(t.Context(), []codestore.Digest{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("second Merge of the same layers = %+v, want the same tree %+v", second, first)
	}
	reversed, err := s.Merge(t.Context(), []codestore.Digest{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if reversed.Key == first.Key || reversed.Dir == first.Dir {
		t.Fatalf("Merge of the layers reversed = %+v, want another tree than %+v", reversed, first)
	}
	if err := s.ReleaseMerged(first.Key); err != nil {
		t.Fatalf("first ReleaseMerged: %v", err)
	}
	if _, err := os.Stat(first.Dir); err != nil {
		t.Fatalf("merged tree removed while a reference remains: %v", err)
	}
	if err := s.ReleaseMerged(first.Key); err != nil {
		t.Fatalf("second ReleaseMerged: %v", err)
	}
	if _, err := os.Stat(first.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("merged tree after the last ReleaseMerged: %v, want it removed", err)
	}
	if err := s.ReleaseMerged(first.Key); err == nil {
		t.Error("ReleaseMerged of a released tree succeeded")
	}
	if _, err := os.Stat(reversed.Dir); err != nil {
		t.Errorf("the reversed tree went with the other: %v", err)
	}
	if err := s.ReleaseMerged(reversed.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Trees(); err != nil {
		t.Errorf("after the merges: %v", err)
	}
}

// A merged tree is a copy: releasing a layer's own tree leaves it whole,
// as a function keeps working after its layer version is deleted
// (lambda-destinations-layers.md 7.2).
func TestMergedTreeOutlivesItsLayers(t *testing.T) {
	s := newStore(t)
	a := put(t, s, entry{name: "nodejs/node_modules/greet/index.js", body: "module.exports = 'hi'\n"})
	m, err := s.Merge(t.Context(), []codestore.Digest{a})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Release(a); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(m.Dir, "nodejs", "node_modules", "greet", "index.js"))
	if err != nil || string(got) != "module.exports = 'hi'\n" {
		t.Errorf("merged file after its layer's release = %q, %v", got, err)
	}
}

// Merge needs every layer's tree in the store, and at least one layer.
func TestMergeRefusesWhatItCannotMerge(t *testing.T) {
	s := newStore(t)
	a := put(t, s, entry{name: "a.txt", body: "a"})
	missing := codestore.Digest(sha256.Sum256([]byte("never added")))
	_, err := s.Merge(t.Context(), []codestore.Digest{a, missing})
	if nf, ok := errors.AsType[*codestore.NotFoundError](err); !ok || nf.Sha256 != missing {
		t.Errorf("Merge with a layer never added = %v, want a *NotFoundError for it", err)
	}
	_, err = s.Merge(t.Context(), nil)
	if me, ok := errors.AsType[*codestore.MergeError](err); !ok || me.Reason != "no layers" {
		t.Errorf("Merge of no layers = %v, want a *MergeError saying no layers", err)
	}
	if _, err := s.Trees(); err != nil {
		t.Errorf("after the refusals: %v", err)
	}
}

// A file in one layer where an earlier layer has a directory, or the
// reverse, cannot be merged: the error names the path and the later
// layer, and nothing is published.
func TestMergeRefusesAFileWhereADirectoryWas(t *testing.T) {
	s := newStore(t)
	dir := put(t, s, entry{name: "python/mod.py", body: "x = 1\n"})
	file := put(t, s, entry{name: "python", body: "not a directory"})
	cases := []struct {
		layers []codestore.Digest
		want   codestore.MergeConflictError
		text   string
	}{
		{
			[]codestore.Digest{dir, file},
			codestore.MergeConflictError{Path: "python", Layer: 1, File: true},
			"codestore: layer 1 has a file at python where an earlier layer has a directory; the layers cannot be merged into /opt",
		},
		{
			[]codestore.Digest{file, dir},
			codestore.MergeConflictError{Path: "python", Layer: 1, File: false},
			"codestore: layer 1 has a directory at python where an earlier layer has a file; the layers cannot be merged into /opt",
		},
	}
	for _, c := range cases {
		_, err := s.Merge(t.Context(), c.layers)
		mc, ok := errors.AsType[*codestore.MergeConflictError](err)
		if !ok || *mc != c.want || err.Error() != c.text {
			t.Errorf("Merge = %v, want %q", err, c.text)
		}
		if err := s.ReleaseMerged(mergeKey(c.layers...)); err == nil {
			t.Errorf("a refused merge of %v left a tree to release", c.layers)
		}
	}
	if _, err := s.Trees(); err != nil {
		t.Errorf("after the refused merges: %v", err)
	}
}

// Merged trees an earlier process left are removed when the store opens,
// as extracted trees are: nothing references them yet.
func TestOpenRemovesMergedTreesLeftBehind(t *testing.T) {
	root := t.TempDir()
	s, err := codestore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a := put(t, s, entry{name: "a.txt", body: "a"})
	m, err := s.Merge(t.Context(), []codestore.Digest{a})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := codestore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := again.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := os.Stat(m.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("merged tree after reopening: %v, want it removed", err)
	}
}

// For any layers whose paths never put a file where another layer has a
// directory, the merged tree holds every layer's files, a path two layers
// hold with the later layer's content (switch lambda.layers.merge-conflict
// later-overwrites, lambda-destinations-layers.md 8.1), and nothing else.
func TestMergeOverlaysInOrderProperty(t *testing.T) {
	if v := mergeConflictSwitch(t); v != "later-overwrites" {
		t.Fatalf("lambda.layers.merge-conflict = %q, a value this property does not model", v)
	}
	rapid.Check(t, func(t *rapid.T) {
		s, err := codestore.Open(filepathTempDir(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}()
		// Files sit two levels down and directories one level down, so no
		// path is a file in one layer and a directory in another.
		path := rapid.StringMatching(`(python|nodejs|lib)/[a-c]\.(txt|py)`)
		n := rapid.IntRange(1, 5).Draw(t, "layers")
		layers := make([]codestore.Digest, n)
		want := map[string]string{}
		for i := range layers {
			names := rapid.SliceOfNDistinct(path, 1, 4, rapid.ID).Draw(t, "paths")
			entries := make([]entry, len(names))
			for j, name := range names {
				body := rapid.StringMatching(`[a-z]{1,6}`).Draw(t, "body")
				entries[j] = entry{name: name, body: body}
				want[name] = "-rw-r--r-- " + body
			}
			layers[i] = put(t, s, entries...)
		}
		m, err := s.Merge(t.Context(), layers)
		if err != nil {
			t.Fatalf("Merge: %v", err)
		}
		if got := files(t, m.Dir); !maps.Equal(got, want) {
			t.Fatalf("merged tree = %v\nwant %v", sorted(got), sorted(want))
		}
	})
}

func sorted(m map[string]string) string {
	keys := slices.Sorted(maps.Keys(m))
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + "; ")
	}
	return b.String()
}
