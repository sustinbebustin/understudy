package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
)

// conns follows one listener's connections through http.Server's
// ConnState hook, so shutdown can close the ones that never sent a request.
// http.Server.Shutdown counts such a connection as idle only once it is 5 s
// old, so a client's spare keep-alive dial would otherwise hold shutdown for
// the whole grace period.
type conns struct {
	log   *slog.Logger
	mu    sync.Mutex
	state map[net.Conn]http.ConnState
	// closing is set once shutdown has closed the unused connections; a
	// connection accepted after that is closed as it arrives.
	closing bool
}

func newConns(log *slog.Logger) *conns {
	return &conns{log: log, state: map[net.Conn]http.ConnState{}}
}

// track is the http.Server.ConnState hook.
func (c *conns) track(conn net.Conn, st http.ConnState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch st {
	case http.StateClosed, http.StateHijacked:
		delete(c.state, conn)
		return
	case http.StateNew:
		if c.closing {
			c.close(conn)
			return
		}
	case http.StateActive, http.StateIdle:
	}
	c.state[conn] = st
}

// closeUnused closes every connection that has not begun a request, and
// every later one as it is accepted. It is an http.Server.RegisterOnShutdown
// hook, which runs once the listeners are closed.
func (c *conns) closeUnused() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closing = true
	for conn, st := range c.state {
		if st == http.StateNew {
			c.close(conn)
		}
	}
}

// close closes a connection no request uses. A failure changes nothing for
// shutdown, which goes on either way, so it is only logged at debug level.
func (c *conns) close(conn net.Conn) {
	if err := conn.Close(); err != nil {
		c.log.LogAttrs(context.Background(), slog.LevelDebug, "closing an unused connection during shutdown failed",
			slog.String("remote", conn.RemoteAddr().String()), slog.String("error", err.Error()))
	}
}

// active is the number of connections with a request in progress.
func (c *conns) active() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, st := range c.state {
		if st == http.StateActive {
			n++
		}
	}
	return n
}
