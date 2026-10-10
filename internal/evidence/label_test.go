package evidence_test

import (
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/evidence"
)

func TestParseLabelAcceptsTheFiveLabels(t *testing.T) {
	for _, s := range []string{"aws-recorded", "third-party-recorded", "aws-doc", "sdk-source", "inferred"} {
		got, err := evidence.ParseLabel(s)
		if err != nil || string(got) != s {
			t.Errorf("ParseLabel(%q) = %q, %v", s, got, err)
		}
	}
	if n := len(evidence.Labels()); n != 5 {
		t.Errorf("Labels() has %d entries, want 5", n)
	}
}

func TestParseLabelRejectsOtherText(t *testing.T) {
	for _, s := range []string{"", "AWS-RECORDED", "recorded", "aws-doc "} {
		_, err := evidence.ParseLabel(s)
		var target *evidence.LabelError
		if !errors.As(err, &target) || target.Text != s {
			t.Errorf("ParseLabel(%q) error = %v, want *LabelError", s, err)
		}
	}
}
