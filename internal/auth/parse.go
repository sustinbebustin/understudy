package auth

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

const (
	algorithm      = "AWS4-HMAC-SHA256"
	algorithmV4A   = "AWS4-ECDSA-P256-SHA256"
	v4SchemePrefix = "AWS4-"
	v2Scheme       = "AWS"
	terminator     = "aws4_request"
	isoBasic       = "20060102T150405Z"
	scopeDate      = "20060102"
	maxPresignLife = 7 * 24 * time.Hour

	hdrAuthorization = "Authorization"
	hdrDate          = "Date"
	hdrAmzDate       = "X-Amz-Date"
	hdrContentSHA256 = "X-Amz-Content-Sha256"
	hdrSecurityToken = "X-Amz-Security-Token" //nolint:gosec // a header name, not a credential.

	qAlgorithm     = "X-Amz-Algorithm"
	qKeyAndScope   = "X-Amz-Credential"
	qDate          = "X-Amz-Date"
	qExpires       = "X-Amz-Expires"
	qSignedHeaders = "X-Amz-SignedHeaders"
	qSignature     = "X-Amz-Signature"
	qContentSHA256 = "X-Amz-Content-Sha256"
	qSecurityToken = "X-Amz-Security-Token" //nolint:gosec // a query parameter name, not a credential.
	qV2Key         = "AWSAccessKeyId"
	qV2Signature   = "Signature"
	qV2Expires     = "Expires"
)

// Mechanism is where a request carries its credentials.
type Mechanism uint8

// Mechanism values.
const (
	HeaderV4 Mechanism = iota + 1 // Authorization: AWS4-HMAC-SHA256
	QueryV4                       // X-Amz-Algorithm and friends
	HeaderV2                      // Authorization: AWS key:signature
	QueryV2                       // AWSAccessKeyId, Signature, Expires
)

// Problem says why Parse refused the credentials of one mechanism.
type Problem uint8

// Problem values. Each value's comment names the Vocabulary identity
// AWSError renders it with, after the Coral services' answers (error-matrix.md 5.5,
// DynamoDB T8).
const (
	// Malformed: a required element is missing or unreadable. Incomplete.
	Malformed Problem = iota + 1
	// UnsupportedAlgorithm: an AWS4- scheme or X-Amz-Algorithm other than
	// AWS4-HMAC-SHA256, such as SigV4A. Incomplete.
	UnsupportedAlgorithm
	// InvalidTerminator: the credential scope does not end in aws4_request.
	// Mismatch.
	InvalidTerminator
	// HostNotSigned: host is not among the signed headers. Mismatch.
	HostNotSigned
	// InvalidPayloadHash: x-amz-content-sha256 is neither a known literal nor
	// 64 lowercase hex digits. Incomplete.
	InvalidPayloadHash
	// InvalidScopeDate: the credential-scope date is not yyyyMMdd. Mismatch,
	// with the text Coral gives a scope date that differs from the request
	// date (dynalite index.js L296-L372).
	InvalidScopeDate
	// InvalidRequestTime: a SigV2 Authorization header whose request has no
	// readable Date or X-Amz-Date. Incomplete; S3, the only service SigV2
	// reaches, answers AccessDenied, which no Vocabulary holds, so it
	// switches on the Problem.
	InvalidRequestTime
	// MissingPayloadHash: an S3 request signed in the Authorization header
	// without x-amz-content-sha256, which S3 requires on every SigV4 request.
	// Incomplete; S3 answers InvalidRequest, which no Vocabulary holds, so it
	// switches on the Problem. The identity and the text "Missing required
	// header for this request: x-amz-content-sha256" are those S3-compatible
	// servers give (REC, UNVERIFIED against AWS): versitygw
	// (https://github.com/versity/versitygw/blob/e56df2384d20605288041ee8d973d7f6b3d6a5c0/s3err/s3err.go#L640-L644)
	// and Swift's s3api
	// (https://github.com/openstack/swift/blob/861f18c8c906c7d302fa88cf851e87e10dd30ecb/swift/common/middleware/s3api/s3request.py#L1334-L1340).
	MissingPayloadHash
)

// Carrier is a place in a request that can carry credentials, as a Conflict
// names it.
type Carrier uint8

// Carrier values, in the order Parse looks for them.
const (
	AuthorizationHeader Carrier = iota + 1 // an Authorization header, in any scheme
	AlgorithmQuery                         // X-Amz-Algorithm in the query string
	CredentialQuery                        // X-Amz-Credential in the query string, without X-Amz-Algorithm
	V2KeyQuery                             // AWSAccessKeyId in the query string
)

