package auth_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// The published vectors of sigv4-checksums-endpoints.md 1.2: credentials
// AKIAIOSFODNN7EXAMPLE / wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY, time
// 20130524T000000Z, us-east-1, s3.
const (
	docSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" //nolint:gosec // AWS's published example secret, not a credential.
	emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var docTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func jsonVocabulary(t *testing.T) auth.Vocabulary {
	t.Helper()
	v, ok := auth.VocabularyFor(awserr.JSONFamily)
	if !ok {
		t.Fatal("VocabularyFor(JSONFamily) reported false")
	}
	return v
}

// serverRequest builds a request as net/http hands it to a handler.
func serverRequest(t *testing.T, method, target, host string, headers map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody)
	r.Host = host
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func parseV4Header(t *testing.T, r *http.Request) auth.SigV4Header {
	t.Helper()
	a, perr := auth.Parse(r, aws.KnownRegions())
	if perr != nil {
		t.Fatalf("Parse: %#v", perr)
	}
	h, ok := a.(auth.SigV4Header)
	if !ok {
		t.Fatalf("Parse = %#v, want SigV4Header", a)
	}
	return h
}

func mustVerify(t *testing.T, err *awserr.Error) {
	t.Helper()
	if err != nil {
		t.Fatalf("verify failed: %s: %s", err.ID.Shape, err.Message)
	}
}

// hashOfAs is the SHA-256 of n bytes 'a', the AWS examples' chunk data.
func hashOfAs(n int) [32]byte {
	return sha256.Sum256(bytes.Repeat([]byte{'a'}, n))
}

func TestDocVectorGetObject(t *testing.T) {
	r := serverRequest(t, http.MethodGet, "/test.txt", "examplebucket.s3.amazonaws.com", map[string]string{
		"Range":                "bytes=0-9",
		"X-Amz-Content-Sha256": emptyHash,
		"X-Amz-Date":           "20130524T000000Z",
		// The AWS example separates elements with bare commas.
		"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
			"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
			"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
	})
	a := parseV4Header(t, r)
	mustVerify(t, auth.Verify(a, auth.Normalize(r, auth.Unread{}), docSecret, docTime, jsonVocabulary(t)))
}

func TestDocVectorPresignedGetObject(t *testing.T) {
	r := serverRequest(t, http.MethodGet, "/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256"+
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request"+
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host"+
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404",
		"examplebucket.s3.amazonaws.com", nil)
	a, perr := auth.Parse(r, aws.KnownRegions())
	if perr != nil {
		t.Fatalf("Parse: %#v", perr)
	}
	q, ok := a.(auth.SigV4Query)
	if !ok {
		t.Fatalf("Parse = %#v, want SigV4Query", a)
	}
	if q.PayloadHash != (auth.Unsigned{}) || q.Expires != 24*time.Hour {
		t.Errorf("parsed payload %#v, expires %s; want UNSIGNED-PAYLOAD and 24h", q.PayloadHash, q.Expires)
	}
	v := jsonVocabulary(t)
	req := auth.Normalize(r, auth.Unread{})
	mustVerify(t, auth.VerifyPresigned(q, req, docSecret, docTime, clock.FromWire(docTime.Add(time.Hour)), v))

	// Expiry is on the emulator clock: the same wall time with the emulator
	// clock at the expiry instant is expired.
	err := auth.VerifyPresigned(q, req, docSecret, docTime, clock.FromWire(docTime.Add(24*time.Hour)), v)
	if err == nil || err.ID != v.Expired || err.Message != "Request has expired" {
		t.Errorf("at expiry: %#v, want Expired \"Request has expired\"", err)
	}

	// The lifetime counts from the emulator instant the URL was signed at:
	// signed after the clock moved a week ahead, it is valid until a day
	// past that instant, whatever its X-Amz-Date reads.
	signed := clock.FromWire(docTime.Add(7 * 24 * time.Hour))
	mustVerify(t, auth.VerifyPresignedSince(q, req, docSecret, docTime, signed, signed.Add(24*time.Hour-time.Second), v))
	if err := auth.VerifyPresignedSince(q, req, docSecret, docTime, signed, signed.Add(24*time.Hour), v); err == nil || err.ID != v.Expired {
		t.Errorf("a day after the emulator signing instant: %#v, want Expired", err)
	}
}

func streamingRequest(t *testing.T, payload, signedHeaders, signature string, extra map[string]string) auth.SigV4Header {
	t.Helper()
	headers := map[string]string{
		"Content-Encoding":             "aws-chunked",
		"X-Amz-Content-Sha256":         payload,
		"X-Amz-Date":                   "20130524T000000Z",
		"X-Amz-Decoded-Content-Length": "66560",
		"X-Amz-Storage-Class":          "REDUCED_REDUNDANCY",
		"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
			"SignedHeaders=" + signedHeaders + ", Signature=" + signature,
	}
	maps.Copy(headers, extra)
	r := serverRequest(t, http.MethodPut, "/examplebucket/chunkObject.txt", "s3.amazonaws.com", headers)
	a := parseV4Header(t, r)
	mustVerify(t, auth.Verify(a, auth.Normalize(r, auth.Unread{}), docSecret, docTime, jsonVocabulary(t)))
	return a
}

func TestDocVectorsSignedStreaming(t *testing.T) {
	const seed = "4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9"
	a := streamingRequest(t, "STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
		"content-encoding;content-length;host;x-amz-content-sha256;x-amz-date;x-amz-decoded-content-length;x-amz-storage-class",
		seed, map[string]string{"Content-Length": "66824"})
	if a.PayloadHash != (auth.StreamingSigned{}) {
		t.Errorf("payload hash %#v, want StreamingSigned", a.PayloadHash)
	}

	v := auth.ChunkSignatureVerifier(a, docSecret)
	prev := seed
	for i, c := range []struct {
		data [32]byte
		sig  string
	}{
		{hashOfAs(65536), "ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648"},
		{hashOfAs(1024), "0055627c9e194cb4542bae2aa5492e3c1575bbb81b612b7d234b86a503ef5497"},
		{sha256.Sum256(nil), "b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9"},
	} {
		if err := v.Chunk(prev, c.data, c.sig); err != nil {
			t.Fatalf("chunk %d: %v", i+1, err)
		}
		prev = c.sig
	}
}

func TestDocVectorsSignedStreamingWithTrailer(t *testing.T) {
	// The canonical request's signed headers, not the doc's printed
	// Authorization line (sigv4-checksums-endpoints.md 1.2 erratum).
	const seed = "106e2a8a18243abcf37539882f36619c00e2dfc72633413f02d3b74544bfeb8e"
	a := streamingRequest(t, "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER",
		"content-encoding;host;x-amz-content-sha256;x-amz-date;x-amz-decoded-content-length;x-amz-storage-class;x-amz-trailer",
		seed, map[string]string{"X-Amz-Trailer": "x-amz-checksum-crc32c"})

	v := auth.ChunkSignatureVerifier(a, docSecret)
	prev := seed
	for i, c := range []struct {
		data [32]byte
		sig  string
	}{
		{hashOfAs(65536), "b474d8862b1487a5145d686f57f013e54db672cee1c953b3010fb58501ef5aa2"},
		{hashOfAs(1024), "1c1344b170168f8e65b41376b44b20fe354e373826ccbbe2c1d40a8cae51e5c7"},
		{sha256.Sum256(nil), "2ca2aba2005185cf7159c6277faf83795951dd77a3a99e6e65d5c9f85863f992"},
	} {
		if err := v.Chunk(prev, c.data, c.sig); err != nil {
			t.Fatalf("chunk %d: %v", i+1, err)
		}
		prev = c.sig
	}
	trailer := sha256.Sum256([]byte("x-amz-checksum-crc32c:sOO8/Q==\n"))
	if err := v.Trailer(prev, trailer, "d81f82fc3505edab99d459891051a732e8730629a2e4a59689829ca17fe2e435"); err != nil {
		t.Fatalf("trailer: %v", err)
	}
	if err := v.Chunk(prev, trailer, "d81f82fc3505edab99d459891051a732e8730629a2e4a59689829ca17fe2e435"); err == nil {
		t.Error("the trailer signature verified as a chunk signature; the two strings to sign must differ")
	}
}

// The boto3 SigV2 presigned URL captured in sigv4-checksums-endpoints.md 2.3
// (WIRE). It reproduces with the documentation secret, so the capture used
// it.
func TestBoto3SigV2PresignedURL(t *testing.T) {
	r := serverRequest(t, http.MethodGet, "/b/k6?AWSAccessKeyId=AKIAIOSFODNN7EXAMPLE&Signature=J8h6b%2B6ZbLojGwbODQcBfutc%2Fh0%3D&Expires=1791438586",
		"127.0.0.1:4566", nil)
	a, perr := auth.Parse(r, aws.KnownRegions())
	if perr != nil {
		t.Fatalf("Parse: %#v", perr)
	}
	q, ok := a.(auth.SigV2Query)
	if !ok {
		t.Fatalf("Parse = %#v, want SigV2Query", a)
	}
	if q.RegionSource() != (aws.None{}) {
		t.Errorf("SigV2 region source = %#v, want None", q.RegionSource())
	}
	v := jsonVocabulary(t)
	req := auth.Normalize(r, auth.Unread{})
	expires := time.Unix(1791438586, 0)
	mustVerify(t, auth.VerifySigV2Presigned(q, req, "/b/k6", docSecret, clock.FromWire(expires.Add(-time.Second)), v))
	if err := auth.VerifySigV2Presigned(q, req, "/b/k6", docSecret, clock.FromWire(expires), v); err == nil || err.ID != v.Expired {
		t.Errorf("at Expires: %#v, want Expired", err)
	}
	if err := auth.VerifySigV2Presigned(q, req, "/b/k7", docSecret, clock.FromWire(expires.Add(-time.Second)), v); err == nil || err.ID != v.Mismatch {
		t.Errorf("another resource: %#v, want Mismatch", err)
	}
}

// The AWS SigV4 test suite's credentials, time, region and service
// (aws-c-auth tests/aws-signing-test-suite/v4 @ 05810b3).
const (
	suiteKeyID  = "AKIDEXAMPLE"
	suiteSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY" //nolint:gosec // the test suite's published example secret, not a credential.
	suiteDate   = "20150830T123600Z"
)

var suiteTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

// The S3 vectors above cover S3's path, signed as sent. These cover the
// other services' canonical URI (empty, "." and ".." segments removed, then
// URI-encoded) and canonical query against the AWS SigV4 test suite, an
// oracle outside this package. The suite gives each path before
// URI-encoding and encodes it once; understudy encodes the path as sent
// once more, as the SDKs do, so a suite path stands in here for a path sent
// unescaped. TestGoSDKLambdaVectors covers the second encoding.
func TestSigV4SuiteNonS3Canonicalization(t *testing.T) {
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	cred := auth.Credential{Key: suiteKeyID, Date: suiteDate[:8], Region: region, Service: "service"}
	formBody := []byte("Param1=value1")
	formHash := sha256.Sum256(formBody)
	if got := hex.EncodeToString(formHash[:]); got != "9095672bbd1f56dfc5b65f3e153adc8731a4a654192329106275f4c7b24d0b6e" {
		t.Fatalf("post-x-www-form-urlencoded body hashes to %s, not the suite's value", got)
	}
	for _, tc := range []struct {
		name, method, path, query, signature string
		headers                              map[string]string
		signed                               []string
		body                                 []byte
		payload                              auth.PayloadHash
	}{
		{name: "get-slashes", path: "//example//", signature: "9a624bd73a37c9a373b5312afbebe7a714a789de108f0bdfe846570885f57e84"},
		{name: "get-relative-relative", path: "/example1/example2/../..", signature: "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{name: "get-space", path: "/example space/", signature: "652487583200325589f1fba4c7e578f72c47cb61beeca81406b39ddec1366741"},
		{name: "get-utf8", path: "/ሴ", signature: "8318018e0b0f223aa2bbf98705b62bb787dc9c0e678f255a891fd03141be5d85"},
		{
			name: "get-vanilla-query-unreserved", path: "/",
			query:     "-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz=-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
			signature: "9c3e54bfcdf0b19771a7f523ee5669cdf59bc7cc0884027167c21bb143a40197",
		},
		{name: "get-vanilla-utf8-query", path: "/", query: "%E1%88%B4=bar", signature: "2cdec8eed098649ff3a119c94853b13c643bcf08f8b0a1d91e12c9027818dd04"},
		{
			name: "get-vanilla-query-order-encoded", path: "/", query: "Param-3=Value3&Param=Value2&%E1%88%B4=Value1",
			signature: "371d3713e185cc334048618a97f809c9ffe339c62934c032af5a0e595648fcac",
		},
		{
			name: "post-x-www-form-urlencoded", method: http.MethodPost, path: "/",
			headers: map[string]string{
				"Content-Type":         "application/x-www-form-urlencoded",
				"Content-Length":       "13",
				"X-Amz-Content-Sha256": hex.EncodeToString(formHash[:]),
			},
			signed:    []string{"content-length", "content-type", "host", "x-amz-content-sha256", "x-amz-date"},
			body:      formBody,
			payload:   auth.Hex{SHA256: formHash},
			signature: "d3875051da38690788ef43de4db0d8f280229d82040bfac253562e56c3f20e0b",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method, signed, payload := tc.method, tc.signed, tc.payload
			if method == "" {
				method, signed, payload = http.MethodGet, []string{"host", "x-amz-date"}, auth.NotSent{}
			}
			header := http.Header{}
			header.Set("X-Amz-Date", suiteDate)
			for k, v := range tc.headers {
				header.Set(k, v)
			}
			req := auth.Normalized{
				Method: method, Host: "example.amazonaws.com", Path: tc.path, RawQuery: tc.query,
				Header: header, Body: auth.Hashed{SHA256: sha256.Sum256(tc.body)},
			}
			a := auth.SigV4Header{Credential: cred, SignedHeaders: signed, Signature: tc.signature, PayloadHash: payload, Time: suiteTime}
			mustVerify(t, auth.Verify(a, req, suiteSecret, suiteTime, jsonVocabulary(t)))
		})
	}
}

// Requests aws-sdk-go-v2 v1.39.2 signed offline (aws/signer/v4 SignHTTP,
// with the suite's credentials and time): a Lambda Invoke whose path holds
// a %3A-escaped ARN, and a GET whose escaped path and query hold reserved
// and non-ASCII bytes. The SDK encodes the escaped path a second time, so
// these fail if understudy encodes it only once, or decodes it first.
func TestGoSDKLambdaVectors(t *testing.T) {
	for _, tc := range []struct {
		name, method, target, body, signedHeaders, signature string
		headers                                              map[string]string
	}{
		{
			name: "Invoke by ARN", method: http.MethodPost, body: `{"k":"v"}`,
			target:        "/2015-03-31/functions/arn%3Aaws%3Alambda%3Aus-east-1%3A123456789012%3Afunction%3Amy-fn/invocations",
			headers:       map[string]string{"Content-Type": "application/json", "Content-Length": "9", "X-Amz-Invocation-Type": "RequestResponse"},
			signedHeaders: "content-length;content-type;host;x-amz-date;x-amz-invocation-type",
			signature:     "85de11a13cb5ec7ebd21cfc998c013b88c8bf57480a53824e3bc71ff92ce2c28",
		},
		{
			// The SDK signed the query sorted; it is sent here in another
			// order, which the canonical query sorts again.
			name: "reserved and non-ASCII bytes", method: http.MethodGet,
			target:        "/2015-03-31/functions/my%20fn%2Fx%E1%88%B4/policy?Qualifier=a%2Fb%20c%2B%E1%88%B4%3D%26&Marker=-._~",
			signedHeaders: "host;x-amz-date",
			signature:     "fa5c2f768bf55957f8150cf129a4f2b2dd323ff31314b303f52e4dd3bf94ed65",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{
				"X-Amz-Date": suiteDate,
				"Authorization": "AWS4-HMAC-SHA256 Credential=" + suiteKeyID + "/20150830/us-east-1/lambda/aws4_request, SignedHeaders=" +
					tc.signedHeaders + ", Signature=" + tc.signature,
			}
			maps.Copy(headers, tc.headers)
			r := serverRequest(t, tc.method, tc.target, "lambda.us-east-1.amazonaws.com", headers)
			a := parseV4Header(t, r)
			mustVerify(t, auth.Verify(a, auth.Normalize(r, auth.Hashed{SHA256: sha256.Sum256([]byte(tc.body))}), suiteSecret, suiteTime, jsonVocabulary(t)))
		})
	}
}
