package form_test

import (
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/form"
)

func parse(t *testing.T, s string) *form.Values {
	t.Helper()
	v, err := form.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return v
}

func get(t *testing.T, v *form.Values, key string) string {
	t.Helper()
	got, ok := v.Get(key)
	if !ok {
		t.Fatalf("Get(%q): absent", key)
	}
	return got
}

// wire-json-query.md 7.1: Go v2 and botocore send spaces as +, JS v3 as
// %20, and every reserved character as %XX.
func TestParseDecodesPlusAndPercent(t *testing.T) {
	t.Parallel()
	v := parse(t, "Action=Publish&Message=a+b%2Bc%26d%3D%C3%A9&Subject=x%20y")
	if got := get(t, v, "Message"); got != "a b+c&d=é" {
		t.Errorf("Message = %q", got)
	}
	if got := get(t, v, "Subject"); got != "x y" {
		t.Errorf("Subject = %q", got)
	}
	if got := get(t, v, "Action"); got != "Publish" {
		t.Errorf("Action = %q", got)
	}
}

func TestParseKeysAreDecodedToo(t *testing.T) {
	t.Parallel()
	v := parse(t, "Attributes.entry.1.key=a&Attributes%2Eentry.1.value=b")
	if got := get(t, v, "Attributes.entry.1.value"); got != "b" {
		t.Errorf("value = %q", got)
	}
}

// A key without = is present with the empty value, and empty pairs are
// skipped, as url.ParseQuery reads them.
func TestParseBareKeysAndEmptyPairs(t *testing.T) {
	t.Parallel()
	v := parse(t, "&&ListArg&Version=2020-01-08&")
	if got := get(t, v, "ListArg"); got != "" {
		t.Errorf("ListArg = %q", got)
	}
	if _, ok := v.Get("Missing"); ok {
		t.Error("Missing is present")
	}
}

// The first value of a repeated key wins, as a servlet's getParameter
// reads it.
func TestParseDuplicateKeysKeepTheFirst(t *testing.T) {
	t.Parallel()
	v := parse(t, "QueueUrl=first&QueueUrl=second")
	if got := get(t, v, "QueueUrl"); got != "first" {
		t.Errorf("QueueUrl = %q, want first", got)
	}
	v.Add("QueueUrl", "third")
	if got := get(t, v, "QueueUrl"); got != "first" {
		t.Errorf("after Add, QueueUrl = %q, want first", got)
	}
	v.Add("Other", "x")
	if got := get(t, v, "Other"); got != "x" {
		t.Errorf("Other = %q", got)
	}
}

// A bad escape in a key or a value is refused, and the error names the
// pair by its raw key.
func TestParseRejectsBadEscapes(t *testing.T) {
	t.Parallel()
	for s, rawKey := range map[string]string{"a=%zz": "a", "a%2=b": "a%2", "a=%": "a"} {
		_, err := form.Parse("x=1&" + s)
		if err == nil || err.Problem != form.BadEscape || err.Offset != 4 || err.RawKey != rawKey {
			t.Errorf("Parse(%q) error = %+v, want BadEscape at byte 4 naming key %q", s, err, rawKey)
		}
	}
}

// A value may be a secret, so no part of it reaches the error: a password
// with a literal %, sent unescaped through curl -d, is the common case.
func TestParseErrorOmitsTheValue(t *testing.T) {
	t.Parallel()
	_, err := form.Parse("Action=X&Pin=hunter2%zz")
	if err == nil || err.RawKey != "Pin" {
		t.Fatalf("Parse error = %+v, want one naming key Pin", err)
	}
	want := "The parameter Pin at byte 9 has a % not followed by two hexadecimal digits; percent-encode a literal % as %25."
	if got := err.Message(); got != want {
		t.Errorf("Message = %q, want %q", got, want)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("Error = %q echoes the value", err.Error())
	}
}

// A key of more than MaxKeySegments segments is refused, an escaped '.'
// counted as one, and the error carries only the start of the key.
func TestParseRefusesDeepKeys(t *testing.T) {
	t.Parallel()
	deepest := strings.Repeat("a.", form.MaxKeySegments-1) + "a"
	parse(t, deepest+"=x")
	for _, key := range []string{deepest + ".a", deepest + "%2Ea", strings.Repeat("a.", 1<<20)} {
		_, err := form.Parse("x=1&" + key + "=x")
		if err == nil || err.Problem != form.KeyTooDeep || err.Offset != 4 || len(err.RawKey) > form.ExcerptBytes+len("...") {
			t.Errorf("Parse of a %d-byte key: error = %+v, want KeyTooDeep at byte 4 with a short excerpt", len(key), err)
		}
	}
}

