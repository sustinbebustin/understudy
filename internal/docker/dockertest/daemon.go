// Package dockertest is an in-process fake of the Docker Engine API on a
// unix socket, for testing code that drives containers without a daemon.
//
// It implements the endpoints internal/docker calls with the statuses and
// messages the real daemon sends (containers.md 2.3): image presence and
// scripted pulls, containers whose behavior on start is a scripted Program,
// framed logs, waits, the events stream, networks with address allocation,
// and archive uploads. Time inside the fake is logical: every event and log
// line advances a counter by one millisecond from a fixed epoch, so outputs
// are deterministic.
package dockertest

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// Options configures a Daemon. The zero value is a Docker Engine serving
// API 1.55 on Linux.
type Options struct {
	// APIVersion is the highest API version served and reported by /_ping;
	// empty means 1.55.
	APIVersion string
	// HideAPIVersion omits the Api-Version header from /_ping, as very old
	// daemons do.
	HideAPIVersion bool
	// Engine selects Docker (nil or docker.Moby) or Podman, which adds the
	// Libpod-Api-Version header and accepts any requested API version.
	Engine docker.Engine
	// Info is served by GET /info; empty identity fields get defaults.
	Info docker.Info
	// BridgeSubnet is the default bridge network's subnet; its first address
	// is the gateway. The zero value means 172.17.0.0/16.
	BridgeSubnet netip.Prefix
	// DefaultNetwork names the default bridge network; empty means
	// "bridge". Podman's compatibility layer may serve it only as "podman".
	// Network mode "bridge" always joins it.
	DefaultNetwork string
}

// Request is one request the daemon received.
type Request struct {
	Method string
	Path   string
	Query  url.Values
}

// Upload is one archive the daemon received for a container.
type Upload struct {
	Path string
	Data []byte
}

type fault struct {
	status  int
	message string
}

// Daemon is a running fake Engine. All methods are safe for concurrent use.
type Daemon struct {
	opts   Options
	socket string

	mu      sync.Mutex
	changed chan struct{}
	ticks   int64
	created int
	subnets int
	// ephemeral counts the host ports handed out for port bindings.
	ephemeral  int
	images     map[string]docker.Image
	remote     map[string]Remote
	programs   map[string]Program
	containers map[string]*container
	history    []*container
	networks   map[string]*network
	events     []docker.Event
	requests   []Request
	faults     map[string][]fault
}

// epoch is where the fake's logical clock starts.
func epoch() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) }

// New starts a fake daemon on a unix socket in a fresh temporary directory
// and stops it when the test ends.
func New(tb testing.TB, opts Options) *Daemon {
	tb.Helper()
	d := newDaemon(opts)
	// A short directory under the system temp dir keeps the socket path
	// below the 108-byte unix socket limit that t.TempDir paths can exceed.
	dir, err := os.MkdirTemp("", "dockertest-") //nolint:usetesting // t.TempDir paths can exceed the unix socket path limit
	if err != nil {
		tb.Fatalf("dockertest: create socket directory: %v", err)
	}
	d.socket = filepath.Join(dir, "docker.sock")
	var lc net.ListenConfig
	l, err := lc.Listen(tb.Context(), "unix", d.socket)
	if err != nil {
		tb.Fatalf("dockertest: listen on %s: %v", d.socket, err)
	}
	srv := &http.Server{Handler: d.routes(), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan struct{})
	go func() {
		defer close(served)
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			tb.Errorf("dockertest: serve: %v", err)
		}
	}()
	tb.Cleanup(func() {
		if err := srv.Close(); err != nil {
			tb.Errorf("dockertest: close server: %v", err)
		}
		<-served
		if err := os.RemoveAll(dir); err != nil {
			tb.Errorf("dockertest: remove %s: %v", dir, err)
		}
	})
	return d
}

func newDaemon(opts Options) *Daemon {
	if opts.APIVersion == "" {
		opts.APIVersion = "1.55"
	}
	if opts.Engine == nil {
		opts.Engine = docker.Moby{}
	}
	if !opts.BridgeSubnet.IsValid() {
		opts.BridgeSubnet = netip.MustParsePrefix("172.17.0.0/16")
	}
	if opts.DefaultNetwork == "" {
		opts.DefaultNetwork = "bridge"
	}
	opts.Info = withInfoDefaults(opts.Info)
	d := &Daemon{
		opts:       opts,
		changed:    make(chan struct{}),
		images:     map[string]docker.Image{},
		remote:     map[string]Remote{},
		programs:   map[string]Program{},
		containers: map[string]*container{},
		networks:   map[string]*network{},
		faults:     map[string][]fault{},
	}
	d.addNetwork(opts.DefaultNetwork, opts.BridgeSubnet, nil, false)
	return d
}

func withInfoDefaults(info docker.Info) docker.Info {
	if info.ID == "" {
		info.ID = "DOCKERTEST"
	}
	if info.Name == "" {
		info.Name = "dockertest"
	}
	if info.OperatingSystem == "" {
		info.OperatingSystem = "Ubuntu 24.04 LTS"
	}
	if info.OSType == "" {
		info.OSType = "linux"
	}
	if info.Architecture == "" {
		info.Architecture = "x86_64"
	}
	return info
}

// Socket returns the daemon's socket path.
func (d *Daemon) Socket() string { return d.socket }

// Client returns a client for the daemon, closed when the test ends.
func (d *Daemon) Client(tb testing.TB) *docker.Client {
	tb.Helper()
	c, err := docker.New(docker.Options{Socket: d.socket})
	if err != nil {
		tb.Fatalf("dockertest: new client: %v", err)
	}
	tb.Cleanup(c.Close)
	return c
}

