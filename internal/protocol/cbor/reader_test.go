package cbor_test

import (
	"encoding/binary"
	"errors"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
)

func TestDecodeRefusesMalformedInput(t *testing.T) {
	tests := []struct {
		name, hex string
		want      cbor.Problem
	}{
		{"empty input", "", cbor.Truncated},
		{"argument cut short", "1901", cbor.Truncated},
		{"string cut short", "6361", cbor.LengthExceedsInput},
		{"indefinite array without break", "9f01", cbor.Truncated},
		{"reserved additional information", "1c", cbor.ReservedInfo},
		{"indefinite integer", "1f", cbor.IndefiniteNotAllowed},
		{"indefinite tag", "df", cbor.IndefiniteNotAllowed},
		{"stray break", "ff", cbor.UnexpectedBreak},
		{"break in a definite array", "81ff", cbor.UnexpectedBreak},
		{"map ends after a key", "bf6161ff", cbor.UnexpectedBreak},
		{"text chunk in a byte string", "5f6161ff", cbor.InvalidChunk},
		{"nested indefinite chunk", "7f7fffff", cbor.InvalidChunk},
		{"integer map key", "a10102", cbor.NonTextKey},
		{"byte string map key", "a1416101", cbor.NonTextKey},
		{"invalid UTF-8", "62c328", cbor.InvalidUTF8},
		{"invalid UTF-8 split across chunks", "7f61c36128ff", cbor.InvalidUTF8},
		{"huge declared string", "5affffffff", cbor.LengthExceedsInput},
		{"huge declared array", "9bffffffffffffffff", cbor.LengthExceedsInput},
		{"map count beyond the input", "a2616101", cbor.LengthExceedsInput},
		{"simple value 0", "e0", cbor.UnsupportedSimple},
		{"one-byte simple value", "f820", cbor.UnsupportedSimple},
		{"trailing bytes", "0000", cbor.TrailingBytes},
		{"129 nested arrays", strings.Repeat("81", 129) + "00", cbor.TooDeep},
		{"129 nested tags", strings.Repeat("c1", 129) + "00", cbor.TooDeep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cbor.Decode(unhex(t, tt.hex))
			var malformed *cbor.MalformedError
			if !errors.As(err, &malformed) {
				t.Fatalf("Decode error = %v, want a *MalformedError", err)
			}
			if malformed.Problem != tt.want {
				t.Errorf("Problem = %v, want %v", malformed.Problem, tt.want)
			}
		})
	}
}

// Decoding costs memory in proportion to the input, not to the input times
// the nesting. In the hostile case MaxDepth definite arrays each declare as
// many elements as bytes are left, over a megabyte of zeros: each count
// fits the input left, but the second cannot fit beside what the first
// still owes, so it is refused before anything is sized from it.
func TestDecodeAllocatesInProportionToTheInput(t *testing.T) {
	const pad = 1 << 20
	nested := make([]byte, cbor.MaxDepth*5+pad)
	for i := range cbor.MaxDepth {
		nested[i*5] = 0x9a
		binary.BigEndian.PutUint32(nested[i*5+1:], uint32(len(nested)-(i+1)*5)) //nolint:gosec // the input is a few megabytes
	}
	flat := make([]byte, 5+pad)
	flat[0] = 0x9a
	binary.BigEndian.PutUint32(flat[1:], pad)
	tests := []struct {
		name string
		in   []byte
		want cbor.Problem
		ok   bool
	}{
		{"nested arrays each claiming the input left", nested, cbor.LengthExceedsInput, false},
		{"one array of a million zeros", flat, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := cbor.Decode(tt.in)
			runtime.ReadMemStats(&after)
			var malformed *cbor.MalformedError
			switch {
			case tt.ok && err != nil:
				t.Errorf("Decode: %v", err)
			case !tt.ok && (!errors.As(err, &malformed) || malformed.Problem != tt.want):
				t.Errorf("Decode error = %v, want %v", err, tt.want)
			}
			if grew := after.TotalAlloc - before.TotalAlloc; grew > 64*uint64(len(tt.in)) {
				t.Errorf("Decode of %d bytes allocated %d bytes, %dx the input; want at most 64x", len(tt.in), grew, grew/uint64(len(tt.in)))
			}
		})
	}
}

