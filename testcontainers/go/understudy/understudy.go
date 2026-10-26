package understudy

import (
	"context"
	"errors"
	"time"

	"github.com/testcontainers/testcontainers-go"

	"github.com/sustinbebustin/understudy/admin"
)

// DefaultImage is the release image of the understudy version this module
// is released with, in lockstep (testcontainers.md 8.8). Run takes the image
// explicitly, by tc-go's convention (testcontainers.md 4.1).
const DefaultImage = "ghcr.io/sustinbebustin/understudy:0.0.0"

// errNotImplemented is what every stub returns until the module is built.
var errNotImplemented = errors.New("understudy: not implemented yet")

// Container is a started understudy container. Terminate, inherited from
// testcontainers.Container, stops it with tc-go's 10 s grace, which lets
// understudy remove its own containers before it exits (testcontainers.md
// 8.6).
type Container struct {
	testcontainers.Container
}

// Run starts img with understudy's module settings and the options given,
// module options (Option) and tc-go's own customizers alike, and returns
// once understudy reports ready.
//
// Not implemented yet.
func Run(ctx context.Context, img string, opts ...testcontainers.ContainerCustomizer) (*Container, error) {
	_, _, _ = ctx, img, opts
	return nil, errNotImplemented
}

// Endpoint is http://<host>:<mapped port of 4566/tcp>, with a host of
// localhost reported as 127.0.0.1 (testcontainers.md 8.2).
//
// Not implemented yet.
func (c *Container) Endpoint(ctx context.Context) (string, error) {
	_ = ctx
	return "", errNotImplemented
}

// Env is the environment that points an SDK at the container, with the
// names and values of understudytest's Instance.Env (testcontainers.md 3.8).
//
// Not implemented yet.
func (c *Container) Env(ctx context.Context) (map[string]string, error) {
	_ = ctx
	return nil, errNotImplemented
}

// Admin returns an admin client for the container, sending the admin token
// when WithAdminToken set one.
//
// Not implemented yet.
func (c *Container) Admin(ctx context.Context) (*admin.Client, error) {
	_ = ctx
	return nil, errNotImplemented
}

// Settle waits up to timeout for understudy's tracked work to finish
// (GET /_understudy/v1/settle).
//
// Not implemented yet.
func (c *Container) Settle(ctx context.Context, timeout time.Duration) error {
	_, _ = ctx, timeout
	return errNotImplemented
}

// AdvanceClock moves the emulator clock forward by d
// (POST /_understudy/v1/clock, op advance).
//
// Not implemented yet.
func (c *Container) AdvanceClock(ctx context.Context, d time.Duration) error {
	_, _ = ctx, d
	return errNotImplemented
}

// Reset drops all service state, keeping the clock and the fault rules
// (POST /_understudy/v1/reset with an empty request).
//
// Not implemented yet.
func (c *Container) Reset(ctx context.Context) error {
	_ = ctx
	return errNotImplemented
}
