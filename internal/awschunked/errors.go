package awschunked

import "strconv"

// IncompleteBodyError reports a body that ended before all its decoded bytes
// arrived. S3 answers 400 IncompleteBody.
type IncompleteBodyError struct {
	// Expected is the declared decoded length, or, when none was declared and
	// the body was cut inside a chunk, the bytes the chunk headers announced.
	Expected int64
	// Got is the decoded bytes received.
	Got int64
}

func (e *IncompleteBodyError) Error() string {
	return "awschunked: incomplete body: expected " + strconv.FormatInt(e.Expected, 10) +
		" decoded bytes, got " + strconv.FormatInt(e.Got, 10)
}

// DecodedLengthExceededError reports a chunk header that announces more
// decoded bytes than x-amz-decoded-content-length declared. It is raised at
// the header, before any of that chunk's data is read. S3's code for this
// case is not recorded; the S3 layer answers 400 IncompleteBody, as it does
// for the short case.
type DecodedLengthExceededError struct {
	Declared  int64 // x-amz-decoded-content-length
	Announced int64 // decoded bytes before the chunk plus the chunk's size
}

func (e *DecodedLengthExceededError) Error() string {
	return "awschunked: chunk headers announce " + strconv.FormatInt(e.Announced, 10) +
		" decoded bytes, more than the " + strconv.FormatInt(e.Declared, 10) +
		" x-amz-decoded-content-length declared"
}

// ChunkTooSmallError reports a data chunk under 8,192 bytes that is not the
// last data chunk, in any mode. It is raised at the next non-zero chunk
// header, since only then is the short chunk known not to be the last.
//
// S3 answers this case with its own code, InvalidChunkSizeError, message
// "Only the last chunk is allowed to have a size less than 8192 bytes" (the
// AWS JS SDK's e2e test against real S3, middleware-flexible-checksums
// .e2e.spec.ts). The status is 403, and S3 renders Chunk and Size as the
// <Chunk> and <BadChunkSize> elements: chunks of 3 then 2 bytes answer
// <Chunk>2</Chunk><BadChunkSize>3</BadChunkSize>
// (testdata/parity/s3/R08/03-trailer-small-nonfinal-chunk.json). The S3
// layer must not fold this case into the generic framing error.
type ChunkTooSmallError struct {
	// Chunk is the 1-based index of the chunk being read when the short one
	// was detected, the one after it, as S3 counts it.
	Chunk int64
	Size  int64 // the short chunk's size in bytes
}

func (e *ChunkTooSmallError) Error() string {
	return "awschunked: chunk " + strconv.FormatInt(e.Chunk-1, 10) + " holds " + strconv.FormatInt(e.Size, 10) +
		" bytes; only the last chunk may hold fewer than 8192"
}

// MalformedChunkError reports aws-chunked framing that does not follow the
// grammar of the request's mode.
type MalformedChunkError struct {
	// Offset is the encoded bytes consumed when the defect was detected: the
	// end of the offending line or terminator, not its start.
	Offset int64
	Defect Defect
}

func (e *MalformedChunkError) Error() string {
	return "awschunked: malformed body at encoded offset " + strconv.FormatInt(e.Offset, 10) + ": " + e.Defect.String()
}

// Defect is what made a body malformed.
type Defect uint8

