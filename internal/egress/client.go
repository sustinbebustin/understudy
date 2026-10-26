package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
)

// DefaultTimeout is the request deadline when Options.Timeout is 0: SNS
// gives up on an endpoint "in about 15 seconds" (sns-http-delivery.md 3.1,
// 7.4).
const DefaultTimeout = 15 * time.Second

// DefaultMaxResponseBytes bounds the response body Send reads and discards
// when Options.MaxResponseBytes is 0 (sns-http-delivery.md 7.4).
const DefaultMaxResponseBytes int64 = 64 << 10

// Resolver is the name-resolution seam.
type Resolver interface {
	// Resolve returns the CNAME chain of host, host first and the
	// canonical name last (one element when there is no CNAME), and the
	// addresses it resolved to, in order.
	Resolve(ctx context.Context, host string) (Resolution, error)
}

// Resolution is what a Resolver found for one host.
type Resolution struct {
	Chain []string
	Addrs []netip.Addr
}

// Dialer is the dial seam. Send hands it an address it already checked, so
// nothing is resolved between the check and the connection (DNS
// rebinding, sns-http-delivery.md 7.4).
type Dialer interface {
	Dial(ctx context.Context, addr netip.AddrPort) (net.Conn, error)
}

// Options configures New.
type Options struct {
	// Allow is the producer's allow-list; the zero value allows nothing.
	Allow AllowList
	// Clock supplies the wall-clock deadline: network I/O is wall time
	// (ADR 0006). Required.
	Clock *clock.Clock
	// Timeout is the request deadline; 0 means DefaultTimeout.
	Timeout time.Duration
	// MaxResponseBytes bounds the response body read; 0 means
	// DefaultMaxResponseBytes.
	MaxResponseBytes int64
	// Refuse are understudy's own listener addresses and the Docker Engine
	// address, refused whatever Allow says (sns-http-delivery.md 7.3). They
	// are compared with the address dialed, so a listener bound to an
	// unspecified address is listed once per local address.
	Refuse []netip.AddrPort
	// RootCAs verifies https endpoints; nil means the system roots.
	RootCAs *x509.CertPool
	// Resolver resolves names; nil means the system resolver, whose chain
	// is the host and LookupCNAME's canonical name: the standard library
	// exposes no intermediate CNAMEs.
	Resolver Resolver
	// Dialer opens connections; nil means a net.Dialer.
	Dialer Dialer
	// Listeners adds understudy's live listener addresses to Refuse; nil
	// adds none. Not implemented yet: New ignores it.
	Listeners Listeners
}

// Listeners reports understudy's own listening addresses when a request is
// checked (sns-http-delivery.md 7.3); the server implements it.
type Listeners interface {
	// ListenerAddrs returns every address one of understudy's listeners
	// accepts on now: main, TLS, container and each Runtime API listener,
	// one per local address for a listener bound to an unspecified
	// address.
	ListenerAddrs() []netip.AddrPort
}

// OptionsError reports Options New cannot use. Nothing was created.
type OptionsError struct {
	Field  string
	Reason string
}

func (e *OptionsError) Error() string { return "egress: Options." + e.Field + " " + e.Reason }

// Client sends outbound HTTP requests under the checks of the package doc.
// It is safe for concurrent use.
type Client struct {
	allow    AllowList
	clock    *clock.Clock
	timeout  time.Duration
	maxBody  int64
	refuse   []netip.AddrPort
	rootCAs  *x509.CertPool
	resolver Resolver
	dialer   Dialer
}

// New returns a client for o, with the defaults of its zero fields.
func New(o Options) (*Client, *OptionsError) {
	switch {
	case o.Clock == nil:
		return nil, &OptionsError{Field: "Clock", Reason: "is nil; the request deadline is read from the emulator clock's wall time, so a client needs one"}
	case o.Timeout < 0:
		return nil, &OptionsError{Field: "Timeout", Reason: "is " + o.Timeout.String() + "; use a positive deadline, or 0 for DefaultTimeout"}
	case o.MaxResponseBytes < 0:
		return nil, &OptionsError{Field: "MaxResponseBytes", Reason: "is " + strconv.FormatInt(o.MaxResponseBytes, 10) + "; use a positive bound, or 0 for DefaultMaxResponseBytes"}
	}
	c := &Client{
		allow:    o.Allow,
		clock:    o.Clock,
		timeout:  o.Timeout,
		maxBody:  o.MaxResponseBytes,
		rootCAs:  o.RootCAs,
		resolver: o.Resolver,
		dialer:   o.Dialer,
	}
	if c.timeout == 0 {
		c.timeout = DefaultTimeout
	}
	if c.maxBody == 0 {
		c.maxBody = DefaultMaxResponseBytes
	}
	if c.resolver == nil {
		c.resolver = systemResolver{}
	}
	if c.dialer == nil {
		c.dialer = netDialer{}
	}
	for _, a := range o.Refuse {
		c.refuse = append(c.refuse, netip.AddrPortFrom(a.Addr().Unmap(), a.Port()))
	}
	return c, nil
}

