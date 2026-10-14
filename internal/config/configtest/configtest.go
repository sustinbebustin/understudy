// Package configtest connects Docker-tagged tests to the Docker Engine
// serve would use, resolved by config so the tests and serve agree on
// which UNDERSTUDY_DOCKER and DOCKER_HOST values are usable.
package configtest

import (
	"os"
	"testing"

	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/docker"
)

// EngineClient connects to the daemon serve would use: UNDERSTUDY_DOCKER,
// else DOCKER_HOST, else the default socket. The test fails when the
// configuration refuses the value or turns Docker off, and the client is
// closed when the test ends.
func EngineClient(t *testing.T) *docker.Client {
	t.Helper()
	host, cerr := config.LoadHost(nil, os.Environ(), os.ReadFile)
	if cerr != nil {
		t.Fatalf("resolve the Docker socket as serve does: %v", cerr)
	}
	var socket string
	switch d := host.Docker.(type) {
	case config.DockerSocket:
		socket = d.Path
	case config.DockerDisabled:
		t.Fatalf("docker-tagged tests need a Docker Engine, but the configuration turns Docker off (warnings: %v); point UNDERSTUDY_DOCKER or DOCKER_HOST at a unix:// socket with an absolute path", host.Warnings)
	}
	c, err := docker.New(docker.Options{Socket: socket})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