// The framing defects a Reader reports.
const (
	// BadSizeLine is a chunk header that is not "<hex size>" (unsigned modes) or
	// "<hex size>;chunk-signature=<64 lower-case hex>" (signed modes).
	BadSizeLine Defect = iota + 1
	// BadLineEnding is a line not ended by CRLF (or, for a trailer field, by
	// LF CRLF).
	BadLineEnding
	// LineTooLong is a header or trailer line longer than any valid one.
	LineTooLong
	// ChunkTooLarge is a chunk larger than Options.MaxChunk, or one whose size
	// would overflow the int64 count of decoded bytes.
	ChunkTooLarge
	// BadChunkTerminator is chunk data not followed by CRLF.
	BadChunkTerminator
	// BadTrailerLine is a trailer line that is not "<name>:<value>".
	BadTrailerLine
	// ExtraTrailer is a second trailer field; S3 allows one.
	ExtraTrailer
	// MissingTrailerSignature is a SignedTrailer body without its
	// x-amz-trailer-signature line, with a line after it, or whose value is
	// not a 64-hex signature.
	MissingTrailerSignature
	// TrailingData is bytes after the closing empty line.
	TrailingData
	// Truncated is a body that ended after all declared data had arrived but
	// before its framing was complete, or, with io.ErrUnexpectedEOF, before
	// the transport's declared length.
	Truncated
)

// String describes the defect for an error message.
func (d Defect) String() string {
	switch d {
	case BadSizeLine:
		return "chunk size line is not valid for the mode"
	case BadLineEnding:
		return "line does not end with CRLF"
	case LineTooLong:
		return "line exceeds the longest valid chunk header or trailer"
	case ChunkTooLarge:
		return "chunk exceeds the size limit"
	case BadChunkTerminator:
		return "chunk data is not followed by CRLF"
	case BadTrailerLine:
		return "trailer line is not <name>:<value>"
	case ExtraTrailer:
		return "more than one trailer field"
	case MissingTrailerSignature:
		return "x-amz-trailer-signature is not the last trailer line or is not a 64-hex signature"
	case TrailingData:
		return "data after the end of the body"
	case Truncated:
		return "body ended inside the chunk framing"
	}
	return "Defect(" + strconv.Itoa(int(d)) + ")"
}

// MissingTrailerError reports a trailer mode body that ended without the
// trailer x-amz-trailer declared.
type MissingTrailerError struct {
	Name string // the declared trailer
}

func (e *MissingTrailerError) Error() string {
	return "awschunked: declared trailer " + strconv.Quote(e.Name) + " is missing from the body"
}

// TrailerMismatchError reports a trailer field whose name is not the one
// x-amz-trailer declared (Declared is empty in modes without trailers).
type TrailerMismatchError struct {
	Declared string
	Got      string
}

func (e *TrailerMismatchError) Error() string {
	if e.Declared == "" {
		return "awschunked: body carries trailer " + strconv.Quote(e.Got) + " but none was declared"
	}
	return "awschunked: body carries trailer " + strconv.Quote(e.Got) + " but x-amz-trailer declared " + strconv.Quote(e.Declared)
}

// InvalidLengthError reports an x-amz-decoded-content-length value that is not
// a non-negative decimal integer.
type InvalidLengthError struct {
	Value string
}

func (e *InvalidLengthError) Error() string {
	return "awschunked: x-amz-decoded-content-length " + strconv.Quote(e.Value) + " is not a non-negative integer"
}

// UnknownModeError reports a Mode value outside the declared constants.
type UnknownModeError struct {
	Mode Mode
}

func (e *UnknownModeError) Error() string {
	return "awschunked: " + e.Mode.String() + " is not an aws-chunked mode"
}

// UnknownLineEndingError reports a WriterOptions.TrailerEnding value outside
// the declared constants.
type UnknownLineEndingError struct {
	Ending LineEnding
}

func (e *UnknownLineEndingError) Error() string {
	return "awschunked: LineEnding(" + strconv.Itoa(int(e.Ending)) + ") is not a trailer line ending; use CRLF or LFCRLF"
}

// UndeclaredTrailerError reports a Reader for a trailer mode built without
// Options.Trailer. It is a caller defect, not a body defect: the caller must
// pass the request's x-amz-trailer value, and reject a request that lacks one
// before decoding its body.
type UndeclaredTrailerError struct {
	Mode Mode
}

func (e *UndeclaredTrailerError) Error() string {
	return "awschunked: " + e.Mode.String() + " needs the x-amz-trailer name in Options.Trailer, which is empty; no body bytes were read"
}
