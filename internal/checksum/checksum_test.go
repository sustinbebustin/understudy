package checksum_test

import (
	"encoding/binary"
	"errors"
	"hash"
	"math/rand/v2"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/checksum"
)

// A *Hash plugs into io.MultiWriter, io.TeeReader and anything else that
// takes a hash.Hash.
var _ hash.Hash = (*checksum.Hash)(nil)

// The S3 model's ChecksumAlgorithm enum values and the header names botocore,
// Java v2 and C++ derive from them (s3-checksum-residuals-and-record-backlog.md 1.1, 1.3).
func TestAlgorithmNames(t *testing.T) {
	t.Parallel()
	want := map[checksum.Algorithm][2]string{
		checksum.CRC32:     {"CRC32", "x-amz-checksum-crc32"},
		checksum.CRC32C:    {"CRC32C", "x-amz-checksum-crc32c"},
		checksum.CRC64NVME: {"CRC64NVME", "x-amz-checksum-crc64nvme"},
		checksum.SHA1:      {"SHA1", "x-amz-checksum-sha1"},
		checksum.SHA256:    {"SHA256", "x-amz-checksum-sha256"},
		checksum.SHA512:    {"SHA512", "x-amz-checksum-sha512"},
		checksum.MD5:       {"MD5", "x-amz-checksum-md5"},
		checksum.XXHASH64:  {"XXHASH64", "x-amz-checksum-xxhash64"},
		checksum.XXHASH3:   {"XXHASH3", "x-amz-checksum-xxhash3"},
		checksum.XXHASH128: {"XXHASH128", "x-amz-checksum-xxhash128"},
	}
	if got := len(checksum.Algorithms()); got != len(want) {
		t.Fatalf("Algorithms() has %d entries, want %d", got, len(want))
	}
	for _, a := range checksum.Algorithms() {
		name, header := want[a][0], want[a][1]
		if a.String() != name || a.HeaderName() != header {
			t.Errorf("%v: String %q HeaderName %q, want %q %q", a, a.String(), a.HeaderName(), name, header)
		}
		if got, err := checksum.ParseAlgorithm(name); err != nil || got != a {
			t.Errorf("ParseAlgorithm(%q) = %v, %v", name, got, err)
		}
		if got, err := checksum.ParseHeaderName("X-Amz-Checksum-" + name); err != nil || got != a {
			t.Errorf("ParseHeaderName is case-insensitive: got %v, %v for %v", got, err, a)
		}
	}
}

func TestParseRejectsOtherNames(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "crc32", "CRC16", "SHA384", "x-amz-checksum-type", "x-amz-checksum-mode"} {
		var unknown *checksum.UnknownAlgorithmError
		if _, err := checksum.ParseAlgorithm(s); !errors.As(err, &unknown) {
			t.Errorf("ParseAlgorithm(%q) error = %v, want UnknownAlgorithmError", s, err)
		}
	}
	for _, s := range []string{"x-amz-checksum-type", "x-amz-checksum-mode", "x-amz-checksum-algorithm", "x-amz-checksum-"} {
		var unknown *checksum.UnknownAlgorithmError
		if _, err := checksum.ParseHeaderName(s); !errors.As(err, &unknown) || unknown.Value != s {
			t.Errorf("ParseHeaderName(%q) error = %v, want UnknownAlgorithmError for %q", s, err, s)
		}
	}
}

func TestNewRejectsUndeclaredAlgorithm(t *testing.T) {
	t.Parallel()
	var unknown *checksum.UnknownAlgorithmError
	if _, err := checksum.New(checksum.Algorithm(0)); !errors.As(err, &unknown) {
		t.Fatalf("New(0) error = %v, want UnknownAlgorithmError", err)
	}
}

