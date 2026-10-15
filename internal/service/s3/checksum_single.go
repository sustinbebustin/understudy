package s3

import (
	"bytes"
	"crypto/md5" //nolint:gosec // S3's ETag and Content-MD5 are MD5 by definition; an integrity check, not a security control
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/checksum"
)

// The single-part checksum rules of s3.md 6.2 and 5.11, with the texts
// S3-compatible servers written against S3 use where AWS's are not
// recorded (sigv4-checksums-endpoints.md 3.5, UNVERIFIED).
const (
	multipleChecksumsMessage = "Expecting a single x-amz-checksum- header. Multiple checksum Types are not allowed."
	missingChecksumMessage   = "x-amz-sdk-checksum-algorithm specified, but no corresponding x-amz-checksum-* or x-amz-trailer headers were found."
	unsupportedTrailer       = "The value specified in the x-amz-trailer header is not supported"
	invalidDigestMessage     = "The Content-MD5 you specified was invalid."
	badMD5Message            = "The Content-MD5 you specified did not match what we received."
)

// StoredChecksum is the one full-object checksum an object keeps, in its
// wire form.
type StoredChecksum struct {
	Algorithm string             `json:"algorithm"`
	Value     string             `json:"value"`
	Type      s3api.ChecksumType `json:"type"`
}

// checksumMembers are an input's or output's x-amz-checksum-* members,
// indexed by their checksum.Algorithm, so every literal and every read
// names the algorithm of the member it fills (m[checksum.SHA256]). Index
// 0, the zero Algorithm, is never set.
type checksumMembers [checksum.Count + 1]*string

// sent returns the members a request set, each with its algorithm.
func (m checksumMembers) sent() []sentChecksum {
	var out []sentChecksum
	for _, a := range checksum.Algorithms() {
		if m[a] != nil {
			out = append(out, sentChecksum{alg: a, value: *m[a]})
		}
	}
	return out
}

// membersOf is s as the one set member of an output's checksum members.
func membersOf(s StoredChecksum) checksumMembers {
	var m checksumMembers
	for _, a := range checksum.Algorithms() {
		if a.String() == s.Algorithm {
			m[a] = new(s.Value)
		}
	}
	return m
}

// sentChecksum is one x-amz-checksum-* value a request carried.
type sentChecksum struct {
	alg   checksum.Algorithm
	value string
}

// declaredChecksum is how a single-part write states its checksum
// (s3.md 6.2): not at all (S3 computes CRC64NVME), in a header, or in the
// aws-chunked trailer x-amz-trailer names.
//
//sumtype:decl
type declaredChecksum interface{ algorithm() checksum.Algorithm }

type noChecksum struct{}

type headerChecksum struct{ sent sentChecksum }

type trailerChecksum struct{ alg checksum.Algorithm }

func (noChecksum) algorithm() checksum.Algorithm        { return checksum.CRC64NVME }
func (h headerChecksum) algorithm() checksum.Algorithm  { return h.sent.alg }
func (t trailerChecksum) algorithm() checksum.Algorithm { return t.alg }

// parseDeclared reads a write's checksum headers before its body:
// sdkAlgorithm is x-amz-sdk-checksum-algorithm and trailer the
// x-amz-trailer value, empty when not sent.
func parseDeclared(m checksumMembers, sdkAlgorithm *s3api.ChecksumAlgorithm, trailer string) (declaredChecksum, *awserr.Error) {
	sent := m.sent()
	switch {
	case len(sent) > 1:
		return nil, errInvalidRequest(multipleChecksumsMessage)
	case len(sent) == 1:
		h := sent[0]
		if _, err := checksum.Decode(h.alg, h.value); err != nil {
			return nil, errInvalidRequest("Value for " + h.alg.HeaderName() + " header is invalid.")
		}
		if sdkAlgorithm != nil && string(*sdkAlgorithm) != h.alg.String() {
			return nil, errBadChecksum(string(*sdkAlgorithm))
		}
		return headerChecksum{sent: h}, nil
	case trailer != "":
		a, err := checksum.ParseHeaderName(trailer)
		if err != nil {
			return nil, errInvalidRequest(unsupportedTrailer)
		}
		if sdkAlgorithm != nil && string(*sdkAlgorithm) != a.String() {
			return nil, errBadChecksum(string(*sdkAlgorithm))
		}
		return trailerChecksum{alg: a}, nil
	case sdkAlgorithm != nil:
		return nil, errInvalidRequest(missingChecksumMessage)
	}
	return noChecksum{}, nil
}

// verifyDeclared checks the declared checksum against the one computed
// over the bytes received, once the body reached EOF; trailerValue is the
// trailer's value for a trailerChecksum.
func verifyDeclared(d declaredChecksum, computed checksum.Sum, trailerValue string) *awserr.Error {
	var value, form string
	switch d := d.(type) {
	case noChecksum:
		return nil
	case headerChecksum:
		value, form = d.sent.value, "header"
	case trailerChecksum:
		value, form = trailerValue, "trailing header"
	}
	err := checksum.Verify(value, computed)
	if isA[*checksum.InvalidValueError](err) {
		return errInvalidRequest("Value for " + computed.Algorithm().HeaderName() + " " + form + " is invalid.")
	}
	if err != nil {
		return errBadChecksum(computed.Algorithm().String())
	}
	return nil
}

// contentMD5 is a parsed Content-MD5 header: the value sent and its 16
// digest bytes.
type contentMD5 struct {
	sent   string
	digest []byte
}

// parseContentMD5 reads Content-MD5; a value that is not base64 of 16
// bytes is InvalidDigest (s3.md 5.11).
func parseContentMD5(v *string) (*contentMD5, *awserr.Error) {
	if v == nil {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(*v)
	if err != nil || len(b) != md5.Size {
		return nil, withField(s3api.ErrInvalidDigest(invalidDigestMessage), "Content-MD5", *v)
	}
	return &contentMD5{sent: *v, digest: b}, nil
}

// verify compares the Content-MD5 sent with the MD5 of the bytes
// received: BadDigest with the expected digest in hex and the calculated
// one in base64, calculated first, as the PutObject recordings show
// (s3.md 5.11; testdata/parity/s3/R08/04-checksum-plus-wrong-content-md5.json).
func (c *contentMD5) verify(computed []byte) *awserr.Error {
	if c == nil || bytes.Equal(c.digest, computed) {
		return nil
	}
	e := s3api.ErrBadDigest(badMD5Message)
	return withField(withField(e, "CalculatedDigest", base64.StdEncoding.EncodeToString(computed)), "ExpectedDigest", hex.EncodeToString(c.digest))
}

// errBadChecksum answers a declared checksum that does not match the bytes
// (s3.md 6.2: "The CRC32 you specified did not match the calculated
// checksum."), naming the algorithm as its S3 enum value.
func errBadChecksum(name string) *awserr.Error {
	return s3api.ErrBadDigest("The " + strings.ToUpper(name) + " you specified did not match the calculated checksum.")
}
