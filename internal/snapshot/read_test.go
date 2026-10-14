package snapshot_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

type rawEntry struct {
	name string
	data string
	kind byte
}

// rawTar builds a tar by hand, so tests can write what Write never would.
func rawTar(t *testing.T, entries ...rawEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		hdr := &tar.Header{Typeflag: kind, Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Format: tar.FormatPAX}
		if kind != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.data[:hdr.Size])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const (
	// manifestHead opens a valid manifest; a test appends members and "}".
	manifestHead    = `{"blobs":"embedded","clock":{"at":"2026-10-09T12:00:00Z","mode":"frozen"},"format":0,"journalNext":1,"writer":"understudy v0.1.0"`
	minimalManifest = manifestHead + `}`
)

func readErr(t *testing.T, archive []byte, opts snapshot.ReadOptions) error {
	t.Helper()
	if opts.MaxBytes == 0 {
		opts.MaxBytes = 1 << 20
	}
	a, err := snapshot.Read(bytes.NewReader(archive), opts)
	if err == nil {
		t.Fatalf("Read accepted the archive: %+v", a)
	}
	return err
}

func TestReadAcceptsAMinimalArchive(t *testing.T) {
	t.Parallel()
	a, err := snapshot.Read(bytes.NewReader(rawTar(t, rawEntry{name: "manifest.json", data: minimalManifest})), snapshot.ReadOptions{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if a.Manifest.Writer != "understudy v0.1.0" || len(a.Sections) != 0 || a.Faults != nil || a.Journal != nil {
		t.Errorf("archive %+v; want only the manifest", a)
	}
}

func TestReadRefusesNonLocalEntryNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"../x", "/x", "services/../../x", "./manifest.json"} {
		err := readErr(t, rawTar(t, rawEntry{name: "manifest.json", data: minimalManifest}, rawEntry{name: name, data: "{}"}), snapshot.ReadOptions{})
		var ee *snapshot.EntryError
		if !errors.As(err, &ee) || ee.Defect != snapshot.NonLocalName || ee.Name != name {
			t.Errorf("%q: error %v; want a NonLocalName *EntryError naming the entry", name, err)
			continue
		}
		want := `snapshot entry "` + name + `" is not a local path: entry names must be relative, clean and stay inside the archive; the archive was refused and nothing was loaded`
		if err.Error() != want {
			t.Errorf("%q: message\n got %s\nwant %s", name, err, want)
		}
	}
}

func TestReadRefusesANewerFormat(t *testing.T) {
	t.Parallel()
	// A newer format may change the rest of the manifest, so only format
	// and writer are read before refusing.
	manifest := `{"format":1,"writer":"understudy v2.0.0","futureField":{"x":1}}`
	err := readErr(t, rawTar(t, rawEntry{name: "manifest.json", data: manifest}), snapshot.ReadOptions{})
	var fe *snapshot.FormatError
	if !errors.As(err, &fe) || fe.Archive != 1 || fe.Reader != 0 || fe.Writer != "understudy v2.0.0" {
		t.Fatalf("error %v; want *FormatError{Archive: 1, Reader: 0, Writer: understudy v2.0.0}", err)
	}
	want := "snapshot archive format 1 is newer than format 0, the newest this understudy reads; the archive was written by understudy v2.0.0; nothing was loaded (load it with that understudy version or a newer one)"
	if err.Error() != want {
		t.Errorf("message\n got %s\nwant %s", err, want)
	}
}

func TestReadRefusesASectionOfAnotherSchemaVersion(t *testing.T) {
	t.Parallel()
	archive := write(t, sample(t), false)
	running := map[ref.ServiceID]int{"sqs": 2, "s3": 2, "dynamodb": 1}
	err := readErr(t, archive, snapshot.ReadOptions{Schemas: running})
	var se *snapshot.SchemaError
	if !errors.As(err, &se) || se.Section != 1 || se.Running != 2 || !se.Known {
		t.Fatalf("error %v; want *SchemaError for the sqs section at version 1 against 2", err)
	}
	want := `snapshot section "services/sqs/000000000000/us-east-1.json" has sqs state version 1 but this understudy runs version 2; the archive was written by understudy v0.0.0-test; nothing was loaded (load it with that understudy version)`
	if err.Error() != want {
		t.Errorf("message\n got %s\nwant %s", err, want)
	}

	delete(running, "s3")
	running["sqs"] = 1
	err = readErr(t, archive, snapshot.ReadOptions{Schemas: running})
	if !errors.As(err, &se) || se.Known || se.Service != "s3" {
		t.Fatalf("error %v; want *SchemaError for the unknown s3 service", err)
	}
	if !strings.Contains(err.Error(), `belongs to service "s3" at state version 2, which this understudy does not have; the archive was written by understudy v0.0.0-test`) {
		t.Errorf("message %q does not name the service, its version and the writer", err)
	}
}

