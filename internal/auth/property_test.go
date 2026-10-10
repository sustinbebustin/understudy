package auth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// clientRequest is a random request as an SDK would send it, signed or
// presigned by the caller.
type clientRequest struct {
	req    *http.Request
	body   []byte
	signer auth.Signer
	now    time.Time
}

func region(t *rapid.T) aws.Region {
	name := rapid.SampledFrom([]string{"us-east-1", "us-west-2", "eu-west-1", "ap-southeast-2", "sa-east-1"}).Draw(t, "region")
	r, err := aws.KnownRegions().Parse(name)
	if err != nil {
		t.Fatalf("Parse(%q): %v", name, err)
	}
	return r
}

func drawClientRequest(ctx context.Context, t *rapid.T) clientRequest {
	segment := rapid.StringMatching(`[A-Za-z0-9._~ !$&'()*+,;=:@%-]{0,8}`)
	n := rapid.IntRange(0, 4).Draw(t, "segments")
	segs := make([]string, 0, n)
	for i := range n {
		segs = append(segs, segment.Draw(t, "segment"+string(rune('0'+i))))
	}
	q := url.Values{}
	text := rapid.StringMatching(`[A-Za-z0-9 _.~+/=&%-]{0,6}`)
	for range rapid.IntRange(0, 4).Draw(t, "params") {
		q.Add(text.Draw(t, "param"), text.Draw(t, "value"))
	}
	host := rapid.SampledFrom([]string{"localhost:4566", "127.0.0.1:45661", "bucket.localhost:4566", "sqs.us-east-1.amazonaws.com"}).Draw(t, "host")
	u := &url.URL{Scheme: "http", Host: host, Path: "/" + strings.Join(segs, "/"), RawQuery: q.Encode()}
	body := rapid.SliceOfN(rapid.Byte(), 0, 64).Draw(t, "body")
	method := rapid.SampledFrom([]string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodHead}).Draw(t, "method")
	r, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest(%s): %v", u, err)
	}
	value := rapid.StringMatching(`[!-~]([ -~]{0,10}[!-~])?`)
	for range rapid.IntRange(0, 4).Draw(t, "headers") {
		r.Header.Set("X-Amz-Meta-"+rapid.StringMatching(`[a-z]{1,6}`).Draw(t, "header"), value.Draw(t, "headerValue"))
	}
	if rapid.Bool().Draw(t, "contentType") {
		r.Header.Set("Content-Type", value.Draw(t, "contentTypeValue"))
	}
	signer := auth.Signer{
		AccessKeyID:     rapid.StringMatching(`[A-Z0-9]{4,20}`).Draw(t, "key"),
		SecretAccessKey: rapid.StringMatching(`[A-Za-z0-9/+]{40}`).Draw(t, "secret"),
		Region:          region(t),
		Service:         rapid.SampledFrom([]string{"s3", "sqs", "lambda", "monitoring"}).Draw(t, "service"),
	}
	if rapid.Bool().Draw(t, "session") {
		signer.SessionToken = rapid.StringMatching(`[A-Za-z0-9/+=]{16,64}`).Draw(t, "token")
	}
	now := time.Unix(rapid.Int64Range(946684800, 4102444800).Draw(t, "now"), 0).UTC()
	return clientRequest{req: r, body: body, signer: signer, now: now}
}

// received is the request as net/http hands it to the emulator.
func (c clientRequest) received(ctx context.Context) *http.Request {
	r := httptest.NewRequestWithContext(ctx, c.req.Method, c.req.URL.RequestURI(), bytes.NewReader(c.body))
	r.Host = c.req.URL.Host
	r.Header = c.req.Header.Clone()
	return r
}

func mutateByte(t *rapid.T, s, label string) string {
	if s == "" {
		return "x"
	}
	i := rapid.IntRange(0, len(s)-1).Draw(t, label+"Index")
	b := rapid.ByteRange(0x20, 0x7e).Filter(func(c byte) bool { return c != s[i] }).Draw(t, label+"Byte")
	return s[:i] + string(b) + s[i+1:]
}

