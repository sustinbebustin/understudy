package egress_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/egress"
)

// response is what a receiver answers.
type response struct {
	status int
	header http.Header
	body   []byte
}

// received is one request a receiver read.
type received struct {
	head string // the request line and header block as sent
	req  *http.Request
	body []byte
	sni  string
	// taken is how many response bytes, head and body, the client read
	// before it hung up.
	taken int64
}

// receiver is an HTTP/1.1 endpoint on the in-memory network. answer is
// called per request; a nil answer holds the request until the client
// hangs up.
type receiver struct {
	answer func(*http.Request) response

	mu  sync.Mutex
	got []received
}

func answering(status int) *receiver {
	return &receiver{answer: func(*http.Request) response { return response{status: status} }}
}

func (r *receiver) requests() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

func (r *receiver) record(x received) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, x)
}

// sendBuffer is a receiver's end of the pipe with its writes queued, as a
// socket's send buffer holds them, so a receiver never blocks writing while
// the client writes too, such as a TLS client's alert against the server's
// handshake flight, which an unbuffered net.Pipe deadlocks on.
type sendBuffer struct {
	net.Conn
	queue chan []byte
	done  chan struct{}
	once  sync.Once
	taken atomic.Int64
}

func buffered(c net.Conn) *sendBuffer {
	b := &sendBuffer{Conn: c, queue: make(chan []byte, 1024), done: make(chan struct{})}
	go b.drain()
	return b
}

func (b *sendBuffer) Write(p []byte) (int, error) {
	b.queue <- bytes.Clone(p)
	return len(p), nil
}

// Close sends what is queued, until the client takes it or hangs up, and
// then closes the pipe.
func (b *sendBuffer) Close() error {
	b.once.Do(func() { close(b.queue) })
	<-b.done
	return b.Conn.Close()
}

// drain writes the queue to the pipe in order, counting what the client
// takes, and discards the rest once the client hangs up.
func (b *sendBuffer) drain() {
	defer close(b.done)
	hungUp := false
	for p := range b.queue {
		if hungUp {
			continue
		}
		n, err := b.Conn.Write(p)
		b.taken.Add(int64(n))
		hungUp = err != nil
	}
}

// serve answers one request on pipe, over TLS with cfg when it is set. ctx
// is the dial's, which net/http may cancel once the connection is up.
func (r *receiver) serve(ctx context.Context, pipe net.Conn, cfg *tls.Config) {
	out := buffered(pipe)
	var conn net.Conn = out
	defer conn.Close()
	var sni string
	if cfg != nil {
		tc := tls.Server(conn, cfg)
		if err := tc.HandshakeContext(context.WithoutCancel(ctx)); err != nil {
			return
		}
		sni = tc.ConnectionState().ServerName
		conn = tc
	}
	var raw bytes.Buffer
	req, err := http.ReadRequest(bufio.NewReader(io.TeeReader(conn, &raw)))
	if err != nil {
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return
	}
	head, _, _ := strings.Cut(raw.String(), "\r\n\r\n")
	x := received{head: head, req: req, body: body, sni: sni}
	if r.answer == nil {
		// Hold the request until the client hangs up, which ends the copy.
		r.record(x)
		if _, err := io.Copy(io.Discard, conn); err != nil {
			return
		}
		return
	}
	res := r.answer(req)
	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", res.status, http.StatusText(res.status))
	if err := res.header.Write(&b); err != nil {
		return
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\nConnection: close\r\n\r\n", len(res.body))
	b.Write(res.body)
	if _, err := conn.Write(b.Bytes()); err != nil {
		return
	}
	// Closing waits until the client took the answer or hung up.
	if err := conn.Close(); err != nil {
		return
	}
	x.taken = out.taken.Load()
	r.record(x)
}

// network is the dial seam: every listening address is a receiver, and any
// other address refuses the connection. It records every address dialed.
type network struct {
	tls *tls.Config // receivers speak TLS with it when set

	mu        sync.Mutex
	receivers map[netip.AddrPort]*receiver
	dials     []netip.AddrPort
}

func newNetwork(receivers map[string]*receiver) *network {
	n := &network{receivers: map[netip.AddrPort]*receiver{}}
	for addr, r := range receivers {
		n.receivers[netip.MustParseAddrPort(addr)] = r
	}
	return n
}

func (n *network) Dial(ctx context.Context, addr netip.AddrPort) (net.Conn, error) {
	n.mu.Lock()
	n.dials = append(n.dials, addr)
	r, ok := n.receivers[addr]
	n.mu.Unlock()
	if !ok {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: net.TCPAddrFromAddrPort(addr), Err: syscall.ECONNREFUSED}
	}
	client, server := net.Pipe()
	go r.serve(ctx, server, n.tls)
	return client, nil
}