// Request is one outbound HTTP request.
type Request struct {
	Method string
	// URL is the endpoint, http or https. Its userinfo is never sent.
	URL    *url.URL
	Header http.Header
	Body   []byte
}

// Response is what the endpoint answered.
type Response struct {
	Status int
}

// Send makes one request. Its error is a *RefusedError, when nothing was
// sent and retrying cannot help, or a *TransportError, when the exchange
// failed.
//
// In order: the scheme must be http or https; a host under an AWS suffix
// and an always-refused IP literal are refused (sns-http-delivery.md 7.3);
// the host and port must match the allow-list (7.2); a name is resolved,
// every name of its CNAME chain checked against the AWS suffixes, and
// every address it resolved to checked (7.3, 7.4) before any is dialed.
// The checked addresses are dialed in order, https verified against
// RootCAs, and one HTTP/1.1 exchange is made over the first connection: no
// proxy, no redirect followed, at most MaxResponseBytes of the body read
// and discarded. Resolution, the dial and the exchange share one deadline
// on the clock's wall time (ADR 0006).
func (c *Client) Send(ctx context.Context, r Request) (Response, error) {
	t, refused := c.check(r.URL)
	if refused != nil {
		return Response{}, refused
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	expired := c.clock.Until(ctx, c.clock.WallDeadline(c.clock.Wall().Add(c.timeout)))
	go func() {
		<-expired
		cancel(&deadlineError{timeout: c.timeout})
	}()
	addrs, err := c.addresses(ctx, t)
	if err != nil {
		return Response{}, c.failed(ctx, t, err)
	}
	conn, err := c.dial(ctx, t, addrs)
	if err != nil {
		return Response{}, c.failed(ctx, t, err)
	}
	status, err := c.exchange(ctx, conn, t, r)
	if err != nil {
		return Response{}, c.failed(ctx, t, err)
	}
	return Response{Status: status}, nil
}

// target is a request that passed the checks that need no network.
type target struct {
	url *url.URL
	// host is the URL's host normalized as Entry.Host, port its port or the
	// scheme's default.
	host string
	port uint16
	// entry is the allow-list entry that matched.
	entry Entry
	// literal is the host's address when the host is an IP literal, and
	// the zero Addr when it is a name.
	literal netip.Addr
}

// check makes the checks of a request that need no network: the scheme,
// the hard refusals of the host itself and the allow-list.
func (c *Client) check(u *url.URL) (target, *RefusedError) {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		var host string
		if u != nil {
			host = normalizeHost(u.Hostname())
		}
		return target{}, c.refused(UnsupportedScheme, host, 0, "")
	}
	t := target{url: u, host: normalizeHost(u.Hostname())}
	switch p := u.Port(); {
	case p != "":
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil || n == 0 {
			return target{}, c.refused(NotAllowed, t.host, 0, "")
		}
		t.port = uint16(n)
	case u.Scheme == "https":
		t.port = 443
	default:
		t.port = 80
	}
	if IsAWSName(t.host) {
		return target{}, c.refused(AWSEndpoint, t.host, t.port, "")
	}
	if a, err := netip.ParseAddr(t.host); err == nil {
		if _, forbidden := forbiddenAddress(a); forbidden {
			return target{}, c.refused(ForbiddenAddress, t.host, t.port, "")
		}
		t.literal = a
	}
	entry, ok := c.allow.Match(t.host, t.port)
	if !ok {
		return target{}, c.refused(NotAllowed, t.host, t.port, "")
	}
	t.entry = entry
	return t, nil
}

