package checksum

import (
	"encoding/binary"
	"math/bits"
)

// XXH3-64 and XXH3-128 ported from the xxHash specification
// (https://github.com/Cyan4973/xxHash/blob/dev/doc/xxhash_spec.md) with seed 0
// and the default secret, which is what the AWS CRT computes for S3's XXHASH3
// and XXHASH128 (s3-checksum-residuals-and-record-backlog.md 1.1).

// xxh3Secret is the 192-byte default secret (kSecret).
const xxh3Secret = "" +
	"\xb8\xfe\x6c\x39\x23\xa4\x4b\xbe\x7c\x01\x81\x2c\xf7\x21\xad\x1c" +
	"\xde\xd4\x6d\xe9\x83\x90\x97\xdb\x72\x40\xa4\xa4\xb7\xb3\x67\x1f" +
	"\xcb\x79\xe6\x4e\xcc\xc0\xe5\x78\x82\x5a\xd0\x7d\xcc\xff\x72\x21" +
	"\xb8\x08\x46\x74\xf7\x43\x24\x8e\xe0\x35\x90\xe6\x81\x3a\x26\x4c" +
	"\x3c\x28\x52\xbb\x91\xc3\x00\xcb\x88\xd0\x65\x8b\x1b\x53\x2e\xa3" +
	"\x71\x64\x48\x97\xa2\x0d\xf9\x4e\x38\x19\xef\x46\xa9\xde\xac\xd8" +
	"\xa8\xfa\x76\x3f\xe3\x9c\x34\x3f\xf9\xdc\xbb\xc7\xc7\x0b\x4f\x1d" +
	"\x8a\x51\xe0\x4b\xcd\xb4\x59\x31\xc8\x9f\x7e\xc9\xd9\x78\x73\x64" +
	"\xea\xc5\xac\x83\x34\xd3\xeb\xc3\xc5\x81\xa0\xff\xfa\x13\x63\xeb" +
	"\x17\x0d\xdd\x51\xb7\xf0\xda\x49\xd3\x16\x55\x26\x29\xd4\x68\x9e" +
	"\x2b\x16\xbe\x58\x7d\x47\xa1\xfc\x8f\xf8\xb8\xd1\x7a\xd0\x31\xce" +
	"\x45\xcb\x3a\x8f\x95\x16\x04\x28\xaf\xd7\xfb\xca\xbb\x4b\x40\x7e"

const (
	prime32x1 uint64 = 0x9E3779B1
	prime32x2 uint64 = 0x85EBCA77
	prime32x3 uint64 = 0xC2B2AE3D
	primeMX1  uint64 = 0x165667919E3779F9
	primeMX2  uint64 = 0x9FB21C651E98DF25
)

const (
	xxh3StripeLen       = 64
	xxh3ConsumeRate     = 8 // secret bytes advanced per stripe
	xxh3StripesPerBlock = (len(xxh3Secret) - xxh3StripeLen) / xxh3ConsumeRate
	xxh3MidsizeMax      = 240 // longest input hashed without the stripe loop
	xxh3MidsizeStart    = 3
	xxh3MidsizeLast     = 17
	xxh3SecretSizeMin   = 136
	xxh3LastAccStart    = 7
	xxh3MergeAccsStart  = 11
	// xxh3BufLen holds a whole midsize input, and in the long path the last
	// consumed stripe plus up to one pending stripe.
	xxh3BufLen = 256
	// xxh3x64Size and xxh3x128Size are the XXH3-64 and XXH3-128 digest
	// lengths in bytes.
	xxh3x64Size  = 8
	xxh3x128Size = 16
)

// secret64 and secret32 read the default secret little-endian at off.
func secret64(off int) uint64 {
	s := xxh3Secret[off : off+8]
	return uint64(s[0]) | uint64(s[1])<<8 | uint64(s[2])<<16 | uint64(s[3])<<24 |
		uint64(s[4])<<32 | uint64(s[5])<<40 | uint64(s[6])<<48 | uint64(s[7])<<56
}

