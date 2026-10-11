package hotreload

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"
)

// The snapshot is the watcher's whole notion of change: a tree left alone
// snapshots equal, and every kind of edit a build makes (a file's content,
// a new file, a removed file, a new directory) snapshots different.
func TestSnapshotSeesEveryEditAndNothingElse(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir := t.TempDir()
		names := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{1,8}(/[a-z]{1,8})?`), 1, 6, rapid.ID).Draw(rt, "files")
		var files []string
		for _, name := range names {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				continue // a file already holds the directory's name
			}
			if err := os.WriteFile(path, []byte(rapid.StringN(0, 16, -1).Draw(rt, "body")), 0o600); err != nil {
				continue // a directory already holds the file's name
			}
			files = append(files, path)
		}
		if len(files) == 0 {
			rt.Skip("every generated name collided")
		}
		before := take(dir)
		if again := take(dir); !maps.Equal(before, again) {
			rt.Fatalf("an untouched tree snapshots differently:\n%v\n%v", before, again)
		}

		target := rapid.SampledFrom(files).Draw(rt, "target")
		switch edit := rapid.IntRange(0, 3).Draw(rt, "edit"); edit {
		case 0:
			f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				rt.Fatal(err)
			}
			_, werr := f.WriteString("x")
			if err := f.Close(); werr != nil || err != nil {
				rt.Fatal(werr, err)
			}
		case 1:
			if err := os.Remove(target); err != nil {
				rt.Fatal(err)
			}
		case 2:
			if err := os.WriteFile(target+"_new", nil, 0o600); err != nil {
				rt.Fatal(err)
			}
		case 3:
			if err := os.Mkdir(target+"_dir", 0o700); err != nil {
				rt.Fatal(err)
			}
		}
		if after := take(dir); maps.Equal(before, after) {
			rt.Fatalf("an edit to %s left the snapshot unchanged", target)
		}
	})
}

func TestSnapshotOfAMissingTreeIsAChange(t *testing.T) {
	dir := t.TempDir()
	before := take(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if after := take(dir); maps.Equal(before, after) {
		t.Fatal("removing the watched directory left the snapshot unchanged")
	}
}
