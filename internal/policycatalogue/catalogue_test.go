package policycatalogue_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
)

// pathOf is the ARN path each Type is published under: the guide's four
// Type spellings pair one for one with these paths (iam-evaluation.md 10.4).
var pathOf = map[policycatalogue.Type]string{
	policycatalogue.AWSManaged:        "/",
	policycatalogue.ServiceLinkedRole: "/aws-service-role/",
	policycatalogue.ServiceRole:       "/service-role/",
	policycatalogue.JobFunction:       "/job-function/",
}

// minute is a creation or edit time as the guide publishes it: UTC, to the
// minute (iam-evaluation.md 10.3, 10.5).
func minute(year int, month time.Month, day, hour, mins int) time.Time {
	return time.Date(year, month, day, hour, mins, 0, 0, time.UTC)
}

// document is a synthetic identity policy in the layout the guide prints
// documents in: two-space indent and "key" : value spacing
// (iam-evaluation.md 10.3).
const document = "{\n  \"Version\" : \"2012-10-17\",\n  \"Statement\" : [\n    {\n      \"Effect\" : \"Allow\",\n      \"Action\" : \"sqs:ReceiveMessage\",\n      \"Resource\" : \"*\"\n    }\n  ]\n}"

// policy is a valid policy named name of type typ, on typ's path.
func policy(name string, typ policycatalogue.Type) policycatalogue.Policy {
	return policycatalogue.Policy{
		Name:        name,
		ARN:         "arn:aws:iam::aws:policy" + pathOf[typ] + name,
		Path:        pathOf[typ],
		Type:        typ,
		Description: "Synthetic policy " + name + ".",
		VersionID:   "v1",
		Created:     minute(2015, time.April, 9, 15, 3),
		Updated:     minute(2021, time.September, 27, 20, 16),
		Document:    document,
	}
}

func mustNew(t *testing.T, ps []policycatalogue.Policy) *policycatalogue.Catalogue {
	t.Helper()
	c, err := policycatalogue.New(ps)
	if err != nil {
		t.Fatalf("New = %v", err)
	}
	return c
}

func names(c *policycatalogue.Catalogue) []string {
	var out []string
	for p := range c.All() {
		out = append(out, p.Name)
	}
	return out
}

// invalids is every *InvalidError err joins, in order.
func invalids(t *testing.T, err error) []*policycatalogue.InvalidError {
	t.Helper()
	errs := []error{err}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		errs = joined.Unwrap()
	}
	out := make([]*policycatalogue.InvalidError, 0, len(errs))
	for _, e := range errs {
		var ie *policycatalogue.InvalidError
		if !errors.As(e, &ie) {
			t.Fatalf("New joined %q (%T), want only *InvalidError", e, e)
		}
		out = append(out, ie)
	}
	return out
}

// The four Type spellings of a policy page and the paths they pair with
// (iam-evaluation.md 10.3, 10.4); any other text, the empty string and a
// different case included, is no Type.
func TestTypeSpellingsAndPaths(t *testing.T) {
	for _, c := range []struct {
		typ            policycatalogue.Type
		spelling, path string
	}{
		{policycatalogue.AWSManaged, "AWS managed policy", "/"},
		{policycatalogue.ServiceLinkedRole, "Service-linked role policy", "/aws-service-role/"},
		{policycatalogue.ServiceRole, "Service role policy", "/service-role/"},
		{policycatalogue.JobFunction, "Job function policy", "/job-function/"},
	} {
		if got, ok := policycatalogue.ParseType(c.spelling); !ok || got != c.typ {
			t.Errorf("ParseType(%q) = %v, %v; want %v, true", c.spelling, got, ok, c.typ)
		}
		if got := c.typ.String(); got != c.spelling {
			t.Errorf("Type(%d).String() = %q, want %q", c.typ, got, c.spelling)
		}
		if got := c.typ.Path(); got != c.path {
			t.Errorf("%s.Path() = %q, want %q", c.spelling, got, c.path)
		}
	}
	var zero policycatalogue.Type
	if zero.String() != "" || zero.Path() != "" {
		t.Errorf("zero Type = %q on %q, want both empty", zero.String(), zero.Path())
	}
	for _, s := range []string{"", "aws managed policy", "AWS managed policy ", " Service role policy", "Service Role policy", "Managed policy"} {
		if got, ok := policycatalogue.ParseType(s); ok {
			t.Errorf("ParseType(%q) = %v, true; want false", s, got)
		}
	}
}