func (n *network) dialed() []netip.AddrPort {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.dials)
}

// resolver answers names from a fixed table; any other name does not
// resolve.
type resolver map[string]egress.Resolution

func (r resolver) Resolve(_ context.Context, host string) (egress.Resolution, error) {
	res, ok := r[host]
	if !ok {
		return egress.Resolution{}, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return res, nil
}

// to resolves a name with no CNAMEs to addrs.
func to(name string, addrs ...string) egress.Resolution {
	res := egress.Resolution{Chain: []string{name}}
	for _, a := range addrs {
		res.Addrs = append(res.Addrs, netip.MustParseAddr(a))
	}
	return res
}

// newClient builds a client over n and r with the allow-list entries, on
// an emulator clock frozen at the bubble's start: a delivery's deadline is
// wall time (ADR 0006, sns-http-delivery.md 7.4), which freezing cannot
// stretch.
func newClient(t *testing.T, o egress.Options, entries ...string) *egress.Client {
	t.Helper()
	allow, perr := egress.ParseAllowList(setting, entries)
	if perr != nil {
		t.Fatalf("ParseAllowList(%q): %v", entries, perr)
	}
	o.Allow = allow
	if o.Clock == nil {
		clk, err := clock.New(clock.Options{Mode: clock.Frozen{}})
		if err != nil {
			t.Fatal(err)
		}
		o.Clock = clk
	}
	c, oerr := egress.New(o)
	if oerr != nil {
		t.Fatalf("New: %v", oerr)
	}
	return c
}

// send POSTs body to rawURL and waits for the receivers to finish.
func send(t *testing.T, c *egress.Client, rawURL string, header http.Header, body string) (egress.Response, error) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Send(t.Context(), egress.Request{Method: http.MethodPost, URL: u, Header: header, Body: []byte(body)})
	synctest.Wait()
	return res, err
}

// wantRefused checks that err is the refusal want of host:port (and name,
// when the refused chain name or address is not the host), that nothing was
// dialed, and that the error names the host.
func wantRefused(t *testing.T, n *network, err error, want egress.Refusal, host string, port uint16, name string) *egress.RefusedError {
	t.Helper()
	var re *egress.RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("Send error = %v, want a *RefusedError %v", err, want)
	}
	if re.Refusal != want || re.Host != host || re.Port != port || re.Name != name || re.Setting != setting {
		t.Errorf("refusal = %+v, want %v of %s:%d (name %q) under %v", *re, want, host, port, name, setting)
	}
	if d := n.dialed(); len(d) != 0 {
		t.Errorf("dialed %v; a refused request connects nowhere", d)
	}
	msg := re.Error()
	if !strings.Contains(msg, host) || (name != "" && !strings.Contains(msg, name)) {
		t.Errorf("error %q does not name %s and %q", msg, host, name)
	}
	return re
}

