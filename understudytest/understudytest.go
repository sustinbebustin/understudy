package understudytest

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// Options configure an Instance. The zero value runs a running clock with
// random ids, Docker at its default socket, DynamoDB enabled and AWS's own
// regions.
type Options struct {
	// Seed makes every generated id reproducible; nil draws from
	// crypto/rand.
	Seed *uint64
	// Clock is how the emulator clock starts; nil is Running.
	Clock Clock
	// Docker is where Lambda functions and DynamoDB Local run; nil is
	// DockerSocket at /var/run/docker.sock.
	Docker Docker
	// DynamoDB is whether the DynamoDB service runs; nil is
	// DynamoDBEnabled.
	DynamoDB DynamoDB
	// Regions are region names accepted beyond AWS's own, such as local-1.
	Regions []string
}

// Clock is how an Instance's emulator clock starts.
//
//sumtype:decl
type Clock interface{ isClock() }

// Running follows wall time.
type Running struct{}

// Frozen holds emulator time still until the test advances it through
// Admin; a zero At freezes at wall time at start.
type Frozen struct{ At time.Time }

func (Running) isClock() {}
func (Frozen) isClock()  {}

// Docker is where an Instance runs containers.
//
//sumtype:decl
type Docker interface{ isDocker() }

// DockerSocket reaches the Docker Engine at the unix socket Path; empty is
// /var/run/docker.sock. Nothing is dialed until a function or DynamoDB
// needs it.
type DockerSocket struct{ Path string }

// DockerDisabled runs no container: Invoke and DynamoDB answer 501
// NotImplemented, while every control plane keeps working.
type DockerDisabled struct{}

func (DockerSocket) isDocker()   {}
func (DockerDisabled) isDocker() {}

// DynamoDB is whether an Instance runs the DynamoDB service.
//
//sumtype:decl
type DynamoDB interface{ isDynamoDB() }

// DynamoDBEnabled serves DynamoDB through Amazon DynamoDB Local, pulled
// and run in Docker on first use under Amazon's licence terms.
type DynamoDBEnabled struct{}

// DynamoDBDisabled turns the DynamoDB service off: DynamoDB calls answer
// 501 and DynamoDB Local is never pulled.
type DynamoDBDisabled struct{}

func (DynamoDBEnabled) isDynamoDB()  {}
func (DynamoDBDisabled) isDynamoDB() {}

// Instance is one in-process understudy serving on an ephemeral loopback
// port until its test ends.
type Instance struct {
	endpoint string
	admin    *admin.Client
}

// readyTimeout bounds how long Start waits for the listener.
const readyTimeout = 10 * time.Second

// Start runs understudy in process on 127.0.0.1:0 and stops it, releasing
// everything it opened, when tb ends. Each Instance has its own state, so
// parallel tests may start one each. Server logs go to tb's output.
func Start(tb testing.TB, o Options) *Instance {
	tb.Helper()
	cfg := serverConfig(o)
	cfg.LogOutput = tb.Output()
	endpoint, release, err := launch(tb.Context(), cfg)
	if release != nil {
		tb.Cleanup(func() {
			if err := release(); err != nil {
				tb.Errorf("understudytest: %v", err)
			}
		})
	}
	if err != nil {
		tb.Fatalf("understudytest: %v", err)
	}
	client, err := admin.New(endpoint, admin.Options{})
	if err != nil {
		tb.Fatalf("understudytest: %v", err)
	}
	return &Instance{endpoint: endpoint, admin: client}
}

