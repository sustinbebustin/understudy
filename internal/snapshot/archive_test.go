package snapshot_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/snapshot"
	"github.com/sustinbebustin/understudy/internal/store"
)

// *blob.Store is the blob store the server hands the Coordinator.
var _ snapshot.BlobStore = (*blob.Store)(nil)

// tb is what the helpers need from *testing.T and *rapid.T.
type tb interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

// blobs is an in-memory BlobSource for archives built by hand.
type blobs map[blob.Ref][]byte

func (b blobs) Size(r blob.Ref) (int64, error) {
	data, ok := b[r]
	if !ok {
		return 0, &blob.NotFoundError{Ref: r}
	}
	return int64(len(data)), nil
}

func (b blobs) Export(r blob.Ref) (io.ReadCloser, error) {
	data, ok := b[r]
	if !ok {
		return nil, &blob.NotFoundError{Ref: r}
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// add stores data and returns its reference.
func (b blobs) add(t tb, data string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(data))
	r, err := blob.ParseRef(hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	b[r] = []byte(data)
	return r.String()
}

func instant(t tb, s string) clock.Instant {
	t.Helper()
	var i clock.Instant
	if err := i.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return i
}

// sample is an archive with every kind of entry.
func sample(t tb) snapshot.Archive {
	t.Helper()
	bs := blobs{}
	body := bs.add(t, "hello")
	seed := uint64(7)
	return snapshot.Archive{
		Manifest: snapshot.Manifest{
			Format:       snapshot.Format,
			Writer:       "understudy v0.0.0-test",
			ModelPin:     "1cfdfb4bb9ea858e65623e66d73af04bec353201",
			Clock:        snapshot.Clock{Mode: clock.Frozen{}, At: instant(t, "2026-10-09T12:00:00Z")},
			Seed:         &seed,
			Positions:    map[ids.Kind]uint64{ids.KindRequestID: 3, ids.KindMessageID: 1},
			JournalNext:  42,
			LateServices: []ref.ServiceID{"dynamodb"},
		},
		Sections: []store.Section{
			{Path: "services/sqs/000000000000/us-east-1.json", Version: 1, Data: []byte(`{"queues":{}}`)},
			{Path: "services/s3/buckets/logs.json", Version: 2, Data: []byte(`{"objects":{"a":"` + body + `"}}`), Blobs: []string{body}},
		},
		Late:    []store.Section{{Path: "services/dynamodb/000000000000/us-east-1/locks.ndjson", Version: 1, Data: []byte("{\"LockID\":{\"S\":\"a\"}}\n")}},
		Faults:  []byte(`{"rules":[],"version":1}`),
		Journal: []byte("{\"id\":1}\n"),
		Blobs:   bs,
	}
}

// write writes a with its blobs embedded.
func write(t tb, a snapshot.Archive, gz bool) []byte {
	t.Helper()
	return writeWith(t, a, snapshot.WriteOptions{Gzip: gz, Blobs: snapshot.EmbedBlobs})
}

func writeWith(t tb, a snapshot.Archive, opts snapshot.WriteOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := snapshot.Write(&buf, a, opts); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.Bytes()
}

func read(t tb, b []byte) *snapshot.Archive {
	t.Helper()
	a, err := snapshot.Read(bytes.NewReader(b), snapshot.ReadOptions{MaxBytes: 1 << 24})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return a
}

func TestWriteSameArchiveTwiceGivesSameBytes(t *testing.T) {
	t.Parallel()
	for _, gz := range []bool{false, true} {
		a := sample(t)
		first, second := write(t, a, gz), write(t, a, gz)
		if !bytes.Equal(first, second) {
			t.Errorf("gzip=%v: two writes of one archive differ", gz)
		}
	}
}

// ARCHITECTURE.md 13: sorted entries, mtime 0, uid and gid 0, mode 0644;
// gzip with an empty name and zero mtime.
func TestWriteProducesTheDeterministicLayout(t *testing.T) {
	t.Parallel()
	b := write(t, sample(t), true)
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if zr.Name != "" || !zr.ModTime.IsZero() {
		t.Errorf("gzip header name %q, mtime %v; want empty name and zero mtime", zr.Name, zr.ModTime)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
		if hdr.Typeflag != tar.TypeReg || hdr.Mode != 0o644 || hdr.Uid != 0 || hdr.Gid != 0 || !hdr.ModTime.Equal(time.Unix(0, 0)) || hdr.Uname != "" || hdr.Gname != "" {
			t.Errorf("entry %q header %+v; want a regular file, mode 0644, uid and gid 0, mtime 0", hdr.Name, hdr)
		}
	}
	sum := sha256.Sum256([]byte("hello"))
	want := []string{
		"blobs/sha256/" + hex.EncodeToString(sum[:]),
		"faults.json",
		"journal.ndjson",
		"manifest.json",
		"services/dynamodb/000000000000/us-east-1/locks.ndjson",
		"services/s3/buckets/logs.json",
		"services/sqs/000000000000/us-east-1.json",
	}
	if !slices.Equal(names, want) {
		t.Errorf("entries\n got %q\nwant %q", names, want)
	}
}

func TestReadReturnsWhatWasWritten(t *testing.T) {
	t.Parallel()
	a := sample(t)
	got := read(t, write(t, a, false))
	if !slices.EqualFunc(got.Sections, sortedSections(a.Sections), sectionEqual) || !slices.EqualFunc(got.Late, a.Late, sectionEqual) {
		t.Errorf("sections\n got %+v %+v\nwant %+v %+v", got.Sections, got.Late, a.Sections, a.Late)
	}
	if !bytes.Equal(got.Faults, a.Faults) || !bytes.Equal(got.Journal, a.Journal) {
		t.Errorf("faults %q journal %q; want %q and %q", got.Faults, got.Journal, a.Faults, a.Journal)
	}
	m := got.Manifest
	if m.Writer != a.Manifest.Writer || *m.Seed != 7 || m.Positions[ids.KindRequestID] != 3 || m.JournalNext != 42 {
		t.Errorf("manifest %+v does not match %+v", m, a.Manifest)
	}
	if f, ok := m.Clock.Mode.(clock.Frozen); !ok || f.At != a.Manifest.Clock.At || m.Clock.At != a.Manifest.Clock.At {
		t.Errorf("clock %+v; want frozen at %v", m.Clock, a.Manifest.Clock.At)
	}
	if len(m.Sections) != 3 || !m.Sections[0].Late {
		t.Errorf("section metadata %+v; want three sections, the dynamodb one late", m.Sections)
	}
	r := got.Sections[0].Blobs[0]
	br, err := blob.ParseRef(r)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := got.Blobs.Export(br)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(rc)
	if err != nil || string(body) != "hello" {
		t.Errorf("blob %s reads %q, %v; want hello", r, body, err)
	}
}

// A clock mode's offset and anchors depend on the wall clock of the process
// that saved, so only the kind and a Scaled factor are recorded.
func TestWriteRecordsOnlyTheClockModeKindAndFactor(t *testing.T) {
	t.Parallel()
	a, b := sample(t), sample(t)
	a.Manifest.Clock.Mode = clock.Scaled{Factor: 60, AnchorWall: time.Unix(100, 0), AnchorEmu: instant(t, "2026-01-01T00:00:00Z")}
	b.Manifest.Clock.Mode = clock.Scaled{Factor: 60, AnchorWall: time.Unix(200, 0)}
	if !bytes.Equal(write(t, a, false), write(t, b, false)) {
		t.Error("archives differing only in Scaled anchors wrote different bytes")
	}
	got := read(t, write(t, a, false)).Manifest.Clock.Mode
	if got != (clock.Scaled{Factor: 60}) {
		t.Errorf("read mode %#v; want Scaled{Factor: 60}", got)
	}
}

func TestWriteRefusesInvalidArchives(t *testing.T) {
	t.Parallel()
	cases := map[string]func(a *snapshot.Archive){
		"section outside services/":             func(a *snapshot.Archive) { a.Sections[0].Path = "sqs/x.json" },
		"climbing section path":                 func(a *snapshot.Archive) { a.Sections[0].Path = "services/sqs/../../x.json" },
		"duplicate section":                     func(a *snapshot.Archive) { a.Late[0].Path = a.Sections[0].Path },
		"no clock mode":                         func(a *snapshot.Archive) { a.Manifest.Clock.Mode = nil },
		"positions without a seed":              func(a *snapshot.Archive) { a.Manifest.Seed = nil },
		"missing blob":                          func(a *snapshot.Archive) { a.Blobs = blobs{} },
		"no blob source":                        func(a *snapshot.Archive) { a.Blobs = nil },
		"late section of an uncaptured service": func(a *snapshot.Archive) { a.Manifest.LateServices = nil },
		"late services out of order": func(a *snapshot.Archive) {
			a.Manifest.LateServices = []ref.ServiceID{"dynamodb", "athena"}
		},
		"late service named dot": func(a *snapshot.Archive) {
			a.Manifest.LateServices = []ref.ServiceID{".", "dynamodb"}
		},
		// Load retains only the cut's blobs, so a late pin would be pruned.
		"late section pinning a blob": func(a *snapshot.Archive) { a.Late[0].Blobs = a.Sections[1].Blobs },
		"blobs unsorted": func(a *snapshot.Archive) {
			bs, ok := a.Blobs.(blobs)
			if !ok {
				t.Fatalf("sample blobs are %T", a.Blobs)
			}
			pins := []string{a.Sections[1].Blobs[0], bs.add(t, "a")}
			slices.Sort(pins)
			slices.Reverse(pins)
			a.Sections[1].Blobs = pins
		},
	}
	for name, mutate := range cases {
		a := sample(t)
		mutate(&a)
		var buf bytes.Buffer
		if err := snapshot.Write(&buf, a, snapshot.WriteOptions{Blobs: snapshot.EmbedBlobs}); err == nil {
			t.Errorf("%s: Write succeeded", name)
		} else if buf.Len() != 0 {
			t.Errorf("%s: Write failed with %v after writing %d bytes", name, err, buf.Len())
		}
	}
	var buf bytes.Buffer
	if err := snapshot.Write(&buf, sample(t), snapshot.WriteOptions{}); !errors.As(err, new(*snapshot.ConfigError)) || buf.Len() != 0 {
		t.Errorf("no blob placement: error %v after %d bytes; want *ConfigError before writing", err, buf.Len())
	}
}

// The --persist checkpoint leaves blobs in the blob store: the archive names
// them but holds none, and needs no blob source.
func TestWriteWithExternalBlobsWritesNone(t *testing.T) {
	t.Parallel()
	a := sample(t)
	a.Blobs = nil
	got := read(t, writeWith(t, a, snapshot.WriteOptions{Blobs: snapshot.ExternalBlobs}))
	if got.Manifest.Blobs != snapshot.ExternalBlobs || got.Blobs != nil {
		t.Errorf("read placement %v, blob source %v; want ExternalBlobs and none", got.Manifest.Blobs, got.Blobs)
	}
	if !slices.EqualFunc(got.Sections, sortedSections(a.Sections), sectionEqual) {
		t.Errorf("sections %+v; want %+v with their blob references", got.Sections, a.Sections)
	}
	for _, e := range tarEntries(t, writeWith(t, a, snapshot.WriteOptions{Blobs: snapshot.ExternalBlobs})) {
		if strings.HasPrefix(e.name, "blobs/") {
			t.Errorf("an external-blob archive holds blob entry %q", e.name)
		}
	}
	if embedded := read(t, write(t, sample(t), false)); embedded.Manifest.Blobs != snapshot.EmbedBlobs {
		t.Errorf("read placement %v of an embedded archive; want EmbedBlobs", embedded.Manifest.Blobs)
	}
}

// Write always records the format it writes, so it never produces an archive
// this binary refuses.
func TestWriteStampsItsOwnFormat(t *testing.T) {
	t.Parallel()
	for _, format := range []int{snapshot.Format + 1, -1} {
		a := sample(t)
		a.Manifest.Format = format
		if got := read(t, write(t, a, false)).Manifest.Format; got != snapshot.Format {
			t.Errorf("Manifest.Format %d: read back %d; want %d", format, got, snapshot.Format)
		}
	}
}

func sectionEqual(a, b store.Section) bool {
	return a.Path == b.Path && a.Version == b.Version && bytes.Equal(a.Data, b.Data) && slices.Equal(a.Blobs, b.Blobs)
}

// drawArchive draws an archive with random sections, blobs and manifest.
func drawArchive(t *rapid.T) snapshot.Archive {
	bs := blobs{}
	contents := rapid.SliceOfDistinct(rapid.StringN(0, 64, -1), func(s string) string { return s }).Draw(t, "blobs")
	refs := make([]string, 0, len(contents))
	for _, c := range contents {
		refs = append(refs, bs.add(t, c))
	}
	// Table and bucket names reach 255 characters, past the 100 a ustar
	// name holds, so long names exercise PAX headers.
	paths := rapid.SliceOfNDistinct(
		rapid.StringMatching(`services/[a-z]{1,6}/([0-9]{12}/)?[a-zA-Z0-9._-]{1,255}\.(json|ndjson)`), 0, 8,
		func(s string) string { return s }).Draw(t, "paths")
	// Captured late services may hold no sections.
	lateServices := rapid.SliceOf(rapid.StringMatching(`[a-z]{1,6}`)).Draw(t, "captured")
	var cut, late []store.Section
	for _, p := range paths {
		s := store.Section{Path: p, Version: rapid.IntRange(0, 5).Draw(t, "version"), Data: rapid.SliceOf(rapid.Byte()).Draw(t, "data")}
		// Late data holds no blobs.
		if rapid.Bool().Draw(t, "late") {
			late = append(late, s)
			lateServices = append(lateServices, strings.Split(p, "/")[1])
			continue
		}
		if len(refs) > 0 {
			s.Blobs = slices.Sorted(slices.Values(rapid.SliceOf(rapid.SampledFrom(refs)).Draw(t, "pins")))
		}
		cut = append(cut, s)
	}
	m := snapshot.Manifest{
		Format:      snapshot.Format,
		Writer:      rapid.StringMatching(`understudy v[0-9]\.[0-9]\.[0-9]`).Draw(t, "writer"),
		ModelPin:    rapid.StringMatching(`[0-9a-f]{0,40}`).Draw(t, "pin"),
		Clock:       snapshot.Clock{Mode: drawMode(t), At: clock.FromWire(time.Unix(rapid.Int64Range(0, 4e9).Draw(t, "at"), 0))},
		JournalNext: 0,
	}
	slices.Sort(lateServices)
	for _, svc := range slices.Compact(lateServices) {
		m.LateServices = append(m.LateServices, ref.ServiceID(svc))
	}
	if rapid.Bool().Draw(t, "seeded") {
		seed := rapid.Uint64().Draw(t, "seed")
		m.Seed = &seed
		m.Positions = map[ids.Kind]uint64{}
		var kinds []ids.Kind
		for k := ids.KindRequestID; k <= ids.KindJitter; k++ {
			kinds = append(kinds, k)
		}
		for _, k := range rapid.SliceOf(rapid.SampledFrom(kinds)).Draw(t, "kinds") {
			m.Positions[k] = rapid.Uint64Range(0, ids.MaxPosition).Draw(t, "position")
		}
	}
	a := snapshot.Archive{Manifest: m, Sections: cut, Late: late, Blobs: bs}
	if rapid.Bool().Draw(t, "faults") {
		a.Faults = rapid.SliceOf(rapid.Byte()).Draw(t, "faults bytes")
	}
	if rapid.Bool().Draw(t, "journal") {
		a.Journal = rapid.SliceOf(rapid.Byte()).Draw(t, "journal bytes")
	}
	return a
}

func drawMode(t *rapid.T) clock.Mode {
	switch rapid.IntRange(0, 2).Draw(t, "mode") {
	case 0:
		return clock.Running{}
	case 1:
		return clock.Frozen{}
	default:
		return clock.Scaled{Factor: rapid.Float64Range(0.001, clock.MaxScale).Draw(t, "factor")}
	}
}

func TestWriteReadWriteIsByteIdentical(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		a := drawArchive(t)
		opts := snapshot.WriteOptions{
			Gzip:  rapid.Bool().Draw(t, "gzip"),
			Blobs: rapid.SampledFrom([]snapshot.BlobPlacement{snapshot.EmbedBlobs, snapshot.ExternalBlobs}).Draw(t, "placement"),
		}
		first := writeWith(t, a, opts)
		got := read(t, first)
		if second := writeWith(t, *got, opts); !bytes.Equal(first, second) {
			t.Fatalf("write, read, write changed the bytes (%+v)", opts)
		}
		if !slices.EqualFunc(sortedSections(got.Sections), sortedSections(a.Sections), sectionEqual) ||
			!slices.EqualFunc(sortedSections(got.Late), sortedSections(a.Late), sectionEqual) {
			t.Fatalf("read sections differ from written ones")
		}
	})
}

func TestClockModeName(t *testing.T) {
	t.Parallel()
	for mode, want := range map[clock.Mode]string{
		clock.Running{}:            "running",
		clock.Frozen{}:             "frozen",
		clock.Scaled{Factor: 60}:   "scaled x60",
		clock.Scaled{Factor: 0.5}:  "scaled x0.5",
		clock.Scaled{Factor: 1e-3}: "scaled x0.001",
	} {
		if got := (snapshot.Clock{Mode: mode}).ModeName(); got != want {
			t.Errorf("ModeName of %#v = %q, want %q", mode, got, want)
		}
	}
}

func sortedSections(ss []store.Section) []store.Section {
	return slices.SortedFunc(slices.Values(ss), func(a, b store.Section) int { return strings.Compare(a.Path, b.Path) })
}
