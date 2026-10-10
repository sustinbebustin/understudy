package cbor

import "fmt"

// Problem says how input fails to be well-formed CBOR of the subset, or
// breaks one of its limits.
type Problem uint8

// Problem values.
const (
	// Truncated input ends inside a data item.
	Truncated Problem = iota + 1
	// ReservedInfo is additional information 28, 29 or 30, which RFC 8949
	// reserves.
	ReservedInfo
	// IndefiniteNotAllowed is an indefinite length on an integer, a tag or
	// a simple value; only strings, arrays and maps have one.
	IndefiniteNotAllowed
	// UnexpectedBreak is the break byte 0xff where a data item belongs.
	UnexpectedBreak
	// InvalidChunk is a chunk of an indefinite-length string that is not a
	// definite-length string of the same major type.
	InvalidChunk
	// NonTextKey is a map key that is not a text string; Smithy maps and
	// structures have text keys only.
	NonTextKey
	// InvalidUTF8 is a text string that is not valid UTF-8.
	InvalidUTF8
	// TooDeep is nesting of arrays, maps and tags beyond MaxDepth.
	TooDeep
	// LengthExceedsInput is a declared length or count larger than the
	// input left, refused before anything is allocated.
	LengthExceedsInput
	// UnsupportedSimple is a simple value other than false, true, null and
	// undefined.
	UnsupportedSimple
	// TrailingBytes is input left after the top-level data item.
	TrailingBytes
)

// String describes the problem for an error message.
func (p Problem) String() string {
	switch p {
	case Truncated:
		return "the input ends inside a data item"
	case ReservedInfo:
		return "additional information 28 to 30 is reserved"
	case IndefiniteNotAllowed:
		return "an integer, tag or simple value cannot have an indefinite length"
	case UnexpectedBreak:
		return "a break byte (0xff) appears where a data item belongs"
	case InvalidChunk:
		return "an indefinite-length string chunk is not a definite-length string of the same type"
	case NonTextKey:
		return "a map key is not a text string"
	case InvalidUTF8:
		return "a text string is not valid UTF-8"
	case TooDeep:
		return fmt.Sprintf("arrays, maps and tags nest deeper than %d levels", MaxDepth)
	case LengthExceedsInput:
		return "a declared length is larger than the input left"
	case UnsupportedSimple:
		return "a simple value other than false, true, null or undefined"
	case TrailingBytes:
		return "bytes follow the top-level data item"
	default:
		return fmt.Sprintf("Problem(%d)", uint8(p))
	}
}

// MalformedError is input that is not well-formed CBOR of the subset, or
// that breaks one of its limits.
type MalformedError struct {
	// Offset is the byte offset of the offending data item.
	Offset  int64
	Problem Problem
}

func (e *MalformedError) Error() string {
	return fmt.Sprintf("cbor: malformed input at byte %d: %s", e.Offset, e.Problem)
}

// MismatchError is a well-formed data item of a type the caller did not
// ask for.
type MismatchError struct {
	Offset int64
	// Want says what the caller asked for, such as "a text string".
	Want string
	Got  Kind
	// GotTag is the tag number when Got is KindTag.
	GotTag uint64
}

func (e *MismatchError) Error() string {
	got := e.Got.String()
	switch e.Got {
	case KindTag:
		got = fmt.Sprintf("tag %d", e.GotTag)
	case KindUint, KindArray, KindUndefined:
		got = "an " + got
	case KindNegInt, KindBytes, KindText, KindMap, KindBool, KindNull, KindFloat:
		got = "a " + got
	}
	return fmt.Sprintf("cbor: expected %s at byte %d, found %s", e.Want, e.Offset, got)
}

// RangeError is a well-formed value the caller's type cannot hold: an
// integer beyond int64, or a timestamp that is not finite or falls outside
// the years 0001 to 9999.
type RangeError struct {
	Offset int64
	// Value is the value as CBOR diagnostic notation would print it.
	Value string
	// Target is what it does not fit, such as "int64".
	Target string
}

func (e *RangeError) Error() string {
	return fmt.Sprintf("cbor: the value %s at byte %d is out of range of %s", e.Value, e.Offset, e.Target)
}

// UsageError is a call sequence the Reader or Writer does not support, such
// as a Writer value written in a map before its key. It is a bug in the
// caller, not bad input.
type UsageError struct {
	Call   string
	Reason string
}

func (e *UsageError) Error() string {
	return fmt.Sprintf("cbor: %s: %s", e.Call, e.Reason)
}
