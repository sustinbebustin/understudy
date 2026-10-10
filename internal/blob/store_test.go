package blob_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"testing/iotest"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/blob"
)

const smallSpill = 64

type mode struct {
	name string
	open func(t *testing.T) (*blob.Store, *os.Root)
}

// modes returns a memory store with a tiny spill threshold, so random bodies
// land on both sides of it, and a disk store. The returned root is where
// on-disk blobs live.
func modes() []mode {
	return []mode{
		{name: "memory", open: func(t *testing.T) (*blob.Store, *os.Root) {
			t.Helper()
			parent := t.TempDir()
			s, err := blob.NewMemory(parent, smallSpill)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			return s, spillRoot(t, parent)
		}},
		{name: "disk", open: func(t *testing.T) (*blob.Store, *os.Root) {
			t.Helper()
			root := openRoot(t, t.TempDir())
			s, err := blob.NewDisk(root)
			if err != nil {
				t.Fatal(err)
			}
			return s, root
		}},
	}
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// spillRoot opens the single temporary directory a memory store created
// under parent.
func spillRoot(t *testing.T, parent string) *os.Root {
	t.Helper()
	des, err := os.ReadDir(parent)
	if err != nil || len(des) != 1 {
		t.Fatalf("spill parent holds %v (err %v), want one directory", des, err)
	}
	return openRoot(t, parent+"/"+des[0].Name())
}

// put takes rapid.TB so a property passes its *rapid.T, keeping a failure
// inside rapid's shrinking and failing-example report.
func put(t rapid.TB, s *blob.Store, body []byte, hint int64) blob.Ref {
	t.Helper()
	ref, n, err := s.Put(context.Background(), bytes.NewReader(body), hint)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n != int64(len(body)) {
		t.Fatalf("Put size = %d, want %d", n, len(body))
	}
	return ref
}

// readAll drains and closes the reader an Open, Range or Export returned.
func readAll[R io.ReadCloser](r R, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(r)
	return b, errors.Join(err, r.Close())
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func drawHint(t *rapid.T, size int) int64 {
	return rapid.SampledFrom([]int64{-1, int64(size), 0, 1 << 30}).Draw(t, "sizeHint")
}

func TestPutStoresBytesUnderTheirSHA256(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, _ := m.open(t)
			rapid.Check(t, func(rt *rapid.T) {
				body := rapid.SliceOfN(rapid.Byte(), 0, 4*smallSpill).Draw(rt, "body")
				ref := put(rt, s, body, drawHint(rt, len(body)))
				if ref.String() != sha256Hex(body) {
					rt.Fatalf("Ref = %s, want sha256 %s", ref, sha256Hex(body))
				}
				if got, err := readAll(s.Open(ref)); err != nil || !bytes.Equal(got, body) {
					rt.Fatalf("Open returned %d bytes (err %v) that differ from the %d put", len(got), err, len(body))
				}
				if size, err := s.Size(ref); err != nil || size != int64(len(body)) {
					rt.Fatalf("Size = %d, %v; want %d", size, err, len(body))
				}
			})
		})
	}
}

func TestRangeEqualsTheSliceOfTheWhole(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, _ := m.open(t)
			rapid.Check(t, func(rt *rapid.T) {
				body := rapid.SliceOfN(rapid.Byte(), 0, 4*smallSpill).Draw(rt, "body")
				ref := put(rt, s, body, -1)
				off := rapid.IntRange(0, len(body)).Draw(rt, "offset")
				n := rapid.IntRange(0, len(body)-off).Draw(rt, "length")
				got, err := readAll(s.Range(ref, int64(off), int64(n)))
				if err != nil || !bytes.Equal(got, body[off:off+n]) {
					rt.Fatalf("Range(%d, %d) = %d bytes (err %v), differing from the slice of the whole", off, n, len(got), err)
				}
			})
		})
	}
}

func TestRangeOutsideTheBlobIsRefused(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, _ := m.open(t)
			ref := put(t, s, []byte("0123456789"), 10)
			for _, r := range [][2]int64{{-1, 1}, {0, -1}, {11, 0}, {5, 6}, {0, 1 << 62}} {
				_, err := s.Range(ref, r[0], r[1])
				if re, ok := errors.AsType[*blob.RangeError](err); !ok || re.Size != 10 {
					t.Errorf("Range(%d, %d) error = %v, want *RangeError naming size 10", r[0], r[1], err)
				}
			}
		})
	}
}

