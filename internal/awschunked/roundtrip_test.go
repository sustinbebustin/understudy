package awschunked_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/awschunked"
)

// chainSign is a stand-in for SigV4 chunk signing: any function of the
// previous signature and the payload exercises the chain the same way.
func chainSign(prev string, p awschunked.SignedPayload) string {
	sum := sha256.Sum256([]byte(prev + strconv.Itoa(int(p.Kind)) + hex.EncodeToString(p.SHA256[:])))
	return hex.EncodeToString(sum[:])
}

func chainVerify(prev string, p awschunked.SignedPayload, sig string) error {
	if chainSign(prev, p) != sig {
		return errBadSignature
	}
	return nil
}

const (
	seed = "4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9"
	// minChunk is the S3 user guide's least size for a chunk other than the
	// last, in every mode.
	minChunk = 8192
)

// encoded is a body framed by a Writer, with what a Reader needs to decode it.
type encoded struct {
	mode    awschunked.Mode
	body    []byte
	trailer *awschunked.Trailer
	wire    []byte
}

func (e encoded) options(t *rapid.T) awschunked.Options {
	l, err := awschunked.ParseLength(strconv.Itoa(len(e.body)))
	if err != nil {
		t.Fatal(err)
	}
	opts := awschunked.Options{DecodedLength: l, Seed: seed, OnChunkSignature: chainVerify}
	if e.trailer != nil {
		opts.Trailer = e.trailer.Name
	}
	return opts
}

var (
	allModes    = []awschunked.Mode{awschunked.PlainChunked, awschunked.UnsignedTrailer, awschunked.Signed, awschunked.SignedTrailer}
	signedModes = []awschunked.Mode{awschunked.Signed, awschunked.SignedTrailer}
)

// drawEncoded draws a body framed in one of modes.
func drawEncoded(t *rapid.T, modes []awschunked.Mode) encoded {
	mode := rapid.SampledFrom(modes).Draw(t, "mode")
	body := drawBody(t)
	e := encoded{mode: mode, body: body}
	if mode == awschunked.UnsignedTrailer || mode == awschunked.SignedTrailer {
		e.trailer = &awschunked.Trailer{
			Name:  rapid.SampledFrom([]string{"x-amz-checksum-crc32", "x-amz-checksum-sha256", "X-Amz-Checksum-Xxhash128"}).Draw(t, "name"),
			Value: rapid.StringMatching(`[A-Za-z0-9+/]{0,86}={0,2}`).Draw(t, "value"),
		}
	}
	ending := rapid.SampledFrom([]awschunked.LineEnding{awschunked.CRLF, awschunked.LFCRLF}).Draw(t, "ending")

	var wire bytes.Buffer
	w := awschunked.NewWriter(&wire, mode, awschunked.WriterOptions{Seed: seed, Sign: chainSign, TrailerEnding: ending})
	// Chunks other than the last hold at least minChunk bytes.
	rest := body
	for len(rest) > 0 {
		lo := min(minChunk, len(rest))
		n := min(rapid.OneOf(rapid.IntRange(lo, lo+16), rapid.IntRange(lo, len(rest))).Draw(t, "chunk"), len(rest))
		if _, err := w.Write(rest[:n]); err != nil {
			t.Fatal(err)
		}
		rest = rest[n:]
	}
	var trailers []awschunked.Trailer
	if e.trailer != nil {
		trailers = append(trailers, *e.trailer)
	}
	if err := w.Finish(trailers...); err != nil {
		t.Fatal(err)
	}
	e.wire = wire.Bytes()
	return e
}

