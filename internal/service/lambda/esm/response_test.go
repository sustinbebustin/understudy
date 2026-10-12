package esm_test

import (
	"encoding/json"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// The partial batch response table of lambda-events.md 2.3, recorded
// against AWS by LocalStack's test_report_batch_item_failures (snapshot
// last validated 2024) and stated in the services-sqs-errorhandling guide.
func TestReadResponse(t *testing.T) {
	batch := []string{"id1", "id2", "id3", "id4"}
	cases := []struct {
		name    string
		payload string
		want    esm.Verdict
	}{
		{"empty response", ``, esm.Succeeded{}},
		{"whitespace response", " \n", esm.Succeeded{}},
		{"null response", `null`, esm.Succeeded{}},
		{"empty object", `{}`, esm.Succeeded{}},
		{"null list", `{"batchItemFailures": null}`, esm.Succeeded{}},
		{"empty list", `{"batchItemFailures": []}`, esm.Succeeded{}},
		{"other keys only", `{"statusCode": 200}`, esm.Succeeded{}},
		{"listed ids in batch order", `{"batchItemFailures": [{"itemIdentifier": "id4"}, {"itemIdentifier": "id2"}]}`, esm.PartiallyFailed{IDs: []string{"id2", "id4"}}},
		{"an id listed twice", `{"batchItemFailures": [{"itemIdentifier": "id2"}, {"itemIdentifier": "id2"}]}`, esm.PartiallyFailed{IDs: []string{"id2"}}},
		{"every id listed", `{"batchItemFailures": [{"itemIdentifier": "id1"}, {"itemIdentifier": "id2"}, {"itemIdentifier": "id3"}, {"itemIdentifier": "id4"}]}`, esm.PartiallyFailed{IDs: batch}},
		{"invalid JSON", `{"batchItemFailures": [`, esm.Failed{Reason: "the response is not valid JSON"}},
		{"empty identifier", `{"batchItemFailures": [{"itemIdentifier": ""}]}`, esm.Failed{Reason: "batchItemFailures[0].itemIdentifier is empty"}},
		{"null identifier", `{"batchItemFailures": [{"itemIdentifier": null}]}`, esm.Failed{Reason: "batchItemFailures[0].itemIdentifier is not a string"}},
		{"misspelled key", `{"batchItemFailures": [{"itemIdentifer": "id2"}]}`, esm.Failed{Reason: "batchItemFailures[0] has no itemIdentifier"}},
		{"key in another case", `{"batchItemFailures": [{"ItemIdentifier": "id2"}]}`, esm.Failed{Reason: "batchItemFailures[0] has no itemIdentifier"}},
		{"unknown id", `{"batchItemFailures": [{"itemIdentifier": "id2"}, {"itemIdentifier": "id9"}]}`, esm.Failed{Reason: `batchItemFailures[1].itemIdentifier "id9" is not a message of the batch`}},
		{"null entry", `{"batchItemFailures": [null]}`, esm.Failed{Reason: "batchItemFailures[0] is not an object"}},
		{"list is an object", `{"batchItemFailures": {"itemIdentifier": "id2"}}`, esm.Failed{Reason: "batchItemFailures is not a list"}},
		{"response is a string", `"done"`, esm.Failed{Reason: "the response is not a JSON object"}},
		{"response is a list", `[]`, esm.Failed{Reason: "the response is not a JSON object"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := esm.ReadResponse([]byte(tc.payload), batch)
			if !verdictsEqual(got, tc.want) {
				t.Fatalf("ReadResponse(%s) = %#v, want %#v", tc.payload, got, tc.want)
			}
		})
	}
}

// A well-formed response that lists some of a batch's ids fails exactly
// those, in batch order; one more id from outside the batch fails all.
func TestReadResponseProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		batch := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z0-9-]{1,12}`), 1, 10, rapid.ID[string]).Draw(t, "batch")
		listed := rapid.SliceOf(rapid.SampledFrom(batch)).Draw(t, "listed")
		entries := make([]map[string]string, 0, len(listed)+1)
		for _, id := range listed {
			entries = append(entries, map[string]string{"itemIdentifier": id})
		}
		payload := mustJSON(t, map[string]any{"batchItemFailures": entries})
		got := esm.ReadResponse(payload, batch)
		var want esm.Verdict = esm.Succeeded{}
		if failed := slices.DeleteFunc(slices.Clone(batch), func(id string) bool { return !slices.Contains(listed, id) }); len(failed) > 0 {
			want = esm.PartiallyFailed{IDs: failed}
		}
		if !verdictsEqual(got, want) {
			t.Fatalf("ReadResponse(%s) = %#v, want %#v", payload, got, want)
		}
		stranger := rapid.StringMatching(`[A-Z]{1,5}`).Draw(t, "stranger")
		at := rapid.IntRange(0, len(entries)).Draw(t, "at")
		withStranger := slices.Insert(slices.Clone(entries), at, map[string]string{"itemIdentifier": stranger})
		if _, ok := esm.ReadResponse(mustJSON(t, map[string]any{"batchItemFailures": withStranger}), batch).(esm.Failed); !ok {
			t.Fatalf("an id outside the batch at %d did not fail the batch", at)
		}
	})
}

func mustJSON(t *rapid.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func verdictsEqual(a, b esm.Verdict) bool {
	switch a := a.(type) {
	case esm.Succeeded:
		_, ok := b.(esm.Succeeded)
		return ok
	case esm.Failed:
		f, ok := b.(esm.Failed)
		return ok && f == a
	case esm.PartiallyFailed:
		p, ok := b.(esm.PartiallyFailed)
		return ok && slices.Equal(p.IDs, a.IDs)
	}
	return false
}
