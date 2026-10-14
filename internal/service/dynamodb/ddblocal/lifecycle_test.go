package ddblocal_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// fakeLocal stands in for the JVM inside the fake daemon's container: it
// refuses connections until ready is set, as a starting JVM does, then
// answers every request 400, as DynamoDB Local answers an unsigned one.
type fakeLocal struct {
	ready atomic.Bool
	// dialed closes on the first connection attempt. The readiness probe
	// dials only after the container started and was inspected, so it
	// means the container is running.
	dialed chan struct{}
	once   sync.Once
}

// answering makes m reach a fakeLocal, ready or not, through an
// in-process server, whatever address the fake daemon hands out.
func answering(t *testing.T, m *ddblocal.Manager, ready bool) *fakeLocal {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	f := &fakeLocal{dialed: make(chan struct{})}
	f.ready.Store(ready)
	ddblocal.SetDial(m, func(ctx context.Context) (net.Conn, error) {
		f.once.Do(func() { close(f.dialed) })
		if !f.ready.Load() {
			return nil, errors.New("connection refused")
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	})
	return f
}

// running waits until the readiness probe first dials, so the container
// is running.
func (f *fakeLocal) running(t *testing.T) {
	t.Helper()
	select {
	case <-f.dialed:
	case <-t.Context().Done():
		t.Fatal("the test ended before the readiness probe dialed the container")
	}
}

// presentDaemon is a fake daemon holding the pinned image, whose
// containers run until stopped.
func presentDaemon(t *testing.T) *dockertest.Daemon {
	t.Helper()
	d := daemon(t)
	d.AddImage(hubRef, docker.Image{ID: ddblocal.Digest})
	return d
}

// runningContainers lists the IDs of the daemon's running containers, in
// creation order.
func runningContainers(d *dockertest.Daemon) []string {
	var ids []string
	created := d.Created()
	for i := range created {
		if live, ok := d.Container(created[i].ID); ok && live.State.Running {
			ids = append(ids, created[i].ID)
		}
	}
	return ids
}

// TestStartReachesRunning: a container that answers HTTP is running, its
// endpoint is its bridge address on port 8000, a second Start reuses it,
// and Stop removes it.
func TestStartReachesRunning(t *testing.T) {
	d := presentDaemon(t)
	m := manager(t, d, ddblocal.Config{})
	answering(t, m, true)
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start = %v", err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("a second Start = %v", err)
	}
	created := d.Created()
	if len(created) != 1 {
		t.Fatalf("created %d containers, want 1", len(created))
	}
	live, ok := d.Container(created[0].ID)
	if !ok || !live.State.Running {
		t.Fatal("the container is not running")
	}
	endpoint, ok := m.Endpoint()
	if want := "http://" + live.NetworkSettings.Networks["bridge"].IPAddress + ":8000"; !ok || endpoint.String() != want {
		t.Errorf("Endpoint = %v, %t; want %s", endpoint, ok, want)
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if _, ok := d.Container(created[0].ID); ok {
		t.Error("Stop left the container")
	}
	if _, ok := m.Endpoint(); ok {
		t.Error("Endpoint after Stop reports a running container")
	}
}

// TestPublishedIsReachedOnLoopback: on a topology whose host cannot route
// to container addresses, port 8000 is published on 127.0.0.1 with an
// ephemeral host port, and the endpoint is that loopback port, not the
// bridge address (ADR 0014).
func TestPublishedIsReachedOnLoopback(t *testing.T) {
	d := presentDaemon(t)
	m := manager(t, d, ddblocal.Config{Network: ddblocal.Published{}})
	answering(t, m, true)
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start = %v", err)
	}
	created := d.Created()
	if len(created) != 1 {
		t.Fatalf("created %d containers, want 1", len(created))
	}
	c := created[0]
	if _, ok := c.Config.ExposedPorts["8000/tcp"]; !ok {
		t.Errorf("ExposedPorts = %v, want 8000/tcp", c.Config.ExposedPorts)
	}
	want := []docker.PortBinding{{HostIP: "127.0.0.1", HostPort: "0"}}
	if got := c.HostConfig.PortBindings["8000/tcp"]; !slices.Equal(got, want) {
		t.Errorf("PortBindings[8000/tcp] = %v, want %v", got, want)
	}
	if c.HostConfig.NetworkMode != "bridge" {
		t.Errorf("NetworkMode = %q, want the default bridge", c.HostConfig.NetworkMode)
	}
	live, ok := d.Container(c.ID)
	if !ok {
		t.Fatal("the container is gone")
	}
	hostPort, ok := live.NetworkSettings.HostPort("8000/tcp")
	if !ok {
		t.Fatalf("the daemon reports no host port: %v", live.NetworkSettings.Ports)
	}
	endpoint, ok := m.Endpoint()
	if want := "http://" + hostPort.String(); !ok || endpoint.String() != want {
		t.Errorf("Endpoint = %v, %t; want %s", endpoint, ok, want)
	}
}