func TestReleaseToZeroRemovesTheBlob(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, root := m.open(t)
			body := bytes.Repeat([]byte("x"), 2*smallSpill)
			ref := put(t, s, body, -1)
			if put(t, s, body, -1) != ref {
				t.Fatal("the same bytes stored twice got different refs")
			}
			file := "sha256/" + ref.String()
			if _, err := root.Stat(file); err != nil {
				t.Fatalf("blob file missing after Put: %v", err)
			}
			if err := s.Release(ref); err != nil {
				t.Fatal(err)
			}
			if _, err := root.Stat(file); err != nil {
				t.Fatalf("blob file removed while one reference remains: %v", err)
			}
			if err := s.Release(ref); err != nil {
				t.Fatal(err)
			}
			if _, err := root.Stat(file); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("blob file still present after the last Release: %v", err)
			}
			if left, err := fs.ReadDir(root.FS(), "tmp"); err != nil || len(left) != 0 {
				t.Fatalf("tmp/ holds %v (err %v) after the last Release, want nothing", left, err)
			}
			_, err := s.Open(ref)
			if nf, ok := errors.AsType[*blob.NotFoundError](err); !ok || nf.Ref != ref {
				t.Fatalf("Open after the last Release: error = %v, want *NotFoundError", err)
			}
			err = s.Release(ref)
			if nf, ok := errors.AsType[*blob.NotFoundError](err); !ok || nf.Ref != ref {
				t.Fatalf("Release after the last Release: error = %v, want *NotFoundError", err)
			}
		})
	}
}

func TestReleaseInMemoryBlobForgetsIt(t *testing.T) {
	t.Parallel()
	s, err := blob.NewMemory(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref := put(t, s, []byte("small"), 5)
	if err := s.Release(ref); err != nil {
		t.Fatal(err)
	}
	if len(s.Refs()) != 0 {
		t.Fatalf("Refs = %v after the last Release, want none", s.Refs())
	}
}

func TestPinKeepsBlobsUntilUnpinned(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, _ := m.open(t)
			a := put(t, s, bytes.Repeat([]byte("a"), 3*smallSpill), -1)
			b := put(t, s, []byte("b"), 1)
			unpin, err := s.Pin([]blob.Ref{a, b})
			if err != nil {
				t.Fatal(err)
			}
			for _, ref := range []blob.Ref{a, b} {
				if err := s.Release(ref); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := readAll(s.Open(a)); err != nil || len(got) != 3*smallSpill {
				t.Fatalf("pinned blob read back %d bytes (err %v)", len(got), err)
			}
			if err := unpin(); err != nil {
				t.Fatal(err)
			}
			if refs := s.Refs(); len(refs) != 0 {
				t.Fatalf("Refs = %v after unpin, want none", refs)
			}
			again := put(t, s, []byte("b"), 1)
			if err := unpin(); err != nil {
				t.Fatalf("second unpin: %v", err)
			}
			if err := s.Retain(again); err != nil {
				t.Fatalf("a second unpin released a later reference: %v", err)
			}
		})
	}
}

