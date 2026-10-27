package codestore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
)

// A later layer's file replaces an earlier one's with its own mode, so an
// executable can become a plain file and the reverse (switch
// lambda.layers.merge-conflict later-overwrites,
// lambda-destinations-layers.md 8.1).
func TestMergeTakesTheLaterLayersMode(t *testing.T) {
	s := newStore(t)
	a := put(t, s, entry{name: "bin/tool", body: "a", mode: 0o700}, entry{name: "bin/plain", body: "a"})
	b := put(t, s, entry{name: "bin/tool", body: "b"}, entry{name: "bin/plain", body: "b", mode: 0o700})
	m, err := s.Merge(t.Context(), []codestore.Digest{a, b})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"bin/tool": "-rw-r--r-- b", "bin/plain": "-rwxr-xr-x b"}
	if got := files(t, m.Dir); !maps.Equal(got, want) {
		t.Errorf("merged tree = %v, want %v", got, want)
	}
}

// A conflict below the top level names the nested path.
func TestMergeConflictNamesTheNestedPath(t *testing.T) {
	s := newStore(t)
	file := put(t, s, entry{name: "lib/x", body: "file"})
	dir := put(t, s, entry{name: "lib/x/y.so", body: "lib"})
	_, err := s.Merge(t.Context(), []codestore.Digest{file, dir})
	mc, ok := errors.AsType[*codestore.MergeConflictError](err)
	if !ok || *mc != (codestore.MergeConflictError{Path: "lib/x", Layer: 1, File: false}) {
		t.Errorf("Merge = %v, want a conflict at lib/x for layer 1", err)
	}
}

// Merge takes no lasting reference on the layers' trees, also when it
// refuses: one Release of each layer removes its tree.
func TestMergeLeavesTheLayersReferencesAsTheyWere(t *testing.T) {
	s := newStore(t)
	a := put(t, s, entry{name: "python/a.py", body: "a"})
	b := put(t, s, entry{name: "python", body: "conflicts with a's directory"})
	if _, err := s.Merge(t.Context(), []codestore.Digest{a}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Merge(t.Context(), []codestore.Digest{a, b}); err == nil {
		t.Fatal("Merge of conflicting layers succeeded")
	}
	missing := codestore.Digest(sha256.Sum256([]byte("never added")))
	if _, err := s.Merge(t.Context(), []codestore.Digest{a, missing}); err == nil {
		t.Fatal("Merge with a missing layer succeeded")
	}
	for _, d := range []codestore.Digest{a, b} {
		if err := s.Release(d); err != nil {
			t.Fatal(err)
		}
	}
	assertNoTrees(t, s)
}

// A merge whose context ends publishes nothing and leaves no staging
// directory.
func TestMergeStopsWithItsContext(t *testing.T) {
	s := newStore(t)
	a := put(t, s, entry{name: "a.txt", body: "a"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.Merge(ctx, []codestore.Digest{a})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Merge with an ended context = %v, want context.Canceled", err)
	}
	if err := s.ReleaseMerged(mergeKey(a)); !isMergedNotFound(err) {
		t.Errorf("ReleaseMerged after a stopped merge = %v, want *MergedNotFoundError", err)
	}
	if err := s.Release(a); err != nil {
		t.Fatal(err)
	}
	assertNoTrees(t, s)
}

// Merges of the same layers at once share one tree and count one
// reference each.
func TestConcurrentMergesShareOneTree(t *testing.T) {
	s := newStore(t)
	a := put(t, s, entry{name: "python/a.py", body: "a"})
	b := put(t, s, entry{name: "python/b.py", body: "b"})
	const n = 8
	got := make([]codestore.Merged, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { got[i], errs[i] = s.Merge(t.Context(), []codestore.Digest{a, b}) })
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || got[i] != got[0] {
			t.Fatalf("Merge %d = %+v, %v; want %+v like the first", i, got[i], errs[i], got[0])
		}
	}
	for i := range n {
		if _, err := os.Stat(got[0].Dir); err != nil {
			t.Fatalf("merged tree gone with %d of %d references left: %v", n-i, n, err)
		}
		if err := s.ReleaseMerged(got[0].Key); err != nil {
			t.Fatalf("ReleaseMerged %d: %v", i, err)
		}
	}
	if _, err := os.Stat(got[0].Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("merged tree after every reference was released: %v", err)
	}
	if _, err := s.Trees(); err != nil {
		t.Error(err)
	}
}

// ReleaseMerged of a tree never merged says so, by key.
func TestReleaseMergedOfAnUnknownKey(t *testing.T) {
	s := newStore(t)
	key := codestore.Digest(sha256.Sum256([]byte("no such tree")))
	err := s.ReleaseMerged(key)
	nf, ok := errors.AsType[*codestore.MergedNotFoundError](err)
	if !ok || nf.Key != key {
		t.Errorf("ReleaseMerged = %v, want a *MergedNotFoundError for the key", err)
	}
}

func isMergedNotFound(err error) bool {
	_, ok := errors.AsType[*codestore.MergedNotFoundError](err)
	return ok
}

// The merged tree lives under the store's directory, named by its key.
func TestMergedTreeIsUnderTheStore(t *testing.T) {
	root := t.TempDir()
	s, err := codestore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	a := put(t, s, entry{name: "a.txt", body: "a"})
	m, err := s.Merge(t.Context(), []codestore.Digest{a})
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(abs, "merged", hex.EncodeToString(m.Key[:])); m.Dir != want {
		t.Errorf("Dir = %q, want %q", m.Dir, want)
	}
}
