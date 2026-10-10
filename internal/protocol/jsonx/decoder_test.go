package jsonx_test

import (
	"errors"
	"io"
	"reflect"
	"testing"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

func decodeValue(t *testing.T, in string) document.Value {
	t.Helper()
	d := jsonx.NewDecoder([]byte(in))
	v, err := d.ReadValue()
	if err != nil {
		t.Fatalf("ReadValue(%q): %v", in, err)
	}
	if err := d.End(); err != nil {
		t.Fatalf("End(%q): %v", in, err)
	}
	return v
}

// SQS answers a message body holding invalid UTF-8 with InvalidMessageContents,
// a validation error, so the decoder must hand the bytes on rather than fail.
func TestReadStringKeepsInvalidUTF8ForValidators(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"raw invalid byte", "\"a\xffb\"", "a\xffb"},
		{"truncated sequence", "\"\xe2\x82\"", "\xe2\x82"},
		{"lone high surrogate escape", `"x\ud800y"`, "x\xed\xa0\x80y"},
		{"lone low surrogate escape", `"\udfff"`, "\xed\xbf\xbf"},
		{"high surrogate before a non-surrogate escape", `"\ud800A"`, "\xed\xa0\x80A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := jsonx.NewDecoder([]byte(tt.in)).ReadString()
			if err != nil {
				t.Fatalf("ReadString: %v", err)
			}
			if got != tt.want {
				t.Errorf("ReadString = %q, want %q", got, tt.want)
			}
			if utf8.ValidString(got) {
				t.Errorf("utf8.ValidString(%q) = true; the validator could not flag the defect", got)
			}
		})
	}
}

// A surrogate pair the decoder does not join, because a half arrived as raw
// WTF-8 bytes, decodes to an invalid string. Re-encoding it, as the
// DynamoDB proxy does when it rewrites a body, must not turn it into a pair
// of escapes that the next decode joins into one valid character.
func TestUnjoinedSurrogatePairStaysInvalidAfterReencoding(t *testing.T) {
	tests := []struct{ name, in string }{
		{"escaped high, raw low", "\"\\ud800\xed\xb0\x80\""},
		{"raw high, escaped low", "\"\xed\xa0\x80\\udc00\""},
		{"raw high, raw low", "\"\xed\xa0\x80\xed\xb0\x80\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, err := jsonx.NewDecoder([]byte(tt.in)).ReadString()
			if err != nil {
				t.Fatalf("ReadString: %v", err)
			}
			if utf8.ValidString(first) {
				t.Fatalf("first decode %q is valid UTF-8", first)
			}
			wire := jsonx.AppendString(nil, first)
			second, err := jsonx.NewDecoder(wire).ReadString()
			if err != nil {
				t.Fatalf("ReadString(%s): %v", wire, err)
			}
			if utf8.ValidString(second) {
				t.Errorf("%q encoded as %s decodes to valid %q; the defect was lost", first, wire, second)
			}
		})
	}
}

func TestReadStringDecodesEscapes(t *testing.T) {
	in := `"\"\\\/\b\f\n\r\té😀 caf` + "é" + `"`
	got, err := jsonx.NewDecoder([]byte(in)).ReadString()
	if err != nil {
		t.Fatal(err)
	}
	if want := "\"\\/\b\f\n\r\té\U0001F600 café"; got != want {
		t.Errorf("ReadString = %q, want %q", got, want)
	}
}

func TestReadValueIsLenient(t *testing.T) {
	got := decodeValue(t, ` {"a": 1, "b": [true, null, -0.5e+10], "a": {"c": "d"}, "n": 123456789012345678901234567890} `)
	want := document.Map{
		{Key: "a", Value: document.Map{{Key: "c", Value: document.String("d")}}},
		{Key: "b", Value: document.List{document.Bool(true), document.Null{}, document.Number("-0.5e+10")}},
		{Key: "n", Value: document.Number("123456789012345678901234567890")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadValue = %#v\nwant %#v", got, want)
	}
}

func TestDecoderStreamsMembers(t *testing.T) {
	d := jsonx.NewDecoder([]byte(`{"Unknown":{"deep":[1,{"x":2}]},"QueueName":"q","Empty":[]}`))
	if err := d.BeginObject(); err != nil {
		t.Fatal(err)
	}
	var names []string
	for {
		name, ok, err := d.NextName()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		names = append(names, name)
		switch name {
		case "QueueName":
			if s, err := d.ReadString(); err != nil || s != "q" {
				t.Fatalf("ReadString = %q, %v", s, err)
			}
		case "Empty":
			if err := d.BeginArray(); err != nil {
				t.Fatal(err)
			}
			if more, err := d.NextElement(); err != nil || more {
				t.Fatalf("NextElement = %v, %v; want the empty array closed", more, err)
			}
		default:
			if err := d.Skip(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := d.End(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Unknown", "QueueName", "Empty"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestDecoderErrors(t *testing.T) {
	t.Run("kind mismatch", func(t *testing.T) {
		d := jsonx.NewDecoder([]byte(`  "5"`))
		_, err := d.ReadNumber()
		var kerr *jsonx.KindError
		if !errors.As(err, &kerr) || kerr.Want != jsonx.Number || kerr.Got != jsonx.String {
			t.Fatalf("ReadNumber error = %v, want a *KindError number/string", err)
		}
		if _, again := d.ReadString(); !errors.Is(again, err) {
			t.Errorf("second call error = %v, want the sticky first error", again)
		}
	})
	for _, in := range []string{`{"a":}`, `{`, `[1,]`, `{"a" 1}`, `tru`, `"abc`, `{} {}`, `{}x`} {
		t.Run(in, func(t *testing.T) {
			d := jsonx.NewDecoder([]byte(in))
			_, err := d.ReadValue()
			if err == nil {
				err = d.End()
			}
			serr, ok := errors.AsType[*jsonx.SyntaxError](err)
			if !ok || serr.Offset < 0 {
				t.Fatalf("error = %v, want a *SyntaxError with an offset", err)
			}
		})
	}
	t.Run("empty input is a truncated document", func(t *testing.T) {
		_, err := jsonx.NewDecoder(nil).Peek()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("Peek error = %v, want io.ErrUnexpectedEOF", err)
		}
	})
}
