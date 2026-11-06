package egress_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/egress"
)

// steadyPipe is a receiver's pipe end whose deadlines cannot be set. The
// receiver queues its writes (sendBuffer), and tls.Conn.Close sets the
// write deadline to now once its close_notify is queued (crypto/tls
// closeNotify), which would fail the queued answer itself.
type steadyPipe struct{ net.Conn }

func (steadyPipe) SetDeadline(time.Time) error      { return nil }
func (steadyPipe) SetWriteDeadline(time.Time) error { return nil }

// tlsNetwork serves one receiver over TLS at one address.
type tlsNetwork struct {
	addr netip.AddrPort
	r    *receiver
	cfg  *tls.Config
}

func (n *tlsNetwork) Dial(ctx context.Context, addr netip.AddrPort) (net.Conn, error) {
	if addr != n.addr {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: net.TCPAddrFromAddrPort(addr), Err: syscall.ECONNREFUSED}
	}
	client, server := net.Pipe()
	go n.r.serve(ctx, steadyPipe{server}, n.cfg)
	return client, nil
}

// https reaches a receiver whose certificate the roots trust, naming the
// URL's host in SNI (sns-http-delivery.md 3.1, 7.4).
func TestSendSpeaksVerifiedTLS(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cert, pool := selfSigned(t)
		r := answering(http.StatusOK)
		n := &tlsNetwork{
			addr: netip.MustParseAddrPort("203.0.113.10:8443"), r: r,
			cfg: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		}
		dns := resolver{"hooks.example.test": to("hooks.example.test", "203.0.113.10")}
		cl := newClient(t, egress.Options{Dialer: n, Resolver: dns, RootCAs: pool}, "hooks.example.test:8443")
		res, err := send(t, cl, "https://hooks.example.test:8443/hook", nil, "hello")
		if err != nil || res.Status != http.StatusOK {
			t.Fatalf("Send = %+v, %v; want 200", res, err)
		}
		got := r.requests()
		if len(got) != 1 || got[0].sni != "hooks.example.test" || string(got[0].body) != "hello" {
			t.Errorf("the receiver got %+v, want one request naming hooks.example.test in SNI", got)
		}
	})
}

// The URL's userinfo is never sent: not in the request line, the Host
// header or an Authorization header. A producer that authenticates to
// its endpoint (sns-http-delivery.md 3.1) writes the header itself.
func TestSendNeverSendsUserinfo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := answering(http.StatusOK)
		n := newNetwork(map[string]*receiver{"127.0.0.1:8080": r})
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}}, "127.0.0.1:8080")
		if _, err := send(t, cl, "http://alice:secret@127.0.0.1:8080/hook", nil, "hello"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		got := r.requests()
		if len(got) != 1 {
			t.Fatalf("the receiver got %d requests, want 1", len(got))
		}
		for _, leak := range []string{"alice", "secret", "Authorization"} {
			if strings.Contains(got[0].head, leak) {
				t.Errorf("request head\n%s\ncarries %q", got[0].head, leak)
			}
		}
	})
}

// rawNetwork answers every connection to addr with raw bytes once it has
// read a request.
type rawNetwork struct {
	addr   netip.AddrPort
	answer string
}

func (n rawNetwork) Dial(_ context.Context, addr netip.AddrPort) (net.Conn, error) {
	if addr != n.addr {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: net.TCPAddrFromAddrPort(addr), Err: syscall.ECONNREFUSED}
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			return
		}
		if _, err := io.Copy(io.Discard, req.Body); err != nil {
			return
		}
		if _, err := io.WriteString(server, n.answer); err != nil {
			return
		}
	}()
	return client, nil
}

// Interim 1xx answers precede the final one (RFC 9110 15.2): the final
// status is the attempt's answer, not the 103.
func TestSendSkipsInterimAnswers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := rawNetwork{
			addr:   netip.MustParseAddrPort("127.0.0.1:8080"),
			answer: "HTTP/1.1 103 Early Hints\r\nLink: </style.css>; rel=preload\r\n\r\nHTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n",
		}
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}}, "127.0.0.1:8080")
		res, err := send(t, cl, "http://127.0.0.1:8080/hook", nil, "hello")
		if err != nil || res.Status != http.StatusNoContent {
			t.Errorf("Send = %+v, %v; want the final 204", res, err)
		}
	})
}

// The checked addresses are dialed in order until one connects.
func TestSendDialsTheNextAddressWhenOneRefuses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := answering(http.StatusOK)
		n := newNetwork(map[string]*receiver{"203.0.113.11:80": r})
		dns := resolver{"hooks.example.test": to("hooks.example.test", "203.0.113.10", "203.0.113.11")}
		cl := newClient(t, egress.Options{Dialer: n, Resolver: dns}, "hooks.example.test:80")
		if res, err := send(t, cl, "http://hooks.example.test/hook", nil, "hello"); err != nil || res.Status != http.StatusOK {
			t.Fatalf("Send = %+v, %v; want 200 from the second address", res, err)
		}
		want := []netip.AddrPort{netip.MustParseAddrPort("203.0.113.10:80"), netip.MustParseAddrPort("203.0.113.11:80")}
		if d := n.dialed(); !slices.Equal(d, want) {
			t.Errorf("dialed %v, want %v", d, want)
		}
	})
}

// blockingResolver never answers before its context ends.
type blockingResolver struct{}

func (blockingResolver) Resolve(ctx context.Context, _ string) (egress.Resolution, error) {
	<-ctx.Done()
	return egress.Resolution{}, context.Cause(ctx)
}

