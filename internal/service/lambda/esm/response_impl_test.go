package esm_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// A stream batch's response is read as a queue batch's except that an
// empty JSON list is full success, as recorded for DynamoDB and Kinesis
// (third-party-recorded LS-D:L3735-L3808, LS-K:L1892-L1946;
// lambda-stream-sources.md 5.5); a list with members stays complete
// failure (inferred, ESMS-08).
func TestReadStreamResponse(t *testing.T) {
	t.Parallel()
	seqs := []string{"100", "200"}
	cases := []struct {
		name, payload string
		want          esm.Verdict
	}{
		{"empty list", `[]`, esm.Succeeded{}},
		{"empty list with spaces", " [ ] ", esm.Succeeded{}},
		{"null", `null`, esm.Succeeded{}},
		{"list with a member", `[{"itemIdentifier":"200"}]`, esm.Failed{Reason: "the response is not a JSON object"}},
		{"reported record", `{"batchItemFailures":[{"itemIdentifier":"200"}]}`, esm.PartiallyFailed{IDs: []string{"200"}}},
		{"empty identifier", `{"batchItemFailures":[{"itemIdentifier":""}]}`, esm.Failed{Reason: "batchItemFailures[0].itemIdentifier is empty"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := esm.ReadStreamResponse([]byte(tc.payload), seqs); !verdictsEqual(got, tc.want) {
				t.Errorf("ReadStreamResponse(%s) = %#v, want %#v", tc.payload, got, tc.want)
			}
		})
	}
}
