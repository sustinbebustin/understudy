package awschunked_test

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awschunked"
	"github.com/sustinbebustin/understudy/internal/checksum"
)

func length(t *testing.T, n int) awschunked.Length {
	t.Helper()
	l, err := awschunked.ParseLength(strconv.Itoa(n))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The bytes aws-sdk-go-v2 sends over HTTPS for a 1000-byte PutObject with the
// default CRC32 trailer: one chunk for the whole payload
// (sigv4-checksums-endpoints.md 3.2).
func TestUnsignedTrailerFromGoSDK(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("a", 1000)
	wire := "3e8\r\n" + payload + "\r\n0\r\nx-amz-checksum-crc32:mjjaAw==\r\n\r\n"
	r := awschunked.NewReader(strings.NewReader(wire), awschunked.UnsignedTrailer, awschunked.Options{
		DecodedLength: length(t, 1000),
		Trailer:       "x-amz-checksum-crc32",
	})
	if _, ok := r.Trailers(); ok {
		t.Fatal("Trailers() reported ready before the body was read")
	}
	h, err := checksum.New(checksum.CRC32)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(io.TeeReader(r, h))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != payload {
		t.Fatalf("decoded %d bytes, want the 1000-byte payload", len(body))
	}
	trailers, ok := r.Trailers()
	if !ok {
		t.Fatal("Trailers() not ready after EOF")
	}
	if err := checksum.Verify(trailers.Get("x-amz-checksum-crc32"), h.Checksum()); err != nil {
		t.Fatalf("trailer checksum: %v", err)
	}
}

// botocore sends 1 MiB chunks over HTTP chunked transfer; the S3 user guide
// shows the trailer value followed by "\n\r\n" and says the extra linefeed
// varies by client (sigv4-checksums-endpoints.md 3.2).
func TestTrailerLineEndings(t *testing.T) {
	t.Parallel()
	for _, ending := range []string{"\r\n", "\n\r\n"} {
		wire := "3\r\nabc\r\n0\r\nx-amz-checksum-crc32c:" + "Nks/tw==" + ending + "\r\n"
		r := awschunked.NewReader(strings.NewReader(wire), awschunked.UnsignedTrailer, awschunked.Options{
			DecodedLength: length(t, 3),
			Trailer:       "x-amz-checksum-crc32c",
		})
		body, err := io.ReadAll(r)
		if err != nil || string(body) != "abc" {
			t.Fatalf("ending %q: body %q, err %v", ending, body, err)
		}
		trailers, _ := r.Trailers()
		if got := trailers.Get("X-Amz-Checksum-Crc32c"); got != "Nks/tw==" {
			t.Errorf("ending %q: trailer value %q", ending, got)
		}
	}
}

// sigv4ChunkVerifier checks chunk and trailer signatures the way S3 does
// (sigv4-checksums-endpoints.md 3.3) for the documented example credentials.
func sigv4ChunkVerifier() awschunked.SignatureFunc {
	const (
		secret    = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" //nolint:gosec // G101: AWS's published example key, not a credential
		timestamp = "20130524T000000Z"
		scope     = "20130524/us-east-1/s3/aws4_request"
	)
	mac := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(data))
		return h.Sum(nil)
	}
	key := mac(mac(mac(mac([]byte("AWS4"+secret), "20130524"), "us-east-1"), "s3"), "aws4_request")
	emptyHash := sha256.Sum256(nil)
	return func(prev string, signed awschunked.SignedPayload, sig string) error {
		var toSign string
		switch signed.Kind {
		case awschunked.ChunkSigned:
			toSign = "AWS4-HMAC-SHA256-PAYLOAD\n" + timestamp + "\n" + scope + "\n" + prev + "\n" +
				hex.EncodeToString(emptyHash[:]) + "\n" + hex.EncodeToString(signed.SHA256[:])
		case awschunked.TrailerSigned:
			toSign = "AWS4-HMAC-SHA256-TRAILER\n" + timestamp + "\n" + scope + "\n" + prev + "\n" +
				hex.EncodeToString(signed.SHA256[:])
		}
		if want := hex.EncodeToString(mac(key, toSign)); want != sig {
			return errBadSignature
		}
		return nil
	}
}

