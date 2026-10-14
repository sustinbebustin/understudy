package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/kernel"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// Files under the data directory.
const (
	lockFile     = ".lock"
	stateFile    = "state.snapshot"
	blobsDir     = "blobs"
	ddbDir       = "dynamodb"
	snapshotsDir = "snapshots"
	spoolDir     = "tmp"
)

// dataDir is the locked persistence directory: the archive, the blobs and
// DynamoDB Local's files live under it, and the lock keeps a second serve
// out for as long as this one runs (ARCHITECTURE.md 13).
type dataDir struct {
	path string
	root *os.Root
	lock *os.File
	// snapshots roots the admin snapshot path form in its own
	// subdirectory, so a save there can never replace the lock, the state
	// file, a blob or DynamoDB Local's files.
	snapshots *os.Root
}

// DataDirLockedError is a data directory another serve process holds.
type DataDirLockedError struct{ Path string }

func (e *DataDirLockedError) Error() string {
	return "server: the data directory " + e.Path + " is in use by another understudy serve; stop that one, or give this one its own --persist directory"
}

// openDataDir creates the directory when missing, opens it and takes its
// lock without waiting.
func openDataDir(path string) (*dataDir, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("server: resolving the data directory %s: %w", path, err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("server: creating the data directory %s: %w", abs, err)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("server: opening the data directory %s: %w", abs, err)
	}
	lock, err := lockDir(root, abs)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	snapshots, err := openSub(root, snapshotsDir)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("server: opening %s under the data directory %s: %w", snapshotsDir, abs, err), lock.Close(), root.Close())
	}
	return &dataDir{path: abs, root: root, lock: lock, snapshots: snapshots}, nil
}

// lockDir takes the lock of the data directory root, at abs, without
// waiting; a *DataDirLockedError when a serve holds it. Closing the
// returned file releases the lock.
func lockDir(root *os.Root, abs string) (*os.File, error) {
	lock, err := root.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("server: opening the lock file of the data directory %s: %w", abs, err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		cerr := lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(&DataDirLockedError{Path: abs}, cerr)
		}
		return nil, errors.Join(fmt.Errorf("server: locking the data directory %s: %w", abs, err), cerr)
	}
	return lock, nil
}

// SweepDataDir removes what a crashed serve persisting under dir left on
// the Docker daemon c reaches: the managed containers labeled with the
// directory's owner, and its networks left empty. It holds the directory's
// lock while it sweeps, so it refuses with a *DataDirLockedError while a
// serve runs there, and no serve of the directory starts meanwhile. A
// missing directory has no lock to take, since no serve runs there, and
// is not created. owner is the understudy.dev/owner label swept.
func SweepDataDir(ctx context.Context, c *docker.Client, dir string) (owner string, swept docker.Swept, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", swept, fmt.Errorf("server: resolving the data directory %s: %w", dir, err)
	}
	owner = dirOwner(abs)
	root, err := os.OpenRoot(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return owner, swept, fmt.Errorf("server: opening the data directory %s: %w", abs, err)
	default:
		lock, err := lockDir(root, abs)
		if err != nil {
			return owner, swept, errors.Join(err, root.Close())
		}
		defer func() { err = errors.Join(err, lock.Close(), root.Close()) }()
	}
	swept, err = c.Sweep(ctx, docker.Ownership{Owner: owner, Instance: sweepInstance})
	return owner, swept, err
}

// sweepInstance is the instance label SweepDataDir's sweep spares. No
// serve draws it (they draw 12 random characters), so the sweep spares
// nothing of the owner it removes.
const sweepInstance = "cleanup"

// openSub creates the directory name under root when missing and opens it
// as a root of its own.
func openSub(root *os.Root, name string) (*os.Root, error) {
	if err := root.MkdirAll(name, 0o750); err != nil {
		return nil, err
	}
	return root.OpenRoot(name)
}

// blobStore opens the blob store under blobs/, indexing what an earlier
// run left there.
func (d *dataDir) blobStore() (*blob.Store, *os.Root, error) {
	if err := d.root.MkdirAll(blobsDir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("server: creating %s under the data directory %s: %w", blobsDir, d.path, err)
	}
	root, err := d.root.OpenRoot(blobsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("server: opening %s under the data directory %s: %w", blobsDir, d.path, err)
	}
	store, err := blob.NewDisk(root)
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("server: opening the blob store under %s: %w", filepath.Join(d.path, blobsDir), err), root.Close())
	}
	return store, root, nil
}

