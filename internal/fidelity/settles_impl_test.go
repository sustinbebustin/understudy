package fidelity_test

import (
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// A step reference is the scenario directory under testdata/parity and the
// step's fixture name without ".json", "<svc>/<item>#<step>".
func TestParseStepRefReadsScenarioAndStep(t *testing.T) {
	t.Parallel()
	got, err := fidelity.ParseStepRef("sqs/R01#03-recv-after-6s")
	if err != nil {
		t.Fatalf("ParseStepRef = %v", err)
	}
	if want := (fidelity.StepRef{Scenario: "sqs/R01", Step: "03-recv-after-6s"}); got != want {
		t.Errorf("ParseStepRef = %+v, want %+v", got, want)
	}
	if s := got.String(); s != "sqs/R01#03-recv-after-6s" {
		t.Errorf("String() = %q", s)
	}
}

func TestParseStepRefRefusesOtherText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ text, reason string }{
		{"", "no #"},
		{"sqs/R01", "no #"},
		{"sqs#03", "no /"},
		{"/R01#03", "service is empty"},
		{"SQS/R01#03", "service"},
		{"sqs/#03", "item is empty"},
		{"sqs/R_01#03", "item"},
		{"sqs/R01/x#03", "item"},
		{"sqs/R01#", "step is empty"},
		{"sqs/R01#a#b", "step"},
		{"sqs/R01#a b", "step"},
	} {
		_, err := fidelity.ParseStepRef(tc.text)
		refErr, ok := errors.AsType[*fidelity.StepRefError](err)
		if !ok {
			t.Errorf("ParseStepRef(%q) = %v, want a *StepRefError", tc.text, err)
			continue
		}
		if refErr.Text != tc.text || !strings.Contains(refErr.Reason, tc.reason) {
			t.Errorf("ParseStepRef(%q) = %+v, want Text %q and a reason naming %q", tc.text, refErr, tc.text, tc.reason)
		}
		if msg := err.Error(); !strings.Contains(msg, "<svc>/<item>#<step>") {
			t.Errorf("ParseStepRef(%q) error %q does not show the form to write", tc.text, msg)
		}
	}
}

// Every reference String writes reads back as itself, as text and through
// MarshalText and UnmarshalText.
func TestStepRefRoundTrips(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		want := fidelity.StepRef{
			Scenario: rapid.StringMatching(`[a-z0-9]{1,8}/[A-Za-z0-9-]{1,12}`).Draw(t, "scenario"),
			Step:     rapid.StringMatching(`[A-Za-z0-9._-]{1,16}`).Draw(t, "step"),
		}
		got, err := fidelity.ParseStepRef(want.String())
		if err != nil || got != want {
			t.Fatalf("ParseStepRef(%q) = %+v, %v; want %+v", want.String(), got, err, want)
		}
		text, err := want.MarshalText()
		if err != nil {
			t.Fatalf("MarshalText = %v", err)
		}
		var back fidelity.StepRef
		if err := back.UnmarshalText(text); err != nil || back != want {
			t.Fatalf("UnmarshalText(%q) = %+v, %v; want %+v", text, back, err, want)
		}
	})
}
