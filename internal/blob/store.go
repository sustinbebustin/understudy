package blob

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
)

// DefaultSpillAbove is the size above which a memory-mode store keeps a blob
// on disk instead of in memory.
const DefaultSpillAbove int64 = 8 << 20

// MaxSpillAbove is the largest spill threshold NewMemory accepts. A larger
// one would let a single in-memory body grow without a useful bound.
const MaxSpillAbove int64 = 1 << 30

const (
	blobDir = "sha256"
	tmpDir  = "tmp"
)

// Store holds content-addressed blobs with reference counts. A disk store
// keeps every blob under its root; a memory store keeps blobs up to its spill
// threshold in memory and larger ones in a private temporary directory.
//
// Store's mutex is a leaf lock. The bytes of a Put are written, and in a disk
// store synced, to tmp/ before it is taken; under it run only the file
// operations that must be atomic with the index: the rename that publishes a
// blob, the rename that detaches a deleted blob from sha256/ into tmp/
// (Release, Prune or a refused Import), and the open in Open and Range. So a
// concurrent Put of the same bytes never renames onto a file being removed
// and an open never races the removal of its blob. No sync and no unlink runs
// under it: Sync makes the renames durable without the mutex, and detached
// blobs and duplicate temporary files are deleted after unlocking, so Pin and
// Retain, which run under the snapshot gate and cell locks, never wait on a
// disk flush or on unlinking a large file.
type Store struct {
	root *os.Root
	mode storeMode

	mu      sync.Mutex
	entries map[Ref]*entry
}

// storeMode says where a store keeps blobs and who owns its root.
//
//sumtype:decl
type storeMode interface{ isStoreMode() }

// memoryMode keeps blobs of at most limit bytes in memory and larger ones
// under dir, a temporary directory the store owns and Close removes.
type memoryMode struct {
	limit int64
	dir   string
}

// diskMode keeps every blob under a root the caller owns.
type diskMode struct{}

func (memoryMode) isStoreMode() {}
func (diskMode) isStoreMode()   {}

type entry struct {
	refs  int
	size  int64
	place placement
}

// placement says where a blob's bytes live.
//
//sumtype:decl
type placement interface{ isPlacement() }

type inMemory struct{ b []byte }

type onDisk struct{}

func (inMemory) isPlacement() {}
func (onDisk) isPlacement()   {}

