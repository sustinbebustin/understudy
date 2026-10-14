package hotreload

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"time"
)

// DefaultInterval and DefaultQuiet are the Options a zero value takes
// (ARCHITECTURE.md 14.4).
const (
	DefaultInterval = 300 * time.Millisecond
	DefaultQuiet    = 300 * time.Millisecond
)

// Options configures a Watcher. Zero values take the defaults.
type Options struct {
	// Interval is the time between two polls of the tree.
	Interval time.Duration
	// Quiet is how long the tree must stay unchanged after a change before
	// the change is reported; at least Interval.
	Quiet time.Duration
}

// DirError reports a directory a Watcher cannot watch.
type DirError struct {
	Dir    string
	Reason string
}

func (e *DirError) Error() string {
	return fmt.Sprintf("hot reload cannot watch %q: %s", e.Dir, e.Reason)
}

// Watcher polls one directory tree for changes. Run is its only loop; a
// Watcher is not safe for concurrent use.
type Watcher struct {
	dir string
	// interval is the poll period; quietPolls the unchanged polls that end
	// a change.
	interval   time.Duration
	quietPolls int
	last       snapshot
}

// New validates the options and dir, an absolute path to a directory, and
// takes the tree's first snapshot: Run reports changes made after New.
func New(dir string, opts Options) (*Watcher, error) {
	interval := opts.Interval
	if interval == 0 {
		interval = DefaultInterval
	}
	quiet := opts.Quiet
	if quiet == 0 {
		quiet = DefaultQuiet
	}
	if interval < 0 || quiet < interval {
		return nil, fmt.Errorf("hotreload: interval %s and quiet period %s: the interval must be positive and the quiet period at least one interval", interval, quiet)
	}
	if !filepath.IsAbs(dir) {
		return nil, &DirError{Dir: dir, Reason: "the path is not absolute; pass the absolute path of the build output as understudy sees it (inside understudy's container, the path the host directory is mounted at)"}
	}
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		return nil, &DirError{Dir: dir, Reason: err.Error()}
	case !info.IsDir():
		return nil, &DirError{Dir: dir, Reason: "it is not a directory; pass the directory holding the function's code"}
	}
	return &Watcher{
		dir:        dir,
		interval:   interval,
		quietPolls: int((quiet + interval - 1) / interval),
		last:       take(dir),
	}, nil
}

// Run polls the tree until ctx ends, calling changed once for every change
// followed by the quiet period. changed runs on Run's goroutine.
func (w *Watcher) Run(ctx context.Context, changed func()) {
	pending, stable := false, 0
	for sleep(ctx, w.interval) {
		now := take(w.dir)
		if !maps.Equal(now, w.last) {
			w.last, pending, stable = now, true, 0
			continue
		}
		if !pending {
			continue
		}
		stable++
		if stable >= w.quietPolls {
			pending = false
			changed()
		}
	}
}

// sleep waits d of wall time, through a context deadline because only
// internal/clock reads the wall clock; false once ctx ends.
func sleep(ctx context.Context, d time.Duration) bool {
	tick, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	<-tick.Done()
	return ctx.Err() == nil
}

// entry is what a poll compares of one path.
type entry struct {
	size    int64
	modTime int64 // Unix nanoseconds
	mode    fs.FileMode
	// unreadable marks a path that could not be read: an error is a state
	// of the tree like any other.
	unreadable bool
}

// snapshot is the tree by slash-separated path relative to its root. The
// root itself is "."; a walk that fails marks it unreadable.
type snapshot map[string]entry

// take walks dir without following symbolic links.
func take(dir string) snapshot {
	s := snapshot{}
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, readErr error) error {
		s[relative(dir, path)] = entryOf(d, readErr)
		return descend(d, readErr)
	})
	if walkErr != nil {
		s["."] = entry{unreadable: true}
	}
	return s
}

// relative is path's slash-separated name under dir.
func relative(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// entryOf is what a poll records of a walked path: unreadable when the walk
// or its stat failed.
func entryOf(d fs.DirEntry, readErr error) entry {
	if readErr != nil || d == nil {
		return entry{unreadable: true}
	}
	info, err := d.Info()
	if err != nil {
		return entry{unreadable: true}
	}
	return entry{size: info.Size(), modTime: info.ModTime().UnixNano(), mode: info.Mode()}
}

// descend tells WalkDir to skip a directory it could not read and to go
// on everywhere else: an unreadable path is recorded, not a failed walk.
func descend(d fs.DirEntry, readErr error) error {
	if readErr != nil && d != nil && d.IsDir() {
		return fs.SkipDir
	}
	return nil
}
