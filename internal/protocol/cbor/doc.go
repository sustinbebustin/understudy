// Package cbor implements the subset of CBOR (RFC 8949) that Smithy RPC v2
// CBOR uses, with the standard library only (wire-cbor.md section 7).
//
// Reader decodes leniently, as a server must: every major type, definite
// and indefinite lengths nested in each other, arguments of any width
// (non-minimal ones included), half, single and double floats, and tag 1
// timestamps wrapping an integer or a float of any width. It is strict
// where the subset is: map keys are text, text is valid UTF-8 (each chunk
// of an indefinite string on its own), simple values other than false,
// true, null and undefined are refused, and so is any tag where a
// timestamp is read. Two limits keep hostile input cheap: arrays, maps and
// tags nest at most MaxDepth (128) levels, and a declared length or count
// larger than the input left is refused before anything is allocated.
// Errors are typed (MalformedError with a Problem, MismatchError,
// RangeError, UsageError) and no input makes the Reader panic.
//
// Writer encodes canonically: definite lengths only, counted while
// writing so a structure need not know its member count up front,
// arguments in their smallest width, floats in the width they were
// written with, timestamps as tag 1 wrapping a double, null as 0xf6. Its
// typed methods never write half floats or undefined; Writer.Value writes
// an Undefined it is given, so a decoded tree re-encodes. Value is the
// whole-item tree (ReadValue, Writer.Value, Decode, Encode) for callers
// that want a document rather than a stream.
//
// Decisions the architecture leaves open:
//
//   - Duplicate map keys are kept in a Value in wire order; a streaming
//     caller sees each one, so the last wins for a structure member, as
//     smithy-go decodes them (wire-cbor.md 3.6, U8).
//
//   - A timestamp float is rounded to the nearest millisecond, the
//     resolution Smithy timestamps carry, so 1.767323045123e+09, whose
//     nearest double lies just below the instant, reads as ...123 ms.
//     Timestamps outside the years 0001 to 9999 are a RangeError.
//
//   - Writer.Timestamp writes the double one ulp further from zero when
//     the nearest double lies on the zero side of the instant far enough
//     that smithy-go's truncating int64(f*1e3) would read it one
//     millisecond off (wire-cbor.md 5.1, U12). Any other instant is
//     written as the plain nearest double, so the response vectors stay
//     byte for byte.
//
//   - Writer.Text replaces invalid UTF-8 with U+FFFD, as jsonx does for
//     JSON, so output is always well-formed and reads back.
package cbor
