package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"os"
	"slices"
	"time"
)

// certValidity is how long the self-signed certificate is valid. Without
// persistence a new one is generated at every start; under --persist the
// stored one is reused until it is within certRenewBefore of expiring.
const certValidity = 365 * 24 * time.Hour

// certRenewBefore is how long before its expiry a stored certificate is
// replaced at start, so a serve started just before it expires does not
// stop being trusted while it runs.
const certRenewBefore = 24 * time.Hour

// Files under the data directory holding the TLS listener's key and
// certificate in PEM.
const (
	tlsKeyFile  = "tls.key"
	tlsCertFile = "tls.crt"
)

// errNoStoredCertificate is a data directory holding neither TLS file, as
// at the first start under --persist.
var errNoStoredCertificate = errors.New("no TLS certificate is stored")

// listenerCertificate returns the TLS listener's certificate. Without a
// data directory (d nil) it is a new one, and nothing is written. With
// one, the stored pair is reused when it still fits this build and now
// (storedCertificate); otherwise a new pair is generated and stored, and
// the replacement of a stored pair is logged at Warn with its reason. A
// failure to store the new pair is logged and the new certificate still
// served. now is wall time.
func listenerCertificate(ctx context.Context, d *dataDir, now time.Time, log *slog.Logger) (tls.Certificate, error) {
	if d == nil {
		return selfSigned(now)
	}
	stored, err := d.storedCertificate(now)
	if err == nil {
		return stored, nil
	}
	cert, gerr := selfSigned(now)
	if gerr != nil {
		return tls.Certificate{}, gerr
	}
	if !errors.Is(err, errNoStoredCertificate) {
		log.LogAttrs(ctx, slog.LevelWarn, "replaced the stored TLS certificate with a new one; a CA bundle written before this start is stale, so run understudy env --tls again",
			slog.String("reason", err.Error()), slog.String("dir", d.path))
	}
	if err := d.storeCertificate(cert); err != nil {
		log.LogAttrs(ctx, slog.LevelWarn, "serving with a new TLS certificate that could not be stored, so the next start makes another; a CA bundle written before this start is stale, so run understudy env --tls again",
			slog.String("error", err.Error()), slog.String("dir", d.path))
	}
	return cert, nil
}

// storedCertificate reads the key and certificate stored under d. It is
// errNoStoredCertificate when neither file exists, and otherwise an error
// naming why the pair is not reused: a file is unreadable, they do not
// hold a certificate and its key, the certificate is not what this build
// issues (fitsThisBuild), or it is not valid from now until
// certRenewBefore later.
func (d *dataDir) storedCertificate(now time.Time) (tls.Certificate, error) {
	certPEM, cerr := d.root.ReadFile(tlsCertFile)
	keyPEM, kerr := d.root.ReadFile(tlsKeyFile)
	switch {
	case errors.Is(cerr, fs.ErrNotExist) && errors.Is(kerr, fs.ErrNotExist):
		return tls.Certificate{}, errNoStoredCertificate
	case cerr != nil:
		return tls.Certificate{}, fmt.Errorf("reading %s: %w", tlsCertFile, cerr)
	case kerr != nil:
		return tls.Certificate{}, fmt.Errorf("reading %s: %w", tlsKeyFile, kerr)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("%s and %s are not a certificate and its key: %w", tlsCertFile, tlsKeyFile, err)
	}
	if err := fitsThisBuild(cert.Leaf); err != nil {
		return tls.Certificate{}, fmt.Errorf("%s is not the certificate this build issues: %w", tlsCertFile, err)
	}
	switch leaf := cert.Leaf; {
	case now.Before(leaf.NotBefore):
		return tls.Certificate{}, fmt.Errorf("%s is not valid until %s", tlsCertFile, leaf.NotBefore.UTC().Format(time.RFC3339))
	case !now.Before(leaf.NotAfter):
		return tls.Certificate{}, fmt.Errorf("%s expired at %s", tlsCertFile, leaf.NotAfter.UTC().Format(time.RFC3339))
	case leaf.NotAfter.Sub(now) < certRenewBefore:
		return tls.Certificate{}, fmt.Errorf("%s expires at %s, within %s", tlsCertFile, leaf.NotAfter.UTC().Format(time.RFC3339), certRenewBefore)
	}
	return cert, nil
}