var errBadSignature = errors.New("signature does not match")

// The worked examples of the S3 streaming documentation: 66560 bytes of 'a'
// in a 65536-byte chunk and a 1024-byte chunk, with and without the signed
// CRC32C trailer (sigv4-checksums-endpoints.md 1.2, published vectors).
func TestSignedStreamingDocumentedExample(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("a", 66560)
	cases := []struct {
		mode    awschunked.Mode
		seed    string
		chunks  [3]string
		trailer string
	}{
		{
			mode: awschunked.Signed,
			seed: "4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9",
			chunks: [3]string{
				"ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648",
				"0055627c9e194cb4542bae2aa5492e3c1575bbb81b612b7d234b86a503ef5497",
				"b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9",
			},
			trailer: "\r\n",
		},
		{
			mode: awschunked.SignedTrailer,
			seed: "106e2a8a18243abcf37539882f36619c00e2dfc72633413f02d3b74544bfeb8e",
			chunks: [3]string{
				"b474d8862b1487a5145d686f57f013e54db672cee1c953b3010fb58501ef5aa2",
				"1c1344b170168f8e65b41376b44b20fe354e373826ccbbe2c1d40a8cae51e5c7",
				"2ca2aba2005185cf7159c6277faf83795951dd77a3a99e6e65d5c9f85863f992",
			},
			trailer: "x-amz-checksum-crc32c:sOO8/Q==\n\r\n" +
				"x-amz-trailer-signature:d81f82fc3505edab99d459891051a732e8730629a2e4a59689829ca17fe2e435\r\n\r\n",
		},
	}
	for _, c := range cases {
		wire := "10000;chunk-signature=" + c.chunks[0] + "\r\n" + payload[:65536] + "\r\n" +
			"400;chunk-signature=" + c.chunks[1] + "\r\n" + payload[65536:] + "\r\n" +
			"0;chunk-signature=" + c.chunks[2] + "\r\n" + c.trailer
		opts := awschunked.Options{
			DecodedLength:    length(t, len(payload)),
			Seed:             c.seed,
			OnChunkSignature: sigv4ChunkVerifier(),
		}
		if c.mode == awschunked.SignedTrailer {
			opts.Trailer = "x-amz-checksum-crc32c"
		}
		body, err := io.ReadAll(awschunked.NewReader(strings.NewReader(wire), c.mode, opts))
		if err != nil {
			t.Fatalf("%v: %v", c.mode, err)
		}
		if string(body) != payload {
			t.Fatalf("%v: decoded %d bytes", c.mode, len(body))
		}

		tampered := map[string]string{"a modified chunk": strings.Replace(wire, payload[:10], "b"+payload[:9], 1)}
		if c.mode == awschunked.SignedTrailer {
			// The trailer signature covers the trailer field and chains from the
			// final chunk's signature, so swapping in another well-formed checksum
			// or presenting another signature both fail.
			tampered["a modified trailer value"] = strings.Replace(wire, "sOO8/Q==", "AAAAAA==", 1)
			tampered["a modified trailer signature"] = strings.Replace(wire, "signature:d81f82fc", "signature:e81f82fc", 1)
		}
		for what, wire := range tampered {
			_, err = io.ReadAll(awschunked.NewReader(strings.NewReader(wire), c.mode, opts))
			if !errors.Is(err, errBadSignature) {
				t.Errorf("%v: %s gave %v, want the verifier's error", c.mode, what, err)
			}
		}
	}
}

