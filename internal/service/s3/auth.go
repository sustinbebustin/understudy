package s3

import (
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// AuthDialect is S3's auth vocabulary and its answer to credentials Parse
// cannot read, which the edge uses for every request it claims for S3, in
// both sigv4 modes (differences.md "S3 auth"). S3's identities come from its
// overlay; the texts and extra elements are auth's S3Dialect.
func AuthDialect() (v auth.Vocabulary, refuse func(auth.ParseError) *awserr.Error) {
	v = authVocabulary()
	return v, func(e auth.ParseError) *awserr.Error { return parseError(e, v) }
}

// authVocabulary is S3's identity for each auth failure (error-matrix.md 5.4
// T8): a request without usable credentials is AccessDenied, as S3 answers
// an anonymous request it refuses, and an unreadable Authorization header
// AuthorizationHeaderMalformed.
func authVocabulary() auth.Vocabulary {
	denied := s3api.IdentityAccessDenied()
	return auth.Vocabulary{
		Missing:          denied,
		Incomplete:       s3api.IdentityAuthorizationHeaderMalformed(),
		Mismatch:         s3api.IdentitySignatureDoesNotMatch(),
		Skewed:           s3api.IdentityRequestTimeTooSkewed(),
		Expired:          denied,
		HeadersNotSigned: denied,
		Conflicting:      s3api.IdentityInvalidRequest(),
		UnknownKey:       s3api.IdentityInvalidAccessKeyId(),
		InvalidToken:     s3api.IdentityInvalidToken(),
		ExpiredToken:     s3api.IdentityExpiredToken(),
		PayloadMismatch:  s3api.IdentityXAmzContentSHA256Mismatch(),
		Dialect:          auth.S3Dialect,
	}
}

// The texts S3 gives parse failures no Vocabulary slot holds. The conflict
// and unknown-scheme texts are UNVERIFIED against AWS (s3rver and minio
// send them).
const (
	conflictMessage      = "Only one auth mechanism allowed; only the X-Amz-Algorithm query parameter, Signature query string parameter or the Authorization header should be specified"
	unknownSchemeMessage = "Unsupported Authorization Type"
)

// parseError is S3's answer to credentials Parse refused, by variant
// (differences.md "S3 auth"). A missing x-amz-content-sha256 is matched before
// the other header failures: it is a HeaderV4 failure, but S3 answers
// InvalidRequest, not AuthorizationHeaderMalformed. The status of
// AccessDenied for an unreadable SigV2 request time is unrecorded
// (S3-compatible servers send 400 or 403).
func parseError(e auth.ParseError, v auth.Vocabulary) *awserr.Error {
	switch e := e.(type) {
	case auth.Invalid:
		switch {
		case e.Problem == auth.MissingPayloadHash:
			// The text is auth's, REC from S3-compatible servers,
			// UNVERIFIED against AWS.
			return s3api.ErrInvalidRequest(e.Message)
		case e.Mechanism == auth.QueryV4:
			return s3api.ErrAuthorizationQueryParametersError(e.Message)
		case e.Mechanism == auth.HeaderV4:
			return s3api.ErrAuthorizationHeaderMalformed(e.Message)
		case e.Mechanism == auth.HeaderV2 && e.Problem == auth.InvalidRequestTime:
			return s3api.ErrAccessDenied(e.Message)
		}
	case auth.MissingQueryParameters:
		err := e.AWSError(v)
		err.ID = s3api.IdentityAuthorizationQueryParametersError()
		return err
	case auth.UnknownRegion:
		// The edge cannot know the bucket's home region, so S3's expected
		// region is the default one, as the recorded aws-global answer
		// names (s3.md 3.2). A presigned URL has no Authorization header:
		// S3 answers it AuthorizationQueryParametersError naming
		// X-Amz-Credential, a text widely quoted from AWS answers but
		// UNVERIFIED by a recording here.
		expected := aws.DefaultRegion().String()
		if e.Mechanism == auth.QueryV4 {
			return withField(s3api.ErrAuthorizationQueryParametersError("Error parsing the X-Amz-Credential parameter; the region '"+e.Region+"' is wrong; expecting '"+expected+"'"), "Region", expected)
		}
		return withField(s3api.ErrAuthorizationHeaderMalformed("The authorization header is malformed; the region '"+e.Region+"' is wrong; expecting '"+expected+"'"), "Region", expected)
	case auth.Conflict:
		return authArgument(conflictMessage)
	case auth.UnknownScheme:
		return authArgument(unknownSchemeMessage)
	}
	return e.AWSError(v)
}

// authArgument is InvalidArgument naming the Authorization argument, as
// s3rver answers. s3rver also echoes the header in ArgumentValue; the
// parse failure does not carry it, and it holds the caller's signature, so
// it is left out.
func authArgument(message string) *awserr.Error {
	return withField(s3api.ErrInvalidArgument(message), "ArgumentName", "Authorization")
}