// New lists the policies by name regardless of case, the order the guide's
// llms.txt lists them in (iam-evaluation.md 10.2), whatever order it is
// given; Lookup matches the ARN exactly.
func TestNewOrdersByNameAndLooksUpByARN(t *testing.T) {
	in := []policycatalogue.Policy{
		policy("ExampleServiceLinked", policycatalogue.ServiceLinkedRole),
		policy("AWSLambdaBasicExecutionRole", policycatalogue.ServiceRole),
		policy("exampleLower", policycatalogue.AWSManaged),
		policy("AmazonSQSFullAccess", policycatalogue.AWSManaged),
		policy("ExampleJobFunction", policycatalogue.JobFunction),
	}
	c := mustNew(t, in)
	want := []string{"AmazonSQSFullAccess", "AWSLambdaBasicExecutionRole", "ExampleJobFunction", "exampleLower", "ExampleServiceLinked"}
	if got := names(c); !slices.Equal(got, want) {
		t.Errorf("All = %v, want %v", got, want)
	}
	if c.Len() != len(in) {
		t.Errorf("Len = %d, want %d", c.Len(), len(in))
	}
	for _, p := range in {
		got, ok := c.Lookup(p.ARN)
		if !ok {
			t.Errorf("Lookup(%s) found nothing", p.ARN)
			continue
		}
		if diff := cmp.Diff(p, got); diff != "" {
			t.Errorf("Lookup(%s) (-given +got):\n%s", p.ARN, diff)
		}
	}
	for _, arn := range []string{
		"arn:aws:iam::aws:policy/service-role/awslambdabasicexecutionrole",
		"arn:aws:iam::aws:policy/AWSLambdaBasicExecutionRole",
		"arn:aws:iam::000000000000:policy/service-role/AWSLambdaBasicExecutionRole",
		"",
	} {
		if p, ok := c.Lookup(arn); ok {
			t.Errorf("Lookup(%q) = %s, want nothing: the ARN must match exactly", arn, p.ARN)
		}
	}
	// All stops when the consumer does.
	for p := range c.All() {
		if p.Name != want[0] {
			t.Errorf("first policy = %s, want %s", p.Name, want[0])
		}
		break
	}
}

// New keeps its own copy: changing the slice it was given changes nothing.
func TestNewCopiesItsInput(t *testing.T) {
	in := []policycatalogue.Policy{policy("ExampleA", policycatalogue.AWSManaged)}
	c := mustNew(t, in)
	in[0].Description = "changed"
	if got, _ := c.Lookup("arn:aws:iam::aws:policy/ExampleA"); got.Description != "Synthetic policy ExampleA." {
		t.Errorf("Description after editing the input = %q, want the original", got.Description)
	}
}

// New accepts what the guide can publish: a 128-character name of IAM's
// policyNameType characters, a version with a suffix, an empty description,
// an edit time before the creation time, and any JSON document (policydoc's
// rules are the generator's -verify check, not New's).
func TestNewAcceptsTheEdges(t *testing.T) {
	long := policy(strings.Repeat("a", 128), policycatalogue.AWSManaged)
	chars := policy("Ex_+=,.@-1", policycatalogue.ServiceRole)
	suffixed := policy("ExampleSuffix", policycatalogue.JobFunction)
	suffixed.VersionID = "v190.2-a"
	dotted := policy("ExampleDot", policycatalogue.JobFunction)
	dotted.VersionID = "v2."
	undescribed := policy("ExampleUndescribed", policycatalogue.ServiceLinkedRole)
	undescribed.Description = ""
	backwards := policy("ExampleBackwards", policycatalogue.AWSManaged)
	backwards.Created, backwards.Updated = backwards.Updated, backwards.Created
	array := policy("ExampleArray", policycatalogue.AWSManaged)
	array.Document = "[]"
	c := mustNew(t, []policycatalogue.Policy{long, chars, suffixed, dotted, undescribed, backwards, array})
	if c.Len() != 7 {
		t.Errorf("Len = %d, want 7", c.Len())
	}
}

