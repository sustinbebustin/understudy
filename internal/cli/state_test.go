package cli_test

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

// silentEndpoint is an endpoint whose listener accepts connections and
// never answers, as a wedged process on the port would.
func silentEndpoint(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Errorf("closing the silent listener: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			if err := conn.Close(); err != nil {
				t.Errorf("closing a held connection: %v", err)
			}
		}
	})
	return "http://" + ln.Addr().String()
}

// health --wait bounds every attempt by the wait, so against a listener
// that never answers it ends as unreachable once the wait has passed,
// saying no answer came.
func TestHealthWaitEndsAgainstASilentListener(t *testing.T) {
	endpoint := silentEndpoint(t)
	started := time.Now()
	r := runCLI(t.Context(), endpoint, nil, "health", "--wait", "300ms")
	took := time.Since(started)
	if r.code != admin.ExitUnreachable || !strings.Contains(r.stderr, "no answer came in time") {
		t.Errorf("health --wait 300ms against a silent listener = %d, stderr %q; want 3 saying it sent no answer", r.code, r.stderr)
	}
	if took > 3*time.Second {
		t.Errorf("health --wait 300ms against a silent listener took %s; want about 300ms", took)
	}
}