// drawBody draws a body of up to 20000 bytes, past the Reader's 4 KiB line
// buffer. The length is drawn directly and the bytes come from a seeded
// generator, because rapid keeps generated slices short.
func drawBody(t *rapid.T) []byte {
	n := rapid.OneOf(rapid.IntRange(0, 64), rapid.IntRange(0, 20000)).Draw(t, "len")
	var key [32]byte
	binary.LittleEndian.PutUint64(key[:], rapid.Uint64().Draw(t, "seed"))
	b := make([]byte, n)
	if _, err := rand.NewChaCha8(key).Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// readSmall drains r through reads of random small sizes, so chunk and line
// boundaries fall anywhere inside a Read.
func readSmall(t *rapid.T, r io.Reader) ([]byte, error) {
	var out []byte
	buf := make([]byte, 64)
	for {
		n, err := r.Read(buf[:rapid.IntRange(1, len(buf)).Draw(t, "read")])
		out = append(out, buf[:n]...)
		if err != nil {
			return out, err
		}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		e := drawEncoded(t, allModes)
		opts := e.options(t)
		if !rapid.Bool().Draw(t, "verify") {
			opts.OnChunkSignature = nil
		}
		r := awschunked.NewReader(bytes.NewReader(e.wire), e.mode, opts)
		got, err := readSmall(t, r)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("%v: read error %v", e.mode, err)
		}
		if !bytes.Equal(got, e.body) {
			t.Fatalf("%v: decoded %d bytes, want %d", e.mode, len(got), len(e.body))
		}
		trailers, ok := r.Trailers()
		if !ok {
			t.Fatal("Trailers() not ready after EOF")
		}
		switch {
		case e.trailer == nil && len(trailers) != 0:
			t.Fatalf("%v: unexpected trailers %v", e.mode, trailers)
		case e.trailer != nil && (len(trailers) != 1 || trailers.Get(e.trailer.Name) != e.trailer.Value):
			t.Fatalf("%v: trailers %v, want %s: %q", e.mode, trailers, e.trailer.Name, e.trailer.Value)
		}
	})
}

// A body cut anywhere reports IncompleteBody with the declared length and the
// bytes actually decoded, or, once every declared byte has arrived,
// MalformedChunk Truncated. The source may end with io.EOF or, as a net/http
// body shorter than its Content-Length does, with io.ErrUnexpectedEOF.
func TestTruncatedBody(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		e := drawEncoded(t, allModes)
		cut := rapid.IntRange(0, len(e.wire)-1).Draw(t, "cut")
		end := rapid.SampledFrom([]error{io.EOF, io.ErrUnexpectedEOF}).Draw(t, "end")
		src := io.MultiReader(bytes.NewReader(e.wire[:cut]), errReader{end})
		r := awschunked.NewReader(src, e.mode, e.options(t))
		got, err := readSmall(t, r)
		if !bytes.Equal(got, e.body[:len(got)]) {
			t.Fatalf("decoded bytes are not a prefix of the body")
		}
		if len(got) < len(e.body) {
			want := awschunked.IncompleteBodyError{Expected: int64(len(e.body)), Got: int64(len(got))}
			var incomplete *awschunked.IncompleteBodyError
			if !errors.As(err, &incomplete) || *incomplete != want {
				t.Fatalf("cut at %d of %d: error %v, want %+v", cut, len(e.wire), err, want)
			}
			return
		}
		var malformed *awschunked.MalformedChunkError
		if !errors.As(err, &malformed) || malformed.Defect != awschunked.Truncated {
			t.Fatalf("cut at %d of %d after all data: error %v, want MalformedChunkError Truncated", cut, len(e.wire), err)
		}
		if _, ok := r.Trailers(); ok {
			t.Fatal("Trailers() ready after a failed read")
		}
	})
}

// Without a declared length, a cut inside a chunk still reports the bytes
// that chunk header announced.
func TestTruncatedChunkWithoutDeclaredLength(t *testing.T) {
	t.Parallel()
	wire := "2000\r\n" + string(bytes.Repeat([]byte{'a'}, minChunk)) + "\r\n5\r\nde"
	r := awschunked.NewReader(bytes.NewReader([]byte(wire)), awschunked.PlainChunked, awschunked.Options{})
	_, err := io.ReadAll(r)
	want := awschunked.IncompleteBodyError{Expected: minChunk + 5, Got: minChunk + 2}
	var got *awschunked.IncompleteBodyError
	if !errors.As(err, &got) || *got != want {
		t.Fatalf("error %v, want %+v", err, want)
	}
}

