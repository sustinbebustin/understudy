package main

import (
	"slices"
	"testing"
)

// A link to the single-file guide under "Full content" that is not under
// the guide is reported at its line by parseIndex, so fetch never requests
// it.
func TestParseIndexRefusesAGuideOffTheSite(t *testing.T) {
	text := replaceOnce(t, readFixture(t, "llms.txt"), "(https://docs.aws.amazon.com/aws-managed-policy/latest/reference/full.md)", "(https://example.com/full.md)")
	_, err := parseIndex([]byte(text))
	pes := parseErrors(t, err)
	if len(pes) != 1 || pes[0].Field != fieldFullLink || pes[0].Line != lineOf(t, text, "https://example.com/full.md") {
		t.Errorf("parseIndex = %v, want one full link error at the link's line", err)
	}
}

// Without its details anchor a page has none of the four detail lines
// where the guide puts them: each is reported for that policy alone.
func TestParseFullWithoutTheDetailsAnchor(t *testing.T) {
	text := replaceOnce(t, readFixture(t, "full.md"), "<a name=\"ExampleQueueReader-details\"></a>\n", "")
	_, err := parseFull([]byte(text), fixtureIndex())
	pes := parseErrors(t, err)
	fields := make([]string, 0, len(pes))
	for _, pe := range pes {
		if pe.Policy != "ExampleQueueReader" {
			t.Errorf("unexpected problem %+v", pe)
		}
		fields = append(fields, pe.Field)
	}
	if want := []string{fieldType, fieldCreationTime, fieldEditedTime, fieldARN}; !slices.Equal(fields, want) {
		t.Errorf("fields = %q, want %q", fields, want)
	}
}
