package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/sustinbebustin/understudy/internal/journal"
)

// Config is what serve needs; the full configuration package maps onto it
// later. The zero Config listens on an ephemeral loopback port with a
// running clock, random ids and text logs at info level.
type Config struct {
	// Listen is the address to bind; port 0 picks a free one.
	Listen     netip.AddrPort
	ClockStart ClockStart
	// Seed makes every generated id reproducible; nil draws from crypto/rand.
	Seed      *uint64
	LogFormat LogFormat
	LogLevel  slog.Level
	// ReadyFile, when set, receives {"endpoint", "pid"} once the listener
	// accepts, and is removed at shutdown.
	ReadyFile string
	// Version is reported by the health endpoint.
	Version string
	// LogOutput receives the logs; nil means stderr.
	LogOutput io.Writer
}

// ClockStart is how the emulator clock starts.
//
//sumtype:decl
type ClockStart interface{ isClockStart() }

// ClockRunning follows wall time.
type ClockRunning struct{}

// ClockFrozen holds emulator time still until it is advanced; a zero At
// freezes at wall time now.
type ClockFrozen struct{ At time.Time }

func (ClockRunning) isClockStart() {}
func (ClockFrozen) isClockStart()  {}

// LogFormat is how log records are written.
type LogFormat uint8

// LogFormat values; the zero value is text.
const (
	LogText LogFormat = iota
	LogJSON
)

// Server is one understudy serve: the wired emulator and its main
// listener.
type Server struct {
	cfg    Config
	log    *slog.Logger
	health *health
	wired  *wiring
}

// ListenError is a Config.Listen address serve refuses: a non-loopback
// listener would expose the unauthenticated admin API (ARCHITECTURE.md 20).
type ListenError struct{ Addr netip.AddrPort }

func (e *ListenError) Error() string {
	return fmt.Sprintf("refusing to listen on %s: a non-loopback listener would expose the admin API under /_understudy/ to the network. "+
		"Listening beyond loopback needs an admin token or a published container port, and this build supports neither yet; "+
		"listen on loopback instead, such as --listen 127.0.0.1:%d", e.Addr, e.Addr.Port())
}

// New wires the emulator. Nothing listens until Serve. A zero Listen is
// 127.0.0.1:0; a non-loopback one is a *ListenError.
func New(cfg Config) (*Server, error) {
	if !cfg.Listen.IsValid() {
		cfg.Listen = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 0)
	}
	if !cfg.Listen.Addr().IsLoopback() {
		return nil, &ListenError{Addr: cfg.Listen}
	}
	out := cfg.LogOutput
	if out == nil {
		out = os.Stderr
	}
	hopts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var handler slog.Handler = slog.NewTextHandler(out, hopts)
	if cfg.LogFormat == LogJSON {
		handler = slog.NewJSONHandler(out, hopts)
	}
	log := slog.New(handler)
	h := &health{version: cfg.Version, log: log}
	w, err := wire(cfg, log, h)
	if err != nil {
		return nil, fmt.Errorf("server: wiring the emulator: %w", err)
	}
	h.services = w.services
	h.uptime = w.kernel.Clock().Stopwatch()
	return &Server{cfg: cfg, log: log, health: h, wired: w}, nil
}

// Handler is the main listener's handler, the edge in front of the wired
// emulator, for tests that drive it without a listener. Only Serve runs the
// kernel, so behind Handler alone no timer fires: a long poll parked on a
// delayed or in-flight message is not woken when the message turns visible.
func (s *Server) Handler() http.Handler { return s.wired.edge }

// Journal is the emulator's journal.
func (s *Server) Journal() *journal.Journal { return s.wired.journal }

// shutdownGrace bounds how long shutdown waits for requests in flight.
const shutdownGrace = 5 * time.Second

