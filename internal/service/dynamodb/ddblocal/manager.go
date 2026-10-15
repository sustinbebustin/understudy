package ddblocal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// Storage is where DynamoDB Local keeps item data.
//
//sumtype:decl
type Storage interface{ isStorage() }

// InMemory keeps data in the container's memory (-inMemory); it is lost when
// the container stops.
type InMemory struct{}

// DBPath keeps data in SQLite files under Dir (-dbPath), a host directory
// the daemon can bind-mount and the image's user (uid 1000) can write.
type DBPath struct{ Dir string }

func (InMemory) isStorage() {}
func (DBPath) isStorage()   {}

// Network is where the container listens and how understudy reaches it.
//
//sumtype:decl
type Network interface{ isNetwork() }

// DefaultBridge joins the daemon's default bridge; understudy reaches the
// container at its bridge address, which a Linux host routes to.
type DefaultBridge struct{}

// Published joins the daemon's default bridge and publishes the container's
// port on HostIP with an ephemeral host port, which understudy reaches
// there (ADR 0014). It serves topologies whose host cannot route to
// container addresses: Docker Desktop and rootless engines on loopback,
// the zero HostIP, and a remote daemon on its own address.
type Published struct{ HostIP netip.Addr }

// hostIP is HostIP, or 127.0.0.1 when it is zero.
func (p Published) hostIP() netip.Addr {
	if !p.HostIP.IsValid() {
		return netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	return p.HostIP
}

// Attached joins understudy's own network when understudy runs in a
// container (topology InContainer); understudy reaches it by container IP.
type Attached struct{ Network string }

func (DefaultBridge) isNetwork() {}
func (Published) isNetwork()     {}
func (Attached) isNetwork()      {}

// Defaults for Config.
const (
	// DefaultMemory bounds the container: DynamoDB Local idles at about
	// 150 MiB (dynamodb-local.md 1.3) and the JVM sizes its heap to a
	// quarter of the limit.
	DefaultMemory int64 = 1 << 30
	// DefaultReadyTimeout bounds the wait for the first HTTP answer after
	// the container starts; a cold start takes about 1.2 to 2.3 s
	// (dynamodb-local.md 1.3, dynamodb-local-terms.md 2.4).
	DefaultReadyTimeout = 60 * time.Second
)

// port is the port DynamoDB Local listens on inside the container.
const port = 8000

// portKey is port as the Engine API keys exposed and published ports.
const portKey = "8000/tcp"

// dataPath is where DBPath's directory is mounted inside the container.
const dataPath = "/data"

// Config configures a Manager.
type Config struct {
	// Docker is the daemon; required.
	Docker *docker.Client
	// Storage is nil for InMemory.
	Storage Storage
	// Network is nil for DefaultBridge.
	Network Network
	// Labels mark the container; Ownership is required and Function nil.
	Labels docker.Labels
	// Memory is the container's memory limit in bytes; 0 means
	// DefaultMemory.
	Memory int64
	// ReadyTimeout is 0 for DefaultReadyTimeout.
	ReadyTimeout time.Duration
	// Log receives lifecycle diagnostics; nil discards them.
	Log *slog.Logger
}

// Manager runs the one DynamoDB Local container of a serve process: Start
// pulls the pinned image when absent, starts it and waits until it answers
// HTTP; Stop removes it. Start is idempotent and shared: concurrent callers
// wait for the same start.
type Manager struct {
	cfg       Config
	log       *slog.Logger
	transport *http.Transport

	mu    sync.Mutex
	state state
	// launched closes when the latest launch has finished and removed any
	// container a Stop left it holding, or when a Stop of the running
	// container has removed it. The next launch waits for it, so two
	// launches never claim the container name at once.
	launched <-chan struct{}
}

// state is where the container is in its life.
//
//sumtype:decl
type state interface{ isState() }

type stopped struct{}

// starting is a start in progress; err is set before done closes.
type starting struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

type running struct {
	id       string
	endpoint *url.URL
}

func (stopped) isState()   {}
func (*starting) isState() {}
func (running) isState()   {}

// ConfigError is a Config New cannot use.
type ConfigError struct{ Reason string }

func (e *ConfigError) Error() string { return "ddblocal: " + e.Reason }

// New returns a stopped Manager.
func New(cfg Config) (*Manager, error) {
	switch {
	case cfg.Docker == nil:
		return nil, &ConfigError{Reason: "Config.Docker is required: DynamoDB Local runs as a container"}
	case cfg.Labels.Function != nil:
		return nil, &ConfigError{Reason: "Config.Labels.Function must be nil: the DynamoDB Local container serves no function"}
	}
	if err := cfg.Labels.Ownership.Validate(); err != nil {
		return nil, err
	}
	if cfg.Storage == nil {
		cfg.Storage = InMemory{}
	}
	if cfg.Network == nil {
		cfg.Network = DefaultBridge{}
	}
	if cfg.Memory == 0 {
		cfg.Memory = DefaultMemory
	}
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = DefaultReadyTimeout
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	none := make(chan struct{})
	close(none)
	return &Manager{
		cfg: cfg,
		log: log,
		// No Proxy: traffic to the container never goes through an
		// HTTP_PROXY the user set for the outside world.
		transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		},
		state:    stopped{},
		launched: none,
	}, nil
}