// addresses returns the addresses to dial for t: its literal, or what its
// name resolves to, once every name of the CNAME chain and every address
// passed the checks. One refused name or address refuses the request.
func (c *Client) addresses(ctx context.Context, t target) ([]netip.Addr, error) {
	addrs := []netip.Addr{t.literal}
	if !t.literal.IsValid() {
		res, err := c.resolver.Resolve(ctx, t.host)
		if err != nil {
			return nil, err
		}
		for _, name := range res.Chain {
			if name = normalizeHost(name); IsAWSName(name) {
				return nil, c.refused(AWSEndpoint, t.host, t.port, name)
			}
		}
		if len(res.Addrs) == 0 {
			return nil, &noAddressError{host: t.host}
		}
		addrs = res.Addrs
	}
	checked := make([]netip.Addr, len(addrs))
	for i, a := range addrs {
		a = a.Unmap()
		if refusal, ok := c.refusal(t, a); ok {
			return nil, c.refused(refusal, t.host, t.port, a.String())
		}
		checked[i] = a
	}
	return checked, nil
}

// refusal returns why t may not connect to a, if it may not: the hard
// refusals (sns-http-delivery.md 7.3), then the rule of the entry that
// allowed t. A localhost entry reaches only loopback (7.4); a DNS-name
// entry reaches neither loopback nor a private address, which only an IP
// literal entry lists (ADR 0015); an IP literal entry reaches that address.
func (c *Client) refusal(t target, a netip.Addr) (Refusal, bool) {
	if _, forbidden := forbiddenAddress(a); forbidden {
		return ForbiddenAddress, true
	}
	if slices.Contains(c.refuse, netip.AddrPortFrom(a, t.port)) {
		return OwnListener, true
	}
	switch {
	case t.literal.IsValid():
	case t.entry.Host == "localhost":
		if !a.IsLoopback() {
			return AddressMismatch, true
		}
	case a.IsLoopback():
		return UnlistedLoopback, true
	case a.IsPrivate():
		return UnlistedPrivate, true
	}
	return 0, false
}

