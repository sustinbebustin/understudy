package parity

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/normalize"
)

func withHeader(r Response, name, value string) Response {
	r.Headers = append(append([]Header(nil), r.Headers...), Header{name, value})
	return r
}

// Cases compare_test.go leaves open, each following a rule of Compare's
// documentation.
func TestCompareRulesBeyondTheRecordedPairs(t *testing.T) {
	t.Parallel()
	errorXML := func(code string) Response {
		return withType("text/xml", 400, `<ErrorResponse><Error><Code>`+code+`</Code><Message>m</Message></Error></ErrorResponse>`)
	}
	checkBodyCases(t, []bodyCase{
		// Duplicate members pair in order, the n-th with the n-th.
		{"duplicate members pair in order", jsonOK(`{"a":1,"a":2}`), jsonOK(`{"a":1,"a":3}`), []string{"body/a changed"}},
		{"a missing duplicate", jsonOK(`{"a":1,"a":2}`), jsonOK(`{"a":1}`), []string{"body/a missing"}},
		// A mask keeps the kind: an epoch number where AWS sends an epoch
		// string still differs.
		{"a masked string against a masked number", jsonOK(`{"SentTimestamp":"1791959411396"}`), jsonOK(`{"SentTimestamp":1791959411396}`), []string{"body/SentTimestamp changed"}},
		// A prefixed attribute keeps its prefix in the path.
		{"a prefixed attribute", xmlOK(`<r xmlns:xsi="u" xsi:type="T"/>`), xmlOK(`<r xmlns:xsi="u" xsi:type="U"/>`), []string{"body/r/@xsi:type changed"}},
		{"an extra attribute", xmlOK(`<r/>`), xmlOK(`<r a="1"/>`), []string{"body/r/@a extra"}},
		{"an extra later sibling", xmlOK(`<r><m>1</m></r>`), xmlOK(`<r><m>1</m><m>2</m></r>`), []string{"body/r/m[1] extra"}},
		// Leaf text is compared exactly, whitespace included.
		{"whitespace in a leaf", xmlOK(`<r><a> </a></r>`), xmlOK(`<r><a></a></r>`), []string{"body/r/a changed"}},
		// Two root elements are not one XML document, so the bodies are
		// compared as text.
		{"two roots are text", xmlOK(`<a/><b/>`), xmlOK(`<a/><b/>`), nil},
		{"two roots that differ", xmlOK(`<a/><b/>`), xmlOK(`<a/><c/>`), []string{"body changed"}},
		{"JSON against text", withType("", 200, `{"a":1}`), withType("", 200, "a"), []string{"body changed"}},
		// A Content-Type with parameters still names its format.
		{"a Content-Type with parameters", withType("application/json; charset=utf-8", 200, `{"a" : 1}`), withType("application/json", 200, `{"a":1}`), []string{"header/content-type changed"}},
		// An unrecorded body is not compared; the rest is.
		{"an unrecorded body", Response{Status: 200, Body: UnrecordedBody{}}, jsonOK(`{"a":1}`), nil},
		{"an unrecorded body with another status", Response{Status: 200, Body: UnrecordedBody{}}, withType("", 500, ""), []string{"status changed"}},
		{"recorded bytes against text", Response{Status: 200, Body: BinaryBody{Bytes: []byte{0xff}}}, withType("", 200, "x"), []string{"body changed"}},
		// The XML error code and message are Error/Code and Error/Message
		// under the root.
		{"an XML error code", errorXML("A"), errorXML("B"), []string{"error/code changed", "body/ErrorResponse/Error/Code changed"}},
		{"no error fields on either side", withType("application/x-amz-json-1.1", 400, `{}`), withType("application/x-amz-json-1.1", 400, `{}`), nil},
		// The header's code drops the namespace and the URL after ':', so
		// it equals understudy's __type; the header itself is compared
		// (x-amzn-*) and missing.
		{
			"the code of an x-amzn-ErrorType header",
			withHeader(withType("application/x-amz-json-1.1", 400, `{}`), "x-amzn-ErrorType", "aws.protocoltests#Foo:http://internal.amazon.com/coral/"),
			withType("application/x-amz-json-1.1", 400, `{"__type":"Foo"}`),
			[]string{"header/x-amzn-errortype missing", "body/__type extra"},
		},
	})
}