func TestReadRefusesMalformedArchives(t *testing.T) {
	t.Parallel()
	good := write(t, sample(t), false)
	entries := tarEntries(t, good)
	cases := map[string]struct {
		archive []byte
		defect  snapshot.EntryDefect
	}{
		"no manifest": {rawTar(t, rawEntry{name: "faults.json", data: "{}"}), snapshot.MissingEntry},
		"directory entry": {rawTar(t, rawEntry{name: "manifest.json", data: minimalManifest},
			rawEntry{name: "services", kind: tar.TypeDir}), snapshot.NotRegular},
		"symlink entry": {rawTar(t, rawEntry{name: "manifest.json", data: minimalManifest},
			rawEntry{name: "faults.json", kind: tar.TypeSymlink}), snapshot.NotRegular},
		"duplicate entry": {rawTar(t, rawEntry{name: "manifest.json", data: minimalManifest},
			rawEntry{name: "manifest.json", data: minimalManifest}), snapshot.DuplicateEntry},
		"unknown entry": {rawTar(t, rawEntry{name: "manifest.json", data: minimalManifest},
			rawEntry{name: "notes.txt", data: "hi"}), snapshot.UnexpectedEntry},
		"unlisted section": {rawTar(t, rawEntry{name: "manifest.json", data: minimalManifest},
			rawEntry{name: "services/sqs/x.json", data: "{}"}), snapshot.UnlistedEntry},
		"tampered section": {replace(t, entries, "services/sqs/000000000000/us-east-1.json", `{"queues":{"x":{}}}`), snapshot.DigestMismatch},
		"missing section":  {replace(t, entries, "services/sqs/000000000000/us-east-1.json", ""), snapshot.MissingEntry},
	}
	for name, c := range cases {
		err := readErr(t, c.archive, snapshot.ReadOptions{})
		var ee *snapshot.EntryError
		if !errors.As(err, &ee) || ee.Defect != c.defect {
			t.Errorf("%s: error %v; want an *EntryError with defect %v", name, err, c.defect)
		}
	}
}

// The format is judged before the entries: a newer format may lay them out
// differently, and manifest.json sorts after most of them.
func TestReadRefusesANewerFormatBeforeItsEntries(t *testing.T) {
	t.Parallel()
	manifest := `{"format":1,"writer":"understudy v2.0.0"}`
	for name, other := range map[string]rawEntry{
		"directory entry": {name: "blobs", kind: tar.TypeDir},
		"non-local name":  {name: "../x", data: "{}"},
		"repeated name":   {name: "manifest.json", data: manifest},
	} {
		err := readErr(t, rawTar(t, other, rawEntry{name: "manifest.json", data: manifest}), snapshot.ReadOptions{})
		if fe, ok := errors.AsType[*snapshot.FormatError](err); !ok || fe.Archive != 1 {
			t.Errorf("%s: error %v; want *FormatError for format 1", name, err)
		}
	}
}

func TestReadRefusesUnsortedBlobs(t *testing.T) {
	t.Parallel()
	hi, lo := digest("y"), digest("x")
	if hi < lo {
		hi, lo = lo, hi
	}
	data := map[string]string{digest("x"): "x", digest("y"): "y"}
	manifest := manifestHead + `,"sections":[{"path":"services/s3/b.json","version":1,"sha256":"` + digest("{}") + `","blobs":["` + hi + `","` + lo + `"]}]}`
	archive := rawTar(t,
		rawEntry{name: "blobs/sha256/" + lo, data: data[lo]},
		rawEntry{name: "blobs/sha256/" + hi, data: data[hi]},
		rawEntry{name: "manifest.json", data: manifest},
		rawEntry{name: "services/s3/b.json", data: "{}"})
	err := readErr(t, archive, snapshot.ReadOptions{})
	if me, ok := errors.AsType[*snapshot.ManifestError](err); !ok || !strings.Contains(me.Reason, "unsorted") {
		t.Errorf("error %v; want a *ManifestError for the unsorted blob list", err)
	}
}