func TestDecodedLengthMismatch(t *testing.T) {
	t.Parallel()
	read := func(wire string, declared int) error {
		r := awschunked.NewReader(strings.NewReader(wire), awschunked.PlainChunked, awschunked.Options{DecodedLength: length(t, declared)})
		_, err := io.ReadAll(r)
		return err
	}

	err := read("3\r\nabc\r\n0\r\n\r\n", 5)
	wantShort := awschunked.IncompleteBodyError{Expected: 5, Got: 3}
	var short *awschunked.IncompleteBodyError
	if !errors.As(err, &short) || *short != wantShort {
		t.Errorf("declared 5, sent 3: error %v, want %+v", err, wantShort)
	}

	// The chunk header alone exceeds the declared length, so the error
	// reports what the header announced, not bytes received.
	for _, c := range []struct {
		wire     string
		declared int
		want     awschunked.DecodedLengthExceededError
	}{
		{"3\r\nabc\r\n0\r\n\r\n", 2, awschunked.DecodedLengthExceededError{Declared: 2, Announced: 3}},
		{"ffff\r\nabc", 3, awschunked.DecodedLengthExceededError{Declared: 3, Announced: 0xffff}},
		{"2\r\nab\r\n3\r\ncde\r\n0\r\n\r\n", 4, awschunked.DecodedLengthExceededError{Declared: 4, Announced: 5}},
	} {
		var got *awschunked.DecodedLengthExceededError
		if err := read(c.wire, c.declared); !errors.As(err, &got) || *got != c.want {
			t.Errorf("%q declared %d: error %v, want %+v", c.wire, c.declared, err, c.want)
		}
	}
}

func TestUndeclaredLengthIsAccepted(t *testing.T) {
	t.Parallel()
	r := awschunked.NewReader(strings.NewReader("3\r\nabc\r\n0\r\n\r\n"), awschunked.PlainChunked, awschunked.Options{})
	body, err := io.ReadAll(r)
	if err != nil || string(body) != "abc" {
		t.Fatalf("body %q, err %v", body, err)
	}
	trailers, ok := r.Trailers()
	if !ok || len(trailers) != 0 {
		t.Errorf("Trailers() = %v, %v; want empty and ready", trailers, ok)
	}
}

// S3: "if a request contains x-amz-trailer: x-amz-checksum-crc32 and the
// trailer chunk has the header name x-amz-checksum-sha1, the request fails"
// (s3-checksum-residuals-and-record-backlog.md 1.6).
func TestTrailerDeclarationIsEnforced(t *testing.T) {
	t.Parallel()
	read := func(mode awschunked.Mode, declared, wire string) error {
		r := awschunked.NewReader(strings.NewReader(wire), mode, awschunked.Options{Trailer: declared})
		_, err := io.ReadAll(r)
		return err
	}

	var mismatch *awschunked.TrailerMismatchError
	err := read(awschunked.UnsignedTrailer, "x-amz-checksum-crc32", "1\r\na\r\n0\r\nx-amz-checksum-sha1:hvfkN/qlp/zhXR3cuerq6jd2Z7g=\r\n\r\n")
	if !errors.As(err, &mismatch) || mismatch.Declared != "x-amz-checksum-crc32" || mismatch.Got != "x-amz-checksum-sha1" {
		t.Errorf("other trailer name: %v", err)
	}
	err = read(awschunked.PlainChunked, "", "1\r\na\r\n0\r\nx-amz-checksum-crc32:6Le+Qw==\r\n\r\n")
	if !errors.As(err, &mismatch) || mismatch.Declared != "" || mismatch.Got != "x-amz-checksum-crc32" {
		t.Errorf("trailer in a mode without trailers: %v, want TrailerMismatchError", err)
	}

	var missing *awschunked.MissingTrailerError
	if err := read(awschunked.UnsignedTrailer, "x-amz-checksum-crc32", "1\r\na\r\n0\r\n\r\n"); !errors.As(err, &missing) || missing.Name != "x-amz-checksum-crc32" {
		t.Errorf("absent trailer: %v, want MissingTrailerError", err)
	}

	var malformed *awschunked.MalformedChunkError
	field := "x-amz-checksum-crc32:6Le+Qw==\r\n"
	err = read(awschunked.UnsignedTrailer, "x-amz-checksum-crc32", "1\r\na\r\n0\r\n"+field+field+"\r\n")
	if !errors.As(err, &malformed) || malformed.Defect != awschunked.ExtraTrailer {
		t.Errorf("second trailer: %v, want MalformedChunkError ExtraTrailer", err)
	}
}

