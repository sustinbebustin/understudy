package containermarker_test

import (
	"errors"
	"io/fs"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/containermarker"
)

func readerOf(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		text, ok := files[path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(text), nil
	}
}

func TestReadReportsEachMarkerFile(t *testing.T) {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		t.Run(marker, func(t *testing.T) {
			r := containermarker.Read(readerOf(map[string]string{marker: ""}))
			if !slices.Equal(r.Files, []string{marker}) {
				t.Fatalf("Files = %v, want [%s]", r.Files, marker)
			}
			if len(r.Runtimes) != 0 {
				t.Fatalf("Runtimes = %v, want none", r.Runtimes)
			}
			if !r.Any() {
				t.Fatal("Any() = false, want true for a marker file")
			}
		})
	}
}

func TestReadReportsEachRuntimeWord(t *testing.T) {
	for _, word := range []string{"docker", "kubepods", "containerd", "libpod"} {
		t.Run(word, func(t *testing.T) {
			r := containermarker.Read(readerOf(map[string]string{"/proc/1/cgroup": "0::/system.slice/" + word + "-abc.scope"}))
			if !slices.Equal(r.Runtimes, []string{word}) {
				t.Fatalf("Runtimes = %v, want [%s]", r.Runtimes, word)
			}
			if len(r.Files) != 0 {
				t.Fatalf("Files = %v, want none", r.Files)
			}
			if !r.Any() || !r.Names(word) {
				t.Fatalf("Any() = %v, Names(%q) = %v, want both true", r.Any(), word, r.Names(word))
			}
		})
	}
}

func TestReadReportsNothingOnAPlainHost(t *testing.T) {
	r := containermarker.Read(readerOf(map[string]string{"/proc/1/cgroup": "0::/init.scope"}))
	if r.Any() || r.Names("docker") {
		t.Fatalf("report = %+v, want nothing found", r)
	}
}

func TestReadTreatsAnUnreadableCgroupAsNoRuntime(t *testing.T) {
	read := func(path string) ([]byte, error) {
		if path == "/proc/1/cgroup" {
			return nil, errors.New("permission denied")
		}
		if path == "/.dockerenv" {
			return nil, nil
		}
		return nil, fs.ErrNotExist
	}
	r := containermarker.Read(read)
	if len(r.Runtimes) != 0 {
		t.Fatalf("Runtimes = %v, want none for an unreadable cgroup", r.Runtimes)
	}
	if !slices.Equal(r.Files, []string{"/.dockerenv"}) {
		t.Fatalf("Files = %v, want the marker file still reported", r.Files)
	}
}
