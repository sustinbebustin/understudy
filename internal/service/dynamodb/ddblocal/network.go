package ddblocal

import "github.com/sustinbebustin/understudy/internal/topology"

// NetworkFor is how understudy reaches DynamoDB Local on top: at its
// default-bridge address where the host routes to container addresses, on
// understudy's own network in a container, on a port published on a remote
// daemon's own address, and otherwise on a port published on loopback (ADR
// 0014). Docker Desktop keeps containers in a VM, rootless engines keep
// them in a network namespace, and a remote daemon keeps them on another
// host, so none can be reached at a container address.
func NetworkFor(top topology.Topology) Network {
	switch t := top.(type) {
	case topology.LinuxBridge:
		return DefaultBridge{}
	case topology.Podman:
		if t.Rootless {
			return Published{}
		}
		return DefaultBridge{}
	case topology.InContainer:
		return Attached{Network: t.Network}
	case topology.Remote:
		return Published{HostIP: t.Daemon}
	case topology.DockerDesktop, topology.RootlessDocker:
		return Published{}
	}
	return Published{}
}