// TestLostContainerIsReplaced: a container that exited (killed for memory,
// Docker restarted) is forgotten when a request to it fails, and the next
// Start launches a new one; a running container reported lost is kept.
func TestLostContainerIsReplaced(t *testing.T) {
	d := presentDaemon(t)
	m := manager(t, d, ddblocal.Config{})
	answering(t, m, true)
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start = %v", err)
	}
	endpoint, _ := m.Endpoint()
	if err := m.Lost(t.Context(), endpoint); err != nil {
		t.Fatalf("Lost of a running container = %v", err)
	}
	if _, ok := m.Endpoint(); !ok {
		t.Fatal("Lost forgot a container that still runs")
	}
	first := runningContainers(d)
	if len(first) != 1 || !d.Exit(first[0], 137) {
		t.Fatalf("running containers = %v, want one to kill", first)
	}
	if err := m.Lost(t.Context(), endpoint); err != nil {
		t.Fatalf("Lost of an exited container = %v", err)
	}
	if _, ok := m.Endpoint(); ok {
		t.Fatal("Lost kept the endpoint of an exited container")
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start after the container exited = %v", err)
	}
	if again := runningContainers(d); len(again) != 1 || again[0] == first[0] {
		t.Errorf("running containers after the restart = %v, want one new container", again)
	}
}

// TestConcurrentStartsShareOneStart: callers arriving together all wait
// for the same start, so exactly one container is created.
func TestConcurrentStartsShareOneStart(t *testing.T) {
	d := presentDaemon(t)
	m := manager(t, d, ddblocal.Config{})
	answering(t, m, true)
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { errs[i] = m.Start(t.Context()) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Start %d = %v", i, err)
		}
	}
	if n := len(d.Created()); n != 1 {
		t.Errorf("created %d containers, want 1", n)
	}
}

// TestStopWhileStartingRemovesTheContainer: Stop ends a start in progress,
// its waiters learn the start was stopped, and the container is gone when
// Stop returns.
func TestStopWhileStartingRemovesTheContainer(t *testing.T) {
	d := presentDaemon(t)
	m := manager(t, d, ddblocal.Config{})
	local := answering(t, m, false)
	errc := make(chan error, 1)
	go func() { errc <- m.Start(t.Context()) }()
	local.running(t)
	if err := m.Stop(t.Context()); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if err := <-errc; !errors.Is(err, ddblocal.ErrStopped) {
		t.Errorf("Start = %v, want the stopped start's error", err)
	}
	if _, ok := d.Container(d.Created()[0].ID); ok {
		t.Error("Stop returned with the starting container still present")
	}
	if _, ok := m.Endpoint(); ok {
		t.Error("Endpoint after Stop reports a running container")
	}
}

