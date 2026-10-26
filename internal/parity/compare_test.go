package parity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/normalize"
)

// equateEmpty treats a nil and an empty slice alike: the file formats omit
// empty members, so neither is observable.
var equateEmpty = cmpopts.EquateEmpty()

// The recorded fixtures the comparison cases answer.
const (
	kmsDuplicate      = "kms/KMS-alias/01-duplicate.json"
	kmsDuplicateRaw   = "kms/KMS-alias/02-duplicate-raw.json"
	kmsGarbageMarker  = "kms/R15/x-listkeys-garbage-marker.json"
	stsAssumeDenied   = "sts/R15/06-assume-denied.json"
	stsAssumeOK       = "sts/R15/06-assume-ok.json"
	stsAccessKeyInfo  = "sts/R15/06-get-access-key-info-deleted.json"
	sqsReceive        = "sqs/R01/02-recv1.json"
	localQueryError   = "compare/sqs-query-error/aws.json"
	localBoundKey     = "compare/kms-bound-key/aws.json"
	assumeResult      = "body/AssumeRoleResponse/AssumeRoleResult"
	deniedError       = "body/ErrorResponse/Error"
	kmsDuplicateText  = "An alias with the name arn:aws:kms:us-east-2:<account>:alias/usr-rec-20261014-dup already exists"
	kmsKeyIDRecorded  = "157cb509-e842-4095-b0b6-e97188e0a507" // kms/KMS-alias/01-duplicate.json TargetKeyId
	kmsKeyIDUnderstud = "0f0e0d0c-0b0a-4909-8807-060504030201"
)

// recordedSide is the final response of the recorded side of a comparison
// case: a fixture under testdata/parity, or, for a path starting
// "compare/", a synthetic one under this package's testdata.
func recordedSide(tb testing.TB, p string) Response {
	tb.Helper()
	if strings.HasPrefix(p, "compare/") {
		return finalResponse(tb, localFixture(tb, p))
	}
	return finalResponse(tb, recordedFixture(tb, p))
}

// localFixture parses the fixture at p under this package's testdata.
func localFixture(tb testing.TB, p string) Fixture {
	tb.Helper()
	b, err := os.ReadFile(path.Join("testdata", p))
	if err != nil {
		tb.Fatal(err)
	}
	f, err := ParseFixture(b)
	if err != nil {
		tb.Fatalf("ParseFixture(testdata/%s): %v", p, err)
	}
	return f
}

// understudySide is the response of testdata/compare/<dir>/understudy.json.
func understudySide(tb testing.TB, dir string) Response {
	tb.Helper()
	return finalResponse(tb, localFixture(tb, path.Join("compare", dir, "understudy.json")))
}

// summary renders differences as "<path> <kind>", the part of a difference
// the outcome reason and the baseline depend on.
func summary(ds []Difference) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, string(d.Path)+" "+d.Kind.String())
	}
	return out
}

