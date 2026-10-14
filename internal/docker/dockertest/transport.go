package dockertest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// Transport is how the fake daemon listens.
//
//sumtype:decl
type Transport interface{ isTransport() }

// PlainTCP listens on 127.0.0.1 at an ephemeral port without TLS, as
// docker-in-docker does on 2375.
type PlainTCP struct{}

// MutualTLS listens on 127.0.0.1 at an ephemeral port with TLS and requires
// a client certificate, as dockerd --tlsverify does. The CA, and the client
// pair it signed, are written as ca.pem, cert.pem and key.pem to the
// directory docker.TLS.CertDir names; the server certificate names
// 127.0.0.1 and localhost.
type MutualTLS struct{}

func (PlainTCP) isTransport()  {}
func (MutualTLS) isTransport() {}

// listen opens the daemon's listener as Options.Transport says and sets
// socket and endpoint. dir is the daemon's private directory.
func (d *Daemon) listen(tb testing.TB, dir string) net.Listener {
	tb.Helper()
	var lc net.ListenConfig
	switch d.opts.Transport.(type) {
	case nil:
		d.socket = filepath.Join(dir, "docker.sock")
		d.endpoint = docker.Unix{Path: d.socket}
		l, err := lc.Listen(tb.Context(), "unix", d.socket)
		if err != nil {
			tb.Fatalf("dockertest: listen on %s: %v", d.socket, err)
		}
		return l
	case PlainTCP:
		l := listenLoopback(tb, &lc)
		d.endpoint = docker.TCP{Addr: l.Addr().String()}
		return l
	case MutualTLS:
		l := listenLoopback(tb, &lc)
		certDir := filepath.Join(dir, "certs")
		d.endpoint = docker.TLS{Addr: l.Addr().String(), CertDir: certDir}
		return tls.NewListener(l, serverTLS(tb, certDir))
	}
	tb.Fatalf("dockertest: unknown transport %T", d.opts.Transport)
	return nil
}

func listenLoopback(tb testing.TB, lc *net.ListenConfig) net.Listener {
	tb.Helper()
	l, err := lc.Listen(tb.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("dockertest: listen on 127.0.0.1: %v", err)
	}
	return l
}

// validity returns the fixed window every certificate is valid over: the
// fake has no clock of its own, and the window spans any date a test runs
// on.
func validity() (notBefore, notAfter time.Time) {
	return time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)
}

// serverTLS makes a CA, a server certificate for 127.0.0.1 and a client
// pair, writes the CA and the client pair to certDir as the Docker CLI
// names them, and returns the server's configuration.
func serverTLS(tb testing.TB, certDir string) *tls.Config {
	tb.Helper()
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		tb.Fatalf("dockertest: create %s: %v", certDir, err)
	}
	caKey, caCert := issue(tb, "dockertest CA", nil, nil, func(c *x509.Certificate) {
		c.IsCA = true
		c.BasicConstraintsValid = true
		c.KeyUsage = x509.KeyUsageCertSign
	})
	serverKey, serverCert := issue(tb, "dockertest daemon", caKey, caCert, func(c *x509.Certificate) {
		c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		c.IPAddresses = []net.IP{net.IP(netip.MustParseAddr("127.0.0.1").AsSlice())}
		c.DNSNames = []string{"localhost"}
	})
	clientKey, clientCert := issue(tb, "dockertest client", caKey, caCert, func(c *x509.Certificate) {
		c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	})
	writePEM(tb, filepath.Join(certDir, "ca.pem"), "CERTIFICATE", caCert.Raw)
	writePEM(tb, filepath.Join(certDir, "cert.pem"), "CERTIFICATE", clientCert.Raw)
	writePEM(tb, filepath.Join(certDir, "key.pem"), "EC PRIVATE KEY", marshalKey(tb, clientKey))
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{serverCert.Raw}, PrivateKey: serverKey}},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    roots,
		MinVersion:   tls.VersionTLS12,
	}
}

// issue makes a key and a certificate for it, signed by parent's key, or
// self-signed when parent is nil.
func issue(tb testing.TB, name string, parentKey *ecdsa.PrivateKey, parent *x509.Certificate, shape func(*x509.Certificate)) (*ecdsa.PrivateKey, *x509.Certificate) {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatalf("dockertest: generate key for %s: %v", name, err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		tb.Fatalf("dockertest: serial for %s: %v", name, err)
	}
	notBefore, notAfter := validity()
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature}
	shape(tmpl)
	signer, signerCert := key, tmpl
	if parent != nil {
		signer, signerCert = parentKey, parent
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &key.PublicKey, signer)
	if err != nil {
		tb.Fatalf("dockertest: certificate for %s: %v", name, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		tb.Fatalf("dockertest: parse certificate for %s: %v", name, err)
	}
	return key, cert
}

func marshalKey(tb testing.TB, key *ecdsa.PrivateKey) []byte {
	tb.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		tb.Fatalf("dockertest: marshal key: %v", err)
	}
	return der
}

func writePEM(tb testing.TB, path, kind string, der []byte) {
	tb.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
		tb.Fatalf("dockertest: write %s: %v", path, err)
	}
}
