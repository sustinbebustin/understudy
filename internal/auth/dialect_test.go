package auth_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// s3Vocabulary is the JSON family's identities with S3's wording; S3 builds
// its own identities, so these tests check only texts and elements.
func s3Vocabulary(t *testing.T) auth.Vocabulary {
	t.Helper()
	v := jsonVocabulary(t)
	v.Dialect = auth.S3Dialect
	return v
}

func fieldNames(fields []awserr.Field) []string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names
}

func field(t *testing.T, err *awserr.Error, name string) string {
	t.Helper()
	for _, f := range err.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	t.Fatalf("%s is missing from %#v", name, err.Fields)
	return ""
}

func docPresigned(t *testing.T, headers map[string]string) (auth.SigV4Query, auth.Normalized) {
	t.Helper()
	r := serverRequest(t, http.MethodGet, "/test.txt?"+docPresignQuery(map[string]string{
		"X-Amz-Signature": "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404",
	}), "examplebucket.s3.amazonaws.com", headers)
	a, perr := auth.Parse(r, aws.KnownRegions())
	q, ok := a.(auth.SigV4Query)
	if perr != nil || !ok {
		t.Fatalf("Parse = %#v, %#v; want SigV4Query", a, perr)
	}
	return q, auth.Normalize(r, auth.Unread{})
}

