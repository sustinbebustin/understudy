package cbor

import (
	"encoding/binary"
	"math"
)

// MaxDepth is how deep arrays, maps and tags may nest. smithy-go and
// smithy-rs cap nesting at 128 (wire-cbor.md 3.6).
const MaxDepth = 128

// Major types (RFC 8949 section 3.1).
const (
	majorUint byte = iota
	majorNegInt
	majorBytes
	majorText
	majorArray
	majorMap
	majorTag
	majorSimple
)

// Additional information values (RFC 8949 section 3).
const (
	infoUint8      = 24 // a one-byte argument follows
	infoUint16     = 25 // a two-byte argument follows
	infoUint32     = 26 // a four-byte argument follows
	infoUint64     = 27 // an eight-byte argument follows
	infoIndefinite = 31 // an indefinite length, or the break
	breakByte      = 0xff
)

// Simple values and float widths of major type 7.
const (
	simpleFalse     = 20
	simpleTrue      = 21
	simpleNull      = 22
	simpleUndefined = 23
	floatHalf       = 25
	floatSingle     = 26
	floatDouble     = 27
)

// head is the initial byte and argument of a data item.
type head struct {
	major byte
	info  byte
	// arg is the argument: an integer, a length or count, a tag number or
	// a float's bits.
	arg        uint64
	indefinite bool
}

// kind is the Kind of the item the head starts.
func (h head) kind() Kind {
	switch h.major {
	case majorUint:
		return KindUint
	case majorNegInt:
		return KindNegInt
	case majorBytes:
		return KindBytes
	case majorText:
		return KindText
	case majorArray:
		return KindArray
	case majorMap:
		return KindMap
	case majorTag:
		return KindTag
	}
	switch h.info {
	case simpleFalse, simpleTrue:
		return KindBool
	case simpleNull:
		return KindNull
	case simpleUndefined:
		return KindUndefined
	default:
		return KindFloat
	}
}

// float is the value of a float head, widened to float64.
func (h head) float() float64 {
	if h.info == floatDouble {
		return math.Float64frombits(h.arg)
	}
	return float64(h.float32())
}

// float32 is the value of a half or single head. It never passes through
// float64, which would quiet a signaling NaN and lose its payload bits.
func (h head) float32() float32 {
	if h.info == floatHalf {
		return halfToFloat32(uint16(h.arg)) //nolint:gosec // a half head's argument is two bytes
	}
	return math.Float32frombits(uint32(h.arg)) //nolint:gosec // a single head's argument is four bytes
}

// halfToFloat32 converts IEEE 754 binary16 bits to the float32 of the same
// value; binary32 holds every binary16 value, NaN payloads included.
func halfToFloat32(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h & 0x3ff)
	switch exp {
	case 0:
		// Zero or subnormal: frac * 2^-24, exact in binary32.
		f := float32(frac) / (1 << 24)
		return math.Float32frombits(math.Float32bits(f) | sign)
	case 0x1f:
		// Infinity or NaN; the payload moves to the top of the fraction.
		return math.Float32frombits(sign | 0x7f800000 | frac<<13)
	default:
		// Rebias the exponent from 15 to 127.
		return math.Float32frombits(sign | (exp+112)<<23 | frac<<13)
	}
}

// appendHead appends the head of major type major with argument arg in its
// smallest width.
func appendHead(b []byte, major byte, arg uint64) []byte {
	m := major << 5
	switch {
	case arg < infoUint8:
		return append(b, m|byte(arg))
	case arg <= math.MaxUint8:
		return append(b, m|infoUint8, byte(arg))
	case arg <= math.MaxUint16:
		return binary.BigEndian.AppendUint16(append(b, m|infoUint16), uint16(arg))
	case arg <= math.MaxUint32:
		return binary.BigEndian.AppendUint32(append(b, m|infoUint32), uint32(arg))
	default:
		return binary.BigEndian.AppendUint64(append(b, m|infoUint64), arg)
	}
}