// NewMemory returns a store that keeps blobs of at most spillAbove bytes in
// memory and larger ones in a new temporary directory under spillDir (the
// system temp directory when empty). A spillAbove of zero or less means
// DefaultSpillAbove; one above MaxSpillAbove is refused with
// *SpillAboveError. Close removes the temporary directory.
func NewMemory(spillDir string, spillAbove int64) (*Store, error) {
	switch {
	case spillAbove <= 0:
		spillAbove = DefaultSpillAbove
	case spillAbove > MaxSpillAbove:
		return nil, &SpillAboveError{Value: spillAbove}
	}
	dir, err := os.MkdirTemp(spillDir, "understudy-blobs-")
	if err != nil {
		return nil, fmt.Errorf("create blob spill directory under %q: %w", spillDir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open blob spill directory %q: %w", dir, err), os.RemoveAll(dir))
	}
	if err := initLayout(root); err != nil {
		return nil, errors.Join(err, root.Close(), os.RemoveAll(dir))
	}
	return &Store{root: root, mode: memoryMode{limit: spillAbove, dir: dir}, entries: map[Ref]*entry{}}, nil
}

// NewDisk returns a store that keeps every blob under root as sha256/<hex>.
// Blobs already there (a persisted data directory) are indexed with no
// references, without re-hashing; loading state retains the ones it uses and
// Prune deletes the rest. Skipping the re-hash is safe after a crash or power
// loss because a disk store's Put syncs the blob file before renaming it into
// sha256/, so a name under sha256/ never points at partly written bytes; a
// rename not yet made durable by Sync is lost whole. Leftover temporary files
// from an interrupted Put are removed. The caller owns root and closes it
// after Close.
func NewDisk(root *os.Root) (*Store, error) {
	if err := root.RemoveAll(tmpDir); err != nil {
		return nil, fmt.Errorf("clear temporary blobs in %q: %w", root.Name(), err)
	}
	if err := initLayout(root); err != nil {
		return nil, err
	}
	if err := syncDir(root, "."); err != nil {
		return nil, fmt.Errorf("sync blob directory %q: %w", root.Name(), err)
	}
	s := &Store{root: root, mode: diskMode{}, entries: map[Ref]*entry{}}
	existing, err := fs.ReadDir(root.FS(), blobDir)
	if err != nil {
		return nil, fmt.Errorf("list blobs in %q: %w", root.Name(), err)
	}
	for _, de := range existing {
		ref, err := ParseRef(de.Name())
		if err != nil || !de.Type().IsRegular() {
			return nil, &UnexpectedFileError{Name: blobDir + "/" + de.Name()}
		}
		info, err := de.Info()
		if err != nil {
			return nil, fmt.Errorf("stat blob %s in %q: %w", ref, root.Name(), err)
		}
		s.entries[ref] = &entry{size: info.Size(), place: onDisk{}}
	}
	return s, nil
}

// Put stores the bytes of r and returns their Ref and size, holding one
// reference for the caller. The digest is computed while the bytes are
// written, so memory stays bounded whatever the size. sizeHint is the
// expected size, or negative when unknown; it only steers where the bytes are
// buffered and is never trusted as the real size.
func (s *Store) Put(ctx context.Context, r io.Reader, sizeHint int64) (Ref, int64, error) {
	ref, n, _, err := s.put(ctx, r, sizeHint, 1)
	return ref, n, err
}

// Open returns a reader over the whole blob.
func (s *Store) Open(ref Ref) (io.ReadSeekCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[ref]
	if !ok {
		return nil, &NotFoundError{Ref: ref}
	}
	return s.openLocked(ref, e)
}

// Range returns a reader over length bytes of the blob starting at offset.
// The range must lie within the blob (*RangeError otherwise); resolving
// HTTP range semantics is the caller's job.
func (s *Store) Range(ref Ref, offset, length int64) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[ref]
	if !ok {
		return nil, &NotFoundError{Ref: ref}
	}
	if offset < 0 || length < 0 || offset > e.size || length > e.size-offset {
		return nil, &RangeError{Ref: ref, Offset: offset, Length: length, Size: e.size}
	}
	br, err := s.openLocked(ref, e)
	if err != nil {
		return nil, err
	}
	return readCloser{Reader: io.NewSectionReader(br, offset, length), Closer: br}, nil
}

// Size returns the blob's size in bytes.
func (s *Store) Size(ref Ref) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[ref]
	if !ok {
		return 0, &NotFoundError{Ref: ref}
	}
	return e.size, nil
}

// Retain adds a reference to a stored blob.
func (s *Store) Retain(ref Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[ref]
	if !ok {
		return &NotFoundError{Ref: ref}
	}
	e.refs++
	return nil
}

// Release drops a reference. The last release deletes the blob.
func (s *Store) Release(ref Ref) error {
	return s.detaching(func() ([]detached, error) {
		e, ok := s.entries[ref]
		switch {
		case !ok:
			return nil, &NotFoundError{Ref: ref}
		case e.refs == 0:
			return nil, &NotRetainedError{Ref: ref}
		}
		e.refs--
		if e.refs > 0 {
			return nil, nil
		}
		return s.detachLocked(ref, e)
	})
}

// Prune deletes every blob with no references: blobs NewDisk indexed or
// Import added that no loaded state retained, including those an Import that
// failed part-way left behind. The snapshot load and the --persist startup
// call it once they have retained every blob the loaded state names; calling
// it before that retain pass ends deletes blobs the state still needs.
func (s *Store) Prune() error {
	return s.detaching(func() ([]detached, error) {
		var ds []detached
		var errs []error
		for ref, e := range s.entries {
			if e.refs == 0 {
				d, err := s.detachLocked(ref, e)
				ds = append(ds, d...)
				errs = append(errs, err)
			}
		}
		return ds, errors.Join(errs...)
	})
}

