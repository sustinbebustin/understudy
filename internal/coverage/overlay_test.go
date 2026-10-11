package coverage_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// An entry's evidence is one item or a list of items covering traits;
// members other than the evidence trait are skipped.
func TestParseOverlayReadsEveryEvidenceItem(t *testing.T) {
	doc := `{
	  "smithy": "2.0",
	  "shapes": {
	    "com.amazonaws.sqs#A": {
	      "type": "apply",
	      "members": {"m": {"traits": {"smithy.api#length": {"max": 3}}}},
	      "traits": {
	        "smithy.api#httpError": 400,
	        "dev.understudy#evidence": {"label": "aws-doc", "source": "https://docs.aws.amazon.com/x", "validated": "2026-10-08"}
	      }
	    },
	    "com.amazonaws.sqs#B": {
	      "type": "structure",
	      "traits": {"dev.understudy#evidence": [
	        {"label": "third-party-recorded", "source": "s1", "covers": ["smithy.api#error", "smithy.api#httpError"]},
	        {"label": "inferred", "source": "s2", "covers": ["aws.protocols#awsQueryError"]}
	      ]}
	    }
	  }
	}`
	got, err := coverage.ParseOverlay("sqs.json", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := []coverage.OverlayEntry{
		{File: "sqs.json", Shape: "com.amazonaws.sqs#A", Label: evidence.AWSDoc, Source: "https://docs.aws.amazon.com/x", Validated: "2026-10-08"},
		{File: "sqs.json", Shape: "com.amazonaws.sqs#B", Label: evidence.ThirdPartyRecorded, Source: "s1", Covers: []string{"smithy.api#error", "smithy.api#httpError"}},
		{File: "sqs.json", Shape: "com.amazonaws.sqs#B", Label: evidence.Inferred, Source: "s2", Covers: []string{"aws.protocols#awsQueryError"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseOverlay =\n%+v\nwant\n%+v", got, want)
	}
}

// An entry without evidence, with a label outside the vocabulary, without
// a source or with an unknown evidence member is refused, naming the
// shape.
func TestParseOverlayRefusesEntriesWithoutEvidence(t *testing.T) {
	entry := func(traits string) string {
		return `{"shapes": {"s#X": {"type": "apply", "traits": {` + traits + `}}}}`
	}
	for name, doc := range map[string]string{
		"no evidence":    entry(`"smithy.api#httpError": 400`),
		"unknown label":  entry(`"dev.understudy#evidence": {"label": "recorded", "source": "s"}`),
		"no source":      entry(`"dev.understudy#evidence": {"label": "inferred"}`),
		"unknown member": entry(`"dev.understudy#evidence": {"label": "inferred", "source": "s", "note": "n"}`),
		"not an item":    entry(`"dev.understudy#evidence": "aws-doc"`),
	} {
		_, err := coverage.ParseOverlay("x.json", []byte(doc))
		var target *coverage.OverlayError
		if !errors.As(err, &target) || target.Shape != "s#X" {
			t.Errorf("%s: ParseOverlay = %v, want an *OverlayError naming s#X", name, err)
		}
	}
	if _, err := coverage.ParseOverlay("x.json", []byte(`{"shapes": `)); err == nil {
		t.Error("a truncated document parsed")
	}
}

// Every checked-in overlay parses, and every entry states its evidence.
func TestCheckedInOverlaysParse(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "codegen", "overlays", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no overlays found: %v", err)
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := coverage.ParseOverlay(filepath.Base(p), data)
		if err != nil {
			t.Error(err)
		}
		if len(entries) == 0 {
			t.Errorf("%s has no entries", p)
		}
	}
}

// Whatever the items, ParseOverlay reads back what was written.
func TestParseOverlayProperty(t *testing.T) {
	text := rapid.StringMatching(`[ -~]{0,12}`)
	rapid.Check(t, func(t *rapid.T) {
		shapes := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{1,6}#[A-Z][a-z]{0,6}`), 1, 8, rapid.ID).Draw(t, "shapes")
		want := make([]coverage.OverlayEntry, 0, len(shapes))
		var doc strings.Builder
		doc.WriteString(`{"shapes": {`)
		for i, shape := range shapes {
			if i > 0 {
				doc.WriteString(", ")
			}
			doc.Write(jsonx.AppendString(nil, shape))
			doc.WriteString(`: {"type": "apply", "traits": {"dev.understudy#evidence": [`)
			for j := range rapid.IntRange(1, 3).Draw(t, "items") {
				e := coverage.OverlayEntry{
					File: "f.json", Shape: shape, Label: rapid.SampledFrom(evidence.Labels()).Draw(t, "label"),
					Source: "src-" + text.Draw(t, "source"), Validated: text.Draw(t, "validated"),
				}
				if j > 0 {
					doc.WriteString(", ")
				}
				doc.WriteString(`{"label": `)
				doc.Write(jsonx.AppendString(nil, string(e.Label)))
				doc.WriteString(`, "source": `)
				doc.Write(jsonx.AppendString(nil, e.Source))
				if e.Validated != "" {
					doc.WriteString(`, "validated": `)
					doc.Write(jsonx.AppendString(nil, e.Validated))
				}
				doc.WriteString("}")
				want = append(want, e)
			}
			doc.WriteString("]}}")
		}
		doc.WriteString("}}")
		got, err := coverage.ParseOverlay("f.json", []byte(doc.String()))
		if err != nil {
			t.Fatalf("ParseOverlay(%s) = %v", doc.String(), err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ParseOverlay =\n%+v\nwant\n%+v", got, want)
		}
	})
}