// Each case pairs a recorded response with an answer understudy could give
// and lists the differences the comparison must report, in comparison
// order. The recorded side is AWS's answer (the fixture named); the notes
// file beside each recording records which fields AWS and understudy share.
func TestCompareRecordedPairs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dir      string
		aws      string
		waivers  []Path
		bindings []normalize.Replacement
		want     []string
	}{
		// Only the request id, Date and headers AWS alone sends differ: the
		// ids and dates are not compared, and headers only one side sends
		// are compared only when AWS sends them and the comparison selects
		// them (testdata/parity/kms/KMS-alias notes: Cache-Control, Expires,
		// Pragma).
		{dir: "kms-alias-same", aws: kmsDuplicate},
		// Message text is compared twice: as the error message and as the
		// body member that carries it; waiving either one leaves the other.
		{dir: "kms-alias-message", aws: kmsDuplicate, want: []string{"error/message changed", "body/message changed"}},
		{dir: "kms-alias-message", aws: kmsDuplicate, waivers: []Path{"body/message"}, want: []string{"error/message changed"}},
		{dir: "kms-alias-message", aws: kmsDuplicate, waivers: []Path{"error"}, want: []string{"body/message changed"}},
		// AWS answers 400 where the model says 409 (KMS-alias notes).
		{dir: "kms-alias-status", aws: kmsDuplicate, want: []string{"status changed"}},
		{dir: "kms-alias-code", aws: kmsDuplicate, want: []string{"error/code changed", "body/__type changed"}},
		// A shape namespace is dropped from the code, not from the body
		// member: waiving body/__type leaves the code compared, and equal.
		{dir: "kms-alias-namespaced-type", aws: kmsDuplicate, want: []string{"body/__type changed"}},
		{dir: "kms-alias-namespaced-type", aws: kmsDuplicate, waivers: []Path{"body/__type"}},
		{dir: "kms-alias-errortype-header", aws: kmsDuplicate, want: []string{"body/__type changed"}},
		// Every client records the exact body bytes (testdata/README.md), so
		// member order is compared.
		{dir: "kms-alias-reordered", aws: kmsDuplicate, want: []string{"body reordered"}},
		{dir: "kms-alias-content-type", aws: kmsDuplicate, want: []string{"header/content-type changed"}},
		{dir: "kms-alias-content-type", aws: kmsDuplicate, waivers: []Path{"header/content-type"}},
		{dir: "kms-alias-content-type", aws: kmsDuplicate, waivers: []Path{"header"}},
		{dir: "kms-alias-no-content-type", aws: kmsDuplicate, want: []string{"header/content-type missing"}},
		// AWS sends exactly {"__type":"InvalidMarkerException"}
		// (testdata/parity/kms/R15/x-listkeys-garbage-marker.json).
		{dir: "kms-garbage-marker-message", aws: kmsGarbageMarker, want: []string{"error/message extra", "body/message extra"}},
		// The replay caller and the recording caller are one placeholder;
		// request ids and the whitespace between elements are not compared.
		{dir: "sts-denied-root-caller", aws: stsAssumeDenied},
		{dir: "sts-denied-resource", aws: stsAssumeDenied, want: []string{"error/message changed", deniedError + "/Message changed"}},
		{dir: "sts-denied-code", aws: stsAssumeDenied, want: []string{"error/code changed", deniedError + "/Code changed"}},
		{dir: "sts-denied-no-xmlns", aws: stsAssumeDenied, want: []string{"body/ErrorResponse/@xmlns missing"}},
		{dir: "sts-denied-reordered", aws: stsAssumeDenied, want: []string{deniedError + " reordered"}},
		{dir: "sts-denied-no-type", aws: stsAssumeDenied, want: []string{deniedError + "/Type missing"}},
		// Minted role ids, credentials, expiration and request id are masked.
		{dir: "sts-assume-ok-minted", aws: stsAssumeOK},
		{dir: "sts-assume-ok-packed-size", aws: stsAssumeOK, want: []string{assumeResult + "/PackedPolicySize changed"}},
		// A mask keeps the format: an epoch number where AWS sends ISO 8601
		// still differs.
		{dir: "sts-assume-ok-epoch-expiration", aws: stsAssumeOK, want: []string{assumeResult + "/Credentials/Expiration changed"}},
		{dir: "sts-assume-ok-session-name", aws: stsAssumeOK, want: []string{
			assumeResult + "/AssumedRoleUser/AssumedRoleId changed",
			assumeResult + "/AssumedRoleUser/Arn changed",
		}},
		// AWS answers 200 {Account}; an unbound operation answers 501
		// (testdata/parity/sts/R15 notes: 06-get-access-key-info-deleted).
		{dir: "sts-access-key-info-unbound", aws: stsAccessKeyInfo, want: []string{
			"status changed", "error/code extra", "error/message extra",
			"body/GetAccessKeyInfoResponse missing", "body/ErrorResponse extra",
		}},
		{dir: "sqs-recv-root-sender", aws: sqsReceive},
		{dir: "sqs-recv-body", aws: sqsReceive, want: []string{"body/Messages/0/Body changed"}},
		{dir: "sqs-recv-empty", aws: sqsReceive, want: []string{"body/Messages/0 missing"}},
		// x-amzn-* headers carry error identity (testdata/parity/sqs/R01/05-cmv-deleted.json).
		{dir: "sqs-query-error", aws: localQueryError, want: []string{"header/x-amzn-query-error missing"}},
		{dir: "sqs-query-error-fault", aws: localQueryError, want: []string{"header/x-amzn-query-error changed"}},
		// A bound value is replaced by the AWS value before comparison;
		// unbound, the minted id differs wherever it appears.
		{dir: "kms-bound-key", aws: localBoundKey, bindings: []normalize.Replacement{{Old: kmsKeyIDUnderstud, New: kmsKeyIDRecorded}}},
		{dir: "kms-bound-key", aws: localBoundKey, want: []string{"body/KeyMetadata/KeyId changed", "body/KeyMetadata/Arn changed"}},
	}
	for _, c := range cases {
		name := c.dir + fmt.Sprintf("/waivers=%v/bound=%d", c.waivers, len(c.bindings))
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := Compare(recordedSide(t, c.aws), understudySide(t, c.dir), CompareOptions{Bindings: c.bindings, Waivers: c.waivers})
			if diff := cmp.Diff(c.want, summary(got), equateEmpty); diff != "" {
				t.Errorf("Compare(%s, %s) (-want +got):\n%s\ndifferences: %v", c.aws, c.dir, diff, got)
			}
		})
	}
}

