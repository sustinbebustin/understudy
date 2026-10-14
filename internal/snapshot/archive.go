package snapshot

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Archive is one snapshot: the manifest, the sections and the bytes they
// pin, in memory.
type Archive struct {
	Manifest Manifest
	// Sections are the cell sections cut under the gate, with paths of the
	// form "services/<svc>/...". Write sorts them.
	Sections []store.Section
	// Late are the sections LateExporters wrote after the cut, with paths
	// of the same form and none shared with Sections.
	Late []store.Section
	// Faults is the fault store's export, written as faults.json; nil
	// writes no entry.
	Faults []byte
	// Journal is the NDJSON journal, written as journal.ndjson; nil writes
	// no entry.
	Journal []byte
	// Blobs supplies every blob a section pins when the archive embeds
	// them. It may be nil when no section pins one. Write ignores it with
	// ExternalBlobs, and Read returns nil for an archive whose blobs are
	// external.
	Blobs BlobSource
}

// BlobPlacement says where the blobs an archive's sections pin live.
type BlobPlacement uint8

// BlobPlacement values.
const (
	// EmbedBlobs writes every pinned blob into the archive under
	// blobs/sha256/, so the archive is self-contained: an explicit
	// `snapshot save`, a fixture to commit or share.
	EmbedBlobs BlobPlacement = iota + 1
	// ExternalBlobs writes no blob: the archive names them and the blob
	// store keeps them. This is the --persist checkpoint, whose blobs live
	// in <dir>/blobs (ADR 0009), so a checkpoint does not rewrite every
	// object body and code zip.
	ExternalBlobs
)

// String returns the placement's manifest name, "embedded" or "external".
func (p BlobPlacement) String() string {
	switch p {
	case EmbedBlobs:
		return placementEmbedded
	case ExternalBlobs:
		return placementExternal
	default:
		return fmt.Sprintf("BlobPlacement(%d)", uint8(p))
	}
}

// BlobSource supplies blob bytes by reference. *blob.Store implements it;
// so does the Blobs of an Archive Read returns.
type BlobSource interface {
	Size(ref blob.Ref) (int64, error)
	Export(ref blob.Ref) (io.ReadCloser, error)
}

// WriteOptions configures Write.
type WriteOptions struct {
	// Gzip compresses the tar with an empty file name and a zero
	// modification time, so it stays deterministic.
	Gzip bool
	// Blobs places the pinned blobs in the archive or leaves them in the
	// blob store. It is required.
	Blobs BlobPlacement
	// MaxBytes caps the tar Write produces, counted before gzip as
	// ReadOptions.MaxBytes counts it, so Write never produces an archive a
	// Read with the same cap refuses. It must be positive.
	MaxBytes int64
}

// Write writes a as the deterministic archive of ARCHITECTURE.md 13: a POSIX
// tar with entries sorted by name, modification time 0, uid and gid 0 and
// mode 0644, optionally gzipped. The same Archive gives the same bytes.
// Manifest.Sections is derived from Sections and Late, Manifest.Format is
// always Format and Manifest.Blobs is opts.Blobs, whatever the values given.
// Write refuses, before writing anything, a missing blob placement or a
// MaxBytes that is not positive (*ConfigError), a section path that is not
// "services/<svc>/<path>", two sections with one path, an invalid manifest
// (including a late section of a service Manifest.LateServices does not
// list, or one that pins blobs) and, with EmbedBlobs, a pinned blob the
// source cannot supply. A tar that would pass MaxBytes (*WriteLimitError) and
// a blob whose bytes change while it is copied fail the write part-way.
func Write(w io.Writer, a Archive, opts WriteOptions) error {
	switch opts.Blobs {
	case EmbedBlobs, ExternalBlobs:
	default:
		return &ConfigError{Reason: fmt.Sprintf("WriteOptions.Blobs is %v; it must be EmbedBlobs or ExternalBlobs; nothing was written", opts.Blobs)}
	}
	if opts.MaxBytes <= 0 {
		return &ConfigError{Reason: fmt.Sprintf("WriteOptions.MaxBytes is %d; it must be a positive byte limit; nothing was written", opts.MaxBytes)}
	}
	entries, err := plan(a, opts.Blobs)
	if err != nil {
		return err
	}
	if !opts.Gzip {
		return writeTar(&limitWriter{w: w, left: opts.MaxBytes, max: opts.MaxBytes}, entries)
	}
	zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("start snapshot gzip stream: %w", err)
	}
	if err := writeTar(&limitWriter{w: zw, left: opts.MaxBytes, max: opts.MaxBytes}, entries); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finish snapshot gzip stream: %w", err)
	}
	return nil
}

// limitWriter fails with *WriteLimitError, writing nothing of p, once more
// than max bytes would have been written.
type limitWriter struct {
	w         io.Writer
	left, max int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.left {
		return 0, &WriteLimitError{Max: l.max}
	}
	l.left -= int64(len(p))
	return l.w.Write(p)
}

// entry is one tar entry to write.
type entry struct {
	name string
	size int64
	data []byte
	// ref is set for a blob, whose bytes are copied from src.
	ref blob.Ref
	src BlobSource
}

