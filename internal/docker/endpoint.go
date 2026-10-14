package docker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Endpoint is where the daemon listens and how the client reaches it.
//
//sumtype:decl
type Endpoint interface {
	// String is the endpoint as DOCKER_HOST spells it.
	String() string
	// Transport names how the client talks to the daemon, for doctor.
	Transport() string
	dial(ctx context.Context, d *net.Dialer) (net.Conn, error)
}

// Unix is the Engine's unix socket at Path; empty is DefaultSocket.
type Unix struct{ Path string }

// TCP is a daemon listening on plain TCP at Addr (host:port), as
// docker-in-docker serves without TLS on port 2375.
type TCP struct{ Addr string }

// TLS is a daemon listening on TCP at Addr that the client verifies, as
// DOCKER_TLS_VERIFY asks: the daemon's certificate must be signed by
// CertDir/ca.pem, and the client presents CertDir/cert.pem with
// CertDir/key.pem, the file names the Docker CLI reads from
// DOCKER_CERT_PATH (containers.md 2.1). The files are read on every new
// connection, so one written after New is picked up.
type TLS struct {
	Addr    string
	CertDir string
}

func (u Unix) String() string { return "unix://" + u.path() }
func (t TCP) String() string  { return "tcp://" + t.Addr }
func (t TLS) String() string  { return "tcp://" + t.Addr }

// Transport names the unix socket.
func (Unix) Transport() string { return "unix socket" }

// Transport names plain TCP.
func (TCP) Transport() string { return "TCP without TLS" }

// Transport names TLS and where its certificates come from.
func (t TLS) Transport() string { return "TCP with TLS, certificates from " + t.CertDir }

func (u Unix) path() string {
	if u.Path == "" {
		return DefaultSocket
	}
	return u.Path
}

// The file names the Docker CLI reads from DOCKER_CERT_PATH.
const (
	caFile   = "ca.pem"
	certFile = "cert.pem"
	keyFile  = "key.pem"
)

// dial opens a connection to the socket; a failure is an *UnavailableError.
func (u Unix) dial(ctx context.Context, d *net.Dialer) (net.Conn, error) {
	conn, err := d.DialContext(ctx, "unix", u.path())
	if err != nil {
		return nil, unavailable(u, err)
	}
	return conn, nil
}

// dial opens a TCP connection; a failure is an *UnavailableError.
func (t TCP) dial(ctx context.Context, d *net.Dialer) (net.Conn, error) {
	conn, err := d.DialContext(ctx, "tcp", t.Addr)
	if err != nil {
		return nil, unavailable(t, err)
	}
	return conn, nil
}

// dial reads the certificates, opens a TCP connection and completes the
// handshake; a failure is an *UnavailableError.
func (t TLS) dial(ctx context.Context, d *net.Dialer) (net.Conn, error) {
	cfg, err := t.config()
	if err != nil {
		return nil, &UnavailableError{Endpoint: t, Reason: CertificatesUnreadable, Err: err}
	}
	conn, err := d.DialContext(ctx, "tcp", t.Addr)
	if err != nil {
		return nil, unavailable(t, err)
	}
	tc := tls.Client(conn, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		conn.Close() //nolint:gosec // the handshake error is the one to report
		return nil, handshakeFailed(t, err)
	}
	return tc, nil
}

// config reads the CA and the client key pair from CertDir.
func (t TLS) config() (*tls.Config, error) {
	ca, err := os.ReadFile(filepath.Join(t.CertDir, caFile))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("%s holds no PEM certificate", filepath.Join(t.CertDir, caFile))
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(t.CertDir, certFile), filepath.Join(t.CertDir, keyFile))
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(t.Addr)
	if err != nil {
		return nil, fmt.Errorf("address %q is not host:port: %w", t.Addr, err)
	}
	return &tls.Config{
		RootCAs:      roots,
		Certificates: []tls.Certificate{pair},
		ServerName:   host,
		MinVersion:   tls.VersionTLS12,
	}, nil
}