// A difference carries both values after normalization: what AWS sent and
// what understudy sent.
func TestCompareReportsBothNormalizedValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dir, aws string
		want     Difference
	}{
		{"kms-alias-status", kmsDuplicate, Difference{Path: "status", Kind: Changed, Expected: "400", Actual: "409"}},
		{"kms-alias-code", kmsDuplicate, Difference{Path: "error/code", Kind: Changed, Expected: "AlreadyExistsException", Actual: "InvalidAliasNameException"}},
		{"kms-alias-message", kmsDuplicate, Difference{
			Path: "error/message", Kind: Changed,
			Expected: kmsDuplicateText,
			Actual:   "Alias arn:aws:kms:us-east-2:<account>:alias/usr-rec-20261014-dup already exists",
		}},
		{"kms-garbage-marker-message", kmsGarbageMarker, Difference{Path: "error/message", Kind: Extra, Actual: "The marker is not valid."}},
		{"sts-denied-resource", stsAssumeDenied, Difference{
			Path: "error/message", Kind: Changed,
			Expected: "User: <caller> is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::<account>:role/usr-rec-20261014-deny",
			Actual:   "User: <caller> is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::<account>:role/usr-rec-20261014-trust",
		}},
		{"sts-denied-code", stsAssumeDenied, Difference{Path: "error/code", Kind: Changed, Expected: "AccessDenied", Actual: "AccessDeniedException"}},
		{"sqs-query-error", localQueryError, Difference{Path: "header/x-amzn-query-error", Kind: Missing, Expected: "InvalidParameterValue;Sender"}},
	}
	for _, c := range cases {
		got := Compare(recordedSide(t, c.aws), understudySide(t, c.dir), CompareOptions{})
		if len(got) == 0 {
			t.Errorf("Compare(%s, %s) found no difference; want %v first", c.aws, c.dir, c.want)
			continue
		}
		if diff := cmp.Diff(c.want, got[0]); diff != "" {
			t.Errorf("Compare(%s, %s)[0] (-want +got):\n%s", c.aws, c.dir, diff)
		}
	}
}

func withType(contentType string, status int, body string) Response {
	r := Response{Status: status, Body: TextBody{Text: body}}
	if contentType != "" {
		r.Headers = []Header{{"Content-Type", contentType}}
	}
	if body == "" {
		r.Body = NoBody{}
	}
	return r
}

func jsonOK(body string) Response { return withType("application/x-amz-json-1.1", 200, body) }
func xmlOK(body string) Response  { return withType("text/xml", 200, body) }

type bodyCase struct {
	name             string
	expected, actual Response
	want             []string
}

func checkBodyCases(t *testing.T, cases []bodyCase) {
	t.Helper()
	for i := range cases {
		c := &cases[i]
		got := Compare(c.expected, c.actual, CompareOptions{})
		if diff := cmp.Diff(c.want, summary(got), equateEmpty); diff != "" {
			t.Errorf("%s (-want +got):\n%s\ndifferences: %v", c.name, diff, got)
		}
	}
}

