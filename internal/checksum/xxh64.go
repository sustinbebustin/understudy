package checksum

import (
	"encoding/binary"
	"math/bits"
)

// XXH64 ported from the xxHash specification
// (https://github.com/Cyan4973/xxHash/blob/dev/doc/xxhash_spec.md), seed 0.

const (
	prime64x1 uint64 = 0x9E3779B185EBCA87
	prime64x2 uint64 = 0xC2B2AE3D27D4EB4F
	prime64x3 uint64 = 0x165667B19E3779F9
	prime64x4 uint64 = 0x85EBCA77C2B2AE63
	prime64x5 uint64 = 0x27D4EB2F165667C5
)

const (
	// xxh64StripeLen is the four-lane stripe XXH64 consumes per round.
	xxh64StripeLen = 32
	// xxh64Size is the XXH64 digest length in bytes.
	xxh64Size = 8
)

// xxh64 is a streaming XXH64 with seed 0.
type xxh64 struct {
	v     [4]uint64
	total uint64
	buf   [xxh64StripeLen]byte
	n     int // bytes buffered in buf
}

func newXXH64() *xxh64 {
	h := &xxh64{}
	h.Reset()
	return h
}

func (h *xxh64) Reset() {
	// seed + P1 + P2, seed + P2, seed, seed - P1, in wrapping arithmetic.
	p1 := prime64x1
	h.v = [4]uint64{p1 + prime64x2, prime64x2, 0, 0 - p1}
	h.total = 0
	h.n = 0
}

func (*xxh64) Size() int { return xxh64Size }

func (*xxh64) BlockSize() int { return xxh64StripeLen }

func (h *xxh64) Write(p []byte) (int, error) {
	written := len(p)
	h.total += uint64(written)
	if h.n > 0 {
		k := copy(h.buf[h.n:], p)
		h.n += k
		p = p[k:]
		if h.n < xxh64StripeLen {
			return written, nil
		}
		h.stripe(h.buf[:])
		h.n = 0
	}
	for len(p) >= xxh64StripeLen {
		h.stripe(p[:xxh64StripeLen])
		p = p[xxh64StripeLen:]
	}
	h.n = copy(h.buf[:], p)
	return written, nil
}

func (h *xxh64) Sum(b []byte) []byte {
	return binary.BigEndian.AppendUint64(b, h.sum64())
}

func (h *xxh64) stripe(p []byte) {
	for i := range h.v {
		h.v[i] = xxh64Round(h.v[i], binary.LittleEndian.Uint64(p[8*i:]))
	}
}

func (h *xxh64) sum64() uint64 {
	var acc uint64
	if h.total >= xxh64StripeLen {
		v := h.v
		acc = bits.RotateLeft64(v[0], 1) + bits.RotateLeft64(v[1], 7) +
			bits.RotateLeft64(v[2], 12) + bits.RotateLeft64(v[3], 18)
		for _, lane := range v {
			acc = xxh64MergeRound(acc, lane)
		}
	} else {
		acc = prime64x5
	}
	acc += h.total
	return xxh64Avalanche(xxh64Finalize(acc, h.buf[:h.n]))
}

func xxh64Finalize(acc uint64, p []byte) uint64 {
	for ; len(p) >= 8; p = p[8:] {
		acc ^= xxh64Round(0, binary.LittleEndian.Uint64(p))
		acc = bits.RotateLeft64(acc, 27)*prime64x1 + prime64x4
	}
	if len(p) >= 4 {
		acc ^= uint64(binary.LittleEndian.Uint32(p)) * prime64x1
		acc = bits.RotateLeft64(acc, 23)*prime64x2 + prime64x3
		p = p[4:]
	}
	for _, c := range p {
		acc ^= uint64(c) * prime64x5
		acc = bits.RotateLeft64(acc, 11) * prime64x1
	}
	return acc
}

func xxh64Round(acc, input uint64) uint64 {
	acc += input * prime64x2
	return bits.RotateLeft64(acc, 31) * prime64x1
}

func xxh64MergeRound(acc, lane uint64) uint64 {
	acc ^= xxh64Round(0, lane)
	return acc*prime64x1 + prime64x4
}

func xxh64Avalanche(h uint64) uint64 {
	h ^= h >> 33
	h *= prime64x2
	h ^= h >> 29
	h *= prime64x3
	h ^= h >> 32
	return h
}
