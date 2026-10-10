package aws_test

import (
	"encoding/json"
	"errors"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
)

func isTwelveASCIIDigits(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// nearAccountID draws strings close to valid account ids: right and wrong
// lengths, ASCII digits mixed with Unicode digits, letters and spaces.
func nearAccountID() *rapid.Generator[string] {
	char := rapid.SampledFrom([]rune("0123456789012345678901234567890123456789a ٣０-"))
	return rapid.Custom(func(t *rapid.T) string {
		n := rapid.IntRange(10, 14).Draw(t, "len")
		return string(rapid.SliceOfN(char, n, n).Draw(t, "runes"))
	})
}

func TestParseAccountIDAcceptsExactlyTwelveASCIIDigits(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(rapid.String(), nearAccountID(), rapid.StringMatching(`[0-9]{12}`)).Draw(t, "input")
		got, err := aws.ParseAccountID(s)
		if want := isTwelveASCIIDigits(s); want != (err == nil) {
			t.Fatalf("ParseAccountID(%q) error = %v, want valid = %v", s, err, want)
		}
		if err != nil {
			var target *aws.AccountIDError
			if !errors.As(err, &target) || target.Input != s {
				t.Fatalf("ParseAccountID(%q) error = %#v, want *AccountIDError naming the input", s, err)
			}
			return
		}
		if got.String() != s {
			t.Fatalf("ParseAccountID(%q).String() = %q", s, got.String())
		}
	})
}

func TestDefaultAccountIsAllZeros(t *testing.T) {
	if got := aws.DefaultAccount().String(); got != "000000000000" {
		t.Errorf("DefaultAccount() = %q, want 000000000000", got)
	}
}

func TestAccountIDJSONRoundTrip(t *testing.T) {
	type record struct{ Account aws.AccountID }
	in := record{Account: aws.DefaultAccount()}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `{"Account":"000000000000"}` {
		t.Fatalf("Marshal = %s", data)
	}
	var out record
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
	if err := json.Unmarshal([]byte(`{"Account":"12345"}`), &out); err == nil {
		t.Error("Unmarshal of a 5-digit account succeeded, want an error")
	}
}