func TestCompareJSONBodies(t *testing.T) {
	t.Parallel()
	checkBodyCases(t, []bodyCase{
		{"numbers compare by literal text", jsonOK(`{"n":1.0}`), jsonOK(`{"n":1}`), []string{"body/n changed"}},
		{"strings compare by decoded value", jsonOK(`{"s":"A"}`), jsonOK(`{"s":"A"}`), nil},
		{"a kind mismatch is a change", jsonOK(`{"n":"1"}`), jsonOK(`{"n":1}`), []string{"body/n changed"}},
		{"null and false differ", jsonOK(`{"b":null}`), jsonOK(`{"b":false}`), []string{"body/b changed"}},
		{"a missing member", jsonOK(`{"a":1,"b":2}`), jsonOK(`{"a":1}`), []string{"body/b missing"}},
		{"an extra member follows the recorded ones", jsonOK(`{"a":1,"b":2}`), jsonOK(`{"c":3,"a":1,"b":9}`), []string{"body/b changed", "body/c extra"}},
		{"order of the root object", jsonOK(`{"a":1,"b":2}`), jsonOK(`{"b":2,"a":1}`), []string{"body reordered"}},
		{"order of a nested object", jsonOK(`{"o":{"x":1,"y":2}}`), jsonOK(`{"o":{"y":2,"x":1}}`), []string{"body/o reordered"}},
		{"order follows the member differences", jsonOK(`{"a":1,"b":2}`), jsonOK(`{"b":3,"a":1}`), []string{"body/b changed", "body reordered"}},
		{"order counts common members only", jsonOK(`{"a":1,"b":2}`), jsonOK(`{"a":1,"c":0,"b":2}`), []string{"body/c extra"}},
		{"an extra element", jsonOK(`{"l":[1,2]}`), jsonOK(`{"l":[1,2,3]}`), []string{"body/l/2 extra"}},
		{"a missing element", jsonOK(`{"l":[1,2,3]}`), jsonOK(`{"l":[1,2]}`), []string{"body/l/2 missing"}},
		{"an array where an object was", jsonOK(`{"l":[1]}`), jsonOK(`{"l":{"0":1}}`), []string{"body/l changed"}},
		{"a root array", jsonOK(`[{"k":"v"}]`), jsonOK(`[{"k":"w"}]`), []string{"body/0/k changed"}},
		{"RFC 6901 escapes", jsonOK(`{"a/b":1,"c~d":2}`), jsonOK(`{"a/b":2,"c~d":3}`), []string{"body/a~1b changed", "body/c~0d changed"}},
		{"member rules mask by name", jsonOK(`{"RequestId":"a","CreationDate":1791987671.668}`), jsonOK(`{"RequestId":"b","CreationDate":1793005200}`), nil},
		{"text rules mask string values", jsonOK(`{"Expiration":"2026-10-14T15:21:12Z"}`), jsonOK(`{"Expiration":"2026-10-26T10:00:00.5Z"}`), nil},
	})
}

