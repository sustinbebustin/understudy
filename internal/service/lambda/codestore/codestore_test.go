package codestore_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
)

type entry struct {
	name    string
	body    string
	mode    fs.FileMode
	symlink bool
}

func buildZip(t rapid.TB, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if e.symlink {
			mode = fs.ModeSymlink | 0o777
		}
		if mode != 0 {
			h.SetMode(mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("zip header %q: %v", e.name, err)
		}
		if e.body == "" {
			continue
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatalf("zip body %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newStore(t *testing.T) *codestore.Store {
	t.Helper()
	s, err := codestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func codeSha256(b []byte) string {
	sum := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func TestPutZipExtractsWithNormalizedModes(t *testing.T) {
	s := newStore(t)
	raw := buildZip(t,
		entry{name: "bootstrap", body: "#!/bin/sh\n", mode: 0o600},
		entry{name: "lib/", mode: fs.ModeDir | 0o700},
		entry{name: "lib/helper.py", body: "x = 1\n", mode: 0o600},
		entry{name: "bin/tool", body: "tool", mode: 0o700},
		entry{name: "deep/nested/file.txt", body: "implicit parents"},
	)
	code, err := s.PutZip(t.Context(), bytes.NewReader(raw), codestore.Options{})
	if err != nil {
		t.Fatalf("PutZip: %v", err)
	}
	if got, want := code.Sha256.String(), codeSha256(raw); got != want {
		t.Errorf("CodeSha256 = %s, want %s (base64 of the zip's SHA-256, as Lambda reports it)", got, want)
	}
	if code.Size != int64(len(raw)) {
		t.Errorf("Size = %d, want the zip's length %d", code.Size, len(raw))
	}
	if !filepath.IsAbs(code.Dir) {
		t.Errorf("Dir = %q, want an absolute path a bind mount can use", code.Dir)
	}
	wantModes := map[string]fs.FileMode{
		"bootstrap":            0o755,
		"lib":                  fs.ModeDir | 0o755,
		"lib/helper.py":        0o644,
		"bin":                  fs.ModeDir | 0o755,
		"bin/tool":             0o755,
		"deep":                 fs.ModeDir | 0o755,
		"deep/nested":          fs.ModeDir | 0o755,
		"deep/nested/file.txt": 0o644,
	}
	for name, want := range wantModes {
		info, err := os.Lstat(filepath.Join(code.Dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := info.Mode() & (fs.ModeDir | fs.ModePerm); got != want {
			t.Errorf("%s mode = %v, want %v", name, got, want)
		}
	}
	got, err := os.ReadFile(filepath.Join(code.Dir, "deep", "nested", "file.txt"))
	if err != nil || string(got) != "implicit parents" {
		t.Errorf("file content = %q, %v", got, err)
	}
	dir, err := s.Dir(code.Sha256)
	if err != nil || dir != code.Dir {
		t.Errorf("Dir(sha) = %q, %v; want %q", dir, err, code.Dir)
	}
}

func TestPutZipIsContentAddressedAndReferenceCounted(t *testing.T) {
	s := newStore(t)
	raw := buildZip(t, entry{name: "index.py", body: "def handler(e, c): return e\n"})
	first, err := s.PutZip(t.Context(), bytes.NewReader(raw), codestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PutZip(t.Context(), bytes.NewReader(raw), codestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("second PutZip of the same bytes = %+v, want the same tree %+v", second, first)
	}
	if err := s.Release(first.Sha256); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if _, err := os.Stat(first.Dir); err != nil {
		t.Fatalf("tree removed while a reference remains: %v", err)
	}
	if err := s.Release(first.Sha256); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	if _, err := os.Stat(first.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("tree after the last Release: %v, want it removed", err)
	}
	var notFound *codestore.NotFoundError
	if _, err := s.Dir(first.Sha256); !errors.As(err, &notFound) {
		t.Errorf("Dir after the last Release = %v, want *NotFoundError", err)
	}
	if err := s.Release(first.Sha256); !errors.As(err, &notFound) {
		t.Errorf("Release of a released tree = %v, want *NotFoundError", err)
	}
}

func TestConcurrentPutZipOfOnePackageSharesOneTree(t *testing.T) {
	s := newStore(t)
	raw := buildZip(t, entry{name: "bootstrap", body: "#!/bin/sh\n"})
	const n = 8
	codes := make(chan codestore.Code, n)
	errs := make(chan error, n)
	for range n {
		go func() {
			code, err := s.PutZip(t.Context(), bytes.NewReader(raw), codestore.Options{})
			codes <- code
			errs <- err
		}()
	}
	var dir string
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		code := <-codes
		if dir == "" {
			dir = code.Dir
		}
		if code.Dir != dir {
			t.Errorf("Dir = %q, want every PutZip to share %q", code.Dir, dir)
		}
	}
	sha := codestore.Digest(sha256.Sum256(raw))
	for i := range n {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("tree gone after %d of %d releases: %v", i, n, err)
		}
		if err := s.Release(sha); err != nil {
			t.Fatal(err)
		}
	}
	assertNoTrees(t, s)
}

func TestPutZipRejectsUnsafeArchives(t *testing.T) {
	cases := []struct {
		name    string
		entries []entry
		opts    codestore.Options
		want    codestore.Problem
	}{
		{"parent traversal", []entry{{name: "../evil", body: "x"}}, codestore.Options{}, codestore.NonLocalName},
		{"nested traversal", []entry{{name: "a/../../evil", body: "x"}}, codestore.Options{}, codestore.NonLocalName},
		{"absolute", []entry{{name: "/etc/passwd", body: "x"}}, codestore.Options{}, codestore.NonLocalName},
		{"symlink", []entry{{name: "link", body: "/etc/passwd", symlink: true}}, codestore.Options{}, codestore.Symlink},
		{"named pipe", []entry{{name: "fifo", mode: fs.ModeNamedPipe | 0o644}}, codestore.Options{}, codestore.SpecialFile},
		{"entry too large", []entry{{name: "big", body: strings.Repeat("a", 101)}}, codestore.Options{MaxEntry: 100}, codestore.EntryTooLarge},
		{"total too large", []entry{
			{name: "a", body: strings.Repeat("a", 60)},
			{name: "b", body: strings.Repeat("b", 60)},
		}, codestore.Options{MaxUnzipped: 100}, codestore.UnzippedTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			_, err := s.PutZip(t.Context(), bytes.NewReader(buildZip(t, tc.entries...)), tc.opts)
			var bad *codestore.UnsafeZipError
			if !errors.As(err, &bad) {
				t.Fatalf("PutZip = %v, want *UnsafeZipError", err)
			}
			if bad.Problem != tc.want {
				t.Errorf("Problem = %v, want %v (%v)", bad.Problem, tc.want, err)
			}
			assertNoTrees(t, s)
		})
	}
}

// endless is a package that never ends; it counts the bytes taken from it.
type endless struct{ read int64 }

func (e *endless) Read(p []byte) (int, error) {
	clear(p)
	e.read += int64(len(p))
	return len(p), nil
}

// The store bounds what it spools to disk: a package over MaxZipped is
// refused after one byte past the limit, before the zip is even parsed.
func TestPutZipStopsReadingAPackageOverMaxZipped(t *testing.T) {
	s := newStore(t)
	const limit = 4096
	r := &endless{}
	_, err := s.PutZip(t.Context(), r, codestore.Options{MaxZipped: limit})
	bad, ok := errors.AsType[*codestore.UnsafeZipError](err)
	if !ok || bad.Problem != codestore.ZippedTooLarge || bad.Limit != limit {
		t.Fatalf("PutZip = %v, want *UnsafeZipError ZippedTooLarge at %d bytes", err, limit)
	}
	if r.read > 64<<10 {
		t.Errorf("read %d bytes of an endless package, want the copy to stop near the %d byte limit", r.read, limit)
	}
	assertNoTrees(t, s)
}

func TestPutZipRejectsBytesThatAreNotAZip(t *testing.T) {
	s := newStore(t)
	_, err := s.PutZip(t.Context(), strings.NewReader("not a zip archive"), codestore.Options{})
	if invalid, ok := errors.AsType[*codestore.InvalidZipError](err); !ok || invalid.Err == nil {
		t.Fatalf("PutZip = %v, want *InvalidZipError", err)
	}
}

func TestPutZipEnforcesSizesAgainstActualBytes(t *testing.T) {
	// A header that understates the uncompressed size must not let more
	// bytes through: archive/zip refuses the mismatch and the store reports
	// the archive as invalid without leaving a tree behind.
	raw := buildZip(t, entry{name: "f", body: strings.Repeat("z", 4096)})
	idx := bytes.LastIndex(raw, []byte("PK\x01\x02"))
	if idx < 0 {
		t.Fatal("central directory header not found")
	}
	// The uncompressed size of the central directory entry is at offset 24.
	lied := bytes.Clone(raw)
	lied[idx+24], lied[idx+25], lied[idx+26], lied[idx+27] = 16, 0, 0, 0
	s := newStore(t)
	_, err := s.PutZip(t.Context(), bytes.NewReader(lied), codestore.Options{MaxUnzipped: 1024})
	if err == nil {
		t.Fatal("PutZip of an archive whose header understates a 4096-byte entry as 16 bytes succeeded")
	}
	assertNoTrees(t, s)
}

func TestPutZipNeverWritesOutsideItsTree(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		segment := rapid.SampledFrom([]string{"a", "b", "..", ".", "", "c.txt", "/", "\\"})
		names := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) string {
			parts := rapid.SliceOfN(segment, 1, 4).Draw(t, "parts")
			name := strings.Join(parts, "/")
			if rapid.Bool().Draw(t, "absolute") {
				name = "/" + name
			}
			return name
		}), 1, 5).Draw(t, "names")
		entries := make([]entry, 0, len(names))
		for i, n := range names {
			body := strings.Repeat("x", i)
			if strings.HasSuffix(n, "/") {
				body = "" // a directory entry carries no data
			}
			entries = append(entries, entry{name: n, body: body})
		}
		root := filepathTempDir(t)
		store := root + "/store"
		s, err := codestore.Open(store)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		code, err := s.PutZip(t.Context(), bytes.NewReader(buildZip(t, entries...)), codestore.Options{})
		escaped := false
		err2 := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path != root && path != store && !strings.HasPrefix(path, store+string(filepath.Separator)) {
				escaped = true
			}
			if d.Type()&fs.ModeSymlink != 0 {
				t.Errorf("symlink %s created", path)
			}
			return nil
		})
		if err2 != nil {
			t.Fatal(err2)
		}
		if escaped {
			t.Fatalf("extraction of %q wrote outside the store", names)
		}
		if err == nil && !strings.HasPrefix(code.Dir, store) {
			t.Fatalf("Dir %q is outside the store %q", code.Dir, store)
		}
	})
}

// filepathTempDir returns a fresh directory for one rapid iteration.
func filepathTempDir(t *rapid.T) string {
	dir, err := os.MkdirTemp("", "codestore-rapid-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func assertNoTrees(t *testing.T, s *codestore.Store) {
	t.Helper()
	trees, err := s.Trees()
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 0 {
		t.Errorf("trees after a refused archive = %v, want none", trees)
	}
}
