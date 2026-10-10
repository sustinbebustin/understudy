package xmlw_test

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
)

// anyString draws arbitrary strings: random Unicode, the characters the escape
// table singles out, and raw bytes that are not valid UTF-8.
func anyString() *rapid.Generator[string] {
	special := rapid.SampledFrom([]string{
		"&", "<", ">", `"`, "'", "]]>", "&amp;", "\t", "\n", "\r", "\r\n",
		"\x00", "\x01", "\x1f", "\x7f", "\u0085", "\u009f", "\U00002028", "\U00002029",
		"\U0000FFFD", "\U0000FFFE", "\U0000FFFF", "\U0010FFFF", "\xff", "\xc3", "\xed\xa0\x80",
	})
	raw := rapid.Map(rapid.SliceOf(rapid.Byte()), func(b []byte) string { return string(b) })
	piece := rapid.OneOf(rapid.String(), special, raw)
	return rapid.Map(rapid.SliceOf(piece), func(p []string) string { return strings.Join(p, "") })
}

// xmlString draws strings whose characters are all allowed by XML 1.0, keeping
// invalid UTF-8 bytes, which the writer replaces with U+FFFD.
func xmlString() *rapid.Generator[string] {
	return rapid.Map(anyString(), func(s string) string {
		var b strings.Builder
		for i := 0; i < len(s); {
			r, width := utf8.DecodeRuneInString(s[i:])
			if isXMLChar(r) || r == utf8.RuneError && width == 1 {
				b.WriteString(s[i : i+width])
			}
			i += width
		}
		return b.String()
	})
}

// isXMLChar is the XML 1.0 Char production.
func isXMLChar(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' ||
		r >= 0x20 && r <= 0xD7FF ||
		r >= 0xE000 && r <= 0xFFFD ||
		r >= 0x10000 && r <= unicode.MaxRune
}

// replaced is s with each invalid UTF-8 byte replaced by U+FFFD, the form the
// writer emits.
func replaced(s string) string {
	return string([]rune(s))
}

func writeTextAndAttr(t *rapid.T, s string) []byte {
	var buf bytes.Buffer
	w := xmlw.New(&buf)
	w.Start("V")
	w.Attr("a", s)
	w.Text(s)
	w.End()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

// Character data and attribute values survive a write and a decode, with
// invalid UTF-8 replaced by U+FFFD and CR, tab and line feed preserved.
func TestTextAndAttrRoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := xmlString().Draw(t, "s")
		doc := writeTextAndAttr(t, s)
		n, err := xmlw.Decode(bytes.NewReader(doc), int64(len(doc)))
		if err != nil {
			t.Fatalf("Decode(%q): %v", doc, err)
		}
		want := replaced(s)
		if n.Text != want {
			t.Fatalf("text %q decoded to %q, want %q", s, n.Text, want)
		}
		if got, _ := n.Attr("a"); got != want {
			t.Fatalf("attribute %q decoded to %q, want %q", s, got, want)
		}
	})
}

// Characters XML 1.0 forbids are still written as numeric references, per the
// T06 spec (S3's own output for them is UNVERIFIED), and a conforming parser
// rejects the document; everything else decodes.
func TestOnlyXMLCharactersDecode(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := anyString().Draw(t, "s")
		doc := writeTextAndAttr(t, s)
		legal := !strings.ContainsFunc(replaced(s), func(r rune) bool { return !isXMLChar(r) })
		_, err := xmlw.Decode(bytes.NewReader(doc), int64(len(doc)))
		malformed, isMalformed := errors.AsType[*xmlw.MalformedXMLError](err)
		switch {
		case legal && err != nil:
			t.Fatalf("Decode of %q failed: %v", doc, err)
		case !legal && !isMalformed:
			t.Fatalf("Decode of %q returned %v, want *xmlw.MalformedXMLError", doc, err)
		case !legal && malformed.Reason == "":
			t.Fatal("MalformedXMLError has no reason")
		}
	})
}

var reference = regexp.MustCompile(`^&(amp|lt|gt|#34|#39|#x[0-9A-F]+);`)

// Whatever the input, the only markup characters in the output are the
// writer's own, every & starts a reference, and no control character or
// non-XML character appears raw.
func TestEscapedOutputIsSafe(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := anyString().Draw(t, "s")
		doc := writeTextAndAttr(t, s)
		if !utf8.Valid(doc) {
			t.Fatalf("output %q is not valid UTF-8", doc)
		}
		for _, c := range []struct {
			char string
			want int
		}{{"<", 2}, {">", 2}, {`"`, 2}, {"'", 0}} {
			if got := strings.Count(string(doc), c.char); got != c.want {
				t.Fatalf("output %q has %d %s, want %d", doc, got, c.char, c.want)
			}
		}
		for i, r := range string(doc) {
			switch {
			case unicode.IsControl(r), !isXMLChar(r), r == '\U00002028':
				t.Fatalf("output %q has raw %U at %d", doc, r, i)
			case r == '&' && !reference.Match(doc[i:]):
				t.Fatalf("output %q has an unescaped & at %d", doc, i)
			}
		}
	})
}