// storeCertificate writes cert's key (mode 0600) and certificate under d,
// each through a temporary file renamed into place. A crash between the
// two renames leaves a key and certificate that do not match, which the
// next start replaces.
func (d *dataDir) storeCertificate(cert tls.Certificate) error {
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return fmt.Errorf("server: encoding the TLS key: %w", err)
	}
	if err := writeFileAtomic(d.root, tlsKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(d.root, tlsCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o644); err != nil {
		return err
	}
	if err := syncDir(d.root); err != nil {
		return fmt.Errorf("server: syncing the data directory after storing the TLS certificate: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to name under root with mode perm, through a
// synced temporary file renamed into place, so name is either its old
// content or data. The temporary file is removed when the write fails.
func writeFileAtomic(root *os.Root, name string, data []byte, perm os.FileMode) error {
	tmp := name + "." + rand.Text() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("server: creating %s: %w", tmp, err)
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("server: writing %s; %s is unchanged: %w", tmp, name, errors.Join(err, root.Remove(tmp)))
	}
	if err := root.Rename(tmp, name); err != nil {
		return fmt.Errorf("server: moving %s into place as %s; %s is unchanged: %w", tmp, name, name, errors.Join(err, root.Remove(tmp)))
	}
	return nil
}

// certTemplate is the certificate this build issues, valid from an hour
// before now until certValidity after it. selfSigned signs it and
// fitsThisBuild compares a stored certificate with it.
func certTemplate(now time.Time, serial *big.Int) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "understudy local endpoint", Organization: []string{"understudy"}},
		DNSNames:              []string{"localhost", "*.localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		// "localhost" permits localhost and every name under it.
		PermittedDNSDomains:         []string{"localhost"},
		PermittedIPRanges:           loopbackRanges(),
		PermittedDNSDomainsCritical: true,
	}
}

// selfSigned generates the TLS listener's certificate: an ECDSA P-256 key
// and a self-signed certificate for localhost, *.localhost and 127.0.0.1
// (ADR 0013), valid from an hour before now, so a client clock slightly
// behind still accepts it. It is its own CA, so a client trusts it by
// adding it as a root (AWS_CA_BUNDLE). Critical name constraints limit what
// it can vouch for to localhost, names under it and loopback addresses, so
// installing it as a root never lets its key sign for any other name. now
// is wall time.
func selfSigned(now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("server: generating the TLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("server: drawing the certificate serial number: %w", err)
	}
	template := certTemplate(now, serial)
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("server: creating the self-signed certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("server: reading back the self-signed certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// fitsThisBuild reports how c differs from what certTemplate issues, in
// key, names, usages and name constraints, so a stored certificate from a
// build that issued other ones is replaced (nil when it fits).
func fitsThisBuild(c *x509.Certificate) error {
	want := certTemplate(time.Time{}, nil)
	if key, ok := c.PublicKey.(*ecdsa.PublicKey); !ok || key.Curve != elliptic.P256() {
		return fmt.Errorf("its key is %s, not ECDSA P-256", c.PublicKeyAlgorithm)
	}
	ipEqual := func(a, b net.IP) bool { return a.Equal(b) }
	rangeEqual := func(a, b *net.IPNet) bool { return a.IP.Equal(b.IP) && bytes.Equal(a.Mask, b.Mask) }
	for _, check := range []struct {
		what      string
		fits      bool
		got, want any
	}{
		{"subject", c.Subject.String() == want.Subject.String(), c.Subject, want.Subject},
		{"DNS names", slices.Equal(c.DNSNames, want.DNSNames), c.DNSNames, want.DNSNames},
		{"IP addresses", slices.EqualFunc(c.IPAddresses, want.IPAddresses, ipEqual), c.IPAddresses, want.IPAddresses},
		{"email addresses and URIs", len(c.EmailAddresses) == 0 && len(c.URIs) == 0, len(c.EmailAddresses) + len(c.URIs), 0},
		{"CA flag", c.BasicConstraintsValid && c.IsCA, c.IsCA, want.IsCA},
		{"key usage", c.KeyUsage == want.KeyUsage, c.KeyUsage, want.KeyUsage},
		{"extended key usage", slices.Equal(c.ExtKeyUsage, want.ExtKeyUsage) && len(c.UnknownExtKeyUsage) == 0, c.ExtKeyUsage, want.ExtKeyUsage},
		{"permitted DNS domains", slices.Equal(c.PermittedDNSDomains, want.PermittedDNSDomains), c.PermittedDNSDomains, want.PermittedDNSDomains},
		{"permitted IP ranges", slices.EqualFunc(c.PermittedIPRanges, want.PermittedIPRanges, rangeEqual), c.PermittedIPRanges, want.PermittedIPRanges},
		{"critical name constraints", c.PermittedDNSDomainsCritical == want.PermittedDNSDomainsCritical, c.PermittedDNSDomainsCritical, want.PermittedDNSDomainsCritical},
		{
			"other name constraints",
			len(c.ExcludedDNSDomains)+len(c.ExcludedIPRanges)+len(c.PermittedEmailAddresses)+len(c.ExcludedEmailAddresses)+len(c.PermittedURIDomains)+len(c.ExcludedURIDomains) == 0,
			"some", "none",
		},
	} {
		if !check.fits {
			return fmt.Errorf("its %s are %v, not %v", check.what, check.got, check.want)
		}
	}
	return nil
}

// loopbackRanges are the addresses the certificate may vouch for:
// 127.0.0.0/8 and ::1.
func loopbackRanges() []*net.IPNet {
	return []*net.IPNet{
		{IP: net.IPv4(127, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)},
		{IP: net.IPv6loopback, Mask: net.CIDRMask(128, 128)},
	}
}