// TestStartRetriesAfterAFailedStart: a failed start leaves the Manager
// stopped, so the next Start tries again.
func TestStartRetriesAfterAFailedStart(t *testing.T) {
	d := presentDaemon(t)
	d.FailNext("POST /containers/create", http.StatusInternalServerError, "no space left on device")
	m := manager(t, d, ddblocal.Config{})
	answering(t, m, true)
	err := m.Start(t.Context())
	if se, ok := errors.AsType[*ddblocal.StartError](err); !ok || !strings.Contains(se.Error(), "no space left on device") {
		t.Fatalf("first Start = %v, want the create failure", err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("second Start = %v, want a retry that succeeds", err)
	}
	if _, ok := m.Endpoint(); !ok {
		t.Error("Endpoint after the retry reports no running container")
	}
}

// TestFailedRemovalDoesNotBlockTheNextStart: a Stop whose removal fails
// reports it, and the container it left holding the name is removed by
// the next Start, which then succeeds.
func TestFailedRemovalDoesNotBlockTheNextStart(t *testing.T) {
	d := presentDaemon(t)
	m := manager(t, d, ddblocal.Config{})
	answering(t, m, true)
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start = %v", err)
	}
	d.FailNext("DELETE /containers/{id}", http.StatusInternalServerError, "driver failed to remove root filesystem")
	if err := m.Stop(t.Context()); err == nil || !strings.Contains(err.Error(), "driver failed to remove root filesystem") {
		t.Fatalf("Stop = %v, want the removal failure", err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start after the failed removal = %v", err)
	}
	created := d.Created()
	if len(created) != 2 {
		t.Fatalf("created %d containers, want 2", len(created))
	}
	if _, ok := d.Container(created[0].ID); ok {
		t.Error("the container the failed removal left is still present")
	}
	if running := runningContainers(d); len(running) != 1 || running[0] != created[1].ID {
		t.Errorf("running containers = %v, want only the new start's %s", running, created[1].ID)
	}
}

// TestLeftoverThatCannotBeRemovedFailsTheStart: a start that cannot clear
// the container name creates nothing and says why.
func TestLeftoverThatCannotBeRemovedFailsTheStart(t *testing.T) {
	d := presentDaemon(t)
	d.FailNext("DELETE /containers/{id}", http.StatusInternalServerError, "driver failed to remove root filesystem")
	m := manager(t, d, ddblocal.Config{})
	answering(t, m, true)
	err := m.Start(t.Context())
	if se, ok := errors.AsType[*ddblocal.StartError](err); !ok || !strings.Contains(se.Error(), "remove the leftover container understudy-dynamodb-local-inst1") {
		t.Fatalf("Start = %v, want the leftover removal's *StartError", err)
	}
	if n := len(d.Created()); n != 0 {
		t.Errorf("created %d containers, want none", n)
	}
}

// TestCallerGivingUpLeavesTheStartRunning: a caller whose context ends
// gets its error at once; the start goes on and a later caller gets it.
func TestCallerGivingUpLeavesTheStartRunning(t *testing.T) {
	d := presentDaemon(t)
	m := manager(t, d, ddblocal.Config{})
	local := answering(t, m, false)
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { errc <- m.Start(ctx) }()
	local.running(t)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start with a cancelled context = %v, want context.Canceled", err)
	}
	local.ready.Store(true)
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("a later Start = %v", err)
	}
	if n := len(d.Created()); n != 1 {
		t.Errorf("created %d containers, want the first start's only", n)
	}
}

// TestStartAfterAnAbandonedStop: a Stop that gave up waiting leaves the old
// start removing its container; a Start right after it waits for that
// removal instead of colliding with the container name.
func TestStartAfterAnAbandonedStop(t *testing.T) {
	d := presentDaemon(t)
	m := manager(t, d, ddblocal.Config{})
	local := answering(t, m, false)
	errc := make(chan error, 1)
	go func() { errc <- m.Start(t.Context()) }()
	local.running(t)
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.Stop(gone); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop = %v", err)
	}
	local.ready.Store(true)
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start after the abandoned Stop = %v", err)
	}
	if err := <-errc; !errors.Is(err, ddblocal.ErrStopped) {
		t.Errorf("the stopped start = %v, want the stopped start's error", err)
	}
	created := d.Created()
	if len(created) != 2 {
		t.Fatalf("created %d containers, want 2", len(created))
	}
	if _, ok := d.Container(created[0].ID); ok {
		t.Error("the stopped start's container is still present")
	}
	if running := runningContainers(d); len(running) != 1 || running[0] != created[1].ID {
		t.Errorf("running containers = %v, want only the new start's %s", running, created[1].ID)
	}
}

