package ddblocal_test

import (
	"net/netip"
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// DynamoDB Local is reached at its container address where the host routes
// to it (a Linux bridge, rootful Podman), on understudy's own network in a
// container, on a port published on a remote daemon's own address, and on a
// port published on loopback everywhere else (ADR 0014).
func TestNetworkFollowsTheTopology(t *testing.T) {
	gw := netip.MustParseAddr("172.17.0.1")
	cases := []struct {
		topo topology.Topology
		want ddblocal.Network
	}{
		{topology.LinuxBridge{Gateway: gw}, ddblocal.DefaultBridge{}},
		{topology.Podman{Gateway: gw}, ddblocal.DefaultBridge{}},
		{topology.Podman{Rootless: true, Gateway: gw}, ddblocal.Published{}},
		{topology.RootlessDocker{HostAddr: gw}, ddblocal.Published{}},
		{topology.DockerDesktop{}, ddblocal.Published{}},
		{topology.InContainer{Network: "understudy", Self: netip.MustParseAddr("172.22.0.2")}, ddblocal.Attached{Network: "understudy"}},
		{topology.Remote{Daemon: netip.MustParseAddr("172.30.0.3"), Self: netip.MustParseAddr("172.30.0.2")}, ddblocal.Published{HostIP: netip.MustParseAddr("172.30.0.3")}},
	}
	for _, tc := range cases {
		if got := ddblocal.NetworkFor(tc.topo); got != tc.want {
			t.Errorf("NetworkFor(%s) = %#v, want %#v", tc.topo, got, tc.want)
		}
	}
}