// New refuses a policy breaking any rule, reporting the policy's position,
// name and the field at fault. Name and VersionID follow the IAM model's
// policyNameType (1 to 128 of [\w+=,.@-]) and policyVersionIdType
// (^v[1-9][0-9]*(\.[A-Za-z0-9-]*)?$); Type pairs with Path as
// iam-evaluation.md 10.4 counts; times are UTC to the minute (10.5).
func TestNewRefusesEachRule(t *testing.T) {
	type mutation struct {
		name   string
		mutate func(p *policycatalogue.Policy)
		want   policycatalogue.Field
		// also lists fields a violation may report besides want.
		also []policycatalogue.Field
	}
	rename := func(n string) func(p *policycatalogue.Policy) {
		return func(p *policycatalogue.Policy) { p.Name, p.ARN = n, "arn:aws:iam::aws:policy"+p.Path+n }
	}
	version := func(v string) func(p *policycatalogue.Policy) {
		return func(p *policycatalogue.Policy) { p.VersionID = v }
	}
	doc := func(d string) func(p *policycatalogue.Policy) {
		return func(p *policycatalogue.Policy) { p.Document = d }
	}
	cases := []mutation{
		{"empty name", rename(""), policycatalogue.FieldPolicyName, nil},
		{"129-character name", rename(strings.Repeat("a", 129)), policycatalogue.FieldPolicyName, nil},
		{"name with a space", rename("Example A"), policycatalogue.FieldPolicyName, nil},
		{"name with a slash", rename("a/b"), policycatalogue.FieldPolicyName, nil},
		{"name with a colon", rename("a:b"), policycatalogue.FieldPolicyName, nil},
		{"path without a leading slash", func(p *policycatalogue.Policy) {
			p.Path, p.ARN = "service-role/", "arn:aws:iam::aws:policyservice-role/"+p.Name
		}, policycatalogue.FieldPath, []policycatalogue.Field{policycatalogue.FieldType}},
		{"path with a space", func(p *policycatalogue.Policy) {
			p.Path, p.ARN = "/a b/", "arn:aws:iam::aws:policy/a b/"+p.Name
		}, policycatalogue.FieldPath, []policycatalogue.Field{policycatalogue.FieldType}},
		{"ARN of another name", func(p *policycatalogue.Policy) { p.ARN = "arn:aws:iam::aws:policy/service-role/Other" }, policycatalogue.FieldARN, nil},
		{"ARN without the path", func(p *policycatalogue.Policy) { p.ARN = "arn:aws:iam::aws:policy/" + p.Name }, policycatalogue.FieldARN, nil},
		{"ARN of a customer account", func(p *policycatalogue.Policy) {
			p.ARN = "arn:aws:iam::000000000000:policy" + p.Path + p.Name
		}, policycatalogue.FieldARN, nil},
		{"ARN in another partition", func(p *policycatalogue.Policy) {
			p.ARN = "arn:aws-cn:iam::aws:policy" + p.Path + p.Name
		}, policycatalogue.FieldARN, nil},
		{"zero Type", func(p *policycatalogue.Policy) { p.Type = 0 }, policycatalogue.FieldType, nil},
		{"unknown Type", func(p *policycatalogue.Policy) { p.Type = 99 }, policycatalogue.FieldType, nil},
		{"Type on another path", func(p *policycatalogue.Policy) { p.Type = policycatalogue.AWSManaged }, policycatalogue.FieldType, nil},
		{"empty version", version(""), policycatalogue.FieldDefaultVersionID, nil},
		{"version without v", version("1"), policycatalogue.FieldDefaultVersionID, nil},
		{"version v0", version("v0"), policycatalogue.FieldDefaultVersionID, nil},
		{"version with a leading zero", version("v01"), policycatalogue.FieldDefaultVersionID, nil},
		{"version with a capital V", version("V1"), policycatalogue.FieldDefaultVersionID, nil},
		{"version suffix with an underscore", version("v1.a_b"), policycatalogue.FieldDefaultVersionID, nil},
		{"version with trailing space", version("v1 "), policycatalogue.FieldDefaultVersionID, nil},
		{"zero creation time", func(p *policycatalogue.Policy) { p.Created = time.Time{} }, policycatalogue.FieldCreateDate, nil},
		{"creation time with seconds", func(p *policycatalogue.Policy) { p.Created = p.Created.Add(30 * time.Second) }, policycatalogue.FieldCreateDate, nil},
		{"creation time with nanoseconds", func(p *policycatalogue.Policy) { p.Created = p.Created.Add(time.Nanosecond) }, policycatalogue.FieldCreateDate, nil},
		{"creation time outside UTC", func(p *policycatalogue.Policy) {
			p.Created = p.Created.In(time.FixedZone("CET", 3600))
		}, policycatalogue.FieldCreateDate, nil},
		{"zero edit time", func(p *policycatalogue.Policy) { p.Updated = time.Time{} }, policycatalogue.FieldUpdateDate, nil},
		{"edit time with seconds", func(p *policycatalogue.Policy) { p.Updated = p.Updated.Add(time.Second) }, policycatalogue.FieldUpdateDate, nil},
		{"edit time outside UTC", func(p *policycatalogue.Policy) {
			p.Updated = p.Updated.In(time.FixedZone("EST", -5*3600))
		}, policycatalogue.FieldUpdateDate, nil},
		{"empty document", doc(""), policycatalogue.FieldDocument, nil},
		{"truncated document", doc("{\n  \"Version\" : \"2012-10-17\""), policycatalogue.FieldDocument, nil},
		{"document that is not JSON", doc("Version: 2012-10-17"), policycatalogue.FieldDocument, nil},
		{"document with trailing data", doc(document + " {}"), policycatalogue.FieldDocument, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := policy("ExampleQueueReader", policycatalogue.ServiceRole)
			c.mutate(&bad)
			in := []policycatalogue.Policy{policy("ExampleFirst", policycatalogue.AWSManaged), bad}
			cat, err := policycatalogue.New(in)
			if err == nil {
				t.Fatalf("New accepted %+v", bad)
			}
			if cat != nil {
				t.Errorf("New returned a catalogue with its error: %d policies", cat.Len())
			}
			seen := false
			for _, ie := range invalids(t, err) {
				if ie.Index != 1 || ie.Name != bad.Name {
					t.Errorf("InvalidError %+v, want Index 1 and Name %q", ie, bad.Name)
				}
				if ie.Reason == "" || !strings.Contains(ie.Error(), string(ie.Field)) {
					t.Errorf("InvalidError %+v: Error() = %q, want a reason and the field named", ie, ie.Error())
				}
				switch {
				case ie.Field == c.want:
					seen = true
				case !slices.Contains(c.also, ie.Field):
					t.Errorf("InvalidError field %s, want %s", ie.Field, c.want)
				}
			}
			if !seen {
				t.Errorf("New = %v, want an InvalidError for %s", err, c.want)
			}
		})
	}
}

