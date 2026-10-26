package parity

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

// bodyText is the text of a mutated response's body; an emptied body is "".
func bodyText(tb testing.TB, r Response) string {
	tb.Helper()
	if b, ok := r.Body.(TextBody); ok {
		return b.Text
	}
	if _, ok := r.Body.(NoBody); !ok {
		tb.Fatalf("mutated body is %T; want text or none", r.Body)
	}
	return ""
}

// headerValue is the first value of the header name, case-insensitively.
func headerValue(r Response, name string) (string, bool) {
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value, true
		}
	}
	return "", false
}

func mutate(tb testing.TB, r Response, field Path) Response {
	tb.Helper()
	m, err := Mutate(r, field)
	if err != nil {
		tb.Fatalf("Mutate(%s): %v", field, err)
	}
	return m
}

// The sensitivity check flips the recorded value: a string's last digit,
// else its last letter, else an appended "x"; a number's last digit; a bool;
// a container or null is removed; an absent field is added. The body is
// written back compactly in its member order.
func TestMutateJSONBodies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		body  string
		field Path
		want  string
	}{
		{`{"b":1, "a":"x9"}`, "body/a", `{"b":1,"a":"x0"}`},
		{`{"a":"abz"}`, "body/a", `{"a":"aba"}`},
		{`{"a":"AZ"}`, "body/a", `{"a":"AA"}`},
		{`{"a":"a1b"}`, "body/a", `{"a":"a2b"}`},
		{`{"a":"--"}`, "body/a", `{"a":"--x"}`},
		{`{"a":""}`, "body/a", `{"a":"x"}`},
		{`{"n":19}`, "body/n", `{"n":10}`},
		{`{"n":1.5}`, "body/n", `{"n":1.6}`},
		{`{"t":true}`, "body/t", `{"t":false}`},
		{`{"n":null,"b":1}`, "body/n", `{"b":1}`},
		{`{"o":{"x":1},"b":1}`, "body/o", `{"b":1}`},
		{`{"l":[1],"b":1}`, "body/l", `{"b":1}`},
		{`{"a":1}`, "body/c", `{"a":1,"c":"x"}`},
		{`{"l":[1]}`, "body/l/1", `{"l":[1,"x"]}`},
		{`{"l":["a9"]}`, "body/l/0", `{"l":["a0"]}`},
		{`{"a/b":"q"}`, "body/a~1b", `{"a/b":"r"}`},
		// The body as a whole is emptied, or becomes "x" when empty.
		{`{"a":1}`, "body", ``},
		{``, "body", `x`},
	}
	for _, c := range cases {
		got := bodyText(t, mutate(t, jsonOK(c.body), c.field))
		if got != c.want {
			t.Errorf("Mutate(%s, %s) body = %s; want %s", c.body, c.field, got, c.want)
		}
	}
}

// The error code and message mutate the node the comparison reads them
// from (testdata/parity/kms/KMS-alias/01-duplicate.json).
func TestMutateErrorFields(t *testing.T) {
	t.Parallel()
	dup := recordedSide(t, kmsDuplicate)
	cases := []struct {
		field Path
		want  string
	}{
		{"error/code", `{"__type":"AlreadyExistsExceptioo","message":"An alias with the name arn:aws:kms:us-east-2:123456789012:alias/usr-rec-20261014-dup already exists"}`},
		{"error/message", `{"__type":"AlreadyExistsException","message":"An alias with the name arn:aws:kms:us-east-2:123456789012:alias/usr-rec-20261015-dup already exists"}`},
	}
	for _, c := range cases {
		if got := bodyText(t, mutate(t, dup, c.field)); got != c.want {
			t.Errorf("Mutate(%s, %s) body = %s; want %s", kmsDuplicate, c.field, got, c.want)
		}
	}

	// The code part of a namespaced __type.
	ns := withType("application/x-amz-json-1.1", 400, `{"__type":"com.amazonaws.kms#AlreadyExistsException"}`)
	if got, want := bodyText(t, mutate(t, ns, "error/code")), `{"__type":"com.amazonaws.kms#AlreadyExistsExceptioo"}`; got != want {
		t.Errorf("Mutate(namespaced __type, error/code) body = %s; want %s", got, want)
	}

	// x-amzn-ErrorType, read before __type.
	hdr := ns
	hdr.Headers = append(slices.Clone(ns.Headers), Header{"x-amzn-ErrorType", "AlreadyExistsException:http://internal.amazon.com/coral/com.amazonaws.kms/"})
	m := mutate(t, hdr, "error/code")
	if got, _ := headerValue(m, "x-amzn-ErrorType"); got != "AlreadyExistsExceptioo:http://internal.amazon.com/coral/com.amazonaws.kms/" {
		t.Errorf("Mutate(x-amzn-ErrorType, error/code) header = %q; want the code part's last letter changed", got)
	}
}

