package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/config"
)

// Server is one understudy serve: the wired emulator and its listeners.
type Server struct {
	cfg   Config
	log   *slog.Logger
	wired *wiring
	// mu guards phase.
	mu    sync.Mutex
	phase phase
}

// phase is where a Server is in its life. New returns it built; Serve moves
// it to serving and, at shutdown, to released; Close moves a built Server
// to released.
type phase uint8

const (
	built phase = iota
	serving
	released
)

// StateError reports a Serve or Close the Server's phase does not allow.
type StateError struct {
	Op     string
	Reason string
}

func (e *StateError) Error() string {
	return "server: " + e.Op + " refused: " + e.Reason
}

// ListenError is a Config.Listen address serve refuses: a non-loopback
// listener that would expose the admin API under /_understudy/, or the
// unauthenticated AWS API, to the network (ARCHITECTURE.md 20).
type ListenError struct {
	Addr   netip.AddrPort
	Reason ListenRefusal
}

// ListenRefusal is why New refused a listener beyond loopback.
type ListenRefusal uint8

// ListenRefusal values.
const (
	// RefusedNoAdminToken is a listener without an admin token.
	RefusedNoAdminToken ListenRefusal = iota
	// RefusedNotInContainer is ContainerPublished asked for outside a
	// container with its own network namespace, or in a Kubernetes pod.
	RefusedNotInContainer
	// RefusedUnauthenticatedAWSAPI is a listener on the host with an admin
	// token but without AWSAPIUnauthenticated: the token guards only the
	// admin API.
	RefusedUnauthenticatedAWSAPI
)

func (e *ListenError) Error() string {
	var why string
	switch e.Reason {
	case RefusedNoAdminToken:
		why = "a non-loopback listener would expose the admin API under /_understudy/ to the network"
	case RefusedNotInContainer:
		why = "the published container port can stand in for an admin token only inside a container with its own network namespace, outside Kubernetes, and this process does not run in one"
	case RefusedUnauthenticatedAWSAPI:
		why = "the admin token guards only the admin API under /_understudy/, and every client may sign the AWS API on the same listener with the public dummy pair test/test, which verifies even under --sigv4 verify, " +
			"so anyone who reaches it could read every secret and object and run any container image as a Lambda function"
	}
	return fmt.Sprintf("refusing to listen on %s: %s. "+
		"Listening beyond loopback on the host needs an admin token, which every admin call must then send as Authorization: Bearer <token>, "+
		"and --allow-unauthenticated-aws-api, which accepts that every peer on the network can use the AWS API; "+
		"inside a container, a published port (docker run -p 127.0.0.1:%d:%d) stands in for both; "+
		"to stay local, listen on loopback, such as --listen 127.0.0.1:%d", e.Addr, why, e.Addr.Port(), e.Addr.Port(), e.Addr.Port())
}

// BindError is a listener Serve could not open, such as an address another
// process holds. Nothing was served; the Server stays built for Close.
type BindError struct {
	Addr netip.AddrPort
	// TLS is set when the address is the TLS listener's.
	TLS bool
	Err error
}

func (e *BindError) Error() string {
	what := "listen on"
	if e.TLS {
		what = "listen for TLS on"
	}
	return fmt.Sprintf("cannot %s %s: %v; pick a free port, or port 0 for any free one", what, e.Addr, e.Err)
}

func (e *BindError) Unwrap() error { return e.Err }

// New wires the emulator and, with persistence, locks the data directory
// and loads the saved state. Nothing listens until Serve. A zero Listen is
// 127.0.0.1:0; a non-loopback one is a *ListenError unless it has the
// ContainerPublished exposure inside a container, or an AdminToken and
// AWSAPIUnauthenticated, and an unwritable ReadyFile is a *ReadyFileError.
// A setting refused as it is applied, such as a fault rule or a hot-reload
// directory, is a *SettingError.
//
// What New opens (the journal, the blob store, the data directory, the
// code store) is released when Serve shuts down, or by Close when the
// Server is never served; a New that fails releases it before returning.
func New(cfg Config) (*Server, error) {
	if !cfg.Listen.IsValid() {
		cfg.Listen = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 0)
	}
	if err := checkExposure(cfg); err != nil {
		return nil, err
	}
	if err := checkReadyFile(cfg.ReadyFile); err != nil {
		return nil, err
	}
	out := cfg.LogOutput
	if out == nil {
		out = os.Stderr
		cfg.LogOutput = out
	}
	hopts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var handler slog.Handler = slog.NewTextHandler(out, hopts)
	if cfg.LogFormat == LogJSON {
		handler = slog.NewJSONHandler(out, hopts)
	}
	log := slog.New(handler)
	// Each wiring step names what it was doing, and a *SettingError reads
	// as the configuration's own refusals do, so the error goes out bare.
	w, err := wire(cfg, log)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, log: log, wired: w}, nil
}

