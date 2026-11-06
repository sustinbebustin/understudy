package dynamodb_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/store"
)

// saveLoad saves, resets and loads the service as a snapshot load does
// (ARCHITECTURE.md 13), passing every saved section through edit first.
func (h *streamsHarness) saveLoad(edit func(*store.Section)) {
	h.Helper()
	items, err := h.svc.ExportLate(h.Context())
	if err != nil {
		h.Fatal(err)
	}
	cells, err := h.svc.Export()
	if err != nil {
		h.Fatal(err)
	}
	for _, ss := range [][]store.Section{items, cells} {
		for i := range ss {
			edit(&ss[i])
		}
	}
	if err := h.svc.ResetLate(h.Context(), kernel.ResetScope{}); err != nil {
		h.Fatal(err)
	}
	h.svc.Reset(kernel.ResetScope{})
	if err := h.svc.ImportLate(h.Context(), items); err != nil {
		h.Fatal(err)
	}
	if err := h.svc.Import(cells); err != nil {
		h.Fatal(err)
	}
	h.svc.Rearm(h.Clock.Now())
	h.settle()
}

func (h *streamsHarness) listStreams() []string {
	h.Helper()
	var out struct{ Streams []struct{ StreamArn string } }
	h.ok200(h.streamsCall("ListStreams", `{}`, defaultCaller()), &out)
	arns := make([]string, 0, len(out.Streams))
	for _, s := range out.Streams {
		arns = append(arns, s.StreamArn)
	}
	return arns
}

// A version 3 section, written before streams were saved, still loads: a
// stream-enabled table gets a fresh stream labeled at the import instant,
// empty, fed from the tip of its new Local stream, and the old label is
// gone (ADR 0009 amendment of 2026-10-27).
func TestSnapshotMigratesAVersion3Section(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		old, _ := latest(h.createStreamTable("Orders", "NEW_IMAGE"))
		h.put("Orders", "a")
		h.advance(time.Minute)
		loaded := h.Clock.Now()
		h.saveLoad(func(sec *store.Section) {
			sec.Version = 3
			var fields map[string]json.RawMessage
			if json.Unmarshal(sec.Data, &fields) != nil {
				return // an item section: written alike in both versions
			}
			delete(fields, "streams")
			delete(fields, "iteratorKey")
			data, err := json.Marshal(fields)
			if err != nil {
				h.Fatal(err)
			}
			sec.Data = data
		})
		arn, label := latest(h.mustCall("DescribeTable", `{"TableName":"Orders"}`))
		if want := labelOf(loaded); label != want || arn != streamARNOf(defaultCaller(), "Orders", want) {
			h.Errorf("after loading version 3 LatestStream = %s, %s; want a stream labeled %s", arn, label, want)
		}
		if got := h.listStreams(); !slices.Equal(got, []string{arn}) || arn == old {
			h.Errorf("ListStreams = %v, want only the fresh %s", got, arn)
		}
		if got := h.allRecords(arn); len(got) != 0 {
			h.Errorf("the fresh stream holds %d records, want none: the load's own writes emit nothing", len(got))
		}
		h.put("Orders", "b")
		got := h.allRecords(arn)
		if len(got) != 1 || got[0].EventName != "INSERT" || got[0].Dynamodb.Keys["id"] == nil {
			h.Fatalf("records after a write = %+v, want the one INSERT", got)
		}
		if id := string(got[0].Dynamodb.Keys["id"]); id != `{"S":"b"}` {
			h.Errorf("the record's key = %s, want b", id)
		}
	})
}

// A disabled stream and a deleted table's stream survive a load as they
// were: listed, DISABLED, with their records and closed shards, and the
// deleted table's stream still goes 24 hours after its delete
// (dynamodb-streams.md 5.3, 5.4; ADR 0009 amendment of 2026-10-27).
func TestSnapshotKeepsDisabledAndDeletedStreams(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		orders, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.put("Orders", "a")
		if r := h.setStream(""); r.Status != http.StatusOK {
			h.Fatalf("disable = %d %s", r.Status, r.Body)
		}
		other, _ := latest(h.createStreamTable("Other", "KEYS_ONLY"))
		h.put("Other", "x")
		h.mustCall("DeleteTable", `{"TableName":"Other"}`)
		deleted := h.Clock.Now()
		listed := h.listStreams()
		before := map[string]streamDescription{orders: h.describe(orders), other: h.describe(other)}
		h.saveLoad(func(*store.Section) {})
		if got := h.listStreams(); !slices.Equal(got, listed) {
			h.Errorf("ListStreams after the load = %v, want %v", got, listed)
		}
		for arn, want := range before {
			got := h.describe(arn)
			end := got.Shards[0].SequenceNumberRange.EndingSequenceNumber
			if got.StreamStatus != "DISABLED" || end == nil || *end != *want.Shards[0].SequenceNumberRange.EndingSequenceNumber {
				h.Errorf("%s after the load: %+v, want DISABLED and closed as before", arn, got)
			}
			if n := len(h.allRecords(arn)); n != 1 {
				h.Errorf("%s holds %d records after the load, want 1", arn, n)
			}
		}
		h.advance(deleted.Add(24 * time.Hour).Sub(h.Clock.Now()))
		if got := h.listStreams(); !slices.Equal(got, []string{orders}) {
			h.Errorf("ListStreams 24 hours after the delete = %v, want only %s", got, orders)
		}
	})
}

// A reset drops the streams with their tables (ARCHITECTURE.md 13): none is
// listed, and the table created again starts an empty stream.
func TestResetDropsStreams(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		h.createStreamTable("Orders", "KEYS_ONLY")
		h.put("Orders", "a")
		if err := h.svc.ResetLate(h.Context(), kernel.ResetScope{}); err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if got := h.listStreams(); len(got) != 0 {
			h.Errorf("ListStreams after a reset = %v, want none", got)
		}
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		if got := h.allRecords(arn); len(got) != 0 {
			h.Errorf("the re-created table's stream holds %d records, want none", len(got))
		}
	})
}
