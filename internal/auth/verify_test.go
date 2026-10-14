package auth_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// docGetObject is the documentation GET vector of TestDocVectorGetObject.
func docGetObject(t *testing.T) (auth.SigV4Header, auth.Normalized) {
	t.Helper()
	r := serverRequest(t, http.MethodGet, "/test.txt", "examplebucket.s3.amazonaws.com", map[string]string{
		"Range":                "bytes=0-9",
		"X-Amz-Content-Sha256": emptyHash,
		"X-Amz-Date":           "20130524T000000Z",
		"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
			"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
			"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
	})
	return parseV4Header(t, r), auth.Normalize(r, auth.Unread{})
}

// The request time must lie within 15 minutes of the server's wall clock,
// inclusive, with AWS's texts either side (error-matrix.md 5.5, DynamoDB T8).
func TestVerifySkewWindow(t *testing.T) {
	a, req := docGetObject(t)
	v := jsonVocabulary(t)
	for _, wall := range []time.Time{docTime.Add(15 * time.Minute), docTime.Add(-15 * time.Minute), docTime} {
		mustVerify(t, auth.Verify(a, req, docSecret, wall, v))
	}
	for _, tc := range []struct {
		wall    time.Time
		message string
	}{
		{docTime.Add(15*time.Minute + time.Second), "Signature expired: 20130524T000000Z is now earlier than 20130524T000001Z (20130524T001501Z - 15 min.)"},
		{docTime.Add(-15*time.Minute - time.Second), "Signature not yet current: 20130524T000000Z is still later than 20130523T235959Z (20130523T234459Z + 15 min.)"},
	} {
		err := auth.Verify(a, req, docSecret, tc.wall, v)
		if err == nil || err.ID != v.Skewed || err.Message != tc.message {
			t.Errorf("wall time %s: Verify = %#v; want Skewed %q", tc.wall, err, tc.message)
		}
	}
}

func TestVerifyScopeDateMustBeRequestDate(t *testing.T) {
	r := serverRequest(t, http.MethodGet, "/", "localhost:4566", map[string]string{
		"X-Amz-Content-Sha256": emptyHash,
		"X-Amz-Date":           "20130524T000000Z",
		"Authorization":        docAuthorization(strings.Replace(docCredential, "/20130524/", "/20130523/", 1), "host;x-amz-date"),
	})
	v := jsonVocabulary(t)
	err := auth.Verify(parseV4Header(t, r), auth.Normalize(r, auth.Hashed{SHA256: sha256.Sum256(nil)}), docSecret, docTime, v)
	const want = "Date in Credential scope does not match YYYYMMDD from ISO-8601 version of date from HTTP: '20130523' != '20130524', from '20130524T000000Z'."
	if err == nil || err.ID != v.Mismatch || err.Message != want {
		t.Errorf("Verify = %#v; want Mismatch %q", err, want)
	}
	// Coral reports an expired signature before a wrong scope date.
	late := docTime.Add(time.Hour)
	if err := auth.Verify(parseV4Header(t, r), auth.Normalize(r, auth.Hashed{SHA256: sha256.Sum256(nil)}), docSecret, late, v); err == nil ||
		!strings.HasPrefix(err.Message, "Signature expired: ") {
		t.Errorf("skewed request with a wrong scope date: Verify = %#v; want \"Signature expired\"", err)
	}
}

func TestVerifyPayloadHashMustMatchBody(t *testing.T) {
	a, req := docGetObject(t)
	req.Body = auth.Hashed{SHA256: sha256.Sum256([]byte("not empty"))}
	v := jsonVocabulary(t)
	err := auth.Verify(a, req, docSecret, docTime, v)
	if err == nil || err.ID != v.Mismatch || !strings.Contains(err.Message, "'x-amz-content-sha256' header does not match") {
		t.Errorf("Verify = %#v; want a payload-hash Mismatch", err)
	}
	req.Body = auth.Hashed{SHA256: sha256.Sum256(nil)}
	mustVerify(t, auth.Verify(a, req, docSecret, docTime, v))
}

