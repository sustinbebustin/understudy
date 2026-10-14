package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// serviceS3 is S3's signing name; S3 alone signs the path as sent.
const serviceS3 = "s3"

// Normalized is the part of a request a signature covers. Build it with
// Normalize.
type Normalized struct {
	Method string
	// Host is the Host header, port included, which net/http moves out of
	// Header.
	Host string
	// Path is the escaped path exactly as the client sent it.
	Path string
	// RawQuery is the query exactly as the client sent it.
	RawQuery string
	Header   http.Header
	Body     Body
}

// Body is what the verifier knows about the request body.
//
//sumtype:decl
type Body interface{ isBody() }

// Hashed is a body the caller read and hashed: non-S3 requests, whose
// signatures cover the body hash whether or not x-amz-content-sha256 is sent.
type Hashed struct{ SHA256 [32]byte }

// Unread is a body not read before verification, so the signed
// x-amz-content-sha256 value stands in for it: a SigV2 request, whose
// signature covers no body, or the streamed input of an S3 operation
// (PutObject, UploadPart), which S3 checks against the value at end of
// body. Every other body is Hashed, or the signature would not bind it.
type Unread struct{}

func (Hashed) isBody() {}
func (Unread) isBody() {}

// Normalize captures r for verification. It reads the path and query from
// r.RequestURI on a server request, so they are exactly what the client
// signed, and from r.URL on a client request.
func Normalize(r *http.Request, body Body) Normalized {
	path, query := r.URL.EscapedPath(), r.URL.RawQuery
	if strings.HasPrefix(r.RequestURI, "/") {
		path, query, _ = strings.Cut(r.RequestURI, "?")
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	return Normalized{Method: r.Method, Host: host, Path: path, RawQuery: query, Header: r.Header.Clone(), Body: body}
}

// withoutSessionToken returns a copy of req whose X-Amz-Security-Token header
// and query values read [REDACTED], for showing a canonical request or
// string to sign in an error. The query name is matched in any case: a SigV2
// presigned URL carries it as x-amz-security-token.
func (req Normalized) withoutSessionToken() Normalized {
	out := req
	out.Header = req.Header.Clone()
	if _, ok := out.Header[hdrSecurityToken]; ok {
		out.Header.Set(hdrSecurityToken, redacted)
	}
	parts := strings.Split(req.RawQuery, "&")
	for i, part := range parts {
		if k, _, _ := strings.Cut(part, "="); strings.EqualFold(unescape(k), qSecurityToken) {
			parts[i] = k + "=" + redacted
		}
	}
	out.RawQuery = strings.Join(parts, "&")
	return out
}

// SessionToken returns the session token r carries: the
// X-Amz-Security-Token header, else the query parameter of that name in
// any case, since a SigV2 presigned URL carries it as
// x-amz-security-token. It reports false when r carries none.
func SessionToken(r *http.Request) (string, bool) {
	if v := r.Header.Values(hdrSecurityToken); len(v) > 0 {
		return v[0], true
	}
	for part := range strings.SplitSeq(r.URL.RawQuery, "&") {
		if k, v, _ := strings.Cut(part, "="); strings.EqualFold(unescape(k), qSecurityToken) {
			return unescape(v), true
		}
	}
	return "", false
}

// presignParameter reports whether name is a query parameter a SigV4
// presign adds for its own signature, which HoistedHeader never reads as a
// header.
func presignParameter(name string) bool {
	for _, p := range []string{qAlgorithm, qKeyAndScope, qDate, qExpires, qSignedHeaders, qSignature, qSecurityToken} {
		if strings.EqualFold(name, p) {
			return true
		}
	}
	return false
}

// HoistedHeader is the header a presigned SigV4 request stands for: r's
// header plus each x-amz-* parameter its query carries that the header
// does not, since a presign moves the request's x-amz-* headers into the
// query (JS v3 hoists x-amz-content-sha256, x-amz-trailer and
// x-amz-decoded-content-length), apart from the presign's own parameters.
// Names compare case-insensitively, as header names do; of two query
// spellings of one name, the first in byte order wins. r's header is not
// changed.
func HoistedHeader(r *http.Request) http.Header {
	h := r.Header.Clone()
	if h == nil {
		h = http.Header{}
	}
	q := r.URL.Query()
	for _, name := range slices.Sorted(maps.Keys(q)) {
		key := http.CanonicalHeaderKey(name)
		if !strings.HasPrefix(key, "X-Amz-") || presignParameter(key) || len(h.Values(key)) > 0 {
			continue
		}
		h[key] = slices.Clone(q[name])
	}
	return h
}

// canonicalRequest is sigv4-checksums-endpoints.md 1.2. omit, when not
// empty, names a query parameter left out (X-Amz-Signature on presigned
// URLs); a header-signed request passes "" and omits nothing.
func canonicalRequest(req Normalized, service string, signed []string, omit, payload string) string {
	var b strings.Builder
	b.WriteString(req.Method)
	b.WriteByte('\n')
	b.WriteString(canonicalURI(req.Path, service))
	b.WriteByte('\n')
	b.WriteString(canonicalQuery(req.RawQuery, omit))
	b.WriteByte('\n')
	for _, name := range signed {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(canonicalHeaderValue(req, name))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strings.Join(signed, ";"))
	b.WriteByte('\n')
	b.WriteString(payload)
	return b.String()
}

// canonicalURI keeps S3 paths as sent ("You do not normalize URI paths for
// requests to Amazon S3"); other services drop empty, "." and ".." segments
// and encode the escaped path a second time, as botocore and the JS signer do.
func canonicalURI(path, service string) string {
	if path == "" {
		return "/"
	}
	if service == serviceS3 {
		return path
	}
	var segs []string
	for seg := range strings.SplitSeq(path, "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(segs) > 0 {
				segs = segs[:len(segs)-1]
			}
		default:
			segs = append(segs, seg)
		}
	}
	normalized := strings.Join(segs, "/")
	if strings.HasPrefix(path, "/") {
		normalized = "/" + normalized
	}
	if len(segs) > 0 && strings.HasSuffix(path, "/") {
		normalized += "/"
	}
	return uriEncode(normalized, true)
}

