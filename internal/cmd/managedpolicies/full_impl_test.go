package main

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The guide writes each underscore of a policy's heading as \_ and keeps
// the plain name in the anchors, llms.txt and the ARN (iam-evaluation.md
// 10.4): the escaped heading still opens the policy's section, including
// for a policy named like a details anchor.
func TestParseFullReadsAnEscapedHeading(t *testing.T) {
	for _, name := range []string{"Example_Queue_Admin", "Example_Queue-details"} {
		t.Run(name, func(t *testing.T) {
			ps := fixturePolicies()[:1]
			ps[0].Name, ps[0].ARN = name, "arn:aws:iam::aws:policy/"+name
			idx, err := parseIndex([]byte(renderIndex(ps)))
			if err != nil {
				t.Fatal(err)
			}
			escaped := strings.ReplaceAll(name, "_", `\_`)
			text := replaceOnce(t, renderFull(ps), "\n## "+name+"\n", "\n## "+escaped+"\n")
			got, err := parseFull([]byte(text), idx)
			if err != nil {
				t.Fatalf("parseFull = %v", err)
			}
			if diff := cmp.Diff(ps, got); diff != "" {
				t.Errorf("parseFull (-want +got):\n%s", diff)
			}
		})
	}
}

// The guide escapes some descriptions' underscores as \_ and once an
// asterisk as \* (iam-evaluation.md 10.4): the escapes are markdown, not
// text, and the catalogue keeps the text. Other markdown stays as published.
func TestParseFullUnescapesTheDescription(t *testing.T) {
	ps := fixturePolicies()[:1]
	ps[0].Description = "Runs the EXAMPLE_ACTION mitigation for *.example names. See `notes`."
	idx, err := parseIndex([]byte(renderIndex(ps)))
	if err != nil {
		t.Fatal(err)
	}
	published := "**Description**: Runs the EXAMPLE\\_ACTION mitigation for \\*.example names. See `notes`.\n"
	text := replaceOnce(t, renderFull(ps), "**Description**: "+ps[0].Description+"\n", published)
	got, err := parseFull([]byte(text), idx)
	if err != nil {
		t.Fatalf("parseFull = %v", err)
	}
	if diff := cmp.Diff(ps, got); diff != "" {
		t.Errorf("parseFull (-want +got):\n%s", diff)
	}
}