// name is the header or query parameter the carrier is.
func (c Carrier) name() string {
	switch c {
	case AuthorizationHeader:
		return hdrAuthorization
	case AlgorithmQuery:
		return qAlgorithm
	case CredentialQuery:
		return qKeyAndScope
	case V2KeyQuery:
		return qV2Key
	}
	return fmt.Sprintf("carrier %d", uint8(c))
}

// ParseError is a request whose credentials cannot be read: Conflict,
// UnknownScheme, MissingQueryParameters, UnknownRegion or Invalid. Parse
// runs before the request is claimed, when the protocol family, and with it
// the error identity and text, is not yet known: the caller renders the
// error with AWSError once it is, or switches on the variants for
// identities a Vocabulary does not hold (S3). It does not implement error,
// so it cannot be mistaken for a Go failure.
//
//sumtype:decl
type ParseError interface {
	isParseError()
	// AWSError renders the error with family v's identity and texts.
	AWSError(v Vocabulary) *awserr.Error
}

// Conflict is a request that carries credentials in more than one place,
// such as an Authorization header and X-Amz-Algorithm. First and Second are
// the first two carriers present, in Carrier order.
type Conflict struct{ First, Second Carrier }

// UnknownScheme is an Authorization header in no AWS scheme, such as Bearer
// or Basic.
type UnknownScheme struct{}

// MissingQueryParameters is a presigned URL without some of the SigV4 query
// parameters. Names lists them in the order the Coral services report them,
// with X-Amz-Expires last.
type MissingQueryParameters struct{ Names []string }

// Invalid is a credential of a known mechanism that cannot be used.
type Invalid struct {
	Mechanism Mechanism
	Problem   Problem
	// Message is the AWS-style text, the same in every family.
	Message string
}

// UnknownRegion is a SigV4 credential scope that names a region the region
// table does not hold, such as aws-global. It is its own variant because
// S3 answers it with the region name (AuthorizationHeaderMalformed, s3.md
// 3.2).
type UnknownRegion struct {
	Mechanism Mechanism
	// Region is the scope's region as sent.
	Region string
	// Service is the scope's signing name as sent, so the front door can
	// still route the request to the service that answers it (S3 for
	// aws-global, s3.md 3.2).
	Service string
	// Message is the AWS-style text, the same in every family. Like
	// Coral's, it also reports a wrong terminator or date in the same scope.
	Message string
}

func (Conflict) isParseError()               {}
func (UnknownScheme) isParseError()          {}
func (MissingQueryParameters) isParseError() {}
func (UnknownRegion) isParseError()          {}
func (Invalid) isParseError()                {}

// Parse reads a request's credentials (sigv4-checksums-endpoints.md 1.1,
// 2.1, 2.3) and validates the credential-scope region against regions. It
// never verifies a signature. A request without credentials is Anonymous.
//
// Within a mechanism the checks run in the order the Coral services report
// faults (dynalite index.js L296-L372): missing elements, the date format,
// host among the signed headers, the algorithm, the credential's shape, then
// its pieces. The skew check Coral runs after the date format needs the wall
// clock and is Verify's.
func Parse(r *http.Request, regions aws.Regions) (Auth, ParseError) {
	authz := strings.TrimSpace(r.Header.Get(hdrAuthorization))
	q := r.URL.Query()
	if c, ok := conflict(authz != "", q); ok {
		return nil, c
	}
	scheme, params, _ := strings.Cut(authz, " ")
	switch {
	case q.Has(qAlgorithm) || q.Has(qKeyAndScope):
		return parseV4Query(r, q, regions)
	case q.Has(qV2Key):
		return parseV2Query(q)
	case authz == "":
		return Anonymous{}, nil
	case strings.HasPrefix(scheme, v4SchemePrefix):
		return parseV4Header(r, scheme, authz, params, regions)
	case scheme == v2Scheme:
		return parseV2Header(r, params)
	}
	return nil, UnknownScheme{}
}

// conflict reports the first two carriers present when there are two or
// more.
func conflict(header bool, q url.Values) (Conflict, bool) {
	var found []Carrier
	if header {
		found = append(found, AuthorizationHeader)
	}
	switch {
	case q.Has(qAlgorithm):
		found = append(found, AlgorithmQuery)
	case q.Has(qKeyAndScope):
		found = append(found, CredentialQuery)
	}
	if q.Has(qV2Key) {
		found = append(found, V2KeyQuery)
	}
	if len(found) < 2 {
		return Conflict{}, false
	}
	return Conflict{First: found[0], Second: found[1]}, true
}