// canonicalQuery decodes each name and value, encodes it with uriEncode and
// sorts by name, then value; a parameter without a value is "name=".
func canonicalQuery(raw, omit string) string {
	type pair struct{ k, v string }
	var pairs []pair
	for part := range strings.SplitSeq(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		k, v = unescape(k), unescape(v)
		if omit != "" && k == omit {
			continue
		}
		pairs = append(pairs, pair{uriEncode(k, false), uriEncode(v, false)})
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		if c := strings.Compare(a.k, b.k); c != 0 {
			return c
		}
		return strings.Compare(a.v, b.v)
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

func unescape(s string) string {
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}

// canonicalHeaderValue joins a header's values with commas, each trimmed of
// spaces with runs of spaces collapsed to one.
func canonicalHeaderValue(req Normalized, name string) string {
	values := []string{req.Host}
	if name != "host" {
		values = req.Header.Values(name)
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = collapseSpaces(v)
	}
	return strings.Join(out, ",")
}

// collapseSpaces trims v of spaces and collapses each run of spaces to one.
func collapseSpaces(v string) string {
	return strings.Join(strings.FieldsFunc(v, func(r rune) bool { return r == ' ' }), " ")
}

// uriEncode encodes every byte but A-Za-z0-9-._~ as %XX with uppercase hex;
// keepSlash leaves "/" alone, as paths need.
func uriEncode(s string, keepSlash bool) string {
	const upperHex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '.', c == '_', c == '~', keepSlash && c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0xf])
		}
	}
	return b.String()
}

func stringToSign(ts, scope, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return algorithm + "\n" + ts + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
}

func signingKey(secret string, c Credential) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), c.Date)
	k = hmacSHA256(k, c.Region.String())
	k = hmacSHA256(k, c.Service)
	return hmacSHA256(k, terminator)
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
