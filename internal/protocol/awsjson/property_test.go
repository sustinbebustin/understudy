package awsjson_test

import (
	"math"
	"math/big"
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Year 1 to 9999: the range date-time and http-date can print.
const (
	minMillis = -62135596800000
	maxMillis = 253402300799999
)

func drawDocument(t *rapid.T, depth int, label string) document.Value {
	kinds := 4
	if depth > 0 {
		kinds = 6
	}
	switch rapid.IntRange(0, kinds-1).Draw(t, label) {
	case 0:
		return document.Bool(rapid.Bool().Draw(t, label+".bool"))
	case 1:
		return document.Number(rapid.StringMatching(`-?(0|[1-9][0-9]{0,20})(\.[0-9]{1,6})?([eE][+-]?[0-9]{1,3})?`).Draw(t, label+".number"))
	case 2:
		return document.String(rapid.String().Draw(t, label+".string"))
	case 3:
		return document.Null{}
	case 4:
		l := document.List{}
		for i := range rapid.IntRange(0, 3).Draw(t, label+".len") {
			l = append(l, drawDocument(t, depth-1, label+"["+strconv.Itoa(i)+"]"))
		}
		return l
	default:
		m := document.Map{}
		for _, k := range rapid.SliceOfNDistinct(rapid.String(), 0, 3, func(s string) string { return s }).Draw(t, label+".keys") {
			m = append(m, document.Entry{Key: k, Value: drawDocument(t, depth-1, label+"."+k)})
		}
		return m
	}
}

func drawFloat64(t *rapid.T, label string) float64 {
	return rapid.OneOf(rapid.Float64(), rapid.SampledFrom([]float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, math.MaxFloat64, math.SmallestNonzeroFloat64})).Draw(t, label)
}

func drawFloat32(t *rapid.T, label string) float32 {
	return rapid.OneOf(rapid.Float32(), rapid.SampledFrom([]float32{float32(math.NaN()), float32(math.Inf(1)), math.MaxFloat32})).Draw(t, label)
}

// drawValue draws a value of m's target that survives the wire unchanged:
// a top-level document is never null, which the wire cannot tell from an
// absent member.
func drawValue(t *rapid.T, m *smithy.Member, depth int, label string) any {
	switch m.Target.Type {
	case smithy.TypeString, smithy.TypeEnum:
		return rapid.String().Draw(t, label)
	case smithy.TypeBoolean:
		return rapid.Bool().Draw(t, label)
	case smithy.TypeByte:
		return rapid.Int8().Draw(t, label)
	case smithy.TypeShort:
		return rapid.Int16().Draw(t, label)
	case smithy.TypeInteger, smithy.TypeIntEnum:
		return rapid.Int32().Draw(t, label)
	case smithy.TypeLong:
		return rapid.Int64().Draw(t, label)
	case smithy.TypeFloat:
		return drawFloat32(t, label)
	case smithy.TypeDouble:
		return drawFloat64(t, label)
	case smithy.TypeBigInteger:
		n, _ := new(big.Int).SetString(rapid.StringMatching(`-?[1-9][0-9]{0,60}|0`).Draw(t, label), 10)
		return n
	case smithy.TypeBigDecimal:
		return document.Number(rapid.StringMatching(`-?(0|[1-9][0-9]{0,40})(\.[0-9]{1,20})?([eE][+-]?[0-9]{1,3})?`).Draw(t, label))
	case smithy.TypeTimestamp:
		ms := rapid.Int64Range(minMillis, maxMillis).Draw(t, label)
		if m.TimestampFormat == smithy.HTTPDate {
			ms -= ((ms % 1000) + 1000) % 1000 // http-date has whole seconds
		}
		return time.UnixMilli(ms).UTC()
	case smithy.TypeBlob:
		return rapid.SliceOf(rapid.Byte()).Draw(t, label)
	case smithy.TypeDocument:
		for {
			if d := drawDocument(t, 2, label); d != (document.Null{}) {
				return d
			}
		}
	case smithy.TypeList:
		elem := m.Target.Members[0]
		l := []any{}
		for i := range rapid.IntRange(0, 3).Draw(t, label+".len") {
			if m.Target.Sparse && rapid.Bool().Draw(t, label+".null") {
				l = append(l, nil)
				continue
			}
			l = append(l, drawValue(t, elem, depth, label+"["+strconv.Itoa(i)+"]"))
		}
		return l
	case smithy.TypeMap:
		out := map[string]any{}
		for _, k := range rapid.SliceOfNDistinct(rapid.String(), 0, 3, func(s string) string { return s }).Draw(t, label+".keys") {
			out[k] = drawValue(t, m.Target.Members[1], depth, label+"."+k)
		}
		return out
	case smithy.TypeStructure:
		return drawStruct(t, m.Target, depth-1, label)
	case smithy.TypeUnion:
		choices := m.Target.Members
		if depth <= 0 {
			choices = choices[:2] // no nested structure
		}
		um := rapid.SampledFrom(choices).Draw(t, label+".member")
		return dynUnion{member: um.Name, value: drawValue(t, um, depth, label+"."+um.Name)}
	case smithy.TypeUnit:
		t.Fatalf("test schema: member %s targets Unit", m.Name)
	}
	t.Fatalf("test schema: unhandled type %v", m.Target.Type)
	return nil
}

