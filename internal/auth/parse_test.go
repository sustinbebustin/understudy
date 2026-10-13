package auth_test

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// AWS's published example key id and credential scope, not credentials.
const (
	docKeyID      = "AKIAIOSFODNN7EXAMPLE"                                    //nolint:gosec // see above
	docCredential = "AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request" //nolint:gosec // see above
)

// docAuthorization is a SigV4 Authorization header over the documentation
// credential with the given credential scope and signed headers.
func docAuthorization(credential, signedHeaders string) string {
	return "AWS4-HMAC-SHA256 Credential=" + credential + ", SignedHeaders=" + signedHeaders + ", Signature=00"
}

// S3 accepts x-amz-content-sha256 only as one of its literals or a SHA-256
// in hex (sigv4-checksums-endpoints.md 3.1).
func TestParsePayloadHash(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  auth.PayloadHash
	}{
		{"UNSIGNED-PAYLOAD", auth.Unsigned{}},
		{"STREAMING-UNSIGNED-PAYLOAD-TRAILER", auth.StreamingUnsignedTrailer{}},
		{"STREAMING-AWS4-HMAC-SHA256-PAYLOAD", auth.StreamingSigned{}},
		{"STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER", auth.StreamingSignedTrailer{}},
		{emptyHash, auth.Hex{SHA256: [32]byte{
			0xe3, 0xb0, 0xc4, 0x42, 0x98, 0xfc, 0x1c, 0x14, 0x9a, 0xfb, 0xf4, 0xc8, 0x99, 0x6f, 0xb9, 0x24,
			0x27, 0xae, 0x41, 0xe4, 0x64, 0x9b, 0x93, 0x4c, 0xa4, 0x95, 0x99, 0x1b, 0x78, 0x52, 0xb8, 0x55,
		}}},
	} {
		r := serverRequest(t, http.MethodGet, "/b/k", "localhost:4566", map[string]string{
			"X-Amz-Content-Sha256": tc.value,
			"X-Amz-Date":           "20130524T000000Z",
			"Authorization":        docAuthorization(docCredential, "host;x-amz-date"),
		})
		if got := parseV4Header(t, r).PayloadHash; got != tc.want {
			t.Errorf("x-amz-content-sha256 %q parsed as %#v, want %#v", tc.value, got, tc.want)
		}
	}
}

// A header request without x-amz-content-sha256 is NotSent for services
// whose verifier hashes the body, but S3 requires the header on every SigV4
// request and answers its absence with a client error.
func TestParseMissingPayloadHash(t *testing.T) {
	headers := func(credential string) map[string]string {
		return map[string]string{
			"X-Amz-Date":    "20130524T000000Z",
			"Authorization": docAuthorization(credential, "host;x-amz-date"),
		}
	}
	r := serverRequest(t, http.MethodPost, "/", "localhost:4566", headers("AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/sqs/aws4_request"))
	if got := parseV4Header(t, r).PayloadHash; got != (auth.NotSent{}) {
		t.Errorf("SQS request without x-amz-content-sha256 parsed as %#v, want NotSent", got)
	}
	r = serverRequest(t, http.MethodGet, "/b/k", "localhost:4566", headers(docCredential))
	want := auth.Invalid{Mechanism: auth.HeaderV4, Problem: auth.MissingPayloadHash, Message: "Missing required header for this request: x-amz-content-sha256"}
	if _, perr := auth.Parse(r, aws.KnownRegions()); perr != want {
		t.Errorf("S3 request without x-amz-content-sha256: Parse error %#v, want %#v", perr, want)
	}
}

