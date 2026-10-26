package understudy

import (
	"net/netip"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

// Option is a module setting. It is a testcontainers.ContainerCustomizer
// whose Customize does nothing, so it mixes with tc-go's own options in
// Run's variadic list; Run reads it before building the request.
type Option func(*options) error

// Customize does nothing: Run applies an Option itself.
func (Option) Customize(*testcontainers.GenericContainerRequest) error { return nil }

var _ testcontainers.ContainerCustomizer = Option(nil)

// options are the module settings Run collects from its Option values.
// Not populated yet.
type options struct{}

func notImplemented(*options) error { return errNotImplemented }

// WithDockerSocket mounts the daemon's Docker socket at
// /var/run/docker.sock and adds its numeric group, so Lambda and DynamoDB
// Local run (testcontainers.md 8.5).
//
// Not implemented yet.
func WithDockerSocket() Option { return notImplemented }

// WithDockerSocketGroup is WithDockerSocket with gid, the numeric group
// owning the socket on the daemon's host, instead of the one read locally.
//
// Not implemented yet.
func WithDockerSocketGroup(gid uint32) Option {
	_ = gid
	return notImplemented
}

// Clock is how the emulator clock starts.
//
//sumtype:decl
type Clock interface{ isClock() }

// Running follows wall time.
type Running struct{}

// Frozen holds emulator time still until it is advanced; a zero At freezes
// at wall time at start.
type Frozen struct{ At time.Time }

func (Running) isClock() {}
func (Frozen) isClock()  {}

// WithClock sets UNDERSTUDY_CLOCK.
//
// Not implemented yet.
func WithClock(c Clock) Option {
	_ = c
	return notImplemented
}

// WithClockScale sets UNDERSTUDY_CLOCK_SCALE.
//
// Not implemented yet.
func WithClockScale(factor float64) Option {
	_ = factor
	return notImplemented
}

// WithSeed sets UNDERSTUDY_SEED.
//
// Not implemented yet.
func WithSeed(seed uint64) Option {
	_ = seed
	return notImplemented
}

// WithSigV4Verify sets UNDERSTUDY_SIGV4=verify.
//
// Not implemented yet.
func WithSigV4Verify() Option { return notImplemented }

// WithSnapshot copies the snapshot archive at path into the container and
// sets UNDERSTUDY_LOAD_SNAPSHOT to it.
//
// Not implemented yet.
func WithSnapshot(path string) Option {
	_ = path
	return notImplemented
}

// WithAdminToken sets a random admin token and
// UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API=true; the wait and the helpers
// send it (testcontainers.md 8.2).
//
// Not implemented yet.
func WithAdminToken() Option { return notImplemented }

// WithPublishedOn publishes port 4566 on addr instead of 127.0.0.1; an
// address beyond loopback needs WithAdminToken (testcontainers.md 8.4).
//
// Not implemented yet.
func WithPublishedOn(addr netip.Addr) Option {
	_ = addr
	return notImplemented
}
