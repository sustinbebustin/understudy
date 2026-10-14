package server

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// testHost is a container host on a socket path that is there but answers
// nothing, so no test reaches a real daemon.
func testHost(t *testing.T, network LambdaNetwork) *containerHost {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return testHostOn(t, network, socket)
}

// testHostOn is a container host on the Docker socket at socket.
func testHostOn(t *testing.T, network LambdaNetwork, socket string) *containerHost {
	t.Helper()
	clk, err := clock.New(clock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := newContainerHost(DockerSocket{Path: socket}, network, docker.Ownership{Instance: "test", Owner: "run-test"}, clk, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.close(); err != nil {
			t.Errorf("close = %v", err)
		}
	})
	return h
}

// lambda.network bridge is the Linux bridge at the configured gateway,
// without topology detection, when the gateway is a Docker bridge
// network's. Any other address, such as a private LAN address the
// configuration cannot tell from a bridge gateway, is refused, so the
// unauthenticated AWS API never opens on it.
func TestBridgeNetworkNeedsADockerBridgeGateway(t *testing.T) {
	daemon := dockertest.New(t, dockertest.Options{BridgeSubnet: netip.MustParsePrefix("172.31.0.0/16")})
	gw := netip.MustParseAddr("172.31.0.1")
	top, err := testHostOn(t, NetworkBridge{Gateway: gw}, daemon.Socket()).topology(t.Context())
	if err != nil || top != (topology.LinuxBridge{Gateway: gw}) {
		t.Fatalf("topology = %v, %v; want the Linux bridge at %s", top, err, gw)
	}
	lan := netip.MustParseAddr("192.168.1.10")
	_, err = testHostOn(t, NetworkBridge{Gateway: lan}, daemon.Socket()).topology(t.Context())
	if ge, ok := errors.AsType[*BridgeGatewayError](err); !ok || ge.Gateway != lan || !slices.Contains(ge.Known, gw) {
		t.Fatalf("topology at %s = %v, want a BridgeGatewayError naming the daemon's gateway %s", lan, err, gw)
	}
}

// lambda.network disabled refuses every function container, before the
// listener's port or Docker is needed.
func TestDisabledNetworkRefusesFunctionContainers(t *testing.T) {
	_, err := testHost(t, NetworkDisabled{}).lambda(t.Context())
	if de, ok := errors.AsType[*LambdaNetworkDisabledError](err); !ok || !strings.Contains(de.Error(), "set lambda.network to auto or bridge") {
		t.Fatalf("lambda = %v, want a LambdaNetworkDisabledError", err)
	}
}

// DynamoDB Local is reached at its container address where the host routes
// to it (a Linux bridge, rootful Podman), on understudy's own network in a
// container, on a port published on a remote daemon's own address, and on a
// port published on loopback everywhere else (ADR 0014).
func TestDynamoDBLocalNetworkFollowsTheTopology(t *testing.T) {
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
		if got := localNetwork(tc.topo); got != tc.want {
			t.Errorf("localNetwork(%s) = %#v, want %#v", tc.topo, got, tc.want)
		}
	}
}

// DynamoDB Local is degraded with a failed start's error until a start
// succeeds; a start its caller abandoned changes nothing.
func TestDynamoDBLocalReportsAFailedStart(t *testing.T) {
	host := testHost(t, NetworkAuto{})
	host.endpoint = docker.Unix{Path: filepath.Join(t.TempDir(), "gone.sock")}
	l := &lazyLocal{host: host, mode: DynamoDBEnabled{}, storage: ddblocal.InMemory{}, log: slog.New(slog.DiscardHandler)}
	if s := l.status(); s.Status != admin.Enabled {
		t.Fatalf("status before any start = %+v, want enabled", s)
	}
	if err := l.Start(t.Context()); err == nil {
		t.Fatal("Start without a Docker socket succeeded")
	}
	if s := l.status(); s.Status != admin.Degraded || !strings.Contains(s.Reason, "no Docker socket") {
		t.Fatalf("status after a failed start = %+v, want degraded naming the socket", s)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	l.record(cancelled, context.Canceled)
	if s := l.status(); s.Status != admin.Degraded || !strings.Contains(s.Reason, "no Docker socket") {
		t.Fatalf("status after an abandoned start = %+v, want the failed start still reported", s)
	}
	l.record(t.Context(), nil)
	if s := l.status(); s.Status != admin.Enabled {
		t.Fatalf("status after a start succeeded = %+v, want enabled", s)
	}
}

// Once stopped, DynamoDB Local refuses to start, so nothing launches after
// shutdown removed the container.
func TestDynamoDBLocalRefusesStartsAfterStop(t *testing.T) {
	l := &lazyLocal{host: testHost(t, NetworkAuto{}), mode: DynamoDBEnabled{}, storage: ddblocal.InMemory{}, log: slog.New(slog.DiscardHandler)}
	if err := l.stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := l.Start(t.Context()); !errors.Is(err, errShuttingDown) {
		t.Fatalf("Start after stop = %v, want errShuttingDown", err)
	}
	if s := l.status(); s.Status != admin.Enabled {
		t.Fatalf("status after a refused start = %+v; shutdown is not a failure of DynamoDB Local", s)
	}
}

// DynamoDB turned off by configuration refuses every start with the
// setting to change, never touches Docker, and reports itself disabled.
func TestDynamoDBLocalDisabledByConfiguration(t *testing.T) {
	host := testHost(t, NetworkAuto{})
	host.endpoint = docker.Unix{Path: filepath.Join(t.TempDir(), "gone.sock")}
	l := &lazyLocal{host: host, mode: DynamoDBDisabled{}, storage: ddblocal.InMemory{}, log: slog.New(slog.DiscardHandler)}
	de, ok := errors.AsType[*dynamodb.DisabledError](l.Start(t.Context()))
	if !ok || !strings.Contains(de.Reason, "dynamodb.enabled is false") {
		t.Fatalf("Start = %v, want a *dynamodb.DisabledError naming dynamodb.enabled", de)
	}
	if s := l.status(); s.Status != admin.Disabled || s.Reason != "disabled by configuration" {
		t.Fatalf("status = %+v, want disabled by configuration", s)
	}
	if _, ok := l.current(); ok {
		t.Error("a disabled DynamoDB built a DynamoDB Local manager")
	}
}