// ARNs are unique, and so are names regardless of case; the later policy is
// the one reported.
func TestNewRefusesDuplicates(t *testing.T) {
	for _, c := range []struct {
		name   string
		second policycatalogue.Policy
		want   policycatalogue.Field
		also   []policycatalogue.Field
	}{
		{"same ARN", policy("ExampleA", policycatalogue.AWSManaged), policycatalogue.FieldARN, []policycatalogue.Field{policycatalogue.FieldPolicyName}},
		{"same name on another path", policy("ExampleA", policycatalogue.ServiceRole), policycatalogue.FieldPolicyName, nil},
		{"name differing only in case", policy("EXAMPLEa", policycatalogue.JobFunction), policycatalogue.FieldPolicyName, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := []policycatalogue.Policy{policy("ExampleZ", policycatalogue.AWSManaged), policy("ExampleA", policycatalogue.AWSManaged), c.second}
			cat, err := policycatalogue.New(in)
			if err == nil || cat != nil {
				t.Fatalf("New = %v, %v; want a refusal of the duplicate", cat, err)
			}
			seen := false
			for _, ie := range invalids(t, err) {
				if ie.Index != 2 {
					t.Errorf("InvalidError %+v, want the later policy, Index 2", ie)
				}
				switch {
				case ie.Field == c.want:
					seen = true
				case !slices.Contains(c.also, ie.Field):
					t.Errorf("InvalidError field %s, want %s", ie.Field, c.want)
				}
			}
			if !seen {
				t.Errorf("New = %v, want an InvalidError for %s", err, c.want)
			}
		})
	}
}

