package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
)

// ChunkVerifier checks the signature chain of a signed aws-chunked body
// (sigv4-checksums-endpoints.md 3.3). The first chunk's previous signature
// is the request's header signature. The dependency rules keep auth from
// naming awschunked's types, so the S3 service adapts Chunk and Trailer to
// an awschunked.SignatureFunc by switching on the payload kind.
type ChunkVerifier struct {
	key   []byte
	time  string
	scope string
}

// ChunkSignatureVerifier returns the verifier for a's streaming body,
// signed with secret.
func ChunkSignatureVerifier(a SigV4Header, secret string) ChunkVerifier {
	return ChunkVerifier{key: signingKey(secret, a.Credential), time: iso(a.Time), scope: a.Credential.scope()}
}

// LogValue logs the request time and credential scope; the signing key,
// derived from the secret and as good as it for that day, region and
// service, never reaches a log.
func (v ChunkVerifier) LogValue() slog.Value {
	return slog.GroupValue(slog.String("time", v.time), slog.String("scope", v.scope), slog.String("key", redacted))
}

// MarshalJSON writes the LogValue view, so slog's JSONHandler redacts a
// ChunkVerifier held in a struct or slice too.
func (v ChunkVerifier) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Time  string `json:"time"`
		Scope string `json:"scope"`
		Key   string `json:"key"`
	}{v.time, v.scope, redacted})
}

// String formats the verifier with the signing key redacted.
func (v ChunkVerifier) String() string {
	return "{Time:" + v.time + " Scope:" + v.scope + " Key:" + redacted + "}"
}

// GoString is String, so %#v redacts too.
func (v ChunkVerifier) GoString() string { return "auth.ChunkVerifier" + v.String() }

// Chunk checks sig, the signature of a chunk whose data hashes to
// dataSHA256, chained to prev.
func (v ChunkVerifier) Chunk(prev string, dataSHA256 [32]byte, sig string) error {
	empty := sha256.Sum256(nil)
	sts := "AWS4-HMAC-SHA256-PAYLOAD\n" + v.time + "\n" + v.scope + "\n" + prev + "\n" +
		hex.EncodeToString(empty[:]) + "\n" + hex.EncodeToString(dataSHA256[:])
	return v.check(ChunkPart, sts, sig)
}

// Trailer checks sig, the signature of the trailer fields, each written
// "<name>:<value>\n", which hash to fieldsSHA256, chained to prev, the
// signature of the final zero-length chunk.
func (v ChunkVerifier) Trailer(prev string, fieldsSHA256 [32]byte, sig string) error {
	sts := "AWS4-HMAC-SHA256-TRAILER\n" + v.time + "\n" + v.scope + "\n" + prev + "\n" + hex.EncodeToString(fieldsSHA256[:])
	return v.check(TrailerPart, sts, sig)
}

func (v ChunkVerifier) check(part StreamPart, sts, sig string) error {
	if hmac.Equal([]byte(sig), []byte(hex.EncodeToString(hmacSHA256(v.key, sts)))) {
		return nil
	}
	return &ChunkSignatureError{Part: part, Signature: sig}
}

// StreamPart is what a streaming signature covers.
type StreamPart uint8

// StreamPart values.
const (
	ChunkPart   StreamPart = iota + 1 // a chunk's data
	TrailerPart                       // the trailer fields
)

// ChunkSignatureError is a streaming signature that does not verify. S3
// answers it with SignatureDoesNotMatch; the bytes read so far must not be
// committed.
type ChunkSignatureError struct {
	Part StreamPart
	// Signature is the signature as sent.
	Signature string
}

func (e *ChunkSignatureError) Error() string {
	what := "chunk"
	if e.Part == TrailerPart {
		what = "trailer"
	}
	return fmt.Sprintf("auth: the %s signature %q does not match the signature computed from the previous signature and the signing key; the body is rejected", what, e.Signature)
}
