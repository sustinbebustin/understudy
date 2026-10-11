package protocoltest_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocoltest"
)

// fataler is what *testing.T and *rapid.T share.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// mismatchAt returns where err says the response differs, failing the
// test unless err is a *MismatchError.
func mismatchAt(t fataler, err error) string {
	t.Helper()
	var m *protocoltest.MismatchError
	if !errors.As(err, &m) {
		t.Fatalf("err = %v, want a *MismatchError", err)
	}
	return m.Where
}

// JSON compares as values: member order and number spelling do not
// matter; a missing, extra or different member does, and the mismatch
// names its path.
func TestCompareJSON(t *testing.T) {
	const mt = "application/x-amz-json-1.0"
	if err := protocoltest.CompareBody(mt, nil, []byte(`{"a": 1, "b": [1.0, "x"]}`), []byte(`{"b":[1e0,"x"],"a":1.00}`)); err != nil {
		t.Errorf("equivalent JSON: %v", err)
	}
	for got, where := range map[string]string{
		`{"a": 1}`:                        "body $.b",
		`{"a": 1, "b": [1, "y"]}`:         "body $.b[1]",
		`{"a": 1, "b": [1, "x"], "c": 0}`: "body $.c",
		`{"a": 2, "b": [1, "x"]}`:         "body $.a",
	} {
		if at := mismatchAt(t, protocoltest.CompareBody(mt, nil, []byte(`{"a": 1, "b": [1, "x"]}`), []byte(got))); at != where {
			t.Errorf("%s: mismatch at %q, want %q", got, at, where)
		}
	}
}