func TestPinIsAllOrNothing(t *testing.T) {
	t.Parallel()
	s, _ := modes()[0].open(t)
	a := put(t, s, []byte("a"), 1)
	missing, err := blob.ParseRef(sha256Hex([]byte("missing")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pin([]blob.Ref{a, missing})
	if nf, ok := errors.AsType[*blob.NotFoundError](err); !ok || nf.Ref != missing {
		t.Fatalf("Pin error = %v, want *NotFoundError naming the missing ref", err)
	}
	if err := s.Release(a); err != nil {
		t.Fatal(err)
	}
	err = s.Retain(a)
	if nf, ok := errors.AsType[*blob.NotFoundError](err); !ok || nf.Ref != a {
		t.Fatalf("a failed Pin left a reference behind: Retain error = %v", err)
	}
}

func TestReleaseWithoutReferenceIsRefused(t *testing.T) {
	t.Parallel()
	root := openRoot(t, t.TempDir())
	body := []byte("persisted")
	if err := root.MkdirAll("sha256", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("sha256/"+sha256Hex(body), body, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := blob.NewDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := blob.ParseRef(sha256Hex(body))
	if err != nil {
		t.Fatal(err)
	}
	if nr, ok := errors.AsType[*blob.NotRetainedError](s.Release(ref)); !ok || nr.Ref != ref {
		t.Fatal("Release of an indexed but unreferenced blob was not refused")
	}
	if err := s.Retain(ref); err != nil {
		t.Fatalf("Retain of an indexed blob: %v", err)
	}
	if got, err := readAll(s.Open(ref)); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Open = %q, %v; want %q", got, err, body)
	}
}

func TestNewDiskRefusesForeignFilesAndClearsTemporaries(t *testing.T) {
	t.Parallel()
	root := openRoot(t, t.TempDir())
	for _, dir := range []string{"sha256", "tmp"} {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := root.WriteFile("tmp/partial", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := blob.NewDisk(root); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat("tmp/partial"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("leftover temporary file survived NewDisk: %v", err)
	}
	if err := root.WriteFile("sha256/notes.txt", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := blob.NewDisk(root); err == nil {
		t.Fatal("NewDisk accepted a non-hex file in sha256/")
	} else if ue, ok := errors.AsType[*blob.UnexpectedFileError](err); !ok || ue.Name != "sha256/notes.txt" {
		t.Fatalf("NewDisk error = %v, want *UnexpectedFileError", err)
	}
}

func TestImportVerifiesContentAgainstNames(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, _ := m.open(t)
			small, large := []byte("small"), bytes.Repeat([]byte("L"), 2*smallSpill)
			good := fstest.MapFS{
				"sha256/" + sha256Hex(small): {Data: small},
				"sha256/" + sha256Hex(large): {Data: large},
			}
			if err := s.Import(t.Context(), good); err != nil {
				t.Fatal(err)
			}
			for _, body := range [][]byte{small, large} {
				ref, err := blob.ParseRef(sha256Hex(body))
				if err != nil {
					t.Fatal(err)
				}
				if got, err := readAll(s.Export(ref)); err != nil || !bytes.Equal(got, body) {
					t.Fatalf("imported blob reads back %d bytes (err %v), want %d", len(got), err, len(body))
				}
				if err := s.Retain(ref); err != nil {
					t.Fatal(err)
				}
			}

			corrupt := fstest.MapFS{"sha256/" + sha256Hex([]byte("claimed")): {Data: []byte("actual")}}
			err := s.Import(t.Context(), corrupt)
			if ce, ok := errors.AsType[*blob.CorruptBlobError](err); !ok || ce.Got.String() != sha256Hex([]byte("actual")) {
				t.Fatalf("Import of a misnamed blob: error = %v, want *CorruptBlobError", err)
			}
			if len(s.Refs()) != 2 {
				t.Fatalf("Refs = %v, want only the two good blobs", s.Refs())
			}

			foreign := fstest.MapFS{"sha256/../../etc": {Data: []byte("x")}, "sha256/NOT-HEX": {Data: []byte("x")}}
			// MapFS lists "sha256/../../etc" as the directory "sha256/..", which
			// sorts first.
			err = s.Import(t.Context(), foreign)
			if ue, ok := errors.AsType[*blob.UnexpectedFileError](err); !ok || ue.Name != "sha256/.." {
				t.Fatalf("Import of files whose names are not refs: error = %v, want *UnexpectedFileError naming sha256/..", err)
			}
		})
	}
}

func TestImportOfAnFSWithNoBlobsDoesNothing(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, _ := m.open(t)
			put(t, s, []byte("kept"), -1)
			before := s.Refs()
			if err := s.Import(t.Context(), fstest.MapFS{"manifest.json": {Data: []byte("{}")}}); err != nil {
				t.Fatalf("Import of a snapshot with no blobs: %v", err)
			}
			if got := s.Refs(); !slices.Equal(got, before) {
				t.Fatalf("Refs = %v after importing nothing, want %v", got, before)
			}
		})
	}
}

// cutShort yields body and then fails with err, the way a net/http request
// body reports a client that disconnected before Content-Length was reached.
type cutShort struct {
	body []byte
	err  error
}

