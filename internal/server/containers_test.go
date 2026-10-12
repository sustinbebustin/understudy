package server

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/docker"
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
// without asking Docker.
func TestBridgeNetworkSkipsDetection(t *testing.T) {
	gw := netip.MustParseAddr("172.31.0.1")
	top, err := testHost(t, NetworkBridge{Gateway: gw}).topology(t.Context())
	if err != nil || top != (topology.LinuxBridge{Gateway: gw}) {
		t.Fatalf("topology = %v, %v; want the Linux bridge at %s", top, err, gw)
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

// DynamoDB Local is degraded with a failed start's error until a start
// succeeds; a start its caller abandoned changes nothing.
func TestDynamoDBLocalReportsAFailedStart(t *testing.T) {
	host := testHost(t, NetworkAuto{})
	host.socket = filepath.Join(t.TempDir(), "gone.sock")
	l := &lazyLocal{host: host, licence: ddblocal.Accepted{Digest: ddblocal.Digest}, storage: ddblocal.InMemory{}, log: slog.New(slog.DiscardHandler)}
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
	l := &lazyLocal{host: testHost(t, NetworkAuto{}), licence: ddblocal.Accepted{Digest: ddblocal.Digest}, storage: ddblocal.InMemory{}, log: slog.New(slog.DiscardHandler)}
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
