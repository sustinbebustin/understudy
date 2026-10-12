package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/ref"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// pingTimeout bounds the Docker ping behind the health endpoint.
const pingTimeout = 2 * time.Second

// containerHost is the Docker Engine Lambda and DynamoDB Local run on,
// reached at first use (ARCHITECTURE.md 14.5): the topology is detected,
// and the launcher, the Runtime API server and the container listener are
// built, only when a function environment, an image or DynamoDB Local
// first needs them. It stands in for the launcher, the Runtime API and the
// log source the Lambda pool and execution layer are built with, since
// those need the topology and the main listener's port, which exist only
// later.
type containerHost struct {
	// client is nil under DockerDisabled.
	client *docker.Client
	socket string
	// network is how function containers reach understudy; never nil.
	network   LambdaNetwork
	ownership docker.Ownership
	clock     *clock.Clock
	log       *slog.Logger
	detector  topology.Detector

	// mu serializes detection and construction and guards the fields
	// below.
	mu sync.Mutex
	// port is the main listener's port, once Serve listens.
	port uint16
	// openListener opens the container listener on an address; Serve
	// sets it with the port.
	openListener func(netip.AddrPort) error
	top          topology.Topology
	run          *lambdaHost
	// built closes once run is set.
	built chan struct{}
}

// lambdaHost is what function environments run on.
type lambdaHost struct {
	launcher *launcher.Launcher
	api      *runtimeapi.Server
	logs     lambda.LogSource
}

// The host stands in for the parts the Lambda data plane is built with.
var (
	_ pool.Launcher      = (*containerHost)(nil)
	_ pool.Runtime       = (*containerHost)(nil)
	_ lambda.ImageSource = (*containerHost)(nil)
	_ lambda.LogSource   = (*containerHost)(nil)
)

// NoDockerError is a container needed while Docker is disabled or its
// socket is missing. The control planes keep working.
type NoDockerError struct{ Reason string }

func (e *NoDockerError) Error() string { return e.Reason }

// NotServingError is a container needed before Serve listens: containers
// reach the emulator at the main listener's port.
type NotServingError struct{}

func (*NotServingError) Error() string {
	return "server: function containers need the main listener's port, and the server is not serving yet; start Serve first"
}

// LambdaNetworkDisabledError is a function container needed while
// lambda.network is disabled.
type LambdaNetworkDisabledError struct{}

func (*LambdaNetworkDisabledError) Error() string {
	return "lambda.network is disabled by configuration, so no function container runs while every Lambda control plane operation keeps working; set lambda.network to auto or bridge to run functions"
}

func newContainerHost(setting Docker, network LambdaNetwork, ownership docker.Ownership, clk *clock.Clock, log *slog.Logger) (*containerHost, error) {
	if network == nil {
		network = NetworkAuto{}
	}
	h := &containerHost{network: network, ownership: ownership, clock: clk, log: log, built: make(chan struct{})}
	switch d := setting.(type) {
	case DockerDisabled:
		return h, nil
	case DockerSocket:
		h.socket = d.Path
	case nil:
	}
	if h.socket == "" {
		h.socket = docker.DefaultSocket
	}
	client, err := docker.New(docker.Options{Socket: h.socket})
	if err != nil {
		return nil, fmt.Errorf("server: Docker client for %s: %w", h.socket, err)
	}
	h.client = client
	return h, nil
}

// Start starts a function environment's container.
func (h *containerHost) Start(ctx context.Context, spec launcher.Spec, slot launcher.Slot) (launcher.Container, error) {
	run, err := h.lambda(ctx)
	if err != nil {
		return launcher.Container{}, err
	}
	return run.launcher.Start(ctx, spec, slot)
}

// Remove removes a function container; before the runtime is built there
// is none.
func (h *containerHost) Remove(ctx context.Context, id string) error {
	run, ok := h.current()
	if !ok {
		return nil
	}
	return run.launcher.Remove(ctx, id)
}

// Exits relays the launcher's container exits once the runtime is built,
// until ctx ends. The pool watches from Serve on, before any function
// runs.
func (h *containerHost) Exits(ctx context.Context) (<-chan launcher.Exit, error) {
	out := make(chan launcher.Exit)
	go func() {
		defer close(out)
		select {
		case <-h.built:
		case <-ctx.Done():
			return
		}
		run, _ := h.current()
		in, err := run.launcher.Exits(ctx)
		if err != nil {
			h.log.LogAttrs(ctx, slog.LevelWarn, "cannot watch Lambda container exits; a crashed environment is noticed at its next invocation", slog.String("error", err.Error()))
			return
		}
		// After ctx ends the launcher closes in; exits until then are
		// dropped, as the pool drops them.
		for exit := range in {
			select {
			case out <- exit:
			case <-ctx.Done():
			}
		}
	}()
	return out, nil
}