// The refusal matrix of sns-http-delivery.md 7.2-7.4: only listed hosts
// are reached, loopback and private addresses only when listed, and AWS
// names (in the URL or anywhere in its CNAME chain), the metadata, link-
// local, unspecified and multicast addresses and understudy's own
// listeners never, whatever the list says.
func TestSendRefusals(t *testing.T) {
	cname := func(addr string, chain ...string) egress.Resolution {
		return egress.Resolution{Chain: chain, Addrs: []netip.Addr{netip.MustParseAddr(addr)}}
	}
	cases := []struct {
		name    string
		entries []string
		dns     resolver
		refuse  []string
		url     string
		want    egress.Refusal
		host    string
		port    uint16
		refused string
	}{
		// 7.1, 7.2: loopback is not a safe default; nothing unlisted is reached.
		{"loopback unlisted", nil, nil, nil, "http://127.0.0.1:8080/hook", egress.NotAllowed, "127.0.0.1", 8080, ""},
		{"IPv6 loopback unlisted", nil, nil, nil, "http://[::1]:8080/hook", egress.NotAllowed, "::1", 8080, ""},
		{"localhost unlisted", nil, resolver{"localhost": to("localhost", "127.0.0.1")}, nil, "http://localhost:8080/", egress.NotAllowed, "localhost", 8080, ""},
		{"another port of a listed host", []string{"127.0.0.1:8080"}, nil, nil, "http://127.0.0.1:8081/", egress.NotAllowed, "127.0.0.1", 8081, ""},
		// 7.2: the URL's port, or 80 for http and 443 for https.
		{"http's default port", []string{"hooks.example.test:8080"}, nil, nil, "http://hooks.example.test/", egress.NotAllowed, "hooks.example.test", 80, ""},
		{"https's default port", []string{"hooks.example.test:80"}, nil, nil, "https://hooks.example.test/", egress.NotAllowed, "hooks.example.test", 443, ""},
		// 7.2: no host wildcards.
		{"a subdomain of a listed name", []string{"example.test:80"}, nil, nil, "http://hooks.example.test/", egress.NotAllowed, "hooks.example.test", 80, ""},
		// Spec S4: loopback only when listed, private refused unless listed;
		// a DNS-name entry lists the name, not what it resolves to.
		{
			"a listed name resolving to loopback",
			[]string{"receiver.example.test:8080"},
			resolver{"receiver.example.test": to("receiver.example.test", "127.0.0.1")},
			nil,
			"http://receiver.example.test:8080/", egress.UnlistedLoopback, "receiver.example.test", 8080, "127.0.0.1",
		},
		{
			"a listed name resolving to a private address",
			[]string{"receiver.example.test:8080"},
			resolver{"receiver.example.test": to("receiver.example.test", "10.0.0.7")},
			nil,
			"http://receiver.example.test:8080/", egress.UnlistedPrivate, "receiver.example.test", 8080, "10.0.0.7",
		},
		// 7.4: an entry named localhost must connect to a loopback address.
		{
			"localhost resolving off the machine",
			[]string{"localhost:8080"},
			resolver{"localhost": to("localhost", "10.0.0.7")},
			nil,
			"http://localhost:8080/", egress.AddressMismatch, "localhost", 8080, "10.0.0.7",
		},
		// 7.3: AWS names, never allowed; refused as such even when unlisted.
		{"an AWS endpoint", nil, nil, nil, "https://sqs.us-east-1.amazonaws.com/", egress.AWSEndpoint, "sqs.us-east-1.amazonaws.com", 443, ""},
		{"an AWS endpoint in capitals", nil, nil, nil, "https://SQS.US-EAST-1.AMAZONAWS.COM./", egress.AWSEndpoint, "sqs.us-east-1.amazonaws.com", 443, ""},
		{"a function URL on on.aws", nil, nil, nil, "https://abc123.lambda-url.us-east-1.on.aws/", egress.AWSEndpoint, "abc123.lambda-url.us-east-1.on.aws", 443, ""},
		{"execute-api.com", nil, nil, nil, "https://petstore-demo-endpoint.execute-api.com/petstore", egress.AWSEndpoint, "petstore-demo-endpoint.execute-api.com", 443, ""},
		{
			"an AWS name through a CNAME chain",
			[]string{"hooks.example.test:443"},
			resolver{"hooks.example.test": cname("203.0.113.10", "hooks.example.test", "edge.cdn.example.test", "abc123.execute-api.us-east-1.amazonaws.com")},
			nil,
			"https://hooks.example.test/", egress.AWSEndpoint, "hooks.example.test", 443, "abc123.execute-api.us-east-1.amazonaws.com",
		},
		{
			"on.aws through a CNAME",
			[]string{"hooks.example.test:443"},
			resolver{"hooks.example.test": cname("203.0.113.10", "hooks.example.test", "abc123.lambda-url.us-east-1.on.aws")},
			nil,
			"https://hooks.example.test/", egress.AWSEndpoint, "hooks.example.test", 443, "abc123.lambda-url.us-east-1.on.aws",
		},
		// 7.3: the metadata endpoints, the ECS endpoint, link-local,
		// unspecified and multicast addresses, checked after resolution.
		{"the instance metadata address", nil, nil, nil, "http://169.254.169.254/latest/meta-data/", egress.ForbiddenAddress, "169.254.169.254", 80, ""},
		{"the IPv6 instance metadata address", nil, nil, nil, "http://[fd00:ec2::254]/latest/meta-data/", egress.ForbiddenAddress, "fd00:ec2::254", 80, ""},
		{"the ECS credentials address", nil, nil, nil, "http://169.254.170.2/v2/credentials", egress.ForbiddenAddress, "169.254.170.2", 80, ""},
		{"an IPv4-mapped metadata address", nil, nil, nil, "http://[::ffff:169.254.169.254]/", egress.ForbiddenAddress, "169.254.169.254", 80, ""},
		{
			"a name resolving to the metadata address",
			[]string{"meta.example.test:80"},
			resolver{"meta.example.test": to("meta.example.test", "169.254.169.254")},
			nil,
			"http://meta.example.test/", egress.ForbiddenAddress, "meta.example.test", 80, "169.254.169.254",
		},
		{
			"a name resolving to an IPv4-mapped metadata address",
			[]string{"meta.example.test:80"},
			resolver{"meta.example.test": to("meta.example.test", "::ffff:169.254.169.254")},
			nil,
			"http://meta.example.test/", egress.ForbiddenAddress, "meta.example.test", 80, "169.254.169.254",
		},
		{
			"a name resolving to a link-local address",
			[]string{"ll.example.test:80"},
			resolver{"ll.example.test": to("ll.example.test", "169.254.7.7")},
			nil,
			"http://ll.example.test/", egress.ForbiddenAddress, "ll.example.test", 80, "169.254.7.7",
		},
		{
			"a name resolving to an IPv6 link-local address",
			[]string{"ll.example.test:80"},
			resolver{"ll.example.test": to("ll.example.test", "fe80::1")},
			nil,
			"http://ll.example.test/", egress.ForbiddenAddress, "ll.example.test", 80, "fe80::1",
		},
		{
			"a name resolving to the unspecified address",
			[]string{"zero.example.test:80"},
			resolver{"zero.example.test": to("zero.example.test", "0.0.0.0")},
			nil,
			"http://zero.example.test/", egress.ForbiddenAddress, "zero.example.test", 80, "0.0.0.0",
		},
		{
			"a name resolving to a multicast address",
			[]string{"mc.example.test:80"},
			resolver{"mc.example.test": to("mc.example.test", "224.0.0.251")},
			nil,
			"http://mc.example.test/", egress.ForbiddenAddress, "mc.example.test", 80, "224.0.0.251",
		},
		{
			"one forbidden address among good ones",
			[]string{"mixed.example.test:80"},
			resolver{"mixed.example.test": to("mixed.example.test", "203.0.113.10", "169.254.169.254")},
			nil,
			"http://mixed.example.test/", egress.ForbiddenAddress, "mixed.example.test", 80, "169.254.169.254",
		},
		// 7.3: understudy's own listeners, whatever the list says.
		{"understudy's own listener", []string{"127.0.0.1:*"}, nil, []string{"127.0.0.1:4566"}, "http://127.0.0.1:4566/_understudy/v1/reset", egress.OwnListener, "127.0.0.1", 4566, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n := newNetwork(map[string]*receiver{
					"127.0.0.1:8080": answering(http.StatusOK), "127.0.0.1:8081": answering(http.StatusOK), "127.0.0.1:4566": answering(http.StatusOK),
					"10.0.0.7:8080": answering(http.StatusOK), "10.0.0.7:80": answering(http.StatusOK), "203.0.113.10:443": answering(http.StatusOK),
					"203.0.113.10:80": answering(http.StatusOK), "169.254.169.254:80": answering(http.StatusOK),
				})
				var refuse []netip.AddrPort
				for _, a := range c.refuse {
					refuse = append(refuse, netip.MustParseAddrPort(a))
				}
				var dns egress.Resolver = resolver{}
				if c.dns != nil {
					dns = c.dns
				}
				cl := newClient(t, egress.Options{Dialer: n, Resolver: dns, Refuse: refuse}, c.entries...)
				_, err := send(t, cl, c.url, nil, "hello")
				re := wantRefused(t, n, err, c.want, c.host, c.port, c.refused)
				// The refusals a list entry can lift name the setting to change.
				liftable := []egress.Refusal{egress.NotAllowed, egress.UnlistedLoopback, egress.UnlistedPrivate}
				if slices.Contains(liftable, c.want) && !strings.Contains(re.Error(), setting.String()) {
					t.Errorf("error %q does not name the setting %s", re.Error(), setting)
				}
			})
		})
	}
}

