package jsonx_test

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// decodedString draws strings ReadString can produce: any Unicode text with
// WTF-8 surrogates mixed in, alone or as adjacent high and low halves (which
// the decoder yields when a half arrived as raw bytes).
func decodedString() *rapid.Generator[string] {
	special := rapid.SampledFrom([]string{
		"\"", "\\", "/", "\b", "\f", "\n", "\r", "\t", "\x00", "\x1f", "\x7f",
		" ", "é", "\U0001F600", "�", "\xed\xa0\x80", "\xed\xbf\xbf", "\xed\xb0\x80",
	})
	piece := rapid.OneOf(rapid.String(), special)
	return rapid.Map(rapid.SliceOf(piece), func(p []string) string { return strings.Join(p, "") })
}

func isWTF8High(s string) bool {
	return len(s) >= 3 && s[0] == 0xED && s[1] >= 0xA0 && s[1] <= 0xAF && s[2] >= 0x80 && s[2] <= 0xBF
}

func isWTF8Low(s string) bool {
	return len(s) >= 3 && s[0] == 0xED && s[1] >= 0xB0 && s[1] <= 0xBF && s[2] >= 0x80 && s[2] <= 0xBF
}

func hasWTF8Surrogate(s string) bool {
	for i := range len(s) {
		if isWTF8High(s[i:]) || isWTF8Low(s[i:]) {
			return true
		}
	}
	return false
}

// afterReencoding is what ReadString returns for a decodedString s after
// AppendString wrote it: s itself, except that a WTF-8 high surrogate
// directly followed by a low one comes back as U+FFFD, so the pair cannot
// rejoin into a valid character.
func afterReencoding(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if isWTF8High(s[i:]) && isWTF8Low(s[i+3:]) {
			b.WriteRune(utf8.RuneError)
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// mapStrings returns v with f applied to every string and member name.
func mapStrings(v document.Value, f func(string) string) document.Value {
	switch v := v.(type) {
	case document.String:
		return document.String(f(string(v)))
	case document.List:
		out := make(document.List, len(v))
		for i, x := range v {
			out[i] = mapStrings(x, f)
		}
		return out
	case document.Map:
		out := make(document.Map, len(v))
		for i, e := range v {
			out[i] = document.Entry{Key: f(e.Key), Value: mapStrings(e.Value, f)}
		}
		return out
	case document.Null, document.Bool, document.Number:
		// Holds no string: returned as it is.
	}
	return v
}

func number() *rapid.Generator[document.Number] {
	return rapid.Map(rapid.StringMatching(`-?(0|[1-9][0-9]{0,25})(\.[0-9]{1,8})?([eE][+-]?[0-9]{1,4})?`), func(s string) document.Number {
		return document.Number(s)
	})
}

func value(depth int) *rapid.Generator[document.Value] {
	kinds := make([]*rapid.Generator[document.Value], 0, 6)
	kinds = append(kinds,
		rapid.Just[document.Value](document.Null{}),
		rapid.Map(rapid.Bool(), func(b bool) document.Value { return document.Bool(b) }),
		rapid.Map(number(), func(n document.Number) document.Value { return n }),
		rapid.Map(decodedString(), func(s string) document.Value { return document.String(s) }),
	)
	if depth == 0 {
		return rapid.OneOf(kinds...)
	}
	list := rapid.Map(rapid.SliceOfN(value(depth-1), 0, 4), func(l []document.Value) document.Value { return document.List(l) })
	object := rapid.Custom(func(t *rapid.T) document.Value {
		// Distinct after re-encoding too, so a decoded object has no
		// duplicate names.
		keys := rapid.SliceOfNDistinct(decodedString(), 0, 4, afterReencoding).Draw(t, "keys")
		m := make(document.Map, 0, len(keys))
		for _, k := range keys {
			m = append(m, document.Entry{Key: k, Value: value(depth-1).Draw(t, "member")})
		}
		return m
	})
	return rapid.OneOf(append(kinds, list, object)...)
}

func encodeValue(t *rapid.T, order jsonx.Order, v document.Value) []byte {
	e := jsonx.NewEncoder(order)
	e.Value(v)
	b, err := e.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	return b
}

func decodeAll(t *rapid.T, b []byte) document.Value {
	d := jsonx.NewDecoder(b)
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	return v
}

// sorted returns v with every map's entries sorted by key, as SortedKeys
// writes them.
func sorted(v document.Value) document.Value {
	switch v := v.(type) {
	case document.List:
		out := make(document.List, len(v))
		for i, x := range v {
			out[i] = sorted(x)
		}
		return out
	case document.Map:
		out := make(document.Map, len(v))
		for i, e := range v {
			out[i] = document.Entry{Key: e.Key, Value: sorted(e.Value)}
		}
		slices.SortStableFunc(out, func(a, b document.Entry) int { return cmp.Compare(a.Key, b.Key) })
		return out
	case document.Null, document.Bool, document.Number, document.String:
		// A scalar holds no map: returned as it is.
	}
	return v
}

// reversed returns v with every map's entries in reverse order.
func reversed(v document.Value) document.Value {
	switch v := v.(type) {
	case document.List:
		out := make(document.List, len(v))
		for i, x := range v {
			out[i] = reversed(x)
		}
		return out
	case document.Map:
		out := make(document.Map, len(v))
		for i, e := range v {
			out[len(v)-1-i] = document.Entry{Key: e.Key, Value: reversed(e.Value)}
		}
		return out
	case document.Null, document.Bool, document.Number, document.String:
		// A scalar holds no map: returned as it is.
	}
	return v
}

func TestValueRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := value(3).Draw(t, "value")
		wire := encodeValue(t, jsonx.WireOrder, v)
		if got, want := decodeAll(t, wire), mapStrings(v, afterReencoding); !reflect.DeepEqual(got, want) {
			t.Fatalf("WireOrder round trip:\n got %#v\nwant %#v\njson %s", got, want, wire)
		}
		canonical := encodeValue(t, jsonx.SortedKeys, v)
		if got, want := decodeAll(t, canonical), mapStrings(sorted(v), afterReencoding); !reflect.DeepEqual(got, want) {
			t.Fatalf("SortedKeys round trip:\n got %#v\nwant %#v\njson %s", got, want, canonical)
		}
		if again := encodeValue(t, jsonx.SortedKeys, reversed(v)); !bytes.Equal(again, canonical) {
			t.Fatalf("SortedKeys depends on member order: %s then %s", canonical, again)
		}
	})
}

