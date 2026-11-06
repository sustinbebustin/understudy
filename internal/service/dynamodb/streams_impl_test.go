package dynamodb_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
)

// listedStreams is the ARNs one ListStreams of the default caller answers
// for body.
func (h *streamsHarness) listedStreams(body string) []string {
	h.Helper()
	var out struct{ Streams []struct{ StreamArn string } }
	h.ok200(h.streamsCall("ListStreams", body, defaultCaller()), &out)
	arns := make([]string, len(out.Streams))
	for i, s := range out.Streams {
		arns[i] = s.StreamArn
	}
	return arns
}

// A table created again under a deleted table's name starts without a
// stream: its description names none, while the deleted table's stream
// stays listed, disabled, until it is deleted 24 hours after its table
// (dynamodb-streams.md 5.4).
func TestRecreatedTableDoesNotInheritTheStream(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		old, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.mustCall("DeleteTable", `{"TableName":"Orders"}`)
		h.mustCall("CreateTable", `{"TableName":"Orders","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],`+
			`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`)
		if a, l := latest(h.mustCall("DescribeTable", `{"TableName":"Orders"}`)); a != "" || l != "" {
			h.Errorf("the re-created table's LatestStream = %q, %q; want neither", a, l)
		}
		if got := h.listedStreams(`{"TableName":"Orders"}`); !slices.Equal(got, []string{old}) {
			h.Errorf("ListStreams = %v, want the deleted table's stream %s", got, old)
		}
	})
}

// A stream of a re-created table enabled at the instant its namesake's
// was takes the next millisecond, so no two stream ARNs are equal
// (dynamodb-streams.md 11.1 item 6).
func TestRecreatedTableStreamTakesAFreeLabel(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		at := h.Clock.Now()
		old, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.mustCall("DeleteTable", `{"TableName":"Orders"}`)
		renewed, label := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		if want := labelOf(at.Add(time.Millisecond)); label != want || renewed == old {
			h.Errorf("re-created stream %s labeled %q, want a label of %q distinct from %s", renewed, label, want, old)
		}
		if got := h.listedStreams(`{}`); !slices.Equal(got, []string{renewed, old}) {
			h.Errorf("ListStreams = %v, want %v", got, []string{renewed, old})
		}
	})
}

// An ExclusiveStartStreamArn naming no listed stream starts from the first
// (contract 5.4).
func TestListStreamsFromAnUnknownStartArn(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		a, _ := latest(h.createStreamTable("Alpha", "KEYS_ONLY"))
		got := h.listedStreams(`{"ExclusiveStartStreamArn":"arn:aws:dynamodb:us-east-1:111111111111:table/Nope/stream/2000-01-01T00:00:00.000"}`)
		if !slices.Equal(got, []string{a}) {
			h.Errorf("ListStreams = %v, want %v", got, []string{a})
		}
	})
}