// Pin retains every ref at once, all or nothing, and returns the function
// that releases them. The snapshot cut pins the blobs its sections name so
// they outlive concurrent deletes until the archive is written. Pin does no
// I/O and the store's mutex is never held across a sync, so it is safe under
// the gate; unpin may delete files and is called after the gate is released.
// Calling unpin again does nothing.
func (s *Store) Pin(refs []Ref) (unpin func() error, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ref := range refs {
		if _, ok := s.entries[ref]; !ok {
			return nil, &NotFoundError{Ref: ref}
		}
	}
	for _, ref := range refs {
		s.entries[ref].refs++
	}
	held := slices.Clone(refs)
	return sync.OnceValue(func() error {
		errs := make([]error, 0, len(held))
		for _, ref := range held {
			errs = append(errs, s.Release(ref))
		}
		return errors.Join(errs...)
	}), nil
}

// Sync makes a disk store's sha256/ directory durable: every blob published,
// and every blob deleted, before Sync was called survives a crash or power
// loss. A deleted blob's file may outlive a crash in tmp/, which NewDisk
// clears. The --persist checkpoint calls it after releasing the gate and before
// renaming the archive into place, so every blob the archive names is durable
// before the archive is. It does not take the store's mutex. A memory store's
// blobs do not outlive the process, so Sync does nothing there.
func (s *Store) Sync() error {
	switch s.mode.(type) {
	case memoryMode:
		return nil
	case diskMode:
		if err := syncDir(s.root, blobDir); err != nil {
			return fmt.Errorf("sync blob directory %q: %w; blobs stored since the last sync may not survive a crash", s.root.Name()+"/"+blobDir, err)
		}
		return nil
	}
	return nil
}

// Refs returns every stored blob, sorted.
func (s *Store) Refs() []Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.SortedFunc(maps.Keys(s.entries), func(a, b Ref) int { return strings.Compare(a.hex, b.hex) })
}

// Import copies every sha256/<hex> blob in src into the store with no
// references; loading state retains the ones it uses. Each file is re-hashed
// and refused with *CorruptBlobError when its bytes do not match its name.
// A src with no sha256 directory (a snapshot whose state holds no blobs)
// imports nothing. Blobs imported before a failure stay in the store,
// unreferenced, until Prune.
func (s *Store) Import(ctx context.Context, src fs.FS) error {
	des, err := fs.ReadDir(src, blobDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list blobs to import: %w", err)
	}
	for _, de := range des {
		name := blobDir + "/" + de.Name()
		want, err := ParseRef(de.Name())
		if err != nil || !de.Type().IsRegular() {
			return &UnexpectedFileError{Name: name}
		}
		if err := s.importOne(ctx, src, name, want); err != nil {
			return err
		}
	}
	return nil
}

// Export returns a reader over the whole blob for the snapshot writer.
func (s *Store) Export(ref Ref) (io.ReadCloser, error) {
	return s.Open(ref)
}

// Close removes a memory store's temporary directory and every blob in it.
// A memory store is unusable afterwards. Closing a disk store does nothing:
// its root's owner ends its use.
func (s *Store) Close() error {
	switch m := s.mode.(type) {
	case memoryMode:
		return errors.Join(s.root.Close(), os.RemoveAll(m.dir))
	case diskMode:
		return nil
	}
	return nil
}

func (s *Store) importOne(ctx context.Context, src fs.FS, name string, want Ref) error {
	f, err := src.Open(name)
	if err != nil {
		return fmt.Errorf("open blob %s to import: %w", name, err)
	}
	defer f.Close()
	hint := int64(-1)
	if info, err := f.Stat(); err == nil {
		hint = info.Size()
	}
	got, _, created, err := s.put(ctx, f, hint, 0)
	if err != nil {
		return fmt.Errorf("import blob %s: %w", name, err)
	}
	if got != want {
		corrupt := &CorruptBlobError{Name: name, Got: got}
		if !created {
			// The bytes were already stored, by an earlier import or as a
			// blob NewDisk indexed; they are valid under their own name.
			return corrupt
		}
		return errors.Join(corrupt, s.dropUnreferenced(got))
	}
	return nil
}