// Uppercase hex is refused: the canonical request's payload line is the
// lowercase hash, so an uppercase value could never verify, and every
// observed client sends lowercase.
func TestParseRejectsMalformedPayloadHash(t *testing.T) {
	for _, value := range []string{
		emptyHash[:63],
		emptyHash + "00",
		strings.ToUpper(emptyHash[:63]) + "g",
		strings.ToUpper(emptyHash),
		"unsigned-payload",
	} {
		r := serverRequest(t, http.MethodGet, "/b/k", "localhost:4566", map[string]string{
			"X-Amz-Content-Sha256": value,
			"X-Amz-Date":           "20130524T000000Z",
			"Authorization":        docAuthorization(docCredential, "host;x-amz-date"),
		})
		_, perr := auth.Parse(r, aws.KnownRegions())
		if inv, ok := perr.(auth.Invalid); !ok || inv.Problem != auth.InvalidPayloadHash {
			t.Errorf("x-amz-content-sha256 %q: Parse error %#v, want InvalidPayloadHash", value, perr)
		}
	}
}

// docPresignQuery is the documentation presigned URL's query without its
// signature, with replace applied: a value of "" drops the parameter.
func docPresignQuery(replace map[string]string) string {
	params := [][2]string{
		{"X-Amz-Algorithm", "AWS4-HMAC-SHA256"},
		{"X-Amz-Credential", "AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request"},
		{"X-Amz-Date", "20130524T000000Z"},
		{"X-Amz-Expires", "86400"},
		{"X-Amz-SignedHeaders", "host"},
		{"X-Amz-Signature", "00"},
	}
	parts := make([]string, 0, len(params))
	for _, p := range params {
		v, ok := replace[p[0]]
		if !ok {
			v = p[1]
		}
		if v != "" {
			parts = append(parts, p[0]+"="+v)
		}
	}
	return strings.Join(parts, "&")
}

