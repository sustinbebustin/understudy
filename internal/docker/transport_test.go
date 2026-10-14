package docker_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

// Every Engine call goes over a tcp:// daemon, plain or with TLS client
// certificates, including the streams that hold a connection open.
func TestTCPDaemonsServeEveryCallIncludingStreams(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport dockertest.Transport
		scheme    string
	}{
		{name: "plain", transport: dockertest.PlainTCP{}, scheme: "tcp://"},
		{name: "tls", transport: dockertest.MutualTLS{}, scheme: "tcp://"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, c := daemonWithImage(t, dockertest.Options{Transport: tc.transport})
			if got := c.Endpoint().String(); !strings.HasPrefix(got, tc.scheme) {
				t.Fatalf("Endpoint = %s, want a %s endpoint", got, tc.scheme)
			}
			ctx := t.Context()
			events, err := c.Events(ctx, docker.EventsOptions{Since: time.Unix(0, 1), Filters: docker.Filters{"event": {"die"}}})
			if err != nil {
				t.Fatalf("Events: %v", err)
			}
			defer events.Close()
			d.SetProgram(providedImage, dockertest.Exit{Code: 3, Output: []docker.Frame{{Stream: docker.Stdout, Payload: []byte("over tcp\n")}}})
			id := create(t, c, "fn", docker.Config{}, docker.HostConfig{LogConfig: docker.LogConfig{Type: "json-file"}})
			wait, err := c.ContainerWait(ctx, id, docker.WaitNextExit)
			if err != nil {
				t.Fatalf("ContainerWait: %v", err)
			}
			if err := c.ContainerStart(ctx, id); err != nil {
				t.Fatalf("ContainerStart: %v", err)
			}
			if code, err := wait.Result(); err != nil || code != 3 {
				t.Fatalf("Wait.Result = %d, %v; want 3", code, err)
			}
			logs, err := c.ContainerLogs(ctx, id, docker.LogsOptions{Follow: true})
			if err != nil {
				t.Fatalf("ContainerLogs: %v", err)
			}
			if stdout, _ := readAll(t, logs); stdout != "over tcp\n" {
				t.Errorf("logs = %q, want the container's output", stdout)
			}
			if e, err := events.Next(); err != nil || e.Actor.ID != id {
				t.Errorf("event = %+v, %v; want the container's die", e, err)
			}
		})
	}
}

// tlsEndpoint is the TLS endpoint of a fresh MutualTLS daemon.
func tlsEndpoint(t *testing.T) docker.TLS {
	t.Helper()
	e, ok := dockertest.New(t, dockertest.Options{Transport: dockertest.MutualTLS{}}).Endpoint().(docker.TLS)
	if !ok {
		t.Fatal("a MutualTLS daemon has no TLS endpoint")
	}
	return e
}

// A refused or misconfigured tcp:// daemon is unavailable for a reason
// whose message says what to change.
func TestMisconfiguredTCPDaemonsAreUnavailableWithTheFix(t *testing.T) {
	secure := tlsEndpoint(t)
	plain, ok := dockertest.New(t, dockertest.Options{Transport: dockertest.PlainTCP{}}).Endpoint().(docker.TCP)
	if !ok {
		t.Fatal("a PlainTCP daemon has no TCP endpoint")
	}

	closed, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := closed.Addr().String()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	missing := t.TempDir()
	if err := os.WriteFile(filepath.Join(missing, "ca.pem"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		endpoint docker.Endpoint
		reason   docker.UnavailableReason
		fix      string
	}{
		{name: "nothing listens", endpoint: docker.TCP{Addr: refused}, reason: docker.ConnectionRefused, fix: "check the host and port in DOCKER_HOST"},
		{name: "plain client, TLS daemon", endpoint: docker.TCP{Addr: secure.Addr}, reason: docker.TLSRequired, fix: "set DOCKER_TLS_VERIFY=1"},
		{name: "TLS client, plain daemon", endpoint: docker.TLS{Addr: plain.Addr, CertDir: secure.CertDir}, reason: docker.TLSNotOffered, fix: "unset DOCKER_TLS_VERIFY"},
		{name: "certificates of another CA", endpoint: docker.TLS{Addr: secure.Addr, CertDir: tlsEndpoint(t).CertDir}, reason: docker.HandshakeFailed, fix: "signed the daemon's certificate"},
		{name: "no certificates", endpoint: docker.TLS{Addr: secure.Addr, CertDir: missing}, reason: docker.CertificatesUnreadable, fix: "point DOCKER_CERT_PATH at the directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := docker.New(docker.Options{Endpoint: tc.endpoint})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, err = c.Info(t.Context())
			uerr, ok := errors.AsType[*docker.UnavailableError](err)
			if !ok {
				t.Fatalf("Info error = %v, want *docker.UnavailableError", err)
			}
			if uerr.Reason != tc.reason || !strings.Contains(err.Error(), tc.endpoint.String()) || !strings.Contains(err.Error(), tc.fix) {
				t.Errorf("Info = reason %v, message %q; want reason %v naming %s and %q", uerr.Reason, err, tc.reason, tc.endpoint, tc.fix)
			}
		})
	}
}
