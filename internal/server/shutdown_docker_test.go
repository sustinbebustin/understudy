//go:build docker

package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// A Serve cancelled while DynamoDB Local starts in the background leaves
// no container of its instance behind: neither when cancelled as soon as
// it is ready, nor once the container has been created.
func TestShutdownDuringTheBackgroundStartLeavesNoContainer(t *testing.T) {
	client, err := docker.New(docker.Options{Socket: docker.DefaultSocket})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if _, err := client.ImageInspect(t.Context(), ddblocal.Reference()); err != nil {
		t.Skipf("serve starts DynamoDB Local in the background only when its pinned image %s is present: %v", ddblocal.Reference(), err)
	}
	for _, tc := range []struct {
		name       string
		cancelWhen func(instance string) bool
	}{
		{"at ready", func(string) bool { return true }},
		{"once its container exists", func(instance string) bool {
			cs, err := client.ContainerList(t.Context(), map[string]string{docker.LabelInstance: instance})
			return err == nil && len(cs) > 0
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready.json")
			srv, err := New(Config{ReadyFile: ready, LogOutput: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			instance := srv.wired.host.ownership.Instance
			ctx, cancel := context.WithCancel(context.Background())
			served := make(chan error, 1)
			go func() { served <- srv.Serve(ctx) }()
			poll(t, "the ready file", func() bool {
				_, err := os.Stat(ready)
				return err == nil
			})
			poll(t, "the moment to cancel", func() bool { return tc.cancelWhen(instance) })
			cancel()
			if err := <-served; err != nil {
				t.Fatalf("Serve = %v, want a clean shutdown", err)
			}
			left, err := client.ContainerList(t.Context(), map[string]string{docker.LabelInstance: instance})
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range left {
				t.Errorf("container %s of instance %s outlived the shutdown", c.ID, instance)
				if err := client.ContainerRemove(t.Context(), c.ID, docker.RemoveOptions{Force: true, Volumes: true}); err != nil {
					t.Errorf("removing it: %v", err)
				}
			}
		})
	}
}

// poll checks cond every few milliseconds until it holds, failing the test
// after a minute.
func poll(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(time.Minute)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case <-deadline:
			t.Fatalf("no %s within a minute", what)
		case <-tick.C:
		}
	}
}