func TestDecodeAcceptsMaxDepth(t *testing.T) {
	if _, err := cbor.Decode(unhex(t, strings.Repeat("9f", cbor.MaxDepth)+"00"+strings.Repeat("ff", cbor.MaxDepth))); err != nil {
		t.Errorf("Decode of %d nested arrays: %v", cbor.MaxDepth, err)
	}
}

// atMaxDepth returns a Reader inside MaxDepth arrays, at a 129th.
func atMaxDepth(t *testing.T) *cbor.Reader {
	t.Helper()
	r := cbor.NewReader(unhex(t, strings.Repeat("81", cbor.MaxDepth+1)+"00"))
	for range cbor.MaxDepth {
		if err := r.BeginArray(); err != nil {
			t.Fatalf("BeginArray: %v", err)
		}
		if more, err := r.NextElement(); err != nil || !more {
			t.Fatalf("NextElement = %v, %v", more, err)
		}
	}
	return r
}

func TestStreamingReaderCapsDepth(t *testing.T) {
	var malformed *cbor.MalformedError
	if err := atMaxDepth(t).BeginArray(); !errors.As(err, &malformed) || malformed.Problem != cbor.TooDeep {
		t.Errorf("BeginArray past MaxDepth = %v, want TooDeep", err)
	}
	if err := atMaxDepth(t).Skip(); !errors.As(err, &malformed) || malformed.Problem != cbor.TooDeep {
		t.Errorf("Skip past MaxDepth = %v, want TooDeep", err)
	}
	if _, err := atMaxDepth(t).ReadValue(); !errors.As(err, &malformed) || malformed.Problem != cbor.TooDeep {
		t.Errorf("ReadValue past MaxDepth = %v, want TooDeep", err)
	}
}

// The non-minimal widths the corpus uses (S03, S10) read like minimal ones.
func TestReadInt64AcceptsEveryWidth(t *testing.T) {
	for _, h := range []string{"0a", "180a", "19000a", "1a0000000a", "1b000000000000000a"} {
		got, err := cbor.NewReader(unhex(t, h)).ReadInt64()
		if err != nil || got != 10 {
			t.Errorf("ReadInt64(%s) = %d, %v; want 10", h, got, err)
		}
	}
	for _, h := range []string{"29", "3809", "390009", "3a00000009", "3b0000000000000009"} {
		got, err := cbor.NewReader(unhex(t, h)).ReadInt64()
		if err != nil || got != -10 {
			t.Errorf("ReadInt64(%s) = %d, %v; want -10", h, got, err)
		}
	}
}

func TestReadInt64Range(t *testing.T) {
	tests := []struct {
		hex  string
		want int64
	}{
		{"1b7fffffffffffffff", math.MaxInt64},
		{"3b7fffffffffffffff", math.MinInt64},
	}
	for _, tt := range tests {
		if got, err := cbor.NewReader(unhex(t, tt.hex)).ReadInt64(); err != nil || got != tt.want {
			t.Errorf("ReadInt64(%s) = %d, %v; want %d", tt.hex, got, err, tt.want)
		}
	}
	for _, h := range []string{"1b8000000000000000", "3b8000000000000000", "3bffffffffffffffff"} {
		var outOfRange *cbor.RangeError
		if _, err := cbor.NewReader(unhex(t, h)).ReadInt64(); !errors.As(err, &outOfRange) {
			t.Errorf("ReadInt64(%s) error = %v, want a *RangeError", h, err)
		}
	}
}

func TestMismatchLeavesTheReaderAtTheItem(t *testing.T) {
	r := cbor.NewReader(unhex(t, "6161"))
	var mismatch *cbor.MismatchError
	if _, err := r.ReadInt64(); !errors.As(err, &mismatch) || mismatch.Got != cbor.KindText {
		t.Fatalf("ReadInt64 of text = %v, want a *MismatchError for a text string", err)
	}
	if s, err := r.ReadText(); err != nil || s != "a" {
		t.Errorf("ReadText after the mismatch = %q, %v; want \"a\"", s, err)
	}
	if err := r.End(); err != nil {
		t.Errorf("End: %v", err)
	}
}

