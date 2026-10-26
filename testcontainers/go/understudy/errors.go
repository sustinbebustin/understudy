package understudy

import (
	"fmt"
	"net/netip"

	"github.com/sustinbebustin/understudy/admin"
)

// ExposureError refuses publishing port 4566 beyond loopback without the
// admin token, before any container is created (testcontainers.md 8.4,
// 9.5).
type ExposureError struct {
	Addr netip.Addr
}

func (e *ExposureError) Error() string {
	return fmt.Sprintf("understudy: publishing port 4566 on %s exposes the admin API, which has no token in the container; add WithAdminToken() or publish on 127.0.0.1.", e.Addr)
}

// SocketGroupError refuses WithDockerSocket on a daemon whose socket cannot
// be stat'ed locally, before any container is created (testcontainers.md
// 8.5, 9.5). Daemon is the Docker host the library connects to.
type SocketGroupError struct {
	Daemon string
}

func (e *SocketGroupError) Error() string {
	return fmt.Sprintf("understudy: cannot read the group of the Docker socket on %s, which is not a local unix socket; pass WithDockerSocketGroup(<gid>), the numeric group owning /var/run/docker.sock on the daemon's host.", e.Daemon)
}

// DockerUnavailableError reports a mounted socket that understudy cannot
// use: after start, its health reports Docker with a status other than
// enabled (testcontainers.md 8.5, 9.5).
type DockerUnavailableError struct {
	Status admin.ServiceState
	Reason string
}

func (e *DockerUnavailableError) Error() string {
	return fmt.Sprintf("understudy: the Docker socket is mounted but understudy reports Docker %s: %s; Lambda and DynamoDB will not run. Check the socket group (WithDockerSocketGroup) or run without WithDockerSocket().", e.Status, e.Reason)
}
