package auth

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 is HMAC-SHA1 by definition; boto3 presigns S3 URLs with it.
	"encoding/base64"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// subresources are the query parameters a SigV2 canonical resource keeps,
// botocore's HmacV1Auth.QSAOfInterest (sigv4-checksums-endpoints.md 2.3).
var subresources = map[string]struct{}{ //nolint:gochecknoglobals // a constant set; Go has no constant maps.
	"accelerate": {}, "acl": {}, "analytics": {}, "cors": {}, "defaultObjectAcl": {}, "delete": {},
	"inventory": {}, "lifecycle": {}, "location": {}, "logging": {}, "metrics": {}, "notification": {},
	"object-lock": {}, "partNumber": {}, "policy": {}, "replication": {}, "requestPayment": {},
	"response-cache-control": {}, "response-content-disposition": {}, "response-content-encoding": {},
	"response-content-language": {}, "response-content-type": {}, "response-expires": {}, "restore": {},
	"select": {}, "select-type": {}, "storageClass": {}, "tagging": {}, "torrent": {}, "uploadId": {},
	"uploads": {}, "versionId": {}, "versioning": {}, "versions": {}, "website": {},
}

// VerifySigV2Presigned checks a SigV2 presigned S3 URL against secret.
// resourcePath is the path-style path, /<bucket>/<key> as sent, which
// differs from the wire path for a virtual-hosted request. Expiry is judged
// on the emulator clock (lambda-runtime-contract.md 3 row 10), with Expires
// read as an emulator instant: unlike SigV4's X-Amz-Date, a SigV2 URL
// carries only when it expires, not when it was signed, so its expiry
// cannot be placed on the emulator timeline. It is right only while
// emulator time read wall time when the URL was signed.
func VerifySigV2Presigned(a SigV2Query, req Normalized, resourcePath, secret string, emuNow clock.Instant, v Vocabulary) *awserr.Error {
	if !emuNow.Before(clock.FromWire(a.Expires)) {
		return expired(a.Expires, emuNow, v)
	}
	expires := strconv.FormatInt(a.Expires.Unix(), 10)
	sts := func(r Normalized) string { return sigV2StringToSign(r, queryValues(r.RawQuery), expires, resourcePath) }
	return checkSigV2(a.Key, req, sts, a.Signature, secret, v)
}

// VerifySigV2 checks an Authorization: AWS header against secret, with the
// request time within 15 minutes of wallNow. resourcePath is as for
// VerifySigV2Presigned.
func VerifySigV2(a SigV2Header, req Normalized, resourcePath, secret string, wallNow time.Time, v Vocabulary) *awserr.Error {
	if err := checkSkew(a.Time, a.Stamp, wallNow, v); err != nil {
		return err
	}
	// The Date line is empty when x-amz-date carries the time.
	dateLine := req.Header.Get(hdrDate)
	if req.Header.Get(hdrAmzDate) != "" {
		dateLine = ""
	}
	sts := func(r Normalized) string { return sigV2StringToSign(r, nil, dateLine, resourcePath) }
	return checkSigV2(a.Key, req, sts, a.Signature, secret, v)
}

// checkSigV2 compares got with the signature of stringToSign(req). SigV2 is
// S3's alone, so a mismatch always has S3's text; S3Dialect adds the
// signing-debug elements, whose string to sign is built over req with the
// session token redacted, as checkSignature's canonical request is.
func checkSigV2(key string, req Normalized, stringToSign func(Normalized) string, got, secret string, v Vocabulary) *awserr.Error {
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(stringToSign(req)))
	want := base64.StdEncoding.EncodeToString(m.Sum(nil))
	if hmac.Equal([]byte(got), []byte(want)) {
		return nil
	}
	if v.Dialect == S3Dialect {
		return s3Mismatch(key, stringToSign(req.withoutSessionToken()), got, v)
	}
	return failure(v.Mismatch, s3MismatchMessage)
}

// sigV2StringToSign is botocore's HmacV1 canonical string. Presigned URLs
// carry x-amz-*, Content-MD5 and Content-Type as query parameters; a header
// of the same name wins.
func sigV2StringToSign(req Normalized, q url.Values, dateLine, resourcePath string) string {
	standard := func(name string) string {
		if v := req.Header.Get(name); v != "" {
			return strings.TrimSpace(v)
		}
		return strings.TrimSpace(q.Get(strings.ToLower(name)))
	}
	amz := map[string]string{}
	for name, vals := range q {
		if strings.HasPrefix(name, "x-amz-") {
			amz[name] = strings.Join(trimAll(vals), ",")
		}
	}
	for name, vals := range req.Header {
		if lower := strings.ToLower(name); strings.HasPrefix(lower, "x-amz-") {
			amz[lower] = strings.Join(trimAll(vals), ",")
		}
	}
	var b strings.Builder
	b.WriteString(req.Method + "\n" + standard("Content-Md5") + "\n" + standard("Content-Type") + "\n" + dateLine + "\n")
	for _, name := range slices.Sorted(maps.Keys(amz)) {
		b.WriteString(name + ":" + amz[name] + "\n")
	}
	b.WriteString(canonicalResource(resourcePath, req.RawQuery))
	return b.String()
}

// canonicalResource appends the sub-resources present, sorted by name, with
// decoded values; a parameter sent without "=" is listed without one.
func canonicalResource(path, rawQuery string) string {
	var kept []string
	for part := range strings.SplitSeq(rawQuery, "&") {
		name, value, hasValue := strings.Cut(part, "=")
		if _, ok := subresources[name]; !ok {
			continue
		}
		if hasValue {
			if u, err := url.PathUnescape(value); err == nil {
				value = u
			}
			name += "=" + value
		}
		kept = append(kept, name)
	}
	if len(kept) == 0 {
		return path
	}
	slices.SortStableFunc(kept, func(a, b string) int {
		an, _, _ := strings.Cut(a, "=")
		bn, _, _ := strings.Cut(b, "=")
		return strings.Compare(an, bn)
	})
	return path + "?" + strings.Join(kept, "&")
}

func trimAll(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strings.TrimSpace(v)
	}
	return out
}

// queryValues decodes a raw query, keeping undecodable escapes verbatim as
// the canonical query does.
func queryValues(raw string) url.Values {
	q := url.Values{}
	for part := range strings.SplitSeq(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		q.Add(unescape(k), unescape(v))
	}
	return q
}