// The message reaches clients inside a SerializationException, so each kind
// reads as English.
func TestMismatchErrorNamesWhatItFound(t *testing.T) {
	tests := []struct {
		got  cbor.Kind
		want string
	}{
		{cbor.KindUint, "an unsigned integer"},
		{cbor.KindNegInt, "a negative integer"},
		{cbor.KindBytes, "a byte string"},
		{cbor.KindText, "a text string"},
		{cbor.KindArray, "an array"},
		{cbor.KindMap, "a map"},
		{cbor.KindBool, "a boolean"},
		{cbor.KindNull, "a null"},
		{cbor.KindUndefined, "an undefined"},
		{cbor.KindFloat, "a float"},
	}
	for _, tt := range tests {
		err := &cbor.MismatchError{Offset: 5, Want: "a text string", Got: tt.got}
		if want := "cbor: expected a text string at byte 5, found " + tt.want; err.Error() != want {
			t.Errorf("Error() = %q, want %q", err.Error(), want)
		}
	}
	tagged := &cbor.MismatchError{Offset: 0, Want: "a timestamp", Got: cbor.KindTag, GotTag: 2}
	if want := "cbor: expected a timestamp at byte 0, found tag 2"; tagged.Error() != want {
		t.Errorf("Error() = %q, want %q", tagged.Error(), want)
	}
}

func TestReadFloat64AcceptsEveryNumber(t *testing.T) {
	tests := []struct {
		hex  string
		want float64
	}{
		{"0a", 10},
		{"29", -10},
		{"f94900", 10},
		{"fa41200000", 10},
		{"fb4024000000000000", 10},
	}
	for _, tt := range tests {
		if got, err := cbor.NewReader(unhex(t, tt.hex)).ReadFloat64(); err != nil || got != tt.want {
			t.Errorf("ReadFloat64(%s) = %v, %v; want %v", tt.hex, got, err, tt.want)
		}
	}
}

// halfReference computes the value of binary16 bits from the IEEE 754
// definition, independently of the decoder's bit manipulation.
func halfReference(h uint16) float64 {
	sign := 1.0
	if h&0x8000 != 0 {
		sign = -1
	}
	exp := int(h>>10) & 0x1f
	frac := float64(h & 0x3ff)
	switch exp {
	case 0:
		return sign * math.Ldexp(frac, -24)
	case 0x1f:
		if frac == 0 {
			return math.Inf(int(sign))
		}
		return math.NaN()
	default:
		return sign * math.Ldexp(1024+frac, exp-25)
	}
}

// Every one of the 65,536 binary16 patterns decodes to its IEEE value:
// zeros keep their sign, subnormals and infinities are exact, and a NaN
// keeps its sign and payload (wire-cbor.md 3.6 and 7).
func TestAllHalfFloatsDecodeToTheirIEEEValue(t *testing.T) {
	for i := range 1 << 16 {
		bits := uint16(i)
		v, err := cbor.Decode([]byte{0xf9, byte(bits >> 8), byte(bits)})
		if err != nil {
			t.Fatalf("Decode of half %#04x: %v", bits, err)
		}
		f, ok := v.(cbor.Float32)
		if !ok {
			t.Fatalf("half %#04x decoded to %T, want Float32", bits, v)
		}
		got := float32(f)
		want := halfReference(bits)
		gotBits := math.Float32bits(got)
		switch {
		case math.IsNaN(want):
			payload := uint32(bits&0x3ff) << 13
			if !math.IsNaN(float64(got)) || gotBits&0x7fffff != payload || gotBits>>31 != uint32(bits>>15) {
				t.Fatalf("half %#04x = %#08x, want NaN with payload %#x and sign %d", bits, gotBits, payload, bits>>15)
			}
		case float64(got) != want || math.Signbit(float64(got)) != math.Signbit(want):
			t.Fatalf("half %#04x = %v, want %v", bits, got, want)
		}
	}
}