// Shuffling an object's members never changes the comparison.
func TestCompareJSONIgnoresMemberOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		keys := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{1,4}`), 1, 6, rapid.ID).Draw(t, "keys")
		values := make(map[string]int, len(keys))
		for _, k := range keys {
			values[k] = rapid.IntRange(-1000, 1000).Draw(t, k)
		}
		render := func(order []string) string {
			parts := make([]string, len(order))
			for i, k := range order {
				parts[i] = fmt.Sprintf("%q: %d", k, values[k])
			}
			return "{" + strings.Join(parts, ", ") + "}"
		}
		shuffled := rapid.Permutation(keys).Draw(t, "shuffled")
		if err := protocoltest.CompareBody("application/json", nil, []byte(render(keys)), []byte(render(shuffled))); err != nil {
			t.Fatal(err)
		}
	})
}

// xmlShape is a structure R with a wrapped map m, a map f flattened under
// the name F, and a list l whose members are named m.
func xmlShape() *smithy.Schema {
	str := &smithy.Schema{Type: smithy.TypeString}
	mapOf := func() *smithy.Schema {
		return &smithy.Schema{Type: smithy.TypeMap, Members: []*smithy.Member{{Name: "key", Target: str}, {Name: "value", Index: 1, Target: str}}}
	}
	list := &smithy.Schema{Type: smithy.TypeList, Members: []*smithy.Member{{Name: "member", XMLName: "m", Target: str}}}
	return &smithy.Schema{Type: smithy.TypeStructure, Members: []*smithy.Member{
		{Name: "m", Target: mapOf()},
		{Name: "f", Index: 1, XMLName: "F", XMLFlattened: true, Target: mapOf()},
		{Name: "l", Index: 2, Target: list},
	}}
}

// XML compares on names resolved to namespace URI and local name, whatever
// prefixes bind them: differently named children in any order,
// a list's members in order, a map's entries, wrapped or flattened, in any
// order; a server's RequestId and ResponseMetadata are not held to a
// client fixture's.
func TestCompareXML(t *testing.T) {
	const mt = "application/xml"
	shape := xmlShape()
	equal := map[string][2]string{
		"sibling order": {`<R><a>1</a><b>2</b></R>`, `<R><b>2</b><a>1</a></R>`},
		"namespace prefixes": {
			`<R xmlns="urn:x" xmlns:q="urn:y"><a q:t="1">1</a><q:b/></R>`,
			`<p:R xmlns:p="urn:x" xmlns:z="urn:y"><p:a z:t="1">1</p:a><z:b/></p:R>`,
		},
		"map entries": {
			`<R><m><entry><key>foo</key><value>1</value></entry><entry><key>baz</key><value>2</value></entry></m></R>`,
			`<R><m><entry><key>baz</key><value>2</value></entry><entry><key>foo</key><value>1</value></entry></m></R>`,
		},
		"flattened map entries": {
			`<R><F><key>foo</key><value>1</value></F><F><key>baz</key><value>2</value></F></R>`,
			`<R><F><key>baz</key><value>2</value></F><F><key>foo</key><value>1</value></F></R>`,
		},
		"request id":        {`<E><RequestId>foo-id</RequestId></E>`, `<E><RequestId>7c1f</RequestId></E>`},
		"response metadata": {`<R><Result/></R>`, `<R><Result/><ResponseMetadata><RequestId>x</RequestId></ResponseMetadata></R>`},
		"indentation":       {"<R>\n  <a>1</a>\n</R>\n", `<R><a>1</a></R>`},
	}
	for name, pair := range equal {
		if err := protocoltest.CompareBody(mt, shape, []byte(pair[0]), []byte(pair[1])); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	differ := map[string][2]string{
		"text":           {`<R><a>1</a></R>`, `<R><a>2</a></R>`},
		"missing child":  {`<R><a>1</a><b/></R>`, `<R><a>1</a></R>`},
		"list contents":  {`<R><l><m>1</m><m>2</m></l></R>`, `<R><l><m>1</m><m>3</m></l></R>`},
		"reordered list": {`<R><l><m>1</m><m>2</m></l></R>`, `<R><l><m>2</m><m>1</m></l></R>`},
		"map entry value": {
			`<R><m><entry><key>foo</key><value>1</value></entry><entry><key>baz</key><value>2</value></entry></m></R>`,
			`<R><m><entry><key>baz</key><value>1</value></entry><entry><key>foo</key><value>2</value></entry></m></R>`,
		},
		"root name":    {`<R/>`, `<S/>`},
		"namespaces":   {`<R xmlns="https://example.com/"><a>1</a></R>`, `<p:R xmlns:p="urn:x"><p:a>1</p:a></p:R>`},
		"no namespace": {`<R xmlns="urn:x"/>`, `<R/>`},
		"child namespace": {
			`<R xmlns="urn:x"><a xmlns="urn:y">1</a></R>`,
			`<R xmlns="urn:x"><a>1</a></R>`,
		},
		"attribute namespace": {`<R xmlns:p="urn:x" p:a="1"/>`, `<R xmlns:p="urn:y" p:a="1"/>`},
		"attribute":           {`<R a="1"/>`, `<R a="2"/>`},
		"wanted request":      {`<E><RequestId>x</RequestId></E>`, `<E/>`},
	}
	for name, pair := range differ {
		if err := protocoltest.CompareBody(mt, shape, []byte(pair[0]), []byte(pair[1])); err == nil {
			t.Errorf("%s: %s and %s compare equal", name, pair[0], pair[1])
		}
	}
	reordered := equal["map entries"]
	if err := protocoltest.CompareBody(mt, nil, []byte(reordered[0]), []byte(reordered[1])); err == nil {
		t.Error("reordered entries compare equal without a shape that says they are a map's")
	}
}

// A form compares as its keys and their values, in any order.
func TestCompareForm(t *testing.T) {
	const mt = "application/x-www-form-urlencoded"
	if err := protocoltest.CompareBody(mt, nil, []byte("Action=A&Version=1&B.member.1=x"), []byte("B.member.1=x&Version=1&Action=A")); err != nil {
		t.Errorf("reordered form: %v", err)
	}
	if at := mismatchAt(t, protocoltest.CompareBody(mt, nil, []byte("Action=A&C=1"), []byte("Action=A&C=2"))); at != "body key C" {
		t.Errorf("mismatch at %q", at)
	}
	mismatchAt(t, protocoltest.CompareBody(mt, nil, []byte("Action=A"), []byte("Action=A&Extra=1")))
}

// CBOR compares structurally: the expected body is base64, map entries in
// any order, floats by value whatever their width.
func TestCompareCBOR(t *testing.T) {
	enc := func(v cbor.Value) []byte {
		b, err := cbor.Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	want := base64.StdEncoding.EncodeToString(enc(cbor.Map{{Key: "a", Value: cbor.Uint(1)}, {Key: "f", Value: cbor.Float64(1.5)}}))
	got := enc(cbor.Map{{Key: "f", Value: cbor.Float32(1.5)}, {Key: "a", Value: cbor.Uint(1)}})
	if err := protocoltest.CompareBody("application/cbor", nil, []byte(want), got); err != nil {
		t.Errorf("equivalent CBOR: %v", err)
	}
	mismatchAt(t, protocoltest.CompareBody("application/cbor", nil, []byte(want), enc(cbor.Map{{Key: "a", Value: cbor.Uint(2)}, {Key: "f", Value: cbor.Float32(1.5)}})))
}

// Any other media type compares byte for byte.
func TestCompareOpaque(t *testing.T) {
	if err := protocoltest.CompareBody("text/plain", nil, []byte("abc"), []byte("abc")); err != nil {
		t.Error(err)
	}
	mismatchAt(t, protocoltest.CompareBody("application/octet-stream", nil, []byte("abc"), []byte("abd")))
}

// Expected headers match a subset, case-insensitively, a repeated header
// joined with ", "; forbidden headers must be absent and required ones
// present.
func TestCompareHeaders(t *testing.T) {
	got := http.Header{"X-List": {"a", "b"}, "Content-Type": {"application/json"}}
	if err := protocoltest.CompareHeaders(map[string]string{"x-list": "a, b"}, got); err != nil {
		t.Error(err)
	}
	if at := mismatchAt(t, protocoltest.CompareHeaders(map[string]string{"X-Missing": "1"}, got)); at != "header X-Missing" {
		t.Errorf("mismatch at %q", at)
	}
	if err := protocoltest.CompareHeaderPresence([]string{"X-Absent"}, []string{"content-type"}, got); err != nil {
		t.Error(err)
	}
	mismatchAt(t, protocoltest.CompareHeaderPresence([]string{"X-List"}, nil, got))
	mismatchAt(t, protocoltest.CompareHeaderPresence(nil, []string{"X-Absent"}, got))
}

// Comparing random bytes as any media type never panics, and every
// difference it finds is a *MismatchError saying where.
func TestCompareBodyNeverPanics(t *testing.T) {
	types := []string{"application/json", "application/xml", "application/x-www-form-urlencoded", "application/cbor", "text/plain"}
	rapid.Check(t, func(t *rapid.T) {
		mt := rapid.SampledFrom(types).Draw(t, "type")
		want := rapid.SliceOf(rapid.Byte()).Draw(t, "want")
		got := rapid.SliceOf(rapid.Byte()).Draw(t, "got")
		if err := protocoltest.CompareBody(mt, nil, want, got); err != nil {
			mismatchAt(t, err)
		}
	})
}