// put stores r with refs initial references (one for Put, none for Import).
// It reports created when the bytes were not stored before.
func (s *Store) put(ctx context.Context, r io.Reader, sizeHint int64, refs int) (ref Ref, size int64, created bool, err error) {
	h := sha256.New()
	src := io.TeeReader(ctxReader{ctx: ctx, r: r}, h)
	var head [][]byte
	if m, ok := s.mode.(memoryMode); ok && sizeHint <= m.limit {
		chunks, n, complete, err := spool(src, m.limit, sizeHint)
		if err != nil {
			return Ref{}, 0, false, err
		}
		if complete {
			ref := refOf(h)
			return ref, n, s.publish(ref, n, inMemory{b: join(chunks)}, refs), nil
		}
		head = chunks
	}
	tmp, n, err := s.writeTemp(head, src)
	if err != nil {
		return Ref{}, 0, false, err
	}
	ref = refOf(h)
	created, err = s.publishFile(ref, n, tmp, refs)
	if err != nil {
		return Ref{}, 0, false, err
	}
	return ref, n, created, nil
}

// writeTemp writes head then the rest of src to a new file under tmp/. A
// disk store syncs the file before closing it, so the rename that publishes
// it never makes a name for bytes that are not yet on disk.
func (s *Store) writeTemp(head [][]byte, src io.Reader) (name string, size int64, err error) {
	name = tmpDir + "/" + rand.Text()
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("create temporary blob file: %w", err)
	}
	size, err = writeAll(f, head, src)
	if _, durable := s.mode.(diskMode); durable && err == nil {
		if serr := f.Sync(); serr != nil {
			err = fmt.Errorf("sync temporary blob file: %w", serr)
		}
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("close temporary blob file: %w", cerr)
	}
	if err != nil {
		return "", 0, errors.Join(err, s.root.Remove(name))
	}
	return name, size, nil
}

// publish indexes an in-memory blob, or adds refs to the stored one. It
// reports whether it created the entry.
func (s *Store) publish(ref Ref, size int64, place placement, refs int) (created bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[ref]; ok {
		e.refs += refs
		return false
	}
	s.entries[ref] = &entry{refs: refs, size: size, place: place}
	return true
}

// publishFile renames tmp into place, or discards it when the bytes are
// already stored. It reports whether it created the entry. The rename is
// made durable by Sync, not here, so no sync runs under the mutex; a
// discarded duplicate, which nothing else names, is deleted after unlocking.
func (s *Store) publishFile(ref Ref, size int64, tmp string, refs int) (created bool, err error) {
	created, err = func() (bool, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if e, ok := s.entries[ref]; ok {
			e.refs += refs
			return false, nil
		}
		if err := s.root.Rename(tmp, ref.path()); err != nil {
			return false, fmt.Errorf("publish blob %s: %w", ref, err)
		}
		s.entries[ref] = &entry{refs: refs, size: size, place: onDisk{}}
		return true, nil
	}()
	switch {
	case err != nil:
		return false, errors.Join(err, s.root.Remove(tmp))
	case created:
		return true, nil
	}
	if err := s.root.Remove(tmp); err != nil {
		// The Put fails, so it must not keep the references it added.
		errs := []error{fmt.Errorf("discard duplicate temporary blob for %s: %w", ref, err)}
		for range refs {
			errs = append(errs, s.Release(ref))
		}
		return false, errors.Join(errs...)
	}
	return false, nil
}

// dropUnreferenced deletes a blob nobody references, undoing a refused
// import without touching a blob some state already holds.
func (s *Store) dropUnreferenced(ref Ref) error {
	return s.detaching(func() ([]detached, error) {
		e, ok := s.entries[ref]
		if !ok || e.refs > 0 {
			return nil, nil
		}
		return s.detachLocked(ref, e)
	})
}

