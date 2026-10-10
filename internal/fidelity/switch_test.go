package fidelity_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

type boundary string

func TestEntryCarriesProvenanceAndFormattedValue(t *testing.T) {
	s := fidelity.Switch[boundary]{
		Name:   "logs.filter.term-boundary",
		Value:  "token",
		Label:  evidence.AWSDoc,
		Ref:    "docs/research/phase1/logs-filter-patterns.md#5",
		Record: "R10/C1",
	}
	want := fidelity.Entry{
		Name:   "logs.filter.term-boundary",
		Value:  "token",
		Label:  evidence.AWSDoc,
		Ref:    "docs/research/phase1/logs-filter-patterns.md#5",
		Record: "R10/C1",
	}
	if got := s.Entry(); got != want {
		t.Errorf("Entry() = %+v, want %+v", got, want)
	}
	n := fidelity.Switch[int]{Name: "sqs.max", Value: 10, Label: evidence.Inferred}
	if got := n.Entry().Value; got != "10" {
		t.Errorf("Entry().Value of an int switch = %q, want 10", got)
	}
}