func TestReadRefusesInvalidManifests(t *testing.T) {
	t.Parallel()
	body := digest("x")
	cases := map[string]struct {
		manifest string
		entries  []rawEntry
		reason   string
	}{
		"no blob placement": {
			manifest: `{"clock":{"at":"2026-10-09T12:00:00Z","mode":"frozen"},"format":0,"journalNext":1,"writer":"w"}`,
			reason:   `blob placement "" is not embedded or external`,
		},
		"late service named dot": {manifest: manifestHead + `,"lateServices":["."]}`, reason: `late service name "." is not one clean path element`},
		"late section pinning a blob": {
			manifest: manifestHead + `,"lateServices":["dynamodb"],"sections":[{"path":"services/dynamodb/t.ndjson","version":1,"sha256":"` + digest("") + `","blobs":["` + body + `"],"late":true}]}`,
			entries:  []rawEntry{{name: "blobs/sha256/" + body, data: "x"}, {name: "services/dynamodb/t.ndjson", data: ""}},
			reason:   "late data holds no blobs",
		},
	}
	for name, c := range cases {
		err := readErr(t, rawTar(t, append(c.entries, rawEntry{name: "manifest.json", data: c.manifest})...), snapshot.ReadOptions{})
		if me, ok := errors.AsType[*snapshot.ManifestError](err); !ok || !strings.Contains(me.Reason, c.reason) {
			t.Errorf("%s: error %v; want a *ManifestError saying %q", name, err, c.reason)
		}
	}
}

func TestReadRefusesABlobEntryInAnExternalBlobArchive(t *testing.T) {
	t.Parallel()
	a := sample(t)
	external := tarEntries(t, writeWith(t, a, snapshot.WriteOptions{Blobs: snapshot.ExternalBlobs, MaxBytes: limit}))
	name := "blobs/sha256/" + a.Sections[1].Blobs[0]
	err := readErr(t, rawTar(t, append([]rawEntry{{name: name, data: "hello"}}, external...)...), snapshot.ReadOptions{})
	if ee, ok := errors.AsType[*snapshot.EntryError](err); !ok || ee.Defect != snapshot.ExternalBlobEmbedded || ee.Name != name {
		t.Errorf("error %v; want an ExternalBlobEmbedded *EntryError naming %s", err, name)
	}
}

// paxRecord formats one PAX extended header record: its length in decimal,
// counting itself, then " key=value\n".
func paxRecord(key, value string) string {
	rest := " " + key + "=" + value + "\n"
	n := len(rest) + 1
	for len(strconv.Itoa(n))+len(rest) != n {
		n++
	}
	return strconv.Itoa(n) + rest
}