// A string ReadString produced reads back after AppendString as itself,
// apart from the broken surrogate pairs afterReencoding describes, and a
// string that failed utf8.ValidString still fails it: the flag validators
// read survives a proxy rewrite.
func TestReencodingKeepsInvalidStringsInvalid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := decodedString().Draw(t, "s")
		wire := jsonx.AppendString(nil, s)
		got, err := jsonx.NewDecoder(wire).ReadString()
		if err != nil {
			t.Fatalf("ReadString(%s): %v", wire, err)
		}
		if want := afterReencoding(s); got != want {
			t.Fatalf("%q encoded as %s reads back as %q, want %q", s, wire, got, want)
		}
		if utf8.ValidString(got) != utf8.ValidString(s) {
			t.Fatalf("utf8.ValidString changed from %t to %t: %q encoded as %s reads back as %q", utf8.ValidString(s), utf8.ValidString(got), s, wire, got)
		}
	})
}

// The encoder's output is valid UTF-8 and syntactically valid JSON,
// whatever bytes the strings held. A strict parser also accepts it unless a
// string held a WTF-8 surrogate: its lone \u escape is how the defect
// reaches the next decoder.
func TestEncoderOutputIsValidJSON(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(rapid.String(), decodedString(), rapid.Map(rapid.SliceOf(rapid.Byte()), func(b []byte) string { return string(b) })).Draw(t, "s")
		e := jsonx.NewEncoder(jsonx.WireOrder)
		e.BeginObject()
		e.Name(s)
		e.String(s)
		e.EndObject()
		b, err := e.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.Valid(b) || !jsontext.Value(b).IsValid(jsontext.AllowInvalidUTF8(true)) {
			t.Fatalf("encoded %q is not valid JSON: %q", s, b)
		}
		if !hasWTF8Surrogate(s) && !jsontext.Value(b).IsValid(jsontext.AllowInvalidUTF8(false)) {
			t.Fatalf("encoded %q, which holds no surrogate, is not strictly valid JSON: %s", s, b)
		}
	})
}

func TestDecodingArbitraryBytesNeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.OneOf(
			rapid.SliceOf(rapid.Byte()),
			rapid.Map(rapid.StringMatching(`[\[\]{}",:0-9a-z\\ .eE+-]{0,40}`), func(s string) []byte { return []byte(s) }),
			rapid.Custom(func(t *rapid.T) []byte { return encodeValue(t, jsonx.WireOrder, value(2).Draw(t, "value")) }),
		).Draw(t, "input")
		cut := rapid.IntRange(0, len(in)).Draw(t, "cut")
		d := jsonx.NewDecoder(in[:cut])
		_, err := d.ReadValue()
		if err == nil {
			err = d.End()
		}
		if serr, ok := errors.AsType[*jsonx.SyntaxError](err); err != nil && (!ok || serr.Offset < 0) {
			t.Fatalf("decoding %q failed with %T (%v), want a *SyntaxError", in[:cut], err, err)
		}
	})
}