// Every violation is reported, in input order, several for one policy
// when it breaks several rules.
func TestNewReportsEveryViolation(t *testing.T) {
	badVersion := policy("ExampleB", policycatalogue.AWSManaged)
	badVersion.VersionID = "v0"
	badDocument := policy("ExampleD", policycatalogue.AWSManaged)
	badDocument.Document = ""
	badTimes := policy("ExampleT", policycatalogue.AWSManaged)
	badTimes.Created, badTimes.Updated = time.Time{}, time.Time{}
	in := []policycatalogue.Policy{badVersion, policy("ExampleOK", policycatalogue.AWSManaged), badDocument, badTimes}
	_, err := policycatalogue.New(in)
	if err == nil {
		t.Fatal("New accepted three broken policies")
	}
	type at struct {
		index int
		field policycatalogue.Field
	}
	errs := invalids(t, err)
	got := make([]at, 0, len(errs))
	for _, ie := range errs {
		if n := len(got); n > 0 && got[n-1].index > ie.Index {
			t.Errorf("InvalidError for index %d after index %d, want input order", ie.Index, got[n-1].index)
		}
		got = append(got, at{ie.Index, ie.Field})
	}
	want := []at{{0, policycatalogue.FieldDefaultVersionID}, {2, policycatalogue.FieldDocument}, {3, policycatalogue.FieldCreateDate}, {3, policycatalogue.FieldUpdateDate}}
	byPosition := func(a, b at) int {
		if a.index != b.index {
			return a.index - b.index
		}
		return strings.Compare(string(a.field), string(b.field))
	}
	slices.SortFunc(got, byPosition)
	slices.SortFunc(want, byPosition)
	if !slices.Equal(got, want) {
		t.Errorf("violations = %v, want %v", got, want)
	}
}

// emptyCatalogue is policies.json holding no policy, in the canonical form.
const emptyCatalogue = "{\n  \"policies\": []\n}\n"

// A nil *Catalogue is the empty catalogue, and so is New of nothing.
func TestNilCatalogueIsEmpty(t *testing.T) {
	var c *policycatalogue.Catalogue
	if c.Len() != 0 || names(c) != nil {
		t.Errorf("nil catalogue Len %d, All %v; want empty", c.Len(), names(c))
	}
	if _, ok := c.Lookup("arn:aws:iam::aws:policy/ExampleA"); ok {
		t.Error("nil catalogue Lookup found a policy")
	}
	for _, cat := range []*policycatalogue.Catalogue{c, mustNew(t, nil)} {
		got, err := cat.Encode()
		if err != nil || string(got) != emptyCatalogue {
			t.Errorf("Encode of the empty catalogue = %q, %v; want %q", got, err, emptyCatalogue)
		}
	}
}

// canonical is policies.json for two synthetic policies in the generator's
// canonical form: sorted by name regardless of case, two-space indent,
// members in file order, dates in RFC 3339 UTC, no HTML escaping, UTF-8 as
// is and one trailing newline. The documents and descriptions are kept as
// published (ADR 0016 amendment: reformatted to JSON with no other changes).
const canonical = `{
  "policies": [
    {
      "policyName": "AmazonExampleReadOnly",
      "arn": "arn:aws:iam::aws:policy/AmazonExampleReadOnly",
      "path": "/",
      "type": "AWS managed policy",
      "description": "Reads <example> resources & \"tags\" for the café, see [the guide](https://example.com/a?b=1&c=2).",
      "defaultVersionId": "v190",
      "createDate": "2015-02-06T18:41:00Z",
      "updateDate": "2026-10-01T00:00:00Z",
      "document": "{\n  \"Version\" : \"2012-10-17\",\n  \"Statement\" : [\n    {\n      \"Effect\" : \"Allow\",\n      \"Action\" : [\n        \"s3:Get*\",\n        \"s3:List*\"\n      ],\n      \"Resource\" : \"*\",\n      \"Condition\" : {\n        \"StringLike\" : {\n          \"aws:username\" : \"a+b<c>&d\\\\e\"\n        }\n      }\n    }\n  ]\n}"
    },
    {
      "policyName": "ExampleQueueReader",
      "arn": "arn:aws:iam::aws:policy/service-role/ExampleQueueReader",
      "path": "/service-role/",
      "type": "Service role policy",
      "description": "",
      "defaultVersionId": "v2",
      "createDate": "2015-04-09T15:03:00Z",
      "updateDate": "2021-09-27T20:16:00Z",
      "document": "{\n  \"Version\" : \"2012-10-17\",\n  \"Statement\" : [\n    {\n      \"Effect\" : \"Allow\",\n      \"Action\" : \"sqs:ReceiveMessage\",\n      \"Resource\" : \"*\"\n    }\n  ]\n}"
    }
  ]
}
`

