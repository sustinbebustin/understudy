package awschunked

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

const (
	// maxLine bounds a chunk header or trailer line and is the read buffer
	// size. The longest valid line is an x-amz-checksum-sha512 trailer, about
	// 110 bytes.
	maxLine = 4096
	// signatureExt introduces the chunk signature in signed modes.
	signatureExt = ";chunk-signature="
	// trailerSignatureName is the last trailer line of a SignedTrailer body.
	trailerSignatureName = "x-amz-trailer-signature"
	// signatureHexLen is the length of a hex SigV4 signature.
	signatureHexLen = 2 * sha256.Size
	// minChunk is the smallest data chunk, other than the last, that a body
	// may carry in any mode (sigv4-checksums-endpoints.md 3.2, 3.3, 12 item 5).
	minChunk = 8192
)

// Options configures a Reader.
type Options struct {
	// DecodedLength is x-amz-decoded-content-length. When declared, the
	// decoded byte count must equal it.
	DecodedLength Length
	// Trailer is the x-amz-trailer value: the one trailer field a trailer mode
	// body must end with, compared ignoring case. It is required in trailer
	// modes; modes without trailers ignore it and accept no trailer field.
	Trailer string
	// Seed is the request's header signature, the previous signature of the
	// first chunk in signed modes.
	Seed string
	// OnChunkSignature checks each chunk signature and the trailer signature
	// in signed modes. Nil parses signatures without checking them, which is
	// what understudy does while SigV4 validation is off.
	OnChunkSignature SignatureFunc
	// MaxChunk is the largest chunk size accepted; zero or less means no
	// limit (S3 documents none).
	MaxChunk int64
}

// readerState is where a Reader is in the body grammar.
type readerState uint8

const (
	expectHeader readerState = iota // next is a chunk size line
	inData                          // inside a chunk's data
	finished                        // the body ended well; Read returns io.EOF
	failed                          // Read returns err
)

// Reader decodes an aws-chunked body into the object bytes. It streams: a
// chunk is never buffered whole, and a chunk signature is checked when the
// chunk's data has been read, so the bytes of a chunk reach the caller before
// its signature is known to be good. Callers commit what they read only after
// Read returns io.EOF.
type Reader struct {
	src   *bufio.Reader
	opts  Options
	mode  Mode
	state readerState
	err   error

	offset    int64     // encoded bytes consumed
	decoded   int64     // data bytes returned
	remaining int64     // data bytes left in the current chunk
	chunks    int64     // data chunks started so far
	lastChunk int64     // size of the latest data chunk; 0 before the first
	sig       string    // signature of the current chunk, in signed modes
	verifier  *verifier // nil unless signatures are checked
	trailers  http.Header
}

// verifier checks the signature chain of a signed body. A Reader has one only
// when Options.OnChunkSignature is set, so a body whose signatures go
// unchecked is not hashed.
type verifier struct {
	check SignatureFunc
	prev  string    // the previous link's signature, Options.Seed at first
	chunk hash.Hash // SHA-256 of the current chunk's data
}

// NewReader returns a Reader that decodes r framed as mode. An undeclared
// mode makes every Read fail with UnknownModeError, and a trailer mode without
// Options.Trailer with UndeclaredTrailerError.
func NewReader(r io.Reader, mode Mode, opts Options) *Reader {
	rd := &Reader{
		// The wrapper stops bufio from reusing a caller's *bufio.Reader, whose
		// larger buffer would lift the maxLine bound.
		src:      bufio.NewReaderSize(struct{ io.Reader }{r}, maxLine),
		opts:     opts,
		mode:     mode,
		trailers: http.Header{},
	}
	signed, trailer, ok := mode.framing()
	switch {
	case !ok:
		rd.state, rd.err = failed, &UnknownModeError{Mode: mode}
	case trailer && opts.Trailer == "":
		rd.state, rd.err = failed, &UndeclaredTrailerError{Mode: mode}
	}
	if signed && opts.OnChunkSignature != nil {
		rd.verifier = &verifier{check: opts.OnChunkSignature, prev: opts.Seed, chunk: sha256.New()}
	}
	return rd
}

// Read reads decoded object bytes. Framing faults surface as
// MalformedChunkError, ChunkTooSmallError, IncompleteBodyError, DecodedLengthExceededError,
// MissingTrailerError and TrailerMismatchError, and signature faults as the
// OnChunkSignature error. The underlying reader ending early, with io.EOF or
// with io.ErrUnexpectedEOF as a net/http body cut short does, is a framing
// fault; its other errors pass through unchanged. The first error is final.
func (r *Reader) Read(p []byte) (int, error) {
	for {
		switch r.state {
		case expectHeader:
			if err := r.readHeader(); err != nil {
				return 0, r.fail(err)
			}
		case inData:
			return r.readData(p)
		case finished:
			return 0, io.EOF
		case failed:
			return 0, r.err
		}
	}
}

