package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
)

var indexOpts = cmp.AllowUnexported(index{}, indexEntry{})

// llms.txt lists each policy under the "AWS managed policies" heading and
// links the single-file guide under "Full content" (iam-evaluation.md
// 10.2); the about-page link above the heading is no policy.
func TestParseIndexReadsTheFixture(t *testing.T) {
	got, err := parseIndex([]byte(readFixture(t, "llms.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(fixtureIndex(), got, indexOpts); diff != "" {
		t.Errorf("parseIndex (-want +got):\n%s", diff)
	}
	crlf, err := parseIndex([]byte(strings.ReplaceAll(readFixture(t, "llms.txt"), "\n", "\r\n")))
	if err != nil || !cmp.Equal(fixtureIndex(), crlf, indexOpts) {
		t.Errorf("parseIndex(CRLF) = %+v, %v; want the LF reading", crlf, err)
	}
}

// Every problem in llms.txt is a *ParseError of Source "llms.txt", all of
// them reported: a policy line not in the recorded form, whose name, page
// and title must agree (iam-evaluation.md 10.2), a name listed twice, no
// policy at all, and no link to the single-file guide.
func TestParseIndexReportsEveryProblem(t *testing.T) {
	fixture := readFixture(t, "llms.txt")
	line := func(name string) string {
		return "- [" + name + "](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/" + name + ".md): About the AWS managed policy: " + name
	}
	type want struct {
		field string
		// line is the 1-based line at fault, 0 when the thing is missing.
		line int
	}
	retitled := replaceOnce(t, fixture, "About the AWS managed policy: ExampleQueueReader", "About the AWS managed policy: ExampleOther")
	repaged := replaceOnce(t, fixture, "reference/ExampleQueueReader.md", "reference/ExampleOther.md")
	offsite := replaceOnce(t, fixture, "https://docs.aws.amazon.com/aws-managed-policy/latest/reference/ExampleQueueReader.md", "https://example.com/ExampleQueueReader.md")
	reshaped := replaceOnce(t, fixture, line("ExampleQueueReader"), "- ExampleQueueReader")
	// ExampleAdminAccess is listed first and again after ExampleQueueReader;
	// the second listing is the one at fault.
	twice := replaceOnce(t, fixture, line("ExampleQueueReader"), line("ExampleQueueReader")+"\n"+line("ExampleAdminAccess"))
	twoProblems := replaceOnce(t, replaceOnce(t, fixture, "reference/full.md)", "reference/full.txt)"), "About the AWS managed policy: ExampleAdminAccess", "About: ExampleAdminAccess")
	for _, c := range []struct {
		name string
		text string
		want []want
	}{
		{"title naming another policy", retitled, []want{{"policy line", lineOf(t, retitled, "[ExampleQueueReader]")}}},
		{"page of another policy", repaged, []want{{"policy line", lineOf(t, repaged, "[ExampleQueueReader]")}}},
		{"page on another site", offsite, []want{{"policy line", lineOf(t, offsite, "[ExampleQueueReader]")}}},
		{"list item of another form", reshaped, []want{{"policy line", lineOf(t, reshaped, "- ExampleQueueReader")}}},
		{"name listed twice", twice, []want{{"policy line", lineOf(t, twice, "[ExampleQueueReader]") + 1}}},
		{
			"no policy",
			strings.Join(slices.DeleteFunc(strings.Split(fixture, "\n"), func(l string) bool {
				return strings.Contains(l, "About the AWS managed policy")
			}), "\n"),
			[]want{{"policy line", 0}},
		},
		{
			"no full.md link",
			replaceOnce(t, fixture, "- [Complete guide (single file)](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/full.md)\n", ""),
			[]want{{"full link", 0}},
		},
		{"two problems", twoProblems, []want{{"policy line", lineOf(t, twoProblems, "[ExampleAdminAccess]")}, {"full link", 0}}},
	} {
		_, err := parseIndex([]byte(c.text))
		if err == nil {
			t.Errorf("%s: parseIndex accepted it", c.name)
			continue
		}
		pes := parseErrors(t, err)
		for _, w := range c.want {
			at := w.line
			if !slices.ContainsFunc(pes, func(pe *ParseError) bool {
				return pe.Source == "llms.txt" && pe.Field == w.field && pe.Line == at && pe.Problem != ""
			}) {
				t.Errorf("%s: parseIndex = %v, want a llms.txt %s error at line %d", c.name, err, w.field, at)
			}
		}
		if len(pes) != len(c.want) {
			t.Errorf("%s: parseIndex reported %d problems, want %d: %v", c.name, len(pes), len(c.want), err)
		}
	}
}

// The single-file guide is every policy page with its headings demoted one
// level, after a banner, a table of contents and the about page, which open
// no policy (iam-evaluation.md 10.3, 10.4).
func TestParseFullReadsTheFixture(t *testing.T) {
	got, err := parseFull([]byte(readFixture(t, "full.md")), fixtureIndex())
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(fixturePolicies(), got); diff != "" {
		t.Errorf("parseFull (-want +got):\n%s", diff)
	}
}

// The guide ends the Type and Creation time lines with a space
// (iam-evaluation.md 10.3); trailing white space outside the document and
// CRLF line ends change nothing.
func TestParseFullIgnoresTrailingSpaceAndCRLF(t *testing.T) {
	fixture := readFixture(t, "full.md")
	var b strings.Builder
	inFence := false
	for i, l := range strings.Split(fixture, "\n") {
		if l == "```" {
			inFence = !inFence
		}
		switch {
		case inFence || l == "```":
		case strings.HasPrefix(l, "+ **Type**:"), strings.HasPrefix(l, "+ **Creation time**:"):
			l += " "
		case strings.HasPrefix(l, "**Description**:"), strings.HasPrefix(l, "**Policy version:**"), strings.HasPrefix(l, "## "):
			l += " \t "
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(l)
	}
	spaced := b.String()
	for name, text := range map[string]string{
		"trailing spaces":         spaced,
		"CRLF":                    strings.ReplaceAll(fixture, "\n", "\r\n"),
		"CRLF and trailing space": strings.ReplaceAll(spaced, "\n", "\r\n"),
	} {
		got, err := parseFull([]byte(text), fixtureIndex())
		if err != nil {
			t.Errorf("%s: parseFull = %v", name, err)
			continue
		}
		if diff := cmp.Diff(fixturePolicies(), got); diff != "" {
			t.Errorf("%s: parseFull (-want +got):\n%s", name, diff)
		}
	}
}

// Every problem in the single-file guide is a *ParseError of that Source
// naming the policy and the field; a policy the index lists must open
// exactly one section, every field of the page layout must be there once,
// with its colon where the guide puts it, and the document is the one bare
// fence (iam-evaluation.md 10.3).
func TestParseFullReportsEveryProblem(t *testing.T) {
	fixture := readFixture(t, "full.md")
	const queue = "ExampleQueueReader"
	queueSection := fixture[strings.Index(fixture, "## "+queue+"\n"):strings.Index(fixture, "## ExampleServiceLinkedRolePolicy\n")]
	type want struct {
		policy, field string
		// line is the text of the line at fault, "" when the problem has
		// no line or its line is not asserted.
		line string
	}
	for _, c := range []struct {
		name string
		text string
		idx  index
		want []want
	}{
		{
			name: "listed policy without a section",
			text: fixture,
			idx:  index{policies: append(fixtureIndex().policies, indexEntry{"ExampleMissing", guidePrefix + "ExampleMissing.md"}), full: fixtureIndex().full},
			want: []want{{"ExampleMissing", "heading", ""}},
		},
		{
			name: "section the index does not list",
			text: fixture,
			idx:  index{policies: slices.DeleteFunc(fixtureIndex().policies, func(e indexEntry) bool { return e.name == queue }), full: fixtureIndex().full},
			want: []want{{queue, "heading", ""}},
		},
		{
			name: "two sections for one policy",
			text: fixture + "\n" + queueSection,
			want: []want{{queue, "heading", ""}},
		},
		{
			name: "heading without its anchor",
			text: replaceOnce(t, fixture, "## "+queue+"\n<a name=\""+queue+"\"></a>\n", "## "+queue+"\n\n"),
			want: []want{{queue, "heading", ""}},
		},
		{
			name: "no description",
			text: replaceOnce(t, fixture, "**Description**: Lets a worker read and delete example queue messages.\n", ""),
			want: []want{{queue, "description", ""}},
		},
		{
			name: "two descriptions",
			text: replaceOnce(t, fixture, "**Description**: Lets a worker read and delete example queue messages.\n", "**Description**: Lets a worker read and delete example queue messages.\n**Description**: Again.\n"),
			want: []want{{queue, "description", ""}},
		},
		{
			name: "unknown type",
			text: replaceOnce(t, fixture, "+ **Type**: Service role policy", "+ **Type**: Managed policy"),
			want: []want{{queue, "type", "+ **Type**: Managed policy"}},
		},
		{
			name: "no type",
			text: replaceOnce(t, fixture, "+ **Type**: Service role policy\n", ""),
			want: []want{{queue, "type", ""}},
		},
		{
			name: "creation time in another layout",
			text: replaceOnce(t, fixture, "+ **Creation time**: April 09, 2015, 15:03 UTC", "+ **Creation time**: 2015-04-09 15:03"),
			want: []want{{queue, "creation time", "+ **Creation time**: 2015-04-09 15:03"}},
		},
		{
			name: "creation time twice",
			text: replaceOnce(t, fixture, "+ **Creation time**: April 09, 2015, 15:03 UTC", "+ **Creation time**: April 09, 2015, 15:03 UTC\n+ **Creation time**: April 09, 2015, 15:04 UTC"),
			want: []want{{queue, "creation time", ""}},
		},
		{
			name: "edited time with the colon outside the bold",
			text: replaceOnce(t, fixture, "+ **Edited time:** April 09, 2015, 15:03 UTC", "+ **Edited time**: April 09, 2015, 15:03 UTC"),
			want: []want{{queue, "edited time", ""}},
		},
		{
			name: "edited time not in UTC",
			text: replaceOnce(t, fixture, "+ **Edited time:** April 09, 2015, 15:03 UTC", "+ **Edited time:** April 09, 2015, 15:03 PST"),
			want: []want{{queue, "edited time", "+ **Edited time:** April 09, 2015, 15:03 PST"}},
		},
		{
			name: "ARN of another policy",
			text: replaceOnce(t, fixture, "`arn:aws:iam::aws:policy/service-role/ExampleQueueReader`", "`arn:aws:iam::aws:policy/service-role/ExampleOther`"),
			want: []want{{queue, "arn", "ExampleOther`"}},
		},
		{
			name: "ARN of a customer account",
			text: replaceOnce(t, fixture, "`arn:aws:iam::aws:policy/service-role/ExampleQueueReader`", "`arn:aws:iam::123456789012:policy/service-role/ExampleQueueReader`"),
			want: []want{{queue, "arn", "123456789012"}},
		},
		{
			name: "no ARN",
			text: replaceOnce(t, fixture, "+ **ARN**: `arn:aws:iam::aws:policy/service-role/ExampleQueueReader`\n", ""),
			want: []want{{queue, "arn", ""}},
		},
		{
			name: "version not marked default",
			text: replaceOnce(t, fixture, "**Policy version:** v1 (default)", "**Policy version:** v1"),
			want: []want{{queue, "policy version", ""}},
		},
		{
			name: "version v0",
			text: replaceOnce(t, fixture, "**Policy version:** v1 (default)", "**Policy version:** v0 (default)"),
			want: []want{{queue, "policy version", "**Policy version:** v0 (default)"}},
		},
		{
			name: "no fence",
			text: replaceOnce(t, fixture, queueSection, strings.ReplaceAll(queueSection, "```\n", "")),
			want: []want{{queue, "document", ""}},
		},
		{
			name: "fence with a language tag",
			text: replaceOnce(t, fixture, queueSection, strings.ReplaceAll(queueSection, "```\n{", "```json\n{")),
			want: []want{{queue, "document", ""}},
		},
		{
			name: "unclosed fence",
			text: replaceOnce(t, fixture, queueSection, strings.Replace(queueSection, "}\n```\n", "}\n", 1)),
			want: []want{{queue, "document", ""}},
		},
		{
			name: "empty document",
			text: replaceOnce(t, fixture, queueSection, queueSection[:strings.Index(queueSection, "```\n")+4]+queueSection[strings.LastIndex(queueSection, "```\n"):]),
			want: []want{{queue, "document", ""}},
		},
		{
			name: "two policies broken",
			text: replaceOnce(t, replaceOnce(t, fixture, "+ **Type**: Service role policy", "+ **Type**: Role policy"), "**Policy version:** v3 (default)", "**Policy version:** 3 (default)"),
			want: []want{{queue, "type", "+ **Type**: Role policy"}, {"ExampleAdminAccess", "policy version", "**Policy version:** 3 (default)"}},
		},
	} {
		idx := c.idx
		if idx.full == "" {
			idx = fixtureIndex()
		}
		got, err := parseFull([]byte(c.text), idx)
		if err == nil {
			t.Errorf("%s: parseFull accepted it: %d policies", c.name, len(got))
			continue
		}
		pes := parseErrors(t, err)
		for _, w := range c.want {
			if !slices.ContainsFunc(pes, func(pe *ParseError) bool {
				return pe.Source == "full.md" && pe.Policy == w.policy && pe.Field == w.field && pe.Problem != "" &&
					(w.line == "" || pe.Line == lineOf(t, c.text, w.line))
			}) {
				t.Errorf("%s: parseFull = %v, want a full.md %s error for %s", c.name, err, w.field, w.policy)
			}
		}
		for _, pe := range pes {
			// A break in one policy may show in more than one of its
			// fields, never in another policy's.
			if !slices.ContainsFunc(c.want, func(w want) bool { return w.policy == pe.Policy }) {
				t.Errorf("%s: unexpected problem %+v", c.name, pe)
			}
			if !strings.Contains(pe.Error(), pe.Field) || (pe.Policy != "" && !strings.Contains(pe.Error(), pe.Policy)) {
				t.Errorf("%s: ParseError.Error() = %q, want the policy and the field named", c.name, pe.Error())
			}
		}
	}
}

// guideTime is a time in the guide's layout (iam-evaluation.md 10.3).
func guideTime(t time.Time) string { return t.Format("January 02, 2006, 15:04 UTC") }

// typeSpelling is each Type as a page's Type line spells it
// (iam-evaluation.md 10.4).
var typeSpelling = map[policycatalogue.Type]string{
	policycatalogue.AWSManaged:        "AWS managed policy",
	policycatalogue.ServiceLinkedRole: "Service-linked role policy",
	policycatalogue.ServiceRole:       "Service role policy",
	policycatalogue.JobFunction:       "Job function policy",
}

// renderFull writes ps as the single-file guide does: a preamble, then each
// policy page of iam-evaluation.md 10.3, trailing spaces included, with its
// headings demoted (10.4).
func renderFull(ps []policycatalogue.Policy) string {
	var b strings.Builder
	b.WriteString("# AWS Managed Policy Reference Guide\n\nSynthetic banner.\n\n## What are AWS managed policies?\n<a name=\"about-managed-policy-reference\"></a>\n\nSynthetic about page.\n")
	for i := range ps {
		p := &ps[i]
		fmt.Fprintf(&b, "\n## %[1]s\n<a name=\"%[1]s\"></a>\n\n**Description**: %[2]s\n\n`%[1]s` is synthetic.\n\n", p.Name, p.Description)
		fmt.Fprintf(&b, "### Using this policy\n<a name=\"%[1]s-how-to-use\"></a>\n\nSynthetic.\n\n", p.Name)
		fmt.Fprintf(&b, "### Policy details\n<a name=\"%s-details\"></a>\n+ **Type**: %s \n+ **Creation time**: %s \n+ **Edited time:** %s\n+ **ARN**: `%s`\n\n",
			p.Name, typeSpelling[p.Type], guideTime(p.Created), guideTime(p.Updated), p.ARN)
		fmt.Fprintf(&b, "### Policy version\n<a name=\"%s-version\"></a>\n\n**Policy version:** %s (default)\n\nSynthetic. \n\n", p.Name, p.VersionID)
		fmt.Fprintf(&b, "### JSON policy document\n<a name=\"%s-json\"></a>\n\n```\n%s\n```\n\n", p.Name, p.Document)
		fmt.Fprintf(&b, "### Learn more\n<a name=\"%s-learn-more\"></a>\n+ [Synthetic](https://example.com/)\n", p.Name)
	}
	return b.String()
}

// renderIndex writes llms.txt for ps (iam-evaluation.md 10.2).
func renderIndex(ps []policycatalogue.Policy) string {
	var b strings.Builder
	b.WriteString("# AWS Managed Policy Reference Guide\n\n## [AWS managed policies](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/policy-list.md)\n\n")
	for i := range ps {
		fmt.Fprintf(&b, "- [%[1]s](%[2]s%[1]s.md): About the AWS managed policy: %[1]s\n", ps[i].Name, guidePrefix)
	}
	b.WriteString("\n## Full content\n\n- [Complete guide (single file)](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/full.md)\n")
	return b.String()
}

// genGuidePolicy draws a policy as the guide can publish it: a name of the
// IAM model's policyNameType characters, a Type on its path
// (iam-evaluation.md 10.4), a v<N> version, minute-precision UTC times, a
// one-line description without trailing space and a multi-line document.
func genGuidePolicy() *rapid.Generator[policycatalogue.Policy] {
	paths := map[policycatalogue.Type]string{
		policycatalogue.AWSManaged: "/", policycatalogue.ServiceLinkedRole: "/aws-service-role/",
		policycatalogue.ServiceRole: "/service-role/", policycatalogue.JobFunction: "/job-function/",
	}
	return rapid.Custom(func(t *rapid.T) policycatalogue.Policy {
		name := rapid.StringMatching(`[A-Za-z0-9][A-Za-z0-9_+=,.@-]{0,30}`).Draw(t, "name")
		typ := rapid.SampledFrom([]policycatalogue.Type{policycatalogue.AWSManaged, policycatalogue.ServiceLinkedRole, policycatalogue.ServiceRole, policycatalogue.JobFunction}).Draw(t, "type")
		at := func(label string) time.Time {
			return time.Date(2010, time.January, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(rapid.Int64Range(0, 20*365*24*60).Draw(t, label)) * time.Minute)
		}
		resources := rapid.SliceOfN(rapid.StringMatching(`arn:aws:s3:::[a-z0-9*/-]{1,12}`), 1, 3).Draw(t, "resources")
		doc := "{\n  \"Version\" : \"2012-10-17\",\n  \"Statement\" : [\n    {\n      \"Effect\" : \"Allow\",\n      \"Action\" : \"s3:GetObject\",\n      \"Resource\" : [\n        \"" +
			strings.Join(resources, "\",\n        \"") + "\"\n      ]\n    }\n  ]\n}"
		return policycatalogue.Policy{
			Name:        name,
			ARN:         "arn:aws:iam::aws:policy" + paths[typ] + name,
			Path:        paths[typ],
			Type:        typ,
			Description: rapid.StringMatching("[A-Za-z0-9][A-Za-z0-9`*\\[\\]()<>&.,:;!? -]{0,40}[A-Za-z0-9.]").Draw(t, "description"),
			VersionID:   fmt.Sprintf("v%d", rapid.IntRange(1, 300).Draw(t, "version")),
			Created:     at("created"),
			Updated:     at("updated"),
			Document:    doc,
		}
	})
}

// For any policies the guide can publish, rendering llms.txt and the
// single-file guide in the guide's layout and parsing them back gives the
// policies, in index order.
func TestParseRoundTripProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ps := rapid.SliceOfNDistinct(genGuidePolicy(), 1, 6, func(p policycatalogue.Policy) string { return strings.ToLower(p.Name) }).Draw(t, "policies")
		idx, err := parseIndex([]byte(renderIndex(ps)))
		if err != nil {
			t.Fatalf("parseIndex = %v\n%s", err, renderIndex(ps))
		}
		want := index{full: guidePrefix + "full.md"}
		for _, p := range ps {
			want.policies = append(want.policies, indexEntry{p.Name, guidePrefix + p.Name + ".md"})
		}
		if diff := cmp.Diff(want, idx, indexOpts); diff != "" {
			t.Fatalf("parseIndex (-want +got):\n%s", diff)
		}
		got, err := parseFull([]byte(renderFull(ps)), idx)
		if err != nil {
			t.Fatalf("parseFull = %v\n%s", err, renderFull(ps))
		}
		if diff := cmp.Diff(ps, got); diff != "" {
			t.Fatalf("parseFull (-want +got):\n%s", diff)
		}
	})
}
