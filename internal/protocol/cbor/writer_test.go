package cbor_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
)

func written(t *testing.T, w *cbor.Writer) string {
	t.Helper()
	b, err := w.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	return hex.EncodeToString(b)
}

// A structure's member count is unknown until it closes; the Writer still
// emits a definite head in the smallest width.
func TestWriterCountsContainers(t *testing.T) {
	w := cbor.NewWriter()
	w.BeginMap()
	for i := range 24 {
		w.Key(fmt.Sprintf("k%02d", i))
		w.BeginArray()
		w.EndArray()
	}
	w.EndMap()
	got := written(t, w)
	if want := "b818"; got[:4] != want {
		t.Errorf("map head = %s, want %s (24 entries, one-byte count)", got[:4], want)
	}
	if want := 2 + 24*(4+1); len(got) != 2*want {
		t.Errorf("length = %d bytes, want %d", len(got)/2, want)
	}
}

func TestWriterEncodesMinimallyAndCanonically(t *testing.T) {
	tests := []struct {
		name  string
		write func(w *cbor.Writer)
		want  string
	}{
		{"small integer", func(w *cbor.Writer) { w.Int(23) }, "17"},
		{"one-byte integer", func(w *cbor.Writer) { w.Int(24) }, "1818"},
		{"two-byte integer", func(w *cbor.Writer) { w.Int(300) }, "19012c"},
		{"four-byte integer", func(w *cbor.Writer) { w.Uint(1 << 16) }, "1a00010000"},
		{"eight-byte integer", func(w *cbor.Writer) { w.Uint(1 << 32) }, "1b0000000100000000"},
		{"negative integer", func(w *cbor.Writer) { w.Int(-500) }, "3901f3"},
		{"double stays a double", func(w *cbor.Writer) { w.Float64(10) }, "fb4024000000000000"},
		{"single stays a single", func(w *cbor.Writer) { w.Float32(7.625) }, "fa40f40000"},
		{"text", func(w *cbor.Writer) { w.Text("tok") }, "63746f6b"},
		{"invalid UTF-8 becomes U+FFFD", func(w *cbor.Writer) { w.Text("a\xffb") }, "6561efbfbd62"},
		{"byte string", func(w *cbor.Writer) { w.ByteString([]byte("foo")) }, "43666f6f"},
		{"null", func(w *cbor.Writer) { w.Null() }, "f6"},
		{"booleans", func(w *cbor.Writer) {
			w.BeginArray()
			w.Bool(true)
			w.Bool(false)
			w.EndArray()
		}, "82f5f4"},
		// G16's whole-second instant: tag 1 and a double, never an integer.
		{"whole-second timestamp", func(w *cbor.Writer) { w.Timestamp(time.Unix(1767323040, 0)) }, "c1fb41da55cd68000000"},
		// G14's instant with milliseconds, which needs no nudge.
		{"millisecond timestamp", func(w *cbor.Writer) { w.Timestamp(time.UnixMilli(1767323045123)) }, "c1fb41da55cd6947df3b"},
		{"sub-millisecond precision is dropped", func(w *cbor.Writer) { w.Timestamp(time.UnixMilli(1767323045123).Add(999 * time.Microsecond)) }, "c1fb41da55cd6947df3b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := cbor.NewWriter()
			tt.write(w)
			if got := written(t, w); got != tt.want {
				t.Errorf("wrote %s, want %s", got, tt.want)
			}
		})
	}
}

func TestWriterReportsMisuse(t *testing.T) {
	tests := []struct {
		name  string
		write func(w *cbor.Writer)
	}{
		{"nothing written", func(*cbor.Writer) {}},
		{"two top-level items", func(w *cbor.Writer) { w.Int(1); w.Int(2) }},
		{"map value without a key", func(w *cbor.Writer) { w.BeginMap(); w.Int(1); w.EndMap() }},
		{"key without a value", func(w *cbor.Writer) { w.BeginMap(); w.Key("a"); w.EndMap() }},
		{"two keys in a row", func(w *cbor.Writer) { w.BeginMap(); w.Key("a"); w.Key("b") }},
		{"key in an array", func(w *cbor.Writer) { w.BeginArray(); w.Key("a") }},
		{"mismatched close", func(w *cbor.Writer) { w.BeginArray(); w.EndMap() }},
		{"container left open", func(w *cbor.Writer) { w.BeginArray() }},
		{"nil Value", func(w *cbor.Writer) { w.Value(nil) }},
		{"nil inside a Value", func(w *cbor.Writer) { w.Value(cbor.Array{nil}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := cbor.NewWriter()
			tt.write(w)
			var usage *cbor.UsageError
			if _, err := w.Bytes(); !errors.As(err, &usage) {
				t.Errorf("Bytes error = %v, want a *UsageError", err)
			}
		})
	}
}
