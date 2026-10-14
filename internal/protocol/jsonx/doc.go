// Package jsonx is the JSON runtime: the only importer of encoding/json/v2
// and encoding/json/jsontext, so a change in either is a change here only.
//
// It has four parts.
//
// Decoder reads a JSON document as a stream of values, leniently, the way
// the AWS JSON protocols need: unknown members are the caller's to skip,
// duplicate object names are all reported (ReadValue keeps the last value,
// in the first one's position), and numbers come back as their decimal
// text (document.Number) so no precision is lost before a schema decides
// the type. Strings keep the bytes the client sent: invalid UTF-8 is not
// replaced, and a \u escape of a lone surrogate becomes the surrogate's
// three-byte generalized UTF-8 form (WTF-8). Either way the decoded string
// fails utf8.ValidString, which is the flag a validator reads to report
// awserr.Encoding (SQS InvalidMessageContents) instead of the request
// failing to decode.
//
// Encoder writes one compact JSON value from a stream of calls. WireOrder
// keeps members in call order, for responses; SortedKeys sorts every
// object's members by name, for documents that must not depend on write
// order. Strings escape the quote, the backslash and control characters; a
// lone WTF-8 surrogate is written back as its \u escape, so it reads back as
// the same bytes. A WTF-8 high surrogate directly followed by a low one is
// written as � then the low one's escape, because escaping both would read
// back as one valid character; the string still fails utf8.ValidString.
// Any other invalid UTF-8 byte becomes �, and so reads back valid.
// U+2028, U+2029, <, > and & are written raw, as encoding/json/v2 does.
// AppendString writes one string literal the same way, for codecs that
// build a small fixed document by hand.
//
// Document values (smithy/document) are the tree for rewriting a body, as
// the DynamoDB proxy does: Decoder.ReadValue reads one and Encoder.Value
// writes one.
//
// Codec is Marshal and Unmarshal for store.Codec and other persisted
// documents. Marshal sorts every object's members, so equal values give
// equal bytes. Unmarshal is strict (unknown members, duplicate names and
// invalid UTF-8 fail) because a persisted document that does not match the
// running binary's types is corrupt or from another schema version.
//
// Union implements the tagged-union convention for persisted sealed sums
// (ARCHITECTURE.md 13): a variant is written as {"kind": "<variant>",
// ...its fields}. Each sum declares its table once with NewUnion and
// Variant, and a Codec built with the table reads and writes the sum
// wherever it appears. A variant type stored directly in a field, rather
// than through the sum interface, carries its kind too, because
// encoding/json/v2 applies a sum's marshaler to every type implementing it;
// reading checks that kind. Variants are stored as values: a pointer to a
// variant fails to marshal, since it would read back as the value.
package jsonx
