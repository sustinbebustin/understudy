package docker

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
)

// Error is a non-success status from the Engine API with the daemon's
// message, for example 404 "No such container: x" or 409 for a name in use.
type Error struct {
	Op      string
	Status  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("docker %s: %s (HTTP %d)", e.Op, e.Message, e.Status)
}

// IsNotFound reports whether err is an Engine API 404: no such image,
// container or network.
func IsNotFound(err error) bool { return hasStatus(err, http.StatusNotFound) }

// IsConflict reports whether err is an Engine API 409: a name already in
// use, a container that is not running, or a running container removed
// without force.
func IsConflict(err error) bool { return hasStatus(err, http.StatusConflict) }

// IsMountNotShared reports whether err is the daemon refusing a bind mount
// whose source it cannot see, as Docker Desktop does for a path outside its
// shared directories: "Mounts denied: The path ... is not shared from the
// host and is not known to Docker" (containers.md 5.1). Docker Desktop
// checks mounts in its API proxy, so the refusal may answer the create or
// the start. It matches the message, not the status, which is unrecorded.
func IsMountNotShared(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	msg := strings.ToLower(e.Message)
	return strings.Contains(msg, "mounts denied") || strings.Contains(msg, "is not shared from the host")
}

func hasStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// IsUnavailable reports whether err means the daemon could not be reached
// at all, as opposed to a request it rejected.
func IsUnavailable(err error) bool {
	var e *UnavailableError
	return errors.As(err, &e)
}

// UnavailableReason classifies why the daemon could not be reached.
type UnavailableReason int

// The reasons a connection fails, each with its own remediation
// (containers.md 2.1 and 2.3). DialFailed, the unclassified reason, is the
// zero value.
const (
	DialFailed UnavailableReason = iota
	SocketMissing
	ConnectionRefused
	PermissionDenied
	// HostUnknown is a tcp:// host name that does not resolve.
	HostUnknown
	// CertificatesUnreadable is a TLS endpoint whose ca.pem, cert.pem or
	// key.pem cannot be read or parsed.
	CertificatesUnreadable
	// HandshakeFailed is a TLS handshake the daemon or the client
	// rejected: an untrusted certificate on either side.
	HandshakeFailed
	// TLSNotOffered is a TLS endpoint whose daemon answered in plain text.
	TLSNotOffered
	// TLSRequired is a plain tcp:// endpoint whose daemon requires TLS.
	TLSRequired
)

func (r UnavailableReason) String() string {
	switch r {
	case DialFailed:
		return "dial failed"
	case SocketMissing:
		return "socket missing"
	case ConnectionRefused:
		return "connection refused"
	case PermissionDenied:
		return "permission denied"
	case HostUnknown:
		return "host unknown"
	case CertificatesUnreadable:
		return "certificates unreadable"
	case HandshakeFailed:
		return "TLS handshake failed"
	case TLSNotOffered:
		return "TLS not offered"
	case TLSRequired:
		return "TLS required"
	}
	return "UnavailableReason(" + strconv.Itoa(int(r)) + ")"
}

// UnavailableError reports that the daemon could not be reached. The
// emulator's control plane keeps working; only container work is affected.
type UnavailableError struct {
	Endpoint Endpoint
	Reason   UnavailableReason
	Err      error
}

