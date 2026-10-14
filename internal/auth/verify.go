package auth

import (
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// skewWindow is how far a request time may stray from the server's wall
// clock (lambda-runtime-contract.md 3 row 9).
const skewWindow = 15 * time.Minute

// Vocabulary is the error identities and texts a protocol family uses for
// each auth failure (error-matrix.md 3). VocabularyFor gives the JSON and
// Query families; S3 builds its own from its overlay's identities.
type Vocabulary struct {
	// Missing: the request carries no credentials, or an Authorization
	// header in no AWS scheme.
	Missing awserr.Identity
	// Incomplete: the credentials cannot be read.
	Incomplete awserr.Identity
	// Mismatch: the signature does not verify, the credential scope is
	// wrong, or the request carries more than one mechanism.
	Mismatch awserr.Identity
	// Skewed: the request time is outside the 15-minute window.
	Skewed awserr.Identity
	// Expired: a presigned URL is past its expiry.
	Expired awserr.Identity
	// HeadersNotSigned: a presigned URL's request carries x-amz-* headers
	// it did not sign. S3 answers AccessDenied.
	HeadersNotSigned awserr.Identity
	// Conflicting: a presigned URL's request carries a signed header and a
	// query parameter of the same name with different values, and its
	// signature verifies. S3 answers a request whose signature does not
	// verify with a mismatch (REC
	// test_s3_put_presigned_url_same_header_and_qs_parameter); its
	// InvalidRequest for one that does, documented in
	// sigv4-checksums-endpoints.md 2.1, is UNVERIFIED.
	Conflicting awserr.Identity
	// UnknownKey: the access key names no credentials the emulator holds a
	// secret for (ADR 0012 pre-tag review amendment).
	UnknownKey awserr.Identity
	// InvalidToken: temporary credentials sent without their session token,
	// or with another one.
	InvalidToken awserr.Identity
	// ExpiredToken: temporary credentials past their expiry.
	ExpiredToken awserr.Identity
	// PayloadMismatch: the body does not hash to the x-amz-content-sha256
	// the request signed. The Coral services use the mismatch identity; S3
	// answers XAmzContentSHA256Mismatch.
	PayloadMismatch awserr.Identity
	// Dialect picks the texts and extra elements of the failures whose
	// wording differs between the Coral services and S3.
	Dialect Dialect
}

// Dialect is the wording a family uses for the failures whose text differs
// between services.
type Dialect uint8

// Dialect values. The zero value is CoralDialect.
const (
	// CoralDialect is the JSON and Query families' wording, as DynamoDB
	// answers (error-matrix.md 5.5, T8).
	CoralDialect Dialect = iota
	// S3Dialect is S3's wording, with the extra elements S3 sends: the
	// signing-debug elements on a mismatch, the times on an expired or
	// skewed request (s3.md 12, error-matrix.md 5.4 T8). The
	// missing-parameter text is recorded.
	S3Dialect
)

// VocabularyFor returns family f's auth identities, and false for a value
// that is not a family. A skewed request is InvalidSignatureException in the
// JSON family (error-matrix.md 5.5, DynamoDB T8); no recording shows a
// distinct identity for an expired presigned URL, an unsigned x-amz-*
// header, a header conflicting with the query, or a skewed request in the
// Query family, so they use the mismatch identity too, whose message says
// which it is. An unknown key and a wrong session token share one identity
// per family, as Coral answers both "invalid" (dynalite index.js
// L296-L372).
func VocabularyFor(f awserr.Family) (Vocabulary, bool) {
	switch f {
	case awserr.JSONFamily:
		mismatch, unknown := awserr.InvalidSignatureException(), awserr.UnrecognizedClientException()
		return Vocabulary{
			Missing:    awserr.MissingAuthenticationTokenException(),
			Incomplete: awserr.IncompleteSignatureException(),
			Mismatch:   mismatch, Skewed: mismatch, Expired: mismatch, HeadersNotSigned: mismatch, Conflicting: mismatch, PayloadMismatch: mismatch,
			UnknownKey: unknown, InvalidToken: unknown, ExpiredToken: awserr.ExpiredTokenException(),
			Dialect: CoralDialect,
		}, true
	case awserr.QueryFamily:
		mismatch, unknown := awserr.SignatureDoesNotMatch(), awserr.InvalidClientTokenId()
		return Vocabulary{
			Missing:    awserr.MissingAuthenticationToken(),
			Incomplete: awserr.IncompleteSignature(),
			Mismatch:   mismatch, Skewed: mismatch, Expired: mismatch, HeadersNotSigned: mismatch, Conflicting: mismatch, PayloadMismatch: mismatch,
			UnknownKey: unknown, InvalidToken: unknown, ExpiredToken: awserr.ExpiredToken(),
			Dialect: CoralDialect,
		}, true
	}
	return Vocabulary{}, false
}

// WithStatus is v with every identity answering status, for a front end
// that refuses all credential failures with one status, as Lambda's
// function URLs answer 403.
func (v Vocabulary) WithStatus(status int) Vocabulary {
	for _, id := range []*awserr.Identity{
		&v.Missing, &v.Incomplete, &v.Mismatch, &v.Skewed, &v.Expired, &v.HeadersNotSigned,
		&v.Conflicting, &v.UnknownKey, &v.InvalidToken, &v.ExpiredToken, &v.PayloadMismatch,
	} {
		id.Status = status
	}
	return v
}

// The Coral and S3 texts of the credential failures. Coral's are DynamoDB's
// (dynalite index.js L296-L372) and AWS's documented expired-token text;
// S3's are its error list's (ErrorResponses.html).
const (
	coralInvalidTokenMessage = "The security token included in the request is invalid."
	coralExpiredTokenMessage = "The security token included in the request is expired"
	s3UnknownKeyMessage      = "The AWS Access Key Id you provided does not exist in our records."
	s3InvalidTokenMessage    = "The provided token is malformed or otherwise invalid."
	s3ExpiredTokenMessage    = "The provided token has expired."
)

// UnknownKey is the answer to a request signed with an access key the
// emulator holds no secret for. S3 names the key in its AWSAccessKeyId
// element.
func UnknownKey(key string, v Vocabulary) *awserr.Error {
	if v.Dialect != S3Dialect {
		return failure(v.UnknownKey, coralInvalidTokenMessage)
	}
	err := failure(v.UnknownKey, s3UnknownKeyMessage)
	err.Fields = []awserr.Field{{Name: "AWSAccessKeyId", Value: key}}
	return err
}

// InvalidToken is the answer to temporary credentials sent without their
// session token or with another one. The token is never echoed: it is a
// credential.
func InvalidToken(v Vocabulary) *awserr.Error {
	if v.Dialect == S3Dialect {
		return failure(v.InvalidToken, s3InvalidTokenMessage)
	}
	return failure(v.InvalidToken, coralInvalidTokenMessage)
}

// ExpiredToken is the answer to temporary credentials past their expiry.
func ExpiredToken(v Vocabulary) *awserr.Error {
	if v.Dialect == S3Dialect {
		return failure(v.ExpiredToken, s3ExpiredTokenMessage)
	}
	return failure(v.ExpiredToken, coralExpiredTokenMessage)
}

const (
	missingTokenMessage = "Request is missing Authentication Token"
	s3MismatchMessage   = "The request signature we calculated does not match the signature you provided. Check your key and signing method."
)

// AWSError renders a Conflict as a signature mismatch, in Coral's words. The
// text for an Authorization header with X-Amz-Algorithm is DynamoDB's
// (error-matrix.md 5.5, T8). Coral detects no other pair, so the texts for
// the rest are understudy's own in the same form, naming the carriers found.
// S3 answers every pair with InvalidArgument, which no Vocabulary holds, so
// it switches on the variant instead, as for UnknownRegion.
func (e Conflict) AWSError(v Vocabulary) *awserr.Error {
	msg := "Found both '" + e.First.name() + "' and '" + e.Second.name() + "' as query-string params."
	if e.First == AuthorizationHeader {
		msg = "Found both '" + e.Second.name() + "' as a query-string param and '" + hdrAuthorization + "' as HTTP header."
	}
	return failure(v.Mismatch, msg)
}

// AWSError renders an UnknownScheme as missing credentials, in Coral's
// words: the Coral services ignore an Authorization header that does not
// start with AWS4-. S3 answers InvalidArgument, which no Vocabulary holds,
// so it switches on the variant instead.
func (UnknownScheme) AWSError(v Vocabulary) *awserr.Error {
	return failure(v.Missing, missingTokenMessage)
}

// AWSError renders MissingQueryParameters as an incomplete signature.
func (e MissingQueryParameters) AWSError(v Vocabulary) *awserr.Error {
	if v.Dialect == S3Dialect {
		return failure(v.Incomplete, "Query-string authentication version 4 requires the X-Amz-Algorithm, X-Amz-Credential, X-Amz-Signature, X-Amz-Date, X-Amz-SignedHeaders, and X-Amz-Expires parameters.")
	}
	var b strings.Builder
	for _, name := range e.Names {
		b.WriteString("AWS query-string parameters must include '" + name + "'. ")
	}
	b.WriteString("Re-examine the query-string parameters.")
	return failure(v.Incomplete, b.String())
}

// AWSError renders an Invalid credential: a wrong credential scope or an
// unsigned host is a signature mismatch, every other problem an incomplete
// signature (error-matrix.md 5.5, DynamoDB T8).
func (e Invalid) AWSError(v Vocabulary) *awserr.Error {
	id := v.Incomplete
	switch e.Problem {
	case InvalidTerminator, InvalidScopeDate, HostNotSigned:
		id = v.Mismatch
	case Malformed, UnsupportedAlgorithm, InvalidPayloadHash, InvalidRequestTime, MissingPayloadHash:
	}
	return failure(id, e.Message)
}

// AWSError renders an UnknownRegion as a signature mismatch, as Coral
// answers a wrong credential scope. S3 answers AuthorizationHeaderMalformed
// with the region it expected, which only it knows, so it switches on the
// variant and reads Region instead.
func (e UnknownRegion) AWSError(v Vocabulary) *awserr.Error {
	return failure(v.Mismatch, e.Message)
}

// Missing is the error for a request without credentials to a service that
// requires them.
func Missing(v Vocabulary) *awserr.Error {
	return failure(v.Missing, missingTokenMessage)
}

// Verify checks a SigV4 Authorization header against secret. It is a pure
// function: the request time must lie within 15 minutes of wallNow (wall
// time, because the client signed with its own clock), the scope date must
// be the request date, and the signature must match the canonical request
// over exactly the client's signed headers. The checks run in that order,
// as Coral's do.
func Verify(a SigV4Header, req Normalized, secret string, wallNow time.Time, v Vocabulary) *awserr.Error {
	if err := checkSkew(a.Time, iso(a.Time), wallNow, v); err != nil {
		return err
	}
	if err := checkScopeDate(a.Credential, a.Time, v); err != nil {
		return err
	}
	payload, err := payloadLine(a.PayloadHash, req.Body, v)
	if err != nil {
		return err
	}
	return checkSignature(a.Credential, a.Time, req, a.SignedHeaders, "", payload, a.Signature, secret, v)
}

// VerifyPresignedSince checks a SigV4 presigned URL against secret. Expiry
// is judged on the emulator clock: the URL expires once emuNow reaches
// signed, the emulator instant it was signed at, plus X-Amz-Expires, so
// advancing the clock expires it and a URL signed after an advance lives
// its full lifetime. A request time more than 15 minutes ahead of wallNow
// is not yet valid (lambda-runtime-contract.md 3 row 10). Every x-amz-*
// header the request carries must be signed, and no signed header may
// differ from a query parameter of the same name
// (sigv4-checksums-endpoints.md 2.1). The conflict is judged only once the
// signature verifies, as S3 reports a mismatch first (REC
// test_s3_put_presigned_url_same_header_and_qs_parameter).
func VerifyPresignedSince(a SigV4Query, req Normalized, secret string, wallNow time.Time, signed, emuNow clock.Instant, v Vocabulary) *awserr.Error {
	if a.Time.After(wallNow.Add(skewWindow)) {
		return notYetCurrent(a.Time, iso(a.Time), wallNow, v)
	}
	if expiry := signed.Add(a.Expires); !emuNow.Before(expiry) {
		lifetime := awserr.Field{Name: qExpires, Value: strconv.FormatInt(int64(a.Expires/time.Second), 10)}
		return expired(expiry.Wire(), emuNow, v, lifetime)
	}
	if err := checkAllAmzHeadersSigned(req, a.SignedHeaders, v); err != nil {
		return err
	}
	if err := checkScopeDate(a.Credential, a.Time, v); err != nil {
		return err
	}
	payload, err := payloadLine(a.PayloadHash, req.Body, v)
	if err != nil {
		return err
	}
	if err := checkSignature(a.Credential, a.Time, req, a.SignedHeaders, qSignature, payload, a.Signature, secret, v); err != nil {
		return err
	}
	return checkHeadersAgreeWithQuery(req, a.SignedHeaders, v)
}

func checkScopeDate(c Credential, t time.Time, v Vocabulary) *awserr.Error {
	if c.Date != t.UTC().Format(scopeDate) {
		return failure(v.Mismatch, scopeDateMismatch(c.Date, t))
	}
	return nil
}

// checkSkew refuses a request time t more than 15 minutes from wallNow
// either way. sent is t as the request carried it, which S3 echoes.
func checkSkew(t time.Time, sent string, wallNow time.Time, v Vocabulary) *awserr.Error {
	if earliest := wallNow.Add(-skewWindow); t.Before(earliest) {
		if v.Dialect == S3Dialect {
			return tooSkewed(sent, wallNow, v)
		}
		return failure(v.Skewed, "Signature expired: "+iso(t)+" is now earlier than "+iso(earliest)+" ("+iso(wallNow)+" - 15 min.)")
	}
	if t.After(wallNow.Add(skewWindow)) {
		return notYetCurrent(t, sent, wallNow, v)
	}
	return nil
}

// notYetCurrent is the answer to a request time ahead of the window. S3's
// answer for a presigned URL dated in the future is UNVERIFIED; it gets
// S3's skew answer.
func notYetCurrent(t time.Time, sent string, wallNow time.Time, v Vocabulary) *awserr.Error {
	if v.Dialect == S3Dialect {
		return tooSkewed(sent, wallNow, v)
	}
	return failure(v.Skewed, "Signature not yet current: "+iso(t)+" is still later than "+iso(wallNow.Add(skewWindow))+" ("+iso(wallNow)+" + 15 min.)")
}

// tooSkewed is S3's RequestTimeTooSkewed answer. The text and elements are
// those of real S3 responses (RequestTime echoes the request's time, ServerTime
// is ISO 8601); S3's error list words the message "the server's time"
// (sigv4-checksums-endpoints.md 1.5), but responses say "the current time".
// SigV2 passes its stamp as sent; SigV4 passes the ISO 8601 basic form of the
// parsed time, which is the stamp as sent unless a header request was dated
// by Date rather than X-Amz-Date, which the SDKs never do.
func tooSkewed(sent string, wallNow time.Time, v Vocabulary) *awserr.Error {
	err := failure(v.Skewed, "The difference between the request time and the current time is too large.")
	err.Fields = []awserr.Field{
		{Name: "RequestTime", Value: sent},
		{Name: "ServerTime", Value: s3Time(wallNow)},
		{Name: "MaxAllowedSkewMilliseconds", Value: strconv.FormatInt(skewWindow.Milliseconds(), 10)},
	}
	return err
}

// expired is the answer to a presigned URL past expiry, judged at emuNow.
// S3 adds the URL's lifetime when it has one (SigV4's X-Amz-Expires), the
// expiry and the server time (s3.md 12, REC test_s3_presigned_url_expired).
func expired(expiry time.Time, emuNow clock.Instant, v Vocabulary, lifetime ...awserr.Field) *awserr.Error {
	err := failure(v.Expired, "Request has expired")
	if v.Dialect == S3Dialect {
		err.Fields = append(slices.Clip(lifetime),
			awserr.Field{Name: "Expires", Value: s3Time(expiry)},
			awserr.Field{Name: "ServerTime", Value: s3Time(emuNow.Wire())},
		)
	}
	return err
}

// checkAllAmzHeadersSigned refuses a presigned request carrying x-amz-*
// headers it did not sign, naming them as S3 does: sorted, joined with ", "
// (error-matrix.md 5.4 T8). S3 names them in its HeadersNotSigned element;
// no recording shows a Coral answer, so the Coral text, which names them in
// the message since Coral has no such member, is understudy's own
// (UNVERIFIED). No x-amz-* header is exempt; no recording shows S3 exempting
// one.
func checkAllAmzHeadersSigned(req Normalized, signed []string, v Vocabulary) *awserr.Error {
	var unsigned []string
	for name := range req.Header {
		if lower := strings.ToLower(name); strings.HasPrefix(lower, "x-amz-") && !slices.Contains(signed, lower) {
			unsigned = append(unsigned, lower)
		}
	}
	if unsigned == nil {
		return nil
	}
	slices.Sort(unsigned)
	const msg = "There were headers present in the request which were not signed"
	names := strings.Join(unsigned, ", ")
	if v.Dialect != S3Dialect {
		return failure(v.HeadersNotSigned, msg+": "+names)
	}
	err := failure(v.HeadersNotSigned, msg)
	err.Fields = []awserr.Field{{Name: "HeadersNotSigned", Value: names}}
	return err
}

// checkHeadersAgreeWithQuery refuses a presigned request that carries a
// signed header and a signed query parameter of the same name, in any case,
// with different values: "the input is conflicting"
// (sigv4-checksums-endpoints.md 2.1). Values are compared as the canonical
// request holds them: the header's as signed, the parameter's decoded, both
// with spaces trimmed and collapsed. A signed header the request does not
// carry conflicts with nothing. The text is understudy's own (UNVERIFIED),
// as no recording shows S3's.
func checkHeadersAgreeWithQuery(req Normalized, signed []string, v Vocabulary) *awserr.Error {
	for part := range strings.SplitSeq(req.RawQuery, "&") {
		k, value, _ := strings.Cut(part, "=")
		k = unescape(k)
		name := strings.ToLower(k)
		if k == qSignature || !slices.Contains(signed, name) || (name != "host" && req.Header.Values(name) == nil) {
			continue
		}
		if header := canonicalHeaderValue(req, name); header != collapseSpaces(unescape(value)) {
			return failure(v.Conflicting, "The signed header '"+name+"' and the query parameter '"+k+"' differ in value; the input is conflicting.")
		}
	}
	return nil
}

// payloadLine is the canonical request's last line: the signed
// x-amz-content-sha256 value, else the hash of the body.
func payloadLine(h PayloadHash, body Body, v Vocabulary) (string, *awserr.Error) {
	hashed, isHashed := body.(Hashed)
	if sent, ok := h.(Hex); ok && isHashed && sent.SHA256 != hashed.SHA256 {
		return "", failure(v.PayloadMismatch, "The provided 'x-amz-content-sha256' header does not match what was computed.")
	}
	if line, ok := PayloadHashValue(h); ok {
		return line, nil
	}
	if !isHashed {
		// Parse refuses an S3 header request without the header
		// (MissingPayloadHash), so only a caller that passed Unread for
		// another service reaches this.
		return "", awserr.Internalf(errors.New("auth: the request sent no x-amz-content-sha256 and the caller did not hash the body; pass Hashed to Normalize for every service but S3"))
	}
	return hex.EncodeToString(hashed.SHA256[:]), nil
}

// checkSignature compares got with the signature of req's canonical request.
// On a mismatch the answer shows the canonical request as AWS does, the
// Coral services in the message and S3 in its signing-debug elements, but
// with the session token redacted: the answer reaches logs and the journal,
// and a minted session token is a credential.
func checkSignature(c Credential, t time.Time, req Normalized, signed []string, omit, payload, got, secret string, v Vocabulary) *awserr.Error {
	sts := stringToSign(iso(t), c.scope(), canonicalRequest(req, c.Service, signed, omit, payload))
	want := hex.EncodeToString(hmacSHA256(signingKey(secret, c), sts))
	if hmac.Equal([]byte(got), []byte(want)) {
		return nil
	}
	shown := canonicalRequest(req.withoutSessionToken(), c.Service, signed, omit, payload)
	if v.Dialect == S3Dialect {
		err := s3Mismatch(c.Key, sts, got, v)
		err.Fields = append(err.Fields,
			awserr.Field{Name: "CanonicalRequest", Value: shown},
			awserr.Field{Name: "CanonicalRequestBytes", Value: spacedHex(shown)},
		)
		return err
	}
	return failure(v.Mismatch, "The request signature we calculated does not match the signature you provided. "+
		"Check your AWS Secret Access Key and signing method. Consult the service documentation for details.\n\n"+
		"The Canonical String for this request should have been\n'"+shown+"'\n\n"+
		"The String-to-Sign should have been\n'"+sts+"'\n")
}

// s3Mismatch is S3's SignatureDoesNotMatch with the elements SigV2 and SigV4
// share, in the order S3 sends them; SigV4 appends the canonical request
// (s3.md 12, error-matrix.md 5.4 T8).
func s3Mismatch(key, sts, got string, v Vocabulary) *awserr.Error {
	err := failure(v.Mismatch, s3MismatchMessage)
	err.Fields = []awserr.Field{
		{Name: "AWSAccessKeyId", Value: key},
		{Name: "StringToSign", Value: sts},
		{Name: "SignatureProvided", Value: got},
		{Name: "StringToSignBytes", Value: spacedHex(sts)},
	}
	return err
}

// spacedHex is s as S3's *Bytes elements show it: each byte as two
// lowercase hex digits, separated by spaces.
func spacedHex(s string) string { return fmt.Sprintf("% x", s) }

func failure(id awserr.Identity, msg string) *awserr.Error {
	return &awserr.Error{ID: id, Message: msg, Origin: awserr.Generic{}}
}

func iso(t time.Time) string { return t.UTC().Format(isoBasic) }

// s3Time is the form of S3's ServerTime and Expires elements:
// 2025-06-03T12:26:02Z.
func s3Time(t time.Time) string { return t.UTC().Format(time.RFC3339) }
