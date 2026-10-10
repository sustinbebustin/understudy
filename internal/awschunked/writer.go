package awschunked

import (
	"crypto/sha256"
	"io"
	"strconv"
	"strings"
)

// Trailer is one trailer field a Writer emits after the final chunk.
type Trailer struct {
	Name  string
	Value string
}

// LineEnding is how a Writer ends trailer field lines.
type LineEnding uint8

// The trailer line endings clients send (sigv4-checksums-endpoints.md 3.2).
const (
	// CRLF is what aws-sdk-go-v2, botocore and the JS SDK send.
	CRLF LineEnding = iota
	// LFCRLF is "\n\r\n", the form the S3 user guide's examples show.
	LFCRLF
)

// SignFunc returns the signature for one link of the chain, given the
// previous signature (WriterOptions.Seed for the first chunk).
type SignFunc func(prev string, signed SignedPayload) string

// WriterOptions configures a Writer.
type WriterOptions struct {
	// Seed is the header signature that starts the chain in signed modes.
	Seed string
	// Sign signs chunks and the trailer in signed modes. Nil writes 64 zeros,
	// which a Reader without OnChunkSignature accepts.
	Sign SignFunc
	// TrailerEnding ends each trailer field line.
	TrailerEnding LineEnding
}

// Writer encodes a body as aws-chunked, one chunk per Write. It is the
// client side of Reader, for tests and the smoke test; it writes whatever
// chunks and trailers it is given, so it can also produce bodies a Reader
// rejects, such as a body with a chunk under 8,192 bytes before the last.
type Writer struct {
	w    io.Writer
	mode Mode
	opts WriterOptions
	prev string
}

// NewWriter returns a Writer that frames its input as mode onto w. An
// undeclared mode makes Write and Finish fail with UnknownModeError.
func NewWriter(w io.Writer, mode Mode, opts WriterOptions) *Writer {
	return &Writer{w: w, mode: mode, opts: opts, prev: opts.Seed}
}

// Write emits p as one chunk. An empty p writes nothing, since a zero-length
// chunk ends the body.
func (w *Writer) Write(p []byte) (int, error) {
	signed, _, ok := w.mode.framing()
	if !ok {
		return 0, &UnknownModeError{Mode: w.mode}
	}
	if len(p) == 0 {
		return 0, nil
	}
	var b strings.Builder
	b.WriteString(strconv.FormatInt(int64(len(p)), 16))
	if signed {
		b.WriteString(signatureExt + w.sign(ChunkSigned, sha256.Sum256(p)))
	}
	b.WriteString("\r\n")
	b.Write(p)
	b.WriteString("\r\n")
	if _, err := io.WriteString(w.w, b.String()); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Finish writes the final zero-length chunk, the trailers (with the trailer
// signature in SignedTrailer mode) and the closing empty line. Call it once,
// after the last Write. An undeclared WriterOptions.TrailerEnding makes it
// fail with UnknownLineEndingError before writing anything.
func (w *Writer) Finish(trailers ...Trailer) error {
	signed, trailer, ok := w.mode.framing()
	if !ok {
		return &UnknownModeError{Mode: w.mode}
	}
	ending, ok := w.opts.TrailerEnding.text()
	if !ok {
		return &UnknownLineEndingError{Ending: w.opts.TrailerEnding}
	}
	var b strings.Builder
	b.WriteString("0")
	if signed {
		b.WriteString(signatureExt + w.sign(ChunkSigned, sha256.Sum256(nil)))
	}
	b.WriteString("\r\n")
	h := sha256.New()
	for _, t := range trailers {
		line := t.Name + ":" + t.Value
		_, _ = h.Write([]byte(line + "\n"))
		b.WriteString(line + ending)
	}
	if signed && trailer {
		var sum [sha256.Size]byte
		h.Sum(sum[:0])
		b.WriteString(trailerSignatureName + ":" + w.sign(TrailerSigned, sum) + "\r\n")
	}
	b.WriteString("\r\n")
	_, err := io.WriteString(w.w, b.String())
	return err
}

func (w *Writer) sign(kind SignedKind, sum [sha256.Size]byte) string {
	sig := strings.Repeat("0", signatureHexLen)
	if w.opts.Sign != nil {
		sig = w.opts.Sign(w.prev, SignedPayload{Kind: kind, SHA256: sum})
	}
	w.prev = sig
	return sig
}

// text returns the bytes that end a trailer line, and false for a value that
// is not a declared LineEnding.
func (e LineEnding) text() (string, bool) {
	switch e {
	case CRLF:
		return "\r\n", true
	case LFCRLF:
		return "\n\r\n", true
	}
	return "", false
}