// A body that never closes its trailer section fails at the first line its
// mode has no room for, not when the input runs out: the Reader holds at most
// two trailer lines. The source carries 1 MiB of trailer lines and then ends,
// so a Reader that buffered past the bound would fail with Truncated instead.
func TestEndlessTrailerSectionFailsAtFirstExcessLine(t *testing.T) {
	t.Parallel()
	const field = "x-amz-checksum-crc32:AAAAAA==\r\n"
	signedZero := "0;chunk-signature=" + strings.Repeat("0", 64) + "\r\n"
	sigLine := "x-amz-trailer-signature:" + strings.Repeat("0", 64) + "\r\n"
	cases := []struct {
		name  string
		mode  awschunked.Mode
		head  string // the bytes before the repeated field line
		check func(error) bool
		want  string
	}{
		{
			"unsigned trailer, second field", awschunked.UnsignedTrailer, "0\r\n",
			isDefectAt(awschunked.ExtraTrailer, int64(len("0\r\n")+2*len(field))),
			"ExtraTrailer at the end of the second field line",
		},
		{
			"signed trailer, second field", awschunked.SignedTrailer, signedZero,
			isDefectAt(awschunked.ExtraTrailer, int64(len(signedZero)+2*len(field))),
			"ExtraTrailer at the end of the second field line",
		},
		{
			"signed trailer, field after the signature", awschunked.SignedTrailer, signedZero + field + sigLine,
			isDefectAt(awschunked.MissingTrailerSignature, int64(len(signedZero)+2*len(field)+len(sigLine))),
			"MissingTrailerSignature at the end of the line after the signature",
		},
		{"no trailer mode, first field", awschunked.PlainChunked, "0\r\n", func(err error) bool {
			var mismatch *awschunked.TrailerMismatchError
			return errors.As(err, &mismatch) && *mismatch == awschunked.TrailerMismatchError{Got: "x-amz-checksum-crc32"}
		}, "TrailerMismatchError for the first field"},
	}
	for _, c := range cases {
		src := io.LimitReader(io.MultiReader(strings.NewReader(c.head), &repeatReader{s: field}), 1<<20)
		opts := awschunked.Options{}
		if c.mode == awschunked.UnsignedTrailer || c.mode == awschunked.SignedTrailer {
			opts.Trailer = "x-amz-checksum-crc32"
		}
		_, err := io.ReadAll(awschunked.NewReader(src, c.mode, opts))
		if !c.check(err) {
			t.Errorf("%s: error %v, want %s", c.name, err, c.want)
		}
	}
}

// repeatReader yields s over and over without end.
type repeatReader struct {
	s   string
	off int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		c := copy(p[n:], r.s[r.off:])
		n += c
		r.off = (r.off + c) % len(r.s)
	}
	return n, nil
}

func isDefectAt(d awschunked.Defect, offset int64) func(error) bool {
	return func(err error) bool {
		var malformed *awschunked.MalformedChunkError
		return errors.As(err, &malformed) && *malformed == awschunked.MalformedChunkError{Offset: offset, Defect: d}
	}
}