func (c *cutShort) Read(p []byte) (int, error) {
	if len(c.body) == 0 {
		return 0, c.err
	}
	n := copy(p, c.body)
	c.body = c.body[n:]
	return n, nil
}

func TestPutOfABodyCutShortFails(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, _ := m.open(t)
			for _, hint := range []int64{-1, 12, 100} {
				r := &cutShort{body: []byte("twelve bytes"), err: io.ErrUnexpectedEOF}
				if _, _, err := s.Put(t.Context(), r, hint); !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("Put (hint %d) of a body cut short: error = %v, want io.ErrUnexpectedEOF", hint, err)
				} else if !strings.Contains(err.Error(), "read blob body") || strings.Contains(err.Error(), "write") {
					t.Fatalf("Put (hint %d) of a body cut short: error %q, want it to say the body read failed and not blame a write", hint, err)
				}
				if refs := s.Refs(); len(refs) != 0 {
					t.Fatalf("Put (hint %d) of a body cut short stored %v", hint, refs)
				}
			}
		})
	}
}

// A reader may return its last bytes together with io.EOF, as net/http
// bodies do; that never keeps a body above the spill threshold in memory.
// The hints are unknown or at the threshold: a larger hint sends any body
// straight to disk.
func TestMemoryStoreSpillsEveryBodyAboveTheThreshold(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	s, err := blob.NewMemory(parent, smallSpill)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root := spillRoot(t, parent)
	for i, hint := range []int64{-1, smallSpill} {
		for _, size := range []int{smallSpill, smallSpill + 1} {
			body := bytes.Repeat([]byte{byte('a' + i)}, size)
			ref, n, err := s.Put(t.Context(), iotest.DataErrReader(bytes.NewReader(body)), hint)
			if err != nil || n != int64(size) {
				t.Fatalf("Put of %d bytes (hint %d) = %d, %v", size, hint, n, err)
			}
			_, err = root.Stat("sha256/" + ref.String())
			switch onDisk := err == nil; {
			case size > smallSpill && !onDisk:
				t.Errorf("a %d-byte body (hint %d) above the %d-byte threshold is not on disk: %v", size, hint, smallSpill, err)
			case size <= smallSpill && onDisk:
				t.Errorf("a %d-byte body (hint %d) within the %d-byte threshold was written to disk", size, hint, smallSpill)
			}
		}
	}
}

// Refusing a misnamed file never deletes a valid blob that already held the
// same bytes under their own name, even one nothing references yet.
func TestImportOfAMisnamedCopyKeepsTheStoredBlob(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, root := m.open(t)
			body := bytes.Repeat([]byte("v"), 2*smallSpill)
			valid := fstest.MapFS{"sha256/" + sha256Hex(body): {Data: body}}
			if err := s.Import(t.Context(), valid); err != nil {
				t.Fatal(err)
			}
			misnamed := fstest.MapFS{"sha256/" + sha256Hex([]byte("claimed")): {Data: body}}
			err := s.Import(t.Context(), misnamed)
			if ce, ok := errors.AsType[*blob.CorruptBlobError](err); !ok || ce.Got.String() != sha256Hex(body) {
				t.Fatalf("Import of a misnamed file: error = %v, want *CorruptBlobError naming the real digest", err)
			}
			ref, err := blob.ParseRef(sha256Hex(body))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := readAll(s.Export(ref)); err != nil || !bytes.Equal(got, body) {
				t.Fatalf("the valid blob reads back %d bytes (err %v) after the refused import, want %d", len(got), err, len(body))
			}
			if _, err := root.Stat("sha256/" + ref.String()); err != nil {
				t.Fatalf("the valid blob's file is gone after the refused import: %v", err)
			}
		})
	}
}

