package snapshot

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// ReadOptions configures Read.
type ReadOptions struct {
	// MaxBytes caps the archive's size once decompressed, headers
	// included. Read holds the whole archive in memory, so it must be
	// positive.
	MaxBytes int64
	// Schemas is the running binary's state schema version per service.
	// When it is non-nil, every section must belong to a listed service and
	// carry its version, or Read fails with *SchemaError. Nil accepts any
	// version, for inspecting and diffing archives without loading them.
	Schemas map[ref.ServiceID]int
}

// gzipMagic starts every gzip stream (RFC 1952).
const gzipMagic = "\x1f\x8b"

// Read parses an archive Write produced, gzipped or not, reading the stream
// to its end. It refuses, with nothing returned and in this order:
//   - an archive larger than MaxBytes decompressed, counting the stream and
//     every entry's contents (*SizeError);
//   - an empty stream, a stream that is not a readable tar, a gzip stream
//     whose header, checksum or length does not check out, and anything but
//     zero padding after the tar's end (*StreamError);
//   - a format newer than Format, naming both formats and the writer
//     (*FormatError), checked before anything else in the archive;
//   - an entry name that is not local (filepath.IsLocal) and clean
//     (fs.ValidPath), a non-regular entry (sparse files included) or a
//     repeated name (*EntryError);
//   - with Schemas set, a section of an unknown service or schema version,
//     naming both versions and the writer (*SchemaError);
//   - a section or blob whose bytes do not match its digest, a listed entry
//     the archive lacks, an entry the manifest does not account for and a
//     blob entry in an archive whose blobs are external (*EntryError).
//
// The returned Archive's Blobs serves embedded blobs from memory; it is nil
// when the manifest places the blobs in the blob store (ExternalBlobs).
func Read(r io.Reader, opts ReadOptions) (*Archive, error) {
	if opts.MaxBytes <= 0 {
		return nil, &ConfigError{Reason: fmt.Sprintf("ReadOptions.MaxBytes is %d; it must be a positive byte limit", opts.MaxBytes)}
	}
	br := bufio.NewReader(r)
	var src io.Reader = br
	if magic, err := br.Peek(len(gzipMagic)); err == nil && string(magic) == gzipMagic {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, &StreamError{Reason: "has a gzip signature but no valid gzip header", Err: err}
		}
		src = zr
	}
	cr := &capReader{r: src, left: opts.MaxBytes, max: opts.MaxBytes}
	files, defect, err := readEntries(tar.NewReader(cr), cr)
	if err != nil {
		return nil, err
	}
	// tar.Reader reports an empty stream as the end of the archive.
	if cr.left == cr.max {
		return nil, &StreamError{Reason: "is empty"}
	}
	if err := drain(cr); err != nil {
		return nil, err
	}
	// A newer format may lay out its entries differently, so it is refused
	// as such before any entry is judged against this format's layout.
	if raw, ok := files[manifestName]; ok {
		if format, writer, err := peekFormat(raw); err == nil && format > Format {
			return nil, &FormatError{Archive: format, Reader: Format, Writer: writer}
		}
	}
	if defect != nil {
		return nil, defect
	}
	return assemble(files, opts.Schemas)
}

// capReader fails with *SizeError once more than max bytes were read.
type capReader struct {
	r         io.Reader
	left, max int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left < int64(len(p)) {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return 0, &SizeError{Max: c.max}
	}
	return n, err
}

// readEntries reads every regular entry into memory, up to the tar's
// end-of-archive marker. It returns the first structural defect (a
// non-local name, a non-regular entry, a repeated name) apart instead of
// stopping at it: manifest.json sorts after blobs/ and most other entries,
// and Read refuses a newer format as such whatever else is wrong. Nothing
// touches disk, so reading past a defective entry is safe.
//
// An entry's contents are counted against cr's budget before they are read.
// Without sparse files they never exceed the stream bytes cr counts, but a
// sparse entry expands holes it does not read from the stream, so sparse
// entries are refused as not regular and skipped unread.
func readEntries(tr *tar.Reader, cr *capReader) (files map[string][]byte, defect *EntryError, err error) {
	files = map[string][]byte{}
	note := func(name string, d EntryDefect) {
		if defect == nil {
			defect = &EntryError{Name: name, Defect: d}
		}
	}
	for {
		hdr, err := tr.Next()
		switch {
		case errors.Is(err, io.EOF):
			return files, defect, nil
		case hdr != nil && errors.Is(err, tar.ErrInsecurePath):
			note(hdr.Name, NonLocalName)
			continue
		case err != nil:
			return nil, nil, readError("is not a readable tar stream", err)
		case !filepath.IsLocal(hdr.Name) || !fs.ValidPath(hdr.Name):
			note(hdr.Name, NonLocalName)
			continue
		case hdr.Typeflag != tar.TypeReg || sparse(hdr):
			note(hdr.Name, NotRegular)
			continue
		}
		if _, dup := files[hdr.Name]; dup {
			note(hdr.Name, DuplicateEntry)
			continue
		}
		if hdr.Size > cr.left {
			return nil, nil, &SizeError{Max: cr.max}
		}
		data, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
		if err != nil {
			return nil, nil, readError("is not a readable tar stream", err)
		}
		files[hdr.Name] = data
	}
}

