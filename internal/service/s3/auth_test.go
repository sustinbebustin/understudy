package s3_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// S3 answers credentials it cannot read with its own identities, never
// Coral's (DEFERRED.md "S3 auth", error-matrix.md 5.4 T8).
func TestS3ParseFailuresCarryS3Identities(t *testing.T) {
	const (
		date  = "20261013T120000Z"
		cred  = "AKIDEXAMPLE/20261013/us-east-1/s3/aws4_request"
		authz = "AWS4-HMAC-SHA256 Credential=" + cred + ", SignedHeaders=host;x-amz-date, Signature=abc"
		query = "X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIDEXAMPLE%2F20261013%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=" + date +
			"&X-Amz-SignedHeaders=host&X-Amz-Signature=abc"
	)
	_, refuse := s3.AuthDialect()
	for _, tc := range []struct {
		name    string
		target  string
		header  map[string]string
		want    awserr.Identity
		message string
		fields  map[string]string
	}{
		{
			name: "missing presigned parameters", target: "/b/k?X-Amz-Algorithm=AWS4-HMAC-SHA256",
			want:    s3api.IdentityAuthorizationQueryParametersError(),
			message: "Query-string authentication version 4 requires the X-Amz-Algorithm, X-Amz-Credential, X-Amz-Signature, X-Amz-Date, X-Amz-SignedHeaders, and X-Amz-Expires parameters.",
		},
		{
			name: "X-Amz-Expires out of range", target: "/b/k?" + query + "&X-Amz-Expires=604801",
			want: s3api.IdentityAuthorizationQueryParametersError(),
		},
		{
			name: "X-Amz-Expires zero", target: "/b/k?" + query + "&X-Amz-Expires=0",
			want: s3api.IdentityAuthorizationQueryParametersError(),
		},
		{
			name: "malformed header", target: "/b/k",
			header: map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=" + cred, "X-Amz-Date": date},
			want:   s3api.IdentityAuthorizationHeaderMalformed(),
		},
		{
			name: "unknown region", target: "/b/k",
			header: map[string]string{
				"Authorization":        "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261013/aws-global/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
				"X-Amz-Date":           date,
				"X-Amz-Content-Sha256": "UNSIGNED-PAYLOAD",
			},
			want:    s3api.IdentityAuthorizationHeaderMalformed(),
			message: "The authorization header is malformed; the region 'aws-global' is wrong; expecting 'us-east-1'",
			fields:  map[string]string{"Region": "us-east-1"},
		},
		{
			name: "unknown region, presigned", target: "/b/k?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIDEXAMPLE%2F20261013%2Fxx-nowhere-9%2Fs3%2Faws4_request&X-Amz-Date=" + date +
				"&X-Amz-Expires=60&X-Amz-SignedHeaders=host&X-Amz-Signature=abc",
			want:    s3api.IdentityAuthorizationQueryParametersError(),
			message: "Error parsing the X-Amz-Credential parameter; the region 'xx-nowhere-9' is wrong; expecting 'us-east-1'",
			fields:  map[string]string{"Region": "us-east-1"},
		},
		{
			name: "missing x-amz-content-sha256", target: "/b/k",
			header:  map[string]string{"Authorization": authz, "X-Amz-Date": date},
			want:    s3api.IdentityInvalidRequest(),
			message: "Missing required header for this request: x-amz-content-sha256",
		},
		{
			name: "two mechanisms", target: "/b/k?" + query + "&X-Amz-Expires=60",
			header: map[string]string{"Authorization": authz},
			want:   s3api.IdentityInvalidArgument(), fields: map[string]string{"ArgumentName": "Authorization"},
		},
		{
			name: "unknown scheme", target: "/b/k",
			header: map[string]string{"Authorization": "Bearer token"},
			want:   s3api.IdentityInvalidArgument(), message: "Unsupported Authorization Type", fields: map[string]string{"ArgumentName": "Authorization"},
		},
		{
			name: "SigV2 without a request time", target: "/b/k",
			header:  map[string]string{"Authorization": "AWS AKIDEXAMPLE:c2lnbmF0dXJl"},
			want:    s3api.IdentityAccessDenied(),
			message: "AWS authentication requires a valid Date or x-amz-date header",
		},
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.target, nil)
		for k, v := range tc.header {
			r.Header.Set(k, v)
		}
		_, perr := auth.Parse(r, aws.KnownRegions())
		if perr == nil {
			t.Errorf("%s: Parse accepted the request", tc.name)
			continue
		}
		err := refuse(perr)
		if err.ID != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, err.ID.Shape, tc.want.Shape)
		}
		if tc.message != "" && err.Message != tc.message {
			t.Errorf("%s: message %q, want %q", tc.name, err.Message, tc.message)
		}
		for name, want := range tc.fields {
			if got := fieldValue(err, name); got != want {
				t.Errorf("%s: element %s = %q, want %q", tc.name, name, got, want)
			}
		}
	}
}

// The verifiers answer in S3's identities: a mismatch, a skewed request,
// an expired URL, an unknown key and the token failures.
func TestS3AuthVocabulary(t *testing.T) {
	v, _ := s3.AuthDialect()
	for name, got := range map[string]awserr.Identity{
		"SignatureDoesNotMatch": v.Mismatch, "RequestTimeTooSkewed": v.Skewed, "AccessDenied": v.Expired,
		"InvalidAccessKeyId": v.UnknownKey, "InvalidToken": v.InvalidToken, "ExpiredToken": v.ExpiredToken,
		"InvalidRequest": v.Conflicting, "AuthorizationHeaderMalformed": v.Incomplete,
		"XAmzContentSHA256Mismatch": v.PayloadMismatch,
	} {
		if got.Shape.Name() != name {
			t.Errorf("vocabulary slot for %s holds %s", name, got.Shape)
		}
	}
	if v.HeadersNotSigned != s3api.IdentityAccessDenied() || v.Missing != s3api.IdentityAccessDenied() {
		t.Errorf("HeadersNotSigned %s and Missing %s, want AccessDenied", v.HeadersNotSigned.Shape, v.Missing.Shape)
	}
	if v.Dialect != auth.S3Dialect {
		t.Error("the S3 vocabulary does not use S3's wording")
	}
}