func canonicalPolicies() []policycatalogue.Policy {
	return []policycatalogue.Policy{
		{
			Name: "ExampleQueueReader", ARN: "arn:aws:iam::aws:policy/service-role/ExampleQueueReader", Path: "/service-role/",
			Type: policycatalogue.ServiceRole, VersionID: "v2",
			Created: minute(2015, time.April, 9, 15, 3), Updated: minute(2021, time.September, 27, 20, 16), Document: document,
		},
		{
			Name: "AmazonExampleReadOnly", ARN: "arn:aws:iam::aws:policy/AmazonExampleReadOnly", Path: "/",
			Type:        policycatalogue.AWSManaged,
			Description: `Reads <example> resources & "tags" for the café, see [the guide](https://example.com/a?b=1&c=2).`,
			VersionID:   "v190",
			Created:     minute(2015, time.February, 6, 18, 41), Updated: minute(2026, time.October, 1, 0, 0),
			Document: "{\n  \"Version\" : \"2012-10-17\",\n  \"Statement\" : [\n    {\n      \"Effect\" : \"Allow\",\n      \"Action\" : [\n        \"s3:Get*\",\n        \"s3:List*\"\n      ],\n      \"Resource\" : \"*\",\n" +
				"      \"Condition\" : {\n        \"StringLike\" : {\n          \"aws:username\" : \"a+b<c>&d\\\\e\"\n        }\n      }\n    }\n  ]\n}",
		},
	}
}

func TestEncodeWritesTheCanonicalForm(t *testing.T) {
	got, err := mustNew(t, canonicalPolicies()).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(canonical, string(got)); diff != "" {
		t.Errorf("Encode (-want +got):\n%s", diff)
	}
}

// Parse reads the canonical form back to the same policies, and Encode of
// what it read is the text it was given.
func TestParseReadsTheCanonicalForm(t *testing.T) {
	c, err := policycatalogue.Parse([]byte(canonical))
	if err != nil {
		t.Fatal(err)
	}
	want := canonicalPolicies()
	slices.Reverse(want)
	if diff := cmp.Diff(want, slices.Collect(c.All())); diff != "" {
		t.Errorf("Parse (-want +got):\n%s", diff)
	}
	again, err := c.Encode()
	if err != nil || string(again) != canonical {
		t.Errorf("Encode(Parse(canonical)) = %q, %v; want the input", again, err)
	}
	if empty, err := policycatalogue.Parse([]byte(emptyCatalogue)); err != nil || empty.Len() != 0 {
		t.Errorf("Parse(empty catalogue) = %v, %v", empty, err)
	}
}

// Parse requires neither the canonical layout nor the canonical order:
// those are -verify's check. It still lists in catalogue order.
func TestParseAcceptsAnyLayoutAndOrder(t *testing.T) {
	text := `{"policies":[` +
		`{"arn":"arn:aws:iam::aws:policy/service-role/ExampleQueueReader","policyName":"ExampleQueueReader","path":"/service-role/","type":"Service role policy","description":"","defaultVersionId":"v2","createDate":"2015-04-09T15:03:00Z","updateDate":"2021-09-27T20:16:00Z","document":"[]"},` +
		`{"policyName":"AmazonExampleReadOnly","arn":"arn:aws:iam::aws:policy/AmazonExampleReadOnly","path":"/","type":"AWS managed policy","description":"d","defaultVersionId":"v1","createDate":"2015-02-06T18:41:00Z","updateDate":"2015-02-06T18:41:00Z","document":"{}"}` +
		`]}`
	c, err := policycatalogue.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if got := names(c); !slices.Equal(got, []string{"AmazonExampleReadOnly", "ExampleQueueReader"}) {
		t.Errorf("All = %v, want catalogue order", got)
	}
	encoded, err := c.Encode()
	if err != nil || string(encoded) == text || !strings.HasPrefix(string(encoded), "{\n  \"policies\": [\n") {
		t.Errorf("Encode = %q, %v; want the canonical form", encoded, err)
	}
}