// plan validates a and lists its entries in name order.
func plan(a Archive, placement BlobPlacement) ([]entry, error) {
	sections, err := describe(a.Sections, a.Late)
	if err != nil {
		return nil, err
	}
	m := a.Manifest
	m.Format = Format
	m.Blobs = placement
	m.Sections = sections
	manifest, err := encodeManifest(m)
	if err != nil {
		return nil, err
	}
	entries := []entry{{name: manifestName, size: int64(len(manifest)), data: manifest}}
	if a.Faults != nil {
		entries = append(entries, entry{name: faultsName, size: int64(len(a.Faults)), data: a.Faults})
	}
	if a.Journal != nil {
		entries = append(entries, entry{name: journalName, size: int64(len(a.Journal)), data: a.Journal})
	}
	for _, s := range slices.Concat(a.Sections, a.Late) {
		entries = append(entries, entry{name: s.Path, size: int64(len(s.Data)), data: s.Data})
	}
	if placement == EmbedBlobs {
		blobs, err := blobEntries(sections, a.Blobs)
		if err != nil {
			return nil, err
		}
		entries = append(entries, blobs...)
	}
	slices.SortFunc(entries, func(x, y entry) int { return strings.Compare(x.name, y.name) })
	return entries, nil
}

// describe returns the manifest's section list for cut and late sections.
func describe(cut, late []store.Section) ([]SectionMeta, error) {
	metas := make([]SectionMeta, 0, len(cut)+len(late))
	add := func(ss []store.Section, isLate bool) {
		for _, s := range ss {
			sum := sha256.Sum256(s.Data)
			metas = append(metas, SectionMeta{
				Path:    s.Path,
				Version: s.Version,
				SHA256:  hex.EncodeToString(sum[:]),
				Blobs:   s.Blobs,
				Late:    isLate,
			})
		}
	}
	add(cut, false)
	add(late, true)
	if err := distinctPaths(metas, func(m SectionMeta) string { return m.Path }); err != nil {
		return nil, err
	}
	slices.SortFunc(metas, func(x, y SectionMeta) int { return strings.Compare(x.Path, y.Path) })
	return metas, nil
}

// distinctPaths refuses two sections with one path; path reads a section's.
func distinctPaths[S any](sections []S, path func(S) string) error {
	paths := make([]string, len(sections))
	for i, s := range sections {
		paths[i] = path(s)
	}
	slices.Sort(paths)
	for i := 1; i < len(paths); i++ {
		if paths[i] == paths[i-1] {
			return &EntryError{Name: paths[i], Defect: DuplicateEntry}
		}
	}
	return nil
}

// blobEntries lists one entry per distinct blob the sections pin.
func blobEntries(sections []SectionMeta, src BlobSource) ([]entry, error) {
	refs, err := pinned(sections, metaBlobs)
	if err != nil {
		return nil, err
	}
	if len(refs) > 0 && src == nil {
		return nil, &EntryError{Name: blobsDir + refs[0].String(), Defect: UnreadableBlob, Err: errors.New("the archive has no blob source")}
	}
	entries := make([]entry, 0, len(refs))
	for _, r := range refs {
		size, err := src.Size(r)
		if err != nil {
			return nil, &EntryError{Name: blobsDir + r.String(), Defect: UnreadableBlob, Err: err}
		}
		entries = append(entries, entry{name: blobsDir + r.String(), size: size, ref: r, src: src})
	}
	return entries, nil
}

// metaBlobs reads the blob references a manifest section pins, for pinned.
func metaBlobs(m SectionMeta) []string { return m.Blobs }

// pinned returns the distinct blob references of sections, sorted; blobs
// reads a section's.
func pinned[S any](sections []S, blobs func(S) []string) ([]blob.Ref, error) {
	var hexes []string
	for _, s := range sections {
		hexes = append(hexes, blobs(s)...)
	}
	slices.Sort(hexes)
	hexes = slices.Compact(hexes)
	refs := make([]blob.Ref, 0, len(hexes))
	for _, h := range hexes {
		r, err := blob.ParseRef(h)
		if err != nil {
			return nil, &ManifestError{Reason: "a section pins an invalid blob reference", Err: err}
		}
		refs = append(refs, r)
	}
	return refs, nil
}

func writeTar(w io.Writer, entries []entry) error {
	tw := tar.NewWriter(w)
	for _, e := range entries {
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     e.name,
			Size:     e.size,
			Mode:     0o644,
			ModTime:  time.Unix(0, 0),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write snapshot entry %q: %w", e.name, err)
		}
		if err := writeBody(tw, e); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish snapshot tar: %w", err)
	}
	return nil
}

func writeBody(w io.Writer, e entry) (err error) {
	if e.src == nil {
		if _, err := w.Write(e.data); err != nil {
			return fmt.Errorf("write snapshot entry %q: %w", e.name, err)
		}
		return nil
	}
	rc, err := e.src.Export(e.ref)
	if err != nil {
		return &EntryError{Name: e.name, Defect: UnreadableBlob, Err: err}
	}
	// A spilled or disk blob is an open file, whose Close can fail.
	defer func() {
		if cerr := rc.Close(); cerr != nil {
			err = errors.Join(err, &EntryError{Name: e.name, Defect: UnreadableBlob, Err: cerr})
		}
	}()
	src := &sourceReader{r: io.LimitReader(rc, e.size+1)}
	n, err := io.Copy(w, src)
	switch {
	case src.err != nil:
		return &EntryError{Name: e.name, Defect: UnreadableBlob, Err: src.err}
	case errors.Is(err, tar.ErrWriteTooLong):
		return &EntryError{Name: e.name, Defect: UnreadableBlob, Err: fmt.Errorf("read more than the %d bytes it reported", e.size)}
	case err != nil:
		return fmt.Errorf("write snapshot entry %q: %w", e.name, err)
	}
	if n != e.size {
		return &EntryError{Name: e.name, Defect: UnreadableBlob, Err: fmt.Errorf("read %d bytes, expected %d", n, e.size)}
	}
	return nil
}

// sourceReader keeps the error its reader failed with, so a copy tells a
// blob it cannot read from an archive it cannot write.
type sourceReader struct {
	r   io.Reader
	err error
}

func (s *sourceReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		s.err = err
	}
	return n, err
}
