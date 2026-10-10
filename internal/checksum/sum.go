package checksum

import (
	"crypto/subtle"
	"encoding/base64"
	"strconv"
)

// Sum is a finished checksum: an algorithm and its big-endian digest. Sums are
// comparable with ==. The zero value is not a valid checksum.
type Sum struct {
	alg    Algorithm
	digest string
}

// Decode parses the wire form of a checksum of algorithm a: standard padded
// base64 of exactly a.Size() digest bytes, as sent in x-amz-checksum-<a>
// headers, trailers and Checksum<A> XML members. Only the canonical encoding
// is accepted: the decoder alone would skip CR and LF and ignore non-zero
// padding bits, so the value must re-encode to itself.
func Decode(a Algorithm, value string) (Sum, error) {
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || a.Size() == 0 || len(b) != a.Size() || base64.StdEncoding.EncodeToString(b) != value {
		return Sum{}, &InvalidValueError{Algorithm: a, Value: value}
	}
	return Sum{alg: a, digest: string(b)}, nil
}

// Verify decodes value as a checksum of computed's algorithm and compares it
// with computed. It returns InvalidValueError when value is not a well-formed
// checksum (S3 answers InvalidRequest) and MismatchError when it is well formed
// but differs (S3 answers BadDigest). A zero computed Sum is a caller defect
// and returns UnknownAlgorithmError.
func Verify(value string, computed Sum) error {
	if computed.alg.Size() == 0 {
		return &UnknownAlgorithmError{Value: computed.alg.String()}
	}
	declared, err := Decode(computed.alg, value)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(declared.digest), []byte(computed.digest)) != 1 {
		return &MismatchError{Declared: declared, Computed: computed}
	}
	return nil
}

// Algorithm returns the algorithm that produced s.
func (s Sum) Algorithm() Algorithm { return s.alg }

// Bytes returns a copy of the big-endian digest.
func (s Sum) Bytes() []byte { return []byte(s.digest) }

// String returns the wire form: standard padded base64 of the big-endian
// digest. For XXHASH128 the digest is high64 then low64.
func (s Sum) String() string {
	return base64.StdEncoding.EncodeToString([]byte(s.digest))
}

// InvalidValueError reports a checksum value that is not base64 of a digest of
// the algorithm's size.
type InvalidValueError struct {
	Algorithm Algorithm
	Value     string
}

func (e *InvalidValueError) Error() string {
	return "checksum: " + strconv.Quote(e.Value) + " is not a base64 " + e.Algorithm.String() +
		" digest of " + strconv.Itoa(e.Algorithm.Size()) + " bytes"
}

// MismatchError reports a declared checksum that differs from the one computed
// over the received bytes.
type MismatchError struct {
	Declared Sum
	Computed Sum
}

func (e *MismatchError) Error() string {
	return "checksum: declared " + e.Declared.alg.String() + " " + e.Declared.String() +
		" does not match " + e.Computed.String() + " computed over the received bytes"
}
