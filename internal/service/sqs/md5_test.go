package sqs

import (
	"bytes"
	"crypto/md5" //nolint:gosec // SQS digests are MD5 by protocol (sqs.md 6.3)
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// referenceAttributesMD5 encodes the sqs.md 6.3 layout step by step, apart
// from attributesMD5: names sorted, then per attribute the 4-byte
// big-endian length and bytes of the name and of the full data type, the
// transport byte (2 for the Binary family, 1 otherwise), and the length and
// bytes of the value.
func referenceAttributesMD5(attrs map[string]Attribute) string {
	if len(attrs) == 0 {
		return ""
	}
	names := make([]string, 0, len(attrs))
	for name := range attrs {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	put := func(b []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(b))) //nolint:gosec // generated values are small
		buf.Write(n[:])
		buf.Write(b)
	}
	for _, name := range names {
		a := attrs[name]
		put([]byte(name))
		put([]byte(a.DataType))
		if a.DataType == "Binary" || strings.HasPrefix(a.DataType, "Binary.") {
			buf.WriteByte(2)
			put(a.Binary)
		} else {
			buf.WriteByte(1)
			put([]byte(a.String))
		}
	}
	return fmt.Sprintf("%x", md5.Sum(buf.Bytes())) //nolint:gosec // protocol digest
}

type namedAttribute struct {
	name string
	attr Attribute
}

func genAttributes() *rapid.Generator[[]namedAttribute] {
	attr := rapid.Custom(func(t *rapid.T) Attribute {
		base := rapid.SampledFrom([]string{"String", "Number", "Binary"}).Draw(t, "base")
		dataType := base
		if rapid.Bool().Draw(t, "custom") {
			dataType += "." + rapid.StringMatching(`[A-Za-z0-9]{1,8}`).Draw(t, "label")
		}
		if base == "Binary" {
			return Attribute{DataType: dataType, Binary: rapid.SliceOfN(rapid.Byte(), 1, 32).Draw(t, "binary")}
		}
		return Attribute{DataType: dataType, String: rapid.StringN(1, 32, -1).Draw(t, "string")}
	})
	return rapid.Custom(func(t *rapid.T) []namedAttribute {
		names := rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Za-z0-9_.-]{1,16}`), 0, 10, rapid.ID).Draw(t, "names")
		out := make([]namedAttribute, len(names))
		for i, n := range names {
			out[i] = namedAttribute{name: n, attr: attr.Draw(t, "attribute")}
		}
		return out
	})
}

func toMap(attrs []namedAttribute) map[string]Attribute {
	out := make(map[string]Attribute, len(attrs))
	for _, a := range attrs {
		out[a.name] = a.attr
	}
	return out
}

// The digest follows the sqs.md 6.3 layout for any attributes, whatever
// order they were added in.
func TestAttributesMD5MatchesTheReferenceLayout(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrs := genAttributes().Draw(t, "attributes")
		got := attributesMD5(toMap(attrs))
		if want := referenceAttributesMD5(toMap(attrs)); got != want {
			t.Fatalf("attributesMD5 = %q, reference layout = %q", got, want)
		}
		shuffled := rapid.Permutation(attrs).Draw(t, "insertion order")
		if again := attributesMD5(toMap(shuffled)); again != got {
			t.Fatalf("attributesMD5 depends on insertion order: %q then %q", got, again)
		}
	})
}

// The reference encoder reproduces AWS-recorded vectors (sqs.md 6.3), so
// the property above compares against AWS's algorithm.
func TestReferenceAttributesMD5ReproducesAWSVectors(t *testing.T) {
	for want, attrs := range map[string]map[string]Attribute{
		"a45daa70926828a2f0a1db3418e6feb4": {"k": {DataType: "String", String: "x"}},
		"4c360f3fdafd970e05fae2f149d997f5": {
			"General": {DataType: "String", String: "Kenobi"},
			"Hello":   {DataType: "String", String: "There"},
			"Help.Me": {DataType: "String", String: "Me"},
		},
		"235c5c510d26fb653d073faed50ae77c": {"timestamp": {DataType: "Number", String: "1493147359900"}},
		"8cbe4db156db8a94db8b801b7addb984": {"attr1": {DataType: "Binary", Binary: []byte("traceparent\x1e00-774062d6c37081a5a0b9b5b88e30627c-2d2482211f6489da-01")}},
	} {
		if got := referenceAttributesMD5(attrs); got != want {
			t.Errorf("reference digest of %v = %s, want %s", attrs, got, want)
		}
	}
}
