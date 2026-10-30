package kinesis_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/kinesis"
)

// saveResetLoad exports the service, resets everything, clears the timers
// as an admin reset does (timers are derived from state, ARCHITECTURE.md
// 13), imports the sections and rearms, as a snapshot load does.
func (h *harness) saveResetLoad() {
	h.Helper()
	sections, err := h.svc.Export()
	if err != nil {
		h.Fatal(err)
	}
	h.svc.Reset(kernel.ResetScope{})
	h.Kernel.Timers().Scheduler().Clear()
	if err := h.svc.Import(sections); err != nil {
		h.Fatal(err)
	}
	h.svc.Rearm(h.Clock.Now())
}

// A reset drops only the scopes it covers: a region reset leaves the other
// region's streams and their records (kernel.ResetScope; contract 3.9).
func TestResetDropsOnlyTheScopesItCovers(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		home := h.Scope
		h.create("s", 1)
		h.put("s", "k", "kept")
		var euWest aws.Region
		if err := euWest.UnmarshalText([]byte("eu-west-1")); err != nil {
			h.Fatal(err)
		}
		h.Scope = aws.Scope{Partition: home.Partition, Account: home.Account, Region: euWest}
		h.create("e", 1)
		dropped := h.Scope
		h.Scope = home

		h.svc.Reset(kernel.ResetScope{Regions: []aws.Region{euWest}})
		if got := h.svc.Resources(dropped); len(got) != 0 {
			h.Errorf("Resources(%v) after its reset = %+v", dropped, got)
		}
		if got := h.svc.Scopes(); !slices.Equal(got, []aws.Scope{home}) {
			h.Errorf("Scopes after resetting %v = %v, want [%v]", euWest, got, home)
		}
		rs := h.readAll(h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, ""))
		if len(rs) != 1 || string(rs[0].Data) != "kept" {
			h.Errorf("the kept region's stream reads %d records", len(rs))
		}
	})
}

// A ListShards NextToken issued before a save resumes its listing after the
// load: the token key is in the snapshot (contract 3.9).
func TestListShardsTokenSurvivesALoad(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 2)
		page := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), MaxResults: new(int32(1))})
		if page.NextToken == nil {
			h.Fatal("MaxResults 1 of 2 shards gives no NextToken")
		}
		h.saveResetLoad()
		rest := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{NextToken: page.NextToken})
		if got := shardIDs(rest.Shards); !slices.Equal(got, []string{shardID(1)}) {
			h.Errorf("a pre-save token resumes at %v, want [%s]", got, shardID(1))
		}
	})
}

// A loaded record keeps its arrival instant: the rearmed retention timer
// trims it 24 hours after its put, not after the load (contract 3.4, 3.9;
// kinesis.md 13.5 item 4).
func TestLoadedRecordsExpireAtTheirRetention(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		h.put("s", "k", "old")
		h.advance(time.Hour)
		h.saveResetLoad()
		if n := h.heldBlobs(); n != 1 {
			h.Fatalf("after load a snapshot holds %d records' data, want 1", n)
		}
		h.advance(retention24 - time.Hour)
		if n := h.heldBlobs(); n != 0 {
			h.Errorf("at the record's expiry a snapshot holds %d records' data, want none", n)
		}
	})
}

// A loaded shard numbers new records after its restored ones, and an
// iterator issued before the save still expires 5 minutes after it was
// issued (contract 3.9: "until they expire"; kinesis.md 13.5 item 3).
func TestLoadContinuesSequenceAndIteratorExpiry(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		first := h.put("s", "k", "a")
		it := h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")
		h.saveResetLoad()
		h.advance(time.Millisecond)
		second := h.put("s", "k", "b")
		rs := h.readAll(h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, ""))
		if len(rs) != 2 || rs[0].SequenceNumber != first.SequenceNumber || rs[1].SequenceNumber != second.SequenceNumber {
			h.Fatalf("after load the shard reads %d records", len(rs))
		}
		if first.SequenceNumber == second.SequenceNumber {
			h.Errorf("a put after load reused sequence number %s", first.SequenceNumber)
		}
		h.advance(5*time.Minute - time.Millisecond)
		if err := fail(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: it}); err.ID.JSONType != "ExpiredIteratorException" {
			h.Errorf("a pre-save iterator 5 minutes after issue = %s, want ExpiredIteratorException", err.ID.JSONType)
		}
	})
}
