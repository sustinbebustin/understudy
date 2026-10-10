package checksum

import (
	"encoding/binary"
	"strconv"
)

// Part is one part of a multipart object: its checksum and its size in bytes.
type Part struct {
	Sum  Sum
	Size int64
}

// Combine returns the checksum of the concatenation of parts, computed from
// the part checksums and sizes alone. S3 reports it as a FULL_OBJECT multipart
// checksum. Only the CRC family combines (Algorithm.Combinable).
func Combine(a Algorithm, parts []Part) (Sum, error) {
	crc, ok := crcFor(a)
	if !ok {
		return Sum{}, &NotCombinableError{Algorithm: a}
	}
	var acc uint64 // the CRC of no bytes is 0: init and final XOR cancel
	for i, p := range parts {
		if p.Sum.alg != a {
			return Sum{}, &PartAlgorithmError{Index: i, Want: a, Got: p.Sum.alg}
		}
		if p.Size < 0 {
			return Sum{}, &PartSizeError{Index: i, Size: p.Size}
		}
		acc = crc.combine(acc, crc.value(p.Sum), uint64(p.Size))
	}
	return crc.sum(a, acc), nil
}

// Composite returns the COMPOSITE multipart checksum: the checksum of the
// concatenated raw part digests, base64 encoded, with "-<number of parts>"
// appended ("rFFwCQ==-2"; s3.md 6.4). A multipart upload has at least one
// part, so no parts is a NoPartsError.
func Composite(a Algorithm, parts []Sum) (string, error) {
	h, err := New(a)
	if err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return "", &NoPartsError{}
	}
	for i, p := range parts {
		if p.alg != a {
			return "", &PartAlgorithmError{Index: i, Want: a, Got: p.alg}
		}
		_, _ = h.Write([]byte(p.digest))
	}
	return h.Checksum().String() + "-" + strconv.Itoa(len(parts)), nil
}

// crcParams describes a bit-reflected CRC whose init and final XOR are all
// ones, which covers CRC32, CRC32C and CRC64NVME.
type crcParams struct {
	width int    // 32 or 64
	poly  uint64 // reflected polynomial
}

func crcFor(a Algorithm) (crcParams, bool) {
	switch a {
	case CRC32:
		return crcParams{width: 32, poly: 0xEDB88320}, true
	case CRC32C:
		return crcParams{width: 32, poly: 0x82F63B78}, true
	case CRC64NVME:
		return crcParams{width: 64, poly: crc64NVMEPoly}, true
	case SHA1, SHA256, SHA512, MD5, XXHASH64, XXHASH3, XXHASH128:
		return crcParams{}, false
	}
	return crcParams{}, false
}

func (p crcParams) value(s Sum) uint64 {
	if p.width == 32 {
		return uint64(binary.BigEndian.Uint32([]byte(s.digest)))
	}
	return binary.BigEndian.Uint64([]byte(s.digest))
}

func (p crcParams) sum(a Algorithm, v uint64) Sum {
	var b []byte
	if p.width == 32 {
		b = binary.BigEndian.AppendUint32(nil, uint32(v)) //nolint:gosec // G115: a 32-bit CRC fits
	} else {
		b = binary.BigEndian.AppendUint64(nil, v)
	}
	return Sum{alg: a, digest: string(b)}
}

// combine returns CRC(A||B) from CRC(A), CRC(B) and len(B):
// x^(8*len(B)) * CRC(A) mod P, XOR CRC(B). It follows Mark Adler's multmodp
// and x2nmodp from zlib's crc32.c, generalized to width 32 and 64.
func (p crcParams) combine(crcA, crcB, lenB uint64) uint64 {
	if lenB == 0 {
		return crcA
	}
	return p.mulMod(p.shiftOperator(lenB), crcA) ^ crcB
}

// mulMod multiplies two polynomials modulo P in the reflected representation.
func (p crcParams) mulMod(a, b uint64) uint64 {
	m := uint64(1) << (p.width - 1)
	var r uint64
	for {
		if a&m != 0 {
			r ^= b
			if a&(m-1) == 0 {
				return r
			}
		}
		m >>= 1
		if b&1 != 0 {
			b = b>>1 ^ p.poly
		} else {
			b >>= 1
		}
	}
}

// shiftOperator returns x^(8n) mod P, the operator that appends n zero bytes.
func (p crcParams) shiftOperator(n uint64) uint64 {
	acc := uint64(1) << (p.width - 1) // x^0
	sq := acc >> 1                    // x^1
	for range 3 {                     // x^8
		sq = p.mulMod(sq, sq)
	}
	for ; n != 0; n >>= 1 {
		if n&1 != 0 {
			acc = p.mulMod(acc, sq)
		}
		sq = p.mulMod(sq, sq)
	}
	return acc
}

// NotCombinableError reports a Combine for an algorithm whose part checksums
// do not determine the whole-object checksum. S3 allows FULL_OBJECT multipart
// checksums only for CRC32, CRC32C and CRC64NVME.
type NotCombinableError struct {
	Algorithm Algorithm
}

func (e *NotCombinableError) Error() string {
	return "checksum: " + e.Algorithm.String() + " part checksums cannot be combined; only CRC32, CRC32C and CRC64NVME combine"
}

// PartAlgorithmError reports a part checksum of another algorithm than the
// multipart checksum being computed.
type PartAlgorithmError struct {
	Index int // position in the parts slice
	Want  Algorithm
	Got   Algorithm
}

func (e *PartAlgorithmError) Error() string {
	return "checksum: part at index " + strconv.Itoa(e.Index) + " has a " + e.Got.String() +
		" checksum, want " + e.Want.String()
}

// NoPartsError reports a Composite of no parts, which S3 never computes: a
// multipart upload completes with at least one part.
type NoPartsError struct{}

func (*NoPartsError) Error() string {
	return "checksum: a COMPOSITE multipart checksum needs at least one part checksum; none were given"
}

// PartSizeError reports a part with a negative size.
type PartSizeError struct {
	Index int // position in the parts slice
	Size  int64
}

func (e *PartSizeError) Error() string {
	return "checksum: part at index " + strconv.Itoa(e.Index) + " has negative size " + strconv.FormatInt(e.Size, 10)
}