// dial connects to the first of addrs that accepts.
func (c *Client) dial(ctx context.Context, t target, addrs []netip.Addr) (net.Conn, error) {
	errs := make([]error, 0, len(addrs))
	for _, a := range addrs {
		conn, err := c.dialer.Dial(ctx, netip.AddrPortFrom(a, t.port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// maxInterim bounds the interim 1xx answers read before the final one, as
// net/http's transport does.
const maxInterim = 5

// exchange makes the one HTTP/1.1 exchange of r over conn, which it
// closes, and returns the status of the final answer. The connection is
// closed when ctx ends, which ends a blocked read or write.
func (c *Client) exchange(ctx context.Context, conn net.Conn, t target, r Request) (int, error) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	rw := conn
	if t.url.Scheme == "https" {
		tc := tls.Client(conn, &tls.Config{
			ServerName: t.host,
			RootCAs:    c.rootCAs,
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
			Time:       c.clock.Wall,
		})
		if err := tc.HandshakeContext(ctx); err != nil {
			return 0, err
		}
		rw = tc
	}
	req := outbound(t.url, r)
	if err := req.Write(rw); err != nil {
		return 0, err
	}
	br := bufio.NewReader(rw)
	for range maxInterim + 1 {
		res, err := http.ReadResponse(br, req) //nolint:bodyclose // the body reads from conn, which exchange closes; Body.Close could read it to its end, past MaxResponseBytes
		if err != nil {
			return 0, err
		}
		if res.StatusCode < 200 && res.StatusCode != http.StatusSwitchingProtocols {
			// An interim answer, such as 100 Continue or 103 Early Hints,
			// comes before the final one (RFC 9110 15.2).
			continue
		}
		// No documented behavior uses the body, so a bounded part of it is
		// read and discarded (sns-http-delivery.md 6.1, 7.4).
		io.CopyN(io.Discard, res.Body, c.maxBody) //nolint:errcheck,gosec // the status is the answer; a failed read of the discarded body changes nothing
		return res.StatusCode, nil
	}
	return 0, &interimError{limit: maxInterim}
}

// outbound is r as written on the wire: its method and header to u's path
// and query, without u's userinfo, announcing that the connection closes
// after the exchange.
func outbound(u *url.URL, r Request) *http.Request {
	dest := *u
	dest.User = nil
	header := r.Header.Clone()
	if header == nil {
		header = http.Header{}
	}
	req := &http.Request{
		Method: r.Method, URL: &dest, Host: dest.Host, Header: header,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Close: true,
		ContentLength: int64(len(r.Body)),
	}
	if len(r.Body) > 0 {
		req.Body = io.NopCloser(bytes.NewReader(r.Body))
	}
	return req
}

// failed returns err, a refusal or the failure of the exchange with t, as
// Send's error.
func (c *Client) failed(ctx context.Context, t target, err error) error {
	if re, ok := errors.AsType[*RefusedError](err); ok {
		return re
	}
	te := &TransportError{Host: t.host, Port: t.port, Err: err}
	if de, ok := errors.AsType[*deadlineError](context.Cause(ctx)); ok {
		te.Timeout, te.Err = true, de
	}
	return te
}

// refused returns the refusal r of host:port, name being the chain name or
// address refused when it is not host.
func (c *Client) refused(r Refusal, host string, port uint16, name string) *RefusedError {
	if name == host {
		name = ""
	}
	return &RefusedError{Refusal: r, Host: host, Port: port, Name: name, Setting: c.allow.Setting()}
}

// deadlineError is the request deadline passing.
type deadlineError struct{ timeout time.Duration }

func (e *deadlineError) Error() string {
	return "the request deadline of " + e.timeout.String() + " passed"
}

// noAddressError is a name the resolver answered with no address.
type noAddressError struct{ host string }

func (e *noAddressError) Error() string {
	return "the name " + e.host + " resolved to no address"
}

// interimError is an endpoint that sent only interim 1xx answers.
type interimError struct{ limit int }

func (e *interimError) Error() string {
	return "the endpoint sent more than " + strconv.Itoa(e.limit) + " interim 1xx answers and no final one"
}

// systemResolver resolves through net.DefaultResolver.
type systemResolver struct{}

// Resolve returns host and its canonical name as the chain, and the
// addresses host resolves to. localhost is never a CNAME, so its chain is
// itself; a failed CNAME lookup of any other name fails the resolution,
// since its chain could not be checked.
func (systemResolver) Resolve(ctx context.Context, host string) (Resolution, error) {
	r := net.DefaultResolver
	chain := []string{host}
	if host != "localhost" {
		cname, err := r.LookupCNAME(ctx, host)
		if err != nil {
			return Resolution{}, err
		}
		if canonical := normalizeHost(cname); canonical != host {
			chain = append(chain, canonical)
		}
	}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{Chain: chain, Addrs: addrs}, nil
}

// netDialer dials TCP through a net.Dialer.
type netDialer struct{}

func (netDialer) Dial(ctx context.Context, addr netip.AddrPort) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr.String())
}

// Refusal is why Send refused a request.
type Refusal uint8

// Refusal values.
const (
	// NotAllowed is a host and port no entry of the allow-list matches.
	NotAllowed Refusal = iota + 1
	// AWSEndpoint is a name under an AWS suffix: understudy never reaches
	// AWS (invariant 1), whatever the allow-list says.
	AWSEndpoint
	// ForbiddenAddress is a metadata, link-local, unspecified or multicast
	// address, never allowed.
	ForbiddenAddress
	// OwnListener is one of understudy's own listeners or the Docker
	// Engine, never allowed.
	OwnListener
	// UnlistedLoopback is a DNS-name entry that resolved to loopback.
	UnlistedLoopback
	// UnlistedPrivate is a DNS-name entry that resolved to a private
	// address.
	UnlistedPrivate
	// AddressMismatch is a localhost entry that resolved to an address
	// that is not loopback.
	AddressMismatch
	// UnsupportedScheme is a URL whose scheme is not http or https.
	UnsupportedScheme
)

// String returns the refusal's name, such as "not-allowed".
func (r Refusal) String() string {
	switch r {
	case NotAllowed:
		return "not-allowed"
	case AWSEndpoint:
		return "aws-endpoint"
	case ForbiddenAddress:
		return "forbidden-address"
	case OwnListener:
		return "own-listener"
	case UnlistedLoopback:
		return "unlisted-loopback"
	case UnlistedPrivate:
		return "unlisted-private"
	case AddressMismatch:
		return "address-mismatch"
	case UnsupportedScheme:
		return "unsupported-scheme"
	default:
		return "Refusal(" + strconv.Itoa(int(r)) + ")"
	}
}

// RefusedError is a request Send refused; nothing was sent.
type RefusedError struct {
	Refusal Refusal
	// Host and Port are the URL's, the host normalized. Port is 0 when the
	// URL names a port outside 1-65535.
	Host string
	Port uint16
	// Name is the CNAME or address refused when it is not Host.
	Name string
	// Setting is the allow-list's setting.
	Setting Setting
}

// Error names the endpoint and what was refused, says nothing was sent,
// and gives the next step: the entry that lifts the refusal, or that no
// setting does.
func (e *RefusedError) Error() string {
	endpoint := e.Host
	if e.Port != 0 {
		endpoint = net.JoinHostPort(e.Host, strconv.Itoa(int(e.Port)))
	}
	return "egress: refused to send to " + endpoint + " (" + e.Refusal.String() + "): " + e.reason() + "; nothing was sent. " + e.remedy()
}

// reason says what was refused.
func (e *RefusedError) reason() string {
	refused := e.Host
	if e.Name != "" {
		refused = e.Name
	}
	switch e.Refusal {
	case NotAllowed:
		if e.Port == 0 {
			return "its URL names a port outside 1-65535"
		}
		return "no entry of " + e.Setting.String() + " allows it"
	case AWSEndpoint:
		if e.Name != "" {
			return "its CNAME chain reaches " + e.Name + ", an AWS endpoint"
		}
		return e.Host + " is an AWS endpoint"
	case ForbiddenAddress:
		kind := "is forbidden"
		if a, err := netip.ParseAddr(refused); err == nil {
			kind, _ = forbiddenAddress(a)
		}
		if e.Name != "" {
			return "it resolved to " + e.Name + ", which " + kind
		}
		return e.Host + " " + kind
	case OwnListener:
		return net.JoinHostPort(refused, strconv.Itoa(int(e.Port))) + " is one of understudy's own listeners or the Docker Engine"
	case UnlistedLoopback:
		return "it resolved to the loopback address " + e.Name + ", and an entry that names a DNS name reaches only public addresses"
	case UnlistedPrivate:
		return "it resolved to the private address " + e.Name + ", and an entry that names a DNS name reaches only public addresses"
	case AddressMismatch:
		return "the entry localhost resolved to " + e.Name + ", which is not a loopback address"
	case UnsupportedScheme:
		return "its URL's scheme is not http or https"
	}
	return "the refusal is " + e.Refusal.String()
}

// remedy is the next step: the entry that lifts the refusal, or why no
// setting does.
func (e *RefusedError) remedy() string {
	switch e.Refusal {
	case NotAllowed:
		if e.Port == 0 {
			return "Give the URL a port 1-65535, or none for the scheme's default."
		}
		if _, err := netip.ParseAddr(e.Host); err != nil && !isDNSName(e.Host) {
			return "Its host is not localhost, an IP literal or a DNS name, so no entry of " + e.Setting.String() + " can allow it."
		}
		return e.addEntry(e.Host)
	case UnlistedLoopback, UnlistedPrivate:
		return e.addEntry(e.Name)
	case AWSEndpoint:
		return "understudy never reaches AWS, so no setting allows it."
	case ForbiddenAddress:
		return "understudy never connects to metadata, link-local, unspecified or multicast addresses, so no setting allows it."
	case OwnListener:
		return "understudy never delivers to itself, so no setting allows it."
	case AddressMismatch:
		return "Check how this machine resolves localhost, or list a loopback address such as " +
			strconv.Quote(Entry{Host: "127.0.0.1", Port: Port(e.Port)}.String()) + " in " + e.Setting.String() + " instead."
	case UnsupportedScheme:
		return "Use an http or https URL."
	}
	return ""
}

// addEntry is the remedy of a refusal an entry for host lifts.
func (e *RefusedError) addEntry(host string) string {
	exact := Entry{Host: host, Port: Port(e.Port)}.String()
	anyPort := Entry{Host: host, Port: AnyPort}.String()
	return "To allow it, add " + strconv.Quote(exact) + " or " + strconv.Quote(anyPort) + " to " + e.Setting.String() + "."
}

// TransportError is an exchange that failed after Send's checks passed:
// the resolution, the dial, TLS, the write or the read, or the deadline.
// The endpoint may have received the request.
type TransportError struct {
	Host string
	Port uint16
	// Timeout is set when the deadline passed.
	Timeout bool
	Err     error
}

func (e *TransportError) Error() string {
	what := "failed"
	if e.Timeout {
		what = "timed out"
	}
	msg := "egress: the request to " + net.JoinHostPort(e.Host, strconv.Itoa(int(e.Port))) + " " + what
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *TransportError) Unwrap() error { return e.Err }