func secret32(off int) uint32 {
	s := xxh3Secret[off : off+4]
	return uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24
}

func le64(p []byte) uint64 { return binary.LittleEndian.Uint64(p) }

func le32(p []byte) uint32 { return binary.LittleEndian.Uint32(p) }

func mulFold64(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	return hi ^ lo
}

func xxh3Avalanche(h uint64) uint64 {
	h ^= h >> 37
	h *= primeMX1
	return h ^ h>>32
}

func rrmxmx(h, n uint64) uint64 {
	h ^= bits.RotateLeft64(h, 49) ^ bits.RotateLeft64(h, 24)
	h *= primeMX2
	h ^= h>>35 + n
	h *= primeMX2
	return h ^ h>>28
}

func mix16(p []byte, so int) uint64 {
	return mulFold64(le64(p)^secret64(so), le64(p[8:])^secret64(so+8))
}

// u128 is a 128-bit hash as the two halves the canonical form writes.
type u128 struct{ hi, lo uint64 }

func mix32(acc u128, in1, in2 []byte, so int) u128 {
	acc.lo += mix16(in1, so)
	acc.lo ^= le64(in2) + le64(in2[8:])
	acc.hi += mix16(in2, so+16)
	acc.hi ^= le64(in1) + le64(in1[8:])
	return acc
}

// combined1to3 packs a 1 to 3 byte input as the spec's 1-3 byte class does.
func combined1to3(p []byte) uint32 {
	n := len(p)
	return uint32(p[0])<<16 | uint32(p[n>>1])<<24 | uint32(p[n-1]) |
		uint32(n)<<8 //nolint:gosec // G115: n is 1 to 3
}

// xxh3x64Short hashes an input of at most xxh3MidsizeMax bytes.
func xxh3x64Short(p []byte) uint64 {
	n := len(p)
	switch {
	case n == 0:
		return xxh64Avalanche(secret64(56) ^ secret64(64))
	case n <= 3:
		return xxh64Avalanche(uint64(combined1to3(p)) ^ uint64(secret32(0)^secret32(4)))
	case n <= 8:
		in64 := uint64(le32(p[n-4:])) + uint64(le32(p))<<32
		return rrmxmx(in64^(secret64(8)^secret64(16)), uint64(len(p)))
	case n <= 16:
		lo := le64(p) ^ secret64(24) ^ secret64(32)
		hi := le64(p[n-8:]) ^ secret64(40) ^ secret64(48)
		return xxh3Avalanche(uint64(n) + bits.ReverseBytes64(lo) + hi + mulFold64(lo, hi))
	case n <= 128:
		acc := uint64(n) * prime64x1
		for i := (n - 1) / 32; i >= 0; i-- {
			acc += mix16(p[16*i:], 32*i) + mix16(p[n-16*(i+1):], 32*i+16)
		}
		return xxh3Avalanche(acc)
	}
	acc := uint64(n) * prime64x1
	for i := range 8 {
		acc += mix16(p[16*i:], 16*i)
	}
	acc = xxh3Avalanche(acc)
	for i := 8; i < n/16; i++ {
		acc += mix16(p[16*i:], 16*(i-8)+xxh3MidsizeStart)
	}
	acc += mix16(p[n-16:], xxh3SecretSizeMin-xxh3MidsizeLast)
	return xxh3Avalanche(acc)
}

// xxh3x128Short hashes an input of at most xxh3MidsizeMax bytes.
func xxh3x128Short(p []byte) u128 {
	n := len(p)
	switch {
	case n == 0:
		return u128{
			hi: xxh64Avalanche(secret64(80) ^ secret64(88)),
			lo: xxh64Avalanche(secret64(64) ^ secret64(72)),
		}
	case n <= 3:
		lo := combined1to3(p)
		hi := bits.RotateLeft32(bits.ReverseBytes32(lo), 13)
		return u128{
			hi: xxh64Avalanche(uint64(hi) ^ uint64(secret32(8)^secret32(12))),
			lo: xxh64Avalanche(uint64(lo) ^ uint64(secret32(0)^secret32(4))),
		}
	case n <= 8:
		return xxh3x128Len4to8(p)
	case n <= 16:
		return xxh3x128Len9to16(p)
	case n <= 128:
		acc := u128{lo: uint64(n) * prime64x1}
		for i := (n - 1) / 32; i >= 0; i-- {
			acc = mix32(acc, p[16*i:], p[n-16*(i+1):], 32*i)
		}
		return xxh3x128Finish(acc, uint64(len(p)))
	}
	return xxh3x128Len129to240(p)
}

