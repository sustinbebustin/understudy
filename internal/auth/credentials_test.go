package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

func queryVocabulary(t *testing.T) auth.Vocabulary {
	t.Helper()
	v, ok := auth.VocabularyFor(awserr.QueryFamily)
	if !ok {
		t.Fatal("VocabularyFor(QueryFamily) reported false")
	}
	return v
}

// An unknown key, a wrong session token and an expired one carry each
// family's identity and text (ADR 0012 pre-tag review amendment): Coral
// answers an unknown key and a wrong token alike (dynalite index.js
// L296-L372); S3 has an identity for each and names an unknown key.
func TestCredentialFailuresPerFamily(t *testing.T) {
	type want struct {
		code, message string
		status        int
	}
	for _, tc := range []struct {
		family                 string
		vocabulary             auth.Vocabulary
		unknown, token, expiry want
	}{
		{
			"json", jsonVocabulary(t),
			want{"UnrecognizedClientException", "The security token included in the request is invalid.", 400},
			want{"UnrecognizedClientException", "The security token included in the request is invalid.", 400},
			want{"ExpiredTokenException", "The security token included in the request is expired", 403},
		},
		{
			"query", queryVocabulary(t),
			want{"InvalidClientTokenId", "The security token included in the request is invalid.", 403},
			want{"InvalidClientTokenId", "The security token included in the request is invalid.", 403},
			want{"ExpiredToken", "The security token included in the request is expired", 403},
		},
	} {
		for what, got := range map[string]struct {
			err  *awserr.Error
			want want
		}{
			"unknown key": {auth.UnknownKey("AKIAEXAMPLE", tc.vocabulary), tc.unknown},
			"bad token":   {auth.InvalidToken(tc.vocabulary), tc.token},
			"expired":     {auth.ExpiredToken(tc.vocabulary), tc.expiry},
		} {
			name := string(got.err.ID.Shape)
			if got.err.ID.Shape.Name() != got.want.code || got.err.Message != got.want.message || got.err.ID.Status != got.want.status {
				t.Errorf("%s %s = %s %d %q, want %s %d %q", tc.family, what, name, got.err.ID.Status, got.err.Message, got.want.code, got.want.status, got.want.message)
			}
			if len(got.err.Fields) != 0 {
				t.Errorf("%s %s carries elements %v; the Coral families have none", tc.family, what, got.err.Fields)
			}
		}
	}
}

func TestCredentialFailuresS3Dialect(t *testing.T) {
	v := s3Vocabulary(t)
	err := auth.UnknownKey("AKIAEXAMPLE", v)
	if err.Message != "The AWS Access Key Id you provided does not exist in our records." || field(t, err, "AWSAccessKeyId") != "AKIAEXAMPLE" {
		t.Errorf("S3 unknown key = %q %v", err.Message, err.Fields)
	}
	if got := auth.InvalidToken(v).Message; got != "The provided token is malformed or otherwise invalid." {
		t.Errorf("S3 invalid token message %q", got)
	}
	if got := auth.ExpiredToken(v).Message; got != "The provided token has expired." {
		t.Errorf("S3 expired token message %q", got)
	}
}

func TestSessionTokenFromHeaderOrQuery(t *testing.T) {
	for _, tc := range []struct {
		name, target, header, want string
		ok                         bool
	}{
		{"header", "/", "tok/en+1", "tok/en+1", true},
		{"SigV4 query", "/?X-Amz-Security-Token=tok%2Fen%2B1", "", "tok/en+1", true},
		{"SigV2 query, lowercase", "/?a=b&x-amz-security-token=tok%2Fen%2B1", "", "tok/en+1", true},
		{"header wins", "/?X-Amz-Security-Token=query", "header", "header", true},
		{"none", "/?X-Amz-Expires=5", "", "", false},
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.target, nil)
		if tc.header != "" {
			r.Header.Set("X-Amz-Security-Token", tc.header)
		}
		got, ok := auth.SessionToken(r)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: SessionToken = %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// A presigned request's hoisted x-amz-* parameters read as headers, a
// header sent wins, and the presign's own parameters never do.
func TestHoistedHeader(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPut,
		"/k?X-Amz-Content-Sha256=STREAMING-UNSIGNED-PAYLOAD-TRAILER&x-amz-trailer=x-amz-checksum-crc32&X-Amz-Meta-Sent=query&partNumber=1"+
			"&X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=test&X-Amz-Date=20260101T000000Z&X-Amz-Expires=60&X-Amz-SignedHeaders=host&X-Amz-Signature=00&X-Amz-Security-Token=t", nil)
	r.Header.Set("X-Amz-Meta-Sent", "header")
	got := auth.HoistedHeader(r)
	for name, want := range map[string]string{
		"X-Amz-Content-Sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER",
		"X-Amz-Trailer":        "x-amz-checksum-crc32",
		"X-Amz-Meta-Sent":      "header",
	} {
		if v := got.Values(name); len(v) != 1 || v[0] != want {
			t.Errorf("HoistedHeader %s = %q, want [%q]", name, v, want)
		}
	}
	for _, name := range []string{"Partnumber", "X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-Signedheaders", "X-Amz-Signature", "X-Amz-Security-Token"} {
		if v := got.Values(name); len(v) != 0 {
			t.Errorf("HoistedHeader %s = %q, want it left out", name, v)
		}
	}
	if len(r.Header) != 1 {
		t.Errorf("r.Header = %v, want it unchanged", r.Header)
	}
}
