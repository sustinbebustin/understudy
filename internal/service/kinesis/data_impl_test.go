package kinesis_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/kinesis"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// heldBlobs counts the record data references a snapshot of the service
// would archive.
func (h *harness) heldBlobs() int {
	h.Helper()
	sections, err := h.svc.Export()
	if err != nil {
		h.Fatal(err)
	}
	n := 0
	for _, s := range sections {
		n += len(s.Blobs)
	}
	return n
}

// The retention timer trims a stream nobody reads at the expiry instant,
// so a snapshot taken after it holds no expired record (the aws-doc
// kinesis.retention.trim of kinesis.md 13.2).
func TestRetentionTimerTrimsWithoutARead(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		h.put("s", "k", "old")
		h.advance(time.Hour)
		h.put("s", "k", "new")
		if n := h.heldBlobs(); n != 2 {
			h.Fatalf("a snapshot holds %d records' data, want 2", n)
		}
		h.advance(retention24 - time.Hour)
		if n := h.heldBlobs(); n != 1 {
			h.Errorf("at the first record's expiry a snapshot holds %d records' data, want 1", n)
		}
		h.advance(time.Hour)
		if n := h.heldBlobs(); n != 0 {
			h.Errorf("at the second record's expiry a snapshot holds %d records' data, want none", n)
		}
	})
}

// DeleteStream drops the stream's records with it: a snapshot holds none
// of their data.
func TestDeleteStreamDropsItsRecords(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 2)
		h.create("t", 1)
		for _, k := range []string{"a", "b", "c"} {
			h.put("s", k, "x-"+k)
		}
		h.put("t", "k", "y")
		call(h, kinesisapi.OpDeleteStream, &kinesisapi.DeleteStreamInput{StreamName: new("s")})
		if n := h.heldBlobs(); n != 1 {
			h.Errorf("after DeleteStream a snapshot holds %d records' data, want only t's 1", n)
		}
	})
}

// GetRecords pins a page's data under the cell lock and reads it after
// (ADR 0005: no I/O under a lock); the pins are released once the data is
// read, so a stream read and then deleted leaves nothing in the blob store.
func TestGetRecordsReleasesItsPins(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory(k.TempDir(), 0)
		if err != nil {
			k.Fatal(err)
		}
		d := k.Deps(kinesisapi.Service())
		d.Blob = blobs
		h := &harness{K: k, svc: kinesis.New(d, kinesis.Options{})}
		h.create("s", 1)
		h.put("s", "k", "a")
		h.put("s", "k", "b")
		it := call(h, kinesisapi.OpGetShardIterator, &kinesisapi.GetShardIteratorInput{StreamName: new("s"), ShardId: "shardId-000000000000", ShardIteratorType: kinesisapi.ShardIteratorTypeTrimHorizon})
		if it.ShardIterator == nil {
			h.Fatal("GetShardIterator returned no iterator")
		}
		out := call(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: *it.ShardIterator})
		if len(out.Records) != 2 || string(out.Records[0].Data) != "a" || string(out.Records[1].Data) != "b" {
			h.Fatalf("GetRecords = %+v, want a then b", out.Records)
		}
		call(h, kinesisapi.OpDeleteStream, &kinesisapi.DeleteStreamInput{StreamName: new("s")})
		if refs := blobs.Refs(); len(refs) != 0 {
			h.Errorf("after GetRecords and DeleteStream the blob store holds %v, want nothing", refs)
		}
	}, kerneltest.Services(kinesisapi.Service()))
}

// A put wakes whoever waits on the stream's shardlog.WaitKey, the key the
// Lambda poller waits on (ARCHITECTURE.md 14.8: readers are woken
// by writes, not a timer), and a put to another stream does not.
func TestPutWakesTheStreamsWaitKey(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		h.create("t", 1)
		arn, err := aws.ParseARN(arnOf("s"))
		if err != nil {
			h.Fatal(err)
		}
		woken := h.Kernel.Waiters().Wait(h.Context(), shardlog.WaitKey(arn))
		h.put("t", "k", "x")
		select {
		case <-woken:
			h.Fatal("a put to t woke s's readers")
		default:
		}
		call(h, kinesisapi.OpPutRecords, &kinesisapi.PutRecordsInput{StreamName: new("s"), Records: []kinesisapi.PutRecordsRequestEntry{{PartitionKey: new("k"), Data: []byte("y")}}})
		select {
		case <-woken:
		default:
			h.Error("PutRecords to s did not wake its readers")
		}
	})
}

// A GetRecords page stops before the record that would take it past
// 10 MiB of data plus partition keys ("The maximum size of data that
// GetRecords can return is 10 MiB", aws-doc kinesis.md 5.5), and the next
// page resumes at that record.
func TestGetRecordsPageStopsAtTenMiB(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		const mib = 1 << 20
		for range 11 {
			// 1 MiB with the one-byte key.
			call(h, kinesisapi.OpPutRecord, &kinesisapi.PutRecordInput{StreamName: new("s"), PartitionKey: new("k"), Data: make([]byte, mib-1)})
		}
		it := h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")
		first := h.records(it, 0)
		if len(first.Records) != 10 {
			h.Fatalf("first page holds %d records, want the 10 that fit in 10 MiB", len(first.Records))
		}
		if rest := h.records(*first.NextShardIterator, 0); len(rest.Records) != 1 {
			h.Errorf("next page holds %d records, want the eleventh", len(rest.Records))
		}
	})
}