// checkAlgorithm refuses every AWS4- algorithm but AWS4-HMAC-SHA256.
func checkAlgorithm(alg string, m Mechanism) ParseError {
	switch {
	case alg == algorithm:
		return nil
	case alg == algorithmV4A:
		return Invalid{Mechanism: m, Problem: UnsupportedAlgorithm, Message: "SigV4A (" + algorithmV4A + ") is not supported; sign with " + algorithm}
	case m == QueryV4:
		// S3's text (testdata/parity/s3/R18/01-presign-unknown-algorithm.json).
		return Invalid{Mechanism: m, Problem: UnsupportedAlgorithm, Message: "X-Amz-Algorithm only supports \"" + algorithm + " and " + algorithmV4A + "\""}
	}
	return Invalid{Mechanism: m, Problem: UnsupportedAlgorithm, Message: "Unsupported AWS 'algorithm': '" + alg + "' (only " + algorithm + " for now). "}
}

func parseV4Header(r *http.Request, scheme, authz, params string, regions aws.Regions) (Auth, ParseError) {
	fields := map[string]string{}
	for part := range strings.SplitSeq(params, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		fields[k] = v
	}
	var missing []string
	for _, k := range []string{"Credential", "Signature", "SignedHeaders"} {
		if fields[k] == "" {
			missing = append(missing, "Authorization header requires '"+k+"' parameter.")
		}
	}
	amzDate, date := r.Header.Get(hdrAmzDate), r.Header.Get(hdrDate)
	if amzDate == "" && date == "" {
		missing = append(missing, "Authorization header requires existence of either a 'X-Amz-Date' or a 'Date' header.")
	}
	if missing != nil {
		return nil, malformed(HeaderV4, strings.Join(missing, " ")+" Authorization="+authz)
	}

	t, perr := parseRequestTime(amzDate, date)
	if perr != nil {
		return nil, perr
	}
	signed, perr := parseSignedHeaders(fields["SignedHeaders"], HeaderV4)
	if perr != nil {
		return nil, perr
	}
	if perr := checkAlgorithm(scheme, HeaderV4); perr != nil {
		return nil, perr
	}
	cred, perr := parseCredential(fields["Credential"], t, regions, HeaderV4)
	if perr != nil {
		return nil, perr
	}
	hash := r.Header.Get(hdrContentSHA256)
	if hash == "" && cred.Service == serviceS3 {
		return nil, Invalid{Mechanism: HeaderV4, Problem: MissingPayloadHash, Message: "Missing required header for this request: x-amz-content-sha256"}
	}
	payload, perr := parsePayloadHash(hash, HeaderV4)
	if perr != nil {
		return nil, perr
	}
	return SigV4Header{Credential: cred, SignedHeaders: signed, Signature: fields["Signature"], PayloadHash: payload, Time: t}, nil
}

func parseV4Query(r *http.Request, q url.Values, regions aws.Regions) (Auth, ParseError) {
	var missing []string
	for _, k := range []string{qAlgorithm, qKeyAndScope, qSignature, qSignedHeaders, qDate, qExpires} {
		if q.Get(k) == "" {
			missing = append(missing, k)
		}
	}
	if missing != nil {
		return nil, MissingQueryParameters{Names: missing}
	}
	t, err := time.Parse(isoBasic, q.Get(qDate))
	if err != nil {
		return nil, malformed(QueryV4, "X-Amz-Date must be in the ISO8601 Long Format \"yyyyMMdd'T'HHmmss'Z'\"")
	}
	signed, perr := parseSignedHeaders(q.Get(qSignedHeaders), QueryV4)
	if perr != nil {
		return nil, perr
	}
	if perr := checkAlgorithm(q.Get(qAlgorithm), QueryV4); perr != nil {
		return nil, perr
	}
	cred, perr := parseCredential(q.Get(qKeyAndScope), t, regions, QueryV4)
	if perr != nil {
		return nil, perr
	}
	expires, perr := parseExpires(q.Get(qExpires))
	if perr != nil {
		return nil, perr
	}
	// A JS presign hoists x-amz-content-sha256 into the query; Go and the
	// CLI sign UNSIGNED-PAYLOAD for S3 without sending it; other services
	// sign the body hash (sigv4-checksums-endpoints.md 2.2).
	hash := q.Get(qContentSHA256)
	if hash == "" {
		hash = r.Header.Get(hdrContentSHA256)
	}
	if hash == "" && cred.Service == serviceS3 {
		hash = unsignedPayload
	}
	payload, perr := parsePayloadHash(hash, QueryV4)
	if perr != nil {
		return nil, perr
	}
	return SigV4Query{
		Credential: cred, SignedHeaders: signed, Signature: q.Get(qSignature),
		PayloadHash: payload, Time: t, Expires: expires,
	}, nil
}