// Bindings reach every decoded component of understudy's response: header
// values, XML attribute values and text, JSON string values and text bodies.
func TestCompareBindsEveryComponent(t *testing.T) {
	t.Parallel()
	const aws, ours = "k-recorded-1", "k-minted-22"
	exp := withHeader(xmlOK(`<r a="`+aws+`"><k>`+aws+`</k></r>`), "x-amz-key", aws)
	act := withHeader(xmlOK(`<r a="`+ours+`"><k>`+ours+`</k></r>`), "x-amz-key", ours)
	bound := []normalize.Replacement{{Old: ours, New: aws}}
	if got := Compare(exp, act, CompareOptions{Bindings: bound}); len(got) != 0 {
		t.Errorf("Compare with the binding = %v; want no difference", got)
	}
	want := []string{"header/x-amz-key changed", "body/r/@a changed", "body/r/k changed"}
	if diff := cmp.Diff(want, summary(Compare(exp, act, CompareOptions{}))); diff != "" {
		t.Errorf("Compare without the binding (-want +got):\n%s", diff)
	}
	text := withType("text/plain", 200, "key "+aws)
	if got := Compare(text, withType("text/plain", 200, "key "+ours), CompareOptions{Bindings: bound}); len(got) != 0 {
		t.Errorf("Compare of text bodies with the binding = %v; want no difference", got)
	}
	// The recorded side is never substituted: the binding maps understudy's
	// value to AWS's, not back.
	if got := summary(Compare(withType("text/plain", 200, ours), withType("text/plain", 200, ours), CompareOptions{Bindings: bound})); !cmp.Equal(got, []string{"body changed"}) {
		t.Errorf("Compare of understudy's value on both sides with the binding = %v; want body changed", got)
	}
}

// A waiver on body drops every body difference and nothing else.
func TestCompareWaivesASubtree(t *testing.T) {
	t.Parallel()
	got := summary(Compare(withType("application/json", 400, `{"a":1,"b":[1]}`), withType("application/json", 409, `{"a":2,"b":[]}`), CompareOptions{Waivers: []Path{"body"}}))
	if want := []string{"status changed"}; !cmp.Equal(got, want) {
		t.Errorf("Compare with body waived = %v; want %v", got, want)
	}
}

func TestCompareReportsTheValuesOfStructuralDifferences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		expected, actual Response
		want             Difference
	}{
		{"a kind mismatch quotes the string", jsonOK(`{"n":"1"}`), jsonOK(`{"n":1}`), Difference{Path: "body/n", Kind: Changed, Expected: `"1"`, Actual: "1"}},
		{"a reordered object lists both orders", jsonOK(`{"a":1,"b":2,"c":3}`), jsonOK(`{"c":3,"a":1,"b":2}`), Difference{Path: "body", Kind: Reordered, Expected: "a,b,c", Actual: "c,a,b"}},
		{"a missing object is compact JSON", jsonOK(`{"o":{"RequestId":"r","x":[1]}}`), jsonOK(`{}`), Difference{Path: "body/o", Kind: Missing, Expected: `{"RequestId":"<request-id>","x":[1]}`}},
		{"a missing element is compact XML", xmlOK(`<r><e a="1"><RequestId>x</RequestId></e></r>`), xmlOK(`<r/>`), Difference{Path: "body/r/e", Kind: Missing, Expected: `<e a="1"><RequestId><request-id></RequestId></e>`}},
		{"an omitted body states length and digest", Response{Status: 200, Body: OmittedBody{Bytes: 3, SHA256: sha256Hex("abc")}}, withType("", 200, "ab"), Difference{
			Path: "body", Kind: Changed,
			Expected: "3 bytes, sha256 " + sha256Hex("abc"),
			Actual:   "2 bytes, sha256 " + sha256Hex("ab"),
		}},
	}
	for _, c := range cases {
		got := Compare(c.expected, c.actual, CompareOptions{})
		if len(got) != 1 {
			t.Errorf("%s: Compare = %v; want exactly %v", c.name, got, c.want)
			continue
		}
		if diff := cmp.Diff(c.want, got[0]); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.name, diff)
		}
	}
}
