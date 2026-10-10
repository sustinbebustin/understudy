package docker_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

// requestedVersions returns the version prefix of every versioned request.
func requestedVersions(d *dockertest.Daemon) []string {
	var out []string
	for _, r := range d.Requests() {
		if v, ok := strings.CutPrefix(r.Path, "/v"); ok {
			version, _, _ := strings.Cut(v, "/")
			out = append(out, version)
		}
	}
	return out
}

func TestClientNegotiatesTheLowerOfClientAndDaemonVersions(t *testing.T) {
	cases := []struct {
		name   string
		opts   dockertest.Options
		want   string
		engine docker.Engine
	}{
		{name: "newer daemon gets the client maximum", opts: dockertest.Options{APIVersion: "1.55"}, want: "1.44", engine: docker.Moby{}},
		{name: "older daemon gets its own maximum", opts: dockertest.Options{APIVersion: "1.42"}, want: "1.42", engine: docker.Moby{}},
		{name: "silent daemon gets the client minimum", opts: dockertest.Options{HideAPIVersion: true}, want: "1.41", engine: docker.Moby{}},
		{
			name:   "podman is recognized by its libpod header",
			opts:   dockertest.Options{APIVersion: "1.44", Engine: docker.Podman{LibpodAPIVersion: "5.4.0"}},
			want:   "1.44",
			engine: docker.Podman{LibpodAPIVersion: "5.4.0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := dockertest.New(t, tc.opts)
			c := d.Client(t)
			if _, err := c.Info(t.Context()); err != nil {
				t.Fatalf("Info: %v", err)
			}
			p, err := c.Ping(t.Context())
			if err != nil {
				t.Fatalf("Ping: %v", err)
			}
			if p.Negotiated.String() != tc.want || p.Engine != tc.engine {
				t.Errorf("Ping = negotiated %s engine %#v, want %s %#v", p.Negotiated, p.Engine, tc.want, tc.engine)
			}
			if got := requestedVersions(d); len(got) != 1 || got[0] != tc.want {
				t.Errorf("versioned requests used %v, want [%s]", got, tc.want)
			}
		})
	}
}

func TestInfoReportsTheDaemonsCapabilities(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{Info: docker.Info{
		OperatingSystem: "Docker Desktop",
		CgroupDriver:    "none",
		SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless"},
	}})
	info, err := d.Client(t).Info(t.Context())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.OperatingSystem != "Docker Desktop" || info.CgroupDriver != "none" || !info.Rootless() {
		t.Errorf("Info = %+v, want Docker Desktop, cgroup driver none, rootless", info)
	}
	if (docker.Info{SecurityOptions: []string{"name=seccomp,profile=builtin"}}).Rootless() {
		t.Error("Rootless without name=rootless")
	}
}

func TestClientRefusesADaemonBelowTheMinimumVersion(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{APIVersion: "1.40"})
	_, err := d.Client(t).Info(t.Context())
	var verr *docker.VersionError
	if !errors.As(err, &verr) {
		t.Fatalf("Info error = %v, want *docker.VersionError", err)
	}
	if verr.Server.String() != "1.40" || verr.Minimum.String() != "1.41" {
		t.Errorf("VersionError = %+v, want server 1.40 minimum 1.41", verr)
	}
	if len(requestedVersions(d)) != 0 {
		t.Errorf("a versioned request was sent to a daemon below the minimum: %v", d.Requests())
	}
}

func TestNewRejectsAMinimumAboveTheClientMaximum(t *testing.T) {
	if _, err := docker.New(docker.Options{APIVersionMin: docker.APIVersion{Major: 1, Minor: 50}}); err == nil {
		t.Fatal("New accepted a minimum version the client cannot speak")
	}
}

func TestAPIVersionRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := docker.APIVersion{Major: rapid.IntRange(0, 99).Draw(t, "major"), Minor: rapid.IntRange(0, 999).Draw(t, "minor")}
		got, err := docker.ParseAPIVersion(v.String())
		if err != nil || got != v {
			t.Fatalf("ParseAPIVersion(%q) = %v, %v", v.String(), got, err)
		}
	})
}

func TestParseAPIVersionRejectsMalformedValues(t *testing.T) {
	for _, s := range []string{"", "1", "1.", ".4", "1.4.4", "v1.44", "1.-4", "a.b"} {
		if v, err := docker.ParseAPIVersion(s); err == nil {
			t.Errorf("ParseAPIVersion(%q) = %v, want an error", s, v)
		}
	}
}

func TestParseDockerHost(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{in: "", want: docker.DefaultSocket, ok: true},
		{in: "unix:///run/user/1000/docker.sock", want: "/run/user/1000/docker.sock", ok: true},
		{in: "tcp://127.0.0.1:2375"},
		{in: "ssh://me@host"},
		{in: "unix://"},
	}
	for _, tc := range cases {
		got, err := docker.ParseDockerHost(tc.in)
		var uerr *docker.UnsupportedEndpointError
		switch {
		case tc.ok && (err != nil || got != tc.want):
			t.Errorf("ParseDockerHost(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		case !tc.ok && !errors.As(err, &uerr):
			t.Errorf("ParseDockerHost(%q) error = %v, want *UnsupportedEndpointError", tc.in, err)
		}
	}
}

func TestUnreachableDaemonIsUnavailableWithTheReason(t *testing.T) {
	dir, err := os.MkdirTemp("", "dkr-") //nolint:usetesting // t.TempDir paths can exceed the unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove %s: %v", dir, err)
		}
	})

	refused := filepath.Join(dir, "refused.sock")
	l, err := new(net.ListenConfig).Listen(t.Context(), "unix", refused)
	if err != nil {
		t.Fatal(err)
	}
	unixL, ok := l.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener is %T, want *net.UnixListener", l)
	}
	unixL.SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		socket string
		reason docker.UnavailableReason
	}{
		{socket: filepath.Join(dir, "missing.sock"), reason: docker.SocketMissing},
		{socket: refused, reason: docker.ConnectionRefused},
	}
	if os.Geteuid() != 0 { // root connects regardless of the socket's mode
		forbidden := dockertest.New(t, dockertest.Options{}).Socket()
		if err := os.Chmod(forbidden, 0); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, struct {
			socket string
			reason docker.UnavailableReason
		}{socket: forbidden, reason: docker.PermissionDenied})
	}
	for _, tc := range cases {
		c, err := docker.New(docker.Options{Socket: tc.socket})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Ping(t.Context())
		var uerr *docker.UnavailableError
		if !docker.IsUnavailable(err) || !errors.As(err, &uerr) {
			t.Fatalf("Ping(%s) error = %v, want unavailable", tc.socket, err)
		}
		if uerr.Reason != tc.reason || !strings.Contains(err.Error(), tc.socket) || c.Socket() != tc.socket {
			t.Errorf("Ping(%s) = reason %v, message %q; want reason %v naming the socket", tc.socket, uerr.Reason, err, tc.reason)
		}
	}
}