// checkExposure refuses a listener beyond loopback that nothing protects.
// Inside a container the published port guards both APIs; on the host the
// admin token guards only the admin API, so the AWS API, which every client
// may sign with the public dummy pair test/test, needs the explicit
// AWSAPIUnauthenticated as well.
func checkExposure(cfg Config) error {
	if cfg.Listen.Addr().IsLoopback() {
		return nil
	}
	_, published := cfg.Exposure.(ContainerPublished)
	switch {
	case published && publishedPortGuards():
		return nil
	case cfg.AdminToken == "" && published:
		return &ListenError{Addr: cfg.Listen, Reason: RefusedNotInContainer}
	case cfg.AdminToken == "":
		return &ListenError{Addr: cfg.Listen, Reason: RefusedNoAdminToken}
	}
	if _, ok := cfg.AWSAPI.(AWSAPIUnauthenticated); !ok {
		return &ListenError{Addr: cfg.Listen, Reason: RefusedUnauthenticatedAWSAPI}
	}
	return nil
}

// publishedPortGuards reports whether only published ports reach this
// process: a container in its own network namespace, outside Kubernetes.
// It repeats config's check, which picks the default listener, so a
// ContainerPublished exposure built some other way is held to the same
// rule. This package reads no environment, so a pod is recognized here by
// its kubepods cgroup only; config also reads KUBERNETES_SERVICE_HOST.
func publishedPortGuards() bool {
	_, in := config.DetectPlacement(os.ReadFile, nil).(config.InContainer)
	return in
}

// Handler is the main listener's handler, the edge in front of the wired
// emulator, for tests that drive it without a listener. Only Serve runs the
// kernel, so behind Handler alone no timer fires: a long poll parked on a
// delayed or in-flight message is not woken when the message turns visible.
func (s *Server) Handler() http.Handler { return s.wired.edge }

// shutdownGrace bounds how long shutdown waits for requests in flight.
const shutdownGrace = 5 * time.Second

// session is what one Serve runs besides the listeners.
type session struct {
	ls          *listeners
	endRequests context.CancelFunc
	stopKernel  context.CancelFunc
	kernelDone  chan error
	// stopBackground ends the pool's watch, the checkpoint loop, the
	// DynamoDB Local start and the sweep; background waits for them.
	stopBackground context.CancelFunc
	background     sync.WaitGroup
}

// Serve listens on Config.Listen (and the TLS listener when configured),
// reports readiness, and serves until ctx ends, then shuts down gracefully:
// long polls end, requests in flight get shutdownGrace to finish, Lambda
// environments and DynamoDB Local are removed, the kernel stops, the state
// is saved with persistence and the ready file goes. The returned error is
// nil after a graceful shutdown.
//
// A Server serves once: Serve while serving, or after shutdown or Close,
// is a *StateError. When listening fails the Server stays built, for Close.
func (s *Server) Serve(ctx context.Context) error {
	if err := s.begin(); err != nil {
		return err
	}
	main, secure, err := s.listen(ctx)
	if err != nil {
		s.setPhase(built)
		return err
	}
	eps := Endpoints{Main: advertised("http", main)}
	if secure != nil {
		eps.TLS = advertised("https", secure)
	}
	sess, err := s.start(ctx, main, secure)
	if err != nil {
		return errors.Join(err, s.shutdown(ctx, sess))
	}
	if err := s.writeReadyFile(eps); err != nil {
		return errors.Join(err, s.shutdown(ctx, sess))
	}
	if s.cfg.OnReady != nil {
		s.cfg.OnReady(eps)
	}
	s.banner(ctx, eps, main.Addr())
	select {
	case <-ctx.Done():
		s.logStopping(ctx)
	case err := <-sess.ls.failed:
		return errors.Join(err, s.shutdown(ctx, sess))
	case err := <-sess.kernelDone:
		// shutdown waits for the kernel's result, which is taken here.
		sess.kernelDone <- nil
		return errors.Join(errors.New("server: the kernel stopped while serving, so timers no longer fire"), err, s.shutdown(ctx, sess))
	}
	return s.shutdown(ctx, sess)
}

// Close releases what New opened for a Server that is not serving: one
// never served, or one whose Serve could not listen. After Serve has shut
// down, or a first Close, it does nothing and returns nil. While Serve runs
// it is a *StateError; cancel Serve's context instead.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.phase {
	case built:
		s.phase = released
		return s.wired.close(context.Background())
	case serving:
		return &StateError{Op: "Close", Reason: "Serve is running and releases everything at shutdown; cancel the context passed to Serve instead"}
	case released:
	}
	return nil
}