func TestSendRefusesOtherSchemes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newNetwork(map[string]*receiver{"127.0.0.1:21": answering(http.StatusOK)})
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}}, "127.0.0.1:*")
		_, err := send(t, cl, "ftp://127.0.0.1:21/hook", nil, "")
		var re *egress.RefusedError
		if !errors.As(err, &re) || re.Refusal != egress.UnsupportedScheme {
			t.Errorf("Send(ftp://...) error = %v, want an UnsupportedScheme refusal", err)
		}
		if d := n.dialed(); len(d) != 0 {
			t.Errorf("dialed %v", d)
		}
	})
}

// A listed host is reached at the address it was checked at, and the
// scheme does not matter to the match (7.2).
func TestSendReachesListedHosts(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		dns     resolver
		url     string
		dial    string
	}{
		{"a loopback literal on any port", []string{"127.0.0.1:*"}, nil, "http://127.0.0.1:8080/hook", "127.0.0.1:8080"},
		{"the IPv6 loopback", []string{"[::1]:8080"}, nil, "http://[::1]:8080/hook", "[::1]:8080"},
		{"localhost", []string{"localhost:8080"}, resolver{"localhost": to("localhost", "127.0.0.1")}, "http://localhost:8080/hook", "127.0.0.1:8080"},
		{"a listed private literal", []string{"10.1.2.3:8080"}, nil, "http://10.1.2.3:8080/hook", "10.1.2.3:8080"},
		{"a public name", []string{"hooks.example.test:8080"}, resolver{"hooks.example.test": to("hooks.example.test", "203.0.113.10")}, "http://hooks.example.test:8080/hook", "203.0.113.10:8080"},
		{"http on a port listed for https", []string{"hooks.example.test:443"}, resolver{"hooks.example.test": to("hooks.example.test", "203.0.113.10")}, "http://hooks.example.test:443/hook", "203.0.113.10:443"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := answering(http.StatusNoContent)
				n := newNetwork(map[string]*receiver{c.dial: r})
				var dns egress.Resolver = resolver{}
				if c.dns != nil {
					dns = c.dns
				}
				cl := newClient(t, egress.Options{Dialer: n, Resolver: dns}, c.entries...)
				res, err := send(t, cl, c.url, nil, "hello")
				if err != nil || res.Status != http.StatusNoContent {
					t.Fatalf("Send = %+v, %v; want the receiver's 204", res, err)
				}
				if d := n.dialed(); !slices.Equal(d, []netip.AddrPort{netip.MustParseAddrPort(c.dial)}) {
					t.Errorf("dialed %v, want %s alone", d, c.dial)
				}
				if got := r.requests(); len(got) != 1 {
					t.Errorf("the receiver got %d requests, want 1", len(got))
				}
			})
		})
	}
}