// FailNext makes the next request to route fail with status and message.
// The route is the method and path pattern without the version prefix,
// for example "POST /containers/{id}/start".
func (d *Daemon) FailNext(route string, status int, message string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.faults[route] = append(d.faults[route], fault{status: status, message: message})
}

// Requests returns every request received so far, in order.
func (d *Daemon) Requests() []Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.requests)
}

// Events returns every event emitted so far, in order.
func (d *Daemon) Events() []docker.Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.events)
}

// now advances the logical clock and returns the new instant. Callers hold
// d.mu.
func (d *Daemon) now() time.Time {
	d.ticks++
	return epoch().Add(time.Duration(d.ticks) * time.Millisecond)
}

// notify wakes every request waiting for a state change. Callers hold d.mu.
func (d *Daemon) notify() {
	close(d.changed)
	d.changed = make(chan struct{})
}

// waitChange blocks until the state changes after ch was read or ctx ends.
func waitChange(ctx context.Context, ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

type handler func(w http.ResponseWriter, r *http.Request)

func (d *Daemon) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_ping", d.wrap("GET /_ping", d.ping))
	mux.HandleFunc("HEAD /_ping", d.wrap("HEAD /_ping", d.ping))
	versioned := map[string]handler{
		"GET /info":                    d.info,
		"GET /images/{ref...}":         d.imageInspect,
		"POST /images/create":          d.imagePull,
		"POST /containers/create":      d.containerCreate,
		"GET /containers/json":         d.containerList,
		"GET /containers/{id}/json":    d.containerInspect,
		"POST /containers/{id}/start":  d.containerStart,
		"POST /containers/{id}/stop":   d.containerStop,
		"POST /containers/{id}/kill":   d.containerKill,
		"POST /containers/{id}/wait":   d.containerWait,
		"DELETE /containers/{id}":      d.containerRemove,
		"GET /containers/{id}/logs":    d.containerLogs,
		"GET /containers/{id}/stats":   d.containerStats,
		"PUT /containers/{id}/archive": d.containerArchive,
		"GET /events":                  d.eventStream,
		"GET /networks":                d.networkList,
		"GET /networks/{id}":           d.networkInspect,
		"POST /networks/create":        d.networkCreate,
		"POST /networks/{id}/connect":  d.networkConnect,
		"DELETE /networks/{id}":        d.networkRemove,
	}
	for route, h := range versioned {
		method, path, _ := strings.Cut(route, " ")
		mux.HandleFunc(method+" /{version}"+path, d.wrap(route, d.versioned(h)))
	}
	return mux
}

// wrap records the request and serves an injected fault if one is queued.
func (d *Daemon) wrap(route string, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.requests = append(d.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query()})
		queued := d.faults[route]
		var f *fault
		if len(queued) > 0 {
			f = &queued[0]
			d.faults[route] = queued[1:]
		}
		d.mu.Unlock()
		if f != nil {
			writeError(w, f.status, f.message)
			return
		}
		h(w, r)
	}
}

// versioned enforces the /v<major.minor> prefix the way each engine does:
// Docker refuses a version above its maximum, Podman accepts any.
func (d *Daemon) versioned(h handler) handler {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.PathValue("version"), "v")
		requested, err := docker.ParseAPIVersion(raw)
		if !ok || err != nil {
			writeError(w, http.StatusNotFound, "page not found")
			return
		}
		served, err := docker.ParseAPIVersion(d.opts.APIVersion)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "dockertest: unparsable Options.APIVersion "+d.opts.APIVersion)
			return
		}
		if _, podman := d.opts.Engine.(docker.Podman); !podman && requested.Compare(served) > 0 {
			writeError(w, http.StatusBadRequest, "client version "+raw+" is too new. Maximum supported API version is "+d.opts.APIVersion)
			return
		}
		h(w, r)
	}
}

// requestedBelow reports whether a versioned request asked for an API
// version older than v; versioned has already validated the prefix.
func requestedBelow(r *http.Request, v docker.APIVersion) bool {
	requested, err := docker.ParseAPIVersion(strings.TrimPrefix(r.PathValue("version"), "v"))
	return err == nil && requested.Compare(v) < 0
}

func (d *Daemon) ping(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	if !d.opts.HideAPIVersion {
		h.Set("Api-Version", d.opts.APIVersion)
	}
	h.Set("Ostype", d.opts.Info.OSType)
	switch e := d.opts.Engine.(type) {
	case docker.Moby:
		h.Set("Server", "Docker/dockertest ("+d.opts.Info.OSType+")")
	case docker.Podman:
		h.Set("Libpod-Api-Version", e.LibpodAPIVersion)
		h.Set("Server", "Libpod/dockertest ("+d.opts.Info.OSType+")")
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		writeBody(w, []byte("OK"))
	}
}

func (d *Daemon) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, d.opts.Info)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "dockertest: encode response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeBody(w, append(raw, '\n'))
}

func writeError(w http.ResponseWriter, status int, message string) {
	raw, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		raw = []byte(`{"message":"dockertest: unencodable error message"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeBody(w, append(raw, '\n'))
}

// writeBody writes to a client; a failed write means the client went away,
// which ends the request either way.
func writeBody(w http.ResponseWriter, b []byte) bool {
	if _, err := w.Write(b); err != nil {
		return false
	}
	return http.NewResponseController(w).Flush() == nil
}

func decodeBody(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}
