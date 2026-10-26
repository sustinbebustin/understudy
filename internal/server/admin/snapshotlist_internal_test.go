package admin

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"path"
	"testing"
	"testing/fstest"
	"time"
)

// The name a save gives its temporary file is the one the list leaves out,
// so the two cannot drift apart.
func TestSaveTempNameIsASaveTemp(t *testing.T) {
	for _, target := range []string{"a.tar", "snaps/one.tar"} {
		if name := saveTempName(target, rand.Text()); !isSaveTemp(name) {
			t.Errorf("isSaveTemp(%q) = false for a name saveTempName made, want true", name)
		}
		if isSaveTemp(target) {
			t.Errorf("isSaveTemp(%q) = true for the archive itself, want false", target)
		}
	}
}

// failingInfoFS is fsys in which the entry for name is listed by its
// directory, but reading its information fails with err, as it does for
// an archive deleted while the list walks the directory.
type failingInfoFS struct {
	fstest.MapFS
	name string
	err  error
}

func (f failingInfoFS) ReadDir(dir string) ([]fs.DirEntry, error) {
	entries, err := f.MapFS.ReadDir(dir)
	for i, e := range entries {
		if path.Join(dir, e.Name()) == f.name {
			entries[i] = failingInfoEntry{DirEntry: e, err: &fs.PathError{Op: "lstat", Path: f.name, Err: f.err}}
		}
	}
	return entries, err
}

type failingInfoEntry struct {
	fs.DirEntry
	err error
}

func (e failingInfoEntry) Info() (fs.FileInfo, error) { return nil, e.err }

// A file removed after its directory was read is left out of the list; any
// other failure to read a file's information fails the list.
func TestListArchivesLeavesOutAFileRemovedWhileListing(t *testing.T) {
	mod := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	files := fstest.MapFS{
		"snaps/gone.tar": {Data: []byte("x"), ModTime: mod},
		"snaps/kept.tar": {Data: []byte("xy"), ModTime: mod},
	}
	got, err := listArchives(failingInfoFS{MapFS: files, name: "snaps/gone.tar", err: fs.ErrNotExist})
	if err != nil || len(got) != 1 || got[0].Path != "snaps/kept.tar" || got[0].Bytes != 2 {
		t.Errorf("listArchives with snaps/gone.tar removed = %+v, %v; want only snaps/kept.tar", got, err)
	}
	_, err = listArchives(failingInfoFS{MapFS: files, name: "snaps/gone.tar", err: fs.ErrPermission})
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("listArchives with snaps/gone.tar unreadable = %v, want the permission error", err)
	}
}