// The request goes out as given: POST over HTTP/1.1 to the URL's path and
// query unchanged (3.1), with the delivery's headers under the names it
// wrote (3.2: SNS's x-amz-sns-* names are lower case) and its body.
func TestSendWritesTheRequestAsGiven(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := answering(http.StatusOK)
		n := newNetwork(map[string]*receiver{"127.0.0.1:8080": r})
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}}, "127.0.0.1:8080")
		header := http.Header{
			"x-amz-sns-message-type": {"Notification"},
			"Content-Type":           {"text/plain; charset=UTF-8"},
		}
		if _, err := send(t, cl, "http://127.0.0.1:8080/hooks/orders?attempt=1&x=%2F", header, `{"Type":"Notification"}`); err != nil {
			t.Fatalf("Send: %v", err)
		}
		got := r.requests()
		if len(got) != 1 {
			t.Fatalf("the receiver got %d requests, want 1", len(got))
		}
		x := got[0]
		if x.req.Method != http.MethodPost || x.req.RequestURI != "/hooks/orders?attempt=1&x=%2F" || x.req.Proto != "HTTP/1.1" {
			t.Errorf("request line = %s %s %s, want POST /hooks/orders?attempt=1&x=%%2F HTTP/1.1", x.req.Method, x.req.RequestURI, x.req.Proto)
		}
		if string(x.body) != `{"Type":"Notification"}` {
			t.Errorf("body = %q", x.body)
		}
		for _, line := range []string{"\r\nx-amz-sns-message-type: Notification", "\r\nContent-Type: text/plain; charset=UTF-8", "\r\nHost: 127.0.0.1:8080"} {
			if !strings.Contains(x.head, line) {
				t.Errorf("request head\n%s\nlacks %q", x.head, strings.TrimPrefix(line, "\r\n"))
			}
		}
	})
}