func TestMalformedFraming(t *testing.T) {
	t.Parallel()
	sig := strings.Repeat("0", 64)
	cases := []struct {
		name string
		mode awschunked.Mode
		wire string
		want awschunked.Defect
	}{
		{"size is not hex", awschunked.PlainChunked, "xyz\r\nabc\r\n0\r\n\r\n", awschunked.BadSizeLine},
		{"size has a sign", awschunked.PlainChunked, "+3\r\nabc\r\n0\r\n\r\n", awschunked.BadSizeLine},
		{"size overflows", awschunked.PlainChunked, "10000000000000000\r\n", awschunked.BadSizeLine},
		{"signature in an unsigned mode", awschunked.UnsignedTrailer, "3;chunk-signature=" + sig + "\r\nabc\r\n", awschunked.BadSizeLine},
		{"signature missing in a signed mode", awschunked.Signed, "3\r\nabc\r\n", awschunked.BadSizeLine},
		{"signature is not 64 hex", awschunked.Signed, "3;chunk-signature=abc\r\nabc\r\n", awschunked.BadSizeLine},
		{"size line ends in bare LF", awschunked.PlainChunked, "3\nabc\r\n0\r\n\r\n", awschunked.BadLineEnding},
		{"data not followed by CRLF", awschunked.PlainChunked, "3\r\nabcd\r\n0\r\n\r\n", awschunked.BadChunkTerminator},
		{"trailer without colon", awschunked.UnsignedTrailer, "0\r\nx-amz-checksum-crc32\r\n\r\n", awschunked.BadTrailerLine},
		{"trailer signature missing", awschunked.SignedTrailer, "0;chunk-signature=" + sig + "\r\nx-amz-checksum-crc32:AAAAAA==\r\n\r\n", awschunked.MissingTrailerSignature},
		{"trailer signature is 63 hex", awschunked.SignedTrailer, signedTrailerWire(sig, strings.Repeat("0", 63)), awschunked.MissingTrailerSignature},
		{"trailer signature is upper-case hex", awschunked.SignedTrailer, signedTrailerWire(sig, strings.Repeat("A", 64)), awschunked.MissingTrailerSignature},
		{"trailer signature is not hex", awschunked.SignedTrailer, signedTrailerWire(sig, strings.Repeat("g", 64)), awschunked.MissingTrailerSignature},
		{"bytes after the terminator", awschunked.PlainChunked, "0\r\n\r\nextra", awschunked.TrailingData},
		{"line longer than any valid line", awschunked.PlainChunked, strings.Repeat("0", 70000) + "\r\n", awschunked.LineTooLong},
	}
	for _, c := range cases {
		opts := awschunked.Options{}
		if c.mode == awschunked.UnsignedTrailer || c.mode == awschunked.SignedTrailer {
			opts.Trailer = "x-amz-checksum-crc32"
		}
		_, err := io.ReadAll(awschunked.NewReader(strings.NewReader(c.wire), c.mode, opts))
		var malformed *awschunked.MalformedChunkError
		if !errors.As(err, &malformed) || malformed.Defect != c.want {
			t.Errorf("%s: error %v, want MalformedChunkError %v", c.name, err, c.want)
		}
	}
}

// signedTrailerWire is an empty SignedTrailer body whose final chunk carries
// chunkSig and whose x-amz-trailer-signature line carries trailerSig.
func signedTrailerWire(chunkSig, trailerSig string) string {
	return "0;chunk-signature=" + chunkSig + "\r\nx-amz-checksum-crc32:AAAAAA==\r\nx-amz-trailer-signature:" + trailerSig + "\r\n\r\n"
}

// S3 refuses a chunk under 8,192 bytes before the last in signed and unsigned
// bodies alike, naming the index of the chunk after the short one and the
// short one's size: chunks of 3 then 2 bytes answer <Chunk>2</Chunk>
// <BadChunkSize>3</BadChunkSize>
// (testdata/parity/s3/R08/03-trailer-small-nonfinal-chunk.json).
func TestChunkTooSmall(t *testing.T) {
	t.Parallel()
	sig := ";chunk-signature=" + strings.Repeat("0", 64)
	full := strings.Repeat("a", 8192)
	cases := []struct {
		name string
		mode awschunked.Mode
		wire string
		want awschunked.ChunkTooSmallError
	}{
		{"recorded", awschunked.UnsignedTrailer, "3\r\nhel\r\n2\r\nlo\r\n0\r\nx-amz-checksum-crc32:NhCmhg==\r\n\r\n", awschunked.ChunkTooSmallError{Chunk: 2, Size: 3}},
		{"signed", awschunked.Signed, "3" + sig + "\r\nabc\r\n3" + sig + "\r\nabc\r\n0" + sig + "\r\n\r\n", awschunked.ChunkTooSmallError{Chunk: 2, Size: 3}},
		{"unsigned trailer", awschunked.UnsignedTrailer, "8\r\nabcdefgh\r\n8\r\nabcdefgh\r\n0\r\nx-amz-checksum-crc32:AAAAAA==\r\n\r\n", awschunked.ChunkTooSmallError{Chunk: 2, Size: 8}},
		{"plain chunked, second chunk", awschunked.PlainChunked, "2000\r\n" + full + "\r\n1\r\na\r\n1\r\nb\r\n0\r\n\r\n", awschunked.ChunkTooSmallError{Chunk: 3, Size: 1}},
	}
	for _, c := range cases {
		opts := awschunked.Options{}
		if c.mode == awschunked.UnsignedTrailer {
			opts.Trailer = "x-amz-checksum-crc32"
		}
		_, err := io.ReadAll(awschunked.NewReader(strings.NewReader(c.wire), c.mode, opts))
		var small *awschunked.ChunkTooSmallError
		if !errors.As(err, &small) || *small != c.want {
			t.Errorf("%s: error %v, want %+v", c.name, err, c.want)
		}
	}
}

