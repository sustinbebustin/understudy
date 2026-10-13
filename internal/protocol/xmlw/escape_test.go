package xmlw_test

import (
	"bytes"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
)

// escapeCases is the AWS SDK escape table (smithy-go encoding/xml/escape.go)
// extended with numeric references for every other control character and for
// U+FFFE and U+FFFF, the package's choice. Whether S3 writes them the same way
// is UNVERIFIED.
var escapeCases = []struct {
	name, in, want string
}{
	{"plain text passes through", "photos/2006/January/sample.jpg", "photos/2006/January/sample.jpg"},
	{"ampersand", "a&b", "a&amp;b"},
	{"less than", "a<b", "a&lt;b"},
	{"greater than", "a>b", "a&gt;b"},
	{"double quote", `"etag"`, "&#34;etag&#34;"},
	{"apostrophe", "it's", "it&#39;s"},
	{"tab", "a\tb", "a&#x9;b"},
	{"line feed", "a\nb", "a&#xA;b"},
	{"carriage return", "a\rb", "a&#xD;b"},
	{"next line U+0085", "a\u0085b", "a&#x85;b"},
	{"line separator U+2028", "a\U00002028b", "a&#x2028;b"},
	{"NUL", "a\x00b", "a&#x0;b"},
	{"start of heading", "a\x01b", "a&#x1;b"},
	{"unit separator", "a\x1fb", "a&#x1F;b"},
	{"delete", "a\x7fb", "a&#x7F;b"},
	{"C1 control U+009F", "a\u009fb", "a&#x9F;b"},
	{"noncharacter U+FFFE", "a\U0000FFFEb", "a&#xFFFE;b"},
	{"noncharacter U+FFFF", "a\U0000FFFFb", "a&#xFFFF;b"},
	{"invalid UTF-8 byte", "a\xffb", "a\U0000FFFDb"},
	{"each invalid byte is replaced", "a\xc3\x28b", "a\U0000FFFD(b"},
	{"encoded U+FFFD passes through", "a\U0000FFFDb", "a\U0000FFFDb"},
	{"paragraph separator passes through", "a\U00002029b", "a\U00002029b"},
	{"non-BMP passes through", "a\U0001F600b", "a\U0001F600b"},
	{"empty", "", ""},
}

func TestTextUsesSDKEscapeTable(t *testing.T) {
	t.Parallel()
	for _, tc := range escapeCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			w := xmlw.New(&buf)
			w.Start("V")
			w.Text(tc.in)
			w.End()
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if got, want := buf.String(), "<V>"+tc.want+"</V>"; got != want {
				t.Errorf("Text(%q) wrote %q, want %q", tc.in, got, want)
			}
		})
	}
}

func TestAttrUsesSDKEscapeTable(t *testing.T) {
	t.Parallel()
	for _, tc := range escapeCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			w := xmlw.New(&buf)
			w.Start("V")
			w.Attr("a", tc.in)
			w.End()
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if got, want := buf.String(), `<V a="`+tc.want+`"></V>`; got != want {
				t.Errorf("Attr(%q) wrote %q, want %q", tc.in, got, want)
			}
		})
	}
}
