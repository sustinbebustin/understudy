package dynamodb_test

import (
	"encoding/json"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
)

// drainedTables are the tables whose Local stream the feed asked for an
// iterator, in request order.
func (h *streamsHarness) drainedTables() []string {
	h.Helper()
	var out []string
	for _, f := range h.local.requests() {
		if f.Op != streamsTarget+"GetShardIterator" {
			continue
		}
		var in struct{ StreamArn string }
		if err := json.Unmarshal([]byte(f.Body), &in); err != nil {
			h.Fatal(err)
		}
		_, resource, _ := strings.Cut(in.StreamArn, ":table/")
		table, _, _ := strings.Cut(resource, "/")
		out = append(out, table)
	}
	return out
}

// stateChanges are the journaled stream state changes of arn, as
// "From->To".
func (h *streamsHarness) stateChanges(arn string) []string {
	var out []string
	events := h.Journal.Query(journal.Filter{Kind: journal.KindStateChange}).Events
	for i := range events {
		if sc, ok := events[i].Detail.(journal.StateChange); ok && sc.Resource.String() == arn {
			out = append(out, string(sc.From)+"->"+string(sc.To))
		}
	}
	return out
}

// While drains keep failing, every write's records stay in Local; the
// drain that finally succeeds stamps them with the first failed write's
// instant, since Local's records do not say which write made them, and
// journals the stream back to Draining (contract 5.3; dynamodb-streams.md
// 11.1 item 3). The emulator-clock retry that fails again re-arms itself.
func TestConsecutiveFailedDrainsKeepTheFirstInstant(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.ls.setFail(true)
		t1 := h.Clock.Now()
		h.put("Orders", "a")
		h.advance(2 * time.Second)
		h.settle()
		h.put("Orders", "b")
		h.ls.setFail(false)
		h.advance(5 * time.Second)
		h.settle()
		t3 := h.Clock.Now()
		h.put("Orders", "c")
		got := h.allRecords(arn)
		if len(got) != 3 {
			h.Fatalf("%d records, want 3", len(got))
		}
		for i, at := range []clock.Instant{t1, t1, t3} {
			if want := big.NewRat(at.Wire().Unix(), 1); epochSeconds(got[i].Dynamodb.ApproximateCreationDateTime).Cmp(want) != 0 {
				h.Errorf("record %d at %s, want %s", i, got[i].Dynamodb.ApproximateCreationDateTime, want.FloatString(0))
			}
		}
		changes := h.stateChanges(arn)
		if len(changes) == 0 || changes[len(changes)-1] != "DrainFailed->Draining" || !slices.Contains(changes, "Draining->DrainFailed") {
			h.Errorf("journaled changes %v, want failures then DrainFailed->Draining last", changes)
		}
	})
}

// A BatchWriteItem drains only the tables of the call that have an enabled
// stream (dynamodb-streams.md 11.1 item 2).
func TestBatchWriteDrainsOnlyStreamedTables(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		a, _ := latest(h.createStreamTable("Alpha", "KEYS_ONLY"))
		h.mustCall("CreateTable", `{"TableName":"Plain","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],`+
			`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`)
		h.mustCall("BatchWriteItem", `{"RequestItems":{"Alpha":[{"PutRequest":{"Item":{"id":{"S":"1"}}}}],"Plain":[{"PutRequest":{"Item":{"id":{"S":"2"}}}}]}}`)
		if got := h.drainedTables(); !slices.Equal(got, []string{"Alpha"}) {
			h.Errorf("drained %v, want only Alpha", got)
		}
		if got := h.allRecords(a); len(got) != 1 {
			h.Errorf("Alpha's stream has %d records, want 1", len(got))
		}
	})
}

// A PartiQL call drains every stream-enabled table of the scope, including
// those its statement does not name: the statement is never parsed
// (dynamodb-streams.md 11.1 item 2).
func TestPartiQLDrainsEveryStreamedTable(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		h.createStreamTable("Alpha", "KEYS_ONLY")
		h.createStreamTable("Bravo", "KEYS_ONLY")
		h.ls.statement = func(string) (string, item) { return "Bravo", item{"id": json.RawMessage(`{"S":"5"}`)} }
		h.mustCall("ExecuteStatement", `{"Statement":"INSERT INTO \"Bravo\" VALUE {'id': '5'}"}`)
		if got := h.drainedTables(); !slices.Equal(got, []string{"Alpha", "Bravo"}) {
			h.Errorf("drained %v, want Alpha and Bravo in name order", got)
		}
	})
}
