package cbor_test

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
)

// valueGen draws Values nested at most depth levels: every kind, floats
// from raw bits so NaN payloads and signed zeros occur, any tag number.
func valueGen(depth int) *rapid.Generator[cbor.Value] {
	gens := []*rapid.Generator[cbor.Value]{
		rapid.Map(rapid.Uint64(), func(n uint64) cbor.Value { return cbor.Uint(n) }),
		rapid.Map(rapid.Uint64(), func(n uint64) cbor.Value { return cbor.NegInt(n) }),
		rapid.Map(rapid.SliceOf(rapid.Byte()), func(b []byte) cbor.Value { return cbor.Bytes(b) }),
		rapid.Map(rapid.String(), func(s string) cbor.Value { return cbor.Text(strings.ToValidUTF8(s, "?")) }),
		rapid.Map(rapid.Bool(), func(b bool) cbor.Value { return cbor.Bool(b) }),
		rapid.Just[cbor.Value](cbor.Null{}),
		rapid.Just[cbor.Value](cbor.Undefined{}),
		rapid.Map(rapid.Uint32(), func(b uint32) cbor.Value { return cbor.Float32(math.Float32frombits(b)) }),
		rapid.Map(rapid.Uint64(), func(b uint64) cbor.Value { return cbor.Float64(math.Float64frombits(b)) }),
	}
	if depth > 0 {
		inner := valueGen(depth - 1)
		entry := rapid.Custom(func(t *rapid.T) cbor.Entry {
			return cbor.Entry{Key: strings.ToValidUTF8(rapid.String().Draw(t, "key"), "?"), Value: inner.Draw(t, "value")}
		})
		gens = append(gens,
			rapid.Map(rapid.SliceOfN(inner, 0, 30), func(a []cbor.Value) cbor.Value { return cbor.Array(a) }),
			rapid.Map(rapid.SliceOfN(entry, 0, 30), func(m []cbor.Entry) cbor.Value { return cbor.Map(m) }),
			rapid.Custom(func(t *rapid.T) cbor.Value {
				return cbor.Tag{Number: rapid.Uint64().Draw(t, "tag"), Content: inner.Draw(t, "content")}
			}),
		)
	}
	return rapid.OneOf(gens...)
}

// sameValue compares two Values exactly: floats by their bits, so NaN
// payloads count and NaN equals itself.
func sameValue(a, b cbor.Value) bool {
	switch a := a.(type) {
	case cbor.Bytes:
		bb, ok := b.(cbor.Bytes)
		return ok && bytes.Equal(a, bb)
	case cbor.Array:
		bb, ok := b.(cbor.Array)
		if !ok || len(a) != len(bb) {
			return false
		}
		for i := range a {
			if !sameValue(a[i], bb[i]) {
				return false
			}
		}
		return true
	case cbor.Map:
		bb, ok := b.(cbor.Map)
		if !ok || len(a) != len(bb) {
			return false
		}
		for i := range a {
			if a[i].Key != bb[i].Key || !sameValue(a[i].Value, bb[i].Value) {
				return false
			}
		}
		return true
	case cbor.Tag:
		bb, ok := b.(cbor.Tag)
		return ok && a.Number == bb.Number && sameValue(a.Content, bb.Content)
	case cbor.Float32:
		bb, ok := b.(cbor.Float32)
		return ok && math.Float32bits(float32(a)) == math.Float32bits(float32(bb))
	case cbor.Float64:
		bb, ok := b.(cbor.Float64)
		return ok && math.Float64bits(float64(a)) == math.Float64bits(float64(bb))
	case cbor.Bool, cbor.NegInt, cbor.Null, cbor.Text, cbor.Uint, cbor.Undefined:
		return a == b
	}
	// a is nil.
	return b == nil
}

func TestEncodeThenDecodeRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := valueGen(4).Draw(t, "value")
		body, err := cbor.Encode(v)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		got, err := cbor.Decode(body)
		if err != nil {
			t.Fatalf("Decode(%x): %v", body, err)
		}
		if !sameValue(got, v) {
			t.Fatalf("round trip\n got %s\nwant %s", diag(got), diag(v))
		}
		again, err := cbor.Encode(got)
		if err != nil || !bytes.Equal(again, body) {
			t.Fatalf("re-encoding is not canonical: %x then %x (%v)", body, again, err)
		}
	})
}