// Serve listens on Config.Listen, reports readiness, and serves until ctx
// ends, then shuts down gracefully: long polls end, requests in flight get
// shutdownGrace to finish, the kernel stops and the ready file goes. The
// returned error is nil after a graceful shutdown.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.cfg.Listen.String())
	if err != nil {
		return fmt.Errorf("server: cannot listen on %s: %w; pick a free port or use 127.0.0.1:0", s.cfg.Listen, err)
	}
	endpoint := "http://" + ln.Addr().String()

	kctx, stopKernel := context.WithCancel(context.WithoutCancel(ctx))
	defer stopKernel()
	kernelDone := make(chan error, 1)
	go func() { kernelDone <- s.wired.kernel.Run(kctx) }()

	requests, endRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer endRequests()
	srv := s.wired.edge.Server(func(net.Listener) context.Context { return requests })
	tracked := newConns(s.log)
	srv.ConnState = tracked.track
	srv.RegisterOnShutdown(tracked.closeUnused)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	s.health.ready.Store(true)
	if err := s.writeReadyFile(endpoint); err != nil {
		return errors.Join(err, s.shutdown(ctx, srv, tracked, endRequests, stopKernel, kernelDone))
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "understudy is serving",
		slog.String("endpoint", endpoint),
		slog.String("admin", endpoint+"/_understudy/v1/"),
		slog.Any("services", s.wired.services),
		slog.String("env", "AWS_ENDPOINT_URL="+endpoint),
	)

	select {
	case <-ctx.Done():
	case err := <-served:
		return errors.Join(fmt.Errorf("server: serving %s stopped: %w", endpoint, err),
			s.shutdown(ctx, srv, tracked, endRequests, stopKernel, kernelDone))
	}
	return s.shutdown(ctx, srv, tracked, endRequests, stopKernel, kernelDone)
}

// shutdown ends requests in flight, stops the kernel and removes the ready
// file.
func (s *Server) shutdown(ctx context.Context, srv *http.Server, tracked *conns, endRequests, stopKernel context.CancelFunc, kernelDone <-chan error) error {
	s.health.ready.Store(false)
	endRequests()
	errs := []error{drain(ctx, srv, tracked)}
	stopKernel()
	errs = append(errs, <-kernelDone, s.wired.journal.Close())
	if s.cfg.ReadyFile != "" {
		if err := os.Remove(s.cfg.ReadyFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("server: shutdown: %w", err)
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "understudy stopped")
	return nil
}

// drain closes the listener and waits up to shutdownGrace for requests in
// flight to finish; connections that never sent a request are closed at
// once (conns). Requests still running at the deadline are cut off, and
// only they make drain fail.
func drain(ctx context.Context, srv *http.Server, tracked *conns) error {
	grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	err := srv.Shutdown(grace)
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	active := tracked.active()
	if cerr := srv.Close(); cerr != nil {
		return fmt.Errorf("closing connections %s after shutdown began: %w", shutdownGrace, cerr)
	}
	if active == 0 {
		return nil
	}
	return fmt.Errorf("%d requests were still running %s after shutdown began and were cut off", active, shutdownGrace)
}

// readyFile is the --ready-file document.
type readyFile struct {
	Endpoint string `json:"endpoint"`
	PID      int    `json:"pid"`
}

// writeReadyFile writes the ready file atomically, so a watcher never reads
// half a document.
func (s *Server) writeReadyFile(endpoint string) error {
	if s.cfg.ReadyFile == "" {
		return nil
	}
	data, err := json.Marshal(readyFile{Endpoint: endpoint, PID: os.Getpid()})
	if err != nil {
		return fmt.Errorf("server: encoding the ready file: %w", err)
	}
	dir := filepath.Dir(s.cfg.ReadyFile)
	tmp, err := os.CreateTemp(dir, ".understudy-ready-*")
	if err != nil {
		return fmt.Errorf("server: cannot write the ready file in %s: %w", dir, err)
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		return fmt.Errorf("server: writing the ready file %s: %w", tmp.Name(), errors.Join(err, os.Remove(tmp.Name())))
	}
	if err := os.Rename(tmp.Name(), s.cfg.ReadyFile); err != nil {
		return fmt.Errorf("server: moving the ready file into place at %s: %w", s.cfg.ReadyFile, errors.Join(err, os.Remove(tmp.Name())))
	}
	return nil
}