// A form of more than MaxNodes keys and key prefixes is refused, and
// parsing allocates in proportion to the nodes it keeps, not to the body:
// a 16 MiB body of keys that each add MaxKeySegments nodes costs no more
// than the cap allows. The test is not parallel: it reads the process's
// allocation counter.
func TestParseBoundsItsAllocation(t *testing.T) {
	var b strings.Builder
	chain := strings.Repeat(".a", form.MaxKeySegments-1)
	for i := 0; b.Len() < 16<<20; i++ {
		b.WriteString(strconv.Itoa(i) + chain + "=&")
	}
	body := b.String()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := form.Parse(body)
	runtime.ReadMemStats(&after)
	if err == nil || err.Problem != form.TooManyNodes {
		t.Fatalf("Parse of a 16 MiB form of distinct deep keys: error = %+v, want TooManyNodes", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 128<<20 {
		t.Errorf("Parse allocated %d MiB, want at most 128 MiB", allocated>>20)
	}
}

// Lookup finds the first value of a key without decoding the rest of the
// form; a bad escape elsewhere is not its concern.
func TestLookup(t *testing.T) {
	t.Parallel()
	s := "%zz=1&Act%69on=Publish&Action=Second&Message=%zz&Version=2010-03-31"
	if got, ok, err := form.Lookup(s, "Action"); err != nil || !ok || got != "Publish" {
		t.Errorf("Lookup Action = %q, %v, %v", got, ok, err)
	}
	if got, ok, err := form.Lookup(s, "Version"); err != nil || !ok || got != "2010-03-31" {
		t.Errorf("Lookup Version = %q, %v, %v", got, ok, err)
	}
	if _, ok, err := form.Lookup(s, "Missing"); err != nil || ok {
		t.Errorf("Lookup Missing = %v, %v", ok, err)
	}
	if _, _, err := form.Lookup(s, "Message"); err == nil || err.Problem != form.BadEscape || err.Offset != 37 {
		t.Errorf("Lookup Message error = %+v, want BadEscape at byte 37", err)
	}
}

// The zero Values is an empty form that Add fills.
func TestZeroValues(t *testing.T) {
	t.Parallel()
	var v form.Values
	if _, ok := v.Get("A"); ok {
		t.Error("the zero Values has A")
	}
	v.Add("A.B", "x")
	if got := get(t, &v, "A.B"); got != "x" {
		t.Errorf("A.B = %q", got)
	}
	if a, ok := v.Root().Child("A"); !ok || a.Key() != "A" {
		t.Errorf("Root().Child(A) = %v, %v", a, ok)
	}
}

// wire-json-query.md 7.1: Go v2 sends member.10 and member.11 before
// member.2; the items come back in numeric order.
func TestIndexedSortsNumerically(t *testing.T) {
	t.Parallel()
	s := "E.member.1=a&E.member.10=j&E.member.11=k&E.member.2=b&E.member.9=i&E.member.x=ignored"
	list, ok := parse(t, s).Root().Child("E")
	if !ok {
		t.Fatal("E absent")
	}
	member, ok := list.Child("member")
	if !ok {
		t.Fatal("member absent")
	}
	items := member.Indexed()
	got := make([]string, 0, len(items))
	for _, n := range items {
		value, _ := n.Value()
		got = append(got, value)
	}
	if want := []string{"a", "b", "i", "j", "k"}; !slices.Equal(got, want) {
		t.Errorf("items = %v, want %v", got, want)
	}
}

// Indexes compare by value, longer than any integer type included, and two
// spellings of one value keep a fixed order.
func TestIndexedOrdersLongAndPaddedIndexes(t *testing.T) {
	t.Parallel()
	s := "L.123456789012345678901234567890=c&L.02=b2&L.2=b&L.0=a"
	l, _ := parse(t, s).Root().Child("L")
	items := l.Indexed()
	got := make([]string, 0, len(items))
	for _, n := range items {
		got = append(got, n.Key())
	}
	if want := []string{"L.0", "L.02", "L.2", "L.123456789012345678901234567890"}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestNodeValueAndChildren(t *testing.T) {
	t.Parallel()
	v := parse(t, "A=1&A.B=2")
	a, _ := v.Root().Child("A")
	if got, ok := a.Value(); !ok || got != "1" {
		t.Errorf("A = %q, %v", got, ok)
	}
	b, _ := a.Child("B")
	if got := b.Key(); got != "A.B" {
		t.Errorf("Key = %q", got)
	}
	if _, ok := v.Root().Value(); ok {
		t.Error("the root has a value")
	}
}

// Escape is RFC 3986: only unreserved characters stay, a space is %20 and
// + is %2B (iam-sts-kms.md 3.3).
func TestEscape(t *testing.T) {
	t.Parallel()
	got := form.Escape(`{"a b":"c+d~e_f.g-h/:,"}`)
	if want := "%7B%22a%20b%22%3A%22c%2Bd~e_f.g-h%2F%3A%2C%22%7D"; got != want {
		t.Errorf("Escape = %s, want %s", got, want)
	}
}

func TestEncodeThenParse(t *testing.T) {
	t.Parallel()
	pairs := []form.Pair{{Key: "Action", Value: "SendMessage"}, {Key: "MessageBody", Value: "a b+c&=%"}}
	got := form.Encode(pairs)
	if want := "Action=SendMessage&MessageBody=a%20b%2Bc%26%3D%25"; got != want {
		t.Errorf("Encode = %s, want %s", got, want)
	}
	if body := get(t, parse(t, got), "MessageBody"); body != "a b+c&=%" {
		t.Errorf("round trip = %q", body)
	}
}