// listen opens the main listener and, when configured, the TLS one.
func (s *Server) listen(ctx context.Context) (main, secure net.Listener, err error) {
	main, err = listen(ctx, s.cfg.Listen)
	if err != nil {
		return nil, nil, &BindError{Addr: s.cfg.Listen, Err: err}
	}
	t, ok := s.cfg.TLS.(TLSSelfSigned)
	if !ok {
		return main, nil, nil
	}
	cert, err := selfSigned(s.wired.kernel.Clock().Wall())
	if err != nil {
		return nil, nil, errors.Join(err, main.Close())
	}
	raw, err := listen(ctx, t.Listen)
	if err != nil {
		return nil, nil, errors.Join(&BindError{Addr: t.Listen, TLS: true, Err: err}, main.Close())
	}
	return main, tls.NewListener(raw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}), nil
}

// start runs the kernel, the listeners and the background work. When it
// fails before handing main and secure to the session's listeners, it
// closes them, since shutdown drains only what the session serves.
func (s *Server) start(ctx context.Context, main, secure net.Listener) (*session, error) {
	w := s.wired
	kctx, stopKernel := context.WithCancel(context.WithoutCancel(ctx))
	requests, endRequests := context.WithCancel(context.WithoutCancel(ctx))
	bg, stopBackground := context.WithCancel(context.WithoutCancel(ctx))
	sess := &session{
		ls:             newListeners(s.log, func(net.Listener) context.Context { return requests }),
		endRequests:    endRequests,
		stopKernel:     stopKernel,
		kernelDone:     make(chan error, 1),
		stopBackground: stopBackground,
	}
	bound, err := netip.ParseAddrPort(main.Addr().String())
	if err != nil {
		stopKernel()
		sess.kernelDone <- nil
		return sess, errors.Join(fmt.Errorf("server: reading the listener's address %s: %w", main.Addr(), err), closeListeners(main, secure))
	}
	// Timers a loaded state armed may need a function container as soon
	// as the kernel runs, and the pollers register their timer kind, so
	// both come first.
	w.host.serving(bound.Port(), func(addr netip.AddrPort) error {
		if bound.Addr().IsUnspecified() || bound.Addr().Unmap() == addr.Addr().Unmap() {
			// The main listener already holds the port on the address
			// functions reach, on every interface (as in a container
			// reaching a remote daemon) or on exactly that address, so a
			// second listener would collide and functions call the main
			// one (ADR 0011).
			s.log.LogAttrs(bg, slog.LevelInfo, "function containers reach the main listener, which binds their address", slog.String("addr", addr.String()), slog.String("main", bound.String()))
			return nil
		}
		ln, err := containerListen(bg, addr, net.InterfaceAddrs, s.log)
		if err != nil {
			return err
		}
		return sess.ls.serve("container listener", w.containerEdge, ln)
	})
	w.lambda.StartPollers()
	// StartPollers registers a timer kind after wire checked the bindings
	// and the armed timers; Run would refuse such a mistake at once,
	// leaving no timer to fire.
	if err := errors.Join(w.kernel.BindErr(), checkTimers(w.kernel.Timers())); err != nil {
		stopKernel()
		sess.kernelDone <- nil
		return sess, errors.Join(err, closeListeners(main, secure))
	}
	go func() { sess.kernelDone <- w.kernel.Run(kctx) }()
	if err := sess.ls.serve("main listener", w.edge, main); err != nil {
		return sess, errors.Join(err, closeListeners(secure))
	}
	if secure != nil {
		if err := sess.ls.serve("TLS listener", w.edge, secure); err != nil {
			return sess, err
		}
	}
	if w.pool != nil {
		sess.background.Go(func() {
			if err := w.pool.Run(bg); err != nil {
				s.log.LogAttrs(bg, slog.LevelWarn, "the Lambda pool stopped watching its environments", slog.String("error", err.Error()))
			}
		})
	}
	if w.persist != nil {
		sess.background.Go(func() { w.persist.run(bg) })
	}
	sess.background.Go(func() {
		// The sweep comes first: a crashed run's DynamoDB Local may still
		// hold <dir>/dynamodb, which the new one would then share.
		if w.persist != nil {
			w.host.sweep(bg)
		}
		w.local.startInBackground(bg)
	})
	sess.background.Go(func() { w.sweepGone(bg, s.log) })
	w.admin.SetReady(bound)
	return sess, nil
}

// closeListeners closes the listeners start did not hand to the session;
// a nil one, a TLS listener not configured, is skipped.
func closeListeners(lns ...net.Listener) error {
	var errs []error
	for _, ln := range lns {
		if ln != nil {
			errs = append(errs, ln.Close())
		}
	}
	return errors.Join(errs...)
}

