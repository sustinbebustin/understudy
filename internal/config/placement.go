package config

import (
	"bytes"
	"slices"
)

// Placement is where the process runs, which decides the default listener
// and whether ContainerPublished is allowed.
//
//sumtype:decl
type Placement interface{ isPlacement() }

// OnHost is a process outside any container.
type OnHost struct{}

// InContainer is a process inside a Docker or Podman container.
type InContainer struct{}

func (OnHost) isPlacement()      {}
func (InContainer) isPlacement() {}

// DetectPlacement reads the container markers internal/topology reads
// (containers.md 8.3): /.dockerenv, /run/.containerenv, or a container
// runtime named in /proc/1/cgroup.
func DetectPlacement(readFile func(path string) ([]byte, error)) Placement {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := readFile(marker); err == nil {
			return InContainer{}
		}
	}
	cgroup, err := readFile("/proc/1/cgroup")
	if err != nil {
		return OnHost{}
	}
	if slices.ContainsFunc([]string{"docker", "kubepods", "containerd", "libpod"}, func(word string) bool {
		return bytes.Contains(cgroup, []byte(word))
	}) {
		return InContainer{}
	}
	return OnHost{}
}
