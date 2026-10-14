package checksum_test

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/cespare/xxhash/v2"
	"github.com/zeebo/xxh3"
	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/checksum"
)

// oracleMaxLen bounds the differential inputs: eight XXH3 1024-byte blocks,
// enough for multi-block bodies with every partial-block tail.
const oracleMaxLen = 8 << 10

// oracleDigest is the big-endian digest the reference libraries give for p:
// cespare/xxhash for XXH64 and zeebo/xxh3 for XXH3-64 and XXH3-128 (ADR 0016),
// in the wire byte order the port uses, XXH3-128 as high64 || low64.
func oracleDigest(t *rapid.T, alg checksum.Algorithm, p []byte) []byte {
	switch alg {
	case checksum.XXHASH64:
		return binary.BigEndian.AppendUint64(nil, xxhash.Sum64(p))
	case checksum.XXHASH3:
		return binary.BigEndian.AppendUint64(nil, xxh3.Hash(p))
	case checksum.XXHASH128:
		sum := xxh3.Hash128(p).Bytes()
		return sum[:]
	case checksum.CRC32, checksum.CRC32C, checksum.CRC64NVME,
		checksum.SHA1, checksum.SHA256, checksum.SHA512, checksum.MD5:
		t.Fatalf("no oracle for %v", alg)
		return nil
	default:
		t.Fatalf("no oracle for %v", alg)
		return nil
	}
}

// oracleLength draws a length up to oracleMaxLen, half the time within three
// bytes of a dispatch boundary: the XXH3 short classes (0, 3, 8, 16, 128,
// 240), the XXH64 32-byte stripe, and the XXH3 64-byte stripe and 1024-byte
// block, whose multiples reach every multi-stripe partial-block tail.
func oracleLength(t *rapid.T) int {
	near := rapid.Custom(func(t *rapid.T) int {
		multiple := rapid.Custom(func(t *rapid.T) int {
			step := rapid.SampledFrom([]int{32, 64, 1024}).Draw(t, "step")
			return step * rapid.IntRange(1, oracleMaxLen/step).Draw(t, "multiple")
		})
		base := rapid.OneOf(rapid.SampledFrom([]int{0, 3, 8, 16, 128, 240}), multiple).Draw(t, "boundary")
		return min(max(base+rapid.IntRange(-3, 3).Draw(t, "offset"), 0), oracleMaxLen)
	})
	return rapid.OneOf(near, rapid.IntRange(0, oracleMaxLen)).Draw(t, "len")
}

// TestXXHashMatchesReferenceLibraries compares the port with the reference
// implementations over random inputs, written in one call and streamed in
// random chunks. The published vectors leave lengths such as 1500 bytes, a
// multi-stripe partial-block tail, unpinned; the oracles cover them.
func TestXXHashMatchesReferenceLibraries(t *testing.T) {
	t.Parallel()
	alg := []checksum.Algorithm{checksum.XXHASH64, checksum.XXHASH3, checksum.XXHASH128}
	rapid.Check(t, func(t *rapid.T) {
		alg := rapid.SampledFrom(alg).Draw(t, "alg")
		var seed [32]byte
		binary.LittleEndian.PutUint64(seed[:], rapid.Uint64().Draw(t, "seed"))
		data := make([]byte, oracleLength(t))
		if _, err := rand.NewChaCha8(seed).Read(data); err != nil {
			t.Fatal(err)
		}
		want := oracleDigest(t, alg, data)

		if got := sumOf(t, alg, data).Bytes(); !bytes.Equal(got, want) {
			t.Fatalf("%v one-shot over %d bytes: %x, oracle %x", alg, len(data), got, want)
		}

		h, err := checksum.New(alg)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range chunks(t, data) {
			_, _ = h.Write(c)
		}
		if got := h.Checksum().Bytes(); !bytes.Equal(got, want) {
			t.Fatalf("%v streamed over %d bytes: %x, oracle %x", alg, len(data), got, want)
		}
	})
}
