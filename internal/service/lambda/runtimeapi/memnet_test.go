package runtimeapi_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
)

// memNet is an in-memory network of net.Pipe connections. A goroutine
// blocked on a pipe is durably blocked in a synctest bubble, unlike one in
// a socket read, so the bubble's fake clock can advance while the runtime
// and the Runtime API wait on each other. Create it inside the bubble.
type memNet struct {
	mu        sync.Mutex
	listeners map[int]*memListener
	lastPort  int
}

func newMemNet() *memNet {
	return &memNet{listeners: map[int]*memListener{}, lastPort: 40000}
}

// listen opens a listener on address's host and the next free port.
func (n *memNet) listen(_ context.Context, _, address string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lastPort++
	l := &memListener{
		net:   n,
		addr:  &net.TCPAddr{IP: net.ParseIP(host), Port: n.lastPort},
		conns: make(chan net.Conn),
		done:  make(chan struct{}),
	}
	n.listeners[l.addr.Port] = l
	return l, nil
}

// dial connects to the listener on address's port.
func (n *memNet) dial(ctx context.Context, _, address string) (net.Conn, error) {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}
	n.mu.Lock()
	l, ok := n.listeners[port]
	n.mu.Unlock()
	refused := fmt.Errorf("dial %s: connection refused: no in-memory listener", address)
	if !ok {
		return nil, refused
	}
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		return nil, errors.Join(refused, client.Close(), server.Close())
	case <-ctx.Done():
		return nil, errors.Join(ctx.Err(), client.Close(), server.Close())
	}
}

type memListener struct {
	net   *memNet
	addr  *net.TCPAddr
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error {
	l.once.Do(func() {
		close(l.done)
		l.net.mu.Lock()
		delete(l.net.listeners, l.addr.Port)
		l.net.mu.Unlock()
	})
	return nil
}

func (l *memListener) Addr() net.Addr { return l.addr }
