package store

import (
	"io/fs"
	"maps"
	"slices"
	"strings"
	"sync"
)

// sectionExt is the extension of every section path; section data is JSON.
const sectionExt = ".json"

// Zeroer is implemented by a state whose pointer can report that it holds
// nothing worth saving. Export skips a cell whose state's IsZero reports
// true, so an untouched or emptied partition leaves no section behind.
//
// Export finds it by type assertion on *S, so an implementing package should
// pin the contract with a compile-time assertion such as
// var _ store.Zeroer = (*State)(nil).
type Zeroer interface {
	IsZero() bool
}

// BlobHolder is implemented by a state whose pointer holds references to blob
// store entries, such as object bodies or code packages. Export sorts the
// result of BlobRefs into Section.Blobs, which is how a snapshot knows which
// blobs to archive beside the section.
//
// Export finds it by type assertion on *S, and a state that holds blobs but
// does not implement BlobHolder saves an archive whose sections name blobs
// the archive lacks. An implementing package should therefore pin the
// contract with a compile-time assertion such as
// var _ store.BlobHolder = (*State)(nil).
type BlobHolder interface {
	BlobRefs() []string
}

// Partitioned lazily creates one cell per key. Keys are aws.Scope for most
// services, aws.AccountID for IAM and STS, a bucket name for S3 buckets and a
// single key for indexes.
//
// Export, Import and Reset do not take the gate: the snapshot cut, load and
// reset call them while holding Gate.Exclusive, which is what makes the cut
// consistent across every cell.
type Partitioned[K comparable, S any] struct {
	gate  *Gate
	codec Codec
	init  func(K) S

	mu    sync.Mutex
	cells map[K]*Cell[S]
}

// NewPartitioned returns an empty Partitioned whose cells share gate, whose
// sections are serialized with codec and whose new cells start from init(k).
//
// Import decodes onto init(k) as well, so init may make empty maps and set
// fields derived from k, but must not seed map entries or give a non-zero
// value to an omitempty field that the state can later remove or clear:
// Export omits the removal and Import would bring the initial value back.
func NewPartitioned[K comparable, S any](gate *Gate, codec Codec, init func(K) S) *Partitioned[K, S] {
	return &Partitioned[K, S]{gate: gate, codec: codec, init: init, cells: map[K]*Cell[S]{}}
}

// For returns the cell for k, creating it from init(k) on first use. The
// lookup takes no gate, so Delete, Reset or Import may retire the returned
// cell before the caller's Update runs; see Cell.Retired for how a caller
// detects that and re-resolves.
func (p *Partitioned[K, S]) For(k K) *Cell[S] {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.cells[k]; ok {
		return c
	}
	c := p.newCell(p.init(k))
	p.cells[k] = c
	return c
}

// Lookup returns the cell for k if it exists. It never creates one, so reads
// of a partition nobody wrote to leave no trace. Like For, it takes no gate,
// and the returned cell may be retired by the time the caller uses it.
func (p *Partitioned[K, S]) Lookup(k K) (*Cell[S], bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.cells[k]
	return c, ok
}

// Keys returns every key with a cell, sorted by its encoded form under kc.
func (p *Partitioned[K, S]) Keys(kc KeyCodec[K]) []K {
	p.mu.Lock()
	keys := slices.Collect(maps.Keys(p.cells))
	p.mu.Unlock()
	return sortedByEncoding(kc, keys)
}

// Delete drops and retires the cell for k. A later For starts again from
// init(k).
//
// Delete takes no gate, so it must run inside the Update (or kernel.Write) fn
// of the cell for k, after Retired reported false there. That cell is then
// the one at k, and the shared side of the gate the fn holds keeps the
// snapshot cut, load and reset on one side of the deletion and of the write
// that decided on it. Called after that fn returned, Delete could land
// between two services' cuts, or remove a cell a load put at k in between
// and lose the loaded state. Delete only takes the map lock, which is never
// held while a cell lock is taken, so calling it under the cell lock is safe.
func (p *Partitioned[K, S]) Delete(k K) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.cells[k]; ok {
		c.retired.Store(true)
		delete(p.cells, k)
	}
}

// Reset drops and retires every cell whose key keep rejects.
func (p *Partitioned[K, S]) Reset(keep func(K) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	maps.DeleteFunc(p.cells, func(k K, c *Cell[S]) bool {
		if keep(k) {
			return false
		}
		c.retired.Store(true)
		return true
	})
}

