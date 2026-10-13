package smithy

import (
	"io"
	"math/big"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// BigDecimal is an arbitrary-precision decimal kept as its decimal text, so a
// bigDecimal member round-trips exactly.
type BigDecimal = document.Number

// Serializer writes a value through its schema; each codec implements it.
// Every value write names the member that holds it: a list element is written
// with the list's "member" member, a map value with the map's "value" member.
// A codec records a write failure and reports it when the output is flushed,
// so the generated Serialize methods return nothing.
type Serializer interface {
	// WriteStruct opens a structure. m is the member that holds it, or nil for
	// the top-level input, output or error structure.
	WriteStruct(m *Member, s *Schema)
	CloseStruct()
	// WriteUnion opens a union; exactly one member write follows.
	WriteUnion(m *Member, s *Schema)
	CloseUnion()
	// WriteList opens a list of n elements.
	WriteList(m *Member, n int)
	CloseList()
	// WriteMap opens a map of n entries; each entry is WriteMapKey then one
	// value write.
	WriteMap(m *Member, n int)
	WriteMapKey(key string)
	CloseMap()

	WriteString(m *Member, v string)
	WriteBool(m *Member, v bool)
	WriteInt8(m *Member, v int8)
	WriteInt16(m *Member, v int16)
	WriteInt32(m *Member, v int32)
	WriteInt64(m *Member, v int64)
	WriteFloat32(m *Member, v float32)
	WriteFloat64(m *Member, v float64)
	WriteBigInt(m *Member, v *big.Int)
	WriteBigDecimal(m *Member, v BigDecimal)
	// WriteTimestamp formats v from the member's effective timestampFormat
	// trait and the protocol default.
	WriteTimestamp(m *Member, v time.Time)
	WriteBlob(m *Member, v []byte)
	WriteDocument(m *Member, v document.Value)
	// WriteNull writes an explicit null: a sparse collection's null value.
	WriteNull(m *Member)
	// WriteBody hands over an output @streaming blob, streamed after the
	// head. Only the REST payload binder accepts one; every other codec
	// records a serialization error, since its protocols model no streaming
	// member.
	WriteBody(m *Member, b BodySource)
}

// Deserializer reads a value through its schema; each codec implements it.
// Codecs are lenient on input: unknown members are skipped and never
// returned, and a null member is reported as absent.
type Deserializer interface {
	// ReadStruct enters a structure: the top-level input or a member value.
	ReadStruct(s *Schema) error
	// ReadStructMember returns the next member present on the wire,
	// positioned at its value. It returns false, consuming the structure's
	// end, when no member is left.
	ReadStructMember() (*Member, bool, error)
	// ReadUnion enters a union and returns its one set member, positioned at
	// that member's value; CloseUnion then fails if another member follows.
	ReadUnion(s *Schema) (*Member, error)
	CloseUnion() error
	// ReadList enters a list; ReadListElement reports whether another element
	// follows, consuming the list's end when none does.
	ReadList(m *Member) error
	ReadListElement() (bool, error)
	// ReadMap enters a map; ReadMapKey returns the next key, positioned at its
	// value, or false, consuming the map's end, when none is left.
	ReadMap(m *Member) error
	ReadMapKey() (string, bool, error)

	ReadString(m *Member) (string, error)
	ReadBool(m *Member) (bool, error)
	ReadInt8(m *Member) (int8, error)
	ReadInt16(m *Member) (int16, error)
	ReadInt32(m *Member) (int32, error)
	ReadInt64(m *Member) (int64, error)
	ReadFloat32(m *Member) (float32, error)
	ReadFloat64(m *Member) (float64, error)
	ReadBigInt(m *Member) (*big.Int, error)
	ReadBigDecimal(m *Member) (BigDecimal, error)
	ReadTimestamp(m *Member) (time.Time, error)
	ReadBlob(m *Member) ([]byte, error)
	ReadDocument(m *Member) (document.Value, error)
	// ReadNull reports whether the next value is null, consuming it if so: a
	// sparse collection's null value.
	ReadNull(m *Member) (bool, error)
	// ReadBody returns an input @streaming blob unread, so the handler pulls
	// it after its pre-checks. Only the REST payload binder supplies one;
	// every other codec returns a serialization error, since its protocols
	// model no streaming member.
	ReadBody(m *Member) (Body, error)
}

// Serializable is a generated output or error type.
type Serializable interface {
	Serialize(s Serializer)
}

// Deserializable is a generated input type. It returns the codec's error for
// input the codec cannot map onto the schema.
type Deserializable interface {
	Deserialize(d Deserializer) error
}

// Body is an input @streaming blob: the handler pulls
// it after its pre-checks, never as []byte.
type Body interface {
	// Open returns the body; net/http sends 100 Continue on the first read.
	Open() (io.ReadCloser, error)
	// Len returns the declared decoded length when the request states one.
	Len() (int64, bool)
	// Trailer returns a trailer-bound member's value (aws-chunked
	// x-amz-checksum-*). It is valid only after the body reached EOF.
	Trailer(name string) (string, bool)
}

// BodySource is an output @streaming blob, streamed from the blob store. A
// source that holds a resource until Range hands it over, such as an open
// file, also implements io.Closer, so an output dropped before encoding
// (kernel.Release) frees it; the encoder closes what Range returns.
type BodySource interface {
	Size() int64
	// Range returns n bytes starting at off.
	Range(off, n int64) (io.ReadCloser, error)
}