// TestNoStartWhileARemovalIsInFlight: Stop removes the running container
// A slowly; a Start (B) stopped before it ran, then another Start (C),
// makes no Docker request until A's removal returns. Otherwise C's
// removal of the leftover name would meet A's removal in progress, which
// a real daemon refuses with 409.
func TestNoStartWhileARemovalIsInFlight(t *testing.T) {
	d := presentDaemon(t)
	gate := &deleteGate{held: make(chan struct{}), release: make(chan struct{})}
	m := manager(t, d, ddblocal.Config{Docker: gate.client(t, d)})
	answering(t, m, true)
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start A = %v", err)
	}
	gate.hold(d.Created()[0].ID)
	stopA := make(chan error, 1)
	go func() { stopA <- m.Stop(t.Context()) }()
	<-gate.held

	startB := make(chan error, 1)
	go func() { startB <- m.Start(t.Context()) }()
	abandoned, cancel := context.WithCancel(t.Context())
	cancel()
	// Stop B once its start is in progress, giving up the wait at once;
	// until B starts, Stop finds nothing to stop.
	for m.Stop(abandoned) == nil {
		select {
		case err := <-startB:
			t.Fatalf("Start B = %v before it was stopped", err)
		default:
		}
	}
	startC := make(chan error, 1)
	go func() { startC <- m.Start(t.Context()) }()

	select {
	case r := <-gate.early:
		t.Errorf("Start C sent %s while A's removal was in flight", r)
	case <-time.After(200 * time.Millisecond):
	}
	close(gate.release)
	if err := <-stopA; err != nil {
		t.Errorf("Stop A = %v", err)
	}
	if err := <-startB; !errors.Is(err, ddblocal.ErrStopped) {
		t.Errorf("Start B = %v, want the stopped start's error", err)
	}
	if err := <-startC; err != nil {
		t.Errorf("Start C = %v", err)
	}
}

// deleteGate is a proxy in front of a fake daemon that holds the removal of
// one container until release closes, and reports any other request made
// while it holds it.
type deleteGate struct {
	mu      sync.Mutex
	id      string
	holding bool
	held    chan struct{}
	release chan struct{}
	early   chan string
}

// hold makes the gate hold the next removal of the container id.
func (g *deleteGate) hold(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.id = id
}

func (g *deleteGate) client(t *testing.T, d *dockertest.Daemon) *docker.Client {
	t.Helper()
	g.early = make(chan string, 64)
	// t.TempDir paths can exceed the 108-byte unix socket path limit.
	dir, err := os.MkdirTemp("", "ddblocal-proxy-") //nolint:usetesting // see above
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "docker"})
	proxy.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", d.Socket())
	}}
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		hold := r.Method == http.MethodDelete && g.id != "" && strings.Contains(r.URL.Path, "/containers/"+g.id)
		if hold {
			g.id, g.holding = "", true
		} else if g.holding {
			g.early <- r.Method + " " + r.URL.Path
		}
		g.mu.Unlock()
		if hold {
			close(g.held)
			<-g.release
			g.mu.Lock()
			g.holding = false
			g.mu.Unlock()
		}
		proxy.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }() //nolint:errcheck // Serve returns ErrServerClosed at cleanup
	t.Cleanup(func() { _ = srv.Close() })
	client, err := docker.New(docker.Options{Socket: socket})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

// TestOOMKilledNamesTheLimit: a container killed for memory says so, with
// the limit to raise.
func TestOOMKilledNamesTheLimit(t *testing.T) {
	d := presentDaemon(t)
	d.SetProgram(hubRef, dockertest.Exit{Code: 137, OOMKilled: true})
	m := manager(t, d, ddblocal.Config{Memory: 256 << 20})
	err := m.Start(t.Context())
	exited, ok := errors.AsType[*ddblocal.ExitedError](err)
	if !ok || !exited.OOMKilled {
		t.Fatalf("Start = %v, want an OOM-killed *ExitedError", err)
	}
	for _, want := range []string{"memory limit of 256 MiB", "raise the DynamoDB memory limit", "the next DynamoDB call starts it again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// TestNoticeNamesTheLicence: the line logged when DynamoDB Local first
// starts says whose terms it runs under and where Amazon publishes them,
// on one line.
func TestNoticeNamesTheLicence(t *testing.T) {
	notice := ddblocal.Notice
	for _, want := range []string{"Amazon DynamoDB Local", "Amazon's licence terms", "https://aws.amazon.com/dynamodb/dynamodblocallicense/"} {
		if !strings.Contains(notice, want) {
			t.Errorf("Notice = %q, lacks %q", notice, want)
		}
	}
	if strings.Contains(notice, "\n") {
		t.Errorf("Notice = %q, want one line", notice)
	}
}