// The deadline covers resolution as well as the exchange
// (sns-http-delivery.md 7.4); a name that does not resolve fails the
// exchange, which is not a refusal.
func TestSendResolutionFailures(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n := newNetwork(nil)
			cl := newClient(t, egress.Options{Dialer: n, Resolver: blockingResolver{}, Timeout: 2 * time.Second}, "hooks.example.test:80")
			start := time.Now()
			_, err := send(t, cl, "http://hooks.example.test/hook", nil, "hello")
			te, ok := errors.AsType[*egress.TransportError](err)
			if !ok || !te.Timeout || te.Host != "hooks.example.test" || te.Port != 80 {
				t.Fatalf("Send error = %v, want a timed-out *TransportError for hooks.example.test:80", err)
			}
			if elapsed := time.Since(start); elapsed != 2*time.Second {
				t.Errorf("Send gave up after %v, want 2s", elapsed)
			}
		})
	})
	t.Run("no such host", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n := newNetwork(nil)
			cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}}, "hooks.example.test:80")
			_, err := send(t, cl, "http://hooks.example.test/hook", nil, "hello")
			te, ok := errors.AsType[*egress.TransportError](err)
			if !ok || te.Timeout {
				t.Fatalf("Send error = %v, want a *TransportError that did not time out", err)
			}
			if _, ok := errors.AsType[*net.DNSError](err); !ok {
				t.Errorf("Send error = %v, want it to wrap the resolver's *net.DNSError", err)
			}
			if d := n.dialed(); len(d) != 0 {
				t.Errorf("dialed %v", d)
			}
		})
	})
}

// Each refusal says what to do next: the entry that lifts it, or that no
// setting does.
func TestRefusedErrorNamesTheNextStep(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		dns     resolver
		url     string
		want    []string
	}{
		{"unlisted", nil, nil, "http://127.0.0.1:8080/", []string{`add "127.0.0.1:8080" or "127.0.0.1:*" to sns.httpDelivery.allow`, "nothing was sent"}},
		{"unlisted IPv6", nil, nil, "http://[::1]:8080/", []string{`add "[::1]:8080" or "[::1]:*" to sns.httpDelivery.allow`}},
		{
			"a name resolving to loopback",
			[]string{"receiver.example.test:8080"},
			resolver{"receiver.example.test": to("receiver.example.test", "127.0.0.1")},
			"http://receiver.example.test:8080/",
			[]string{"loopback address 127.0.0.1", `add "127.0.0.1:8080" or "127.0.0.1:*" to sns.httpDelivery.allow`},
		},
		{"an AWS endpoint", nil, nil, "https://sqs.us-east-1.amazonaws.com/", []string{"AWS endpoint", "no setting allows it"}},
		{"the metadata address", nil, nil, "http://169.254.169.254/", []string{"instance metadata address", "no setting allows it"}},
		{"a host no entry can name", nil, nil, "http://under_score.example.test/", []string{"no entry of sns.httpDelivery.allow can allow it"}},
		{"a port no entry can name", []string{"127.0.0.1:*"}, nil, "http://127.0.0.1:99999/", []string{"port outside 1-65535"}},
		{"another scheme", nil, nil, "ftp://127.0.0.1/", []string{"not http or https"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n := newNetwork(nil)
				var dns egress.Resolver = resolver{}
				if c.dns != nil {
					dns = c.dns
				}
				cl := newClient(t, egress.Options{Dialer: n, Resolver: dns}, c.entries...)
				_, err := send(t, cl, c.url, nil, "hello")
				re, ok := errors.AsType[*egress.RefusedError](err)
				if !ok {
					t.Fatalf("Send error = %v, want a *RefusedError", err)
				}
				for _, w := range c.want {
					if !strings.Contains(re.Error(), w) {
						t.Errorf("error %q does not contain %q", re.Error(), w)
					}
				}
			})
		})
	}
}

// A request with no URL has no scheme to send by.
func TestSendRefusesAMissingURL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cl := newClient(t, egress.Options{Dialer: newNetwork(nil), Resolver: resolver{}})
		_, err := cl.Send(t.Context(), egress.Request{Method: http.MethodPost})
		if re, ok := errors.AsType[*egress.RefusedError](err); !ok || re.Refusal != egress.UnsupportedScheme {
			t.Errorf("Send without a URL = %v, want an UnsupportedScheme refusal", err)
		}
	})
}

// A listener address given IPv4-mapped is refused as its IPv4 form.
func TestSendRefusesMappedListenerAddresses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newNetwork(map[string]*receiver{"127.0.0.1:4566": answering(http.StatusOK)})
		refuse := []netip.AddrPort{netip.MustParseAddrPort("[::ffff:127.0.0.1]:4566")}
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}, Refuse: refuse}, "127.0.0.1:*")
		_, err := send(t, cl, "http://127.0.0.1:4566/", nil, "")
		if re, ok := errors.AsType[*egress.RefusedError](err); !ok || re.Refusal != egress.OwnListener {
			t.Errorf("Send = %v, want an OwnListener refusal", err)
		}
		if d := n.dialed(); len(d) != 0 {
			t.Errorf("dialed %v", d)
		}
	})
}

// The DNS-name grammar takes LDH labels only, and a name whose last label
// is numeric is an IPv4 address in a form netip does not parse (7.2).
func TestParseAllowListRefusesNumericNames(t *testing.T) {
	for _, entry := range []string{"127.1:80", "10.0.1:80", "a..example.test:80", strings.Repeat("a", 64) + ".test:80"} {
		if _, err := egress.ParseAllowList(setting, []string{entry}); err == nil {
			t.Errorf("ParseAllowList(%q) accepted it", entry)
		}
	}
}
