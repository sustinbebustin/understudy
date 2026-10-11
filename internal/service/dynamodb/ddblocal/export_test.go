package ddblocal

import (
	"context"
	"net"
)

// ErrStopped is what Start's waiters get when Stop ends the start.
var ErrStopped = errStopped

// SetDial replaces how m connects to the container, whatever address the
// daemon reports, so a fake daemon's container can answer through an
// in-process server. Call it before Start.
func SetDial(m *Manager, dial func(ctx context.Context) (net.Conn, error)) {
	m.transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) }
}