// Transport reaches the running container. Stop closes its idle
// connections.
func (m *Manager) Transport() http.RoundTripper { return m.transport }

// Endpoint returns the base URL of the running container.
func (m *Manager) Endpoint() (*url.URL, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.state.(running); ok {
		u := *r.endpoint
		return &u, true
	}
	return nil, false
}

// Start makes DynamoDB Local ready: it returns at once when it is running,
// waits for a start in progress, and otherwise starts it. ctx bounds the
// caller's wait only; the start goes on for the other callers, and Stop
// ends it.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	var st *starting
	switch s := m.state.(type) {
	case running:
		m.mu.Unlock()
		return nil
	case *starting:
		st = s
	case stopped:
		launch, cancel := context.WithCancel(context.WithoutCancel(ctx))
		st = &starting{done: make(chan struct{}), cancel: cancel}
		prev := m.launched
		m.state, m.launched = st, st.done
		go m.launch(launch, st, prev)
	}
	m.mu.Unlock()
	select {
	case <-st.done:
		return st.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop removes the container and closes idle connections to it. A start in
// progress is cancelled and its container removed. Stopping a stopped
// Manager does nothing.
func (m *Manager) Stop(ctx context.Context) error {
	removed := make(chan struct{})
	defer close(removed)
	m.mu.Lock()
	prev := m.state
	m.state = stopped{}
	if _, ok := prev.(running); ok {
		// The next launch waits for this removal, as it waits for a
		// stopped start's, so it never creates while the name is held.
		m.launched = removed
	}
	m.mu.Unlock()
	defer m.transport.CloseIdleConnections()
	switch s := prev.(type) {
	case *starting:
		s.cancel()
		select {
		case <-s.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	case running:
		return m.remove(ctx, s.id)
	case stopped:
	}
	return nil
}

// Lost reports that a request to endpoint failed to reach DynamoDB Local.
// When the container running there is gone or no longer running (killed
// for memory, removed by hand, Docker restarted), the Manager forgets it,
// so the next Start launches a new one; run removes what is left under the
// container name first. A container that still runs, or an endpoint the
// Manager has moved on from, changes nothing. An error means Docker could
// not say, and the state is kept.
func (m *Manager) Lost(ctx context.Context, endpoint *url.URL) error {
	m.mu.Lock()
	r, ok := m.state.(running)
	m.mu.Unlock()
	if !ok || r.endpoint.String() != endpoint.String() {
		return nil
	}
	c, err := m.cfg.Docker.ContainerInspect(ctx, r.id)
	switch {
	case err != nil && !docker.IsNotFound(err):
		return fmt.Errorf("ddblocal: inspecting container %s after a request to it failed: %w", r.id, err)
	case err == nil && c.State.Running:
		return nil
	}
	m.mu.Lock()
	// Only the container inspected is forgotten: a Stop or a new start
	// since then owns the state.
	if now, ok := m.state.(running); ok && now.id == r.id {
		m.state = stopped{}
	}
	m.mu.Unlock()
	m.transport.CloseIdleConnections()
	m.log.LogAttrs(ctx, slog.LevelWarn, "DynamoDB Local's container is gone or stopped; the next DynamoDB call starts a new one",
		slog.String("container", r.id))
	return nil
}

// launch runs one start once the previous launch has finished, and
// publishes its outcome. A Stop during the start has already moved the
// state on: its waiters get errStopped, and a container that came up is
// removed again. A start stopped before it ran still publishes only after
// the previous launch or removal ends, since the next launch waits for
// this one alone: two launches, or a launch and a removal, then never
// claim the container name at once.
func (m *Manager) launch(ctx context.Context, st *starting, prev <-chan struct{}) {
	defer st.cancel()
	var r running
	var err error
	select {
	case <-prev:
		r, err = m.run(ctx)
	case <-ctx.Done():
		<-prev
		err = ctx.Err()
	}
	m.mu.Lock()
	current := m.state == state(st)
	switch {
	case current && err != nil:
		m.state = stopped{}
	case current:
		m.state = r
	}
	m.mu.Unlock()
	if !current {
		ended := err
		err = errStopped
		if ended == nil {
			err = errors.Join(errStopped, m.remove(ctx, r.id))
		}
	}
	st.err = err
	close(st.done)
}

// errStopped is a start that Stop ended.
var errStopped = errors.New("ddblocal: DynamoDB Local was stopped while it started; the next DynamoDB call starts it again")

// run pulls, creates and starts the container and waits until it answers.
// A container an earlier removal failed to remove still holds the name, so
// it is removed first. On failure the container, if created, is removed,
// and a removal that fails is joined to the error.
func (m *Manager) run(ctx context.Context) (running, error) {
	ref, err := ensureImage(ctx, m.cfg.Docker)
	if err != nil {
		return running{}, err
	}
	cfg, host, netCfg, err := m.spec(ref)
	if err != nil {
		return running{}, err
	}
	if err := m.remove(ctx, m.name()); err != nil {
		return running{}, &StartError{Step: "remove the leftover container " + m.name(), Err: err}
	}
	created, err := m.cfg.Docker.ContainerCreate(ctx, m.name(), cfg, host, netCfg)
	switch {
	case err != nil && ctx.Err() != nil:
		// The daemon may have created it before the cancel reached it.
		return running{}, errors.Join(ctx.Err(), m.remove(ctx, m.name()))
	case err != nil:
		return running{}, &StartError{Step: "create the container", Err: err}
	}
	r, err := m.boot(ctx, created.ID)
	if err != nil {
		return running{}, errors.Join(err, m.remove(ctx, created.ID))
	}
	m.log.LogAttrs(ctx, slog.LevelInfo, "DynamoDB Local is ready",
		slog.String("container", created.ID), slog.String("endpoint", r.endpoint.String()), slog.String("image", ref))
	return r, nil
}

// boot starts the created container, finds its address and waits for it.
func (m *Manager) boot(ctx context.Context, id string) (running, error) {
	if err := m.cfg.Docker.ContainerStart(ctx, id); err != nil {
		return running{}, &StartError{Step: "start the container", Err: err}
	}
	addr, err := m.address(ctx, id)
	if err != nil {
		return running{}, err
	}
	endpoint := &url.URL{Scheme: "http", Host: addr.String()}
	if err := m.waitReady(ctx, id, endpoint); err != nil {
		return running{}, err
	}
	return running{id: id, endpoint: endpoint}, nil
}

// networkName is the network the container joins.
func (m *Manager) networkName() string {
	if a, ok := m.cfg.Network.(Attached); ok {
		return a.Network
	}
	return "bridge"
}

// address reads where understudy reaches the started container: the host
// port published for Published, else the container's IPv4 address on its
// network at port.
func (m *Manager) address(ctx context.Context, id string) (netip.AddrPort, error) {
	c, err := m.cfg.Docker.ContainerInspect(ctx, id)
	if err != nil {
		return netip.AddrPort{}, &StartError{Step: "inspect the container", Err: err}
	}
	if _, ok := m.cfg.Network.(Published); ok {
		hostPort, ok := c.NetworkSettings.HostPort(portKey)
		if !ok {
			return netip.AddrPort{}, &StartError{Step: "find the container's published port", Err: fmt.Errorf("the daemon reports no IPv4 host binding for %s (Ports = %v)", portKey, c.NetworkSettings.Ports)}
		}
		return hostPort, nil
	}
	ep, ok := c.NetworkSettings.Networks[m.networkName()]
	if !ok {
		return netip.AddrPort{}, &StartError{Step: "find the container's address", Err: fmt.Errorf("it is not attached to network %q", m.networkName())}
	}
	addr, err := netip.ParseAddr(ep.IPAddress)
	if err != nil || !addr.Is4() {
		return netip.AddrPort{}, &StartError{Step: "find the container's address", Err: fmt.Errorf("network %q reports address %q, not an IPv4 address", m.networkName(), ep.IPAddress)}
	}
	return netip.AddrPortFrom(addr, port), nil
}

// probeInterval is the wait between readiness probes.
const probeInterval = 100 * time.Millisecond

// waitReady polls GET / until DynamoDB Local answers anything: without
// credentials it answers 400 MissingAuthenticationToken, which means it is
// up (dynamodb-local.md 1.3). A container that exits fails at once; a
// cancelled ctx ends the wait with its error.
func (m *Manager) waitReady(ctx context.Context, id string, endpoint *url.URL) error {
	bounded, cancel := context.WithTimeout(ctx, m.cfg.ReadyTimeout)
	defer cancel()
	client := &http.Client{Transport: m.transport, Timeout: 2 * time.Second}
	for {
		c, err := m.cfg.Docker.ContainerInspect(bounded, id)
		switch {
		case err == nil && !c.State.Running:
			return &ExitedError{Container: id, Code: c.State.ExitCode, OOMKilled: c.State.OOMKilled, Memory: m.cfg.Memory, Output: m.exitOutput(ctx, id)}
		case m.answers(bounded, client, endpoint):
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		case bounded.Err() != nil:
			return &NotReadyError{Container: id, Endpoint: endpoint.String(), Timeout: m.cfg.ReadyTimeout}
		}
		wait, stop := context.WithTimeout(bounded, probeInterval)
		<-wait.Done()
		stop()
	}
}

// exitOutputBytes bounds the output an ExitedError carries: the JVM's last
// lines say why it exited.
const exitOutputBytes = 4 << 10

// exitOutput is the end of an exited container's output, read before the
// container and its log are removed. A log that cannot be read gives "",
// and the failure is logged: the exit is the error to report.
func (m *Manager) exitOutput(ctx context.Context, id string) string {
	s, err := m.cfg.Docker.ContainerLogs(ctx, id, docker.LogsOptions{})
	if err != nil {
		m.log.LogAttrs(ctx, slog.LevelWarn, "cannot read the exited DynamoDB Local container's log", slog.String("container", id), slog.Any("error", err))
		return ""
	}
	defer s.Close()
	var out []byte
	for {
		f, err := s.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			m.log.LogAttrs(ctx, slog.LevelWarn, "cannot read the exited DynamoDB Local container's log", slog.String("container", id), slog.Any("error", err))
			break
		}
		out = append(out, f.Payload...)
		if len(out) > 2*exitOutputBytes {
			out = out[len(out)-exitOutputBytes:]
		}
	}
	if len(out) > exitOutputBytes {
		out = out[len(out)-exitOutputBytes:]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(out), "?"))
}

