package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
)

// The fixtures under testdata are hand-written in the guide's layouts
// (iam-evaluation.md 10.1 to 10.4) and hold no AWS policy text: robots.txt,
// llms.txt and the single-file guide, testdata/full.md, whose four synthetic
// policies are fixturePolicies.

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// utc is a time as the guide prints it, to the minute in UTC
// (iam-evaluation.md 10.3, 10.5).
func utc(year int, month time.Month, day, hour, mins int) time.Time {
	return time.Date(year, month, day, hour, mins, 0, 0, time.UTC)
}

// fixtureNames are the policies testdata/llms.txt lists, in its order.
var fixtureNames = []string{"ExampleAdminAccess", "ExampleDataScientist", "ExampleQueueReader", "ExampleServiceLinkedRolePolicy"}

// fixtureIndex is testdata/llms.txt read: each policy's page is the guide's
// prefix, its name and .md (iam-evaluation.md 10.2).
func fixtureIndex() index {
	idx := index{full: "https://docs.aws.amazon.com/aws-managed-policy/latest/reference/full.md"}
	for _, name := range fixtureNames {
		idx.policies = append(idx.policies, indexEntry{name: name, page: "https://docs.aws.amazon.com/aws-managed-policy/latest/reference/" + name + ".md"})
	}
	return idx
}

// fixturePolicies are testdata/full.md's policies in testdata/llms.txt's
// order: the page's name, its ARN split into path and name, its Type, its
// Description with the markdown kept, its default version, its times and the
// lines between its bare fences joined by "\n" (iam-evaluation.md 10.3,
// 10.5).
func fixturePolicies() []policycatalogue.Policy {
	return []policycatalogue.Policy{
		{
			Name: "ExampleAdminAccess", ARN: "arn:aws:iam::aws:policy/ExampleAdminAccess", Path: "/",
			Type:        policycatalogue.AWSManaged,
			Description: "Grants every example permission. See the [example guide](https://example.com/guide?a=1&b=2) and `notes` <here>.",
			VersionID:   "v3",
			Created:     utc(2015, time.February, 6, 18, 39), Updated: utc(2024, time.March, 4, 10, 5),
			Document: strings.Join([]string{
				`{`,
				`  "Version" : "2012-10-17",`,
				`  "Statement" : [`,
				`    {`,
				`      "Effect" : "Allow",`,
				`      "Action" : [`,
				`        "sqs:*",`,
				`        "sns:*"`,
				`      ],`,
				`      "Resource" : "*"`,
				`    }`,
				`  ]`,
				`}`,
			}, "\n"),
		},
		{
			Name: "ExampleDataScientist", ARN: "arn:aws:iam::aws:policy/job-function/ExampleDataScientist", Path: "/job-function/",
			Type:        policycatalogue.JobFunction,
			Description: "Synthetic job function policy for data scientists.",
			VersionID:   "v190",
			Created:     utc(2016, time.November, 10, 17, 2), Updated: utc(2026, time.October, 1, 0, 0),
			Document: strings.Join([]string{
				`{`,
				`  "Version" : "2012-10-17",`,
				`  "Statement" : [`,
				`    {`,
				`      "Sid" : "ReadNotebooks",`,
				`      "Effect" : "Allow",`,
				`      "Action" : "s3:GetObject",`,
				`      "Resource" : "arn:aws:s3:::example-notebooks/*",`,
				`      "Condition" : {`,
				`        "StringLike" : {`,
				`          "aws:username" : "data+science"`,
				`        }`,
				`      }`,
				`    }`,
				`  ]`,
				`}`,
			}, "\n"),
		},
		{
			Name: "ExampleQueueReader", ARN: "arn:aws:iam::aws:policy/service-role/ExampleQueueReader", Path: "/service-role/",
			Type:        policycatalogue.ServiceRole,
			Description: "Lets a worker read and delete example queue messages.",
			VersionID:   "v1",
			Created:     utc(2015, time.April, 9, 15, 3), Updated: utc(2015, time.April, 9, 15, 3),
			Document: strings.Join([]string{
				`{`,
				`  "Version" : "2012-10-17",`,
				`  "Statement" : [`,
				`    {`,
				`      "Action" : [`,
				`        "sqs:ReceiveMessage",`,
				`        "sqs:DeleteMessage"`,
				`      ],`,
				`      "Effect" : "Allow",`,
				`      "Resource" : "*"`,
				`    }`,
				`  ]`,
				`}`,
			}, "\n"),
		},
		{
			Name: "ExampleServiceLinkedRolePolicy", ARN: "arn:aws:iam::aws:policy/aws-service-role/ExampleServiceLinkedRolePolicy", Path: "/aws-service-role/",
			Type:        policycatalogue.ServiceLinkedRole,
			Description: "Lets the example service manage its log groups.",
			VersionID:   "v12",
			Created:     utc(2019, time.January, 31, 23, 59), Updated: utc(2022, time.June, 15, 8, 30),
			Document: strings.Join([]string{
				`{`,
				`  "Version" : "2012-10-17",`,
				`  "Statement" : [`,
				`    {`,
				`      "Effect" : "Allow",`,
				`      "Action" : "logs:PutLogEvents",`,
				`      "Resource" : "arn:aws:logs:*:*:log-group:/example/*"`,
				`    }`,
				`  ]`,
				`}`,
			}, "\n"),
		},
	}
}

func fixtureCatalogue(t *testing.T) *policycatalogue.Catalogue {
	t.Helper()
	c, err := policycatalogue.New(fixturePolicies())
	if err != nil {
		t.Fatalf("policycatalogue.New(fixture) = %v", err)
	}
	return c
}

// parseErrors is every *ParseError err joins, in order.
func parseErrors(t *testing.T, err error) []*ParseError {
	t.Helper()
	errs := []error{err}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		errs = joined.Unwrap()
	}
	out := make([]*ParseError, 0, len(errs))
	for _, e := range errs {
		var pe *ParseError
		if !errors.As(e, &pe) {
			t.Fatalf("error %q (%T), want only *ParseError", e, e)
		}
		out = append(out, pe)
	}
	return out
}

// lineOf is the 1-based number of the first line of text holding s.
func lineOf(t *testing.T, text, s string) int {
	t.Helper()
	for i, line := range strings.Split(text, "\n") {
		if strings.Contains(line, s) {
			return i + 1
		}
	}
	t.Fatalf("no line holds %q", s)
	return 0
}

// replaceOnce replaces the one occurrence of old in text with by, failing
// the test when old is not there exactly once.
func replaceOnce(t *testing.T, text, old, by string) string {
	t.Helper()
	if n := strings.Count(text, old); n != 1 {
		t.Fatalf("%q occurs %d times in the fixture, want once", old, n)
	}
	return strings.Replace(text, old, by, 1)
}