func TestCompareXMLBodies(t *testing.T) {
	t.Parallel()
	checkBodyCases(t, []bodyCase{
		{"a later sibling of one name", xmlOK(`<r><member>1</member><member>2</member></r>`), xmlOK(`<r><member>1</member><member>3</member></r>`), []string{"body/r/member[1] changed"}},
		{
			"declaration, comments, processing instructions and whitespace are not compared",
			xmlOK(`<?xml version="1.0" encoding="UTF-8"?><!-- c --><r><a>1</a><?pi x?></r>`), xmlOK("<r>\n  <a>1</a>\n</r>\n"), nil,
		},
		{"an attribute", xmlOK(`<r a="1"/>`), xmlOK(`<r a="2"/>`), []string{"body/r/@a changed"}},
		{"a namespace declaration is an attribute", xmlOK(`<r xmlns:p="u"><a/></r>`), xmlOK(`<r><a/></r>`), []string{"body/r/@xmlns:p missing"}},
		{"an extra element", xmlOK(`<r><a>1</a></r>`), xmlOK(`<r><a>1</a><b>2</b></r>`), []string{"body/r/b extra"}},
		{"a missing element", xmlOK(`<r><a>1</a><b>2</b></r>`), xmlOK(`<r><a>1</a></r>`), []string{"body/r/b missing"}},
		{"child order", xmlOK(`<r><a>1</a><b>2</b></r>`), xmlOK(`<r><b>2</b><a>1</a></r>`), []string{"body/r reordered"}},
		{"member rules mask by element name", xmlOK(`<r><RequestId>a</RequestId></r>`), xmlOK(`<r><RequestId>b</RequestId></r>`), nil},
		// Text is compared decoded: understudy escapes an apostrophe as
		// &#39; where AWS does not (testdata/parity/kms/R15/notes.md,
		// 04-nonexistent-key: decoded text equal).
		{"character references are decoded", xmlOK(`<r><m>Key 'k' does not exist</m></r>`), xmlOK(`<r><m>Key &#39;k&#39; does not exist</m></r>`), nil},
	})
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestCompareBodyForms(t *testing.T) {
	t.Parallel()
	omitted := Response{Status: 200, Body: OmittedBody{Bytes: 3, SHA256: sha256Hex("abc")}}
	binary := func(b ...byte) Response { return Response{Status: 200, Body: BinaryBody{Bytes: b}} }
	checkBodyCases(t, []bodyCase{
		{"text is exact", withType("text/plain", 200, "hello"), withType("text/plain", 200, "hellx"), []string{"body changed"}},
		{"text is normalized", withType("text/plain", 200, "at 2026-10-14T15:21:12Z"), withType("text/plain", 200, "at 2026-10-26T10:00:00Z"), nil},
		{"empty against non-empty", withType("", 200, ""), withType("", 200, "x"), []string{"body changed"}},
		{"JSON against XML is one change", withType("", 200, `{"a":"1"}`), withType("", 200, `<a>1</a>`), []string{"body changed"}},
		{"a body that does not parse is text", jsonOK(`{bad`), jsonOK(`{bad`), nil},
		{"a body that does not parse is exact text", jsonOK(`{bad`), jsonOK(`{bae`), []string{"body changed"}},
		{"an omitted body compares length and digest", omitted, withType("", 200, "abc"), nil},
		{"an omitted body with another digest", omitted, withType("", 200, "abd"), []string{"body changed"}},
		{"bytes are exact", binary(0, 1, 0xff), binary(0, 1, 0xff), nil},
		{"bytes that differ", binary(0, 1, 0xff), binary(0, 1, 0xfe), []string{"body changed"}},
	})
}

func TestCompareStatusAndErrorFields(t *testing.T) {
	t.Parallel()
	checkBodyCases(t, []bodyCase{
		// boto3 records no status (testdata/parity/s3/R08/02-sdk-head-SHA512.json).
		{"no recorded status", Response{Body: NoBody{}}, Response{Status: 200, Body: NoBody{}}, nil},
		{"no error fields below 400", jsonOK(`{"message":"a"}`), jsonOK(`{"message":"b"}`), []string{"body/message changed"}},
		{
			"error fields when either side is an error",
			jsonOK(`{}`), withType("application/x-amz-json-1.1", 500, `{"__type":"InternalFailure","message":"boom"}`),
			[]string{"status changed", "error/code extra", "error/message extra", "body/__type extra", "body/message extra"},
		},
		{
			"code and Code, message and Message",
			withType("application/x-amz-json-1.0", 400, `{"Code":"X","Message":"m"}`), withType("application/x-amz-json-1.0", 400, `{"code":"X","message":"m"}`),
			[]string{"body/Code missing", "body/Message missing", "body/code extra", "body/message extra"},
		},
	})
}

func TestComparedHeader(t *testing.T) {
	t.Parallel()
	for _, h := range []string{"Content-Type", "content-type", "CONTENT-TYPE", "x-amzn-query-error", "x-amzn-ErrorType", "X-Amz-Server-Side-Encryption"} {
		if !ComparedHeader(h) {
			t.Errorf("ComparedHeader(%q) = false; want true", h)
		}
	}
	for _, h := range []string{
		// Ids, dates and a body digest.
		"x-amzn-RequestId", "x-amz-request-id", "x-amz-id-2", "X-Amz-Sts-Extended-Request-Id",
		"X-Amzn-Trace-Id", "x-amz-cf-id", "X-Amz-Date", "x-amz-crc32",
		// Neither Content-Type nor x-amz-*.
		"Date", "Content-Length", "Cache-Control", "Expires", "Pragma", "connection", "ETag",
	} {
		if ComparedHeader(h) {
			t.Errorf("ComparedHeader(%q) = true; want false", h)
		}
	}
}

func TestDifferenceString(t *testing.T) {
	t.Parallel()
	cases := map[string]Difference{
		"status: 400 -> 409":                     {Path: "status", Kind: Changed, Expected: "400", Actual: "409"},
		"error/message: missing (AWS has m)":     {Path: "error/message", Kind: Missing, Expected: "m"},
		"body/message: extra (understudy has x)": {Path: "body/message", Kind: Extra, Actual: "x"},
		"body: order a,b -> b,a":                 {Path: "body", Kind: Reordered, Expected: "a,b", Actual: "b,a"},
	}
	for want, d := range cases {
		if got := d.String(); got != want {
			t.Errorf("%#v.String() = %q; want %q", d, got, want)
		}
	}
	kinds := map[DiffKind]string{Changed: "changed", Missing: "missing", Extra: "extra", Reordered: "reordered"}
	for k, want := range kinds {
		if got := k.String(); got != want {
			t.Errorf("DiffKind(%d).String() = %q; want %q", k, got, want)
		}
	}
}

// memberNames mixes names that member rules match with names none does.
var memberNames = []string{"RequestId", "Account", "CreationDate", "message", "__type", "Body", "k", "a/b", "Messages"}

// jsonValue draws a JSON value as text.
func jsonValue(t *rapid.T, depth int) string {
	kinds := 4
	if depth > 0 {
		kinds = 6
	}
	switch rapid.IntRange(0, kinds-1).Draw(t, "kind") {
	case 0:
		return strconv.Quote(rapid.StringMatching(`[A-Za-z0-9 :./-]{0,16}`).Draw(t, "string"))
	case 1:
		return rapid.SampledFrom([]string{"0", "1.5", "-3", "1791987671.668", "1e3"}).Draw(t, "number")
	case 2:
		return rapid.SampledFrom([]string{"true", "false"}).Draw(t, "bool")
	case 3:
		return "null"
	case 4:
		n := rapid.IntRange(0, 3).Draw(t, "elements")
		elems := make([]string, n)
		for i := range n {
			elems[i] = jsonValue(t, depth-1)
		}
		return "[" + strings.Join(elems, ",") + "]"
	default:
		names := rapid.SliceOfNDistinct(rapid.SampledFrom(memberNames), 0, 4, rapid.ID[string]).Draw(t, "names")
		members := make([]string, len(names))
		for i, n := range names {
			members[i] = strconv.Quote(n) + ":" + jsonValue(t, depth-1)
		}
		return "{" + strings.Join(members, ",") + "}"
	}
}

// xmlElement draws an XML element.
func xmlElement(t *rapid.T, depth int) string {
	name := rapid.SampledFrom([]string{"RequestId", "Account", "member", "Code", "a"}).Draw(t, "element")
	attr := ""
	if rapid.Bool().Draw(t, "attr") {
		attr = ` x="` + rapid.StringMatching(`[a-z0-9]{0,4}`).Draw(t, "attr value") + `"`
	}
	if depth == 0 || rapid.Bool().Draw(t, "leaf") {
		return "<" + name + attr + ">" + rapid.StringMatching(`[A-Za-z0-9 :-]{0,12}`).Draw(t, "text") + "</" + name + ">"
	}
	n := rapid.IntRange(0, 3).Draw(t, "children")
	var b strings.Builder
	for range n {
		b.WriteString(xmlElement(t, depth-1))
	}
	return "<" + name + attr + ">" + b.String() + "</" + name + ">"
}

// A response compared with itself has no difference, whatever its body.
func TestCompareIsReflexive(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		body := jsonValue(t, 3)
		if rapid.Bool().Draw(t, "xml") {
			body = xmlElement(t, 3)
		}
		status := rapid.SampledFrom([]int{200, 400, 500}).Draw(t, "status")
		r := withType("", status, body)
		if got := Compare(r, r, CompareOptions{}); len(got) != 0 {
			t.Fatalf("Compare(r, r) = %v for body %s; want no difference", got, body)
		}
	})
}

// Changing one body member that no normalizer covers is exactly one
// difference, at that member.
func TestCompareFindsEachUnnormalizedMember(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		vals := rapid.SliceOfN(rapid.StringMatching(`[a-z]{0,6}`), 1, 6).Draw(t, "values")
		i := rapid.IntRange(0, len(vals)-1).Draw(t, "changed")
		render := func(vs []string) string {
			ms := make([]string, len(vs))
			for j, v := range vs {
				ms[j] = `"k` + strconv.Itoa(j) + `":` + strconv.Quote(v)
			}
			return "{" + strings.Join(ms, ",") + "}"
		}
		changed := slices.Clone(vals)
		changed[i] += "q"
		got := summary(Compare(jsonOK(render(vals)), jsonOK(render(changed)), CompareOptions{}))
		want := []string{"body/k" + strconv.Itoa(i) + " changed"}
		if !slices.Equal(got, want) {
			t.Fatalf("Compare = %v; want %v", got, want)
		}
	})
}
