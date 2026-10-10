package codestore

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// DefaultMaxUnzipped is Lambda's limit on a function's unzipped code
// (lambda-api.md 4.3).
const DefaultMaxUnzipped int64 = 262_144_000

// zipOverhead is what the default MaxZipped allows beyond MaxUnzipped for
// the zip's own structure: local and central headers of a few hundred
// bytes per entry, so room for a few hundred thousand entries. Lambda's
// 50 MB zipped limit applies to API uploads only, not to packages from
// S3, so it is the caller's to enforce.
const zipOverhead int64 = 64 << 20

const (
	treesDir = "trees"
	tmpDir   = "tmp"

	dirMode  fs.FileMode = 0o755
	fileMode fs.FileMode = 0o644
	execMode fs.FileMode = 0o755
)

// Options bounds one archive. Zero values take the defaults.
type Options struct {
	// MaxUnzipped caps the sum of the entries' uncompressed sizes; zero
	// means DefaultMaxUnzipped.
	MaxUnzipped int64
	// MaxEntry caps one entry's uncompressed size; zero means MaxUnzipped.
	MaxEntry int64
	// MaxZipped caps the package's own bytes, which are spooled to disk
	// before the zip can be read; zero means MaxUnzipped plus 64 MiB for
	// the zip's headers.
	MaxZipped int64
}

func (o Options) resolve() (Options, error) {
	for _, l := range []LimitError{{"MaxUnzipped", o.MaxUnzipped}, {"MaxEntry", o.MaxEntry}, {"MaxZipped", o.MaxZipped}} {
		if l.Value < 0 {
			return o, &l
		}
	}
	if o.MaxUnzipped == 0 {
		o.MaxUnzipped = DefaultMaxUnzipped
	}
	if o.MaxEntry == 0 {
		o.MaxEntry = o.MaxUnzipped
	}
	if o.MaxZipped == 0 {
		o.MaxZipped = o.MaxUnzipped + zipOverhead
	}
	return o, nil
}

// Digest is the SHA-256 of a code package's zip bytes.
type Digest [sha256.Size]byte

// String returns the digest as Lambda's CodeSha256: standard base64.
func (d Digest) String() string { return base64.StdEncoding.EncodeToString(d[:]) }

// Code is one extracted package.
type Code struct {
	// Sha256 is the package's CodeSha256.
	Sha256 Digest
	// Dir is the absolute path of the extracted tree, the source of the
	// read-only /var/task mount.
	Dir string
	// Size is the zip's length in bytes, Lambda's CodeSize.
	Size int64
}

// Store holds extracted code trees under one directory, one per CodeSha256,
// with reference counts. It is safe for concurrent use.
type Store struct {
	root *os.Root
	dir  string

	mu    sync.Mutex
	trees map[Digest]*tree
}

type tree struct {
	refs int
	size int64
}

// Open prepares dir (created when missing) as a store. Trees left by an
// earlier process are removed: they are derived from packages the Lambda
// state keeps, which re-adds them, and nothing references them yet.
func Open(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("code store %s: %w", dir, err)
	}
	if err := os.MkdirAll(abs, dirMode); err != nil {
		return nil, fmt.Errorf("code store: create %s: %w", abs, err)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("code store: open %s: %w", abs, err)
	}
	for _, d := range []string{treesDir, tmpDir} {
		if err := root.RemoveAll(d); err != nil {
			return nil, errors.Join(fmt.Errorf("code store: clear %s: %w", filepath.Join(abs, d), err), root.Close())
		}
		if err := root.Mkdir(d, dirMode); err != nil {
			return nil, errors.Join(fmt.Errorf("code store: create %s: %w", filepath.Join(abs, d), err), root.Close())
		}
	}
	return &Store{root: root, dir: abs, trees: map[Digest]*tree{}}, nil
}

// Close releases the store's directory handle. Trees stay on disk until
// the next Open.
func (s *Store) Close() error { return s.root.Close() }

// PutZip reads a zip package from r, extracts it once per CodeSha256 and
// adds a reference to its tree. Every entry is checked before anything is
// written: names must be local (filepath.IsLocal), symbolic links and
// special files are refused, and uncompressed sizes are capped per entry
// and in total. Modes are normalized: directories 0755, files 0644, and
// 0755 for files with any execute bit and for the top-level bootstrap.
func (s *Store) PutZip(ctx context.Context, r io.Reader, opts Options) (Code, error) {
	opts, err := opts.resolve()
	if err != nil {
		return Code{}, err
	}
	spool, digest, size, err := s.spool(ctx, r, opts.MaxZipped)
	if err != nil {
		return Code{}, err
	}
	defer s.discard(spool)
	if code, ok := s.retain(digest); ok {
		return code, nil
	}
	staged, err := s.extract(ctx, spool, size, opts)
	if err != nil {
		return Code{}, err
	}
	return s.publish(staged, digest, size)
}

