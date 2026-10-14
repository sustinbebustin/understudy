package envelope

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // SNS signature version 1 is SHA1withRSA by definition (sns.md 5.6)
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// Version is a topic's SignatureVersion.
type Version uint8

// V1 and V2 are the two SignatureVersion values (sns.md 5.6).
const (
	V1 Version = iota + 1 // SHA1withRSA, the default
	V2                    // SHA256withRSA
)

// String returns the attribute value, "1" or "2".
func (v Version) String() string {
	switch v {
	case V1:
		return "1"
	case V2:
		return "2"
	default:
		return fmt.Sprintf("Version(%d)", uint8(v))
	}
}

// ParseVersion returns the Version a SignatureVersion attribute names.
func ParseVersion(s string) (Version, bool) {
	switch s {
	case "1":
		return V1, true
	case "2":
		return V2, true
	default:
		return 0, false
	}
}

// VersionError is a persisted SignatureVersion that names no Version.
type VersionError struct {
	Text string
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("sns signature version %q is not 1 or 2", e.Text)
}

// MarshalText returns the attribute value; a Version that is not V1 or V2
// is an error.
func (v Version) MarshalText() ([]byte, error) {
	if _, ok := ParseVersion(v.String()); !ok {
		return nil, &VersionError{Text: v.String()}
	}
	return []byte(v.String()), nil
}

// UnmarshalText parses "1" or "2".
func (v *Version) UnmarshalText(text []byte) error {
	parsed, ok := ParseVersion(string(text))
	if !ok {
		return &VersionError{Text: string(text)}
	}
	*v = parsed
	return nil
}

// Signer signs a notification's string to sign.
type Signer interface {
	Sign(v Version, stringToSign []byte) ([]byte, error)
}

// Key is a signing key and its self-signed certificate, both DER: what
// the SNS service persists per account and region.
type Key struct {
	// Private is the PKCS #1 RSA private key.
	Private []byte
	// Certificate is the X.509 certificate of the public key.
	Certificate []byte
}

// keyBits is the RSA modulus size of the signing key. sns.md does not record
// the size of AWS's SNS signing keys; 2048 is UNVERIFIED.
const keyBits = 2048

// NewKey makes a key from crypto/rand and a certificate for it naming
// subject.
func NewKey(subject string) (Key, error) {
	priv, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return Key{}, &KeyError{Op: "generate the RSA key", Err: err}
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: subject},
		NotBefore:             time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	cert, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return Key{}, &KeyError{Op: "create the certificate", Err: err}
	}
	return Key{Private: x509.MarshalPKCS1PrivateKey(priv), Certificate: cert}, nil
}

// Signer returns the signer of k's private key.
func (k Key) Signer() (Signer, error) {
	priv, err := x509.ParsePKCS1PrivateKey(k.Private)
	if err != nil {
		return nil, &KeyError{Op: "parse the stored RSA key", Err: err}
	}
	return rsaSigner{key: priv}, nil
}

// CertificatePEM returns the certificate in PEM, as SigningCertURL serves it.
func (k Key) CertificatePEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: k.Certificate})
}

// CertificateName is the file name of the certificate in SigningCertURL:
// SimpleNotificationService-<hex>.pem, the hex the first 16 bytes of the
// certificate's SHA-256, as long as the recorded names (sns.md 5.1).
func (k Key) CertificateName() string {
	sum := sha256.Sum256(k.Certificate)
	return "SimpleNotificationService-" + hex.EncodeToString(sum[:16]) + ".pem"
}

// KeyError is a signing key that could not be made or read.
type KeyError struct {
	Op  string
	Err error
}

func (e *KeyError) Error() string {
	return "sns signing key: could not " + e.Op + ": " + e.Err.Error()
}

func (e *KeyError) Unwrap() error { return e.Err }

// rsaSigner signs with RSA PKCS #1 v1.5.
type rsaSigner struct {
	key *rsa.PrivateKey
}

// Sign signs with SHA1withRSA for version 1 and SHA256withRSA for 2.
func (s rsaSigner) Sign(v Version, stringToSign []byte) ([]byte, error) {
	var (
		hash   crypto.Hash
		digest []byte
	)
	switch v {
	case V1:
		sum := sha1.Sum(stringToSign) //nolint:gosec // SignatureVersion 1 is SHA1withRSA
		hash, digest = crypto.SHA1, sum[:]
	case V2:
		sum := sha256.Sum256(stringToSign)
		hash, digest = crypto.SHA256, sum[:]
	default:
		return nil, &KeyError{Op: "sign", Err: fmt.Errorf("unknown signature version %s", v)}
	}
	sig, err := rsa.SignPKCS1v15(nil, s.key, hash, digest)
	if err != nil {
		return nil, &KeyError{Op: "sign", Err: err}
	}
	return sig, nil
}
