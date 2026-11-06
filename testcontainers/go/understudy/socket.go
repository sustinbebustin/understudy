package understudy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/system"
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
		if err := cli.Close(); err != nil {
			return nil, fmt.Errorf("understudy: closing the Docker client used to read the socket's group: %w", err)
		}
		var operatingSystem string
		if path, ok := strings.CutPrefix(host, "unix://"); ok {
			if operatingSystem, err = engineOperatingSystem(ctx, path); err != nil {
				return nil, err
			}
		}
		if gid, err = daemonSocketGroup(host, operatingSystem); err != nil {
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
// a Docker host URL, as a container sees it. Docker Desktop, which names
// itself in operatingSystem, mounts its VM's socket, owned by group 0 (TC-01,
// testcontainers.md 8.5). Elsewhere it is the group of the local unix
// socket; any other daemon is a *SocketGroupError (9.5).
func daemonSocketGroup(host, operatingSystem string) (uint32, error) {
	if strings.Contains(operatingSystem, "Docker Desktop") {
		return 0, nil
	}
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

// engineOperatingSystem asks the daemon listening on the unix socket at
// path for the OperatingSystem its GET /info reports. It speaks the Engine
// API directly because the module's package code imports moby's API types
// but not its client (ADR 0016, amendment of 2026-10-26).
func engineOperatingSystem(ctx context.Context, path string) (string, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/info", http.NoBody)
	if err != nil {
		return "", fmt.Errorf("understudy: building the Docker info request: %w", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return "", fmt.Errorf("understudy: WithDockerSocket cannot ask Docker at unix://%s which engine it runs, which decides the socket's group: %w; pass WithDockerSocketGroup(<gid>)", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("understudy: Docker at unix://%s answered GET /info with %s; pass WithDockerSocketGroup(<gid>), the numeric group owning the socket", path, resp.Status)
	}
	var info system.Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("understudy: decoding Docker's GET /info from unix://%s: %w", path, err)
	}
	return info.OperatingSystem, nil
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
