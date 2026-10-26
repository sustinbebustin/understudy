package understudy_test

import (
	"context"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/testcontainers/go/understudy"
)

// sessionChildren lists understudy's own containers carrying this tc-go
// session's label (testcontainers.md 3.6, 4.5). It takes its context so a
// cleanup can pass one that t.Context's cancellation does not reach.
func sessionChildren(ctx context.Context, t *testing.T, cli *testcontainers.DockerClient) []container.Summary {
	t.Helper()
	res, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All: true,
		Filters: make(client.Filters).
			Add("label", "understudy.dev/managed=true").
			Add("label", "org.testcontainers.sessionId="+testcontainers.SessionID()),
	})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	return res.Items
}

// After Terminate no child understudy started for this session remains:
// the children carry every session label, and Terminate's 10 s grace lets
// understudy drain them (testcontainers.md 8.6, 8.7, 9.3).
func TestTerminateRemovesEveryChildContainer(t *testing.T) {
	cli := dockerClient(t)
	c, err := understudy.Run(t.Context(), image(), understudy.WithDockerSocket())
	cleanup(t, c)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() {
		// t.Context is canceled before cleanups run, so the sweep of what a
		// failed run left uses its own context.
		ctx := context.Background()
		for _, kid := range sessionChildren(ctx, t, cli) {
			if _, err := cli.ContainerRemove(ctx, kid.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
				t.Errorf("removing leftover child %s: %v; remove it with docker rm -f %s", kid.ID, err, kid.ID)
			}
		}
	})
	invokeEcho(t, c)
	kids := sessionChildren(t.Context(), t, cli)
	if len(kids) == 0 {
		t.Fatal("no understudy child carries this session's label after an invoke")
	}
	for _, kid := range kids {
		for k, v := range testcontainers.GenericLabels() {
			if kid.Labels[k] != v {
				t.Errorf("child %s label %s = %q, want %q", kid.ID, k, kid.Labels[k], v)
			}
		}
	}
	if err := c.Terminate(t.Context()); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if left := sessionChildren(t.Context(), t, cli); len(left) != 0 {
		t.Errorf("after Terminate, %d understudy children remain: %+v", len(left), left)
	}
}

// The post-start Docker check sends the admin token, since health answers
// 401 without it (testcontainers.md 8.5, 3.3); and the socket mount is
// appended to the caller's mounts, never replacing them (testcontainers.md
// 8.5).
func TestDockerSocketWithTheTokenKeepsTheCallersMounts(t *testing.T) {
	scratch := testcontainers.ContainerMount{Source: testcontainers.GenericTmpfsMountSource{}, Target: "/scratch"}
	c := run(t, understudy.WithDockerSocket(), understudy.WithAdminToken(), testcontainers.WithMounts(scratch))
	ctx := t.Context()
	info := inspect(t, c)
	if !slices.ContainsFunc(info.HostConfig.Mounts, func(m mount.Mount) bool {
		return m.Type == mount.TypeTmpfs && m.Target == "/scratch"
	}) {
		t.Errorf("Mounts = %+v, want the caller's tmpfs at /scratch kept", info.HostConfig.Mounts)
	}
	if !slices.ContainsFunc(info.HostConfig.Mounts, func(m mount.Mount) bool {
		return m.Type == mount.TypeBind && m.Target == "/var/run/docker.sock"
	}) {
		t.Errorf("Mounts = %+v, want the socket's bind mount beside the caller's", info.HostConfig.Mounts)
	}
	a, err := c.Admin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h, err := a.Health(ctx); err != nil || h.Docker.Status != admin.Enabled {
		t.Errorf("health = %+v, %v; want Docker enabled", h, err)
	}
}

// A nil Option is refused before anything is created.
func TestRunRefusesANilOption(t *testing.T) {
	c, err := understudy.Run(t.Context(), image(), understudy.Option(nil))
	cleanup(t, c)
	if err == nil || c != nil {
		t.Errorf("Run with a nil Option = %v, %v; want an error and no container", c, err)
	}
}