// S3 answers InvalidRequest "Value for x-amz-checksum-<alg> header is invalid."
// when the value is not base64 of the right length, and BadDigest when it is
// well formed but wrong (s3.md 6.2).
func TestVerify(t *testing.T) {
	t.Parallel()
	computed := sumOf(t, checksum.CRC32, []byte("123456789"))
	if err := checksum.Verify("y/Q5Jg==", computed); err != nil {
		t.Fatalf("Verify of the matching value: %v", err)
	}

	var mismatch *checksum.MismatchError
	if err := checksum.Verify("AAAAAA==", computed); !errors.As(err, &mismatch) {
		t.Fatalf("Verify of another digest = %v, want MismatchError", err)
	}
	if mismatch.Declared.String() != "AAAAAA==" || mismatch.Computed != computed {
		t.Errorf("MismatchError = %+v", *mismatch)
	}

	// Only the canonical encoding is well formed: no line breaks, and no
	// non-zero padding bits ("y/Q5Jh==" decodes to the same digest as
	// "y/Q5Jg==" under a lenient decoder).
	for _, bad := range []string{
		"", "y/Q5Jg", "not base64!", "AAAAAAAAAAA=", "y/Q5Jg==y/Q5Jg==",
		"y/Q5Jh==", "y/Q5\nJg==", "\r\ny/Q5Jg==\r\n",
	} {
		var invalid *checksum.InvalidValueError
		if err := checksum.Verify(bad, computed); !errors.As(err, &invalid) {
			t.Errorf("Verify(%q) = %v, want InvalidValueError", bad, err)
		} else if invalid.Algorithm != checksum.CRC32 || invalid.Value != bad {
			t.Errorf("Verify(%q) InvalidValueError = %+v", bad, *invalid)
		}
	}

	// A zero Sum is the caller's mistake, not a fault in the received value.
	var unknown *checksum.UnknownAlgorithmError
	if err := checksum.Verify("y/Q5Jg==", checksum.Sum{}); !errors.As(err, &unknown) {
		t.Errorf("Verify against a zero Sum = %v, want UnknownAlgorithmError", err)
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		alg := rapid.SampledFrom(checksum.Algorithms()).Draw(t, "alg")
		data := rapid.SliceOf(rapid.Byte()).Draw(t, "data")
		h, err := checksum.New(alg)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = h.Write(data)
		s := h.Checksum()
		if len(s.Bytes()) != alg.Size() || h.Size() != alg.Size() {
			t.Fatalf("%v digest is %d bytes, Size() %d, hash.Size() %d", alg, len(s.Bytes()), alg.Size(), h.Size())
		}
		back, err := checksum.Decode(alg, s.String())
		if err != nil || back != s {
			t.Fatalf("Decode(%v, %q) = %v, %v; want the original sum", alg, s.String(), back, err)
		}
	})
}

// chunks draws a split of data into consecutive pieces of random sizes,
// biased towards the 64-byte stripe and 1024-byte block boundaries of XXH3.
func chunks(t *rapid.T, data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		n := rapid.OneOf(
			rapid.IntRange(0, 3),
			rapid.IntRange(60, 68),
			rapid.IntRange(1020, 1028),
			rapid.IntRange(0, len(data)),
		).Draw(t, "chunk")
		n = min(n, len(data))
		out = append(out, data[:n])
		data = data[n:]
	}
	return out
}

// bodies draws inputs that reach every XXH3 length class, including several
// 1024-byte blocks. Lengths are drawn directly and the bytes come from a
// seeded generator, because rapid keeps generated slices short.
func bodies() *rapid.Generator[[]byte] {
	return rapid.Custom(func(t *rapid.T) []byte {
		n := rapid.OneOf(rapid.IntRange(0, 300), rapid.IntRange(0, 5000)).Draw(t, "len")
		var seed [32]byte
		binary.LittleEndian.PutUint64(seed[:], rapid.Uint64().Draw(t, "seed"))
		b := make([]byte, n)
		if _, err := rand.NewChaCha8(seed).Read(b); err != nil {
			t.Fatal(err)
		}
		return b
	})
}

func TestStreamingEqualsOneShot(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		alg := rapid.SampledFrom(checksum.Algorithms()).Draw(t, "alg")
		data := bodies().Draw(t, "data")

		oneShot, err := checksum.New(alg)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = oneShot.Write(data)

		streaming, err := checksum.New(alg)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range chunks(t, data) {
			_, _ = streaming.Write(c)
			_ = streaming.Sum(nil) // Sum must not disturb the running state
		}
		if got, want := streaming.Checksum(), oneShot.Checksum(); got != want {
			t.Fatalf("%v over %d bytes: streaming %s, one-shot %s", alg, len(data), got, want)
		}

		streaming.Reset()
		_, _ = streaming.Write(data)
		if got, want := streaming.Checksum(), oneShot.Checksum(); got != want {
			t.Fatalf("%v after Reset: %s, want %s", alg, got, want)
		}
	})
}
