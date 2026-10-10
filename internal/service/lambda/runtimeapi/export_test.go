package runtimeapi

import (
	"context"
	"net"
)

// SetListen replaces how s opens environment listeners. Call it before the
// first Open.
func SetListen(s *Server, listen func(ctx context.Context, network, address string) (net.Listener, error)) {
	s.listen = listen
}