func TestSignThenVerifySucceeds(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		c := drawClientRequest(t.Context(), rt)
		auth.Sign(c.req, c.body, c.signer, c.now)
		r := c.received(t.Context())
		a, perr := auth.Parse(r, aws.KnownRegions())
		if perr != nil {
			rt.Fatalf("Parse of a signed request: %#v", perr)
		}
		h, ok := a.(auth.SigV4Header)
		if !ok {
			rt.Fatalf("Parse = %#v, want SigV4Header", a)
		}
		if h.RegionSource() != (aws.CredentialScope{Region: c.signer.Region}) {
			rt.Fatalf("region source %#v, want the signer's region %s", h.RegionSource(), c.signer.Region)
		}
		if key, _ := auth.AccessKey(h); key != c.signer.AccessKeyID {
			rt.Fatalf("access key %q, want %q", key, c.signer.AccessKeyID)
		}
		v := jsonVocabulary(t)
		// Within the 15-minute skew window either side.
		wall := c.now.Add(time.Duration(rapid.Int64Range(-int64(15*time.Minute), int64(15*time.Minute)).Draw(rt, "skew")))
		if err := auth.Verify(h, auth.Normalize(r, auth.Hashed{SHA256: sha256.Sum256(c.body)}), c.signer.SecretAccessKey, wall, v); err != nil {
			rt.Fatalf("Verify of a signed request: %s", err.Message)
		}
	})
}

func TestVerifyFailsAfterAnySingleByteMutation(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		c := drawClientRequest(t.Context(), rt)
		auth.Sign(c.req, c.body, c.signer, c.now)
		r := c.received(t.Context())
		signed := parseSignedHeaders(rt, r)

		target := rapid.SampledFrom(append([]string{"signature"}, signed...)).Draw(rt, "target")
		switch target {
		case "signature":
			authz := r.Header.Get("Authorization")
			i := strings.LastIndex(authz, "Signature=") + len("Signature=")
			r.Header.Set("Authorization", authz[:i]+mutateByte(rt, authz[i:], "signature"))
		case "host":
			r.Host = mutateByte(rt, r.Host, "host")
		default:
			r.Header.Set(target, mutateByte(rt, r.Header.Get(target), "header"))
		}

		a, perr := auth.Parse(r, aws.KnownRegions())
		if perr != nil {
			return // a mutation that makes the request unreadable fails too
		}
		h, ok := a.(auth.SigV4Header)
		if !ok {
			rt.Fatalf("Parse = %#v, want SigV4Header", a)
		}
		err := auth.Verify(h, auth.Normalize(r, auth.Hashed{SHA256: sha256.Sum256(c.body)}), c.signer.SecretAccessKey, c.now, jsonVocabulary(t))
		if err == nil {
			rt.Fatalf("Verify accepted a request whose %s was mutated", target)
		}
	})
}

func parseSignedHeaders(t *rapid.T, r *http.Request) []string {
	authz := r.Header.Get("Authorization")
	_, after, ok := strings.Cut(authz, "SignedHeaders=")
	if !ok {
		t.Fatalf("no SignedHeaders in %q", authz)
	}
	list, _, _ := strings.Cut(after, ",")
	return strings.Split(list, ";")
}

func TestPresignThenVerifySucceedsAndMutationFails(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		c := drawClientRequest(t.Context(), rt)
		expires := time.Duration(rapid.Int64Range(1, 604800).Draw(rt, "expires")) * time.Second
		auth.Presign(c.req, c.signer, expires, c.now)
		r := c.received(t.Context())
		a, perr := auth.Parse(r, aws.KnownRegions())
		if perr != nil {
			rt.Fatalf("Parse of a presigned request: %#v", perr)
		}
		q, ok := a.(auth.SigV4Query)
		if !ok {
			rt.Fatalf("Parse = %#v, want SigV4Query", a)
		}
		v := jsonVocabulary(t)
		emu := clock.FromWire(c.now.Add(expires - time.Second))
		body := auth.Hashed{SHA256: sha256.Sum256(nil)}
		if err := auth.VerifyPresigned(q, auth.Normalize(r, body), c.signer.SecretAccessKey, c.now, emu, v); err != nil {
			rt.Fatalf("VerifyPresigned of a presigned request: %s", err.Message)
		}

		target := rapid.SampledFrom(append([]string{"signature"}, q.SignedHeaders...)).Draw(rt, "target")
		switch target {
		case "host":
			r.Host = mutateByte(rt, r.Host, "host")
		case "signature":
			raw := r.URL.RawQuery
			i := strings.LastIndex(raw, "X-Amz-Signature=") + len("X-Amz-Signature=")
			r.URL.RawQuery = raw[:i] + mutateByte(rt, raw[i:], "signature")
			r.RequestURI = r.URL.EscapedPath() + "?" + r.URL.RawQuery
			a, perr = auth.Parse(r, aws.KnownRegions())
			if perr != nil {
				return
			}
			if q, ok = a.(auth.SigV4Query); !ok {
				rt.Fatalf("Parse = %#v, want SigV4Query", a)
			}
		default:
			r.Header.Set(target, mutateByte(rt, r.Header.Get(target), "header"))
		}
		if err := auth.VerifyPresigned(q, auth.Normalize(r, body), c.signer.SecretAccessKey, c.now, emu, v); err == nil {
			rt.Fatalf("VerifyPresigned accepted a presigned request whose %s was mutated", target)
		}
	})
}