// parseV2Header reads key:signature and the request time, from X-Amz-Date
// when present, else Date. The text for a missing or unreadable time is S3's
// as the S3-compatible servers copy it (minio, lakeFS and versitygw carry it
// under AccessDenied).
func parseV2Header(r *http.Request, params string) (Auth, ParseError) {
	key, sig, ok := strings.Cut(params, ":")
	if !ok || key == "" || sig == "" {
		return nil, malformed(HeaderV2, "AWS authorization header is invalid.  Expected AwsAccessKeyId:signature")
	}
	stamp := r.Header.Get(hdrAmzDate)
	if stamp == "" {
		stamp = r.Header.Get(hdrDate)
	}
	t, ok := parseHTTPDate(stamp)
	if !ok {
		return nil, Invalid{Mechanism: HeaderV2, Problem: InvalidRequestTime, Message: "AWS authentication requires a valid Date or x-amz-date header"}
	}
	return SigV2Header{Key: key, Signature: sig, Time: t, Stamp: stamp}, nil
}

func parseV2Query(q url.Values) (Auth, ParseError) {
	key, sig, exp := q.Get(qV2Key), q.Get(qV2Signature), q.Get(qV2Expires)
	if key == "" || sig == "" || exp == "" {
		return nil, malformed(QueryV2, "Query-string authentication requires the Signature, Expires and AWSAccessKeyId parameters")
	}
	secs, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return nil, malformed(QueryV2, "Invalid date (should be seconds since epoch): "+exp)
	}
	return SigV2Query{Key: key, Signature: sig, Expires: time.Unix(secs, 0).UTC()}, nil
}

// parseCredential reads keyid/date/region/service/aws4_request. Like Coral,
// it reports every wrong piece of the scope in one message, in the order
// region, terminator, date: as an UnknownRegion when the region is wrong,
// else under the first wrong piece's Problem. Whether a well-formed date is
// the request date t is Verify's question.
func parseCredential(s string, t time.Time, regions aws.Regions, m Mechanism) (Credential, ParseError) {
	parts := strings.Split(s, "/")
	if len(parts) != 5 {
		return Credential{}, malformed(m, "Credential must have exactly 5 slash-delimited elements, e.g. keyid/date/region/service/term, got '"+s+"'")
	}
	key, date, regionName, service, term := parts[0], parts[1], parts[2], parts[3], parts[4]
	var messages []string
	region, regionErr := regions.Parse(regionName)
	if regionErr != nil {
		messages = append(messages, "Credential should be scoped to a valid region, not '"+regionName+"'. Add the name to the regions array in the understudy configuration to use it.")
	}
	// first is the first wrong piece after the region; the zero Problem is
	// none.
	var first Problem
	if term != terminator {
		first = InvalidTerminator
		messages = append(messages, "Credential should be scoped with a valid terminator: '"+terminator+"', not '"+term+"'.")
	}
	if _, err := time.Parse(scopeDate, date); err != nil || len(date) != len(scopeDate) {
		if first == 0 {
			first = InvalidScopeDate
		}
		messages = append(messages, scopeDateMismatch(date, t))
	}
	switch {
	case regionErr != nil:
		return Credential{}, UnknownRegion{Mechanism: m, Region: regionName, Service: service, Message: strings.Join(messages, " ")}
	case first != 0:
		return Credential{}, Invalid{Mechanism: m, Problem: first, Message: strings.Join(messages, " ")}
	}
	if key == "" || service == "" {
		return Credential{}, malformed(m, "Credential must name an access key and a service, got '"+s+"'")
	}
	return Credential{Key: key, Date: date, Region: region, Service: service}, nil
}

