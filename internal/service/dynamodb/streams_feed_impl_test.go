package dynamodb_test

import (
	"encoding/json"
	"math/big"
	"net/http"
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
// journals the stream back to Draining (dynamodb-streams.md 11.1 item 3).
// The emulator-clock retry that fails again re-arms itself.
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

// Disabling the stream, or deleting the table, while a failed drain is
// pending drains it first: the record stays readable on the closed shard
// with its write's instant, as AWS keeps a disabled or deleted table's
// records for 24 hours (dynamodb-streams.md 5.3, 5.4, 11.1 item 3: nothing
// is dropped).
func TestClosingAStreamDrainsAPendingFailedDrain(t *testing.T) {
	for _, closing := range []struct {
		name, op, body string
	}{
		{"disable", "UpdateTable", `{"TableName":"Orders","StreamSpecification":{"StreamEnabled":false}}`},
		{"delete table", "DeleteTable", `{"TableName":"Orders"}`},
	} {
		t.Run(closing.name, func(t *testing.T) {
			runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
				arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
				h.ls.setFail(true)
				at := h.Clock.Now()
				h.put("Orders", "a")
				h.ls.setFail(false)
				h.mustCall(closing.op, closing.body)
				if d := h.describe(arn); d.StreamStatus != "DISABLED" {
					h.Errorf("StreamStatus = %s, want DISABLED", d.StreamStatus)
				}
				got := h.allRecords(arn)
				if len(got) != 1 {
					h.Fatalf("%d records on the closed shard, want the failed drain's 1", len(got))
				}
				if want := big.NewRat(at.Wire().Unix(), 1); epochSeconds(got[0].Dynamodb.ApproximateCreationDateTime).Cmp(want) != 0 {
					h.Errorf("record at %s, want its write's %s", got[0].Dynamodb.ApproximateCreationDateTime, want.FloatString(0))
				}
			})
		})
	}
}

// Drains that keep failing journal one change to DrainFailed, not one per
// retry: a retry that fails again changes no state.
func TestRepeatedDrainFailuresJournalOneChange(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.ls.setFail(true)
		h.put("Orders", "a")
		h.advance(3 * time.Second)
		h.settle()
		h.put("Orders", "b")
		h.ls.setFail(false)
		h.put("Orders", "c")
		if got, want := h.stateChanges(arn), []string{"Draining->DrainFailed", "DrainFailed->Draining"}; !slices.Equal(got, want) {
			h.Errorf("journaled changes %v, want %v", got, want)
		}
	})
}

// A disable that cannot drain a pending failed drain is refused with a 500
// before it reaches Local, and the stream stays enabled with its record
// still to come, rather than closing a shard that lacks it.
func TestDisableRefusedWhileTheDrainStillFails(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.ls.setFail(true)
		h.put("Orders", "a")
		updates := h.localSaw("UpdateTable")
		if r := h.setStream(""); r.Status != http.StatusInternalServerError || !strings.Contains(r.Body, "table and stream are unchanged") {
			h.Errorf("disable = %d %s, want 500 saying the table and stream are unchanged", r.Status, r.Body)
		}
		if n := h.localSaw("UpdateTable"); n != updates {
			h.Errorf("Local saw %d UpdateTable calls, want %d: the refused disable must not reach it", n, updates)
		}
		h.ls.setFail(false)
		if d := h.describe(arn); d.StreamStatus != "ENABLED" {
			h.Errorf("StreamStatus = %s, want ENABLED", d.StreamStatus)
		}
		h.mustCall("UpdateTable", `{"TableName":"Orders","StreamSpecification":{"StreamEnabled":false}}`)
		if got := h.allRecords(arn); len(got) != 1 {
			h.Errorf("%d records after the retried disable, want 1", len(got))
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
