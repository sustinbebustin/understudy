package understudy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/sustinbebustin/understudy/admin"
)

// DefaultImage is the release image of the understudy version this module
// is released with, in lockstep (testcontainers.md 8.8). Run takes the image
// explicitly, by tc-go's convention (testcontainers.md 4.1).
const DefaultImage = "ghcr.io/sustinbebustin/understudy:0.0.0"

const (
	// apiPort is understudy's listener in the container (testcontainers.md
	// 3.2).
	apiPort = "4566/tcp"
	// startupTimeout bounds the wait, set explicitly though it is tc-go's
	// default; understudy is ready in milliseconds, so it bounds only image
	// pulls and failures (testcontainers.md 8.3).
	startupTimeout = 60 * time.Second
	// loopbackHost is the default publish address (testcontainers.md 8.4)
	// and the endpoint host reported for localhost (testcontainers.md 8.2).
	loopbackHost = "127.0.0.1"
)

// Container is a started understudy container. Terminate, inherited from
// testcontainers.Container, stops it with tc-go's 10 s grace, which lets
// understudy remove its own containers before it exits (testcontainers.md
// 8.6).
type Container struct {
	testcontainers.Container

	// token is the admin token WithAdminToken set, empty without it.
	token string
}

// Run starts img with understudy's module settings and the options given,
// module options (Option) and tc-go's own customizers alike, and returns
// once understudy reports ready: GET /_understudy/v1/health answers 200
// with "ready": true (testcontainers.md 8.3).
//
// Run applies every Option before checking any, then refuses, with a nil
// *Container and before creating anything, publishing beyond loopback
// without the admin token (*ExposureError) and a Docker socket whose group
// it cannot read (*SocketGroupError). tc-go's customizers apply after the
// module's settings, so a caller's WithEnv or WithHostConfigModifier wins.
//
// Once a container exists, a failed start, a wait that sees understudy
// exit, or a mounted socket understudy cannot use (*DockerUnavailableError)
// returns the container with the error, so testcontainers.CleanupContainer
// registered before the error check removes it (testcontainers.md 4.1).
func Run(ctx context.Context, img string, opts ...testcontainers.ContainerCustomizer) (*Container, error) {
	o := newOptions()
	for _, opt := range opts {
		mod, ok := opt.(Option)
		if !ok {
			continue
		}
		if mod == nil {
			return nil, errors.New("understudy: Run got a nil Option; pass the options this package's With functions return")
		}
		if err := mod(&o); err != nil {
			return nil, err
		}
	}
	if err := o.exposure(); err != nil {
		return nil, err
	}
	var sock *socketMount
	if o.socket != nil {
		var err error
		if sock, err = resolveSocket(ctx, o.socket); err != nil {
			return nil, err
		}
	}
	module, err := o.customizers(sock)
	if err != nil {
		return nil, err
	}

	ctr, err := testcontainers.Run(ctx, img, append(module, opts...)...)
	var c *Container
	if ctr != nil {
		c = &Container{Container: ctr, token: o.token}
	}
	if err != nil {
		return c, fmt.Errorf("understudy: starting %s: %w", img, err)
	}
	if sock != nil {
		if err := c.checkDocker(ctx); err != nil {
			return c, err
		}
	}
	return c, nil
}

// customizers are the module's settings as tc-go customizers: the port and
// its binding, the environment, the socket, the snapshot and the wait.
func (o *options) customizers(sock *socketMount) ([]testcontainers.ContainerCustomizer, error) {
	env := maps.Clone(o.env)
	// The session's Ryuk filter labels, all five, go on every container
	// understudy starts, so Ryuk reaps them too (testcontainers.md 8.6,
	// 4.5, 9.3).
	labels, err := json.Marshal(testcontainers.GenericLabels())
	if err != nil {
		return nil, fmt.Errorf("understudy: encoding tc-go's session labels for UNDERSTUDY_DOCKER_LABELS: %w", err)
	}
	env["UNDERSTUDY_DOCKER_LABELS"] = string(labels)
	if o.token != "" {
		// understudy refuses the token beyond loopback without the open
		// AWS API (testcontainers.md 3.3).
		env["UNDERSTUDY_ADMIN_TOKEN"] = o.token
		env["UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API"] = "true"
	}
	if o.snapshot != "" {
		env["UNDERSTUDY_LOAD_SNAPSHOT"] = snapshotPath
	}
	publish := o.publish
	if !publish.IsValid() {
		publish = netip.MustParseAddr(loopbackHost)
	}
	port := network.MustParsePort(apiPort)

	cs := []testcontainers.ContainerCustomizer{
		testcontainers.WithExposedPorts(apiPort),
		testcontainers.WithEnv(env),
		// tc-go keeps a binding a modifier set for an exposed port
		// (testcontainers.md 4.3).
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			if hc.PortBindings == nil {
				hc.PortBindings = network.PortMap{}
			}
			hc.PortBindings[port] = []network.PortBinding{{HostIP: publish, HostPort: "0"}}
			if sock != nil {
				sock.apply(hc)
			}
		}),
		testcontainers.WithWaitStrategy(readiness(o.token)),
	}
	if o.snapshot != "" {
		cs = append(cs, testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath: o.snapshot, ContainerFilePath: snapshotPath, FileMode: 0o644,
		}))
	}
	return cs, nil
}