// scopeDateMismatch is Coral's text for a credential-scope date that is not
// the day of request time t.
func scopeDateMismatch(date string, t time.Time) string {
	return "Date in Credential scope does not match YYYYMMDD from ISO-8601 version of date from HTTP: '" +
		date + "' != '" + t.UTC().Format(scopeDate) + "', from '" + iso(t) + "'."
}

func parseSignedHeaders(s string, m Mechanism) ([]string, ParseError) {
	names := strings.Split(s, ";")
	hasHost := false
	for _, n := range names {
		if n == "" || n != strings.ToLower(n) {
			return nil, malformed(m, "SignedHeaders must be lowercase header names separated by ';', got '"+s+"'")
		}
		hasHost = hasHost || n == "host"
	}
	if !hasHost {
		return nil, Invalid{Mechanism: m, Problem: HostNotSigned, Message: "'Host' or ':authority' must be a 'SignedHeader' in the AWS Authorization."}
	}
	return names, nil
}

// parseRequestTime reads X-Amz-Date, which wins over Date when both are
// present (sigv4-checksums-endpoints.md 1.1).
func parseRequestTime(amzDate, date string) (time.Time, ParseError) {
	if amzDate != "" {
		t, err := time.Parse(isoBasic, amzDate)
		if err != nil {
			return time.Time{}, malformed(HeaderV4, "Date must be in ISO-8601 'basic format'. Got '"+amzDate+"'. See http://en.wikipedia.org/wiki/ISO_8601")
		}
		return t, nil
	}
	t, ok := parseHTTPDate(date)
	if !ok {
		return time.Time{}, malformed(HeaderV4, "Date header must be an HTTP date such as 'Fri, 24 May 2013 00:00:00 GMT'. Got '"+date+"'.")
	}
	return t, nil
}

// parseHTTPDate reads a Date header in the forms net/http accepts or with a
// numeric zone, "Tue, 27 Mar 2007 19:36:42 +0000", the form of AWS's own
// SigV2 examples.
func parseHTTPDate(s string) (time.Time, bool) {
	if t, err := http.ParseTime(s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC1123Z, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// parseExpires reads X-Amz-Expires, 1 to 604800 seconds
// (sigv4-checksums-endpoints.md 2.1). The bound is checked on the seconds,
// before the conversion to a Duration can overflow.
func parseExpires(s string) (time.Duration, ParseError) {
	const maxSecs = int64(maxPresignLife / time.Second)
	secs, err := strconv.ParseInt(s, 10, 64)
	switch {
	case err != nil:
		return 0, malformed(QueryV4, "X-Amz-Expires should be a number")
	case secs < 0:
		return 0, malformed(QueryV4, "X-Amz-Expires must be non-negative")
	case secs == 0:
		return 0, malformed(QueryV4, "X-Amz-Expires must be at least 1 second; got 0")
	case secs > maxSecs:
		// S3's text (testdata/parity/s3/R18/01-presign-expires-604801.json).
		return 0, malformed(QueryV4, fmt.Sprintf("X-Amz-Expires must be less than a week (in seconds); that is, the given X-Amz-Expires must be less than %d seconds", maxSecs))
	}
	return time.Duration(secs) * time.Second, nil
}

func parsePayloadHash(v string, m Mechanism) (PayloadHash, ParseError) {
	switch v {
	case "":
		return NotSent{}, nil
	case unsignedPayload:
		return Unsigned{}, nil
	case streamingUnsignedTrailer:
		return StreamingUnsignedTrailer{}, nil
	case streamingSigned:
		return StreamingSigned{}, nil
	case streamingSignedTrailer:
		return StreamingSignedTrailer{}, nil
	}
	var h Hex
	// Only lowercase hex: the payload line is re-encoded in lowercase, so an
	// uppercase value could never verify. The length check comes first:
	// hex.Decode panics when dst is shorter than the decoded input.
	if len(v) == hex.EncodedLen(len(h.SHA256)) && v == strings.ToLower(v) {
		if _, err := hex.Decode(h.SHA256[:], []byte(v)); err == nil {
			return h, nil
		}
	}
	return nil, Invalid{
		Mechanism: m, Problem: InvalidPayloadHash,
		Message: "x-amz-content-sha256 must be " + unsignedPayload + ", " + streamingUnsignedTrailer + ", " + streamingSigned + ", " +
			streamingSignedTrailer + " or a valid sha256 value.",
	}
}

func malformed(m Mechanism, msg string) Invalid {
	return Invalid{Mechanism: m, Problem: Malformed, Message: msg}
}
