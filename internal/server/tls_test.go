package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"
)

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