// A presigned URL dated more than 15 minutes ahead of the wall clock is not
// yet valid, whatever the emulator clock says.
func TestVerifyPresignedNotYetCurrent(t *testing.T) {
	r := serverRequest(t, http.MethodGet, "/test.txt?"+docPresignQuery(map[string]string{
		"X-Amz-Signature": "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404",
	}), "examplebucket.s3.amazonaws.com", nil)
	a, perr := auth.Parse(r, aws.KnownRegions())
	q, ok := a.(auth.SigV4Query)
	if perr != nil || !ok {
		t.Fatalf("Parse = %#v, %#v; want SigV4Query", a, perr)
	}
	v := jsonVocabulary(t)
	req := auth.Normalize(r, auth.Unread{})
	emu := clock.FromWire(docTime)
	mustVerify(t, auth.VerifyPresignedSince(q, req, docSecret, docTime.Add(-15*time.Minute), clock.FromWire(q.Time), emu, v))
	err := auth.VerifyPresignedSince(q, req, docSecret, docTime.Add(-16*time.Minute), clock.FromWire(q.Time), emu, v)
	if err == nil || err.ID != v.Skewed || !strings.HasPrefix(err.Message, "Signature not yet current: 20130524T000000Z") {
		t.Errorf("16 minutes early: %#v; want Skewed \"Signature not yet current\"", err)
	}
}

// A signature mismatch shows the canonical request or string to sign, in
// the Coral message or in S3's elements, which logs and the journal carry,
// so the session token in it is redacted (ARCHITECTURE.md 10).
func TestSignatureMismatchRedactsTheSessionToken(t *testing.T) {
	const token = "TOKEN123sessiontoken"
	signer := auth.Signer{AccessKeyID: "ASIAEXAMPLE", SecretAccessKey: docSecret, SessionToken: token, Region: aws.DefaultRegion(), Service: "sqs"}
	body := auth.Hashed{SHA256: sha256.Sum256(nil)}
	checkRedacted := func(t *testing.T, err *awserr.Error, v auth.Vocabulary) {
		t.Helper()
		if err == nil || err.ID != v.Mismatch {
			t.Fatalf("verify with the wrong secret = %#v; want Mismatch", err)
		}
		var b strings.Builder
		b.WriteString(err.Message)
		for _, f := range err.Fields {
			b.WriteString("\n" + f.Name + ": " + f.Value)
		}
		shown := b.String()
		// The canonical query URI-encodes the marker as %5BREDACTED%5D.
		if strings.Contains(shown, token) || strings.Contains(shown, fmt.Sprintf("% x", token)) || !strings.Contains(shown, "REDACTED") {
			t.Errorf("the answer does not redact the session token:\n%s", shown)
		}
	}

	for _, v := range []auth.Vocabulary{jsonVocabulary(t), s3Vocabulary(t)} {
		t.Run(fmt.Sprintf("dialect %d", v.Dialect), func(t *testing.T) {
			t.Run("header", func(t *testing.T) {
				client := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566/", http.NoBody)
				auth.Sign(client, nil, signer, docTime)
				r := serverRequest(t, http.MethodPost, "/", "localhost:4566", nil)
				r.Header = client.Header.Clone()
				checkRedacted(t, auth.Verify(parseV4Header(t, r), auth.Normalize(r, body), "wrong", docTime, v), v)
			})
			t.Run("presigned", func(t *testing.T) {
				client := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost:4566/", http.NoBody)
				auth.Presign(client, signer, time.Hour, docTime)
				r := serverRequest(t, http.MethodGet, client.URL.RequestURI(), "localhost:4566", nil)
				a, perr := auth.Parse(r, aws.KnownRegions())
				q, ok := a.(auth.SigV4Query)
				if perr != nil || !ok {
					t.Fatalf("Parse = %#v, %#v; want SigV4Query", a, perr)
				}
				checkRedacted(t, auth.VerifyPresignedSince(q, auth.Normalize(r, body), "wrong", docTime, clock.FromWire(q.Time), clock.FromWire(docTime), v), v)
			})
		})
	}

	// SigV2 is S3's alone; its string to sign holds every x-amz-* header and,
	// presigned, every x-amz-* query parameter, which boto3 sends lowercase.
	v := s3Vocabulary(t)
	t.Run("SigV2 header", func(t *testing.T) {
		r := serverRequest(t, http.MethodGet, "/b/k", "localhost:4566", map[string]string{
			"Date":                 "Tue, 27 Mar 2007 19:36:42 +0000",
			"X-Amz-Security-Token": token,
			"Authorization":        "AWS " + docKeyID + ":bWq2s1WEIj+Ydj0vQ697zp+IXMU=",
		})
		a, perr := auth.Parse(r, aws.KnownRegions())
		h, ok := a.(auth.SigV2Header)
		if perr != nil || !ok {
			t.Fatalf("Parse = %#v, %#v; want SigV2Header", a, perr)
		}
		checkRedacted(t, auth.VerifySigV2(h, auth.Normalize(r, auth.Unread{}), "/b/k", docSecret, time.Date(2007, 3, 27, 19, 36, 42, 0, time.UTC), v), v)
	})
	t.Run("SigV2 presigned", func(t *testing.T) {
		r := serverRequest(t, http.MethodGet, "/b/k?AWSAccessKeyId="+docKeyID+"&Signature=AAAA&Expires=1791438586&x-amz-security-token="+token, "localhost:4566", nil)
		a, perr := auth.Parse(r, aws.KnownRegions())
		q, ok := a.(auth.SigV2Query)
		if perr != nil || !ok {
			t.Fatalf("Parse = %#v, %#v; want SigV2Query", a, perr)
		}
		checkRedacted(t, auth.VerifySigV2Presigned(q, auth.Normalize(r, auth.Unread{}), "/b/k", docSecret, clock.FromWire(docTime), v), v)
	})
}

