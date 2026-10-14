package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// lazyLocal is the DynamoDB Local the DynamoDB service forwards to. The
// ddblocal.Manager behind it is built at the first start, once the
// topology says how understudy reaches the container (localNetwork).
type lazyLocal struct {
	host *containerHost
	// mode is whether the configuration runs DynamoDB at all.
	mode    DynamoDB
	storage ddblocal.Storage
	// memory is the container's memory limit; 0 is the manager's default.
	memory int64
	log    *slog.Logger

	// mu guards m and closed, and is held while the manager is built.
	mu sync.Mutex
	m  *ddblocal.Manager
	// closed is set by stop: no manager is built and no start begins after
	// it.
	closed bool

	// outcome guards failed apart from mu, so health never waits for a
	// topology detection.
	outcome sync.Mutex
	// failed is the last start's error, until a start succeeds.
	failed error
}

var _ dynamodb.Local = (*lazyLocal)(nil)

// Start starts DynamoDB Local, building its manager first. DynamoDB or
// Docker turned off by configuration is a *dynamodb.DisabledError before
// Docker is touched; a start after shutdown began is errShuttingDown.
func (l *lazyLocal) Start(ctx context.Context) error {
	if reason := l.unavailable(); reason != "" {
		return &dynamodb.DisabledError{Reason: reason}
	}
	m, err := l.manager(ctx)
	if err == nil {
		err = m.Start(ctx)
		if l.isClosed() {
			// stop ran between building the manager and this start, so
			// it may have stopped the manager before the start began; the
			// container this start launched is removed here.
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
			err = errors.Join(errShuttingDown, m.Stop(stopCtx))
			cancel()
		}
	}
	l.record(ctx, err)
	return err
}

// Endpoint is the running container's base URL.
func (l *lazyLocal) Endpoint() (*url.URL, bool) {
	m, ok := l.current()
	if !ok {
		return nil, false
	}
	return m.Endpoint()
}

// Transport reaches the running container.
func (l *lazyLocal) Transport() http.RoundTripper { return localTransport{l} }

// Lost hands a failed request to the manager, which forgets a container
// that is gone so the next Start launches another.
func (l *lazyLocal) Lost(ctx context.Context, endpoint *url.URL) error {
	m, ok := l.current()
	if !ok {
		return nil
	}
	return m.Lost(ctx, endpoint)
}

// record keeps a start's outcome for health. A start ended by its caller
// or by shutdown says nothing about DynamoDB Local.
func (l *lazyLocal) record(ctx context.Context, err error) {
	if err != nil && (ctx.Err() != nil || errors.Is(err, errShuttingDown)) {
		return
	}
	l.outcome.Lock()
	defer l.outcome.Unlock()
	l.failed = err
}

func (l *lazyLocal) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// disabledByConfig is the answer to a DynamoDB call while the
// configuration turns DynamoDB off.
const disabledByConfig = "The DynamoDB service is disabled by the understudy configuration (dynamodb.enabled is false in understudy.json, or in the file --config names); " +
	"remove that setting and restart understudy to use DynamoDB."

// off reports whether the configuration turns DynamoDB off.
func (l *lazyLocal) off() bool {
	_, off := l.mode.(DynamoDBDisabled)
	return off
}

// unavailable is why DynamoDB Local can never run in this process, or
// empty when it may: DynamoDB turned off, or Docker disabled by
// configuration. A Docker socket that is missing now may appear later, so
// it is a failed start instead, which health reports and the next call
// retries.
func (l *lazyLocal) unavailable() string {
	if l.off() {
		return disabledByConfig
	}
	if l.host.client == nil {
		return "DynamoDB runs on DynamoDB Local, which needs Docker: " + l.host.missing() + "."
	}
	return ""
}

