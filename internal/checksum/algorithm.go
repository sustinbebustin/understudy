package checksum

import (
	"crypto/md5"  //nolint:gosec // S3 accepts MD5 as a flexible checksum; it is an integrity check, not a security control
	"crypto/sha1" //nolint:gosec // S3 accepts SHA1 as a flexible checksum; it is an integrity check, not a security control
	"crypto/sha256"
	"crypto/sha512"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"strconv"
	"strings"
)

// Algorithm is one of the ten S3 flexible checksum algorithms
// (com.amazonaws.s3#ChecksumAlgorithm). The zero value is not an algorithm.
type Algorithm uint8

// The S3 flexible checksum algorithms.
const (
	CRC32 Algorithm = iota + 1
	CRC32C
	CRC64NVME
	SHA1
	SHA256
	SHA512
	MD5
	XXHASH64
	XXHASH3
	XXHASH128
)

// headerPrefix starts every x-amz-checksum-<algorithm> header and trailer name.
const headerPrefix = "x-amz-checksum-"

// Algorithms returns every algorithm in declaration order.
func Algorithms() []Algorithm {
	return []Algorithm{CRC32, CRC32C, CRC64NVME, SHA1, SHA256, SHA512, MD5, XXHASH64, XXHASH3, XXHASH128}
}

// ParseAlgorithm returns the algorithm named by its exact S3 enum value, as
// sent in x-amz-checksum-algorithm and x-amz-sdk-checksum-algorithm ("CRC32").
func ParseAlgorithm(s string) (Algorithm, error) {
	for _, a := range Algorithms() {
		if a.String() == s {
			return a, nil
		}
	}
	return 0, &UnknownAlgorithmError{Value: s}
}

// ParseHeaderName returns the algorithm whose x-amz-checksum-<algorithm>
// header or trailer name is name, ignoring case as HTTP field names do.
func ParseHeaderName(name string) (Algorithm, error) {
	for _, a := range Algorithms() {
		if strings.EqualFold(a.HeaderName(), name) {
			return a, nil
		}
	}
	return 0, &UnknownAlgorithmError{Value: name}
}

// String returns the S3 enum value ("CRC64NVME").
func (a Algorithm) String() string {
	switch a {
	case CRC32:
		return "CRC32"
	case CRC32C:
		return "CRC32C"
	case CRC64NVME:
		return "CRC64NVME"
	case SHA1:
		return "SHA1"
	case SHA256:
		return "SHA256"
	case SHA512:
		return "SHA512"
	case MD5:
		return "MD5"
	case XXHASH64:
		return "XXHASH64"
	case XXHASH3:
		return "XXHASH3"
	case XXHASH128:
		return "XXHASH128"
	}
	return "Algorithm(" + strconv.Itoa(int(a)) + ")"
}

// HeaderName returns the lower-case header and trailer name that carries this
// algorithm's value ("x-amz-checksum-crc32").
func (a Algorithm) HeaderName() string {
	return headerPrefix + strings.ToLower(a.String())
}

// Size returns the digest length in bytes, or 0 for a value that is not an
// algorithm.
func (a Algorithm) Size() int {
	switch a {
	case CRC32, CRC32C:
		return crc32.Size
	case CRC64NVME:
		return crc64.Size
	case SHA1:
		return sha1.Size
	case SHA256:
		return sha256.Size
	case SHA512:
		return sha512.Size
	case MD5:
		return md5.Size
	case XXHASH64:
		return xxh64Size
	case XXHASH3:
		return xxh3x64Size
	case XXHASH128:
		return xxh3x128Size
	}
	return 0
}

// Combinable reports whether part checksums of this algorithm combine into
// the whole-object checksum, which is what S3 calls a FULL_OBJECT multipart
// checksum (the CRC family).
func (a Algorithm) Combinable() bool {
	_, ok := crcFor(a)
	return ok
}

// Hash is a running checksum for one algorithm. It is a hash.Hash whose Sum
// appends the big-endian digest S3 encodes on the wire. The running hash is
// unexported so it always matches the algorithm, which every Sum it produces
// relies on. The zero value is not usable; obtain a Hash from New.
type Hash struct {
	h   hash.Hash
	alg Algorithm
}

// New returns a running checksum for a. It fails only for a value that is not
// one of the declared algorithms.
func New(a Algorithm) (*Hash, error) {
	h, err := newHash(a)
	if err != nil {
		return nil, err
	}
	return &Hash{h: h, alg: a}, nil
}

// Write adds p to the running checksum. It never returns an error.
func (h *Hash) Write(p []byte) (int, error) { return h.h.Write(p) }

// Sum appends the big-endian digest of everything written so far to b.
func (h *Hash) Sum(b []byte) []byte { return h.h.Sum(b) }

// Reset discards everything written so far.
func (h *Hash) Reset() { h.h.Reset() }

// Size returns the digest length in bytes.
func (h *Hash) Size() int { return h.h.Size() }

// BlockSize returns the underlying hash's block size.
func (h *Hash) BlockSize() int { return h.h.BlockSize() }

// Algorithm returns the algorithm h computes.
func (h *Hash) Algorithm() Algorithm { return h.alg }

// Checksum returns the checksum of everything written so far.
func (h *Hash) Checksum() Sum {
	return Sum{alg: h.alg, digest: string(h.Sum(nil))}
}

func newHash(a Algorithm) (hash.Hash, error) {
	switch a {
	case CRC32:
		return crc32.NewIEEE(), nil
	case CRC32C:
		return crc32.New(crc32.MakeTable(crc32.Castagnoli)), nil
	case CRC64NVME:
		return crc64.New(crc64NVMETable()), nil
	case SHA1:
		return sha1.New(), nil //nolint:gosec // see the import
	case SHA256:
		return sha256.New(), nil
	case SHA512:
		return sha512.New(), nil
	case MD5:
		return md5.New(), nil //nolint:gosec // see the import
	case XXHASH64:
		return newXXH64(), nil
	case XXHASH3:
		return newXXH3x64(), nil
	case XXHASH128:
		return newXXH3x128(), nil
	}
	return nil, &UnknownAlgorithmError{Value: a.String()}
}

// UnknownAlgorithmError reports a value that names no S3 checksum algorithm.
type UnknownAlgorithmError struct {
	Value string // the algorithm or header name as received
}

func (e *UnknownAlgorithmError) Error() string {
	return "checksum: " + strconv.Quote(e.Value) + " is not an S3 checksum algorithm or x-amz-checksum-<algorithm> name"
}