// Dir returns the absolute path of the tree for sha.
func (s *Store) Dir(sha Digest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.trees[sha]; !ok {
		return "", &NotFoundError{Sha256: sha}
	}
	return s.treePath(sha), nil
}

// Release drops one reference to sha's tree and removes the tree with the
// last one.
func (s *Store) Release(sha Digest) error {
	s.mu.Lock()
	t, ok := s.trees[sha]
	if !ok {
		s.mu.Unlock()
		return &NotFoundError{Sha256: sha}
	}
	t.refs--
	if t.refs > 0 {
		s.mu.Unlock()
		return nil
	}
	delete(s.trees, sha)
	// Detach the tree under the lock so a concurrent PutZip of the same
	// package extracts afresh instead of finding a tree being deleted.
	detached := path.Join(tmpDir, "released-"+hex.EncodeToString(sha[:])+"-"+randomName())
	err := s.root.Rename(path.Join(treesDir, hex.EncodeToString(sha[:])), detached)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("code store: detach tree %s: %w", sha, err)
	}
	if err := s.root.RemoveAll(detached); err != nil {
		return fmt.Errorf("code store: remove released tree %s: %w", sha, err)
	}
	return nil
}

func (s *Store) treePath(sha Digest) string {
	return filepath.Join(s.dir, treesDir, hex.EncodeToString(sha[:]))
}

func (s *Store) retain(sha Digest) (Code, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.trees[sha]
	if !ok {
		return Code{}, false
	}
	t.refs++
	return Code{Sha256: sha, Dir: s.treePath(sha), Size: t.size}, true
}

// publish moves a staged tree into place, or adds a reference to the tree
// a concurrent PutZip of the same package published first.
func (s *Store) publish(staged string, sha Digest, size int64) (Code, error) {
	s.mu.Lock()
	if t, ok := s.trees[sha]; ok {
		t.refs++
		s.mu.Unlock()
		return Code{Sha256: sha, Dir: s.treePath(sha), Size: size}, s.removeStaged(staged)
	}
	if err := s.root.Rename(staged, path.Join(treesDir, hex.EncodeToString(sha[:]))); err != nil {
		s.mu.Unlock()
		return Code{}, errors.Join(fmt.Errorf("code store: publish tree %s: %w", sha, err), s.removeStaged(staged))
	}
	s.trees[sha] = &tree{refs: 1, size: size}
	s.mu.Unlock()
	return Code{Sha256: sha, Dir: s.treePath(sha), Size: size}, nil
}

func (s *Store) removeStaged(staged string) error {
	if err := s.root.RemoveAll(staged); err != nil {
		return fmt.Errorf("code store: remove staging directory %s: %w", staged, err)
	}
	return nil
}

// spool copies the package to a temporary file, hashing it on the way: the
// zip reader needs random access and the digest names the tree. It reads at
// most one byte past limit, so an oversized package never fills the disk.
func (s *Store) spool(ctx context.Context, r io.Reader, limit int64) (*os.File, Digest, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, Digest{}, 0, err
	}
	f, err := s.root.OpenFile(path.Join(tmpDir, "upload-"+randomName()+".zip"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, Digest{}, 0, fmt.Errorf("code store: create spool file: %w", err)
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, limit+1))
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.discard(f)
		return nil, Digest{}, 0, fmt.Errorf("code store: read code package: %w", err)
	}
	if size > limit {
		s.discard(f)
		return nil, Digest{}, 0, &UnsafeZipError{Problem: ZippedTooLarge, Size: size, Limit: limit}
	}
	var d Digest
	h.Sum(d[:0])
	return f, d, size, nil
}

// discard closes and deletes a spool file. Failing to delete it leaves a
// file in tmp that the next Open clears, so the error is not reported.
func (s *Store) discard(f *os.File) {
	name := path.Join(tmpDir, filepath.Base(f.Name()))
	_ = f.Close()
	_ = s.root.Remove(name) //nolint:errcheck // a leftover spool is cleared by the next Open
}

// entryPlan is one validated entry: where it goes, its normalized mode
// (fs.ModeDir set for a directory) and its declared uncompressed size.
type entryPlan struct {
	file *zip.File
	path string
	mode fs.FileMode
	size int64
}

func (s *Store) extract(ctx context.Context, spool *os.File, size int64, opts Options) (string, error) {
	zr, err := zip.NewReader(spool, size)
	// With GODEBUG zipinsecurepath=0 (set in main) the reader reports
	// insecure names itself; plan reports them as UnsafeZipError.
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return "", &InvalidZipError{Err: err}
	}
	plans, err := plan(zr.File, opts)
	if err != nil {
		return "", err
	}
	staged := path.Join(tmpDir, "tree-"+randomName())
	if err := s.root.Mkdir(staged, dirMode); err != nil {
		return "", fmt.Errorf("code store: create staging directory: %w", err)
	}
	for _, p := range plans {
		if err := ctx.Err(); err != nil {
			return "", errors.Join(err, s.removeStaged(staged))
		}
		if err := s.write(staged, p); err != nil {
			return "", errors.Join(err, s.removeStaged(staged))
		}
	}
	if err := s.normalizeDirs(staged); err != nil {
		return "", errors.Join(err, s.removeStaged(staged))
	}
	return staged, nil
}