func (l *lazyLocal) manager(ctx context.Context) (*ddblocal.Manager, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, errShuttingDown
	}
	if l.m != nil {
		return l.m, nil
	}
	top, err := l.host.topology(ctx)
	if err != nil {
		return nil, err
	}
	m, err := ddblocal.New(ddblocal.Config{
		Docker:  l.host.client,
		Storage: l.storage,
		Network: localNetwork(top),
		Labels:  docker.Labels{Ownership: l.host.ownership},
		Memory:  l.memory,
		Log:     l.log,
	})
	if err != nil {
		return nil, err
	}
	// The manager is built once, at the first start: say what DynamoDB
	// runs on, and under whose terms, before anything is pulled.
	const notice = ddblocal.Notice
	l.log.LogAttrs(ctx, slog.LevelInfo, notice, slog.String("image", ddblocal.Reference()))
	l.m = m
	return m, nil
}

// localNetwork is how understudy reaches DynamoDB Local on top: at its
// default-bridge address where the host routes to container addresses, on
// understudy's own network in a container, on a port published on a remote
// daemon's own address, and otherwise on a port published on loopback (ADR
// 0014). Docker Desktop keeps containers in a VM, rootless engines keep
// them in a network namespace, and a remote daemon keeps them on another
// host, so none can be reached at a container address.
func localNetwork(top topology.Topology) ddblocal.Network {
	switch t := top.(type) {
	case topology.LinuxBridge:
		return ddblocal.DefaultBridge{}
	case topology.Podman:
		if t.Rootless {
			return ddblocal.Published{}
		}
		return ddblocal.DefaultBridge{}
	case topology.InContainer:
		return ddblocal.Attached{Network: t.Network}
	case topology.Remote:
		return ddblocal.Published{HostIP: t.Daemon}
	case topology.DockerDesktop, topology.RootlessDocker:
		return ddblocal.Published{}
	}
	return ddblocal.Published{}
}

func (l *lazyLocal) current() (*ddblocal.Manager, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m, l.m != nil
}

// startInBackground starts DynamoDB Local at serve when it can run here
// and the pinned image is already present (ARCHITECTURE.md 15); otherwise
// the first DynamoDB call starts it.
func (l *lazyLocal) startInBackground(ctx context.Context) {
	if l.unavailable() != "" || l.host.missing() != "" {
		return
	}
	if _, err := l.host.client.ImageInspect(ctx, ddblocal.Reference()); err != nil {
		return
	}
	if err := l.Start(ctx); err != nil && ctx.Err() == nil {
		l.log.LogAttrs(ctx, slog.LevelWarn, "DynamoDB Local did not start in the background; the first DynamoDB call tries again", slog.String("error", err.Error()))
	}
}

// stop removes the container at shutdown; no start begins after it.
func (l *lazyLocal) stop(ctx context.Context) error {
	l.mu.Lock()
	l.closed = true
	m := l.m
	l.mu.Unlock()
	if m == nil {
		return nil
	}
	return m.Stop(ctx)
}

// status is DynamoDB Local's health while Docker is up: degraded with the
// last start's error until a start succeeds.
func (l *lazyLocal) status() admin.ServiceStatus {
	if l.off() {
		return admin.ServiceStatus{Status: admin.Disabled, Reason: "disabled by configuration"}
	}
	l.outcome.Lock()
	failed := l.failed
	l.outcome.Unlock()
	if failed != nil {
		return admin.ServiceStatus{Status: admin.Degraded, Reason: "DynamoDB Local did not start, and the next DynamoDB call tries again: " + failed.Error()}
	}
	return admin.ServiceStatus{Status: admin.Enabled}
}

// localTransport sends through the manager's transport, which exists once
// a start has built the manager; the service forwards only after a start.
type localTransport struct{ l *lazyLocal }

// errLocalNotStarted is a forward before any start built the manager.
var errLocalNotStarted = errors.New("DynamoDB Local has not been started")

func (t localTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	m, ok := t.l.current()
	if !ok {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, errLocalNotStarted
	}
	return m.Transport().RoundTrip(r)
}