// readiness waits for health to answer 200 with "ready": true, never the
// image's HEALTHCHECK, an open port or a sleep (testcontainers.md 8.3, 9.7).
// tc-go's HTTP wait fails at once when the container exits (testcontainers.md
// 4.2).
func readiness(token string) *wait.HTTPStrategy {
	s := wait.ForHTTP(admin.Prefix + "/health").
		WithPort(apiPort).
		WithForcedIPv4LocalHost().
		WithResponseMatcher(reportsReady).
		WithStartupTimeout(startupTimeout)
	if token != "" {
		s = s.WithHeaders(map[string]string{"Authorization": "Bearer " + token})
	}
	return s
}

// reportsReady reports whether a health body says ready.
func reportsReady(body io.Reader) bool {
	var h admin.Health
	return json.NewDecoder(body).Decode(&h) == nil && h.Ready
}

// Endpoint is http://<host>:<mapped port of 4566/tcp>, with a host of
// localhost reported as 127.0.0.1 so SDKs address S3 path style, and an
// IPv6 host bracketed (testcontainers.md 8.2).
func (c *Container) Endpoint(ctx context.Context) (string, error) {
	host, err := c.Host(ctx)
	if err != nil {
		return "", fmt.Errorf("understudy: reading the container's host: %w", err)
	}
	port, err := c.MappedPort(ctx, apiPort)
	if err != nil {
		return "", fmt.Errorf("understudy: reading the host port of %s: %w", apiPort, err)
	}
	return endpointURL(host, port.Port()), nil
}

// endpointURL joins a daemon host, as tc-go reports it, and a port.
func endpointURL(host, port string) string {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "localhost" {
		host = loopbackHost
	}
	return "http://" + net.JoinHostPort(host, port)
}

// Env is the environment that points an SDK at the container, with the
// names and values of understudytest's Instance.Env (testcontainers.md 3.8).
func (c *Container) Env(ctx context.Context) (map[string]string, error) {
	endpoint, err := c.Endpoint(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"AWS_ENDPOINT_URL":      endpoint,
		"AWS_ACCESS_KEY_ID":     "test",
		"AWS_SECRET_ACCESS_KEY": "test",
		"AWS_REGION":            "us-east-1",
		"AWS_DEFAULT_REGION":    "us-east-1",
	}, nil
}

// Admin returns an admin client for the container, sending the admin token
// when WithAdminToken set one.
func (c *Container) Admin(ctx context.Context) (*admin.Client, error) {
	endpoint, err := c.Endpoint(ctx)
	if err != nil {
		return nil, err
	}
	return admin.New(endpoint, admin.Options{Token: c.token})
}

// Settle waits up to timeout for understudy's tracked work to finish
// (GET /_understudy/v1/settle). On timeout it returns admin's *admin.Error
// with code condition_not_met, naming the pending work.
func (c *Container) Settle(ctx context.Context, timeout time.Duration) error {
	a, err := c.Admin(ctx)
	if err != nil {
		return err
	}
	_, err = a.Settle(ctx, timeout)
	return err
}

// AdvanceClock moves the emulator clock forward by d
// (POST /_understudy/v1/clock, op advance).
func (c *Container) AdvanceClock(ctx context.Context, d time.Duration) error {
	a, err := c.Admin(ctx)
	if err != nil {
		return err
	}
	_, err = a.ChangeClock(ctx, admin.Advance{By: d})
	return err
}

// Reset drops all service state, keeping the clock and the fault rules
// (POST /_understudy/v1/reset with an empty request).
func (c *Container) Reset(ctx context.Context) error {
	a, err := c.Admin(ctx)
	if err != nil {
		return err
	}
	_, err = a.Reset(ctx, admin.ResetRequest{})
	return err
}

// checkDocker fails the start unless understudy reports Docker enabled: a
// socket that is mounted but unusable must never pass silently
// (testcontainers.md 8.5, 9.7).
func (c *Container) checkDocker(ctx context.Context) error {
	a, err := c.Admin(ctx)
	if err != nil {
		return err
	}
	h, err := a.Health(ctx)
	if err != nil {
		return fmt.Errorf("understudy: reading health to check the mounted Docker socket: %w", err)
	}
	if h.Docker.Status != admin.Enabled {
		return &DockerUnavailableError{Status: h.Docker.Status, Reason: h.Docker.Reason}
	}
	return nil
}