// No proxy routes a delivery (7.4): with every proxy variable set, the
// request still goes straight to the receiver, in origin form.
func TestSendIgnoresProxyVariables(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := answering(http.StatusOK)
		n := newNetwork(map[string]*receiver{"10.1.2.3:8080": r, proxy: answering(http.StatusOK)})
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}}, "10.1.2.3:8080")
		if _, err := send(t, cl, "http://10.1.2.3:8080/hook", nil, "hello"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if d := n.dialed(); !slices.Equal(d, []netip.AddrPort{netip.MustParseAddrPort("10.1.2.3:8080")}) {
			t.Errorf("dialed %v, want the receiver alone, never the proxy %s", d, proxy)
		}
		if got := r.requests(); len(got) != 1 || got[0].req.RequestURI != "/hook" {
			t.Errorf("the receiver got %v, want one request for /hook in origin form", got)
		}
	})
}

// A redirect is never followed (7.4): the 3xx is the attempt's answer and
// its Location is not contacted (6.1, sns.http.redirects).
func TestSendDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				from := &receiver{answer: func(*http.Request) response {
					return response{status: status, header: http.Header{"Location": {"http://127.0.0.1:8081/moved"}}}
				}}
				moved := answering(http.StatusOK)
				n := newNetwork(map[string]*receiver{"127.0.0.1:8080": from, "127.0.0.1:8081": moved})
				cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}}, "127.0.0.1:*")
				res, err := send(t, cl, "http://127.0.0.1:8080/hook", nil, "hello")
				if err != nil || res.Status != status {
					t.Errorf("Send = %+v, %v; want the %d itself", res, err, status)
				}
				if got := moved.requests(); len(got) != 0 {
					t.Errorf("the Location received %d requests, want none", len(got))
				}
				if d := n.dialed(); !slices.Equal(d, []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:8080")}) {
					t.Errorf("dialed %v, want the first receiver alone", d)
				}
			})
		})
	}
}

// The address is checked once and that address is dialed: a resolver that
// answers differently afterwards cannot steer the connection (7.4, DNS
// rebinding between match and connect).
type rebinding struct {
	mu    sync.Mutex
	calls int
}

func (r *rebinding) Resolve(_ context.Context, host string) (egress.Resolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls == 1 {
		return to(host, "127.0.0.1"), nil
	}
	return to(host, "10.0.0.7"), nil
}

func TestSendDialsTheAddressItChecked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newNetwork(map[string]*receiver{"127.0.0.1:8080": answering(http.StatusOK), "10.0.0.7:8080": answering(http.StatusOK)})
		cl := newClient(t, egress.Options{Dialer: n, Resolver: &rebinding{}}, "localhost:8080")
		if _, err := send(t, cl, "http://localhost:8080/hook", nil, "hello"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if d := n.dialed(); !slices.Equal(d, []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:8080")}) {
			t.Errorf("dialed %v, want the checked loopback address alone", d)
		}
	})
}

// A receiver that never answers fails the attempt at the deadline, about
// 15 seconds (3.1), on the wall clock even while emulator time is frozen
// (7.4, ADR 0006).
func TestSendTimesOutOnTheWallClock(t *testing.T) {
	if egress.DefaultTimeout != 15*time.Second {
		t.Errorf("DefaultTimeout = %v, want 15s (3.1: about 15 seconds)", egress.DefaultTimeout)
	}
	for _, c := range []struct {
		timeout, want time.Duration
	}{{0, egress.DefaultTimeout}, {3 * time.Second, 3 * time.Second}} {
		t.Run(c.want.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n := newNetwork(map[string]*receiver{"127.0.0.1:8080": {}})
				cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}, Timeout: c.timeout}, "127.0.0.1:8080")
				start := time.Now()
				_, err := send(t, cl, "http://127.0.0.1:8080/hook", nil, "hello")
				elapsed := time.Since(start)
				var te *egress.TransportError
				if !errors.As(err, &te) || !te.Timeout || te.Host != "127.0.0.1" || te.Port != 8080 {
					t.Fatalf("Send error = %v, want a timed-out *TransportError for 127.0.0.1:8080", err)
				}
				if elapsed < c.want || elapsed > c.want+time.Second {
					t.Errorf("Send gave up after %v, want %v", elapsed, c.want)
				}
			})
		})
	}
}