// Trailers returns the trailer fields, keyed by canonical header name, once
// Read has returned io.EOF; before that it returns false.
func (r *Reader) Trailers() (http.Header, bool) {
	if r.state != finished {
		return nil, false
	}
	return r.trailers.Clone(), true
}

func (r *Reader) readHeader() error {
	line, err := r.readLine()
	if err != nil {
		return err
	}
	size, sig, err := r.parseSizeLine(line)
	if err != nil {
		return err
	}
	if r.opts.MaxChunk > 0 && size > r.opts.MaxChunk || size > math.MaxInt64-r.decoded {
		return r.malformed(ChunkTooLarge)
	}
	if n, ok := r.opts.DecodedLength.Get(); ok && r.decoded+size > n {
		return &DecodedLengthExceededError{Declared: n, Announced: r.decoded + size}
	}
	r.sig = sig
	if r.verifier != nil {
		r.verifier.chunk.Reset()
	}
	if size == 0 {
		return r.finish()
	}
	if r.chunks > 0 && r.lastChunk < minChunk {
		return &ChunkTooSmallError{Chunk: r.chunks, Size: r.lastChunk}
	}
	r.chunks++
	r.lastChunk = size
	r.remaining = size
	r.state = inData
	return nil
}

func (r *Reader) parseSizeLine(line string) (size int64, sig string, err error) {
	sizeHex := line
	if r.mode.signed() {
		var ok bool
		sizeHex, sig, ok = strings.Cut(line, signatureExt)
		if !ok || !isSignature(sig) {
			return 0, "", r.malformed(BadSizeLine)
		}
	}
	n, err := strconv.ParseUint(sizeHex, 16, 63)
	if err != nil {
		return 0, "", r.malformed(BadSizeLine)
	}
	return int64(n), sig, nil
}

func (r *Reader) readData(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := r.src.Read(p[:min(int64(len(p)), r.remaining)])
	r.offset += int64(n)
	r.decoded += int64(n)
	r.remaining -= int64(n)
	if r.verifier != nil {
		_, _ = r.verifier.chunk.Write(p[:n])
	}
	// A source may return a chunk's last bytes together with a fault; reading
	// on past it would hide the fault behind whatever the source yields next.
	// An end of input there leaves no room for the chunk terminator.
	if err != nil {
		return n, r.fail(r.sourceError(err))
	}
	if r.remaining == 0 {
		if err := r.endChunk(); err != nil {
			return n, r.fail(err)
		}
		r.state = expectHeader
	}
	return n, nil
}

func (r *Reader) endChunk() error {
	var end [2]byte
	n, err := io.ReadFull(r.src, end[:])
	r.offset += int64(n)
	if err != nil {
		return r.sourceError(err)
	}
	if end != [2]byte{'\r', '\n'} {
		return r.malformed(BadChunkTerminator)
	}
	if r.verifier != nil {
		return r.verifier.link(ChunkSigned, r.verifier.chunk, r.sig)
	}
	return nil
}

// finish handles everything after the final zero-length chunk header.
func (r *Reader) finish() error {
	if r.verifier != nil {
		if err := r.verifier.link(ChunkSigned, r.verifier.chunk, r.sig); err != nil {
			return err
		}
	}
	if n, ok := r.opts.DecodedLength.Get(); ok && r.decoded != n {
		return &IncompleteBodyError{Expected: n, Got: r.decoded}
	}
	if err := r.readTrailers(); err != nil {
		return err
	}
	if err := r.expectEOF(); err != nil {
		return err
	}
	r.state = finished
	return nil
}

type field struct{ name, value string }

func (r *Reader) readTrailers() error {
	fields, err := r.readTrailerSection()
	if err != nil {
		return err
	}
	if !r.mode.trailing() {
		// admitTrailer let no field through.
		return nil
	}
	if r.mode == SignedTrailer {
		if fields, err = r.checkTrailerSignature(fields); err != nil {
			return err
		}
	}
	// admitTrailer let at most one field through besides the signature.
	for _, f := range fields {
		if !strings.EqualFold(f.name, r.opts.Trailer) {
			return &TrailerMismatchError{Declared: r.opts.Trailer, Got: f.name}
		}
		r.trailers.Add(f.name, f.value)
	}
	if len(r.trailers) == 0 {
		return &MissingTrailerError{Name: r.opts.Trailer}
	}
	return nil
}

// checkTrailerSignature checks the x-amz-trailer-signature line that must end
// a SignedTrailer trailer section and returns the fields before it, which it
// signs.
func (r *Reader) checkTrailerSignature(fields []field) ([]field, error) {
	last := len(fields) - 1
	if last < 0 || !isTrailerSignature(fields[last]) || !isSignature(fields[last].value) {
		return nil, r.malformed(MissingTrailerSignature)
	}
	if r.verifier != nil {
		h := sha256.New()
		for _, f := range fields[:last] {
			_, _ = h.Write([]byte(f.name + ":" + f.value + "\n"))
		}
		if err := r.verifier.link(TrailerSigned, h, fields[last].value); err != nil {
			return nil, err
		}
	}
	return fields[:last], nil
}

