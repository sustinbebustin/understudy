package cbor

// Value is one decoded CBOR data item of the subset: the tree ReadValue
// builds and Writer.Value writes. Map keys are text, as Smithy requires; a
// half-precision float decodes to Float32, which holds every half value
// exactly; definite and indefinite lengths decode alike.
//
//sumtype:decl
type Value interface{ isValue() }

// Uint is major type 0, an unsigned integer.
type Uint uint64

// NegInt is major type 1: the integer -1 - N, so the full range
// -2^64 to -1 is representable.
type NegInt uint64

// Bytes is major type 2, a byte string.
type Bytes []byte

// Text is major type 3, a UTF-8 text string.
type Text string

// Array is major type 4.
type Array []Value

// Map is major type 5: entries in wire order, duplicate keys included.
type Map []Entry

// Entry is one key and value of a Map.
type Entry struct {
	Key   string
	Value Value
}

// Tag is major type 6: a tag number and the item it wraps. Tag 1 wraps an
// epoch timestamp.
type Tag struct {
	Number  uint64
	Content Value
}

// Bool is simple value 20 (false) or 21 (true).
type Bool bool

// Null is simple value 22.
type Null struct{}

// Undefined is simple value 23. Smithy reads it as null; understudy never
// emits it in a response.
type Undefined struct{}

// Float32 is a single-precision float, or a half-precision one widened.
type Float32 float32

// Float64 is a double-precision float.
type Float64 float64

func (Uint) isValue()      {}
func (NegInt) isValue()    {}
func (Bytes) isValue()     {}
func (Text) isValue()      {}
func (Array) isValue()     {}
func (Map) isValue()       {}
func (Tag) isValue()       {}
func (Bool) isValue()      {}
func (Null) isValue()      {}
func (Undefined) isValue() {}
func (Float32) isValue()   {}
func (Float64) isValue()   {}

// Kind is the type of the next data item, as Reader.Peek reports it.
type Kind uint8

// Kind values.
const (
	KindUint Kind = iota + 1
	KindNegInt
	KindBytes
	KindText
	KindArray
	KindMap
	KindTag
	KindBool
	KindNull
	KindUndefined
	KindFloat
)

// String names the kind as error messages do.
func (k Kind) String() string {
	switch k {
	case KindUint:
		return "unsigned integer"
	case KindNegInt:
		return "negative integer"
	case KindBytes:
		return "byte string"
	case KindText:
		return "text string"
	case KindArray:
		return "array"
	case KindMap:
		return "map"
	case KindTag:
		return "tag"
	case KindBool:
		return "boolean"
	case KindNull:
		return "null"
	case KindUndefined:
		return "undefined"
	case KindFloat:
		return "float"
	default:
		return "unknown kind"
	}
}
