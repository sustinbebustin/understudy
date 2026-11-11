package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// certNow is the wall time the stored-certificate tests start at.
var certNow = time.Date(2026, time.November, 11, 12, 0, 0, 0, time.UTC)

// openTestDataDir is a locked data directory in a temporary directory,
// released when the test ends.
func openTestDataDir(t *testing.T) *dataDir {
	t.Helper()
	d, err := openDataDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.close(); err != nil {
			t.Error(err)
		}
	})
	return d
}

// logRecord is one JSON log line listenerCertificate writes.
type logRecord struct {
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	Reason string `json:"reason"`
	Error  string `json:"error"`
}

// certificateAt runs listenerCertificate on d at now and returns the
// certificate with the log lines it wrote.
func certificateAt(t *testing.T, d *dataDir, now time.Time) (tls.Certificate, []logRecord) {
	t.Helper()
	var out bytes.Buffer
	cert, err := listenerCertificate(t.Context(), d, now, slog.New(slog.NewJSONHandler(&out, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var records []logRecord
	for line := range strings.Lines(out.String()) {
		var r logRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		records = append(records, r)
	}
	return cert, records
}

// signedLike signs a certificate this build would issue at now, changed
// by edit, with a new ECDSA P-256 key.
func signedLike(t *testing.T, now time.Time, edit func(*x509.Certificate)) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := certTemplate(now, big.NewInt(1))
	edit(template)
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Under --persist the first start stores the pair it generates, the key
// readable by its owner only, and logs nothing; a later start reuses it.
func TestListenerCertificateIsStoredAndReused(t *testing.T) {
	d := openTestDataDir(t)
	first, logs := certificateAt(t, d, certNow)
	if len(logs) != 0 {
		t.Errorf("the first start logged %+v, want nothing", logs)
	}
	info, err := os.Stat(filepath.Join(d.path, tlsKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("%s has mode %o, want 0600", tlsKeyFile, mode)
	}
	// A start late in the certificate's life, but more than a day before
	// its expiry, still reuses it.
	later := certNow.Add(certValidity - 2*certRenewBefore)
	again, logs := certificateAt(t, d, later)
	if len(logs) != 0 {
		t.Errorf("the second start logged %+v, want nothing", logs)
	}
	if !again.Leaf.Equal(first.Leaf) {
		t.Error("the second start presents a new certificate, want the stored one")
	}
}

// Without a data directory every start generates a new certificate.
func TestListenerCertificateWithoutPersistenceIsNew(t *testing.T) {
	first, logs := certificateAt(t, nil, certNow)
	second, more := certificateAt(t, nil, certNow)
	if first.Leaf.Equal(second.Leaf) {
		t.Error("two starts without persistence presented the same certificate")
	}
	if len(logs)+len(more) != 0 {
		t.Errorf("starts without persistence logged %+v %+v", logs, more)
	}
}

// A stored pair that is unreadable, mismatched, issued for other names or
// constraints, or not valid for at least another day is replaced: the
// start serves a new certificate, stores it, and logs at Warn why.
func TestListenerCertificateReplacesAStoredOneThatDoesNotFit(t *testing.T) {
	store := func(cert tls.Certificate) func(*testing.T, *dataDir) {
		return func(t *testing.T, d *dataDir) {
			t.Helper()
			if err := d.storeCertificate(cert); err != nil {
				t.Fatal(err)
			}
		}
	}
	write := func(name, content string) func(*testing.T, *dataDir) {
		return func(t *testing.T, d *dataDir) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(d.path, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	generated := func(t *testing.T, now time.Time) tls.Certificate {
		t.Helper()
		cert, err := selfSigned(now)
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}
	a, b := generated(t, certNow), generated(t, certNow)
	for _, tc := range []struct {
		name   string
		stored func(*testing.T, *dataDir)
		reason string
	}{
		{"expired", store(generated(t, certNow.Add(-certValidity-time.Hour))), "expired at"},
		{"expiring within a day", store(generated(t, certNow.Add(-certValidity+12*time.Hour))), "within 24h0m0s"},
		{"not yet valid", store(generated(t, certNow.Add(48*time.Hour))), "not valid until"},
		{"unreadable certificate", func(t *testing.T, d *dataDir) {
			t.Helper()
			store(a)(t, d)
			write(tlsCertFile, "not a certificate")(t, d)
		}, "are not a certificate and its key"},
		{"key missing", func(t *testing.T, d *dataDir) {
			t.Helper()
			store(a)(t, d)
			if err := os.Remove(filepath.Join(d.path, tlsKeyFile)); err != nil {
				t.Fatal(err)
			}
		}, "reading " + tlsKeyFile},
		{"key of another certificate", store(tls.Certificate{Certificate: a.Certificate, PrivateKey: b.PrivateKey}), "private key does not match public key"},
		{"other DNS names", store(signedLike(t, certNow, func(c *x509.Certificate) { c.DNSNames = []string{"localhost"} })), "DNS names"},
		{"other IP addresses", store(signedLike(t, certNow, func(c *x509.Certificate) { c.IPAddresses = nil })), "IP addresses"},
		{"name constraints not critical", store(signedLike(t, certNow, func(c *x509.Certificate) { c.PermittedDNSDomainsCritical = false })), "critical name constraints"},
		{"no IP range constraint", store(signedLike(t, certNow, func(c *x509.Certificate) { c.PermittedIPRanges = nil })), "permitted IP ranges"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDataDir(t)
			tc.stored(t, d)
			cert, logs := certificateAt(t, d, certNow)
			if len(logs) != 1 || logs[0].Level != "WARN" || !strings.Contains(logs[0].Reason, tc.reason) || !strings.Contains(logs[0].Msg, "env --tls") {
				t.Fatalf("logs = %+v, want one Warn line whose reason names %q", logs, tc.reason)
			}
			if !certNow.After(cert.Leaf.NotBefore) || cert.Leaf.NotAfter.Sub(certNow) != certValidity {
				t.Errorf("the replacement is valid %s to %s, want a new certificate from %s", cert.Leaf.NotBefore, cert.Leaf.NotAfter, certNow)
			}
			again, logs := certificateAt(t, d, certNow)
			if len(logs) != 0 || !again.Leaf.Equal(cert.Leaf) {
				t.Errorf("the next start logged %+v and reused the replacement: %t, want it stored", logs, again.Leaf.Equal(cert.Leaf))
			}
		})
	}
}

// A pair that cannot be stored is logged, saying an earlier CA bundle is
// stale, and the new certificate is still served.
func TestListenerCertificateServesWhenItCannotBeStored(t *testing.T) {
	d := openTestDataDir(t)
	// A non-empty directory where the key goes can be neither read nor
	// replaced.
	if err := os.MkdirAll(filepath.Join(d.path, tlsKeyFile, "blocked"), 0o750); err != nil {
		t.Fatal(err)
	}
	cert, logs := certificateAt(t, d, certNow)
	if cert.Leaf == nil || cert.Leaf.NotAfter.Sub(certNow) != certValidity {
		t.Fatalf("certificate = %+v, want a new one", cert.Leaf)
	}
	var stored bool
	for _, r := range logs {
		if r.Level == "WARN" && strings.Contains(r.Msg, "could not be stored") && strings.Contains(r.Msg, "stale") && r.Error != "" {
			stored = true
		}
	}
	if !stored {
		t.Errorf("logs = %+v, want a Warn line that the certificate could not be stored and an earlier CA bundle is stale", logs)
	}
	if _, err := os.Stat(filepath.Join(d.path, tlsCertFile)); err == nil {
		t.Errorf("%s was written although its key could not be", tlsCertFile)
	}
}

// The self-signed certificate is installed as a trust root, so its key must
// not be able to vouch for any name beyond localhost and loopback: a
// certificate it signs for another name fails verification.
func TestSelfSignedRootCannotVouchForOtherNames(t *testing.T) {
	now := time.Date(2026, time.October, 13, 0, 0, 0, 0, time.UTC)
	root, err := selfSigned(now)
	if err != nil {
		t.Fatal(err)
	}
	if !root.Leaf.PermittedDNSDomainsCritical {
		t.Error("the name constraints are not critical, so a client that does not know them would ignore them")
	}
	roots := x509.NewCertPool()
	roots.AddCert(root.Leaf)
	signed := func(t *testing.T, dns string, ip net.IP) *x509.Certificate {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(2),
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		if dns != "" {
			template.DNSNames = []string{dns}
		}
		if ip != nil {
			template.IPAddresses = []net.IP{ip}
		}
		der, err := x509.CreateCertificate(rand.Reader, template, root.Leaf, &key.PublicKey, root.PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}
	verify := func(cert *x509.Certificate, name string) error {
		_, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: name, CurrentTime: now})
		return err
	}
	for _, tc := range []struct {
		name string
		dns  string
		ip   net.IP
	}{
		{"example.com", "example.com", nil},
		{"localhost.example.com", "localhost.example.com", nil},
		{"10.0.0.1", "", net.IPv4(10, 0, 0, 1)},
	} {
		err := verify(signed(t, tc.dns, tc.ip), tc.name)
		if invalid, ok := errors.AsType[x509.CertificateInvalidError](err); !ok || invalid.Reason != x509.CANotAuthorizedForThisName {
			t.Errorf("a certificate the root signed for %s verifies with %v, want CANotAuthorizedForThisName", tc.name, err)
		}
	}
	for _, tc := range []struct {
		name string
		dns  string
		ip   net.IP
	}{
		{"bucket.localhost", "bucket.localhost", nil},
		{"127.0.0.2", "", net.IPv4(127, 0, 0, 2)},
		{"::1", "", net.IPv6loopback},
	} {
		if err := verify(signed(t, tc.dns, tc.ip), tc.name); err != nil {
			t.Errorf("a certificate the root signed for %s does not verify: %v", tc.name, err)
		}
	}
}