// dynamoDBDir creates dynamodb/ when missing and returns its absolute
// path, which the daemon bind-mounts as DynamoDB Local's -dbPath and
// refuses when it does not exist. The image runs as uid 1000, whatever
// uid serve runs as, so the directory is writable by everyone; the data
// directory above it keeps its 0o750, which keeps other host users out.
func (d *dataDir) dynamoDBDir() (string, error) {
	if err := d.root.MkdirAll(ddbDir, 0o750); err != nil {
		return "", fmt.Errorf("server: creating %s under the data directory %s: %w", ddbDir, d.path, err)
	}
	if err := d.root.Chmod(ddbDir, 0o777); err != nil {
		return "", fmt.Errorf("server: making %s under the data directory %s writable by DynamoDB Local's uid 1000: %w", ddbDir, d.path, err)
	}
	return filepath.Join(d.path, ddbDir), nil
}

// spool empties tmp/ under the data directory and returns its absolute
// path, where snapshot downloads are spooled before they are sent. The
// lock is held, so anything already there is a spooled archive a crashed
// run left behind.
func (d *dataDir) spool() (string, error) {
	if err := d.root.RemoveAll(spoolDir); err != nil {
		return "", fmt.Errorf("server: removing what an earlier run left in %s under the data directory %s: %w", spoolDir, d.path, err)
	}
	if err := d.root.Mkdir(spoolDir, 0o700); err != nil {
		return "", fmt.Errorf("server: creating %s under the data directory %s: %w", spoolDir, d.path, err)
	}
	return filepath.Join(d.path, spoolDir), nil
}

// owner is the container ownership label of this data directory: stable
// across restarts, so a restart sweeps what a crashed run left.
func (d *dataDir) owner() string { return dirOwner(d.path) }

// dirOwner is the ownership label of the data directory at the absolute
// path abs: "dir-" and the first 8 bytes of its SHA-256, in hex.
func dirOwner(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	return fmt.Sprintf("dir-%x", sum[:8])
}

// close unlocks and closes the directory.
func (d *dataDir) close() error {
	if err := errors.Join(d.snapshots.Close(), d.lock.Close(), d.root.Close()); err != nil {
		return fmt.Errorf("server: releasing the data directory %s: %w", d.path, err)
	}
	return nil
}

// persister loads the state at start and checkpoints it while it changes
// (ARCHITECTURE.md 13): the archive at state.snapshot with its blobs left
// in the blob store and without DynamoDB's items, which DynamoDB Local
// keeps under dynamodb/.
type persister struct {
	dir   *dataDir
	coord *snapshot.Coordinator
	blobs *blob.Store
	// clock is the emulator clock, whose wall time a restart resumes a
	// Running clock at.
	clock *clock.Clock
	log   *slog.Logger
	// every is the checkpoint interval.
	every time.Duration
	// maxBytes caps the archive the load reads and a checkpoint writes
	// (snapshotMaxBytes), so a checkpoint never writes one the next start
	// refuses.
	maxBytes int64
	// last is the digest of the state last loaded or saved; a checkpoint
	// of the same state writes nothing.
	last [sha256.Size]byte
	// unpin releases the blobs the archive on disk names. The archive
	// keeps them in the blob store rather than embedding them, so they
	// stay pinned until a newer archive replaces it: otherwise deleting an
	// object after a checkpoint would delete its blob, and a crash before
	// the next checkpoint would leave an archive that cannot load. nil
	// while no archive is on disk. The pin is never released at close,
	// since the last checkpoint may have failed and left the older
	// archive in place; the blob store's counts live only in memory.
	unpin func() error
}

// pin pins the blobs a's sections name and returns the function that
// releases them.
func (p *persister) pin(a snapshot.Archive) (func() error, error) {
	var refs []blob.Ref
	for _, s := range a.Sections {
		for _, name := range s.Blobs {
			ref, err := blob.ParseRef(name)
			if err != nil {
				return nil, fmt.Errorf("server: section %s names the blob %q: %w", s.Path, name, err)
			}
			refs = append(refs, ref)
		}
	}
	return p.blobs.Pin(refs)
}

// replacePin keeps unpin as the pin of the archive now on disk and
// releases the previous one.
func (p *persister) replacePin(unpin func() error) error {
	previous := p.unpin
	p.unpin = unpin
	if previous == nil {
		return nil
	}
	if err := previous(); err != nil {
		return fmt.Errorf("server: releasing the blobs only the replaced state file named: %w", err)
	}
	return nil
}