// readTrailerSection reads the trailer lines up to the closing empty line. It
// rejects each line the mode has no room for as it arrives, so the section
// holds at most two lines however long the body goes on.
func (r *Reader) readTrailerSection() ([]field, error) {
	var fields []field
	for {
		line, err := r.readTrailerLine()
		if err != nil {
			return nil, err
		}
		if line == "" {
			return fields, nil
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || !isFieldName(name) || strings.ContainsFunc(value, isControl) {
			return nil, r.malformed(BadTrailerLine)
		}
		f := field{name: name, value: strings.Trim(value, " \t")}
		if err := r.admitTrailer(fields, f); err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
}

// admitTrailer rejects f when the mode has no room for it after fields: a mode
// without trailers takes no field, a trailer mode one, and SignedTrailer one
// plus the x-amz-trailer-signature line, which must come last.
func (r *Reader) admitTrailer(fields []field, f field) error {
	switch {
	case !r.mode.trailing():
		return &TrailerMismatchError{Got: f.name}
	case r.mode == SignedTrailer && len(fields) > 0 && isTrailerSignature(fields[len(fields)-1]):
		return r.malformed(MissingTrailerSignature)
	case r.mode == SignedTrailer && isTrailerSignature(f):
		return nil
	case len(fields) > 0:
		return r.malformed(ExtraTrailer)
	}
	return nil
}

func isTrailerSignature(f field) bool {
	return strings.EqualFold(f.name, trailerSignatureName)
}

// readLine reads a CRLF-terminated chunk header line.
func (r *Reader) readLine() (string, error) {
	raw, err := r.readRawLine()
	if err != nil {
		return "", err
	}
	line, ok := strings.CutSuffix(raw, "\r\n")
	if !ok {
		return "", r.malformed(BadLineEnding)
	}
	return line, nil
}

// readTrailerLine reads a trailer line ended by CRLF or, as the S3 user guide
// shows and some clients send, by LF CRLF. The closing empty line must be CRLF.
func (r *Reader) readTrailerLine() (string, error) {
	raw, err := r.readRawLine()
	if err != nil {
		return "", err
	}
	if line, ok := strings.CutSuffix(raw, "\r\n"); ok {
		return line, nil
	}
	line := strings.TrimSuffix(raw, "\n")
	if line == "" {
		return "", r.malformed(BadLineEnding)
	}
	var end [2]byte
	n, err := io.ReadFull(r.src, end[:])
	r.offset += int64(n)
	if err != nil {
		return "", r.sourceError(err)
	}
	if end != [2]byte{'\r', '\n'} {
		return "", r.malformed(BadLineEnding)
	}
	return line, nil
}

// readRawLine reads through the next LF.
func (r *Reader) readRawLine() (string, error) {
	raw, err := r.src.ReadSlice('\n')
	r.offset += int64(len(raw))
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		return "", r.malformed(LineTooLong)
	case err != nil:
		return "", r.sourceError(err)
	}
	return string(raw), nil
}

func (r *Reader) expectEOF() error {
	_, err := r.src.ReadByte()
	switch {
	case err == nil:
		return r.malformed(TrailingData)
	case errors.Is(err, io.EOF):
		return nil
	}
	return r.sourceError(err)
}

// link checks one chain link, sig over the SHA-256 in h, and makes sig the
// previous signature of the next link.
func (v *verifier) link(kind SignedKind, h hash.Hash, sig string) error {
	payload := SignedPayload{Kind: kind}
	h.Sum(payload.SHA256[:0])
	if err := v.check(v.prev, payload, sig); err != nil {
		return err
	}
	v.prev = sig
	return nil
}

// sourceError maps an end of input inside the framing to the AWS-facing
// error and passes any other error of the underlying reader through.
func (r *Reader) sourceError(err error) error {
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	expected := r.decoded + r.remaining
	if n, ok := r.opts.DecodedLength.Get(); ok {
		expected = n
	}
	if r.decoded < expected {
		return &IncompleteBodyError{Expected: expected, Got: r.decoded}
	}
	return r.malformed(Truncated)
}

func (r *Reader) malformed(d Defect) error {
	return &MalformedChunkError{Offset: r.offset, Defect: d}
}

func (r *Reader) fail(err error) error {
	r.state, r.err = failed, err
	return err
}

func isSignature(s string) bool {
	if len(s) != signatureHexLen {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// isFieldName accepts a non-empty run of visible ASCII, which every
// x-amz-checksum-* name is.
func isFieldName(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(c rune) bool { return c <= ' ' || c >= 0x7f })
}

func isControl(c rune) bool {
	return c < ' ' && c != '\t' || c == 0x7f
}
