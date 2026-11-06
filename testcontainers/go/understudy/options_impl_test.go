package understudy

import (
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func apply(t *testing.T, opts ...Option) options {
	t.Helper()
	o := newOptions()
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			t.Fatalf("option: %v", err)
		}
	}
	return o
}

// WithDockerSocketGroup implies WithDockerSocket and its group wins in
// either order, since Run applies every option before checking any
// (testcontainers.md 9.5).
func TestSocketGroupOptionsCommute(t *testing.T) {
	for name, opts := range map[string][]Option{
		"group then socket": {WithDockerSocketGroup(7), WithDockerSocket()},
		"socket then group": {WithDockerSocket(), WithDockerSocketGroup(7)},
		"group alone":       {WithDockerSocketGroup(7)},
	} {
		if got := apply(t, opts...).socket; got != (givenGroup{gid: 7}) {
			t.Errorf("%s: socket = %#v, want group 7", name, got)
		}
	}
	if got := apply(t, WithDockerSocket()).socket; got != (localGroup{}) {
		t.Errorf("WithDockerSocket alone: socket = %#v, want the local socket's group", got)
	}
	if got := apply(t).socket; got != nil {
		t.Errorf("no socket option: socket = %#v, want none", got)
	}
}

// 127.0.0.1 needs no token; beyond loopback the token is needed
// (testcontainers.md 8.4, 9.5).
func TestExposureNeedsTheTokenOnlyBeyondLoopback(t *testing.T) {
	cases := []struct {
		opts    []Option
		refused bool
	}{
		{nil, false},
		{[]Option{WithPublishedOn(netip.MustParseAddr("127.0.0.1"))}, false},
		{[]Option{WithPublishedOn(netip.MustParseAddr("0.0.0.0"))}, true},
		{[]Option{WithPublishedOn(netip.MustParseAddr("0.0.0.0")), WithAdminToken()}, false},
	}
	for _, c := range cases {
		o := apply(t, c.opts...)
		err := o.exposure()
		if _, refused := errors.AsType[*ExposureError](err); refused != c.refused {
			t.Errorf("publish %v token %t: exposure = %v, want refused %t", o.publish, o.token != "", err, c.refused)
		}
	}
}

// Shape errors in an option's argument are refused when Run applies it.
func TestOptionArgumentShapes(t *testing.T) {
	dir := t.TempDir()
	for name, opt := range map[string]Option{
		"nil clock":         WithClock(nil),
		"zero address":      WithPublishedOn(netip.Addr{}),
		"snapshot is a dir": WithSnapshot(dir),
	} {
		o := newOptions()
		if err := opt(&o); err == nil || !strings.HasPrefix(err.Error(), "understudy: ") {
			t.Errorf("%s: err = %v, want an understudy refusal", name, err)
		}
		if o.snapshot != "" || o.publish.IsValid() || len(o.env) != 0 {
			t.Errorf("%s: a refused option set %+v", name, o)
		}
	}
}

// A loopback address other than 127.0.0.1 is refused before any container:
// the endpoint and the wait dial the daemon's host, 127.0.0.1 for a local
// daemon, so Run would otherwise fail only after the startup timeout
// (testcontainers.md 8.2, 9.5).
func TestOtherLoopbackAddressesAreRefused(t *testing.T) {
	for _, addr := range []string{"127.0.0.2", "::1"} {
		c, err := Run(t.Context(), "understudy:never-pulled", WithPublishedOn(netip.MustParseAddr(addr)))
		want := "understudy: WithPublishedOn(" + addr + ") is a loopback address the endpoint never dials: the endpoint and the readiness wait reach port 4566 at the Docker host's address, 127.0.0.1 for a local daemon; publish on 127.0.0.1, or on 0.0.0.0 with WithAdminToken()"
		if err == nil || err.Error() != want {
			t.Errorf("Run publishing on %s = %v, want %q", addr, err, want)
		}
		if c != nil {
			t.Errorf("Run publishing on %s returned a container with the refusal", addr)
		}
	}
}

// The group is read only from a local unix socket; any other daemon names
// itself in the socket-group text (testcontainers.md 8.5, 9.5).
func TestDaemonSocketGroup(t *testing.T) {
	for _, host := range []string{"tcp://10.0.0.5:2375", "npipe:////./pipe/docker_engine", "ssh://docker@remote"} {
		_, err := daemonSocketGroup(host)
		if se, ok := errors.AsType[*SocketGroupError](err); !ok || se.Daemon != host {
			t.Errorf("daemonSocketGroup(%q) = %v, want a *SocketGroupError naming it", host, err)
		}
	}

	sock := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	gid, err := daemonSocketGroup("unix://" + sock)
	if err != nil || int(gid) != os.Getegid() {
		t.Errorf("daemonSocketGroup(unix://%s) = %d, %v; want the file's group %d", sock, gid, err, os.Getegid())
	}

	missing := filepath.Join(t.TempDir(), "absent.sock")
	_, err = daemonSocketGroup("unix://" + missing)
	if _, isGroupErr := errors.AsType[*SocketGroupError](err); isGroupErr || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("daemonSocketGroup on a missing socket = %v, want the stat error, not the remote-daemon text", err)
	}
}

// localhost is reported as 127.0.0.1 and an IPv6 host is bracketed
// (testcontainers.md 8.2).
func TestEndpointURL(t *testing.T) {
	for _, c := range []struct{ host, want string }{
		{"localhost", "http://127.0.0.1:4566"},
		{"127.0.0.1", "http://127.0.0.1:4566"},
		{"10.0.0.5", "http://10.0.0.5:4566"},
		{"::1", "http://[::1]:4566"},
		{"[2001:db8::1]", "http://[2001:db8::1]:4566"},
		{"docker.internal", "http://docker.internal:4566"},
	} {
		if got := endpointURL(c.host, "4566"); got != c.want {
			t.Errorf("endpointURL(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

// Ready means a health body with "ready": true, nothing less
// (testcontainers.md 8.3, 9.7).
func TestReportsReady(t *testing.T) {
	for body, want := range map[string]bool{
		`{"ready": true, "version": "dev"}`:                                      true,
		`{"ready": false, "waiting": ["sqs"]}`:                                   false,
		`{"error": {"code": "unauthorized", "message": "no token", "hint": ""}}`: false,
		`not json`: false,
		``:         false,
	} {
		if got := reportsReady(strings.NewReader(body)); got != want {
			t.Errorf("reportsReady(%q) = %t, want %t", body, got, want)
		}
	}
}