// sparse reports whether hdr is a PAX sparse file, which archive/tar reports
// as a regular entry whose Size is the expanded size.
func sparse(hdr *tar.Header) bool {
	for k := range hdr.PAXRecords {
		if strings.HasPrefix(k, "GNU.sparse.") {
			return true
		}
	}
	return false
}

// drain reads the stream to its end after the tar's end-of-archive marker.
// Only zero padding may follow the marker, as tar writers pad to a record
// size. Reading a gzip stream to its end checks its CRC-32 and length
// trailer, and refuses anything after the gzip member but another member.
func drain(r io.Reader) error {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if slices.ContainsFunc(buf[:n], func(b byte) bool { return b != 0 }) {
			return &StreamError{Reason: "has data after the end of its tar stream"}
		}
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return readError("cannot be read to its end after its last entry", err)
		}
	}
}

// readError keeps a *SizeError and reports any other failure to read the
// stream as a *StreamError.
func readError(reason string, err error) error {
	if se, ok := errors.AsType[*SizeError](err); ok {
		return se
	}
	return &StreamError{Reason: reason, Err: err}
}

// assemble checks the entries against the manifest and builds the Archive.
// Every entry must be accounted for exactly once.
func assemble(files map[string][]byte, schemas map[ref.ServiceID]int) (*Archive, error) {
	raw, ok := files[manifestName]
	if !ok {
		return nil, &EntryError{Name: manifestName, Defect: MissingEntry}
	}
	delete(files, manifestName)
	m, err := decodeManifest(raw)
	if err != nil {
		return nil, err
	}
	if schemas != nil {
		if err := checkSchemas(m, schemas); err != nil {
			return nil, err
		}
	}
	a := &Archive{Manifest: m}
	for _, meta := range m.Sections {
		data, err := take(files, meta.Path, meta.SHA256)
		if err != nil {
			return nil, err
		}
		s := store.Section{Path: meta.Path, Version: meta.Version, Data: data, Blobs: meta.Blobs}
		if meta.Late {
			a.Late = append(a.Late, s)
		} else {
			a.Sections = append(a.Sections, s)
		}
	}
	a.Faults = files[faultsName]
	a.Journal = files[journalName]
	delete(files, faultsName)
	delete(files, journalName)
	if m.Blobs == EmbedBlobs {
		if a.Blobs, err = takeBlobs(files, m.Sections); err != nil {
			return nil, err
		}
	}
	if rest := slices.Sorted(maps.Keys(files)); len(rest) > 0 {
		var defect EntryDefect
		switch {
		case strings.HasPrefix(rest[0], blobsDir) && m.Blobs == ExternalBlobs:
			defect = ExternalBlobEmbedded
		case strings.HasPrefix(rest[0], servicesDir) || strings.HasPrefix(rest[0], blobsDir):
			defect = UnlistedEntry
		default:
			defect = UnexpectedEntry
		}
		return nil, &EntryError{Name: rest[0], Defect: defect}
	}
	return a, nil
}

// takeBlobs removes the blob entries sections pin from files.
func takeBlobs(files map[string][]byte, sections []SectionMeta) (memBlobs, error) {
	refs, err := pinned(sections, metaBlobs)
	if err != nil {
		return nil, err
	}
	blobs := make(memBlobs, len(refs))
	for _, r := range refs {
		data, err := take(files, blobsDir+r.String(), r.String())
		if err != nil {
			return nil, err
		}
		blobs[r] = data
	}
	return blobs, nil
}

// take removes the named entry from files and checks it hashes to digest.
func take(files map[string][]byte, name, digest string) ([]byte, error) {
	data, ok := files[name]
	if !ok {
		return nil, &EntryError{Name: name, Defect: MissingEntry}
	}
	delete(files, name)
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != digest {
		return nil, &EntryError{Name: name, Defect: DigestMismatch}
	}
	return data, nil
}

// checkSchemas refuses a section of a service schemas does not list or at a
// version other than the listed one.
func checkSchemas(m Manifest, schemas map[ref.ServiceID]int) error {
	for _, s := range m.Sections {
		svc, err := sectionService(s.Path)
		if err != nil {
			return err
		}
		running, known := schemas[svc]
		if !known || running != s.Version {
			return &SchemaError{Path: s.Path, Service: svc, Section: s.Version, Running: running, Known: known, Writer: m.Writer}
		}
	}
	return nil
}

// memBlobs serves the blobs Read found.
type memBlobs map[blob.Ref][]byte

func (b memBlobs) Size(r blob.Ref) (int64, error) {
	data, ok := b[r]
	if !ok {
		return 0, &blob.NotFoundError{Ref: r}
	}
	return int64(len(data)), nil
}

func (b memBlobs) Export(r blob.Ref) (io.ReadCloser, error) {
	data, ok := b[r]
	if !ok {
		return nil, &blob.NotFoundError{Ref: r}
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