// allVectors is every golden body, as seeds for mutation.
func allVectors(t *testing.T) [][]byte {
	t.Helper()
	var out [][]byte
	for _, set := range [][]vector{requestVectors, canonicalResponses, otherResponses} {
		for _, v := range set {
			out = append(out, unhex(t, v.hex))
		}
	}
	return out
}

// mutated draws a golden vector with a few random edits: a byte replaced,
// inserted or deleted, or the tail cut off.
func mutated(t *testing.T) *rapid.Generator[[]byte] {
	t.Helper()
	seeds := allVectors(t)
	return rapid.Custom(func(t *rapid.T) []byte {
		b := bytes.Clone(rapid.SampledFrom(seeds).Draw(t, "seed"))
		for range rapid.IntRange(1, 4).Draw(t, "edits") {
			if len(b) == 0 {
				break
			}
			i := rapid.IntRange(0, len(b)-1).Draw(t, "at")
			switch rapid.IntRange(0, 3).Draw(t, "edit") {
			case 0:
				b[i] = rapid.Byte().Draw(t, "byte")
			case 1:
				b = append(b[:i], append([]byte{rapid.Byte().Draw(t, "byte")}, b[i:]...)...)
			case 2:
				b = append(b[:i], b[i+1:]...)
			default:
				b = b[:i]
			}
		}
		return b
	})
}

// Decoding never panics: mutated vectors and arbitrary bytes either decode
// to a tree that re-encodes and decodes alike, or fail with a
// *MalformedError (wire-cbor.md section 7).
func TestDecodeNeverPanics(t *testing.T) {
	inputs := rapid.OneOf(mutated(t), rapid.SliceOf(rapid.Byte()))
	rapid.Check(t, func(t *rapid.T) {
		body := inputs.Draw(t, "body")
		v, err := cbor.Decode(body)
		if err != nil {
			if malformed, ok := errors.AsType[*cbor.MalformedError](err); !ok || malformed.Problem == 0 {
				t.Fatalf("Decode(%x) error = %v, want a *MalformedError naming its problem", body, err)
			}
			return
		}
		canonical, err := cbor.Encode(v)
		if err != nil {
			t.Fatalf("Encode of an accepted input: %v", err)
		}
		again, err := cbor.Decode(canonical)
		if err != nil || !sameValue(again, v) {
			t.Fatalf("accepted %x but its canonical form %x decodes to %v (%v)", body, canonical, again, err)
		}
	})
}

// Every millisecond instant in the years 0001 to 9999 reads back exactly,
// and from 1970 on, smithy-go's truncating int64(f*1e3) reads the same
// millisecond (wire-cbor.md 5.1).
func TestTimestampsRoundTripToTheMillisecond(t *testing.T) {
	const (
		minMs = -62135596800000
		maxMs = 253402300799999
	)
	rapid.Check(t, func(t *rapid.T) {
		ms := rapid.Int64Range(minMs, maxMs).Draw(t, "ms")
		w := cbor.NewWriter()
		w.Timestamp(time.UnixMilli(ms))
		body, err := w.Bytes()
		if err != nil {
			t.Fatalf("Bytes: %v", err)
		}
		got, err := cbor.NewReader(body).ReadTimestamp()
		if err != nil {
			t.Fatalf("ReadTimestamp(%x): %v", body, err)
		}
		if got.UnixMilli() != ms {
			t.Fatalf("instant %d ms read back as %d ms", ms, got.UnixMilli())
		}
		if ms >= 0 {
			v, err := cbor.Decode(body)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			tag, ok := v.(cbor.Tag)
			if !ok {
				t.Fatalf("Timestamp wrote %T, want a Tag", v)
			}
			f, ok := tag.Content.(cbor.Float64)
			if !ok {
				t.Fatalf("Timestamp content is %T, want Float64", tag.Content)
			}
			if trunc := int64(float64(f) * 1e3); trunc != ms {
				t.Fatalf("instant %d ms: smithy-go would read %d ms", ms, trunc)
			}
		}
	})
}