func xxh3x128Len4to8(p []byte) u128 {
	n := len(p)
	in64 := uint64(le32(p)) + uint64(le32(p[n-4:]))<<32
	keyed := in64 ^ (secret64(16) ^ secret64(24))
	hi, lo := bits.Mul64(keyed, prime64x1+uint64(n)<<2)
	hi += lo << 1
	lo ^= hi >> 3
	lo ^= lo >> 35
	lo *= primeMX2
	lo ^= lo >> 28
	return u128{hi: xxh3Avalanche(hi), lo: lo}
}

func xxh3x128Len9to16(p []byte) u128 {
	n := len(p)
	inLo := le64(p)
	inHi := le64(p[n-8:])
	mHi, mLo := bits.Mul64(inLo^inHi^(secret64(32)^secret64(40)), prime64x1)
	mLo += (uint64(len(p)) - 1) << 54
	inHi ^= secret64(48) ^ secret64(56)
	mHi += inHi + (inHi&0xFFFFFFFF)*(prime32x2-1)
	mLo ^= bits.ReverseBytes64(mHi)
	hHi, hLo := bits.Mul64(mLo, prime64x2)
	hHi += mHi * prime64x2
	return u128{hi: xxh3Avalanche(hHi), lo: xxh3Avalanche(hLo)}
}

func xxh3x128Len129to240(p []byte) u128 {
	n := len(p)
	acc := u128{lo: uint64(n) * prime64x1}
	for i := 32; i < 160; i += 32 {
		acc = mix32(acc, p[i-32:], p[i-16:], i-32)
	}
	acc = u128{hi: xxh3Avalanche(acc.hi), lo: xxh3Avalanche(acc.lo)}
	for i := 160; i <= n; i += 32 {
		acc = mix32(acc, p[i-32:], p[i-16:], xxh3MidsizeStart+i-160)
	}
	acc = mix32(acc, p[n-16:], p[n-32:], xxh3SecretSizeMin-xxh3MidsizeLast-16)
	return xxh3x128Finish(acc, uint64(len(p)))
}

func xxh3x128Finish(acc u128, n uint64) u128 {
	lo := acc.lo + acc.hi
	hi := acc.lo*prime64x1 + acc.hi*prime64x4 + n*prime64x2
	return u128{hi: 0 - xxh3Avalanche(hi), lo: xxh3Avalanche(lo)}
}

// xxh3 is the streaming state XXH3-64 and XXH3-128 share. Up to
// xxh3MidsizeMax bytes it only buffers; past that it consumes every stripe
// that is followed by at least one more byte, which is the
// floor((len-1)/64) stripes the one-shot long path consumes, and keeps the
// last 64 bytes for the final stripe.
type xxh3 struct {
	acc    [8]uint64
	stripe int // stripes consumed in the current block
	total  uint64
	buf    []byte // buf[:off] consumed (only the last stripe is kept), buf[off:] pending
	off    int
}

func (h *xxh3) Reset() {
	h.acc = [8]uint64{prime32x3, prime64x1, prime64x2, prime64x3, prime64x4, prime32x2, prime64x5, prime32x1}
	h.stripe = 0
	h.total = 0
	h.buf = h.buf[:0]
	h.off = 0
}

func (*xxh3) BlockSize() int { return xxh3StripeLen }