// sparseTar builds an archive whose one entry is a PAX 1.0 sparse file of
// realsize bytes, all holes, followed by a minimal manifest. archive/tar's
// writer drops GNU.sparse records, so the PAX header is written as a
// regular entry and its type flag and checksum are patched.
func sparseTar(t *testing.T, name string, realsize int64) []byte {
	t.Helper()
	records := paxRecord("GNU.sparse.major", "1") + paxRecord("GNU.sparse.minor", "0") +
		paxRecord("GNU.sparse.name", name) + paxRecord("GNU.sparse.realsize", strconv.FormatInt(realsize, 10))
	// The sparse map, one block: one fragment of zero bytes at offset 0.
	sparseMap := make([]byte, 512)
	copy(sparseMap, "1\n0\n0\n")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range []struct {
		name string
		data []byte
	}{{"PaxHeaders/x", []byte(records)}, {name, sparseMap}, {"manifest.json", []byte(minimalManifest)}} {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	const typeflag, chksum = 156, 148
	b[typeflag] = tar.TypeXHeader
	copy(b[chksum:chksum+8], "        ")
	sum := 0
	for _, c := range b[:512] {
		sum += int(c)
	}
	copy(b[chksum:chksum+8], fmt.Sprintf("%06o\x00 ", sum))
	return b
}

// A sparse entry expands holes it never reads from the stream, so a small
// archive could make Read buffer far more than MaxBytes. Not parallel:
// it measures the allocations of the Read alone.
func TestReadRefusesSparseEntriesWithoutExpandingThem(t *testing.T) {
	const realsize = 256 << 20
	archive := sparseTar(t, "blobs/sha256/"+digest("x"), realsize)
	if hdr, err := tar.NewReader(bytes.NewReader(archive)).Next(); err != nil || hdr.Size != realsize {
		t.Fatalf("the hand-built sparse entry reads as size %v, %v; want %d", hdr, err, realsize)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := snapshot.Read(bytes.NewReader(archive), snapshot.ReadOptions{MaxBytes: 1 << 20})
	runtime.ReadMemStats(&after)
	if ee, ok := errors.AsType[*snapshot.EntryError](err); !ok || ee.Defect != snapshot.NotRegular {
		t.Errorf("error %v; want a NotRegular *EntryError for the sparse entry", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8<<20 {
		t.Errorf("Read allocated %d bytes for a %d byte archive under a 1 MiB cap", grew, len(archive))
	}
}

func TestReadChecksTheStreamToItsEnd(t *testing.T) {
	t.Parallel()
	plain, gz := write(t, sample(t), false), write(t, sample(t), true)
	flipped := bytes.Clone(gz)
	flipped[len(flipped)-8] ^= 0xff // the first byte of the CRC-32 trailer
	cases := map[string]struct {
		archive []byte
		cause   error
	}{
		"flipped gzip checksum":      {archive: flipped, cause: gzip.ErrChecksum},
		"bytes after the tar":        {archive: append(bytes.Clone(plain), "garbage trailing bytes"...)},
		"bytes after the gzip trail": {archive: append(bytes.Clone(gz), "garbage trailing bytes"...), cause: gzip.ErrHeader},
		// tar.Reader reports no input as the end of an empty archive.
		"empty stream": {archive: nil},
	}
	for name, c := range cases {
		err := readErr(t, c.archive, snapshot.ReadOptions{})
		if !errors.As(err, new(*snapshot.StreamError)) || c.cause != nil && !errors.Is(err, c.cause) {
			t.Errorf("%s: error %v; want a *StreamError caused by %v", name, err, c.cause)
		}
	}
	// Tar writers pad to a record size with zeros.
	padded := append(bytes.Clone(plain), make([]byte, 10240)...)
	if _, err := snapshot.Read(bytes.NewReader(padded), snapshot.ReadOptions{MaxBytes: 1 << 20}); err != nil {
		t.Errorf("Read refused an archive padded with zeros: %v", err)
	}
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestReadEnforcesMaxBytes(t *testing.T) {
	t.Parallel()
	for _, gz := range []bool{false, true} {
		archive := write(t, sample(t), gz)
		err := readErr(t, archive, snapshot.ReadOptions{MaxBytes: 1024})
		var se *snapshot.SizeError
		if !errors.As(err, &se) || se.Max != 1024 {
			t.Errorf("gzip=%v: error %v; want *SizeError{Max: 1024}", gz, err)
		}
	}
	if _, err := snapshot.Read(bytes.NewReader(nil), snapshot.ReadOptions{}); err == nil {
		t.Error("Read accepted a zero MaxBytes")
	}
}

// tarEntries lists the entries of an uncompressed archive.
func tarEntries(t *testing.T, archive []byte) []rawEntry {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(archive))
	var out []rawEntry
	for {
		hdr, err := tr.Next()
		if err != nil {
			return out
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(tr); err != nil {
			t.Fatal(err)
		}
		out = append(out, rawEntry{name: hdr.Name, data: buf.String()})
	}
}

// replace rebuilds an archive with one entry's data replaced, or dropped
// when data is empty.
func replace(t *testing.T, entries []rawEntry, name, data string) []byte {
	t.Helper()
	var out []rawEntry
	for _, e := range entries {
		if e.name == name {
			if data == "" {
				continue
			}
			e.data = data
		}
		out = append(out, e)
	}
	return rawTar(t, out...)
}
