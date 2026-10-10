//go:build docker

package topology_test

import (
	"os"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/topology"
)

func TestDetectOnThisHost(t *testing.T) {
	socket, err := docker.ParseDockerHost(os.Getenv("DOCKER_HOST"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := docker.New(docker.Options{Socket: socket})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	got, err := new(topology.Detector).Detect(t.Context(), c, topology.Options{})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	t.Logf("detected %v", got)
	var addr bool
	switch tp := got.(type) {
	case topology.LinuxBridge:
		addr = tp.Gateway.IsValid() && !tp.Gateway.IsUnspecified()
	case topology.InContainer:
		addr = tp.Self.IsValid() && !tp.Self.IsUnspecified()
	case topology.DockerDesktop, topology.RootlessDocker, topology.Podman:
		t.Fatalf("Detect = %v, want Linux bridge or in-container on the CI host", got)
	}
	if !addr {
		t.Errorf("Detect = %v, want a non-zero address", got)
	}
	if a, err := got.Addresses(); err != nil || !a.FunctionEndpointHost.Is4() {
		t.Errorf("Addresses = %+v, %v", a, err)
	}
}
