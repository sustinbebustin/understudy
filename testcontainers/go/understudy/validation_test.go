package understudy_test

import (
	"errors"
	"io/fs"
	"net/netip"
	"path/filepath"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/testcontainers/go/understudy"
)

// The module refuses a misconfiguration before creating anything, so it
// fails at once rather than after the startup timeout, with the texts of
// testcontainers.md 9.5 in Go's option names.

func TestValidationTexts(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{
			&understudy.ExposureError{Addr: netip.MustParseAddr("0.0.0.0")},
			"understudy: publishing port 4566 on 0.0.0.0 exposes the admin API, which has no token in the container; add WithAdminToken() or publish on 127.0.0.1.",
		},
		{
			&understudy.ExposureError{Addr: netip.MustParseAddr("2001:db8::1")},
			"understudy: publishing port 4566 on 2001:db8::1 exposes the admin API, which has no token in the container; add WithAdminToken() or publish on 127.0.0.1.",
		},
		{
			&understudy.SocketGroupError{Daemon: "tcp://10.0.0.5:2375"},
			"understudy: cannot read the group of the Docker socket on tcp://10.0.0.5:2375, which is not a local unix socket; pass WithDockerSocketGroup(<gid>), the numeric group owning /var/run/docker.sock on the daemon's host.",
		},
		{
			&understudy.DockerUnavailableError{Status: admin.Degraded, Reason: "the Docker Engine does not answer"},
			"understudy: the Docker socket is mounted but understudy reports Docker degraded: the Docker Engine does not answer; Lambda and DynamoDB will not run. Check the socket group (WithDockerSocketGroup) or run without WithDockerSocket().",
		},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%T.Error() = %q, want %q", c.err, got, c.want)
		}
	}
}

// Publishing beyond loopback, every interface included, needs the admin
// token; without it Run refuses before any container exists, whatever the
// order of the options (testcontainers.md 8.4, 9.5, 9.7).
func TestPublishingBeyondLoopbackWithoutTheTokenIsRefused(t *testing.T) {
	cli := dockerClient(t)
	before := len(imageContainers(t, cli))
	for _, addr := range []netip.Addr{
		netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("::"),
		netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("2001:db8::1"),
	} {
		orders := [][]testcontainers.ContainerCustomizer{
			{understudy.WithPublishedOn(addr)},
			{understudy.WithPublishedOn(addr), understudy.WithSeed(1)},
			{understudy.WithSeed(1), understudy.WithPublishedOn(addr)},
		}
		for _, opts := range orders {
			c, err := understudy.Run(t.Context(), image(), opts...)
			cleanup(t, c)
			ee, ok := errors.AsType[*understudy.ExposureError](err)
			if !ok || ee.Addr != addr {
				t.Errorf("Run publishing on %s without the token = %v, want an *ExposureError for it", addr, err)
				continue
			}
			want := "understudy: publishing port 4566 on " + addr.String() + " exposes the admin API, which has no token in the container; add WithAdminToken() or publish on 127.0.0.1."
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
			if c != nil {
				t.Errorf("Run publishing on %s returned a container with the refusal", addr)
			}
		}
	}
	if after := len(imageContainers(t, cli)); after != before {
		t.Errorf("refused runs left %d containers of %s, had %d", after, image(), before)
	}
}

// 127.0.0.1 needs no token: it is the default binding (testcontainers.md
// 8.4).
func TestPublishingOnLoopbackNeedsNoToken(t *testing.T) {
	c := run(t, understudy.WithPublishedOn(netip.MustParseAddr("127.0.0.1")))
	want := []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}}
	if got := inspect(t, c).HostConfig.PortBindings[network.MustParsePort("4566/tcp")]; !slices.Equal(got, want) {
		t.Errorf("PortBindings[4566/tcp] = %v, want %v", got, want)
	}
}

// A snapshot file that does not exist fails Run with the stat error, before
// any container (testcontainers.md 8.2).
func TestMissingSnapshotIsRefusedBeforeAnyContainer(t *testing.T) {
	cli := dockerClient(t)
	before := len(imageContainers(t, cli))
	missing := filepath.Join(t.TempDir(), "absent.tar")
	c, err := understudy.Run(t.Context(), image(), understudy.WithSnapshot(missing))
	cleanup(t, c)
	if pe, ok := errors.AsType[*fs.PathError](err); !ok || pe.Path != missing || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Run with a missing snapshot = %v, want an error wrapping the *fs.PathError for %s", err, missing)
	}
	if c != nil {
		t.Error("Run with a missing snapshot returned a container")
	}
	if after := len(imageContainers(t, cli)); after != before {
		t.Errorf("a refused run left %d containers of %s, had %d", after, image(), before)
	}
}