// S3's SignatureDoesNotMatch carries the signing-debug elements, in the
// order S3 sends them (s3.md 12, error-matrix.md 5.4 T8). The values are
// those of AWS's documented GET example, which do not depend on the secret.
func TestS3DialectSignatureMismatch(t *testing.T) {
	a, req := docGetObject(t)
	v := s3Vocabulary(t)
	err := auth.Verify(a, req, "wrong", docTime, v)
	if err == nil || err.ID != v.Mismatch {
		t.Fatalf("Verify with the wrong secret = %#v; want Mismatch", err)
	}
	if want := "The request signature we calculated does not match the signature you provided. Check your key and signing method."; err.Message != want {
		t.Errorf("message %q, want %q", err.Message, want)
	}
	if got, want := fieldNames(err.Fields), []string{"AWSAccessKeyId", "StringToSign", "SignatureProvided", "StringToSignBytes", "CanonicalRequest", "CanonicalRequestBytes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("elements %v, want %v", got, want)
	}
	const (
		sts       = "AWS4-HMAC-SHA256\n20130524T000000Z\n20130524/us-east-1/s3/aws4_request\n7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972"
		canonical = "GET\n/test.txt\n\nhost:examplebucket.s3.amazonaws.com\nrange:bytes=0-9\nx-amz-content-sha256:" + emptyHash +
			"\nx-amz-date:20130524T000000Z\n\nhost;range;x-amz-content-sha256;x-amz-date\n" + emptyHash
	)
	for name, want := range map[string]string{
		"AWSAccessKeyId":    docKeyID,
		"SignatureProvided": "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		"StringToSign":      sts,
		"CanonicalRequest":  canonical,
	} {
		if got := field(t, err, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// S3 shows each byte as two lowercase hex digits, space separated:
	// "AWS4-HMAC" is 41 57 53 34 2d 48 4d 41 43.
	if got := field(t, err, "StringToSignBytes"); !strings.HasPrefix(got, "41 57 53 34 2d 48 4d 41 43 ") || got != fmt.Sprintf("% x", sts) {
		t.Errorf("StringToSignBytes = %q", got)
	}
	if got := field(t, err, "CanonicalRequestBytes"); got != fmt.Sprintf("% x", canonical) {
		t.Errorf("CanonicalRequestBytes = %q", got)
	}

	// The Coral services carry the debug text in the message instead.
	if err := auth.Verify(a, req, "wrong", docTime, jsonVocabulary(t)); err == nil || err.Fields != nil || !strings.Contains(err.Message, canonical) {
		t.Errorf("Coral dialect: Verify = %#v; want the canonical request in the message and no elements", err)
	}
}

// S3 answers a skewed request with RequestTimeTooSkewed's text and times,
// as its responses carry them.
func TestS3DialectRequestTimeTooSkewed(t *testing.T) {
	a, req := docGetObject(t)
	v := s3Vocabulary(t)
	for _, wall := range []time.Time{docTime.Add(16 * time.Minute), docTime.Add(-16 * time.Minute)} {
		err := auth.Verify(a, req, docSecret, wall, v)
		if err == nil || err.ID != v.Skewed || err.Message != "The difference between the request time and the current time is too large." {
			t.Fatalf("wall time %s: Verify = %#v; want Skewed", wall, err)
		}
		want := []awserr.Field{
			{Name: "RequestTime", Value: "20130524T000000Z"},
			{Name: "ServerTime", Value: wall.Format(time.RFC3339)},
			{Name: "MaxAllowedSkewMilliseconds", Value: "900000"},
		}
		if !reflect.DeepEqual(err.Fields, want) {
			t.Errorf("wall time %s: elements %#v, want %#v", wall, err.Fields, want)
		}
	}
}

// S3 answers an expired presigned URL with AccessDenied, the URL's lifetime
// (SigV4 only), its expiry and the server time on the emulator clock (REC
// test_s3_presigned_url_expired[s3v4] and [s3]).
func TestS3DialectExpiredPresignedURL(t *testing.T) {
	v := s3Vocabulary(t)
	t.Run("SigV4", func(t *testing.T) {
		q, req := docPresigned(t, nil)
		emu := docTime.Add(25 * time.Hour)
		err := auth.VerifyPresignedSince(q, req, docSecret, docTime, clock.FromWire(q.Time), clock.FromWire(emu), v)
		if err == nil || err.ID != v.Expired || err.Message != "Request has expired" {
			t.Fatalf("VerifyPresignedSince =%#v; want Expired", err)
		}
		want := []awserr.Field{
			{Name: "X-Amz-Expires", Value: "86400"},
			{Name: "Expires", Value: "2013-05-25T00:00:00Z"},
			{Name: "ServerTime", Value: "2013-05-25T01:00:00Z"},
		}
		if !reflect.DeepEqual(err.Fields, want) {
			t.Errorf("elements %#v, want %#v", err.Fields, want)
		}
	})
	t.Run("SigV2", func(t *testing.T) {
		r := serverRequest(t, http.MethodGet, "/b/k6?AWSAccessKeyId=AKIAIOSFODNN7EXAMPLE&Signature=J8h6b%2B6ZbLojGwbODQcBfutc%2Fh0%3D&Expires=1791438586",
			"127.0.0.1:4566", nil)
		a, perr := auth.Parse(r, aws.KnownRegions())
		q, ok := a.(auth.SigV2Query)
		if perr != nil || !ok {
			t.Fatalf("Parse = %#v, %#v; want SigV2Query", a, perr)
		}
		expires := time.Unix(1791438586, 0).UTC()
		err := auth.VerifySigV2Presigned(q, auth.Normalize(r, auth.Unread{}), "/b/k6", docSecret, clock.FromWire(expires.Add(time.Minute)), v)
		if err == nil || err.ID != v.Expired || err.Message != "Request has expired" {
			t.Fatalf("VerifySigV2Presigned = %#v; want Expired", err)
		}
		want := []awserr.Field{
			{Name: "Expires", Value: expires.Format(time.RFC3339)},
			{Name: "ServerTime", Value: expires.Add(time.Minute).Format(time.RFC3339)},
		}
		if !reflect.DeepEqual(err.Fields, want) {
			t.Errorf("elements %#v, want %#v", err.Fields, want)
		}
	})
}

// A SigV2 mismatch carries the elements SigV2 has, without a canonical
// request (REC test_presigned_url_signature_authentication[s3-*]).
func TestS3DialectSigV2Mismatch(t *testing.T) {
	r := serverRequest(t, http.MethodGet, "/photos/puppy.jpg", "johnsmith.s3.amazonaws.com", map[string]string{
		"Date":          "Tue, 27 Mar 2007 19:36:42 +0000",
		"Authorization": "AWS " + docKeyID + ":bWq2s1WEIj+Ydj0vQ697zp+IXMU=",
	})
	a, perr := auth.Parse(r, aws.KnownRegions())
	h, ok := a.(auth.SigV2Header)
	if perr != nil || !ok {
		t.Fatalf("Parse = %#v, %#v; want SigV2Header", a, perr)
	}
	v := s3Vocabulary(t)
	err := auth.VerifySigV2(h, auth.Normalize(r, auth.Unread{}), "/johnsmith/photos/puppy.jpg", "wrong", time.Date(2007, 3, 27, 19, 36, 42, 0, time.UTC), v)
	if err == nil || err.ID != v.Mismatch {
		t.Fatalf("VerifySigV2 = %#v; want Mismatch", err)
	}
	if got, want := fieldNames(err.Fields), []string{"AWSAccessKeyId", "StringToSign", "SignatureProvided", "StringToSignBytes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("elements %v, want %v", got, want)
	}
	if got, want := field(t, err, "StringToSign"), "GET\n\n\nTue, 27 Mar 2007 19:36:42 +0000\n/johnsmith/photos/puppy.jpg"; got != want {
		t.Errorf("StringToSign = %q, want %q", got, want)
	}
}

// A presigned URL must sign every x-amz-* header its request carries
// (sigv4-checksums-endpoints.md 2.1); S3 names the others, sorted and
// joined with ", " (REC test_put_object_with_md5_and_chunk_signature_bad_headers,
// test_put_url_metadata_with_sig_s3v4), in its HeadersNotSigned element.
// Coral has no such member, so it names them in the message.
func TestVerifyPresignedRefusesUnsignedAmzHeaders(t *testing.T) {
	const msg = "There were headers present in the request which were not signed"
	unsignedRequest := func(t *testing.T, v auth.Vocabulary) *awserr.Error {
		t.Helper()
		q, req := docPresigned(t, map[string]string{"X-Amz-Meta-Wrong": "1", "X-Amz-Date": "20130524T000000Z", "Range": "bytes=0-9"})
		return auth.VerifyPresignedSince(q, req, docSecret, docTime, clock.FromWire(q.Time), clock.FromWire(docTime), v)
	}

	t.Run("S3", func(t *testing.T) {
		v := s3Vocabulary(t)
		err := unsignedRequest(t, v)
		if err == nil || err.ID != v.HeadersNotSigned || err.Message != msg {
			t.Fatalf("VerifyPresignedSince =%#v; want HeadersNotSigned %q", err, msg)
		}
		want := []awserr.Field{{Name: "HeadersNotSigned", Value: "x-amz-date, x-amz-meta-wrong"}}
		if !reflect.DeepEqual(err.Fields, want) {
			t.Errorf("elements %#v, want %#v", err.Fields, want)
		}
	})
	t.Run("Coral", func(t *testing.T) {
		v := jsonVocabulary(t)
		err := unsignedRequest(t, v)
		wantMsg := msg + ": x-amz-date, x-amz-meta-wrong"
		if err == nil || err.ID != v.HeadersNotSigned || err.Message != wantMsg {
			t.Fatalf("VerifyPresignedSince =%#v; want HeadersNotSigned %q", err, wantMsg)
		}
		if err.Fields != nil {
			t.Errorf("elements %#v, want none: Coral has no HeadersNotSigned member", err.Fields)
		}
	})
	t.Run("non-amz header need not be signed", func(t *testing.T) {
		q, req := docPresigned(t, map[string]string{"Range": "bytes=0-9"})
		mustVerify(t, auth.VerifyPresignedSince(q, req, docSecret, docTime, clock.FromWire(q.Time), clock.FromWire(docTime), s3Vocabulary(t)))
	})
}

// "If you add a signed header that is also a signed query parameter, and they
// differ in value, you will receive an InvalidRequest error as the input is
// conflicting" (sigv4-checksums-endpoints.md 2.1), even when the client
// signed both. That answer for a request whose signature verifies is
// UNVERIFIED.
func TestVerifyPresignedRefusesHeaderConflictingWithQuery(t *testing.T) {
	v := s3Vocabulary(t)
	// A marker identity, so the test tells Conflicting from Mismatch.
	v.Conflicting = awserr.AccessDeniedException()
	signer := auth.Signer{AccessKeyID: docKeyID, SecretAccessKey: docSecret, Region: aws.DefaultRegion(), Service: "s3"}
	presignedPut := func(t *testing.T, query, header string) *awserr.Error {
		t.Helper()
		client := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "http://localhost:4566/b/k?"+query, http.NoBody)
		client.Header.Set("X-Amz-Meta-A", header)
		auth.Presign(client, signer, time.Hour, docTime)
		r := serverRequest(t, http.MethodPut, client.URL.RequestURI(), "localhost:4566", map[string]string{"X-Amz-Meta-A": header})
		a, perr := auth.Parse(r, aws.KnownRegions())
		q, ok := a.(auth.SigV4Query)
		if perr != nil || !ok {
			t.Fatalf("Parse = %#v, %#v; want SigV4Query", a, perr)
		}
		if !slices.Contains(q.SignedHeaders, "x-amz-meta-a") {
			t.Fatalf("signed headers %v lack x-amz-meta-a", q.SignedHeaders)
		}
		return auth.VerifyPresignedSince(q, auth.Normalize(r, auth.Unread{}), docSecret, docTime, clock.FromWire(q.Time), clock.FromWire(docTime), v)
	}

	for _, query := range []string{"x-amz-meta-a=two", "X-Amz-Meta-A=two", "x-amz-meta-a=one&x-amz-meta-a=two"} {
		if err := presignedPut(t, query, "one"); err == nil || err.ID != v.Conflicting || !strings.Contains(err.Message, "x-amz-meta-a") {
			t.Errorf("query %s with header x-amz-meta-a: one: VerifyPresignedSince =%#v; want Conflicting naming x-amz-meta-a", query, err)
		}
	}
	// Equal values do not conflict, compared as the canonical request holds
	// them; nor does a parameter no signed header shares.
	for _, tc := range []struct{ query, header string }{
		{"x-amz-meta-a=one", "one"},
		{"X-Amz-Meta-A=one%20%20two", "one two "},
		{"x-amz-meta-b=two", "one"},
	} {
		mustVerify(t, presignedPut(t, tc.query, tc.header))
	}

	// A conflicting parameter added after signing breaks the signature, and S3
	// reports the mismatch, not the conflict (REC
	// test_s3_put_presigned_url_same_header_and_qs_parameter).
	t.Run("added parameter is a mismatch", func(t *testing.T) {
		client := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "http://localhost:4566/b/k", http.NoBody)
		client.Header.Set("X-Amz-Request-Payer", "requester")
		auth.Presign(client, signer, time.Hour, docTime)
		target := client.URL.Path + "?x-amz-request-payer=non-valid&" + client.URL.RawQuery
		r := serverRequest(t, http.MethodPut, target, "localhost:4566", map[string]string{"X-Amz-Request-Payer": "requester"})
		a, perr := auth.Parse(r, aws.KnownRegions())
		q, ok := a.(auth.SigV4Query)
		if perr != nil || !ok {
			t.Fatalf("Parse = %#v, %#v; want SigV4Query", a, perr)
		}
		if !slices.Contains(q.SignedHeaders, "x-amz-request-payer") {
			t.Fatalf("signed headers %v lack x-amz-request-payer", q.SignedHeaders)
		}
		err := auth.VerifyPresignedSince(q, auth.Normalize(r, auth.Unread{}), docSecret, docTime, clock.FromWire(q.Time), clock.FromWire(docTime), v)
		if err == nil || err.ID != v.Mismatch {
			t.Fatalf("VerifyPresignedSince =%#v; want Mismatch", err)
		}
		if got, want := fieldNames(err.Fields), []string{"AWSAccessKeyId", "StringToSign", "SignatureProvided", "StringToSignBytes", "CanonicalRequest", "CanonicalRequestBytes"}; !reflect.DeepEqual(got, want) {
			t.Errorf("elements %v, want %v", got, want)
		}
	})
}
