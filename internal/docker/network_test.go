package docker_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

// A network named like another's ID prefix is found by its name, as the
// daemon tries an exact name before an ID prefix.
func TestNetworkNameWinsOverAnIDPrefix(t *testing.T) {
	_, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	first, err := c.NetworkCreate(ctx, "aaa", docker.NetworkOptions{})
	if err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	prefix := first[:4]
	named, err := c.NetworkCreate(ctx, prefix, docker.NetworkOptions{})
	if err != nil {
		t.Fatalf("NetworkCreate %s: %v", prefix, err)
	}
	for range 20 {
		got, err := c.NetworkInspect(ctx, prefix)
		if err != nil {
			t.Fatalf("NetworkInspect %s: %v", prefix, err)
		}
		if got.ID != named {
			t.Fatalf("NetworkInspect %s = %s, want the network named %s (%s), not the ID-prefix match %s", prefix, got.ID, prefix, named, first)
		}
	}
}

func TestNetworkLifecycle(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	labels := labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: "i", Owner: "o"}})
	id, err := c.NetworkCreate(ctx, "understudy-net", docker.NetworkOptions{Labels: labels})
	if err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	if _, err := c.NetworkCreate(ctx, "understudy-net", docker.NetworkOptions{}); !docker.IsConflict(err) {
		t.Errorf("duplicate NetworkCreate = %v, want conflict", err)
	}

	ct := create(t, c, "emu", docker.Config{}, docker.HostConfig{NetworkMode: "understudy-net"})
	if err := c.ContainerStart(ctx, ct); err != nil {
		t.Fatal(err)
	}
	other := create(t, c, "fn", docker.Config{}, docker.HostConfig{})
	if err := c.NetworkConnect(ctx, "understudy-net", other, docker.EndpointSettings{Aliases: []string{"fn.internal"}}); err != nil {
		t.Fatalf("NetworkConnect: %v", err)
	}
	// Docker creates a container's endpoints when it starts, so one connected
	// while only created is not listed yet.
	n, err := c.NetworkInspect(ctx, "understudy-net")
	if err != nil {
		t.Fatalf("NetworkInspect: %v", err)
	}
	if _, listed := n.Containers[other]; listed || len(n.Containers) != 1 {
		t.Errorf("network containers = %+v, want only the running %s", n.Containers, ct)
	}
	if err := c.ContainerStart(ctx, other); err != nil {
		t.Fatal(err)
	}
	n, err = c.NetworkInspect(ctx, "understudy-net")
	if err != nil {
		t.Fatalf("NetworkInspect: %v", err)
	}
	if n.ID != id || len(n.IPAM.Config) != 1 || n.IPAM.Config[0].Gateway == "" || len(n.Containers) != 2 {
		t.Errorf("network = %+v, want one pool with a gateway and two containers", n)
	}
	inspected, err := c.ContainerInspect(ctx, ct)
	if err != nil {
		t.Fatal(err)
	}
	if ep := inspected.NetworkSettings.Networks["understudy-net"]; ep.IPAddress == "" || ep.Gateway != n.IPAM.Config[0].Gateway {
		t.Errorf("endpoint = %+v, want an address on the network's pool", ep)
	}

	listed, err := c.NetworkList(ctx, map[string]string{docker.LabelOwner: "o"})
	if err != nil || len(listed) != 1 || listed[0].Name != "understudy-net" {
		t.Errorf("NetworkList by owner = %+v, %v", listed, err)
	}
	if err := c.NetworkRemove(ctx, id); err == nil {
		t.Error("NetworkRemove of a network with running containers succeeded")
	}
	// Stopping releases the endpoints, so the network can go while the
	// stopped containers still name it in their config.
	for _, x := range []string{ct, other} {
		if !d.Exit(x, 0) {
			t.Fatalf("container %s was not running", x)
		}
	}
	if err := c.NetworkRemove(ctx, id); err != nil {
		t.Fatalf("NetworkRemove with only stopped containers: %v", err)
	}
	if _, err := c.NetworkInspect(ctx, id); !docker.IsNotFound(err) {
		t.Errorf("NetworkInspect after remove = %v, want not found", err)
	}
}

func TestNetworkInspectReportsTheBridgeGateway(t *testing.T) {
	_, c := daemonWithImage(t, dockertest.Options{})
	n, err := c.NetworkInspect(t.Context(), "bridge")
	if err != nil || n.IPAM.Config[0].Gateway != "172.17.0.1" {
		t.Errorf("bridge = %+v, %v; want gateway 172.17.0.1", n, err)
	}
}
