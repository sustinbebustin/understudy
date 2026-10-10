package jsonx_test

import (
	"errors"
	"math"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

func encode(t *testing.T, order jsonx.Order, write func(e *jsonx.Encoder)) string {
	t.Helper()
	e := jsonx.NewEncoder(order)
	write(e)
	b, err := e.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	return string(b)
}

func writeSample(e *jsonx.Encoder) {
	e.BeginObject()
	e.Name("z")
	e.Int(-12)
	e.Name("a")
	e.BeginArray()
	e.Bool(true)
	e.Null()
	e.BeginObject()
	e.Name("y")
	e.String("1")
	e.Name("b")
	e.Float(0.5)
	e.EndObject()
	e.EndArray()
	e.Name("m")
	e.Number("1e400")
	e.EndObject()
}

func TestEncoderOrders(t *testing.T) {
	if got, want := encode(t, jsonx.WireOrder, writeSample), `{"z":-12,"a":[true,null,{"y":"1","b":0.5}],"m":1e400}`; got != want {
		t.Errorf("WireOrder = %s, want %s", got, want)
	}
	if got, want := encode(t, jsonx.SortedKeys, writeSample), `{"a":[true,null,{"b":0.5,"y":"1"}],"m":1e400,"z":-12}`; got != want {
		t.Errorf("SortedKeys = %s, want %s", got, want)
	}
}

func TestEncoderEscapesStrings(t *testing.T) {
	in := "q\"b\\s/\b\f\n\r\t\x00\x1f\x7f <>&  é \U0001F600 \xff \xed\xa0\x80"
	got := encode(t, jsonx.WireOrder, func(e *jsonx.Encoder) { e.String(in) })
	want := `"q\"b\\s/\b\f\n\r\t\u0000\u001f` + "\x7f <>&  é \U0001F600 " + `� \ud800"`
	if got != want {
		t.Errorf("String = %s\nwant     %s", got, want)
	}
}

func TestEncoderFloats(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{-1.5, "-1.5"},
		{1e20, "100000000000000000000"},
		{1e21, "1e+21"},
		{1e-6, "0.000001"},
		{1e-7, "1e-7"},
		{123456789.125, "123456789.125"},
		{math.MaxFloat64, "1.7976931348623157e+308"},
	}
	for _, tt := range tests {
		if got := encode(t, jsonx.WireOrder, func(e *jsonx.Encoder) { e.Float(tt.in) }); got != tt.want {
			t.Errorf("Float(%v) = %s, want %s", tt.in, got, tt.want)
		}
	}
	if got := encode(t, jsonx.WireOrder, func(e *jsonx.Encoder) { e.Float32(0.1) }); got != "0.1" {
		t.Errorf("Float32(0.1) = %s, want 0.1", got)
	}
}

func TestEncoderUsageErrors(t *testing.T) {
	tests := map[string]func(e *jsonx.Encoder){
		"value without a name":   func(e *jsonx.Encoder) { e.BeginObject(); e.Int(1) },
		"name outside an object": func(e *jsonx.Encoder) { e.BeginArray(); e.Name("a") },
		"name without a value":   func(e *jsonx.Encoder) { e.BeginObject(); e.Name("a"); e.EndObject() },
		"mismatched close":       func(e *jsonx.Encoder) { e.BeginObject(); e.EndArray() },
		"two top-level values":   func(e *jsonx.Encoder) { e.Null(); e.Null() },
		"unclosed object":        func(e *jsonx.Encoder) { e.BeginObject() },
		"nothing written":        func(*jsonx.Encoder) {},
		"NaN":                    func(e *jsonx.Encoder) { e.Float(math.NaN()) },
		"not a number":           func(e *jsonx.Encoder) { e.Number("0x10") },
		"nil document":           func(e *jsonx.Encoder) { e.Value(nil) },
	}
	for name, write := range tests {
		t.Run(name, func(t *testing.T) {
			e := jsonx.NewEncoder(jsonx.WireOrder)
			write(e)
			var uerr *jsonx.UsageError
			if _, err := e.Bytes(); !errors.As(err, &uerr) {
				t.Errorf("Bytes error = %v, want a *UsageError", err)
			}
		})
	}
}

func TestEncoderValueKeepsDuplicateNamesInOrder(t *testing.T) {
	v := document.Map{{Key: "b", Value: document.Number("2")}, {Key: "a", Value: document.Number("1")}, {Key: "b", Value: document.Number("3")}}
	got := encode(t, jsonx.SortedKeys, func(e *jsonx.Encoder) { e.Value(v) })
	if want := `{"a":1,"b":2,"b":3}`; got != want {
		t.Errorf("Value = %s, want %s", got, want)
	}
}