// plan validates every entry before anything is written.
func plan(files []*zip.File, opts Options) ([]entryPlan, error) {
	plans := make([]entryPlan, 0, len(files))
	var total int64
	for _, f := range files {
		name := strings.TrimSuffix(f.Name, "/")
		if !filepath.IsLocal(name) {
			return nil, &UnsafeZipError{Entry: f.Name, Problem: NonLocalName}
		}
		mode := f.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			return nil, &UnsafeZipError{Entry: f.Name, Problem: Symlink}
		case mode&(fs.ModeType&^fs.ModeDir) != 0:
			return nil, &UnsafeZipError{Entry: f.Name, Problem: SpecialFile}
		}
		clean := filepath.ToSlash(filepath.Clean(name))
		if mode.IsDir() || strings.HasSuffix(f.Name, "/") {
			plans = append(plans, entryPlan{file: f, path: clean, mode: fs.ModeDir | dirMode})
			continue
		}
		entrySize := int64(min(f.UncompressedSize64, 1<<62))
		if entrySize > opts.MaxEntry {
			return nil, &UnsafeZipError{Entry: f.Name, Problem: EntryTooLarge, Size: entrySize, Limit: opts.MaxEntry}
		}
		total += entrySize
		if total > opts.MaxUnzipped {
			return nil, &UnsafeZipError{Entry: f.Name, Problem: UnzippedTooLarge, Size: total, Limit: opts.MaxUnzipped}
		}
		m := fileMode
		if mode&0o111 != 0 || clean == "bootstrap" {
			m = execMode
		}
		plans = append(plans, entryPlan{file: f, path: clean, mode: m, size: entrySize})
	}
	return plans, nil
}

func (s *Store) write(staged string, p entryPlan) error {
	target := path.Join(staged, p.path)
	if p.mode.IsDir() {
		return s.mkdirAll(target, p.file.Name)
	}
	if err := s.mkdirAll(path.Dir(target), p.file.Name); err != nil {
		return err
	}
	src, err := p.file.Open()
	if err != nil {
		return &InvalidZipError{Err: fmt.Errorf("open entry %q: %w", p.file.Name, err)}
	}
	defer src.Close()
	dst, err := s.root.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return conflictOr(err, p.file.Name)
	}
	// The copy stops one byte past the declared size, which plan capped:
	// archive/zip refuses longer data itself, and the bound holds anyway.
	n, err := io.CopyN(dst, src, p.size+1)
	switch {
	case errors.Is(err, io.EOF):
	case err != nil:
		_ = dst.Close()
		if writeErr, ok := errors.AsType[*fs.PathError](err); ok {
			return fmt.Errorf("code store: write entry %q: %w", p.file.Name, writeErr)
		}
		return &InvalidZipError{Err: fmt.Errorf("read entry %q: %w", p.file.Name, err)}
	}
	if n > p.size {
		_ = dst.Close()
		return &InvalidZipError{Err: fmt.Errorf("entry %q holds more than its declared %d bytes", p.file.Name, p.size)}
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("code store: write entry %q: %w", p.file.Name, err)
	}
	if err := s.root.Chmod(target, p.mode); err != nil {
		return fmt.Errorf("code store: set mode of entry %q: %w", p.file.Name, err)
	}
	return nil
}

func (s *Store) mkdirAll(dir, entry string) error {
	if err := s.root.MkdirAll(dir, dirMode); err != nil {
		return conflictOr(err, entry)
	}
	return nil
}

// conflictOr reports an entry that needs a directory where an earlier entry
// wrote a file (or the reverse) as an invalid archive.
func conflictOr(err error, entry string) error {
	if errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.EISDIR) || errors.Is(err, fs.ErrExist) {
		return &InvalidZipError{Err: fmt.Errorf("entry %q conflicts with an earlier entry: %w", entry, err)}
	}
	return fmt.Errorf("code store: extract entry %q: %w", entry, err)
}

// normalizeDirs gives every directory, including the parents MkdirAll
// created under the process umask, mode 0755.
func (s *Store) normalizeDirs(staged string) error {
	return fs.WalkDir(s.root.FS(), staged, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("code store: walk extracted tree: %w", err)
		}
		if !d.IsDir() {
			return nil
		}
		if err := s.root.Chmod(p, dirMode); err != nil {
			return fmt.Errorf("code store: set directory mode: %w", err)
		}
		return nil
	})
}

// randomName names a temporary file or directory; crypto/rand.Text is 26
// base32 characters, safe in a path component.
func randomName() string { return rand.Text() }
