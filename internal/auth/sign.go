package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// Signer is the access key and scope a client signs with. It is for tests
// and tools that play the client; the emulator only verifies.
type Signer struct {
	AccessKeyID     string
	SecretAccessKey string
	// SessionToken is sent as X-Amz-Security-Token when set.
	SessionToken string
	Region       aws.Region
	// Service is the signing name: "sqs", "s3", "monitoring".
	Service string
}

const redacted = "[REDACTED]"

// LogValue logs the key and scope; the secret and token never reach a log.
func (s Signer) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("access_key_id", s.AccessKeyID),
		slog.String("secret_access_key", redacted),
		slog.String("region", s.Region.String()),
		slog.String("service", s.Service),
	)
}

// MarshalJSON writes the LogValue view, so slog's JSONHandler redacts a
// Signer held in a struct or slice too.
func (s Signer) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		Region          string `json:"region"`
		Service         string `json:"service"`
	}{s.AccessKeyID, redacted, s.Region.String(), s.Service})
}

// String formats the signer with the secret and token redacted.
func (s Signer) String() string {
	return "{AccessKeyID:" + s.AccessKeyID + " SecretAccessKey:" + redacted + " Region:" + s.Region.String() + " Service:" + s.Service + "}"
}

// GoString is String, so %#v redacts too.
func (s Signer) GoString() string { return "auth.Signer" + s.String() }

// unsignable are the headers a signer leaves out: aws-sdk-go-v2's ignored
// set (sigv4-checksums-endpoints.md 1.3).
func unsignable(name string) bool {
	switch name {
	case "authorization", "user-agent", "x-amzn-trace-id", "expect", "transfer-encoding":
		return true
	}
	return false
}

// Sign adds X-Amz-Date, X-Amz-Security-Token and an Authorization header to
// r as aws-sdk-go-v2 does: it signs host and every header present but the
// unsignable ones. body is r's body. For S3 it sends x-amz-content-sha256
// with the body hash unless r already carries a value such as
// UNSIGNED-PAYLOAD, which then becomes the payload line.
func Sign(r *http.Request, body []byte, s Signer, now time.Time) {
	now = now.UTC()
	r.Header.Set(hdrAmzDate, iso(now))
	if s.SessionToken != "" {
		r.Header.Set(hdrSecurityToken, s.SessionToken)
	}
	sum := sha256.Sum256(body)
	payload := r.Header.Get(hdrContentSHA256)
	if payload == "" {
		payload = hex.EncodeToString(sum[:])
		if s.Service == serviceS3 {
			r.Header.Set(hdrContentSHA256, payload)
		}
	}
	cred := s.credential(now)
	signed := signedNames(r.Header)
	canonical := canonicalRequest(Normalize(r, Hashed{SHA256: sum}), s.Service, signed, "", payload)
	sig := hex.EncodeToString(hmacSHA256(signingKey(s.SecretAccessKey, cred), stringToSign(iso(now), cred.scope(), canonical)))
	r.Header.Set(hdrAuthorization, algorithm+" Credential="+s.AccessKeyID+"/"+cred.scope()+
		", SignedHeaders="+strings.Join(signed, ";")+", Signature="+sig)
}

// Presign appends the SigV4 query parameters to r.URL as the SDK presigners
// do, signing host and any headers present but the unsignable ones. S3 URLs
// sign UNSIGNED-PAYLOAD; other services sign the hash of an empty body.
func Presign(r *http.Request, s Signer, expires time.Duration, now time.Time) {
	now = now.UTC()
	cred := s.credential(now)
	signed := signedNames(r.Header)
	params := [][2]string{
		{qAlgorithm, algorithm},
		{qKeyAndScope, s.AccessKeyID + "/" + cred.scope()},
		{qDate, iso(now)},
		{qExpires, strconv.FormatInt(int64(expires/time.Second), 10)},
		{qSignedHeaders, strings.Join(signed, ";")},
	}
	if s.SessionToken != "" {
		params = append(params, [2]string{qSecurityToken, s.SessionToken})
	}
	query := r.URL.RawQuery
	for _, p := range params {
		if query != "" {
			query += "&"
		}
		query += uriEncode(p[0], false) + "=" + uriEncode(p[1], false)
	}
	r.URL.RawQuery = query

	empty := sha256.Sum256(nil)
	payload := hex.EncodeToString(empty[:])
	if s.Service == serviceS3 {
		payload = unsignedPayload
	}
	canonical := canonicalRequest(Normalize(r, Hashed{SHA256: empty}), s.Service, signed, qSignature, payload)
	sig := hex.EncodeToString(hmacSHA256(signingKey(s.SecretAccessKey, cred), stringToSign(iso(now), cred.scope(), canonical)))
	r.URL.RawQuery += "&" + qSignature + "=" + sig
}

func (s Signer) credential(now time.Time) Credential {
	return Credential{Key: s.AccessKeyID, Date: now.Format(scopeDate), Region: s.Region, Service: s.Service}
}

// signedNames is host plus every signable header name present, lowercase
// and sorted.
func signedNames(h http.Header) []string {
	names := []string{"host"}
	for name := range h {
		if lower := strings.ToLower(name); lower != "host" && !unsignable(lower) {
			names = append(names, lower)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}
