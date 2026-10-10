package runtimeapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// DefaultInitTimeout is Lambda's limit on the init phase: a runtime must ask
// for its first invocation within it (containers.md 4.6).
const DefaultInitTimeout = 10 * time.Second

// readHeaderTimeout bounds how long a runtime may take to send request
// headers; bodies and the blocking /next are not bounded by it.
const readHeaderTimeout = 10 * time.Second

// Options configures a Server.
type Options struct {
	// Clock supplies wall time for Lambda-Runtime-Deadline-Ms.
	Clock *clock.Clock
	// Topology decides the address every listener binds and the address
	// containers are told to dial.
	Topology topology.Topology
	// InitTimeout bounds the wait for a cold environment's first /next;
	// zero means DefaultInitTimeout.
	InitTimeout time.Duration
	// Logger receives listener errors; nil discards them.
	Logger *slog.Logger
}

// Server opens one Runtime API listener per execution environment, on the
// topology's bind address and an ephemeral port. It is safe for concurrent
// use.
type Server struct {
	clock       *clock.Clock
	bind        netip.Addr
	advertise   netip.Addr
	topology    string
	initTimeout time.Duration
	log         *slog.Logger
	// listen opens each environment's listener; tests replace it with an
	// in-memory network so a synctest bubble can drive the timeouts.
	listen func(ctx context.Context, network, address string) (net.Listener, error)

	mu     sync.Mutex
	envs   map[*Environment]struct{}
	closed bool
}

// NewServer validates opts and derives the listener addresses from the
// topology. A topology phase 1 cannot serve returns its
// *topology.UnsupportedError.
func NewServer(opts Options) (*Server, error) {
	if opts.Clock == nil {
		return nil, errors.New("runtime API server: Options.Clock is nil; pass the emulator clock")
	}
	if opts.Topology == nil {
		return nil, errors.New("runtime API server: Options.Topology is nil; detect the container topology first")
	}
	addrs, err := opts.Topology.Addresses()
	if err != nil {
		return nil, err
	}
	initTimeout := opts.InitTimeout
	if initTimeout == 0 {
		initTimeout = DefaultInitTimeout
	}
	if initTimeout < 0 {
		return nil, fmt.Errorf("runtime API server: Options.InitTimeout is %s; use a positive duration or zero for %s", initTimeout, DefaultInitTimeout)
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		clock:       opts.Clock,
		bind:        addrs.ListenerBind,
		advertise:   addrs.FunctionEndpointHost,
		topology:    opts.Topology.String(),
		initTimeout: initTimeout,
		log:         log,
		listen:      new(net.ListenConfig).Listen,
		envs:        map[*Environment]struct{}{},
	}, nil
}

// Open starts the listener of a new environment. The container is told
// Environment.Addr as AWS_LAMBDA_RUNTIME_API.
func (s *Server) Open(ctx context.Context) (*Environment, error) {
	ln, err := s.listen(ctx, "tcp", netip.AddrPortFrom(s.bind, 0).String())
	if err != nil {
		return nil, fmt.Errorf("runtime API: listen on %s (the %s): %w", s.bind, s.topology, err)
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return nil, errors.Join(fmt.Errorf("runtime API: listener address %v is not TCP", ln.Addr()), ln.Close())
	}
	e := newEnvironment(s, netip.AddrPortFrom(s.advertise, uint16(tcp.Port))) //nolint:gosec // a TCP port fits in 16 bits
	e.http = &http.Server{
		Handler:           e.routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	// Publishing and serving happen together under s.mu, so a concurrent
	// Close either refuses the environment or finds it fully started.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.Join(&ClosedError{Addr: e.addr}, ln.Close())
	}
	s.envs[e] = struct{}{}
	go e.serve(ln)
	return e, nil
}

// Close closes every open environment and refuses new ones.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	envs := make([]*Environment, 0, len(s.envs))
	for e := range s.envs {
		envs = append(envs, e)
	}
	s.mu.Unlock()
	errs := make([]error, 0, len(envs))
	for _, e := range envs {
		errs = append(errs, e.Close())
	}
	return errors.Join(errs...)
}

func (s *Server) forget(e *Environment) {
	s.mu.Lock()
	delete(s.envs, e)
	s.mu.Unlock()
}