func (m *Manager) answers(ctx context.Context, client *http.Client, endpoint *url.URL) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String()+"/", http.NoBody)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	return resp.Body.Close() == nil
}

// removeTimeout bounds a removal that outlives a cancelled start.
const removeTimeout = 30 * time.Second

// remove force-removes a container with its anonymous volumes; one already
// gone is not an error.
func (m *Manager) remove(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()
	err := m.cfg.Docker.ContainerRemove(ctx, id, docker.RemoveOptions{Force: true, Volumes: true})
	if err != nil && !docker.IsNotFound(err) {
		return fmt.Errorf("ddblocal: removing container %s (it carries understudy's labels, so the next sweep removes it): %w", id, err)
	}
	return nil
}

// name is the container's name, unique per serve process.
func (m *Manager) name() string {
	return "understudy-dynamodb-local-" + m.cfg.Labels.Ownership.Instance
}

// spec is the container understudy runs: the pinned image with its java
// entrypoint, both telemetry opt-outs (only the literal 0 disables it
// through the environment; dynamodb-local-terms.md 2), the storage flag,
// understudy's labels, a memory bound without swap, and no capabilities.
func (m *Manager) spec(ref string) (docker.Config, docker.HostConfig, docker.NetworkingConfig, error) {
	labels, err := m.cfg.Labels.Map()
	if err != nil {
		return docker.Config{}, docker.HostConfig{}, docker.NetworkingConfig{}, err
	}
	args := []string{"-jar", "DynamoDBLocal.jar", "-port", strconv.Itoa(port), "-disableTelemetry"}
	host := docker.HostConfig{
		NetworkMode: m.networkName(),
		Memory:      m.cfg.Memory,
		MemorySwap:  m.cfg.Memory,
		PidsLimit:   2048,
		LogConfig:   docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "2"}},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
	}
	switch s := m.cfg.Storage.(type) {
	case DBPath:
		args = append(args, "-dbPath", dataPath)
		host.Mounts = []docker.Mount{{Type: "bind", Source: s.Dir, Target: dataPath}}
	case InMemory:
		args = append(args, "-inMemory")
	}
	cfg := docker.Config{
		Image:  ref,
		Cmd:    args,
		Env:    []string{"DDB_LOCAL_TELEMETRY=0"},
		Labels: labels,
	}
	var netCfg docker.NetworkingConfig
	switch n := m.cfg.Network.(type) {
	case Attached:
		netCfg.EndpointsConfig = map[string]docker.EndpointSettings{n.Network: {}}
	case Published:
		// One address only, loopback unless the daemon is remote, and an
		// ephemeral port so two serve processes never collide (ADR 0014).
		cfg.ExposedPorts = docker.PortSet{portKey: {}}
		host.PortBindings = docker.PortMap{portKey: {{HostIP: n.hostIP().String(), HostPort: "0"}}}
	case DefaultBridge:
	}
	return cfg, host, netCfg, nil
}

