package understudy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
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

// options are the module settings Run collects from its Option values. Run
// checks them only once every Option is applied, so option order never
// changes the outcome (testcontainers.md 9.5).
type options struct {
	// env holds the understudy variables that an option sets as is.
	env map[string]string
	// token is the admin token, empty without WithAdminToken.
	token string
	// publish is the address port 4566 is published on; the zero Addr
	// means 127.0.0.1 (testcontainers.md 8.4).
	publish netip.Addr
	// socket says where the Docker socket's group comes from; nil leaves
	// the socket unmounted.
	socket socketGroup
	// snapshot is the host path of the archive loaded at start, empty for
	// none.
	snapshot string
}

func newOptions() options {
	return options{env: map[string]string{}}
}

// socketGroup is where the group added for the mounted Docker socket comes
// from.
//
//sumtype:decl
type socketGroup interface{ isSocketGroup() }

// localGroup reads the group of the local unix socket tc-go's client uses
// (testcontainers.md 8.5).
type localGroup struct{}

// givenGroup is the group WithDockerSocketGroup names.
type givenGroup struct{ gid uint32 }

func (localGroup) isSocketGroup() {}
func (givenGroup) isSocketGroup() {}

// WithDockerSocket mounts the daemon's Docker socket at
// /var/run/docker.sock and adds its numeric group, so Lambda and DynamoDB
// Local run (testcontainers.md 8.5). The group is read from the socket the
// tc-go client reaches over a local unix socket; with any other daemon Run
// refuses with a *SocketGroupError, and WithDockerSocketGroup names it.
// After start, Run fails with a *DockerUnavailableError unless understudy
// reports Docker enabled.
func WithDockerSocket() Option {
	return func(o *options) error {
		if o.socket == nil {
			o.socket = localGroup{}
		}
		return nil
	}
}

// WithDockerSocketGroup is WithDockerSocket with gid, the numeric group
// owning the socket on the daemon's host, instead of the one read locally.
// A name cannot be given: the image has no /etc/group to resolve it
// (testcontainers.md 8.5).
func WithDockerSocketGroup(gid uint32) Option {
	return func(o *options) error {
		o.socket = givenGroup{gid: gid}
		return nil
	}
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

// WithClock sets UNDERSTUDY_CLOCK: running, frozen, or frozen@<At> in RFC
// 3339 (testcontainers.md 8.2).
func WithClock(c Clock) Option {
	return func(o *options) error {
		switch c := c.(type) {
		case Running:
			o.env["UNDERSTUDY_CLOCK"] = "running"
		case Frozen:
			if c.At.IsZero() {
				o.env["UNDERSTUDY_CLOCK"] = "frozen"
			} else {
				o.env["UNDERSTUDY_CLOCK"] = "frozen@" + c.At.Format(time.RFC3339Nano)
			}
		case nil:
			return errors.New("understudy: WithClock got a nil Clock; pass understudy.Running{} or understudy.Frozen{At: ...}")
		}
		return nil
	}
}

// WithClockScale sets UNDERSTUDY_CLOCK_SCALE. understudy checks the range
// itself, and with a frozen clock it refuses to start; Run then reports the
// container's exit (testcontainers.md 9.5).
func WithClockScale(factor float64) Option {
	return func(o *options) error {
		o.env["UNDERSTUDY_CLOCK_SCALE"] = strconv.FormatFloat(factor, 'g', -1, 64)
		return nil
	}
}

// WithSeed sets UNDERSTUDY_SEED.
func WithSeed(seed uint64) Option {
	return func(o *options) error {
		o.env["UNDERSTUDY_SEED"] = strconv.FormatUint(seed, 10)
		return nil
	}
}

// WithSigV4Verify sets UNDERSTUDY_SIGV4=verify.
func WithSigV4Verify() Option {
	return func(o *options) error {
		o.env["UNDERSTUDY_SIGV4"] = "verify"
		return nil
	}
}

// snapshotPath is where WithSnapshot copies the archive in the container.
const snapshotPath = "/snapshot/archive"

// WithSnapshot copies the snapshot archive at path into the container and
// sets UNDERSTUDY_LOAD_SNAPSHOT to it. A path that cannot be stat'ed fails
// Run with an error wrapping the *fs.PathError, before any container
// exists.
func WithSnapshot(path string) Option {
	return func(o *options) error {
		fi, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("understudy: WithSnapshot cannot read the archive: %w; pass the path of a snapshot archive, such as one admin.Client.SaveSnapshotTo wrote", err)
		}
		if fi.IsDir() {
			return fmt.Errorf("understudy: WithSnapshot(%q) names a directory; pass the archive file, such as one admin.Client.SaveSnapshotTo wrote", path)
		}
		o.snapshot = path
		return nil
	}
}

// WithAdminToken sets a random admin token and
// UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API=true; the wait and the helpers
// send it (testcontainers.md 8.2). Health then answers 401 without it.
func WithAdminToken() Option {
	return func(o *options) error {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return fmt.Errorf("understudy: WithAdminToken cannot draw a random token: %w", err)
		}
		o.token = hex.EncodeToString(secret)
		return nil
	}
}

// WithPublishedOn publishes port 4566 on addr instead of 127.0.0.1; an
// address beyond loopback needs WithAdminToken, or Run refuses with an
// *ExposureError (testcontainers.md 8.4).
func WithPublishedOn(addr netip.Addr) Option {
	return func(o *options) error {
		if !addr.IsValid() {
			return errors.New("understudy: WithPublishedOn got the zero netip.Addr; pass an address such as netip.MustParseAddr(\"127.0.0.1\")")
		}
		o.publish = addr
		return nil
	}
}

// exposure refuses publishing beyond loopback without the admin token,
// since the image's admin API has no token otherwise (testcontainers.md
// 8.4, 9.5).
func (o *options) exposure() error {
	if o.publish.IsValid() && !o.publish.IsLoopback() && o.token == "" {
		return &ExposureError{Addr: o.publish}
	}
	return nil
}
