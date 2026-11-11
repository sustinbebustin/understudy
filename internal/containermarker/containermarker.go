// Package containermarker reads the signs that a process runs inside a
// container: the marker files Docker and Podman leave and the container
// runtime a cgroup names (containers.md 8.3). It only reports what it found;
// callers decide what a finding means, for example that kubepods is a
// Kubernetes pod to config's placement and a container to topology.
package containermarker

import (
	"bytes"
	"slices"
)

// cgroupPath is PID 1's cgroup, which names the runtime that started it.
const cgroupPath = "/proc/1/cgroup"

// markerFiles are the files whose existence marks a container.
func markerFiles() []string { return []string{"/.dockerenv", "/run/.containerenv"} }

// runtimeWords are the container runtime names looked for in the cgroup.
func runtimeWords() []string { return []string{"docker", "kubepods", "containerd", "libpod"} }

// Report is what Read found.
type Report struct {
	// Files are the marker files that exist, as absolute paths.
	Files []string
	// Runtimes are the runtime words the cgroup names.
	Runtimes []string
}

// Read reports the markers visible through readFile, which takes absolute
// paths. A marker file counts when readFile succeeds on it; an unreadable
// cgroup names no runtime.
func Read(readFile func(path string) ([]byte, error)) Report {
	var r Report
	for _, marker := range markerFiles() {
		if _, err := readFile(marker); err == nil {
			r.Files = append(r.Files, marker)
		}
	}
	cgroup, err := readFile(cgroupPath)
	if err != nil {
		return r
	}
	for _, word := range runtimeWords() {
		if bytes.Contains(cgroup, []byte(word)) {
			r.Runtimes = append(r.Runtimes, word)
		}
	}
	return r
}

// Any reports whether any marker file exists or any runtime word was found.
func (r Report) Any() bool {
	return len(r.Files) > 0 || len(r.Runtimes) > 0
}

// Names reports whether the cgroup named the runtime word.
func (r Report) Names(word string) bool {
	return slices.Contains(r.Runtimes, word)
}
