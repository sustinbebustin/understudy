//go:build docker

package docker_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/config/configtest"
	"github.com/sustinbebustin/understudy/internal/docker"
)

// engineClient connects to the daemon serve would use.
func engineClient(t *testing.T) *docker.Client {
	t.Helper()
	return configtest.EngineClient(t)
}

// runID is unique per test run, so names and owner labels never collide
// with other agents' containers on the same daemon.
func runID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// ensureImage pulls ref when it is absent.
func ensureImage(t *testing.T, c *docker.Client, ref string) {
	t.Helper()
	_, err := c.ImageInspect(t.Context(), ref)
	if docker.IsNotFound(err) {
		err = c.ImagePull(t.Context(), ref, docker.Platform{}, nil)
	}
	if err != nil {
		t.Fatalf("image %s: %v", ref, err)
	}
}

// removeOnCleanup deletes the container by ID when the test ends, whatever
// the test already did.
func removeOnCleanup(t *testing.T, c *docker.Client, id string) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context()) // t.Context is canceled before cleanups run
	t.Cleanup(func() {
		err := c.ContainerRemove(ctx, id, docker.RemoveOptions{Force: true, Volumes: true})
		if err != nil && !docker.IsNotFound(err) {
			t.Errorf("cleanup: remove %s: %v", id, err)
		}
	})
}

func TestEngineRunsALabeledContainerAndReadsItsLogs(t *testing.T) {
	c := engineClient(t)
	ctx := t.Context()
	p, err := c.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	info, err := c.Info(ctx)
	if err != nil || info.OSType != "linux" {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	t.Logf("daemon %s, API %s negotiated %s", info.ServerVersion, p.APIVersion, p.Negotiated)
	ensureImage(t, c, providedImage)

	run := runID(t)
	labels := labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: run, Owner: "understudy-test-" + run}, Test: true})
	created, err := c.ContainerCreate(ctx, "understudy-test-"+run,
		docker.Config{Image: providedImage, Entrypoint: []string{"/bin/sh", "-c", "echo hello"}, Labels: labels},
		docker.HostConfig{LogConfig: docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "2"}}},
		docker.NetworkingConfig{})
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	removeOnCleanup(t, c, created.ID)

	wait, err := c.ContainerWait(ctx, created.ID, docker.WaitNextExit)
	if err != nil {
		t.Fatalf("ContainerWait: %v", err)
	}
	if err := c.ContainerStart(ctx, created.ID); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}
	if code, err := wait.Result(); err != nil || code != 0 {
		t.Fatalf("exit = %d, %v; want 0", code, err)
	}
	logs, err := c.ContainerLogs(ctx, created.ID, docker.LogsOptions{})
	if err != nil {
		t.Fatalf("ContainerLogs: %v", err)
	}
	if stdout, _ := readAll(t, logs); stdout != "hello\n" {
		t.Errorf("stdout = %q, want hello", stdout)
	}
	if err := c.ContainerRemove(ctx, created.ID, docker.RemoveOptions{Force: true, Volumes: true}); err != nil {
		t.Fatalf("ContainerRemove: %v", err)
	}
	if _, err := c.ContainerInspect(ctx, created.ID); !docker.IsNotFound(err) {
		t.Errorf("inspect after remove = %v, want not found", err)
	}
}

func TestEngineSweepRemovesALeftoverContainer(t *testing.T) {
	c := engineClient(t)
	ctx := t.Context()
	ensureImage(t, c, providedImage)
	run := runID(t)
	owner := "understudy-test-" + run
	leftover := labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: "previous", Owner: owner}, Test: true})
	created, err := c.ContainerCreate(ctx, "understudy-test-"+run+"-leftover",
		docker.Config{Image: providedImage, Entrypoint: []string{"/bin/sh", "-c", "sleep 300"}, Labels: leftover},
		docker.HostConfig{}, docker.NetworkingConfig{})
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	removeOnCleanup(t, c, created.ID)
	if err := c.ContainerStart(ctx, created.ID); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}

	swept, err := c.Sweep(ctx, docker.Ownership{Instance: "current", Owner: owner})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !slices.Equal(swept.Containers, []string{created.ID}) {
		t.Errorf("swept %v, want only the leftover %s", swept.Containers, created.ID)
	}
	if _, err := c.ContainerInspect(ctx, created.ID); !docker.IsNotFound(err) {
		t.Errorf("leftover still present after Sweep: %v", err)
	}
}

// TestEnginePublishesAPortOnLoopback: the Lambda base images declare no
// ExposedPorts, so publishing needs both entries; the ephemeral host port
// is read back from inspect (containers.md 3.2).
func TestEnginePublishesAPortOnLoopback(t *testing.T) {
	c := engineClient(t)
	ctx := t.Context()
	ensureImage(t, c, providedImage)
	run := runID(t)
	labels := labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: run, Owner: "understudy-test-" + run}, Test: true})
	created, err := c.ContainerCreate(ctx, "understudy-test-"+run+"-published",
		docker.Config{Image: providedImage, Entrypoint: []string{"/bin/sh", "-c", "sleep 300"}, Labels: labels, ExposedPorts: docker.PortSet{"8000/tcp": {}}},
		docker.HostConfig{PortBindings: docker.PortMap{"8000/tcp": {{HostIP: "127.0.0.1", HostPort: "0"}}}},
		docker.NetworkingConfig{})
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	removeOnCleanup(t, c, created.ID)
	if err := c.ContainerStart(ctx, created.ID); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}
	inspected, err := c.ContainerInspect(ctx, created.ID)
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	got, ok := inspected.NetworkSettings.HostPort("8000/tcp")
	if !ok || got.Addr() != netip.MustParseAddr("127.0.0.1") || got.Port() == 0 {
		t.Errorf("HostPort = %v, %t; want 127.0.0.1 and an ephemeral port (Ports = %v)", got, ok, inspected.NetworkSettings.Ports)
	}
}

func TestEngineErrorsAreTyped(t *testing.T) {
	c := engineClient(t)
	_, err := c.ImageInspect(t.Context(), "public.ecr.aws/lambda/nonexistent:zz")
	if !docker.IsNotFound(err) || !strings.Contains(err.Error(), "No such image") {
		t.Errorf("inspect of a missing image = %v, want not found", err)
	}
}
