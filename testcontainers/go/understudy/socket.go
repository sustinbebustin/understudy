package understudy

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/testcontainers/testcontainers-go"
)

// socketTarget is where understudy looks for the Docker socket in its
// container (testcontainers.md 3.4).
const socketTarget = "/var/run/docker.sock"

// socketMount is the resolved WithDockerSocket request: the daemon-side
// socket path and the numeric group added for it.
type socketMount struct {
	source string
	gid    uint32
}

// resolveSocket finds the group and the daemon-side path of the socket to
// mount, before any container exists (testcontainers.md 8.5).
func resolveSocket(ctx context.Context, group socketGroup) (*socketMount, error) {
	var gid uint32
	switch g := group.(type) {
	case givenGroup:
		gid = g.gid
	case localGroup:
		cli, err := testcontainers.NewDockerClientWithOpts(ctx)
		if err != nil {
			return nil, fmt.Errorf("understudy: WithDockerSocket cannot reach Docker to read the socket's group: %w", err)
		}
		host := cli.DaemonHost()
		_ = cli.Close()
		if gid, err = daemonSocketGroup(host); err != nil {
			return nil, err
		}
	}
	source, err := daemonSocketPath(ctx)
	if err != nil {
		return nil, err
	}
	return &socketMount{source: source, gid: gid}, nil
}

// daemonSocketGroup is the group owning the socket of the daemon at host,
// a Docker host URL, when it is a local unix socket; any other daemon is a
// *SocketGroupError (testcontainers.md 8.5, 9.5).
func daemonSocketGroup(host string) (uint32, error) {
	path, ok := strings.CutPrefix(host, "unix://")
	if !ok {
		return 0, &SocketGroupError{Daemon: host}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("understudy: WithDockerSocket cannot read the group of the Docker socket: %w; pass WithDockerSocketGroup(<gid>), the numeric group owning the socket", err)
	}
	gid, ok := fileGroup(fi)
	if !ok {
		return 0, &SocketGroupError{Daemon: host}
	}
	return gid, nil
}

// daemonSocketPath is the socket's path on the daemon's host, as tc-go
// resolves it for Ryuk (testcontainers.md 4.4). tc-go panics when it finds
// no daemon; Run returns that as an error instead.
func daemonSocketPath(ctx context.Context) (path string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("understudy: WithDockerSocket cannot find the Docker socket to mount: %v", r)
		}
	}()
	return testcontainers.MustExtractDockerSocket(ctx), nil
}

// apply appends the socket's bind mount and group to hc, keeping the
// caller's own. A bind mount, unlike Binds, fails on a missing source
// instead of creating a root-owned directory (testcontainers.md 3.4).
func (s *socketMount) apply(hc *container.HostConfig) {
	hc.Mounts = append(hc.Mounts, mount.Mount{Type: mount.TypeBind, Source: s.source, Target: socketTarget})
	hc.GroupAdd = append(hc.GroupAdd, strconv.FormatUint(uint64(s.gid), 10))
}