// load replaces the empty state with the archive an earlier run saved,
// when there is one, and drops the blobs no state names. restored reports
// whether there was one, whose clock the emulator now runs on.
func (p *persister) load(ctx context.Context) (restored bool, err error) {
	f, err := p.dir.root.Open(stateFile)
	if errors.Is(err, fs.ErrNotExist) {
		if err := p.blobs.Prune(); err != nil {
			return false, fmt.Errorf("server: removing blobs no state names under %s: %w", p.dir.path, err)
		}
		return false, p.remember(ctx)
	}
	if err != nil {
		return false, fmt.Errorf("server: opening the saved state %s: %w", filepath.Join(p.dir.path, stateFile), err)
	}
	defer f.Close()
	a, err := snapshot.Read(f, snapshot.ReadOptions{MaxBytes: p.maxBytes, Schemas: p.coord.Schemas()})
	if err != nil {
		next := "move it aside to start empty"
		if errors.As(err, new(*snapshot.SizeError)) {
			next = "start with a larger " + serveradmin.SnapshotMaxBytesSetting + " to load it, or move it aside to start empty"
		}
		return false, fmt.Errorf("server: reading the saved state %s; %s: %w", filepath.Join(p.dir.path, stateFile), next, err)
	}
	a.Manifest.Clock = resumed(a.Manifest.Clock, clock.FromWire(p.clock.Wall()))
	if err := p.coord.Load(ctx, a); err != nil {
		released, ok := errors.AsType[*snapshot.BlobReleaseError](err)
		if !ok {
			return false, fmt.Errorf("server: loading the saved state %s; move it aside to start empty: %w", filepath.Join(p.dir.path, stateFile), err)
		}
		p.log.LogAttrs(ctx, slog.LevelWarn, "saved state loaded; some blobs stay stored until the next start", slog.String("error", released.Error()))
	}
	unpin, err := p.pin(*a)
	if err != nil {
		return true, fmt.Errorf("server: keeping the blobs the saved state %s names: %w", filepath.Join(p.dir.path, stateFile), err)
	}
	if err := p.replacePin(unpin); err != nil {
		return true, err
	}
	p.log.LogAttrs(ctx, slog.LevelInfo, "loaded the saved state", slog.String("path", filepath.Join(p.dir.path, stateFile)))
	return true, p.remember(ctx)
}

// resumed is the clock a restart restores from the persisted saved one.
// A Running clock follows wall time, which kept passing while no serve
// ran, so it resumes at wall time, or at its saved instant when that is
// later (a clock advanced ahead of wall time), so emulator time never
// moves back past what the saved state recorded. The archive records no
// offset from wall time, so an advance shorter than the downtime is
// absorbed. A Frozen clock holds its instant, and a Scaled one resumes
// from it, since nothing defines its time across the downtime. An
// explicit snapshot load keeps the archive's instant in every mode.
func resumed(saved snapshot.Clock, wall clock.Instant) snapshot.Clock {
	if _, ok := saved.Mode.(clock.Running); ok && saved.At.Before(wall) {
		saved.At = wall
	}
	return saved
}

// remember records the digest of the current state as saved.
func (p *persister) remember(ctx context.Context) error {
	cut, err := p.coord.Cut(ctx)
	if err != nil {
		return fmt.Errorf("server: reading the loaded state: %w", err)
	}
	p.last = digest(cut.Archive())
	return cut.Release()
}

// checkpoint writes the state to state.snapshot atomically when it
// differs from the last saved, or always when force is set, and moves the
// blob pin from the replaced archive to the new one.
func (p *persister) checkpoint(ctx context.Context, force bool) (err error) {
	cut, err := p.coord.Cut(ctx)
	if err != nil {
		return fmt.Errorf("server: checkpoint: %w", err)
	}
	sum := digest(cut.Archive())
	if !force && sum == p.last {
		return cut.Release()
	}
	if n := cut.PendingEffects(); n > 0 {
		p.log.LogAttrs(ctx, slog.LevelWarn, "the checkpoint holds writes whose effects, such as deliveries, had not run; a restart from it does not run them",
			slog.Int("pending_effects", n))
	}
	// The cut holds its blobs until WriteAfterCut releases it, so this pin
	// cannot miss one a delete removed in between.
	unpin, err := p.pin(cut.Archive())
	if err != nil {
		return errors.Join(fmt.Errorf("server: checkpoint: %w", err), cut.Release())
	}
	if err := p.write(ctx, cut); err != nil {
		return errors.Join(err, unpin())
	}
	// Until the rename is durable a crash can bring the replaced archive
	// back, so its blobs stay pinned until the directory is synced.
	if err := syncDir(p.dir.root); err != nil {
		p.keepPin(unpin)
		return fmt.Errorf("server: checkpoint: syncing the data directory %s after replacing %s; both the new and the replaced state keep their blobs, and the next checkpoint tries again: %w", p.dir.path, stateFile, err)
	}
	p.last = sum
	return p.replacePin(unpin)
}

// keepPin adds unpin to the held pin without releasing it: both archives
// may be the one on disk after a crash. The next replacePin releases both.
func (p *persister) keepPin(unpin func() error) {
	previous := p.unpin
	if previous == nil {
		p.unpin = unpin
		return
	}
	p.unpin = func() error { return errors.Join(previous(), unpin()) }
}

