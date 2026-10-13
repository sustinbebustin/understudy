package edge_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// pipeListener hands the server the far ends of in-memory pipes, whose
// deadlines run on the synctest bubble's clock.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (*pipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "pipe", Net: "pipe"} }

// servePipe serves admin behind an edge handler's own http.Server and
// returns a client connection to it, and a stop that closes both.
func servePipe(t *testing.T, admin http.Handler) (client net.Conn, stop func()) {
	t.Helper()
	h, err := edge.New(edge.Options{Kernel: kernelOf(t), Registry: mustRegistry(t), Listener: ref.ListenerMain, Admin: admin})
	if err != nil {
		t.Fatal(err)
	}
	srv := h.Server(nil)
	l := &pipeListener{conns: make(chan net.Conn, 1), done: make(chan struct{})}
	client, server := net.Pipe()
	l.conns <- server
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()
	return client, func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve = %v, want http.ErrServerClosed", err)
		}
		// net/http lingers up to 500 ms on a connection it closes; the
		// bubble ends only once every connection goroutine has.
		time.Sleep(time.Second)
		synctest.Wait()
	}
}

// bodyRead is what an admin handler saw of a request body and its context.
type bodyRead struct {
	body string
	err  error
}

// A client that stops sending in the middle of a body frees its handler
// and connection after a minute instead of holding them forever.
func TestAStalledRequestBodyTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got := make(chan bodyRead, 1)
		client, stop := servePipe(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			got <- bodyRead{string(b), err}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer stop()
		go func() {
			if _, err := io.WriteString(client, "POST /_understudy/v1/reset HTTP/1.1\r\nHost: localhost\r\nContent-Length: 10\r\n\r\nab"); err != nil {
				t.Error(err)
			}
		}()
		time.Sleep(time.Minute + time.Second)
		synctest.Wait()
		select {
		case read := <-got:
			if !errors.Is(read.err, os.ErrDeadlineExceeded) {
				t.Errorf("reading a body stalled for a minute = %q, %v; want a deadline error", read.body, read.err)
			}
		default:
			t.Fatal("the handler is still reading a body stalled for a minute")
		}
	})
}

// A body that keeps arriving is never cut, however long it takes in all,
// and once it is read a long poll is not cut either.
func TestASlowBodyAndALongPollAfterItComplete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got := make(chan bodyRead, 1)
		client, stop := servePipe(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			// A decoder checking for trailing bytes reads past EOF.
			if n, again := r.Body.Read(make([]byte, 1)); n != 0 || !errors.Is(again, io.EOF) {
				err = errors.Join(err, fmt.Errorf("a read past EOF = %d, %w; want 0, EOF", n, again))
			}
			time.Sleep(90 * time.Second)
			got <- bodyRead{string(b), errors.Join(err, r.Context().Err())}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer stop()
		go func() {
			if _, err := io.WriteString(client, "POST /_understudy/v1/reset HTTP/1.1\r\nHost: localhost\r\nContent-Length: 3\r\n\r\n"); err != nil {
				t.Error(err)
				return
			}
			for _, b := range []string{"a", "b", "c"} {
				time.Sleep(50 * time.Second)
				if _, err := io.WriteString(client, b); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		res, err := http.ReadResponse(bufio.NewReader(client), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
		if read := <-got; read.body != "abc" || read.err != nil || res.StatusCode != http.StatusNoContent {
			t.Errorf("a 150 s body then a 90 s poll = %q, %v, status %d; want \"abc\", no error, 204", read.body, read.err, res.StatusCode)
		}
	})
}

// A keep-alive connection left idle between requests is closed after two
// minutes, so idle clients cannot pin connections forever.
func TestIdleConnectionsClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, stop := servePipe(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer stop()
		go func() {
			if _, err := io.WriteString(client, "GET /_understudy/v1/health HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
				t.Error(err)
			}
		}()
		conn := bufio.NewReader(client)
		res, err := http.ReadResponse(conn, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
		closed := make(chan error, 1)
		go func() {
			_, err := conn.ReadByte()
			closed <- err
		}()
		time.Sleep(2*time.Minute + time.Second)
		synctest.Wait()
		select {
		case err := <-closed:
			if !errors.Is(err, io.EOF) {
				t.Errorf("reading an idle connection = %v, want EOF", err)
			}
		default:
			t.Error("a connection idle for two minutes is still open")
		}
	})
}

// net/http's own diagnostics, such as a failed TLS handshake, reach the
// configured logger in its format instead of the standard library's log
// on stderr.
func TestNetHTTPDiagnosticsReachTheLogger(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h, err := edge.New(edge.Options{Kernel: kernelOf(t), Registry: mustRegistry(t), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	h.Server(nil).ErrorLog.Print("http: TLS handshake error from 127.0.0.1:1: remote error: tls: bad certificate")
	if got := buf.String(); !strings.Contains(got, `"level":"WARN"`) || !strings.Contains(got, "TLS handshake error") {
		t.Errorf("logged %q, want one JSON WARN line carrying the net/http message", got)
	}
}
