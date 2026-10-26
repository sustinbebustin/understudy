package egress

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/netip"
	"net/url"
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
	// address, refused whatever Allow says (sns-http-delivery.md 7.3).
	Refuse []netip.AddrPort
	// RootCAs verifies https endpoints; nil means the system roots.
	RootCAs *x509.CertPool
	// Resolver resolves names; nil means the system resolver.
	Resolver Resolver
	// Dialer opens connections; nil means a net.Dialer.
	Dialer Dialer
}

// OptionsError reports Options New cannot use. Nothing was created.
type OptionsError struct {
	Field  string
	Reason string
}

func (e *OptionsError) Error() string { return "egress: Options." + e.Field + " " + e.Reason }

// Client sends outbound HTTP requests under the checks of the package doc.
type Client struct {
	opts Options
}

// New returns a client. It is not implemented yet and refuses every
// Options.
func New(o Options) (*Client, *OptionsError) {
	_ = o
	return nil, &OptionsError{Field: "Allow", Reason: "cannot be used: the egress client is not implemented yet"}
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
// failed. It is not implemented yet and refuses every request.
func (c *Client) Send(ctx context.Context, r Request) (Response, error) {
	_ = ctx
	host := ""
	if r.URL != nil {
		host = r.URL.Hostname()
	}
	var setting Setting
	if c != nil {
		setting = c.opts.Allow.Setting()
	}
	return Response{}, &RefusedError{Refusal: NotAllowed, Host: host, Setting: setting}
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
	// Host and Port are the URL's, the host normalized.
	Host string
	Port uint16
	// Name is the CNAME or address refused when it is not Host.
	Name string
	// Setting is the allow-list's setting.
	Setting Setting
}

func (e *RefusedError) Error() string {
	return "egress: refused to send to " + e.Host + " (" + e.Refusal.String() + "); outbound delivery is not implemented yet, so nothing was sent and no " +
		e.Setting.String() + " entry changes that"
}

// TransportError is an exchange that failed after Send's checks passed:
// the dial, TLS, the write or the read, or the deadline.
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
	msg := "egress: the request to " + e.Host + ":" + strconv.Itoa(int(e.Port)) + " " + what
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *TransportError) Unwrap() error { return e.Err }
