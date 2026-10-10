package awschunked

import (
	"crypto/sha256"
	"strconv"
)

// Mode is the aws-chunked framing a request uses, which its
// x-amz-content-sha256 value (and, for PlainChunked, Content-Encoding:
// aws-chunked) selects (sigv4-checksums-endpoints.md 3.1). The zero value is
// not a mode.
type Mode uint8

// The aws-chunked framings.
const (
	// PlainChunked is Content-Encoding: aws-chunked with neither chunk
	// signatures nor trailers: "<hex size>\r\n<data>\r\n" ... "0\r\n\r\n".
	PlainChunked Mode = iota + 1
	// UnsignedTrailer is STREAMING-UNSIGNED-PAYLOAD-TRAILER: plain chunks, then
	// the trailer declared in x-amz-trailer after the final zero-length chunk.
	UnsignedTrailer
	// Signed is STREAMING-AWS4-HMAC-SHA256-PAYLOAD: every chunk, including the
	// final zero-length one, carries ";chunk-signature=<64 hex>".
	Signed
	// SignedTrailer is STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER: signed chunks,
	// then the trailer and an x-amz-trailer-signature line.
	SignedTrailer
)

// String returns the constant's name ("SignedTrailer").
func (m Mode) String() string {
	switch m {
	case PlainChunked:
		return "PlainChunked"
	case UnsignedTrailer:
		return "UnsignedTrailer"
	case Signed:
		return "Signed"
	case SignedTrailer:
		return "SignedTrailer"
	}
	return "Mode(" + strconv.Itoa(int(m)) + ")"
}

// framing returns whether chunks carry signatures and whether the body ends
// with a declared trailer, and false for a value that is not a mode.
func (m Mode) framing() (signed, trailer, ok bool) {
	switch m {
	case PlainChunked:
		return false, false, true
	case UnsignedTrailer:
		return false, true, true
	case Signed:
		return true, false, true
	case SignedTrailer:
		return true, true, true
	}
	return false, false, false
}

// signed reports whether chunks framed as m carry signatures.
func (m Mode) signed() bool {
	signed, _, _ := m.framing()
	return signed
}

// trailing reports whether a body framed as m ends with a declared trailer.
func (m Mode) trailing() bool {
	_, trailer, _ := m.framing()
	return trailer
}

// Length is the decoded length a request declares in
// x-amz-decoded-content-length. The zero value declares none: botocore omits
// the header when it streams a body of unknown length.
type Length struct {
	n        int64
	declared bool
}

// ParseLength parses an x-amz-decoded-content-length value; the empty string
// (header absent) declares no length.
func ParseLength(v string) (Length, error) {
	if v == "" {
		return Length{}, nil
	}
	n, err := strconv.ParseUint(v, 10, 63)
	if err != nil {
		return Length{}, &InvalidLengthError{Value: v}
	}
	return Length{n: int64(n), declared: true}, nil
}

// Get returns the declared length and whether one was declared.
func (l Length) Get() (int64, bool) { return l.n, l.declared }

// SignedKind is what a chunk-signature chain link covers.
type SignedKind uint8

// The two things a streaming signature covers.
const (
	// ChunkSigned is a chunk's data; the string to sign starts
	// AWS4-HMAC-SHA256-PAYLOAD.
	ChunkSigned SignedKind = iota + 1
	// TrailerSigned is the trailer fields; the string to sign starts
	// AWS4-HMAC-SHA256-TRAILER.
	TrailerSigned
)

// SignedPayload is what one streaming signature covers: the SHA-256 of a
// chunk's data, or of the trailer fields each written "<name>:<value>\n".
type SignedPayload struct {
	Kind   SignedKind
	SHA256 [sha256.Size]byte
}

// SignatureFunc checks one link of the signature chain: prev is the previous
// signature (Options.Seed for the first chunk), sig the signature as sent.
// A non-nil error ends the body with that error.
type SignatureFunc func(prev string, signed SignedPayload, sig string) error