// A connection the receiver refuses is a failed exchange, not a refusal:
// understudy tried (7.3 is for attempts it chose not to make).
func TestSendReportsConnectionFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newNetwork(nil)
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}}, "127.0.0.1:8080")
		_, err := send(t, cl, "http://127.0.0.1:8080/hook", nil, "hello")
		var te *egress.TransportError
		if !errors.As(err, &te) || te.Timeout || te.Host != "127.0.0.1" || te.Port != 8080 {
			t.Errorf("Send error = %v, want a *TransportError for 127.0.0.1:8080 that did not time out", err)
		}
		if _, ok := errors.AsType[*egress.RefusedError](err); ok {
			t.Errorf("Send error = %v, a refusal; the attempt was made", err)
		}
	})
}

// Only a bounded part of the response body is read (7.4): a receiver
// sending far more than the bound is cut off.
func TestSendReadsABoundedBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		long := bytes.Repeat([]byte("x"), 1<<20)
		r := &receiver{answer: func(*http.Request) response { return response{status: http.StatusOK, body: long} }}
		n := newNetwork(map[string]*receiver{"127.0.0.1:8080": r})
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}, MaxResponseBytes: 1024}, "127.0.0.1:8080")
		res, err := send(t, cl, "http://127.0.0.1:8080/hook", nil, "hello")
		if err != nil || res.Status != http.StatusOK {
			t.Fatalf("Send = %+v, %v; want 200", res, err)
		}
		got := r.requests()
		if len(got) != 1 {
			t.Fatalf("the receiver got %d requests, want 1", len(got))
		}
		if got[0].taken >= int64(len(long)) {
			t.Errorf("the client read %d bytes of a %d-byte body, want it to stop near its 1024-byte bound", got[0].taken, len(long))
		}
	})
}

// selfSigned returns a certificate for hooks.example.test, valid around
// the bubble's start.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hooks.example.test"},
		DNSNames: []string{"hooks.example.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// https sends SNI and verifies the receiver's certificate; an untrusted
// certificate fails the exchange, never skipped (3.1, 7.4).
func TestSendVerifiesTLS(t *testing.T) {
	for _, trusted := range []bool{true, false} {
		t.Run(fmt.Sprintf("trusted=%t", trusted), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cert, pool := selfSigned(t)
				r := answering(http.StatusOK)
				n := newNetwork(map[string]*receiver{"203.0.113.10:8443": r})
				n.tls = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
				o := egress.Options{Dialer: n, Resolver: resolver{"hooks.example.test": to("hooks.example.test", "203.0.113.10")}}
				if trusted {
					o.RootCAs = pool
				}
				cl := newClient(t, o, "hooks.example.test:8443")
				res, err := send(t, cl, "https://hooks.example.test:8443/hook", nil, "hello")
				got := r.requests()
				if !trusted {
					if _, ok := errors.AsType[*egress.TransportError](err); !ok {
						t.Errorf("Send to an untrusted certificate = %+v, %v; want a *TransportError", res, err)
					}
					if len(got) != 0 {
						t.Errorf("the receiver got %d requests over an unverified connection", len(got))
					}
					return
				}
				if err != nil || res.Status != http.StatusOK {
					t.Fatalf("Send = %+v, %v; want 200", res, err)
				}
				if len(got) != 1 || got[0].sni != "hooks.example.test" {
					t.Errorf("the receiver got %d requests with SNI %v, want one naming hooks.example.test", len(got), got)
				}
			})
		})
	}
}

// New refuses options it cannot run with.
func TestNewRefusesOptions(t *testing.T) {
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		o     egress.Options
		field string
	}{
		{egress.Options{}, "Clock"},
		{egress.Options{Clock: clk, Timeout: -time.Second}, "Timeout"},
		{egress.Options{Clock: clk, MaxResponseBytes: -1}, "MaxResponseBytes"},
	}
	for _, c := range cases {
		cl, oerr := egress.New(c.o)
		if oerr == nil || oerr.Field != c.field {
			t.Errorf("New(%+v) = %v, %v; want an *OptionsError for %s", c.o, cl, oerr, c.field)
			continue
		}
		if !strings.HasPrefix(oerr.Error(), "egress: Options."+c.field+" ") {
			t.Errorf("error %q does not name Options.%s", oerr.Error(), c.field)
		}
	}
	if cl, oerr := egress.New(egress.Options{Clock: clk}); oerr != nil || cl == nil {
		t.Errorf("New with a clock = %v, %v; want a client", cl, oerr)
	}
}
