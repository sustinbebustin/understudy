package evidence_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/evidence"
)

// A claim backed by several items is as strong as the weakest of them, in
// Labels order; no items, or a label outside the vocabulary, gives none.
func TestWeakestIsTheLastInLabelsOrder(t *testing.T) {
	tests := []struct {
		in   []evidence.Label
		want evidence.Label
		ok   bool
	}{
		{[]evidence.Label{evidence.AWSRecorded}, evidence.AWSRecorded, true},
		{[]evidence.Label{evidence.AWSRecorded, evidence.Inferred, evidence.AWSDoc}, evidence.Inferred, true},
		{[]evidence.Label{evidence.AWSDoc, evidence.ThirdPartyRecorded}, evidence.AWSDoc, true},
		{[]evidence.Label{evidence.SDKSource, evidence.AWSDoc}, evidence.SDKSource, true},
		{nil, "", false},
		{[]evidence.Label{evidence.AWSDoc, "hearsay"}, "", false},
	}
	for _, tt := range tests {
		got, ok := evidence.Weakest(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Weakest(%q) = %q, %t; want %q, %t", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