func (e *UnavailableError) Error() string {
	at := e.Endpoint.String()
	switch e.Reason {
	case SocketMissing:
		return fmt.Sprintf("docker daemon unavailable: no socket at %s; start Docker, or set DOCKER_HOST=unix:///path/to/docker.sock for Docker Desktop, Colima or Podman", at)
	case ConnectionRefused:
		if _, unix := e.Endpoint.(Unix); unix {
			return fmt.Sprintf("docker daemon unavailable: connection refused at %s; the socket exists but no daemon is listening, so start or restart Docker", at)
		}
		return fmt.Sprintf("docker daemon unavailable: connection refused at %s; nothing listens on that port, so check the host and port in DOCKER_HOST and that the daemon listens on TCP (dockerd -H tcp://0.0.0.0:2375, or a running docker:dind service)", at)
	case PermissionDenied:
		return fmt.Sprintf("docker daemon unavailable: permission denied on %s; add your user to the docker group and log in again, or set DOCKER_HOST to a socket you can use", at)
	case HostUnknown:
		return fmt.Sprintf("docker daemon unavailable: the host of %s does not resolve (%v); check the host name in DOCKER_HOST, which in CI is the docker:dind service's alias, such as docker", at, e.Err)
	case CertificatesUnreadable:
		return fmt.Sprintf("docker daemon unavailable: cannot read the TLS certificates for %s: %v; DOCKER_TLS_VERIFY is set, so understudy needs %s, %s and %s in DOCKER_CERT_PATH (default ~/.docker): point DOCKER_CERT_PATH at the directory holding them (docker:dind writes them to /certs/client), or unset DOCKER_TLS_VERIFY for a daemon without TLS",
			at, e.Err, caFile, certFile, keyFile)
	case HandshakeFailed:
		return fmt.Sprintf("docker daemon unavailable: the TLS handshake with %s failed: %v; check that DOCKER_CERT_PATH's %s signed the daemon's certificate, that the certificate names the host in DOCKER_HOST, and that the daemon trusts %s", at, e.Err, caFile, certFile)
	case TLSNotOffered:
		return fmt.Sprintf("docker daemon unavailable: %s answered without TLS although DOCKER_TLS_VERIFY is set; unset DOCKER_TLS_VERIFY for a daemon without TLS (port 2375), or point DOCKER_HOST at its TLS port (2376)", at)
	case TLSRequired:
		return fmt.Sprintf("docker daemon unavailable: %s requires TLS; set DOCKER_TLS_VERIFY=1 and DOCKER_CERT_PATH to the directory holding %s, %s and %s (docker:dind writes them to /certs/client)", at, caFile, certFile, keyFile)
	case DialFailed:
		// unclassified: the message below names the dial error
	}
	return fmt.Sprintf("docker daemon unavailable at %s: %v", at, e.Err)
}

func (e *UnavailableError) Unwrap() error { return e.Err }

func unavailable(endpoint Endpoint, err error) *UnavailableError {
	reason := DialFailed
	var dns *net.DNSError
	switch {
	case errors.Is(err, syscall.ENOENT):
		reason = SocketMissing
	case errors.Is(err, syscall.ECONNREFUSED):
		reason = ConnectionRefused
	case errors.Is(err, syscall.EACCES):
		reason = PermissionDenied
	case errors.As(err, &dns) && dns.IsNotFound:
		reason = HostUnknown
	}
	return &UnavailableError{Endpoint: endpoint, Reason: reason, Err: err}
}

// handshakeFailed classifies a failed TLS handshake: a daemon that answered
// the client hello with an HTTP response speaks plain HTTP.
func handshakeFailed(endpoint TLS, err error) *UnavailableError {
	reason := HandshakeFailed
	if record, ok := errors.AsType[tls.RecordHeaderError](err); ok && string(record.RecordHeader[:]) == "HTTP/" {
		reason = TLSNotOffered
	}
	return &UnavailableError{Endpoint: endpoint, Reason: reason, Err: err}
}

// plainToTLS is what Go's HTTP server, which dockerd runs, answers a plain
// request on a TLS listener with (net/http server.go).
const plainToTLS = "Client sent an HTTP request to an HTTPS server"

// VersionError reports a daemon whose highest API version is below the
// client's minimum.
type VersionError struct {
	Server  APIVersion
	Minimum APIVersion
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("docker daemon serves API version %s, below the minimum %s understudy needs to run Lambda containers; upgrade Docker Engine or Podman to a release that serves API %s or later",
		e.Server, e.Minimum, e.Minimum)
}

// StreamError is a failure the daemon reports inside a response that began
// with a success status: an errorDetail line in a pull, a systemerr frame in
// a log stream, or the Error member of a wait result.
type StreamError struct {
	Op      string
	Message string
}

func (e *StreamError) Error() string {
	return fmt.Sprintf("docker %s: %s", e.Op, e.Message)
}

// InvalidReferenceError reports an image reference that is not in the
// Engine's reference grammar, refused before any request is sent.
type InvalidReferenceError struct {
	Ref    string
	Reason string
}

func (e *InvalidReferenceError) Error() string {
	return fmt.Sprintf("image reference %q is invalid: %s; use a reference such as public.ecr.aws/lambda/provided:al2023 or registry.example.com:5000/team/app@sha256:<digest>", e.Ref, e.Reason)
}

// OwnershipError reports an Ownership without an instance or an owner.
type OwnershipError struct {
	Ownership Ownership
}

func (e *OwnershipError) Error() string {
	return fmt.Sprintf("ownership %+v lacks an instance or an owner, so sweeps could not tell its objects from other instances'; set both", e.Ownership)
}

// EngineLimitError reports a request the detected engine cannot honor, so
// it is refused before it is sent.
type EngineLimitError struct {
	Op     string
	Reason string
}

func (e *EngineLimitError) Error() string {
	return fmt.Sprintf("docker %s: %s", e.Op, e.Reason)
}
