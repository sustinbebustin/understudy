package cbor_test

import (
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
)

// diag renders v in RFC 8949 section 8 diagnostic notation without length
// markers, since a Value does not keep them: floats carry their width
// suffix (_2 single, _3 double), a byte string is h'hex'.
func diag(v cbor.Value) string {
	var b strings.Builder
	writeDiag(&b, v)
	return b.String()
}

func writeDiag(b *strings.Builder, v cbor.Value) {
	switch v := v.(type) {
	case cbor.Uint:
		b.WriteString(strconv.FormatUint(uint64(v), 10))
	case cbor.NegInt:
		if uint64(v) == math.MaxUint64 {
			b.WriteString("-18446744073709551616")
		} else {
			b.WriteString("-" + strconv.FormatUint(uint64(v)+1, 10))
		}
	case cbor.Bytes:
		b.WriteString("h'" + hex.EncodeToString(v) + "'")
	case cbor.Text:
		b.WriteString(strconv.Quote(string(v)))
	case cbor.Array:
		b.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			writeDiag(b, e)
		}
		b.WriteByte(']')
	case cbor.Map:
		b.WriteByte('{')
		for i, e := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Quote(e.Key) + ": ")
			writeDiag(b, e.Value)
		}
		b.WriteByte('}')
	case cbor.Tag:
		b.WriteString(strconv.FormatUint(v.Number, 10) + "(")
		writeDiag(b, v.Content)
		b.WriteByte(')')
	case cbor.Bool:
		b.WriteString(strconv.FormatBool(bool(v)))
	case cbor.Null:
		b.WriteString("null")
	case cbor.Undefined:
		b.WriteString("undefined")
	case cbor.Float32:
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32) + "_2")
	case cbor.Float64:
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 64) + "_3")
	case nil:
		b.WriteString("<nil>")
	}
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex in test: %v", err)
	}
	return b
}