// Open opens a function environment's Runtime API.
func (h *containerHost) Open(ctx context.Context) (pool.Endpoint, error) {
	run, err := h.lambda(ctx)
	if err != nil {
		return nil, err
	}
	env, err := run.api.Open(ctx)
	if err != nil {
		return nil, err
	}
	return env, nil
}

// EnsureImage makes an image present.
func (h *containerHost) EnsureImage(ctx context.Context, image string, arch launcher.Architecture, progress func(docker.Progress)) (launcher.Image, error) {
	run, err := h.lambda(ctx)
	if err != nil {
		return launcher.Image{}, err
	}
	return run.launcher.EnsureImage(ctx, image, arch, progress)
}

// Follow follows a function container's output.
func (h *containerHost) Follow(ctx context.Context, containerID string) (lambda.Output, error) {
	run, err := h.lambda(ctx)
	if err != nil {
		return nil, err
	}
	return run.logs.Follow(ctx, containerID)
}

// serving records the main listener's port and how to open the container
// listener, which the first function environment may need.
func (h *containerHost) serving(port uint16, open func(netip.AddrPort) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.port, h.openListener = port, open
}

// missing says why no container can run: Docker is disabled or its socket
// is not there. Empty when Docker is configured and its socket exists; a
// socket that does not answer surfaces on the first call instead.
func (h *containerHost) missing() string {
	if h.client == nil {
		return "Docker is disabled by configuration, so Lambda functions cannot run and DynamoDB Local cannot start"
	}
	if _, err := os.Stat(h.socket); errors.Is(err, fs.ErrNotExist) {
		return "no Docker socket at " + h.socket + ": mount the Docker socket or point DOCKER_HOST at one to run Lambda functions and DynamoDB Local"
	}
	return ""
}

// topology detects the topology once; a failed detection is retried by
// the next caller.
func (h *containerHost) topology(ctx context.Context) (topology.Topology, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.topologyLocked(ctx)
}

func (h *containerHost) topologyLocked(ctx context.Context) (topology.Topology, error) {
	if h.top != nil {
		return h.top, nil
	}
	if reason := h.missing(); reason != "" {
		return nil, &NoDockerError{Reason: reason}
	}
	if bridge, ok := h.network.(NetworkBridge); ok {
		h.top = topology.LinuxBridge{Gateway: bridge.Gateway}
		h.log.LogAttrs(ctx, slog.LevelInfo, "using the configured container topology", slog.String("topology", h.top.String()))
		return h.top, nil
	}
	top, err := h.detector.Detect(ctx, h.client, topology.Options{})
	if err != nil {
		return nil, err
	}
	h.top = top
	h.log.LogAttrs(ctx, slog.LevelInfo, "detected the container topology", slog.String("topology", top.String()))
	return top, nil
}

// topologyName names how containers reach understudy, for health: the
// detected topology once a container needed it, before that in-container
// when this process runs in a container, and host otherwise.
func (h *containerHost) topologyName() admin.Topology {
	h.mu.Lock()
	top := h.top
	h.mu.Unlock()
	switch t := top.(type) {
	case topology.LinuxBridge:
		return admin.TopologyLinuxBridge
	case topology.DockerDesktop:
		return admin.TopologyDockerDesktop
	case topology.RootlessDocker:
		return admin.TopologyRootlessDocker
	case topology.Podman:
		if t.Rootless {
			return admin.TopologyRootlessPodman
		}
		return admin.TopologyPodman
	case topology.InContainer:
		return admin.TopologyInContainer
	case nil:
	}
	if inContainer() {
		return admin.TopologyInContainer
	}
	return admin.TopologyHost
}

// lambdaOff says why no function container runs although Docker may:
// lambda.network is disabled. Empty otherwise.
func (h *containerHost) lambdaOff() string {
	if _, ok := h.network.(NetworkDisabled); ok {
		return (&LambdaNetworkDisabledError{}).Error()
	}
	return ""
}