// syncDir makes the renames in the directory root durable.
func syncDir(root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// write writes cut's archive to state.snapshot atomically, after syncing
// the blob store so the archive never names a blob a crash could lose.
func (p *persister) write(ctx context.Context, cut *snapshot.Cut) error {
	if err := p.blobs.Sync(); err != nil {
		return errors.Join(fmt.Errorf("server: checkpoint: syncing the blob store: %w", err), cut.Release())
	}
	tmp := stateFile + "." + rand.Text() + ".tmp"
	f, err := p.dir.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Join(fmt.Errorf("server: checkpoint: creating %s: %w", tmp, err), cut.Release())
	}
	werr := p.coord.WriteAfterCut(ctx, f, cut, snapshot.WithoutLateData, nil, snapshot.WriteOptions{Blobs: snapshot.ExternalBlobs, MaxBytes: p.maxBytes})
	if werr == nil {
		werr = f.Sync()
	}
	if err := errors.Join(werr, f.Close()); err != nil {
		rerr := p.dir.root.Remove(tmp)
		kept := "the previous state file is kept"
		if rerr != nil {
			kept += " and the incomplete " + tmp + " was left in the persist directory"
		}
		if errors.As(err, new(*snapshot.WriteLimitError)) {
			if rerr == nil {
				kept += " and the new one discarded"
			}
			kept += "; reset or delete state the emulator does not need, or restart with a larger " + serveradmin.SnapshotMaxBytesSetting
		}
		return fmt.Errorf("server: checkpoint: writing %s; %s: %w", tmp, kept, errors.Join(err, rerr))
	}
	if err := p.dir.root.Rename(tmp, stateFile); err != nil {
		return fmt.Errorf("server: checkpoint: moving %s into place; the previous state file is kept: %w", tmp, errors.Join(err, p.dir.root.Remove(tmp)))
	}
	return nil
}

// SnapshotLoadError is the archive Config.LoadSnapshot names, which could
// not be read or loaded at start. Nothing was served.
type SnapshotLoadError struct {
	Path string
	Err  error
}

func (e *SnapshotLoadError) Error() string {
	next := "check the path, or start without it and load one later with 'understudy snapshot load FILE'"
	if errors.As(e.Err, new(*snapshot.SizeError)) {
		next = "start with a larger " + serveradmin.SnapshotMaxBytesSetting + " to load it, or check that it is the snapshot you meant"
	}
	return "server: loading the snapshot " + e.Path + " at start: " + e.Err.Error() + "; " + next
}

func (e *SnapshotLoadError) Unwrap() error { return e.Err }

// loadSnapshot replaces the state with the archive at path, when set, as
// PUT /snapshots does: the timers are cleared and Load rearms them from the
// loaded state. maxBytes caps the archive.
func loadSnapshot(path string, maxBytes int64, k *kernel.Kernel, coord *snapshot.Coordinator, log *slog.Logger) error {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return &SnapshotLoadError{Path: path, Err: err}
	}
	defer f.Close()
	a, err := snapshot.Read(f, snapshot.ReadOptions{MaxBytes: maxBytes, Schemas: coord.Schemas()})
	if err != nil {
		return &SnapshotLoadError{Path: path, Err: err}
	}
	ctx := context.Background()
	k.Timers().Scheduler().Clear()
	if err := coord.Load(ctx, a); err != nil {
		released, ok := errors.AsType[*snapshot.BlobReleaseError](err)
		if !ok {
			return &SnapshotLoadError{Path: path, Err: err}
		}
		log.LogAttrs(ctx, slog.LevelWarn, "snapshot loaded; some blobs of the replaced state stay stored until the next start", slog.String("error", released.Error()))
	}
	log.LogAttrs(ctx, slog.LevelInfo, "loaded the snapshot", slog.String("path", path))
	return nil
}

// digest identifies the state an archive holds: its sections and fault
// rules, not the clock, journal counter or id positions, which move
// without any state changing.
func digest(a snapshot.Archive) [sha256.Size]byte {
	h := sha256.New()
	field := func(b []byte) {
		// A hash.Hash's Write never fails.
		_, _ = h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(b))))
		_, _ = h.Write(b)
	}
	for _, s := range a.Sections {
		field([]byte(s.Path))
		field([]byte(strconv.Itoa(s.Version)))
		field(s.Data)
		for _, b := range s.Blobs {
			field([]byte(b))
		}
	}
	field(a.Faults)
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return sum
}

// run checkpoints every p.every of wall time until ctx ends. The wait is a
// context deadline because only internal/clock reads the wall clock.
func (p *persister) run(ctx context.Context) {
	for {
		wait, cancel := context.WithTimeout(ctx, p.every)
		<-wait.Done()
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err := p.checkpoint(ctx, false); err != nil && ctx.Err() == nil {
			p.log.LogAttrs(ctx, slog.LevelWarn, "saving the state failed; the next checkpoint tries again", slog.String("error", err.Error()))
		}
	}
}