// AWS's SigV2 REST authentication examples (credentials as in
// sigv4-checksums-endpoints.md 1.2), as openstack/swift's s3api tests and
// kr/s3 reproduce them; their Date headers use a numeric zone. The doc's
// printed DELETE signature is wrong; R4dJ... is the value kr/s3 and an
// offline HMAC-SHA1 of the string to sign agree on.
func TestSigV2RESTAuthenticationExamples(t *testing.T) {
	for _, tc := range []struct {
		name, method, target, host, resource string
		headers                              map[string]string
		at                                   time.Time
	}{
		{
			"object GET", http.MethodGet, "/photos/puppy.jpg", "johnsmith.s3.amazonaws.com", "/johnsmith/photos/puppy.jpg",
			map[string]string{
				"Date":          "Tue, 27 Mar 2007 19:36:42 +0000",
				"Authorization": "AWS " + docKeyID + ":bWq2s1WEIj+Ydj0vQ697zp+IXMU=",
			},
			time.Date(2007, 3, 27, 19, 36, 42, 0, time.UTC),
		},
		{
			"object PUT", http.MethodPut, "/photos/puppy.jpg", "johnsmith.s3.amazonaws.com", "/johnsmith/photos/puppy.jpg",
			map[string]string{
				"Content-Type":   "image/jpeg",
				"Content-Length": "94328",
				"Date":           "Tue, 27 Mar 2007 21:15:45 +0000",
				"Authorization":  "AWS " + docKeyID + ":MyyxeRY7whkBe+bq8fHCL/2kKUg=",
			},
			time.Date(2007, 3, 27, 21, 15, 45, 0, time.UTC),
		},
		{
			"path-style DELETE with x-amz-date", http.MethodDelete, "/johnsmith/photos/puppy.jpg", "s3.amazonaws.com", "/johnsmith/photos/puppy.jpg",
			map[string]string{
				"X-Amz-Date":    "Tue, 27 Mar 2007 21:20:26 +0000",
				"Authorization": "AWS " + docKeyID + ":R4dJ53KECjStyBO5iTBJZ4XVOaI=",
			},
			time.Date(2007, 3, 27, 21, 20, 26, 0, time.UTC),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := serverRequest(t, tc.method, tc.target, tc.host, tc.headers)
			a, perr := auth.Parse(r, aws.KnownRegions())
			h, ok := a.(auth.SigV2Header)
			if perr != nil || !ok {
				t.Fatalf("Parse = %#v, %#v; want SigV2Header", a, perr)
			}
			v := jsonVocabulary(t)
			req := auth.Normalize(r, auth.Unread{})
			mustVerify(t, auth.VerifySigV2(h, req, tc.resource, docSecret, tc.at, v))
			if err := auth.VerifySigV2(h, req, "/johnsmith/photos/kitten.jpg", docSecret, tc.at, v); err == nil || err.ID != v.Mismatch {
				t.Errorf("another resource: %#v, want Mismatch", err)
			}
			if err := auth.VerifySigV2(h, req, tc.resource, docSecret, tc.at.Add(16*time.Minute), v); err == nil || err.ID != v.Skewed {
				t.Errorf("16 minutes later: %#v, want Skewed", err)
			}
		})
	}
}

