package auth

import (
	"encoding/hex"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// Auth is how a request is authenticated, as Parse found it.
//
//sumtype:decl
type Auth interface {
	isAuth()
	// RegionSource is the region the authentication asserts: the SigV4
	// credential scope, or none (ARCHITECTURE.md 7).
	RegionSource() aws.RegionSource
}

// Credential is a SigV4 credential scope with its access key:
// <key>/<yyyymmdd>/<region>/<service>/aws4_request.
type Credential struct {
	Key string
	// Date is the scope date, yyyymmdd, as signed.
	Date   string
	Region aws.Region
	// Service is the signing name: "sqs", "s3", "monitoring".
	Service string
}

func (c Credential) scope() string {
	return c.Date + "/" + c.Region.String() + "/" + c.Service + "/" + terminator
}

// SigV4Header is an Authorization: AWS4-HMAC-SHA256 header.
type SigV4Header struct {
	Credential Credential
	// SignedHeaders are the lowercase header names the client signed, in its
	// order; the verifier signs exactly these.
	SignedHeaders []string
	Signature     string
	PayloadHash   PayloadHash
	// Time is the request time from X-Amz-Date, else Date.
	Time time.Time
}

// SigV4Query is a presigned URL: the X-Amz-* query parameters.
type SigV4Query struct {
	Credential    Credential
	SignedHeaders []string
	Signature     string
	PayloadHash   PayloadHash
	// Time is X-Amz-Date, the signer's wall time.
	Time time.Time
	// Expires is X-Amz-Expires, at most seven days.
	Expires time.Duration
}

// SigV2Header is an Authorization: AWS <key>:<signature> header (S3 only).
type SigV2Header struct {
	Key       string
	Signature string
	// Time is the request time from X-Amz-Date, else Date.
	Time time.Time
	// Stamp is the header Time was read from, as sent; S3 echoes it in
	// RequestTimeTooSkewed.
	Stamp string
}

// SigV2Query is a SigV2 presigned S3 URL, which boto3 produces by default in
// the legacy regions (sigv4-checksums-endpoints.md 2.3).
type SigV2Query struct {
	Key       string
	Signature string
	// Expires is the Expires parameter, an absolute instant.
	Expires time.Time
}

// Anonymous is a request without credentials.
type Anonymous struct{}

// RegionSource is the credential scope's region.
func (a SigV4Header) RegionSource() aws.RegionSource {
	return aws.CredentialScope{Region: a.Credential.Region}
}

// RegionSource is the credential scope's region.
func (a SigV4Query) RegionSource() aws.RegionSource {
	return aws.CredentialScope{Region: a.Credential.Region}
}

// RegionSource is None: SigV2 carries no region.
func (SigV2Header) RegionSource() aws.RegionSource { return aws.None{} }

// RegionSource is None: SigV2 carries no region.
func (SigV2Query) RegionSource() aws.RegionSource { return aws.None{} }

// RegionSource is None.
func (Anonymous) RegionSource() aws.RegionSource { return aws.None{} }

func (SigV4Header) isAuth() {}
func (SigV4Query) isAuth()  {}
func (SigV2Header) isAuth() {}
func (SigV2Query) isAuth()  {}
func (Anonymous) isAuth()   {}

// AccessKey returns the access key a request names, and false for an
// anonymous request.
func AccessKey(a Auth) (string, bool) {
	switch a := a.(type) {
	case SigV4Header:
		return a.Credential.Key, true
	case SigV4Query:
		return a.Credential.Key, true
	case SigV2Header:
		return a.Key, true
	case SigV2Query:
		return a.Key, true
	case Anonymous:
		return "", false
	}
	return "", false
}

// PayloadHash is the x-amz-content-sha256 value, the last line of the
// canonical request (sigv4-checksums-endpoints.md 3.1).
//
//sumtype:decl
type PayloadHash interface{ isPayloadHash() }

// Hex is the SHA-256 of a single-chunk body.
type Hex struct{ SHA256 [32]byte }

// Unsigned is UNSIGNED-PAYLOAD.
type Unsigned struct{}

// StreamingUnsignedTrailer is STREAMING-UNSIGNED-PAYLOAD-TRAILER, what the
// SDKs send for streamed uploads over HTTPS.
type StreamingUnsignedTrailer struct{}

// StreamingSigned is STREAMING-AWS4-HMAC-SHA256-PAYLOAD.
type StreamingSigned struct{}

// StreamingSignedTrailer is STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER.
type StreamingSignedTrailer struct{}

// NotSent is a request without x-amz-content-sha256: the verifier hashes the
// body itself, as botocore, the CLI and Go expect for non-S3 services. Parse
// never returns it for an S3 Authorization header, which S3 requires to
// carry the header (MissingPayloadHash).
type NotSent struct{}

func (Hex) isPayloadHash()                      {}
func (Unsigned) isPayloadHash()                 {}
func (StreamingUnsignedTrailer) isPayloadHash() {}
func (StreamingSigned) isPayloadHash()          {}
func (StreamingSignedTrailer) isPayloadHash()   {}
func (NotSent) isPayloadHash()                  {}

// The x-amz-content-sha256 literals.
const (
	unsignedPayload          = "UNSIGNED-PAYLOAD"
	streamingUnsignedTrailer = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	streamingSigned          = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamingSignedTrailer   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
)

// wire returns the header value for h, and false for NotSent.
func wire(h PayloadHash) (string, bool) {
	switch h := h.(type) {
	case Hex:
		return hex.EncodeToString(h.SHA256[:]), true
	case Unsigned:
		return unsignedPayload, true
	case StreamingUnsignedTrailer:
		return streamingUnsignedTrailer, true
	case StreamingSigned:
		return streamingSigned, true
	case StreamingSignedTrailer:
		return streamingSignedTrailer, true
	case NotSent:
		return "", false
	}
	return "", false
}
