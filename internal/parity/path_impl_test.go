package parity

import (
	"errors"
	"testing"
)

// Spellings the grammar refuses beyond TestParsePathRefusesWhatIsNotAPath:
// an exchange index with a leading zero, a header without a name or with a
// character no HTTP header name holds, a dangling ~.
func TestParsePathRefusesMoreSpellings(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"exchange/01/status", "exchange//status", "header/", "header/x y", "body/a~", "exchange/0/exchange/1/status"} {
		_, err := ParsePath(s)
		if pe, ok := errors.AsType[*PathError](err); !ok || pe.Text != s || pe.Reason == "" {
			t.Errorf("ParsePath(%q) error = %v; want a *PathError naming it with a reason", s, err)
		}
	}
	// RFC 6901 allows an empty reference token: the member "" of the root.
	if _, err := ParsePath("body/"); err != nil {
		t.Errorf("ParsePath(%q) = %v; want it accepted", "body/", err)
	}
}

// The path of a JSON member escapes ~ and / as RFC 6901 does, and an XML
// sibling after the first carries its 0-based position.
func TestPathTokens(t *testing.T) {
	t.Parallel()
	cases := map[Path]Path{
		bodyPath.member("a/b~c"):         "body/a~1b~0c",
		bodyPath.child(xmlToken("m", 0)): "body/m",
		bodyPath.child(xmlToken("m", 2)): "body/m[2]",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q; want %q", got, want)
		}
	}
}