func TestNewMemoryRefusesASpillThresholdAboveTheCap(t *testing.T) {
	t.Parallel()
	_, err := blob.NewMemory(t.TempDir(), math.MaxInt64)
	if se, ok := errors.AsType[*blob.SpillAboveError](err); !ok || se.Value != math.MaxInt64 {
		t.Fatalf("NewMemory(MaxInt64) error = %v, want *SpillAboveError", err)
	}
	s, err := blob.NewMemory(t.TempDir(), blob.MaxSpillAbove)
	if err != nil {
		t.Fatalf("NewMemory(MaxSpillAbove): %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	put(t, s, []byte("body"), blob.MaxSpillAbove)
}

func TestPutHonoursContextCancellation(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, _ := m.open(t)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, _, err := s.Put(ctx, bytes.NewReader([]byte("body")), 4); !errors.Is(err, context.Canceled) {
				t.Fatalf("Put error = %v, want context.Canceled", err)
			}
			if len(s.Refs()) != 0 {
				t.Fatalf("a cancelled Put stored %v", s.Refs())
			}
		})
	}
}

func TestCloseRemovesTheSpillDirectory(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	s, err := blob.NewMemory(parent, smallSpill)
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, bytes.Repeat([]byte("s"), 2*smallSpill), -1)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if des, err := os.ReadDir(parent); err != nil || len(des) != 0 {
		t.Fatalf("spill parent holds %v after Close (err %v), want nothing", des, err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// A 64 MiB streaming Put allocates far less than the body: memory mode
// buffers at most the spill threshold before streaming to disk. Not parallel:
// TotalAlloc is process-wide.
func TestStreamingPutUsesBoundedMemory(t *testing.T) {
	const body, bound = 64 << 20, 16 << 20
	cases := map[string]func(t *testing.T) *blob.Store{
		"memory": func(t *testing.T) *blob.Store {
			t.Helper()
			s, err := blob.NewMemory(t.TempDir(), 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
		"disk": func(t *testing.T) *blob.Store {
			t.Helper()
			s, err := blob.NewDisk(openRoot(t, t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	}
	for name, open := range cases {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			_, n, err := s.Put(t.Context(), io.LimitReader(zeroReader{}, body), -1)
			runtime.ReadMemStats(&after)
			if err != nil || n != body {
				t.Fatalf("Put = %d, %v; want %d bytes", n, err, body)
			}
			if grew := after.TotalAlloc - before.TotalAlloc; grew >= bound {
				t.Fatalf("Put of %d bytes allocated %d bytes, want under %d", body, grew, bound)
			}
		})
	}
}

// A body given its exact size, up to the spill threshold, is read into one
// buffer and kept without a copy, so a default 8 MiB in-memory Put costs about
// 8 MiB, not twice that. Not parallel: TotalAlloc is process-wide.
func TestExactlyHintedInMemoryPutIsNotCopied(t *testing.T) {
	const size = blob.DefaultSpillAbove
	s, err := blob.NewMemory(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	body := bytes.Repeat([]byte("h"), int(size))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	ref := put(t, s, body, size)
	runtime.ReadMemStats(&after)
	if grew, bound := after.TotalAlloc-before.TotalAlloc, uint64(size+size/4); grew >= bound {
		t.Fatalf("Put of %d bytes with an exact hint allocated %d bytes, want under %d", size, grew, bound)
	}
	if got, err := readAll(s.Open(ref)); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("the blob reads back %d bytes (err %v), want %d", len(got), err, size)
	}
}

func TestParseRefAcceptsExactlyLowercaseHexSHA256(t *testing.T) {
	t.Parallel()
	ref := regexp.MustCompile(`^[0-9a-f]{64}$`)
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(
			rapid.String(),
			rapid.StringMatching(`[0-9a-f]{64}`),
			rapid.StringMatching(`[0-9a-fA-F]{63,65}`),
		).Draw(t, "s")
		got, err := blob.ParseRef(s)
		valid := ref.MatchString(s)
		if valid != (err == nil) {
			t.Fatalf("ParseRef(%q) error = %v, want valid=%v", s, err, valid)
		}
		if valid && got.String() != s {
			t.Fatalf("ParseRef(%q).String() = %q", s, got)
		}
		if ie, ok := errors.AsType[*blob.InvalidRefError](err); !valid && (!ok || ie.Value != s) {
			t.Fatalf("ParseRef(%q) error = %v, want *InvalidRefError naming the input", s, err)
		}
	})
	for _, s := range []string{"", "../../etc/passwd", sha256Hex(nil)[:63], sha256Hex(nil) + "0", "ABCDEF" + sha256Hex(nil)[6:]} {
		if _, err := blob.ParseRef(s); err == nil {
			t.Errorf("ParseRef(%q) accepted a non-ref", s)
		}
	}
}

func TestRefRoundTripsThroughJSON(t *testing.T) {
	t.Parallel()
	ref, err := blob.ParseRef(sha256Hex([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]blob.Ref{"body": ref})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]blob.Ref
	if err := json.Unmarshal(data, &back); err != nil || back["body"] != ref {
		t.Fatalf("round trip = %v, %v; want %v", back, err, ref)
	}
	if err := json.Unmarshal([]byte(`{"body":"../x"}`), &back); err == nil {
		t.Fatal("a non-hex ref decoded from JSON")
	}
	// The zero Ref could not be loaded back, so saving it fails instead.
	_, err = json.Marshal(struct{ R blob.Ref }{})
	if zr, ok := errors.AsType[*blob.ZeroRefError](err); !ok || !strings.Contains(zr.Error(), "zero Ref") {
		t.Fatalf("marshalling the zero Ref: error = %v, want *ZeroRefError saying the Ref is unset", err)
	}
}

func TestPruneDeletesEveryUnreferencedBlob(t *testing.T) {
	t.Parallel()
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, root := m.open(t)
			kept, orphan := []byte("kept"), bytes.Repeat([]byte("o"), 2*smallSpill)
			src := fstest.MapFS{
				"sha256/" + sha256Hex(kept):   {Data: kept},
				"sha256/" + sha256Hex(orphan): {Data: orphan},
			}
			if err := s.Import(t.Context(), src); err != nil {
				t.Fatal(err)
			}
			keptRef, err := blob.ParseRef(sha256Hex(kept))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Retain(keptRef); err != nil {
				t.Fatal(err)
			}
			if err := s.Prune(); err != nil {
				t.Fatal(err)
			}
			if refs := s.Refs(); len(refs) != 1 || refs[0] != keptRef {
				t.Fatalf("Refs = %v after Prune, want only %s", refs, keptRef)
			}
			if _, err := root.Stat("sha256/" + sha256Hex(orphan)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("unreferenced blob file survived Prune: %v", err)
			}
		})
	}
}