// Export serializes every cell to one section, sorted by path, stamped with
// version. When *S is a Zeroer, a cell whose IsZero reports true is skipped.
// When *S is a BlobHolder, its BlobRefs are sorted into Section.Blobs. A key
// whose encoding is empty or not a local path, or two keys encoding to one
// path, fail with *SectionError, so every export that succeeds is one Import
// accepts.
//
// Export does no I/O; the caller holds Gate.Exclusive for a consistent cut.
func (p *Partitioned[K, S]) Export(kc KeyCodec[K], version int) ([]Section, error) {
	p.mu.Lock()
	cells := maps.Clone(p.cells)
	p.mu.Unlock()

	// Walking in key order makes the first failing cell, and so the error,
	// the same on every run. The final sort by path stays: "a" sorts before
	// "a-b" but "a.json" after "a-b.json".
	sections := make([]Section, 0, len(cells))
	for _, k := range sortedByEncoding(kc, slices.Collect(maps.Keys(cells))) {
		s, ok, err := p.exportCell(kc, version, k, cells[k])
		if err != nil {
			return nil, err
		}
		if ok {
			sections = append(sections, s)
		}
	}
	slices.SortFunc(sections, func(a, b Section) int { return strings.Compare(a.Path, b.Path) })
	// A KeyCodec that is not injective would otherwise save an archive that
	// Import refuses.
	for i := 1; i < len(sections); i++ {
		if sections[i].Path == sections[i-1].Path {
			return nil, &SectionError{Path: sections[i].Path, Defect: DuplicateKey}
		}
	}
	return sections, nil
}

// Import decodes sections into new cells, replacing and retiring any cells
// with the same keys. Each section's state is decoded on top of init(k), so
// fields absent from the data keep their initial values and maps init made
// stay non-nil. Decoding merges into those maps, so init must leave empty
// every map, and at its zero value every omitempty field, that the state can
// later clear (see NewPartitioned). It validates every section before
// changing anything: a section with a different version
// (*VersionMismatchError), a path that is not a local, canonical encoding
// of a key, undecodable data or two sections for one key (*SectionError)
// leave the Partitioned unchanged.
//
// The caller holds Gate.Exclusive, normally after Reset.
func (p *Partitioned[K, S]) Import(kc KeyCodec[K], version int, sections []Section) error {
	decoded := make(map[K]*Cell[S], len(sections))
	for _, sec := range sections {
		if sec.Version != version {
			return &VersionMismatchError{Path: sec.Path, Section: sec.Version, Running: version}
		}
		k, err := decodePath(kc, sec.Path)
		if err != nil {
			return err
		}
		if _, dup := decoded[k]; dup {
			return &SectionError{Path: sec.Path, Defect: DuplicateKey}
		}
		s := p.init(k)
		if err := p.codec.Unmarshal(sec.Data, &s); err != nil {
			return &SectionError{Path: sec.Path, Defect: UndecodableState, Err: err}
		}
		decoded[k] = p.newCell(s)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for k, c := range decoded {
		if old, ok := p.cells[k]; ok {
			old.retired.Store(true)
		}
		p.cells[k] = c
	}
	return nil
}

func (p *Partitioned[K, S]) newCell(s S) *Cell[S] {
	return &Cell[S]{gate: p.gate, s: s}
}

// exportCell serializes one cell. It reports false for a zero cell.
func (p *Partitioned[K, S]) exportCell(kc KeyCodec[K], version int, k K, c *Cell[S]) (Section, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if z, ok := any(&c.s).(Zeroer); ok && z.IsZero() {
		return Section{}, false, nil
	}
	enc := kc.Encode(k)
	path := enc + sectionExt
	if err := checkLocal(enc, path); err != nil {
		return Section{}, false, err
	}
	data, err := p.codec.Marshal(&c.s)
	if err != nil {
		return Section{}, false, &SectionError{Path: path, Defect: UnencodableState, Err: err}
	}
	var blobs []string
	if b, ok := any(&c.s).(BlobHolder); ok {
		blobs = slices.Sorted(slices.Values(b.BlobRefs()))
	}
	return Section{Path: path, Version: version, Data: data, Blobs: blobs}, true, nil
}

// decodePath parses a section path back to its key. It refuses the paths
// Export refuses and non-canonical paths, so every imported key exports again
// and two paths can never name one key.
func decodePath[K any](kc KeyCodec[K], path string) (K, error) {
	var zero K
	enc, ok := strings.CutSuffix(path, sectionExt)
	if !ok {
		return zero, &SectionError{Path: path, Defect: MissingExtension}
	}
	if err := checkLocal(enc, path); err != nil {
		return zero, err
	}
	k, err := kc.Decode(enc)
	if err != nil {
		return zero, &SectionError{Path: path, Defect: UndecodablePath, Err: err}
	}
	if kc.Encode(k) != enc {
		return zero, &SectionError{Path: path, Defect: NonCanonicalPath}
	}
	return k, nil
}

// checkLocal refuses an encoded key that is empty, which is how a KeyCodec
// marks a key it cannot represent, or not a local, slash-separated path
// (fs.ValidPath), so no section can name a file outside its service's
// directory in the archive. path is the section path to report.
func checkLocal(enc, path string) error {
	if enc == "" {
		return &SectionError{Path: path, Defect: NoEncoding}
	}
	if !fs.ValidPath(enc) {
		return &SectionError{Path: path, Defect: NonLocalPath}
	}
	return nil
}

func sortedByEncoding[K any](kc KeyCodec[K], keys []K) []K {
	type encoded struct {
		key K
		enc string
	}
	es := make([]encoded, len(keys))
	for i, k := range keys {
		es[i] = encoded{key: k, enc: kc.Encode(k)}
	}
	slices.SortFunc(es, func(a, b encoded) int { return strings.Compare(a.enc, b.enc) })
	out := make([]K, len(es))
	for i, e := range es {
		out[i] = e.key
	}
	return out
}
