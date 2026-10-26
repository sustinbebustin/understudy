package admin

import (
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/admin"
)

// snapshotList answers GET /snapshots: every archive under the snapshots
// directory, which exists only with persistence.
func (h *Handler) snapshotList(w http.ResponseWriter, r *http.Request, _ []string) {
	if h.o.DataDir == nil {
		h.fail(w, r, &admin.Error{
			Code:    admin.Unavailable,
			Message: "listing snapshots needs persistence, and this server has none",
			Hint:    "start serve with --persist DIR",
		})
		return
	}
	files, err := listArchives(h.o.DataDir.FS())
	if err != nil {
		h.fail(w, r, internal("listing the snapshots directory; no archive was listed", err))
		return
	}
	h.ok(w, r, admin.SnapshotList{Snapshots: files})
}

// listArchives lists every regular file under fsys at any depth, except a
// save's in-flight temporary file, by its slash-separated path, size and
// modification time in UTC, sorted by path in byte order. Directories,
// symlinks and other entries are left out. A directory that cannot be read
// fails the list rather than leaving its archives out of it.
func listArchives(fsys fs.FS) ([]admin.SnapshotFile, error) {
	files := []admin.SnapshotFile{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || isSaveTemp(name) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		// The modification time is the file system's wall time, not
		// emulator time: no clock call.
		files = append(files, admin.SnapshotFile{Path: name, Bytes: info.Size(), Modified: info.ModTime().UTC()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// WalkDir's per-directory order puts snaps/one.tar before snaps.tar.
	slices.SortFunc(files, func(a, b admin.SnapshotFile) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// randTextLen is the length of crypto/rand.Text's result: 26 characters of
// A-Z and 2-7.
const randTextLen = 26

// isSaveTemp reports whether name is a save's in-flight temporary file,
// <target>.<rand.Text()>.tmp (createTemp), which a list must not offer as
// an archive.
func isSaveTemp(name string) bool {
	rest, ok := strings.CutSuffix(path.Base(name), ".tmp")
	if !ok {
		return false
	}
	dot := strings.LastIndexByte(rest, '.')
	if dot <= 0 {
		return false
	}
	random := rest[dot+1:]
	return len(random) == randTextLen && strings.Trim(random, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") == ""
}