// A disk store reopened over a data directory indexes blobs no loaded state
// names (Put after the last checkpoint before a crash); Prune reclaims them.
func TestPruneReclaimsOrphansNewDiskIndexed(t *testing.T) {
	t.Parallel()
	root := openRoot(t, t.TempDir())
	first, err := blob.NewDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	orphan := put(t, first, []byte("written after the last checkpoint"), -1)
	reopened, err := blob.NewDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Prune(); err != nil {
		t.Fatal(err)
	}
	if refs := reopened.Refs(); len(refs) != 0 {
		t.Fatalf("Refs = %v after Prune, want none", refs)
	}
	if _, err := root.Stat("sha256/" + orphan.String()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("orphan blob file survived Prune: %v", err)
	}
}

// Put, Open and Release of one body from many goroutines, with the body on
// disk, never lets a Put rename onto a file a Release is removing or an Open
// race that removal; at the end nothing is stored.
func TestConcurrentPutOpenReleaseOfOneBodyLeavesNothing(t *testing.T) {
	t.Parallel()
	const goroutines, rounds = 16, 200
	for _, m := range modes() {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			s, root := m.open(t)
			body := bytes.Repeat([]byte("r"), 4*smallSpill)
			var wg sync.WaitGroup
			for range goroutines {
				wg.Go(func() {
					for range rounds {
						ref, _, err := s.Put(t.Context(), bytes.NewReader(body), -1)
						if err != nil {
							t.Errorf("Put: %v", err)
							return
						}
						if got, err := readAll(s.Open(ref)); err != nil || !bytes.Equal(got, body) {
							t.Errorf("Open while holding a reference read %d bytes (err %v), want %d", len(got), err, len(body))
							return
						}
						if err := s.Release(ref); err != nil {
							t.Errorf("Release: %v", err)
							return
						}
					}
				})
			}
			wg.Wait()
			if refs := s.Refs(); len(refs) != 0 {
				t.Fatalf("Refs = %v after every reference was released, want none", refs)
			}
			if _, err := root.Stat("sha256/" + sha256Hex(body)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("blob file present after every reference was released: %v", err)
			}
		})
	}
}