// launch serves cfg in the background and waits until it is ready. Once
// New succeeded, release is non-nil whatever the error: it stops the
// server, waits for Serve and closes what New opened, which a Serve that
// could not listen leaves open. It reports Serve's result unless launch
// already returned it.
func launch(ctx context.Context, cfg server.Config) (endpoint string, release func() error, err error) {
	ready := make(chan server.Endpoints, 1)
	cfg.OnReady = func(eps server.Endpoints) { ready <- eps }
	srv, err := server.New(cfg) //nolint:contextcheck // server.New takes no context: wiring does no I/O a caller could cancel
	if err != nil {
		return "", nil, fmt.Errorf("starting understudy: %w", err)
	}
	serving, stop := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- srv.Serve(serving) }()
	endpoint, err = awaitReady(serving, ready, done)
	var stopped *serveStoppedError
	returned := errors.As(err, &stopped)
	release = func() error { //nolint:contextcheck // Close takes no context: it releases files and memory
		stop()
		var errs []error
		if !returned {
			if err := <-done; err != nil {
				errs = append(errs, fmt.Errorf("understudy did not stop cleanly: %w", err))
			}
		}
		// After a graceful shutdown Close does nothing.
		if err := srv.Close(); err != nil {
			errs = append(errs, fmt.Errorf("releasing what understudy opened: %w", err))
		}
		return errors.Join(errs...)
	}
	return endpoint, release, err
}

// serverConfig is the server configuration o describes, always on an
// ephemeral loopback port, where the admin API needs no token.
func serverConfig(o Options) server.Config {
	cfg := server.Config{
		Listen:  netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 0),
		Seed:    o.Seed,
		Regions: o.Regions,
		Version: "understudytest",
	}
	switch c := o.Clock.(type) {
	case Frozen:
		cfg.ClockStart = server.ClockFrozen{At: c.At}
	case Running, nil:
		cfg.ClockStart = server.ClockRunning{}
	}
	switch d := o.Docker.(type) {
	case DockerDisabled:
		cfg.Docker = server.DockerDisabled{}
	case DockerSocket:
		cfg.Docker = server.DockerSocket{Path: d.Path}
	case nil:
		cfg.Docker = server.DockerSocket{}
	}
	switch o.DynamoDB.(type) {
	case DynamoDBDisabled:
		cfg.DynamoDB = server.DynamoDBDisabled{}
	case DynamoDBEnabled, nil:
		cfg.DynamoDB = server.DynamoDBEnabled{}
	}
	return cfg
}

// readyError reports a server that did not become ready.
type readyError struct{ reason string }

func (e *readyError) Error() string { return "understudy did not become ready: " + e.reason }

// serveStoppedError reports a Serve that returned before the server was
// ready; Err is what it returned, nil for a clean stop.
type serveStoppedError struct{ Err error }

func (e *serveStoppedError) Error() string {
	if e.Err == nil {
		return "understudy did not become ready: serve stopped without an error"
	}
	return "understudy did not become ready: serve stopped: " + e.Err.Error()
}

func (e *serveStoppedError) Unwrap() error { return e.Err }

// awaitReady returns the main endpoint once the server reports it ready,
// or a *serveStoppedError carrying what Serve returned when it stopped
// first.
func awaitReady(ctx context.Context, ready <-chan server.Endpoints, done <-chan error) (string, error) {
	deadline, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	select {
	case eps := <-ready:
		return eps.Main, nil
	case err := <-done:
		return "", &serveStoppedError{Err: err}
	case <-deadline.Done():
		return "", &readyError{reason: "its listener did not accept within " + readyTimeout.String()}
	}
}

// Endpoint is the instance's base URL, such as http://127.0.0.1:41234.
func (i *Instance) Endpoint() string { return i.endpoint }

// Admin is a client for the instance's admin API.
func (i *Instance) Admin() *admin.Client { return i.admin }

// settleTimeout bounds Settle.
const settleTimeout = 30 * time.Second

// Settle waits until no tracked work is pending, for at most 30 seconds or
// until ctx ends. When the 30 seconds pass it returns the *admin.Error
// with code condition_not_met whose Pending names the work outstanding.
func (i *Instance) Settle(ctx context.Context) error {
	_, err := i.admin.Settle(ctx, settleTimeout)
	return err
}

// Env is the environment that points an SDK at the instance with the test
// credentials, which resolve to account 000000000000.
func (i *Instance) Env() map[string]string {
	return map[string]string{
		"AWS_ENDPOINT_URL":      i.endpoint,
		"AWS_ACCESS_KEY_ID":     "test",
		"AWS_SECRET_ACCESS_KEY": "test",
		"AWS_REGION":            "us-east-1",
		"AWS_DEFAULT_REGION":    "us-east-1",
	}
}