// detached is a blob file moved from sha256/ to tmp/ under s.mu, to be deleted
// once the mutex is released.
type detached struct {
	ref  Ref
	name string
}

// detaching runs f under s.mu and then, without the mutex, deletes the files f
// detached. Unlinking a multi-GiB file can take long enough to stall Pin and
// Retain, which run under the snapshot gate and cell locks; the rename that
// detaches it is a quick metadata operation that still orders against a
// concurrent Put publishing the same bytes.
func (s *Store) detaching(f func() ([]detached, error)) error {
	ds, err := func() ([]detached, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return f()
	}()
	errs := []error{err}
	for _, d := range ds {
		if rerr := s.root.Remove(d.name); rerr != nil {
			errs = append(errs, fmt.Errorf("delete blob %s from %s: %w; the file stays there until the store is next opened or closed", d.ref, d.name, rerr))
		}
	}
	return errors.Join(errs...)
}

// detachLocked forgets a blob and moves its file, if it has one, to tmp/ for
// detaching to delete. The caller holds s.mu.
func (s *Store) detachLocked(ref Ref, e *entry) ([]detached, error) {
	delete(s.entries, ref)
	switch e.place.(type) {
	case inMemory:
		return nil, nil
	case onDisk:
		d := detached{ref: ref, name: tmpDir + "/" + rand.Text()}
		if err := s.root.Rename(ref.path(), d.name); err != nil {
			return nil, fmt.Errorf("delete blob %s: %w; the file is left behind but no longer indexed", ref, err)
		}
		return []detached{d}, nil
	}
	return nil, nil
}

func (s *Store) openLocked(ref Ref, e *entry) (blobReader, error) {
	switch p := e.place.(type) {
	case inMemory:
		return nopCloser{bytes.NewReader(p.b)}, nil
	case onDisk:
		f, err := s.root.Open(ref.path())
		if err != nil {
			return nil, fmt.Errorf("open blob %s: %w", ref, err)
		}
		return f, nil
	}
	return nil, &NotFoundError{Ref: ref}
}

func initLayout(root *os.Root) error {
	for _, dir := range []string{blobDir, tmpDir} {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s in blob directory %q: %w", dir, root.Name(), err)
		}
	}
	return nil
}

// syncDir flushes the directory name under root, making the entries created
// or renamed in it durable.
func syncDir(root *os.Root, name string) error {
	d, err := root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// writeAll writes head then the rest of src to w. Errors come back already
// labelled: tempWriter labels write failures and ctxReader labels read ones.
func writeAll(w io.Writer, head [][]byte, src io.Reader) (int64, error) {
	dst := tempWriter{w: w}
	var n int64
	for _, c := range head {
		k, err := dst.Write(c)
		n += int64(k)
		if err != nil {
			return n, err
		}
	}
	k, err := io.Copy(dst, src)
	return n + k, err
}

// tempWriter labels write failures, so a full disk reads differently from a
// body that could not be read, which io.Copy would otherwise return alike.
type tempWriter struct{ w io.Writer }

func (t tempWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if err != nil {
		return n, fmt.Errorf("write temporary blob file: %w", err)
	}
	return n, nil
}

type ctxReader struct {
	ctx context.Context //nolint:containedctx // a reader that honours cancellation between reads has to carry it
	r   io.Reader
}

// Read labels every failure as a body read, so a client that disconnects or a
// cancelled request is never reported as a failure to write the blob. io.EOF
// stays unwrapped: io.Copy and spool compare against it to see the body end.
func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, fmt.Errorf("read blob body: %w", err)
	}
	n, err := c.r.Read(p)
	if err != nil && err != io.EOF {
		return n, fmt.Errorf("read blob body: %w", err)
	}
	return n, err
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

type readCloser struct {
	io.Reader
	io.Closer
}

// blobReader is what both placements open to: *os.File and nopCloser.
type blobReader interface {
	io.ReadSeekCloser
	io.ReaderAt
}
