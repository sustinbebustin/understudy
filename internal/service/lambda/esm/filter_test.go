package esm_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// The body and pattern format matrix of lambda-events.md 2.4 (the
// with-sqs-filtering guide; the plain-body, JSON-pattern drop is recorded
// against AWS by LocalStack's test_sqs_event_filter), plus metadata
// filtering and the OR across filters.
func TestFiltersMatch(t *testing.T) {
	queue := queueARN(t, "arn:aws:sqs:us-east-1:000000000000:q")
	cases := []struct {
		name     string
		patterns []string
		body     string
		want     bool
	}{
		{"no filters", nil, "anything", true},
		{"plain body, plain pattern", []string{`{"body":["plain-string"]}`}, "plain-string", true},
		{"plain body, other plain pattern", []string{`{"body":["other"]}`}, "plain-string", false},
		{"plain body, JSON pattern", []string{`{"body":{"kind":["order"]}}`}, "plain-string", false},
		{"JSON body, JSON pattern", []string{`{"body":{"kind":["order"]}}`}, `{"kind":"order"}`, true},
		{"JSON body, JSON pattern on another value", []string{`{"body":{"kind":["order"]}}`}, `{"kind":"refund"}`, false},
		{"JSON body, plain pattern", []string{`{"body":["{\"kind\":\"order\"}"]}`}, `{"kind":"order"}`, false},
		{"plain body, metadata pattern", []string{`{"eventSource":["aws:sqs"]}`}, "plain-string", true},
		{"JSON body, metadata pattern", []string{`{"awsRegion":["us-west-2"]}`}, `{"kind":"order"}`, false},
		{"numeric range", []string{`{"body":{"n":[{"numeric":[">",10,"<=",20]}]}}`}, `{"n":15}`, true},
		{"prefix", []string{`{"body":{"id":[{"prefix":"ord-"}]}}`}, `{"id":"ord-1"}`, true},
		{"filters are ORed", []string{`{"body":{"kind":["order"]}}`, `{"body":{"kind":["refund"]}}`}, `{"kind":"refund"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := esm.CompileFilters(tc.patterns)
			if err != nil {
				t.Fatal(err)
			}
			r := esm.NewRecord(&sqsapi.Message{MessageId: new("m"), Body: new(tc.body)}, queue)
			got, matchErr := f.Match(&r)
			if matchErr != nil {
				t.Fatal(matchErr)
			}
			if got != tc.want {
				t.Fatalf("Match(%q) under %v = %v, want %v", tc.body, tc.patterns, got, tc.want)
			}
		})
	}
}

// A pattern Lambda rejects does not compile; the control plane refuses it
// with the same profile before it is stored.
func TestCompileFiltersRejects(t *testing.T) {
	if _, err := esm.CompileFilters([]string{`{"body":"plain-string"}`}); err == nil {
		t.Fatal("a non-array leaf compiled, want Lambda's invalid filter pattern")
	}
}

// TestSwitches lists the filter engine's unverified choices as inferred
// fidelity switches with their record-backlog items, so the coverage
// report shows them (matching-engines.md 10).
func TestSwitches(t *testing.T) {
	type setting struct{ value, record string }
	got := map[string]setting{}
	for _, e := range esm.Switches() {
		if e.Label != evidence.Inferred {
			t.Errorf("switch %s is labelled %s, want inferred", e.Name, e.Label)
		}
		got[e.Name] = setting{e.Value, e.Record}
	}
	for name, want := range map[string]setting{
		"lambda.esm-filter.exists-true-empty-value": {"any-value", "matching-engines.md H2"},
		"lambda.esm-filter.empty-input":             {"matchable", ""},
		"lambda.esm-filter.bare-number":             {"numeric-or-text", "matching-engines.md H7"},
		"lambda.esm-filter.malformed-or":            {"rejected", "matching-engines.md H10"},
		"lambda.esm-filter.operator-allow-list":     {"documented-table", "matching-engines.md H13"},
	} {
		if have, ok := got[name]; !ok || have != want {
			t.Errorf("switch %s = %+v (listed %v), want %+v", name, have, ok, want)
		}
	}
}