// The line bound holds when the caller's source is itself a *bufio.Reader
// with a larger buffer.
func TestLineBoundIgnoresCallerBuffer(t *testing.T) {
	t.Parallel()
	src := bufio.NewReaderSize(strings.NewReader(strings.Repeat("0", 70000)+"\r\n"), 1<<20)
	_, err := io.ReadAll(awschunked.NewReader(src, awschunked.PlainChunked, awschunked.Options{}))
	var malformed *awschunked.MalformedChunkError
	if !errors.As(err, &malformed) || malformed.Defect != awschunked.LineTooLong {
		t.Fatalf("error %v, want MalformedChunkError LineTooLong", err)
	}
}

func TestMaxChunk(t *testing.T) {
	t.Parallel()
	r := awschunked.NewReader(strings.NewReader("5\r\nabcde\r\n0\r\n\r\n"), awschunked.PlainChunked, awschunked.Options{MaxChunk: 4})
	_, err := io.ReadAll(r)
	var malformed *awschunked.MalformedChunkError
	if !errors.As(err, &malformed) || malformed.Defect != awschunked.ChunkTooLarge {
		t.Fatalf("error %v, want MalformedChunkError ChunkTooLarge", err)
	}
}

func TestUnderlyingErrorsPropagate(t *testing.T) {
	t.Parallel()
	cause := errors.New("connection reset")
	src := io.MultiReader(strings.NewReader("5\r\nab"), errReader{cause})
	_, err := io.ReadAll(awschunked.NewReader(src, awschunked.PlainChunked, awschunked.Options{}))
	if !errors.Is(err, cause) {
		t.Fatalf("error %v, want the source's error", err)
	}
}

// A net/http request body ends with io.ErrUnexpectedEOF when the client
// disconnects before Content-Length bytes arrive. That is a short body, so it
// gets the same errors as a source that ends with io.EOF.
func TestShortHTTPBody(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		check      func(error) bool
		want       string
	}{
		{"inside chunk data", "5\r\nab", func(err error) bool {
			var incomplete *awschunked.IncompleteBodyError
			return errors.As(err, &incomplete) && *incomplete == awschunked.IncompleteBodyError{Expected: 5, Got: 2}
		}, "IncompleteBodyError{Expected: 5, Got: 2}"},
		{"inside the framing", "5\r\nabcde\r\n0\r", isDefect(awschunked.Truncated), "MalformedChunkError Truncated"},
		{"after the closing line", "5\r\nabcde\r\n0\r\n\r\n", isDefect(awschunked.Truncated), "MalformedChunkError Truncated"},
	} {
		req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(
			"PUT /bucket/key HTTP/1.1\r\nHost: s3.localhost\r\nContent-Length: 100\r\n\r\n" + tc.body)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(awschunked.NewReader(req.Body, awschunked.PlainChunked, awschunked.Options{}))
		if !tc.check(err) {
			t.Errorf("%s: error %v (%T), want %s", tc.name, err, err, tc.want)
		}
	}
}

func isDefect(d awschunked.Defect) func(error) bool {
	return func(err error) bool {
		var malformed *awschunked.MalformedChunkError
		return errors.As(err, &malformed) && malformed.Defect == d
	}
}

// A source error delivered with the last bytes of a chunk ends the body even
// when the source would go on to yield valid framing. A read of 4 KiB or more
// into an empty buffer makes bufio hand the source's error straight through.
func TestErrorWithLastChunkBytesPropagates(t *testing.T) {
	t.Parallel()
	cause := errors.New("connection reset")
	src := &scriptedReader{steps: []readStep{
		{data: "1388\r\n"},
		{data: strings.Repeat("a", 5000), err: cause},
		{data: "\r\n0\r\n\r\n"},
	}}
	r := awschunked.NewReader(src, awschunked.PlainChunked, awschunked.Options{})
	buf := make([]byte, 8192)
	n, err := r.Read(buf)
	if n != 5000 || !errors.Is(err, cause) {
		t.Fatalf("Read = %d, %v; want 5000 bytes and the source's error", n, err)
	}
	if _, err := r.Read(buf); !errors.Is(err, cause) {
		t.Fatalf("Read after the failure = %v; want the source's error again", err)
	}
}

