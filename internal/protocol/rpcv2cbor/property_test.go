package rpcv2cbor_test

import (
	"bytes"
	"math"
	"math/big"
	"net/http"
	"reflect"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/rpcv2cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// kitchenSink has a member of every shape kind the codec serves, with
// collections of aggregates and a nested structure.
func kitchenSink() *smithy.Schema {
	sparse := listOf("test#SparseStrings", str)
	sparse.Sparse = true
	inner := structure("test#Inner", member("Name", str), member("Count", integer))
	choice := union("test#Choice", member("Text", str), member("Number", integer), member("Inner", inner))
	return structure("test#KitchenSink",
		member("String", str), member("Boolean", boolean), member("Byte", byteShape), member("Short", short),
		member("Integer", integer), member("Long", long), member("Float", float), member("Double", double),
		member("Timestamp", timestamp), member("Blob", blob), member("BigInteger", bigInt),
		member("List", stringList), member("SparseList", sparse), member("Map", mapOf("test#Counts", integer)),
		member("Inners", listOf("test#Inners", inner)), member("Choices", mapOf("test#Choices", choice)),
		member("Inner", inner), member("Choice", choice))
}

func finite32() *rapid.Generator[float32] {
	return rapid.Map(rapid.Uint32(), func(b uint32) float32 {
		if f := math.Float32frombits(b); !math.IsNaN(float64(f)) {
			return f
		}
		return 0
	})
}

func finite64() *rapid.Generator[float64] {
	return rapid.Map(rapid.Uint64(), func(b uint64) float64 {
		if f := math.Float64frombits(b); !math.IsNaN(f) {
			return f
		}
		return 0
	})
}

// sinkValue draws a value of kitchenSink with any subset of members set,
// in the forms the codec reads back: UTC millisecond timestamps, non-nil
// empty slices.
func sinkValue(s *smithy.Schema) *rapid.Generator[dynStruct] {
	member := func(name string) *smithy.Member {
		for _, m := range s.Members {
			if m.Name == name {
				return m
			}
		}
		panic("no member " + name)
	}
	inner := member("Inner").Target
	innerGen := rapid.Custom(func(t *rapid.T) any {
		return fields(inner, "Name", rapid.String().Draw(t, "name"), "Count", rapid.Int32().Draw(t, "count"))
	})
	choiceGen := rapid.Custom(func(t *rapid.T) any {
		switch rapid.IntRange(0, 2).Draw(t, "variant") {
		case 0:
			return dynUnion{member: "Text", value: rapid.String().Draw(t, "text")}
		case 1:
			return dynUnion{member: "Number", value: rapid.Int32().Draw(t, "number")}
		default:
			return dynUnion{member: "Inner", value: innerGen.Draw(t, "inner")}
		}
	})
	list := func(g *rapid.Generator[any]) *rapid.Generator[any] {
		return rapid.Map(rapid.SliceOfN(g, 0, 4), func(v []any) any { return append([]any{}, v...) })
	}
	entries := func(g *rapid.Generator[any]) *rapid.Generator[any] {
		return rapid.Custom(func(t *rapid.T) any {
			keys := rapid.SliceOfNDistinct(rapid.String(), 0, 4, rapid.ID).Draw(t, "keys")
			out := make([]entry, 0, len(keys))
			for _, k := range keys {
				out = append(out, entry{key: k, value: g.Draw(t, "value")})
			}
			return out
		})
	}
	anyOf := func(g any) *rapid.Generator[any] {
		switch g := g.(type) {
		case *rapid.Generator[string]:
			return rapid.Map(g, func(v string) any { return v })
		case *rapid.Generator[int32]:
			return rapid.Map(g, func(v int32) any { return v })
		}
		panic("anyOf: unsupported generator")
	}
	gens := map[string]*rapid.Generator[any]{
		"String":     anyOf(rapid.String()),
		"Boolean":    rapid.Map(rapid.Bool(), func(v bool) any { return v }),
		"Byte":       rapid.Map(rapid.Int8(), func(v int8) any { return v }),
		"Short":      rapid.Map(rapid.Int16(), func(v int16) any { return v }),
		"Integer":    anyOf(rapid.Int32()),
		"Long":       rapid.Map(rapid.Int64(), func(v int64) any { return v }),
		"Float":      rapid.Map(finite32(), func(v float32) any { return v }),
		"Double":     rapid.Map(finite64(), func(v float64) any { return v }),
		"Timestamp":  rapid.Map(rapid.Int64Range(-62135596800000, 253402300799999), func(ms int64) any { return time.UnixMilli(ms).UTC() }),
		"Blob":       rapid.Map(rapid.SliceOf(rapid.Byte()), func(v []byte) any { return append([]byte{}, v...) }),
		"BigInteger": rapid.Map(rapid.Int64(), func(v int64) any { return big.NewInt(v) }),
		"List":       list(anyOf(rapid.String())),
		"SparseList": list(rapid.OneOf(anyOf(rapid.String()), rapid.Just[any](nil))),
		"Map":        entries(anyOf(rapid.Int32())),
		"Inners":     list(innerGen),
		"Choices":    entries(choiceGen),
		"Inner":      innerGen,
		"Choice":     choiceGen,
	}
	return rapid.Custom(func(t *rapid.T) dynStruct {
		out := fields(s)
		for _, m := range s.Members {
			if rapid.Bool().Draw(t, "set "+m.Name) {
				out.fields[m.Name] = gens[m.Name].Draw(t, m.Name)
			}
		}
		return out
	})
}

// Whatever a handler returns, the request decoder reads the encoder's
// bytes back as the same value: the two agree on every shape kind.
func TestEncodeThenDecodeRoundTrips(t *testing.T) {
	sink := kitchenSink()
	op := corpusOp(sink)
	op.Output = sink
	values := sinkValue(sink)
	outer := t
	rapid.Check(t, func(t *rapid.T) {
		v := values.Draw(t, "value")
		resp, aerr := rpcv2cbor.Codec{}.Encode(op, "req-1", &v, http.StatusOK)
		if aerr != nil {
			t.Fatalf("Encode: %+v", aerr)
		}
		got := dynStruct{schema: sink}
		r := goRequest(outer, "Op", resp.Body)
		if aerr := (rpcv2cbor.Codec{}).Decode(r, op, &got); aerr != nil {
			t.Fatalf("Decode of %x: %s", resp.Body, aerr.Message)
		}
		if !reflect.DeepEqual(got.fields, v.fields) {
			t.Fatalf("round trip\n got %#v\nwant %#v", got.fields, v.fields)
		}
	})
}

// Decoding through the codec never panics: mutations of the Go client's
// request vectors decode or fail with the malformed-request answer, never
// InternalFailure.
func TestDecodeMutatedRequestsNeverPanics(t *testing.T) {
	_, cw := newRegistry(t)
	seeds := []struct{ op, hex string }{
		{"PutMetricAlarm", "bf6e416374696f6e73456e61626c6564f56c416c61726d416374696f6e739f782461726e3a6177733a736e733a75732d656173742d313a3030303030303030303030303a74ff70416c61726d4465736372697074696f6e646465736369416c61726d4e616d656874662d616c61726d72436f6d70617269736f6e4f70657261746f7274477265617465725468616e5468726573686f6c646a44696d656e73696f6e739fbf644e616d656951756575654e616d656556616c75656171ffff714576616c756174696f6e506572696f6473016a4d65747269634e616d657822417070726f78696d6174654e756d6265724f664d6573736167657356697369626c65694e616d657370616365674157532f53515366506572696f6419012c695374617469737469636353756d64546167739fbf634b657963656e766556616c756563646576ffbf634b6579647465616d6556616c75656178ffff695468726573686f6c64fb40240000000000007054726561744d697373696e6744617461676d697373696e67ff"},
		{"DescribeAlarms", "bf6a416c61726d4e616d65739f6874662d616c61726dff6a416c61726d54797065739f6b4d6574726963416c61726dffff"},
		{"DescribeAlarms", "bf6a4d61785265636f7264731864694e657874546f6b656e63746f6bff"},
		{"PutMetricData", "bf6a4d6574726963446174619fbf6a44696d656e73696f6e739fbf644e616d656553746167656556616c756563646576ffff6a4d65747269634e616d65644a6f62736954696d657374616d70c1fb41da55cd6947df3b64556e697465436f756e746556616c7565fb3ff8000000000000ffff694e616d65737061636563417070ff"},
	}
	outer := t
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.SampledFrom(seeds).Draw(t, "seed")
		b := unhex(outer, seed.hex)
		for range rapid.IntRange(1, 4).Draw(t, "edits") {
			if len(b) == 0 {
				break
			}
			i := rapid.IntRange(0, len(b)-1).Draw(t, "at")
			switch rapid.IntRange(0, 2).Draw(t, "edit") {
			case 0:
				b[i] = rapid.Byte().Draw(t, "byte")
			case 1:
				b = append(b[:i], b[i+1:]...)
			default:
				b = b[:i]
			}
		}
		op := operation(outer, cw, seed.op)
		r := goRequest(outer, seed.op, bytes.Clone(b))
		err := (rpcv2cbor.Codec{}).Decode(r, op, &dynStruct{schema: op.Input})
		if err != nil && err.ID != awserr.SerializationException() {
			t.Fatalf("Decode(%x) = %s: %s; want success or SerializationException", b, err.ID.Shape, err.Message)
		}
	})
}
