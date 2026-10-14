package topology_test

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/topology"
)

func TestAddresses(t *testing.T) {
	gw := netip.MustParseAddr("172.17.0.1")
	self := netip.MustParseAddr("172.22.0.2")
	cases := []struct {
		name string
		topo topology.Topology
		want topology.Addresses
	}{
		{
			name: "linux bridge listens on the gateway and maps localhost to the host",
			topo: topology.LinuxBridge{Gateway: gw},
			want: topology.Addresses{ListenerBind: gw, FunctionEndpointHost: gw, ExtraHosts: []string{"localhost:host-gateway"}, NeedsContainerListener: true},
		},
		{
			name: "docker desktop stays on loopback and hands functions the probed host",
			topo: topology.DockerDesktop{Host: topology.ProbedHost{Addr: netip.MustParseAddr("192.168.65.2")}},
			want: topology.Addresses{ListenerBind: netip.MustParseAddr("127.0.0.1"), FunctionEndpointHost: netip.MustParseAddr("192.168.65.2")},
		},
		{
			name: "docker desktop without a probe falls back to the documented host",
			topo: topology.DockerDesktop{Host: topology.FallbackHost{Cause: errors.New("no probe image was given")}},
			want: topology.Addresses{ListenerBind: netip.MustParseAddr("127.0.0.1"), FunctionEndpointHost: topology.DesktopDefaultHostAddr()},
		},
		{
			name: "docker desktop with no host recorded falls back to the documented host",
			topo: topology.DockerDesktop{},
			want: topology.Addresses{ListenerBind: netip.MustParseAddr("127.0.0.1"), FunctionEndpointHost: topology.DesktopDefaultHostAddr()},
		},
		{
			name: "rootful podman behaves like a linux bridge",
			topo: topology.Podman{Gateway: netip.MustParseAddr("10.88.0.1")},
			want: topology.Addresses{ListenerBind: netip.MustParseAddr("10.88.0.1"), FunctionEndpointHost: netip.MustParseAddr("10.88.0.1"), ExtraHosts: []string{"localhost:host-gateway"}, NeedsContainerListener: true},
		},
		{
			name: "in a container functions join understudy's network",
			topo: topology.InContainer{Network: "understudy", Self: self},
			want: topology.Addresses{ListenerBind: self, FunctionEndpointHost: self, FunctionNetwork: "understudy"},
		},
		{
			name: "a remote daemon's functions reach understudy at its address toward the daemon",
			topo: topology.Remote{Daemon: netip.MustParseAddr("172.30.0.3"), Self: netip.MustParseAddr("172.30.0.2")},
			want: topology.Addresses{ListenerBind: netip.MustParseAddr("172.30.0.2"), FunctionEndpointHost: netip.MustParseAddr("172.30.0.2"), NeedsContainerListener: true},
		},
	}
	for _, tc := range cases {
		got, err := tc.topo.Addresses()
		if err != nil {
			t.Errorf("%s: Addresses: %v", tc.name, err)
			continue
		}
		if got.ListenerBind != tc.want.ListenerBind || got.FunctionEndpointHost != tc.want.FunctionEndpointHost ||
			!slices.Equal(got.ExtraHosts, tc.want.ExtraHosts) || got.NeedsContainerListener != tc.want.NeedsContainerListener ||
			got.FunctionNetwork != tc.want.FunctionNetwork {
			t.Errorf("%s: Addresses = %+v, want %+v", tc.name, got, tc.want)
		}
		if got.ListenerBind.IsUnspecified() {
			t.Errorf("%s: listener binds %s; container listeners never bind every interface", tc.name, got.ListenerBind)
		}
	}
}

// Functions get an IPv4 literal (Addresses.FunctionEndpointHost), so a
// daemon reached over IPv6 is refused with the remedy.
func TestRemoteDaemonOverIPv6IsUnsupported(t *testing.T) {
	topo := topology.Remote{Daemon: netip.MustParseAddr("fd00::3"), Self: netip.MustParseAddr("fd00::2")}
	_, err := topo.Addresses()
	if uerr, ok := errors.AsType[*topology.UnsupportedError](err); !ok || !strings.Contains(uerr.Remedy, "IPv4") {
		t.Errorf("Addresses error = %v, want *UnsupportedError naming IPv4", err)
	}
}

func TestRootlessTopologiesAreUnsupported(t *testing.T) {
	for _, topo := range []topology.Topology{
		topology.RootlessDocker{HostAddr: netip.MustParseAddr("172.17.0.1")},
		topology.Podman{Rootless: true, Gateway: netip.MustParseAddr("10.88.0.1")},
	} {
		_, err := topo.Addresses()
		if uerr, ok := errors.AsType[*topology.UnsupportedError](err); !ok || uerr.Topology != topo {
			t.Errorf("%v: Addresses error = %v, want *UnsupportedError", topo, err)
			continue
		}
		msg := err.Error()
		for _, want := range []string{"not supported", "rootless", "control plane keeps working", "understudy doctor"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%v: message %q does not mention %q", topo, msg, want)
			}
		}
	}
}
