package admin_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
)

// snapshotsDir is a temporary snapshots directory and the handler serving
// it as its data directory.
func snapshotsDir(k *kerneltest.K) (string, *serveradmin.Handler) {
	k.Helper()
	dir := k.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		k.Fatal(err)
	}
	k.Cleanup(func() {
		if err := root.Close(); err != nil {
			k.Error(err)
		}
	})
	return dir, handlerWith(k, func(o *serveradmin.Options) { o.DataDir = root })
}

// writeFile writes size bytes at name under dir, creating its directories,
// and sets its modification time to mod.
func writeFile(k *kerneltest.K, dir, name string, size int, mod time.Time) {
	k.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		k.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		k.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		k.Fatal(err)
	}
}

// Listing snapshots needs persistence: without a data directory the
// endpoint is unavailable and says how to get one (ADR 0017: the
// unavailable code; every error names the next command to try).
func TestSnapshotListWithoutPersistenceIsUnavailable(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		rec := serve(k, handler(k, ""), http.MethodGet, admin.Prefix+"/snapshots")
		e := envelope(k, rec)
		if rec.Code != http.StatusServiceUnavailable || e.Code != admin.Unavailable || !strings.Contains(e.Hint, "--persist") {
			k.Errorf("GET snapshots without persistence = %d %+v, want 503 unavailable with a hint naming --persist", rec.Code, e)
		}
	})
}

// The list holds every regular file under the snapshots directory, at any
// depth, by its slash-separated path relative to it, its size and its
// modification time in UTC, sorted by path byte order; directories,
// symlinks and a save's in-flight temporary file are left out.
func TestSnapshotListNamesEveryArchive(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		dir, h := snapshotsDir(k)
		mod := time.Date(2026, 10, 26, 12, 0, 0, 0, time.FixedZone("UTC+2", 2*3600))
		want := []admin.SnapshotFile{
			{Path: "B.tar", Bytes: 3},
			{Path: "b.tar", Bytes: 10},
			{Path: "deep/er/x.tar", Bytes: 2},
			{Path: "notes.tmp", Bytes: 1},
			{Path: "snaps.tar", Bytes: 7},
			{Path: "snaps/one.tar", Bytes: 5},
		}
		for i := range want {
			want[i].Modified = mod.Add(time.Duration(i) * time.Second).UTC()
			writeFile(k, dir, want[i].Path, int(want[i].Bytes), want[i].Modified)
		}
		// A save writes <target>.<rand.Text()>.tmp and renames it into place.
		writeFile(k, dir, "b.tar.ABCDEFGHIJKLMNOPQRSTUVWXYZ.tmp", 4, mod)
		writeFile(k, dir, "snaps/one.tar.ABCDEFGHIJKLMNOPQRSTUVWX27.tmp", 4, mod)
		if err := os.Symlink("b.tar", filepath.Join(dir, "link.tar")); err != nil {
			k.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "empty"), 0o750); err != nil {
			k.Fatal(err)
		}

		rec := serve(k, h, http.MethodGet, admin.Prefix+"/snapshots")
		var got admin.SnapshotList
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			k.Fatalf("GET snapshots = %d %s, %v", rec.Code, rec.Body, err)
		}
		if len(got.Snapshots) != len(want) {
			k.Fatalf("GET snapshots = %+v, want %+v", got.Snapshots, want)
		}
		for i, f := range got.Snapshots {
			if f.Path != want[i].Path || f.Bytes != want[i].Bytes || !f.Modified.Equal(want[i].Modified) {
				k.Errorf("snapshot %d = %+v, want %+v", i, f, want[i])
			}
		}
		var raw struct {
			Snapshots []struct {
				Modified string `json:"modified"`
			} `json:"snapshots"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			k.Fatal(err)
		}
		for _, f := range raw.Snapshots {
			if !strings.HasSuffix(f.Modified, "Z") {
				k.Errorf("modified %q is not in UTC", f.Modified)
			}
		}

		head := serve(k, h, http.MethodHead, admin.Prefix+"/snapshots")
		if head.Code != http.StatusOK || head.Body.Len() != 0 {
			k.Errorf("HEAD snapshots = %d with %d body bytes, want 200 and no body", head.Code, head.Body.Len())
		}
		if rec := serve(k, h, http.MethodGet, admin.Prefix+"/snapshots?path=b.tar"); rec.Code != http.StatusBadRequest {
			k.Errorf("GET snapshots with a query parameter = %d %s, want 400 as every route refuses one", rec.Code, rec.Body)
		}
	})
}

// An empty snapshots directory lists an empty array, never null, so a
// client reads an empty list without a nil check.
func TestSnapshotListOfAnEmptyDirectory(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		_, h := snapshotsDir(k)
		rec := serve(k, h, http.MethodGet, admin.Prefix+"/snapshots")
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || string(body["snapshots"]) != "[]" {
			k.Errorf("GET snapshots of an empty directory = %d %s, want 200 {\"snapshots\":[]}", rec.Code, rec.Body)
		}
	})
}

// A directory under the snapshots directory that cannot be read fails the
// list as internal rather than answering a list that silently misses its
// archives.
func TestSnapshotListOfAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	kerneltest.Run(t, func(k *kerneltest.K) {
		dir, h := snapshotsDir(k)
		writeFile(k, dir, "locked/a.tar", 1, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
		locked := filepath.Join(dir, "locked")
		if err := os.Chmod(locked, 0); err != nil {
			k.Fatal(err)
		}
		k.Cleanup(func() {
			// The temporary directory's cleanup must enter it to remove it.
			if err := os.Chmod(locked, 0o700); err != nil { //nolint:gosec // a directory needs its search bit
				k.Error(err)
			}
		})
		rec := serve(k, h, http.MethodGet, admin.Prefix+"/snapshots")
		if e := envelope(k, rec); rec.Code != http.StatusInternalServerError || e.Code != admin.Internal {
			k.Errorf("GET snapshots with an unreadable directory = %d %+v, want 500 internal", rec.Code, e)
		}
	})
}
