package checksum_test

import (
	"errors"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/checksum"
)

// Composite values: the checksum of the concatenated raw part digests with
// "-<parts>" appended. CRC32 of "xxxxx" and "yyy" is the computed example of
// sigv4-checksums-endpoints.md 4.4; the others are the two-part fixture of
// s3-checksum-residuals-and-record-backlog.md 1.4.3 (pat5242880, pat1048576).
func TestCompositeVectors(t *testing.T) {
	t.Parallel()
	if got := composite(t, checksum.CRC32, []byte("xxxxx"), []byte("yyy")); got != "rFFwCQ==-2" {
		t.Errorf("CRC32 composite = %s, want rFFwCQ==-2", got)
	}
	part1, part2 := pattern(5<<20), pattern(1<<20)
	for alg, want := range map[checksum.Algorithm]string{
		checksum.XXHASH64:  "9GhH8zktinM=-2",
		checksum.XXHASH3:   "f6EQWtiO3o8=-2",
		checksum.XXHASH128: "Ger1rESaX/PiNtq26MVKfQ==-2",
		checksum.SHA512:    "hbWfVq2dROxQPWBMcw1EZgLLOpNmP/zfonMOFFvvC1LaQbYma4kY9a3JmG7oMa9BgHj2grxxoS0u7FKeSlg7vQ==-2",
		checksum.MD5:       "TcMA1YYQbcbHRS4dun66LA==-2",
	} {
		if got := composite(t, alg, part1, part2); got != want {
			t.Errorf("%v composite = %s, want %s", alg, got, want)
		}
	}
}

func composite(t failer, alg checksum.Algorithm, parts ...[]byte) string {
	t.Helper()
	sums := make([]checksum.Sum, 0, len(parts))
	for _, p := range parts {
		sums = append(sums, sumOf(t, alg, p))
	}
	got, err := checksum.Composite(alg, sums)
	if err != nil {
		t.Fatalf("Composite(%v): %v", alg, err)
	}
	return got
}

func TestCompositeRejectsPartOfAnotherAlgorithm(t *testing.T) {
	t.Parallel()
	crc := sumOf(t, checksum.CRC32, []byte("a"))
	sha := sumOf(t, checksum.SHA256, []byte("a"))
	var partErr *checksum.PartAlgorithmError
	if _, err := checksum.Composite(checksum.CRC32, []checksum.Sum{crc, sha}); !errors.As(err, &partErr) {
		t.Fatalf("Composite error = %v, want PartAlgorithmError", err)
	}
	if partErr.Index != 1 || partErr.Want != checksum.CRC32 || partErr.Got != checksum.SHA256 {
		t.Errorf("PartAlgorithmError = %+v", *partErr)
	}
}

func TestCompositeRejectsNoParts(t *testing.T) {
	t.Parallel()
	for _, alg := range checksum.Algorithms() {
		var noParts *checksum.NoPartsError
		if got, err := checksum.Composite(alg, nil); !errors.As(err, &noParts) {
			t.Errorf("Composite(%v, nil) = %q, %v; want NoPartsError", alg, got, err)
		}
	}
}

// Composite depends only on the part digests: it is the algorithm over their
// concatenation, whatever chunking produced each part's checksum.
func TestCompositeIsStable(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		alg := rapid.SampledFrom(checksum.Algorithms()).Draw(t, "alg")
		parts := rapid.SliceOfN(bodies(), 1, 5).Draw(t, "parts")

		streamed := make([]checksum.Sum, 0, len(parts))
		concat := make([]byte, 0, alg.Size()*len(parts))
		for _, p := range parts {
			h, err := checksum.New(alg)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range chunks(t, p) {
				_, _ = h.Write(c)
			}
			streamed = append(streamed, h.Checksum())
			concat = append(concat, h.Checksum().Bytes()...)
		}
		got, err := checksum.Composite(alg, streamed)
		if err != nil {
			t.Fatal(err)
		}
		if again := composite(t, alg, parts...); got != again {
			t.Fatalf("Composite differs between chunkings: %s vs %s", got, again)
		}
		h, err := checksum.New(alg)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = h.Write(concat)
		if want := h.Checksum().String() + "-" + strconv.Itoa(len(parts)); got != want {
			t.Fatalf("Composite = %s, want %s", got, want)
		}
	})
}

// A FULL_OBJECT multipart checksum is the CRC of the whole object, which S3
// derives from the part CRCs and sizes (go-toolchain.md 7.4).
func TestCombineEqualsWholeBody(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		alg := rapid.SampledFrom([]checksum.Algorithm{checksum.CRC32, checksum.CRC32C, checksum.CRC64NVME}).Draw(t, "alg")
		data := bodies().Draw(t, "data")

		pieces := chunks(t, data)
		parts := make([]checksum.Part, 0, len(pieces))
		for _, p := range pieces {
			parts = append(parts, checksum.Part{Sum: sumOf(t, alg, p), Size: int64(len(p))})
		}
		got, err := checksum.Combine(alg, parts)
		if err != nil {
			t.Fatal(err)
		}
		if want := sumOf(t, alg, data); got != want {
			t.Fatalf("%v Combine of %d parts = %s, whole body %s", alg, len(parts), got, want)
		}
	})
}

func TestCombineRejects(t *testing.T) {
	t.Parallel()
	crc := sumOf(t, checksum.CRC32, []byte("abc"))

	for _, alg := range checksum.Algorithms() {
		_, err := checksum.Combine(alg, nil)
		var notCombinable *checksum.NotCombinableError
		if alg.Combinable() != (err == nil) || (err != nil && (!errors.As(err, &notCombinable) || notCombinable.Algorithm != alg)) {
			t.Errorf("Combine(%v) error = %v; Combinable() = %v", alg, err, alg.Combinable())
		}
	}

	var partAlg *checksum.PartAlgorithmError
	if _, err := checksum.Combine(checksum.CRC32C, []checksum.Part{{Sum: crc, Size: 3}}); !errors.As(err, &partAlg) {
		t.Errorf("Combine of a CRC32 part into CRC32C = %v, want PartAlgorithmError", err)
	} else if partAlg.Index != 0 || partAlg.Want != checksum.CRC32C || partAlg.Got != checksum.CRC32 {
		t.Errorf("PartAlgorithmError = %+v", *partAlg)
	}
	var partSize *checksum.PartSizeError
	if _, err := checksum.Combine(checksum.CRC32, []checksum.Part{{Sum: crc, Size: -1}}); !errors.As(err, &partSize) {
		t.Errorf("Combine of a negative size = %v, want PartSizeError", err)
	} else if partSize.Index != 0 || partSize.Size != -1 {
		t.Errorf("PartSizeError = %+v", *partSize)
	}
}