// StartError is a Docker call that failed while starting the container.
type StartError struct {
	Step string
	Err  error
}

func (e *StartError) Error() string {
	return "ddblocal: could not " + e.Step + " for DynamoDB Local: " + e.Err.Error() + "; run `understudy doctor` to check Docker"
}

func (e *StartError) Unwrap() error { return e.Err }

// ExitedError is a container that exited before it answered.
type ExitedError struct {
	Container string
	Code      int
	OOMKilled bool
	// Memory is the container's memory limit in bytes.
	Memory int64
	// Output is the end of what the container wrote, "" when its log could
	// not be read.
	Output string
}

func (e *ExitedError) Error() string {
	var b strings.Builder
	b.WriteString("ddblocal: DynamoDB Local container " + e.Container)
	if e.OOMKilled {
		fmt.Fprintf(&b, " was killed for exceeding its memory limit of %d MiB before it answered HTTP; raise the DynamoDB memory limit in the understudy configuration.", e.Memory>>20)
	} else {
		fmt.Fprintf(&b, " exited with code %d before it answered HTTP.", e.Code)
	}
	b.WriteString(" Retrying is safe: the next DynamoDB call starts it again.")
	if e.Output != "" {
		b.WriteString(" Its last output:\n" + e.Output)
	}
	return b.String()
}

// NotReadyError is a container that did not answer HTTP in time.
type NotReadyError struct {
	Container string
	Endpoint  string
	Timeout   time.Duration
}

func (e *NotReadyError) Error() string {
	return fmt.Sprintf("ddblocal: DynamoDB Local container %s did not answer at %s within %s and was removed; the next DynamoDB call starts it again. "+
		"Run `understudy doctor` to check Docker and the detected topology",
		e.Container, e.Endpoint, e.Timeout)
}