// Parse reads strictly: anything but the format's one object of string
// members is a *DecodeError, and nothing is returned with it.
func TestParseRefusesWhatIsNotTheFormat(t *testing.T) {
	member := func(name, value string) string {
		p := map[string]string{
			"policyName": `"ExampleA"`, "arn": `"arn:aws:iam::aws:policy/ExampleA"`, "path": `"/"`, "type": `"AWS managed policy"`,
			"description": `""`, "defaultVersionId": `"v1"`, "createDate": `"2015-02-06T18:41:00Z"`, "updateDate": `"2015-02-06T18:41:00Z"`, "document": `"{}"`,
		}
		p[name] = value
		var b strings.Builder
		b.WriteString(`{"policies":[{`)
		for i, k := range []string{"policyName", "arn", "path", "type", "description", "defaultVersionId", "createDate", "updateDate", "document"} {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`"` + k + `":` + p[k])
		}
		b.WriteString(`}]}`)
		return b.String()
	}
	if _, err := policycatalogue.Parse([]byte(member("description", `""`))); err != nil {
		t.Fatalf("Parse of a valid policy = %v", err)
	}
	for name, text := range map[string]string{
		"empty input":             "",
		"not JSON":                "policies: []",
		"an array":                "[]",
		"no policies member":      "{}",
		"unknown top member":      `{"policies":[],"retrieved":"2026-10-25"}`,
		"duplicate top member":    `{"policies":[],"policies":[]}`,
		"policies not an array":   `{"policies":{}}`,
		"trailing data":           emptyCatalogue + "{}",
		"unknown policy member":   strings.Replace(member("description", `""`), `"document"`, `"sha256":"",`+`"document"`, 1),
		"duplicate policy member": strings.Replace(member("description", `""`), `"document"`, `"path":"/","document"`, 1),
		"number for a string":     member("defaultVersionId", "1"),
		"null for a string":       member("description", "null"),
		"object for a document":   member("document", "{}"),
	} {
		c, err := policycatalogue.Parse([]byte(text))
		var de *policycatalogue.DecodeError
		if !errors.As(err, &de) || c != nil {
			t.Errorf("Parse(%s) = %v, %v; want only a *DecodeError", name, c, err)
			continue
		}
		if de.Error() == "" || errors.Unwrap(de) == nil {
			t.Errorf("Parse(%s): DecodeError %q wraps %v, want a message and the cause", name, de.Error(), errors.Unwrap(de))
		}
	}
	// A member that is a string but no value of its kind is refused, as a
	// DecodeError or as New's InvalidError.
	for name, text := range map[string]string{
		"date in the guide's layout": member("createDate", `"April 09, 2015, 15:03 UTC"`),
		"date with an offset":        member("createDate", `"2015-02-06T19:41:00+01:00"`),
		"unknown type spelling":      member("type", `"Managed policy"`),
	} {
		if c, err := policycatalogue.Parse([]byte(text)); err == nil || c != nil {
			t.Errorf("Parse(%s) = %v, %v; want a refusal", name, c, err)
		}
	}
}

// Parse applies New's rules, reporting the policy's position in the file.
func TestParseAppliesNewsRules(t *testing.T) {
	text := strings.Replace(canonical, `"defaultVersionId": "v2"`, `"defaultVersionId": "v0"`, 1)
	c, err := policycatalogue.Parse([]byte(text))
	if c != nil {
		t.Errorf("Parse returned a catalogue of %d with a broken policy", c.Len())
	}
	var ie *policycatalogue.InvalidError
	if !errors.As(err, &ie) || ie.Index != 1 || ie.Name != "ExampleQueueReader" || ie.Field != policycatalogue.FieldDefaultVersionID {
		t.Errorf("Parse = %v, want an InvalidError for ExampleQueueReader, index 1, defaultVersionId", err)
	}
}

// Static serves the catalogue it was given, nil included.
func TestStaticServesItsCatalogue(t *testing.T) {
	c := mustNew(t, canonicalPolicies())
	for _, want := range []*policycatalogue.Catalogue{c, nil} {
		load := policycatalogue.Static(want)
		for range 2 {
			if got, err := load(); got != want || err != nil {
				t.Errorf("Static(%p)() = %p, %v; want the catalogue and no error", want, got, err)
			}
		}
	}
}

// The embedded catalogue loads, and every call returns the same one.
func TestEmbeddedLoadsOnce(t *testing.T) {
	first, err := policycatalogue.Embedded()
	if err != nil {
		t.Fatalf("Embedded = %v", err)
	}
	again, err := policycatalogue.Embedded()
	if err != nil || again != first {
		t.Errorf("second Embedded = %p, %v; want %p", again, err, first)
	}
}