// readStep is one Read result of a scriptedReader.
type readStep struct {
	data string
	err  error
}

// scriptedReader returns one step per Read, then io.EOF.
type scriptedReader struct{ steps []readStep }

func (s *scriptedReader) Read(p []byte) (int, error) {
	if len(s.steps) == 0 {
		return 0, io.EOF
	}
	step := s.steps[0]
	if len(step.data) > len(p) {
		return 0, errors.New("scriptedReader: Read buffer smaller than the scripted step")
	}
	s.steps = s.steps[1:]
	return copy(p, step.data), step.err
}

// errReader fails every Read with err.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestUnknownModeFails(t *testing.T) {
	t.Parallel()
	_, err := io.ReadAll(awschunked.NewReader(strings.NewReader("0\r\n\r\n"), awschunked.Mode(0), awschunked.Options{}))
	var unknown *awschunked.UnknownModeError
	if !errors.As(err, &unknown) || unknown.Mode != awschunked.Mode(0) {
		t.Fatalf("error %v, want UnknownModeError", err)
	}
	var buf bytes.Buffer
	_, err = awschunked.NewWriter(&buf, awschunked.Mode(9), awschunked.WriterOptions{}).Write([]byte("a"))
	if !errors.As(err, &unknown) || unknown.Mode != awschunked.Mode(9) {
		t.Fatalf("Writer error %v, want UnknownModeError", err)
	}
}

func TestWriterRejectsUnknownLineEnding(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := awschunked.NewWriter(&buf, awschunked.UnsignedTrailer, awschunked.WriterOptions{TrailerEnding: awschunked.LineEnding(9)})
	err := w.Finish(awschunked.Trailer{Name: "x-amz-checksum-crc32", Value: "AAAAAA=="})
	var unknown *awschunked.UnknownLineEndingError
	if !errors.As(err, &unknown) || unknown.Ending != awschunked.LineEnding(9) {
		t.Fatalf("Finish error %v, want UnknownLineEndingError", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("Finish wrote %q before failing", buf.String())
	}
}

// A trailer mode without the declared trailer name is a caller defect and is
// reported as one, whatever the body holds.
func TestTrailerModeWithoutDeclaredTrailerFails(t *testing.T) {
	t.Parallel()
	sig := strings.Repeat("0", 64)
	for mode, wire := range map[awschunked.Mode]string{
		awschunked.UnsignedTrailer: "0\r\nx-amz-checksum-crc32:AAAAAA==\r\n\r\n",
		awschunked.SignedTrailer:   "0;chunk-signature=" + sig + "\r\n\r\n",
	} {
		_, err := io.ReadAll(awschunked.NewReader(strings.NewReader(wire), mode, awschunked.Options{}))
		var undeclared *awschunked.UndeclaredTrailerError
		if !errors.As(err, &undeclared) || undeclared.Mode != mode {
			t.Errorf("%v: error %v, want UndeclaredTrailerError", mode, err)
		}
	}
}

func TestParseLength(t *testing.T) {
	t.Parallel()
	if l, err := awschunked.ParseLength(""); err != nil {
		t.Errorf("empty header: %v", err)
	} else if _, ok := l.Get(); ok {
		t.Error("empty header declared a length")
	}
	if l, err := awschunked.ParseLength("66560"); err != nil {
		t.Errorf("66560: %v", err)
	} else if n, ok := l.Get(); !ok || n != 66560 {
		t.Errorf("66560 parsed as %d, %v", n, ok)
	}
	for _, bad := range []string{"-1", "+1", "1.0", "abc", " 1", "99999999999999999999"} {
		var invalid *awschunked.InvalidLengthError
		if _, err := awschunked.ParseLength(bad); !errors.As(err, &invalid) || invalid.Value != bad {
			t.Errorf("ParseLength(%q) = %v, want InvalidLengthError", bad, err)
		}
	}
}