func TestSignerNeverReachesLogsOrFormatting(t *testing.T) {
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	s := auth.Signer{ //nolint:gosec // AWS's published example credentials and a dummy token.
		AccessKeyID:     docKeyID,
		SecretAccessKey: docSecret,
		SessionToken:    "FwoGZXIvYXdzEXAMPLESESSIONTOKEN",
		Region:          region,
		Service:         "s3",
	}
	assertRedacted(t, s, []string{s.SecretAccessKey, s.SessionToken}, s.AccessKeyID)
}

// A ChunkVerifier holds the day's signing key, derived from the secret and
// as good as it for S3 in that region.
func TestChunkVerifierNeverRevealsItsSigningKey(t *testing.T) {
	a, _ := docGetObject(t)
	v := auth.ChunkSignatureVerifier(a, docSecret)
	key := docSigningKey()
	assertRedacted(t, v, []string{docSecret, string(key), hex.EncodeToString(key)}, "20130524/us-east-1/s3/aws4_request")
}

// docSigningKey is the SigV4 signing key of docSecret for 20130524,
// us-east-1, s3.
func docSigningKey() []byte {
	key := []byte("AWS4" + docSecret)
	for _, part := range []string{"20130524", "us-east-1", "s3", "aws4_request"} {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(part))
		key = m.Sum(nil)
	}
	return key
}

// assertRedacted renders v through slog's text and JSON handlers, at the top
// level and held in a struct, a slice and a pointer, and through fmt, and
// fails if any rendering holds one of secrets or lacks marker, the proof that
// v was rendered at all.
func assertRedacted[T any](t *testing.T, v T, secrets []string, marker string) {
	t.Helper()
	handlers := []struct {
		name string
		new  func(io.Writer) slog.Handler
	}{
		{"text", func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, nil) }},
		{"json", func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, nil) }},
	}
	shapes := []struct {
		name string
		attr slog.Attr
	}{
		{"top level", slog.Any("v", v)},
		{"struct holder", slog.Any("holder", struct{ V T }{v})},
		{"slice", slog.Any("vs", []T{v})},
		{"pointer", slog.Any("v", &v)},
	}
	verbs := []string{"%v", "%+v", "%#v", "%s"}
	outputs := make([]string, 0, len(handlers)*len(shapes)+len(verbs))
	for _, h := range handlers {
		for _, shape := range shapes {
			var buf bytes.Buffer
			slog.New(h.new(&buf)).Info("m", shape.attr)
			outputs = append(outputs, h.name+" handler, "+shape.name+": "+buf.String())
		}
	}
	for _, verb := range verbs {
		outputs = append(outputs, verb+": "+fmt.Sprintf(verb, v))
	}
	for _, out := range outputs {
		for _, secret := range secrets {
			if strings.Contains(out, secret) {
				t.Errorf("a secret reached the output: %s", out)
			}
		}
		if !strings.Contains(out, marker) {
			t.Errorf("%q is missing from the output, so the redaction check proves nothing: %s", marker, out)
		}
	}
}