func TestMutateStatusAndHeaders(t *testing.T) {
	t.Parallel()
	dup := recordedSide(t, kmsDuplicate)
	if got := mutate(t, dup, "status").Status; got != 401 {
		t.Errorf("Mutate(status 400) = %d; want 401", got)
	}
	if got, _ := headerValue(mutate(t, dup, "header/content-type"), "Content-Type"); got != "application/x-amz-json-1.2" {
		t.Errorf("Mutate(header/content-type) = %q; want application/x-amz-json-1.2", got)
	}
	if got, ok := headerValue(mutate(t, dup, "header/x-amzn-query-error"), "x-amzn-query-error"); !ok || got != "x" {
		t.Errorf("Mutate(absent header) = %q, %v; want \"x\" added", got, ok)
	}
	// The recording is not changed in place.
	if dup.Status != 400 {
		t.Errorf("Mutate changed its argument's status to %d", dup.Status)
	}
}

// XML leaves are text: their mutation is visible to the comparison at the
// same path, and an absent element is added under its parent
// (testdata/parity/sts/R15/06-assume-ok.json).
func TestMutateXMLBodies(t *testing.T) {
	t.Parallel()
	ok := recordedSide(t, stsAssumeOK)
	cases := []struct {
		field Path
		want  Difference
	}{
		{assumeResult + "/PackedPolicySize", Difference{Path: assumeResult + "/PackedPolicySize", Kind: Changed, Expected: "22", Actual: "21"}},
		// The last digit of the Arn is the 4 of 20261014.
		{assumeResult + "/AssumedRoleUser/Arn", Difference{
			Path: assumeResult + "/AssumedRoleUser/Arn", Kind: Changed,
			Expected: "arn:aws:sts::<account>:assumed-role/usr-rec-20261015-trust/rec",
			Actual:   "arn:aws:sts::<account>:assumed-role/usr-rec-20261014-trust/rec",
		}},
		{assumeResult + "/Nope", Difference{Path: assumeResult + "/Nope", Kind: Missing, Expected: "x"}},
		{"body/AssumeRoleResponse/@xmlns", Difference{
			Path: "body/AssumeRoleResponse/@xmlns", Kind: Changed,
			Expected: "https://sts.amazonaws.com/doc/2011-06-16/", Actual: "https://sts.amazonaws.com/doc/2011-06-15/",
		}},
	}
	for _, c := range cases {
		got := Compare(mutate(t, ok, c.field), ok, CompareOptions{})
		if len(got) != 1 {
			t.Errorf("Compare(Mutate(%s), recording) = %v; want one difference", c.field, got)
			continue
		}
		if diff := cmp.Diff(c.want, got[0]); diff != "" {
			t.Errorf("Compare(Mutate(%s), recording) (-want +got):\n%s", c.field, diff)
		}
	}
}

func TestMutateRefusesWhatItCannotFlip(t *testing.T) {
	t.Parallel()
	r := jsonOK(`{"a":1}`)
	for _, field := range []Path{"error", "header", "body/nope/deeper"} {
		_, err := Mutate(r, field)
		if _, ok := errors.AsType[*PathError](err); !ok {
			t.Errorf("Mutate(%s) error = %v; want a *PathError", field, err)
		}
	}
}

// Mutating a member no normalizer covers is always seen by the comparison:
// the property the sensitivity check relies on.
func TestMutateIsSeenByCompare(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		v := rapid.OneOf(
			rapid.Map(rapid.StringMatching(`[a-z0-9 ]{0,8}`), func(s string) string { return `"` + s + `"` }),
			rapid.SampledFrom([]string{"0", "7", "12.5", "true", "false", "null", "[1]", `{"x":1}`}),
		).Draw(t, "value")
		r := jsonOK(`{"k":` + v + `,"z":1}`)
		m, err := Mutate(r, "body/k")
		if err != nil {
			t.Fatalf("Mutate(body/k of %s): %v", v, err)
		}
		if got := Compare(m, r, CompareOptions{}); len(got) == 0 {
			t.Fatalf("Compare(Mutate(%s), original) found no difference", v)
		}
	})
}