func TestParseErrors(t *testing.T) {
	invalid := func(m auth.Mechanism, p auth.Problem) auth.ParseError { return auth.Invalid{Mechanism: m, Problem: p} }
	v4 := func(authz string) map[string]string {
		return map[string]string{"X-Amz-Date": "20130524T000000Z", "Authorization": authz}
	}
	header := docAuthorization(docCredential, "host;x-amz-date")
	v2Header := "AWS " + docKeyID + ":c2ln"
	// withAlgorithm is a complete Authorization header in scheme alg.
	withAlgorithm := func(alg, credential, signedHeaders string) string {
		return alg + " Credential=" + credential + ", SignedHeaders=" + signedHeaders + ", Signature=00"
	}
	for _, tc := range []struct {
		name    string
		target  string
		headers map[string]string
		want    auth.ParseError
	}{
		{"header and X-Amz-Algorithm", "/b/k?" + docPresignQuery(nil), v4(header), auth.Conflict{First: auth.AuthorizationHeader, Second: auth.AlgorithmQuery}},
		{"header and X-Amz-Credential", "/b/k?X-Amz-Credential=" + docCredential, v4(header), auth.Conflict{First: auth.AuthorizationHeader, Second: auth.CredentialQuery}},
		{"header and SigV2 query", "/b/k?AWSAccessKeyId=a&Signature=b&Expires=1", v4(header), auth.Conflict{First: auth.AuthorizationHeader, Second: auth.V2KeyQuery}},
		{"Bearer header and X-Amz-Algorithm", "/b/k?" + docPresignQuery(nil), v4("Bearer abc"), auth.Conflict{First: auth.AuthorizationHeader, Second: auth.AlgorithmQuery}},
		{"SigV4 and SigV2 query", "/b/k?" + docPresignQuery(nil) + "&AWSAccessKeyId=a", nil, auth.Conflict{First: auth.AlgorithmQuery, Second: auth.V2KeyQuery}},
		{"all three", "/b/k?" + docPresignQuery(nil) + "&AWSAccessKeyId=a", v4(header), auth.Conflict{First: auth.AuthorizationHeader, Second: auth.AlgorithmQuery}},
		{"Bearer token", "/", v4("Bearer abc"), auth.UnknownScheme{}},
		{"bare AWS4", "/", v4("AWS4"), auth.UnknownScheme{}},
		{"SigV4A", "/", v4(withAlgorithm("AWS4-ECDSA-P256-SHA256", "AKIAIOSFODNN7EXAMPLE/20130524/s3/aws4_request", "host;x-amz-date")), invalid(auth.HeaderV4, auth.UnsupportedAlgorithm)},
		{"unknown AWS4 algorithm", "/", v4(withAlgorithm("AWS4-HMAC-SHA25", docCredential, "host;x-amz-date")), invalid(auth.HeaderV4, auth.UnsupportedAlgorithm)},
		{"algorithm without elements", "/", v4("AWS4-HMAC-SHA256"), invalid(auth.HeaderV4, auth.Malformed)},
		// Coral's order: missing elements, date format, host, algorithm,
		// credential shape, credential pieces.
		{"unknown algorithm without elements", "/", v4("AWS4-HMAC-SHA25 Credential=" + docCredential), invalid(auth.HeaderV4, auth.Malformed)},
		{"unknown algorithm and a bad date", "/", map[string]string{"X-Amz-Date": "201", "Authorization": withAlgorithm("AWS4-HMAC-SHA25", docCredential, "host")}, invalid(auth.HeaderV4, auth.Malformed)},
		{"unknown algorithm and host not signed", "/", v4(withAlgorithm("AWS4-HMAC-SHA25", docCredential, "x-amz-date")), invalid(auth.HeaderV4, auth.HostNotSigned)},
		{"unknown algorithm and four scope elements", "/", v4(withAlgorithm("AWS4-HMAC-SHA25", "a/b/c/d", "host")), invalid(auth.HeaderV4, auth.UnsupportedAlgorithm)},
		{"host not signed and a bad scope", "/", v4(docAuthorization("AKIAIOSFODNN7EXAMPLE/b/c/s3/e", "x-amz-date")), invalid(auth.HeaderV4, auth.HostNotSigned)},
		{"no date header", "/", map[string]string{"Authorization": header}, invalid(auth.HeaderV4, auth.Malformed)},
		{"bad X-Amz-Date", "/", map[string]string{"X-Amz-Date": "2013-05-24", "Authorization": header}, invalid(auth.HeaderV4, auth.Malformed)},
		{"bad Date", "/", map[string]string{"Date": "yesterday", "Authorization": header}, invalid(auth.HeaderV4, auth.Malformed)},
		{"four scope elements", "/", v4(docAuthorization("AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3", "host")), invalid(auth.HeaderV4, auth.Malformed)},
		{"bad scope date", "/", v4(docAuthorization("AKIAIOSFODNN7EXAMPLE/2013-5-24/us-east-1/s3/aws4_request", "host")), invalid(auth.HeaderV4, auth.InvalidScopeDate)},
		{"bad scope date and region", "/", v4(docAuthorization("AKIAIOSFODNN7EXAMPLE/b/c/s3/aws4_request", "host")), auth.UnknownRegion{Mechanism: auth.HeaderV4, Region: "c", Service: "s3"}},
		{"unknown region", "/", v4(docAuthorization("AKIAIOSFODNN7EXAMPLE/20130524/xx-nowhere-1/s3/aws4_request", "host")), auth.UnknownRegion{Mechanism: auth.HeaderV4, Region: "xx-nowhere-1", Service: "s3"}},
		{"aws-global region", "/", v4(docAuthorization("AKIAIOSFODNN7EXAMPLE/20130524/aws-global/s3/aws4_request", "host")), auth.UnknownRegion{Mechanism: auth.HeaderV4, Region: "aws-global", Service: "s3"}},
		{"bad terminator", "/", v4(docAuthorization("AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws5_request", "host")), invalid(auth.HeaderV4, auth.InvalidTerminator)},
		{"host not signed", "/", v4(docAuthorization(docCredential, "x-amz-date")), invalid(auth.HeaderV4, auth.HostNotSigned)},
		{"uppercase signed header", "/", v4(docAuthorization(docCredential, "Host")), invalid(auth.HeaderV4, auth.Malformed)},
		{"SigV2 header without signature", "/", v4("AWS AKIAIOSFODNN7EXAMPLE"), invalid(auth.HeaderV2, auth.Malformed)},
		{"SigV2 header without a date", "/", map[string]string{"Authorization": v2Header}, invalid(auth.HeaderV2, auth.InvalidRequestTime)},
		// SigV2 dates are HTTP dates, even in X-Amz-Date.
		{"SigV2 header with an ISO 8601 X-Amz-Date", "/", v4(v2Header), invalid(auth.HeaderV2, auth.InvalidRequestTime)},
		{"SigV2 header with a bad Date", "/", map[string]string{"Date": "yesterday", "Authorization": v2Header}, invalid(auth.HeaderV2, auth.InvalidRequestTime)},
		{"SigV2 query without Expires", "/b/k?AWSAccessKeyId=a&Signature=b", nil, invalid(auth.QueryV2, auth.Malformed)},
		{"SigV2 query with a bad Expires", "/b/k?AWSAccessKeyId=a&Signature=b&Expires=soon", nil, invalid(auth.QueryV2, auth.Malformed)},
		{
			"presign missing parameters", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Signature": "", "X-Amz-Date": "", "X-Amz-Expires": ""}), nil,
			auth.MissingQueryParameters{Names: []string{"X-Amz-Signature", "X-Amz-Date", "X-Amz-Expires"}},
		},
		{
			"presign with only a credential", "/b/k?X-Amz-Credential=" + docCredential, nil,
			auth.MissingQueryParameters{Names: []string{"X-Amz-Algorithm", "X-Amz-Signature", "X-Amz-SignedHeaders", "X-Amz-Date", "X-Amz-Expires"}},
		},
		{"presign SigV4A", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Algorithm": "AWS4-ECDSA-P256-SHA256"}), nil, invalid(auth.QueryV4, auth.UnsupportedAlgorithm)},
		{"presign unknown region", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Credential": url.QueryEscape(strings.Replace(docCredential, "us-east-1", "mars-1", 1))}), nil, auth.UnknownRegion{Mechanism: auth.QueryV4, Region: "mars-1", Service: "s3"}},
		{"presign bad date", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Date": "20130524"}), nil, invalid(auth.QueryV4, auth.Malformed)},
		{"presign bad date and SigV4A", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Date": "20130524", "X-Amz-Algorithm": "AWS4-ECDSA-P256-SHA256"}), nil, invalid(auth.QueryV4, auth.Malformed)},
		{"presign host not signed and SigV4A", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-SignedHeaders": "x-amz-date", "X-Amz-Algorithm": "AWS4-ECDSA-P256-SHA256"}), nil, invalid(auth.QueryV4, auth.HostNotSigned)},
		{"presign SigV4A and an unknown region", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Algorithm": "AWS4-ECDSA-P256-SHA256", "X-Amz-Credential": url.QueryEscape(strings.Replace(docCredential, "us-east-1", "mars-1", 1))}), nil, invalid(auth.QueryV4, auth.UnsupportedAlgorithm)},
		{"presign bad scope date", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Credential": url.QueryEscape(strings.Replace(docCredential, "20130524", "b", 1))}), nil, invalid(auth.QueryV4, auth.InvalidScopeDate)},
		{"X-Amz-Expires not a number", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Expires": "1h"}), nil, invalid(auth.QueryV4, auth.Malformed)},
		{"X-Amz-Expires -1", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Expires": "-1"}), nil, invalid(auth.QueryV4, auth.Malformed)},
		{"X-Amz-Expires 0", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Expires": "0"}), nil, invalid(auth.QueryV4, auth.Malformed)},
		{"X-Amz-Expires 604801", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Expires": "604801"}), nil, invalid(auth.QueryV4, auth.Malformed)},
		// These overflow time.Duration if converted before the bound check.
		{"X-Amz-Expires 9223372037", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Expires": "9223372037"}), nil, invalid(auth.QueryV4, auth.Malformed)},
		{"X-Amz-Expires 18446744074", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Expires": "18446744074"}), nil, invalid(auth.QueryV4, auth.Malformed)},
		{"X-Amz-Expires 18447348073", "/b/k?" + docPresignQuery(map[string]string{"X-Amz-Expires": "18447348073"}), nil, invalid(auth.QueryV4, auth.Malformed)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := serverRequest(t, http.MethodGet, tc.target, "localhost:4566", tc.headers)
			a, perr := auth.Parse(r, aws.KnownRegions())
			// The texts are checked elsewhere; here only their presence.
			got := perr
			switch e := got.(type) {
			case auth.Invalid:
				if e.Message == "" {
					t.Errorf("Invalid without a message: %#v", e)
				}
				e.Message = ""
				got = e
			case auth.UnknownRegion:
				if e.Message == "" {
					t.Errorf("UnknownRegion without a message: %#v", e)
				}
				e.Message = ""
				got = e
			case auth.Conflict, auth.MissingQueryParameters, auth.UnknownScheme:
				// Compared whole: they carry no free-text message.
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse = %#v, %#v; want error %#v", a, perr, tc.want)
			}
		})
	}
}

// Like Coral, Parse reports every wrong piece of the credential scope in one
// message, region first, and a date that is not yyyyMMdd with the request's
// day and time (dynalite index.js L296-L372).
func TestParseReportsEveryWrongScopePiece(t *testing.T) {
	r := serverRequest(t, http.MethodGet, "/", "localhost:4566", map[string]string{
		"X-Amz-Date":    "20130524T000000Z",
		"Authorization": docAuthorization("AKIAIOSFODNN7EXAMPLE/b/c/s3/e", "host"),
	})
	_, perr := auth.Parse(r, aws.KnownRegions())
	const want = "Credential should be scoped to a valid region, not 'c'. Add the name to the regions array in the understudy configuration to use it. " +
		"Credential should be scoped with a valid terminator: 'aws4_request', not 'e'. " +
		"Date in Credential scope does not match YYYYMMDD from ISO-8601 version of date from HTTP: 'b' != '20130524', from '20130524T000000Z'."
	if got, ok := perr.(auth.UnknownRegion); !ok || got.Region != "c" || got.Message != want {
		t.Errorf("Parse error = %#v; want UnknownRegion c with\n%s", perr, want)
	}
}

func TestParseExpiresBounds(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"1", time.Second},
		{"604800", 7 * 24 * time.Hour},
	} {
		r := serverRequest(t, http.MethodGet, "/b/k?"+docPresignQuery(map[string]string{"X-Amz-Expires": tc.value}), "localhost:4566", nil)
		a, perr := auth.Parse(r, aws.KnownRegions())
		if q, ok := a.(auth.SigV4Query); perr != nil || !ok || q.Expires != tc.want {
			t.Errorf("X-Amz-Expires=%s: Parse = %#v, %#v; want SigV4Query expiring after %s", tc.value, a, perr, tc.want)
		}
	}
}

// X-Amz-Date wins over Date; Date is read in HTTP form, including the
// numeric-zone form AWS's own examples use.
func TestParseRequestTime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"X-Amz-Date", map[string]string{"X-Amz-Date": "20130524T000000Z"}},
		{"X-Amz-Date wins over Date", map[string]string{"X-Amz-Date": "20130524T000000Z", "Date": "Mon, 01 Jan 2001 00:00:00 GMT"}},
		{"Date in GMT", map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT"}},
		{"Date with a numeric zone", map[string]string{"Date": "Fri, 24 May 2013 02:00:00 +0200"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.headers["Authorization"] = docAuthorization(docCredential, "host")
			tc.headers["X-Amz-Content-Sha256"] = emptyHash
			r := serverRequest(t, http.MethodGet, "/", "localhost:4566", tc.headers)
			if got := parseV4Header(t, r).Time; !got.Equal(docTime) || got.Location() != time.UTC {
				t.Errorf("Time = %s, want %s in UTC", got, docTime)
			}
		})
	}
}

func TestParseErrorAWSError(t *testing.T) {
	families := []struct {
		name                          string
		family                        awserr.Family
		missing, incomplete, mismatch awserr.Identity
	}{
		{"JSON", awserr.JSONFamily, awserr.MissingAuthenticationTokenException(), awserr.IncompleteSignatureException(), awserr.InvalidSignatureException()},
		{"Query", awserr.QueryFamily, awserr.MissingAuthenticationToken(), awserr.IncompleteSignature(), awserr.SignatureDoesNotMatch()},
	}
	for _, f := range families {
		v, ok := auth.VocabularyFor(f.family)
		if !ok {
			t.Fatalf("VocabularyFor(%s) reported false", f.name)
		}
		if got := auth.Missing(v); got == nil || got.ID != f.missing || got.Message != "Request is missing Authentication Token" {
			t.Errorf("%s family: Missing = %#v; want %s", f.name, got, f.missing.Shape)
		}
		for _, tc := range []struct {
			name    string
			err     auth.ParseError
			want    awserr.Identity
			message string
		}{
			// The texts are DynamoDB's (error-matrix.md 5.5, T8).
			{
				"header and X-Amz-Algorithm",
				auth.Conflict{First: auth.AuthorizationHeader, Second: auth.AlgorithmQuery},
				f.mismatch,
				"Found both 'X-Amz-Algorithm' as a query-string param and 'Authorization' as HTTP header.",
			},
			// Coral detects no other pair; these texts are understudy's own
			// and name what the request carried.
			{
				"header and X-Amz-Credential",
				auth.Conflict{First: auth.AuthorizationHeader, Second: auth.CredentialQuery},
				f.mismatch,
				"Found both 'X-Amz-Credential' as a query-string param and 'Authorization' as HTTP header.",
			},
			{
				"header and AWSAccessKeyId",
				auth.Conflict{First: auth.AuthorizationHeader, Second: auth.V2KeyQuery},
				f.mismatch,
				"Found both 'AWSAccessKeyId' as a query-string param and 'Authorization' as HTTP header.",
			},
			{
				"X-Amz-Algorithm and AWSAccessKeyId",
				auth.Conflict{First: auth.AlgorithmQuery, Second: auth.V2KeyQuery},
				f.mismatch,
				"Found both 'X-Amz-Algorithm' and 'AWSAccessKeyId' as query-string params.",
			},
			{
				"X-Amz-Credential and AWSAccessKeyId",
				auth.Conflict{First: auth.CredentialQuery, Second: auth.V2KeyQuery},
				f.mismatch,
				"Found both 'X-Amz-Credential' and 'AWSAccessKeyId' as query-string params.",
			},
			{"unknown scheme", auth.UnknownScheme{}, f.missing, "Request is missing Authentication Token"},
			{
				"missing query parameters",
				auth.MissingQueryParameters{Names: []string{"X-Amz-Algorithm", "X-Amz-Date"}},
				f.incomplete,
				"AWS query-string parameters must include 'X-Amz-Algorithm'. AWS query-string parameters must include 'X-Amz-Date'. Re-examine the query-string parameters.",
			},
			{"malformed", auth.Invalid{Mechanism: auth.HeaderV4, Problem: auth.Malformed, Message: "m"}, f.incomplete, "m"},
			{"unsupported algorithm", auth.Invalid{Mechanism: auth.HeaderV4, Problem: auth.UnsupportedAlgorithm, Message: "m"}, f.incomplete, "m"},
			{"invalid payload hash", auth.Invalid{Mechanism: auth.HeaderV4, Problem: auth.InvalidPayloadHash, Message: "m"}, f.incomplete, "m"},
			{"invalid request time", auth.Invalid{Mechanism: auth.HeaderV2, Problem: auth.InvalidRequestTime, Message: "m"}, f.incomplete, "m"},
			{"missing payload hash", auth.Invalid{Mechanism: auth.HeaderV4, Problem: auth.MissingPayloadHash, Message: "m"}, f.incomplete, "m"},
			{"unknown region", auth.UnknownRegion{Mechanism: auth.QueryV4, Region: "aws-global", Message: "m"}, f.mismatch, "m"},
			{"invalid terminator", auth.Invalid{Mechanism: auth.HeaderV4, Problem: auth.InvalidTerminator, Message: "m"}, f.mismatch, "m"},
			{"invalid scope date", auth.Invalid{Mechanism: auth.HeaderV4, Problem: auth.InvalidScopeDate, Message: "m"}, f.mismatch, "m"},
			{"host not signed", auth.Invalid{Mechanism: auth.HeaderV4, Problem: auth.HostNotSigned, Message: "m"}, f.mismatch, "m"},
		} {
			got := tc.err.AWSError(v)
			if got == nil || got.ID != tc.want || got.Message != tc.message {
				t.Errorf("%s family, %s: AWSError = %#v; want %s %q", f.name, tc.name, got, tc.want.Shape, tc.message)
			}
		}
	}
}

func TestParseErrorAWSErrorS3Dialect(t *testing.T) {
	coral := jsonVocabulary(t)
	v := s3Vocabulary(t)
	// s3.md 12: S3 names every parameter whichever are missing.
	missing := auth.MissingQueryParameters{Names: []string{"X-Amz-Date"}}
	if got, want := missing.AWSError(v).Message, "Query-string authentication version 4 requires the X-Amz-Algorithm, X-Amz-Credential, X-Amz-Signature, X-Amz-Date, X-Amz-SignedHeaders, and X-Amz-Expires parameters."; got != want {
		t.Errorf("MissingQueryParameters: message %q, want %q", got, want)
	}
	// S3 answers these with identities no Vocabulary holds (InvalidArgument,
	// AccessDenied, AuthorizationHeaderMalformed, InvalidRequest) and
	// switches on the variant, so the dialect leaves Coral's answer unchanged
	// rather than pair S3's text with an identity S3 does not send.
	for _, e := range []auth.ParseError{
		auth.Conflict{First: auth.AuthorizationHeader, Second: auth.AlgorithmQuery},
		auth.Conflict{First: auth.AlgorithmQuery, Second: auth.V2KeyQuery},
		auth.UnknownScheme{},
		auth.Invalid{Mechanism: auth.HeaderV2, Problem: auth.InvalidRequestTime, Message: "m"},
		auth.Invalid{Mechanism: auth.HeaderV4, Problem: auth.MissingPayloadHash, Message: "m"},
		auth.UnknownRegion{Mechanism: auth.HeaderV4, Region: "aws-global", Message: "m"},
	} {
		if got, want := e.AWSError(v), e.AWSError(coral); !reflect.DeepEqual(got, want) {
			t.Errorf("%#v: AWSError under S3Dialect = %#v, want Coral's %#v", e, got, want)
		}
	}
}

// S3 answers a SigV2 header request without a readable time with
// AccessDenied and its text, which it builds from the Invalid variant.
func TestParseSigV2RequestTime(t *testing.T) {
	const want = "AWS authentication requires a valid Date or x-amz-date header"
	r := serverRequest(t, http.MethodGet, "/b/k", "localhost:4566", map[string]string{"Authorization": "AWS " + docKeyID + ":c2ln"})
	if _, perr := auth.Parse(r, aws.KnownRegions()); perr != (auth.Invalid{Mechanism: auth.HeaderV2, Problem: auth.InvalidRequestTime, Message: want}) {
		t.Errorf("Parse error = %#v; want InvalidRequestTime %q", perr, want)
	}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		stamp   string
	}{
		{"Date", map[string]string{"Date": "Tue, 27 Mar 2007 19:36:42 +0000"}, "Tue, 27 Mar 2007 19:36:42 +0000"},
		{"X-Amz-Date wins over Date", map[string]string{"X-Amz-Date": "Tue, 27 Mar 2007 19:36:42 GMT", "Date": "Mon, 01 Jan 2001 00:00:00 GMT"}, "Tue, 27 Mar 2007 19:36:42 GMT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.headers["Authorization"] = "AWS " + docKeyID + ":c2ln"
			a, perr := auth.Parse(serverRequest(t, http.MethodGet, "/b/k", "localhost:4566", tc.headers), aws.KnownRegions())
			h, ok := a.(auth.SigV2Header)
			if perr != nil || !ok {
				t.Fatalf("Parse = %#v, %#v; want SigV2Header", a, perr)
			}
			if at := time.Date(2007, 3, 27, 19, 36, 42, 0, time.UTC); !h.Time.Equal(at) || h.Stamp != tc.stamp {
				t.Errorf("Time %s, Stamp %q; want %s, %q", h.Time, h.Stamp, at, tc.stamp)
			}
		})
	}
}
