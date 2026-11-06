package understudy_test

import (
	"slices"
	"testing"

	"github.com/moby/moby/api/types/mount"
	"github.com/testcontainers/testcontainers-go"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/testcontainers/go/understudy"
)

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
