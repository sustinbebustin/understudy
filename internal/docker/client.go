package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultSocket is the Engine socket used when Options.Socket is empty.
const DefaultSocket = "/var/run/docker.sock"

// requestHost is the Host header sent over the unix socket; the daemon
// accepts any valid host, and this is the one the moby client uses.
const requestHost = "api.moby.localhost"

// maxErrorBody caps how much of an error response body is read.
const maxErrorBody = 64 << 10

// Options configures a Client.
type Options struct {
	// Socket is the path of the Engine's unix socket; empty means
	// DefaultSocket.
	Socket string
	// APIVersionMin is the lowest API version accepted from the daemon; the
	// zero value means DefaultMinAPIVersion.
	APIVersionMin APIVersion
}

// Client talks to a Docker Engine (or Podman's compatible service) over its
// unix socket. It is safe for concurrent use. The API version is negotiated
// on the first call and renegotiated by every Ping.
type Client struct {
	socket string
	min    APIVersion
	http   *http.Client

	mu         sync.Mutex
	negotiated *Ping
}

// New builds a Client. It does no I/O; an unreachable daemon surfaces as an
// UnavailableError on the first call.
func New(opts Options) (*Client, error) {
	socket := opts.Socket
	if socket == "" {
		socket = DefaultSocket
	}
	minimum := opts.APIVersionMin
	if minimum == (APIVersion{}) {
		minimum = DefaultMinAPIVersion()
	}
	if minimum.Compare(MaxAPIVersion()) > 0 {
		return nil, fmt.Errorf("docker client: minimum API version %s is above the highest version this client speaks (%s)", minimum, MaxAPIVersion())
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
		DisableCompression:  true,
		MaxIdleConnsPerHost: 8,
	}
	return &Client{
		socket: socket,
		min:    minimum,
		http:   &http.Client{Transport: transport},
	}, nil
}

// Socket returns the socket path the client dials.
func (c *Client) Socket() string { return c.socket }

// Close releases idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Engine identifies the daemon behind the socket.
//
//sumtype:decl
type Engine interface{ isEngine() }

// Moby is Docker Engine (including Docker Desktop and rootless Docker).
type Moby struct{}

// Podman is Podman's Docker-compatible service, recognized by the
// Libpod-API-Version header it sets on every response (containers.md 10.1).
type Podman struct {
	LibpodAPIVersion string
}

func (Moby) isEngine()   {}
func (Podman) isEngine() {}

// Ping is the daemon's answer to GET /_ping.
type Ping struct {
	// APIVersion is the highest version the daemon serves, or the client
	// minimum when the daemon does not say (an old daemon).
	APIVersion APIVersion
	// Negotiated is the version the client uses for every other call.
	Negotiated APIVersion
	OSType     string
	Engine     Engine
}

// Ping checks that the daemon answers and renegotiates the API version.
// /_ping is never versioned.
func (c *Client) Ping(ctx context.Context) (Ping, error) {
	resp, err := c.send(ctx, "Ping", http.MethodGet, "/_ping", nil, nil, "")
	if err != nil {
		return Ping{}, err
	}
	drain(resp)
	p, err := c.parsePing(resp.Header)
	if err != nil {
		return Ping{}, err
	}
	c.mu.Lock()
	c.negotiated = &p
	c.mu.Unlock()
	return p, nil
}

func (c *Client) parsePing(h http.Header) (Ping, error) {
	server := c.min
	if raw := h.Get("Api-Version"); raw != "" {
		v, err := ParseAPIVersion(raw)
		if err != nil {
			return Ping{}, fmt.Errorf("docker Ping: daemon at %s sent an unreadable Api-Version header: %w", c.socket, err)
		}
		server = v
	}
	negotiated, err := negotiate(server, c.min)
	if err != nil {
		return Ping{}, err
	}
	var engine Engine = Moby{}
	if libpod := h.Get("Libpod-Api-Version"); libpod != "" {
		engine = Podman{LibpodAPIVersion: libpod}
	}
	return Ping{APIVersion: server, Negotiated: negotiated, OSType: h.Get("Ostype"), Engine: engine}, nil
}

// session returns the negotiated ping, pinging first when needed.
func (c *Client) session(ctx context.Context) (Ping, error) {
	c.mu.Lock()
	p := c.negotiated
	c.mu.Unlock()
	if p != nil {
		return *p, nil
	}
	return c.Ping(ctx)
}

// call sends a versioned request and maps error statuses to *Error. The
// caller owns the returned body.
func (c *Client) call(ctx context.Context, op, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error) {
	p, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	return c.send(ctx, op, method, "/v"+p.Negotiated.String()+path, query, body, contentType)
}

func (c *Client) send(ctx context.Context, op, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error) {
	u := url.URL{Scheme: "http", Host: requestHost, Path: path, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("docker %s: build request: %w", op, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return nil, unavailable(c.socket, err)
		}
		return nil, fmt.Errorf("docker %s %s %s: %w", op, method, path, err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		defer drain(resp)
		return nil, &Error{Op: op, Status: resp.StatusCode, Message: errorMessage(resp)}
	}
	return resp, nil
}

func errorMessage(resp *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return fmt.Sprintf("%s (error body unreadable: %v)", http.StatusText(resp.StatusCode), err)
	}
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Message != "" {
		return body.Message
	}
	if text := strings.TrimSpace(string(raw)); text != "" {
		return text
	}
	return http.StatusText(resp.StatusCode)
}

// callJSON sends in (when non-nil) as JSON and decodes the response into out
// (when non-nil).
func (c *Client) callJSON(ctx context.Context, op, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("docker %s: encode request: %w", op, err)
		}
		body = bytes.NewReader(raw)
		contentType = "application/json"
	}
	resp, err := c.call(ctx, op, method, path, query, body, contentType)
	if err != nil {
		return err
	}
	defer drain(resp)
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("docker %s: decode response: %w", op, err)
	}
	return nil
}

// drain reads what is left of a body so the connection can be reused, then
// closes it. A failed read only means the connection is dropped instead of
// reused, so its error is deliberately ignored.
func drain(resp *http.Response) {
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody)) //nolint:errcheck,gosec // a failed drain only drops the connection; nothing depends on it
}