func drawStruct(t *rapid.T, s *smithy.Schema, depth int, label string) dynStruct {
	v := dynStruct{schema: s, fields: map[string]any{}}
	for _, m := range s.Members {
		if m.Target.Type == smithy.TypeStructure && depth <= 0 {
			continue
		}
		if rapid.Bool().Draw(t, label+"."+m.Name+"?") {
			v.fields[m.Name] = drawValue(t, m, depth, label+"."+m.Name)
		}
	}
	return v
}

// Encoding any value of the kitchen-sink schema and decoding the bytes
// gives the value back.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	f := newFixture(t)
	versions := []struct {
		version   awsjson.Version
		mediaType string
	}{
		{awsjson.V10, "application/x-amz-json-1.0"},
		{awsjson.V11, "application/x-amz-json-1.1"},
	}
	for _, tt := range versions {
		codec := awsjson.Codec{Version: tt.version}
		mediaType := tt.mediaType
		t.Run(tt.version.String(), func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) {
				in := drawStruct(t, f.sink.Output, 2, "in")
				resp, aerr := codec.Encode(f.sink, requestID, &in, 200)
				if aerr != nil {
					t.Fatalf("Encode: %v", aerr.AsError())
				}
				r := request("AmazonSQS.Echo", mediaType, resp.Body)
				out := dynStruct{schema: f.sink.Input}
				if aerr := codec.Decode(r, f.sink, &out); aerr != nil {
					t.Fatalf("Decode(%s): %s %q", resp.Body, aerr.ID.Shape, aerr.Message)
				}
				if !equalValues(out, in) {
					t.Fatalf("round trip changed the value\nbody %s\n got %#v\nwant %#v", resp.Body, out.fields, in.fields)
				}
			})
		})
	}
}

// Decoding any bytes never panics, and a body the codec cannot read is
// always SerializationException, never an internal failure.
func TestDecodeArbitraryBytes(t *testing.T) {
	f := newFixture(t)
	codec := awsjson.Codec{Version: awsjson.V10}
	rapid.Check(t, func(t *rapid.T) {
		var body []byte
		if rapid.Bool().Draw(t, "mutate") {
			in := drawStruct(t, f.sink.Output, 2, "in")
			resp, aerr := codec.Encode(f.sink, requestID, &in, 200)
			if aerr != nil {
				t.Fatalf("Encode: %v", aerr.AsError())
			}
			body = resp.Body
			for range rapid.IntRange(1, 3).Draw(t, "mutations") {
				if len(body) == 0 {
					break
				}
				i := rapid.IntRange(0, len(body)-1).Draw(t, "at")
				body[i] = rapid.Byte().Draw(t, "byte")
			}
		} else {
			body = rapid.SliceOf(rapid.Byte()).Draw(t, "body")
		}
		out := dynStruct{schema: f.sink.Input}
		aerr := codec.Decode(request("AmazonSQS.Echo", "application/x-amz-json-1.0", body), f.sink, &out)
		if aerr != nil && aerr.ID != awserr.SerializationException() {
			t.Fatalf("Decode(%q) = %s %q, want success or SerializationException", body, aerr.ID.Shape, aerr.Message)
		}
	})
}