// lambda builds the function runtime at first use: the topology, the
// container listener when the topology needs one, the launcher and the
// Runtime API server.
func (h *containerHost) lambda(ctx context.Context) (*lambdaHost, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run != nil {
		return h.run, nil
	}
	if _, ok := h.network.(NetworkDisabled); ok {
		return nil, &LambdaNetworkDisabledError{}
	}
	if h.port == 0 {
		return nil, &NotServingError{}
	}
	top, err := h.topologyLocked(ctx)
	if err != nil {
		return nil, err
	}
	addrs, err := top.Addresses()
	if err != nil {
		return nil, err
	}
	// The listener opens last: one left open by a failed build would make
	// the next call's open fail with the address in use.
	l, err := launcher.New(h.client, launcher.Options{Ownership: h.ownership, Topology: top, EndpointPort: h.port, Logger: h.log})
	if err != nil {
		return nil, err
	}
	api, err := runtimeapi.NewServer(runtimeapi.Options{Clock: h.clock, Topology: top, Logger: h.log})
	if err != nil {
		return nil, err
	}
	if addrs.NeedsContainerListener {
		if err := h.openListener(netip.AddrPortFrom(addrs.ListenerBind, h.port)); err != nil {
			return nil, err
		}
	}
	h.run = &lambdaHost{launcher: l, api: api, logs: lambda.ContainerLogs(l)}
	close(h.built)
	return h.run, nil
}

// current is the function runtime when it is built.
func (h *containerHost) current() (*lambdaHost, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.run, h.run != nil
}

// sweep removes the containers and networks this owner's earlier
// instances left behind. The caller holds the data directory's lock.
func (h *containerHost) sweep(ctx context.Context) {
	if h.missing() != "" {
		return
	}
	swept, err := h.client.Sweep(ctx, h.ownership)
	if err != nil {
		h.log.LogAttrs(ctx, slog.LevelWarn, "sweeping containers an earlier run left behind failed; run understudy cleanup to remove them",
			slog.String("error", err.Error()))
	}
	if n := len(swept.Containers) + len(swept.Networks); n > 0 {
		h.log.LogAttrs(ctx, slog.LevelInfo, "removed what an earlier run left behind",
			slog.Int("containers", len(swept.Containers)), slog.Int("networks", len(swept.Networks)))
	}
}

// close releases the Runtime API listeners and the Docker client's
// connections, after the pool has drained.
func (h *containerHost) close() error {
	var err error
	if run, ok := h.current(); ok {
		err = run.api.Close()
	}
	if h.client != nil {
		h.client.Close()
	}
	if err != nil {
		return fmt.Errorf("closing the Lambda Runtime API listeners: %w", err)
	}
	return nil
}

// dockerStatus is Docker's health: disabled with the reason no container
// can run, or whether it answers a ping.
func (h *containerHost) dockerStatus(ctx context.Context) admin.ServiceStatus {
	if reason := h.missing(); reason != "" {
		return admin.ServiceStatus{Status: admin.Disabled, Reason: reason}
	}
	ping, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if _, err := h.client.Ping(ping); err != nil {
		return admin.ServiceStatus{Status: admin.Degraded, Reason: "Docker at " + h.socket + " does not answer, so Lambda functions cannot run and DynamoDB Local cannot start: " + err.Error()}
	}
	return admin.ServiceStatus{Status: admin.Enabled}
}

// components is the health of Docker and DynamoDB Local; Lambda and
// DynamoDB take Docker's state while it is not enabled, and Lambda is
// disabled while lambda.network is.
type components struct {
	host  *containerHost
	local *lazyLocal
}

var _ serveradmin.Components = components{}

// Components reports Docker, DynamoDB Local and the services that need
// them.
func (c components) Components(ctx context.Context) serveradmin.ComponentStatus {
	out := serveradmin.ComponentStatus{Docker: c.host.dockerStatus(ctx), Topology: c.host.topologyName(), Services: map[ref.ServiceID]admin.ServiceStatus{}}
	if out.Docker.Status != admin.Enabled {
		out.Services["lambda"] = out.Docker
		out.Services["dynamodb"] = out.Docker
		out.DynamoDBLocal = out.Docker
	} else {
		out.DynamoDBLocal = c.local.status()
		if out.DynamoDBLocal.Status != admin.Enabled {
			out.Services["dynamodb"] = out.DynamoDBLocal
		}
	}
	if reason := c.host.lambdaOff(); reason != "" {
		out.Services["lambda"] = admin.ServiceStatus{Status: admin.Disabled, Reason: reason}
	}
	return out
}