// The trailer signature covers the trailer field, so changing any byte of the
// field or of the signature itself fails the verifier.
func TestTrailerSignatureIsChecked(t *testing.T) {
	t.Parallel()
	const (
		fieldBytes = "ABCXYZabcxyz0189+/"
		hexDigits  = "0123456789abcdef"
	)
	rapid.Check(t, func(t *rapid.T) {
		e := drawEncoded(t, []awschunked.Mode{awschunked.SignedTrailer})
		wire := bytes.Clone(e.wire)
		var at int
		alphabet := hexDigits
		if rapid.Bool().Draw(t, "alter signature") {
			sigLine := []byte("x-amz-trailer-signature:")
			at = bytes.LastIndex(wire, sigLine) + len(sigLine) + rapid.IntRange(0, 63).Draw(t, "at")
		} else {
			// Any byte of "<name>:<value>" except the colon, which would make
			// the line malformed rather than differently signed.
			field := e.trailer.Name + ":" + e.trailer.Value
			i := rapid.IntRange(0, len(field)-2).Draw(t, "at")
			if i >= len(e.trailer.Name) {
				i++
			}
			at = bytes.LastIndex(wire, []byte(field)) + i
			alphabet = fieldBytes
		}
		others := bytes.ReplaceAll([]byte(alphabet), []byte{wire[at]}, nil)
		wire[at] = rapid.SampledFrom(others).Draw(t, "byte")

		_, err := readSmall(t, awschunked.NewReader(bytes.NewReader(wire), e.mode, e.options(t)))
		if !errors.Is(err, errBadSignature) {
			t.Fatalf("byte %d of %d altered: error %v, want the verifier's error", at, len(wire), err)
		}
	})
}

// In every mode a chunk under 8,192 bytes is refused unless it is the last
// data chunk (sigv4-checksums-endpoints.md 3.2, 3.3).
func TestChunkMinimum(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		mode := rapid.SampledFrom(allModes).Draw(t, "mode")
		first := rapid.OneOf(
			rapid.IntRange(minChunk-16, minChunk+16),
			rapid.IntRange(1, 2*minChunk),
		).Draw(t, "first")
		second := rapid.IntRange(1, 64).Draw(t, "second")
		body := bytes.Repeat([]byte{'a'}, first+second)

		var wire bytes.Buffer
		w := awschunked.NewWriter(&wire, mode, awschunked.WriterOptions{Seed: seed, Sign: chainSign})
		for _, p := range [][]byte{body[:first], body[first:]} {
			if _, err := w.Write(p); err != nil {
				t.Fatal(err)
			}
		}
		e := encoded{mode: mode, body: body}
		var trailers []awschunked.Trailer
		if mode == awschunked.UnsignedTrailer || mode == awschunked.SignedTrailer {
			e.trailer = &awschunked.Trailer{Name: "x-amz-checksum-crc32", Value: "AAAAAA=="}
			trailers = append(trailers, *e.trailer)
		}
		if err := w.Finish(trailers...); err != nil {
			t.Fatal(err)
		}

		got, err := readSmall(t, awschunked.NewReader(&wire, mode, e.options(t)))
		if first >= minChunk {
			if !errors.Is(err, io.EOF) || !bytes.Equal(got, body) {
				t.Fatalf("%v chunks %d, %d: read %d bytes, error %v; want the body and io.EOF", mode, first, second, len(got), err)
			}
			return
		}
		want := awschunked.ChunkTooSmallError{Chunk: 1, Size: int64(first)}
		var small *awschunked.ChunkTooSmallError
		if !errors.As(err, &small) || *small != want {
			t.Fatalf("%v chunks %d, %d: error %v, want %+v", mode, first, second, err, want)
		}
		if len(got) != first {
			t.Fatalf("%v chunks %d, %d: read %d bytes before the error, want the first chunk only", mode, first, second, len(got))
		}
	})
}

func TestSignatureChainIsChecked(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		e := drawEncoded(t, signedModes)
		opts := e.options(t)
		opts.Seed = "0" + seed[1:]
		_, err := readSmall(t, awschunked.NewReader(bytes.NewReader(e.wire), e.mode, opts))
		if !errors.Is(err, errBadSignature) {
			t.Fatalf("%v with another seed: error %v, want the verifier's error", e.mode, err)
		}
	})
}
