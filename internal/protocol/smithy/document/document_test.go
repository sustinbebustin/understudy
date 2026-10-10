package document_test

import (
	"encoding/json"
	"errors"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// ParseNumber must accept exactly the JSON number grammar; encoding/json's
// validator is the reference.
func TestParseNumberMatchesJSONGrammar(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(
			rapid.StringMatching(`-?(0|[1-9][0-9]{0,5})(\.[0-9]{1,4})?([eE][+-]?[0-9]{1,3})?`),
			rapid.StringMatching(`[-+0-9.eE]{0,8}`),
			rapid.String(),
		).Draw(t, "text")
		var probe json.Number
		wantOK := json.Unmarshal([]byte(s), &probe) == nil && string(probe) == s
		got, err := document.ParseNumber(s)
		if wantOK != (err == nil) {
			t.Fatalf("ParseNumber(%q) error = %v, want valid = %v", s, err, wantOK)
		}
		if err != nil {
			var target *document.NumberError
			if !errors.As(err, &target) || target.Text != s {
				t.Fatalf("ParseNumber(%q) error = %#v, want *NumberError", s, err)
			}
			return
		}
		if string(got) != s {
			t.Fatalf("ParseNumber(%q) = %q, want the text kept verbatim", s, got)
		}
	})
}

func TestNumberConversions(t *testing.T) {
	n, err := document.ParseNumber("9007199254740993")
	if err != nil {
		t.Fatal(err)
	}
	if i, err := n.Int64(); err != nil || i != 9007199254740993 {
		t.Errorf("Int64() = %d, %v; want the exact value", i, err)
	}
	if _, err := document.Number("1.5").Int64(); err == nil {
		t.Error("Int64() of 1.5 succeeded, want an error")
	}
	if f, err := document.Number("1.5e2").Float64(); err != nil || f != 150 {
		t.Errorf("Float64() = %v, %v; want 150", f, err)
	}
}