// reencodeUnreserved percent-encodes some of raw's unreserved bytes
// (A-Za-z0-9-._~), in upper- or lowercase hex, leaving its escapes and
// reserved bytes alone: a query a proxy or client library could send in
// place of raw.
func reencodeUnreserved(t *rapid.T, raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		unreserved := 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || strings.IndexByte("-._~", c) >= 0
		switch {
		case c == '%' && i+2 < len(raw):
			b.WriteString(raw[i : i+3])
			i += 2
		case unreserved && rapid.Bool().Draw(t, "encode"):
			format := "%%%02X"
			if rapid.Bool().Draw(t, "lowercase") {
				format = "%%%02x"
			}
			fmt.Fprintf(&b, format, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// The canonical query decodes before it encodes, so encoding unreserved
// query bytes differently leaves it, and the signature, unchanged for every
// service (sigv4-checksums-endpoints.md, Implications 2). The path has no
// such invariance: S3 signs it as sent and the other services encode it as
// sent once more.
func TestSignatureSurvivesReencodedUnreservedQueryBytes(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		c := drawClientRequest(t.Context(), rt)
		auth.Sign(c.req, c.body, c.signer, c.now)
		target := c.req.URL.EscapedPath()
		if q := c.req.URL.RawQuery; q != "" {
			target += "?" + reencodeUnreserved(rt, q)
		}
		r := httptest.NewRequestWithContext(t.Context(), c.req.Method, target, bytes.NewReader(c.body))
		r.Host = c.req.URL.Host
		r.Header = c.req.Header.Clone()
		h, ok := mustParse(rt, r).(auth.SigV4Header)
		if !ok {
			rt.Fatalf("Parse of a signed request did not give a SigV4Header")
		}
		if err := auth.Verify(h, auth.Normalize(r, auth.Hashed{SHA256: sha256.Sum256(c.body)}), c.signer.SecretAccessKey, c.now, jsonVocabulary(t)); err != nil {
			rt.Fatalf("Verify with the query sent as %q: %s", target, err.Message)
		}
	})
}

func mustParse(t *rapid.T, r *http.Request) auth.Auth {
	a, perr := auth.Parse(r, aws.KnownRegions())
	if perr != nil {
		t.Fatalf("Parse: %#v", perr)
	}
	return a
}

func TestParseNeverPanics(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		r.URL.RawQuery = rapid.SampledFrom([]string{"", "X-Amz-Algorithm=AWS4-HMAC-SHA256", "AWSAccessKeyId=a&Signature=b"}).Draw(rt, "query") +
			rapid.String().Draw(rt, "rawQuery")
		r.Header.Set("Authorization", rapid.SampledFrom([]string{"", "AWS4-HMAC-SHA256 ", "AWS "}).Draw(rt, "scheme")+rapid.String().Draw(rt, "authorization"))
		r.Header.Set("X-Amz-Date", rapid.String().Draw(rt, "date"))
		// Hex runs around 64 digits reach the Hex branch with every length.
		r.Header.Set("X-Amz-Content-Sha256", rapid.OneOf(rapid.String(), rapid.StringMatching(`[0-9a-fA-F]{60,70}`)).Draw(rt, "payload"))
		a, perr := auth.Parse(r, aws.KnownRegions())
		if (a == nil) == (perr == nil) {
			rt.Fatalf("Parse returned auth %#v and error %#v; want exactly one", a, perr)
		}
	})
}