func (h *xxh3) Write(p []byte) (int, error) {
	written := len(p)
	h.total += uint64(written)
	for len(p) > 0 {
		k := min(len(p), xxh3BufLen-len(h.buf))
		h.buf = append(h.buf, p[:k]...)
		p = p[k:]
		if h.total > xxh3MidsizeMax {
			h.consume()
		}
	}
	return written, nil
}

func (h *xxh3) consume() {
	for len(h.buf)-h.off > xxh3StripeLen {
		xxh3Accumulate(&h.acc, h.buf[h.off:h.off+xxh3StripeLen], h.stripe*xxh3ConsumeRate)
		h.off += xxh3StripeLen
		h.stripe++
		if h.stripe == xxh3StripesPerBlock {
			xxh3Scramble(&h.acc)
			h.stripe = 0
		}
	}
	if h.off > xxh3StripeLen {
		kept := copy(h.buf, h.buf[h.off-xxh3StripeLen:])
		h.buf = h.buf[:kept]
		h.off = xxh3StripeLen
	}
}

// longAcc returns the accumulators after the final stripe, without changing h.
func (h *xxh3) longAcc() [8]uint64 {
	acc := h.acc
	xxh3Accumulate(&acc, h.buf[len(h.buf)-xxh3StripeLen:], len(xxh3Secret)-xxh3StripeLen-xxh3LastAccStart)
	return acc
}

func xxh3Accumulate(acc *[8]uint64, p []byte, so int) {
	for i := range acc {
		v := le64(p[8*i:])
		k := v ^ secret64(so+8*i)
		acc[i^1] += v
		acc[i] += (k & 0xFFFFFFFF) * (k >> 32)
	}
}

func xxh3Scramble(acc *[8]uint64) {
	for i := range acc {
		a := acc[i]
		a ^= a >> 47
		a ^= secret64(len(xxh3Secret) - xxh3StripeLen + 8*i)
		acc[i] = a * prime32x1
	}
}

func xxh3MergeAccs(acc *[8]uint64, so int, start uint64) uint64 {
	r := start
	for i := range 4 {
		r += mulFold64(acc[2*i]^secret64(so+16*i), acc[2*i+1]^secret64(so+16*i+8))
	}
	return xxh3Avalanche(r)
}

// xxh3x64 is the streaming XXH3-64 hash.
type xxh3x64 struct{ xxh3 }

func newXXH3x64() *xxh3x64 {
	h := &xxh3x64{xxh3{buf: make([]byte, 0, xxh3BufLen)}}
	h.Reset()
	return h
}

func (*xxh3x64) Size() int { return xxh3x64Size }

func (h *xxh3x64) Sum(b []byte) []byte {
	return binary.BigEndian.AppendUint64(b, h.sum64())
}

func (h *xxh3x64) sum64() uint64 {
	if h.total <= xxh3MidsizeMax {
		return xxh3x64Short(h.buf)
	}
	acc := h.longAcc()
	return xxh3MergeAccs(&acc, xxh3MergeAccsStart, h.total*prime64x1)
}

// xxh3x128 is the streaming XXH3-128 hash; Sum appends high64 then low64.
type xxh3x128 struct{ xxh3 }

func newXXH3x128() *xxh3x128 {
	h := &xxh3x128{xxh3{buf: make([]byte, 0, xxh3BufLen)}}
	h.Reset()
	return h
}

func (*xxh3x128) Size() int { return xxh3x128Size }

func (h *xxh3x128) Sum(b []byte) []byte {
	v := h.sum128()
	b = binary.BigEndian.AppendUint64(b, v.hi)
	return binary.BigEndian.AppendUint64(b, v.lo)
}

func (h *xxh3x128) sum128() u128 {
	if h.total <= xxh3MidsizeMax {
		return xxh3x128Short(h.buf)
	}
	acc := h.longAcc()
	return u128{
		hi: xxh3MergeAccs(&acc, len(xxh3Secret)-xxh3StripeLen-xxh3MergeAccsStart, ^(h.total * prime64x2)),
		lo: xxh3MergeAccs(&acc, xxh3MergeAccsStart, h.total*prime64x1),
	}
}