// logStopping logs once that the graceful shutdown has begun. A context
// ended with a cause other than plain cancellation or a deadline was ended
// by a signal (signal.NotifyContext's cause), and the understudy command
// restores the default signal handling then, so the line says that a
// second signal ends the process at once.
func (s *Server) logStopping(ctx context.Context) {
	cause := context.Cause(ctx)
	attrs := []slog.Attr{slog.String("cause", cause.Error())}
	if !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		attrs = append(attrs, slog.String("force", "send the signal again to exit at once, skipping the cleanup"))
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "understudy shutting down", attrs...)
}

// shutdown ends requests in flight, stops the background work, removes the
// Lambda environments and DynamoDB Local, stops the kernel, saves the
// state with persistence and removes the ready file. The background work
// stops before the containers are removed, so a DynamoDB Local start it
// began is stopped with the container rather than launching after it.
func (s *Server) shutdown(ctx context.Context, sess *session) error {
	w := s.wired
	w.admin.SetNotReady()
	sess.endRequests()
	errs := []error{sess.ls.drain(ctx)}
	sess.stopBackground()
	sess.background.Wait()
	errs = append(errs, w.stopContainers(ctx))
	sess.stopKernel()
	errs = append(errs, <-sess.kernelDone)
	if w.persist != nil {
		errs = append(errs, w.persist.checkpoint(context.WithoutCancel(ctx), true))
	}
	errs = append(errs, w.close(ctx))
	s.setPhase(released)
	if s.cfg.ReadyFile != "" {
		if err := os.Remove(s.cfg.ReadyFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("server: shutdown: %w", err)
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "understudy stopped")
	return nil
}

// begin moves a built Server to serving.
func (s *Server) begin() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.phase {
	case built:
		s.phase = serving
		return nil
	case serving:
		return &StateError{Op: "Serve", Reason: "Serve is already running on this Server"}
	case released:
		return &StateError{Op: "Serve", Reason: "this Server was shut down or closed and its state released; build a new one with New"}
	}
	return nil
}

func (s *Server) setPhase(p phase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = p
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

// Endpoints are the URLs a serving Server answers at, as clients should
// dial them: a wildcard listener is advertised as loopback.
type Endpoints struct {
	Main string
	// TLS is empty without the TLS listener.
	TLS string
}

// readyFile is the --ready-file document.
type readyFile struct {
	Endpoint string `json:"endpoint"`
	// TLSEndpoint is the TLS listener's URL, absent without one.
	TLSEndpoint string `json:"tlsEndpoint,omitempty"`
	PID         int    `json:"pid"`
}

// ReadyFileError is a Config.ReadyFile path serve cannot write. New
// refuses one whose directory is missing or not writable before anything
// listens.
type ReadyFileError struct {
	Path string
	Err  error
}

func (e *ReadyFileError) Error() string {
	return "server: cannot write --ready-file " + e.Path + ": " + e.Err.Error() + "; create its directory or make it writable, or choose another path"
}

func (e *ReadyFileError) Unwrap() error { return e.Err }

// checkReadyFile refuses a ready file path whose directory serve cannot
// create the file in, by creating and removing a temporary file there.
func checkReadyFile(path string) error {
	if path == "" {
		return nil
	}
	tmp, err := createReadyTemp(path)
	if err != nil {
		return err
	}
	if err := errors.Join(tmp.Close(), os.Remove(tmp.Name())); err != nil {
		return &ReadyFileError{Path: path, Err: err}
	}
	return nil
}

// createReadyTemp creates the temporary file the ready file at path is
// written to before it moves into place. A failure names the directory,
// not the temporary file, which the user never named.
func createReadyTemp(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".understudy-ready-*")
	if err != nil {
		if pe, ok := errors.AsType[*fs.PathError](err); ok {
			err = fmt.Errorf("its directory %s: %w", dir, pe.Err)
		}
		return nil, &ReadyFileError{Path: path, Err: err}
	}
	return tmp, nil
}

// writeReadyFile writes the ready file atomically, so a watcher never reads
// half a document.
func (s *Server) writeReadyFile(eps Endpoints) error {
	if s.cfg.ReadyFile == "" {
		return nil
	}
	data, err := json.Marshal(readyFile{Endpoint: eps.Main, TLSEndpoint: eps.TLS, PID: os.Getpid()})
	if err != nil {
		return fmt.Errorf("server: encoding the ready file: %w", err)
	}
	tmp, err := createReadyTemp(s.cfg.ReadyFile)
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		return &ReadyFileError{Path: s.cfg.ReadyFile, Err: errors.Join(err, os.Remove(tmp.Name()))}
	}
	if err := os.Rename(tmp.Name(), s.cfg.ReadyFile); err != nil {
		return &ReadyFileError{Path: s.cfg.ReadyFile, Err: errors.Join(err, os.Remove(tmp.Name()))}
	}
	return nil
}
