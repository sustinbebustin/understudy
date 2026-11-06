package kinesis_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/kinesis"
	"github.com/sustinbebustin/understudy/internal/shardlog"
	"github.com/sustinbebustin/understudy/internal/store"
)

// iteratorTTL: "A shard iterator expires 5 minutes after it is returned to
// the requester" (aws-doc, kinesis.md 3.5).
const iteratorTTL = 5 * time.Minute

// retention24 is the default retention period, 24 hours (aws-doc,
// kinesis.md 4.7).
const retention24 = 24 * time.Hour

// shardOf is the shard a partition key lands in under the harness's two
// hash switches, read from kinesis.Split and kinesis.PartitionHash, never
// written out (kinesis.hash.even-split, kinesis.hash.md5-byte-order).
func (h *harness) shardOf(n int, key string) string {
	return shardID(kinesis.ShardFor(kinesis.Split(n, h.opts.EvenSplit), kinesis.PartitionHash(key, h.opts.HashByteOrder)))
}

// ---- PutRecord and PutRecords (kinesis.md 5.1-5.3) ----.

// A record goes to the shard whose range holds MD5 of its key, or its
// ExplicitHashKey, which "overrides the partition key to shard mapping"
// (aws-doc, kinesis.md 5.3); the output is ShardId and SequenceNumber
// without EncryptionType (PutRecord example, aws-doc 5.1). Flipping the
// byte-order switch moves keys (kinesis.md 14.13).
func TestPutRecordPlacement(t *testing.T) {
	for _, o := range []kinesis.Options{{}, {HashByteOrder: kinesis.HashLittleEndianUTF8, EvenSplit: kinesis.SplitFloorI}} {
		run(t, o, func(h *harness) {
			h.create("s", 4)
			for _, key := range []string{"a", "b", "c", "d", "1", "ééé", "order-17"} {
				out := h.put("s", key, "x")
				if out.ShardId != h.shardOf(4, key) || out.EncryptionType != nil {
					h.Errorf("%v: key %q -> %s (EncryptionType %v), want %s", o, key, out.ShardId, deref(out.EncryptionType), h.shardOf(4, key))
				}
			}
			ranges := kinesis.Split(4, o.EvenSplit)
			for i, r := range ranges {
				for _, ehk := range []string{r.Start.String(), r.End.String()} {
					out := call(h, kinesisapi.OpPutRecord, &kinesisapi.PutRecordInput{StreamName: new("s"), PartitionKey: new("a"), ExplicitHashKey: new(ehk), Data: []byte("y")})
					if out.ShardId != shardID(i) {
						h.Errorf("ExplicitHashKey %s -> %s, want %s", ehk, out.ShardId, shardID(i))
					}
				}
			}
		})
	}
}

// PutRecord's checks in the contract's order (contract 3.5; kinesis.md
// 13.3): DryRun answers 501 (13.9); an ExplicitHashKey above 2^128-1 is
// kinesalite's text (k, KIN-07); a missing PartitionKey is understudy's
// (u, KIN-07); data plus UTF-8 key above MaxRecordSizeInKiB*1024 is
// refused (u, KIN-14) and exactly at it accepted. Empty data is legal
// (third-party-recorded, golden 3: localstack@8b9a79f
// tests/aws/services/kinesis/test_kinesis.snapshot.json:L58-L88, validated
// 2022-08-25).
func TestPutRecordChecks(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		put := func(in kinesisapi.PutRecordInput) *kinesisapi.PutRecordInput {
			in.StreamName = new("s")
			return &in
		}
		wantNotImplemented(h, "DryRun", fail(h, kinesisapi.OpPutRecord, put(kinesisapi.PutRecordInput{PartitionKey: new("k"), Data: []byte("x"), DryRun: new(true)})), "DryRun", "PutRecord")
		over := "340282366920938463463374607431768211456"
		invalidArg(h, "ExplicitHashKey 2^128", fail(h, kinesisapi.OpPutRecord, put(kinesisapi.PutRecordInput{PartitionKey: new("k"), ExplicitHashKey: new(over), Data: []byte("x")})),
			"Invalid ExplicitHashKey. ExplicitHashKey must be in the range: [0, 2^128-1]. Specified value was "+over)
		invalidArg(h, "no PartitionKey", fail(h, kinesisapi.OpPutRecord, put(kinesisapi.PutRecordInput{Data: []byte("x")})),
			"PartitionKey is required for a stream whose RecordDistributionStrategy is USER_PARTITION_KEY.")
		const maxBytes = 1024 * 1024 // MaxRecordSizeInKiB default 1024 (aws-doc, kinesis.md 4.1)
		key := "é"                   // two UTF-8 bytes: the key counts in UTF-8 (contract 3.5)
		call(h, kinesisapi.OpPutRecord, put(kinesisapi.PutRecordInput{PartitionKey: new(key), Data: make([]byte, maxBytes-2)}))
		invalidArg(h, "a byte over", fail(h, kinesisapi.OpPutRecord, put(kinesisapi.PutRecordInput{PartitionKey: new(key), Data: make([]byte, maxBytes-1)})),
			fmt.Sprintf("Record size %d bytes (data plus partition key) exceeds the stream's maximum record size of %d bytes.", maxBytes+1, maxBytes))
		call(h, kinesisapi.OpPutRecord, put(kinesisapi.PutRecordInput{PartitionKey: new("k"), Data: []byte{}}))
		wantErr(h, "absent stream", fail(h, kinesisapi.OpPutRecord, &kinesisapi.PutRecordInput{StreamName: new("nope"), PartitionKey: new("k"), Data: []byte("x")}),
			"ResourceNotFoundException", 400, notFound("nope", account))
		// The stream's own maximum applies (contract 3.5: enforced by the
		// record-size check).
		call(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "big", ShardCount: new(int32(1)), MaxRecordSizeInKiB: new(int32(2048))})
		call(h, kinesisapi.OpPutRecord, &kinesisapi.PutRecordInput{StreamName: new("big"), PartitionKey: new("k"), Data: make([]byte, 2*maxBytes-1)})
	})
}

// PutRecords answers each entry in request order with FailedRecordCount 0
// (aws-doc example, kinesis.md 5.2), appends in request order, fails the
// whole call on the first bad entry with "Record <i>: " and appends
// nothing (contract 3.5), and refuses a request over 10 MiB of data plus
// keys (aws-doc 5.2; the text is understudy's, KIN-14) before the entries.
func TestPutRecords(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 2)
		keys := []string{"a", "b", "c", "d", "e", "f"}
		entries := make([]kinesisapi.PutRecordsRequestEntry, 0, len(keys))
		for _, k := range keys {
			entries = append(entries, kinesisapi.PutRecordsRequestEntry{PartitionKey: new(k), Data: []byte("data-" + k)})
		}
		out := call(h, kinesisapi.OpPutRecords, &kinesisapi.PutRecordsInput{StreamName: new("s"), Records: entries})
		if deref(out.FailedRecordCount) != int32(0) || len(out.Records) != len(keys) || out.EncryptionType != nil {
			h.Fatalf("PutRecords = %+v", out)
		}
		perShard := map[string][]string{}
		for i, r := range out.Records {
			if deref(r.ShardId) != h.shardOf(2, keys[i]) || r.SequenceNumber == nil || r.ErrorCode != nil || r.ErrorMessage != nil {
				h.Errorf("entry %d = %+v, want shard %s", i, r, h.shardOf(2, keys[i]))
			}
			perShard[str(r.ShardId)] = append(perShard[str(r.ShardId)], "data-"+keys[i])
		}
		for shard, want := range perShard {
			var got []string
			for _, r := range h.readAll(h.iterator("s", shard, kinesisapi.ShardIteratorTypeTrimHorizon, "")) {
				got = append(got, string(r.Data))
			}
			if !slices.Equal(got, want) {
				h.Errorf("%s holds %v, want %v in request order", shard, got, want)
			}
		}

		bad := []kinesisapi.PutRecordsRequestEntry{
			{PartitionKey: new("ok"), Data: []byte("x")},
			{PartitionKey: new("k"), ExplicitHashKey: new("340282366920938463463374607431768211456"), Data: []byte("x")},
		}
		invalidArg(h, "entry 1", fail(h, kinesisapi.OpPutRecords, &kinesisapi.PutRecordsInput{StreamName: new("s"), Records: bad}),
			"Record 1: Invalid ExplicitHashKey. ExplicitHashKey must be in the range: [0, 2^128-1]. Specified value was 340282366920938463463374607431768211456")
		wantNotImplemented(h, "DryRun", fail(h, kinesisapi.OpPutRecords, &kinesisapi.PutRecordsInput{StreamName: new("s"), Records: entries[:1], DryRun: new(true)}), "DryRun", "PutRecords")

		big := make([]kinesisapi.PutRecordsRequestEntry, 11)
		for i := range big {
			big[i] = kinesisapi.PutRecordsRequestEntry{PartitionKey: new("k"), Data: make([]byte, 1024*1024-1)}
		}
		big[10].ExplicitHashKey = new("340282366920938463463374607431768211456") // the size check comes first
		invalidArg(h, "11 MiB request", fail(h, kinesisapi.OpPutRecords, &kinesisapi.PutRecordsInput{StreamName: new("s"), Records: big}),
			"PutRecords request size 11534336 bytes (data plus partition keys) exceeds the limit of 10485760 bytes.")

		total := 0
		for i := range 2 {
			total += len(h.readAll(h.iterator("s", shardID(i), kinesisapi.ShardIteratorTypeTrimHorizon, "")))
		}
		if total != len(keys) {
			h.Errorf("%d records after the refused calls, want the %d first appended", total, len(keys))
		}
	})
}

// ---- GetShardIterator (kinesis.md 3.5, 5.4) ----.

// An iterator is Kinesis's opaque form: padded standard base64 starting
// AAAAAAAAAAE (the 8-byte prefix 00..01; aws-doc examples begin
// "AAAAAAAAAAE", kinesis.md 3.5), at most 512 characters (model
// ShardIterator length), with no "|1|" (contract 2).
func TestShardIteratorForm(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		name := strings.Repeat("n", 128) // the longest StreamName (model, kinesis.md 3.1)
		h.create(name, 1)
		seq := h.put(name, "k", "x").SequenceNumber
		for _, it := range []string{
			h.iterator(name, shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, ""),
			h.iterator(name, shardID(0), kinesisapi.ShardIteratorTypeAtSequenceNumber, seq),
			h.iterator(name, shardID(0), kinesisapi.ShardIteratorTypeLatest, ""),
		} {
			if !strings.HasPrefix(it, "AAAAAAAAAAE") || len(it) > 512 || strings.Contains(it, "|1|") {
				h.Errorf("iterator %q (%d chars)", it, len(it))
			}
			if _, err := base64.StdEncoding.Strict().DecodeString(it); err != nil {
				h.Errorf("iterator %q is not padded standard base64: %v", it, err)
			}
		}
	})
}

// GetShardIterator's refusals in contract order (contract 3.5; texts
// kinesalite's (k) or understudy's (u), UNVERIFIED, KIN-05, KIN-12; the
// GetShardIterator note).
func TestGetShardIteratorRefusals(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 2)
		ranges := kinesis.Split(2, h.opts.EvenSplit)
		s1 := call(h, kinesisapi.OpPutRecord, &kinesisapi.PutRecordInput{StreamName: new("s"), PartitionKey: new("k"), ExplicitHashKey: new(ranges[1].Start.String()), Data: []byte("x")}).SequenceNumber
		get := func(in kinesisapi.GetShardIteratorInput) *kinesisapi.GetShardIteratorInput {
			in.StreamName = new("s")
			if in.ShardId == "" {
				in.ShardId = shardID(0)
			}
			return &in
		}
		gsi := func(in kinesisapi.GetShardIteratorInput) string {
			h.Helper()
			return fail(h, kinesisapi.OpGetShardIterator, get(in)).Message
		}
		wantNotImplemented(h, "DryRun", fail(h, kinesisapi.OpGetShardIterator, get(kinesisapi.GetShardIteratorInput{ShardIteratorType: kinesisapi.ShardIteratorTypeTrimHorizon, DryRun: new(true)})), "DryRun", "GetShardIterator")
		wantErr(h, "missing shard", fail(h, kinesisapi.OpGetShardIterator, get(kinesisapi.GetShardIteratorInput{ShardId: shardID(7), ShardIteratorType: kinesisapi.ShardIteratorTypeTrimHorizon})),
			"ResourceNotFoundException", 400, "Could not find shard "+shardID(7)+" in stream s under account "+account+".")
		combo := "Must either specify (1) AT_SEQUENCE_NUMBER or AFTER_SEQUENCE_NUMBER and StartingSequenceNumber or (2) TRIM_HORIZON or LATEST and no StartingSequenceNumber. Request specified "
		for _, typ := range []kinesisapi.ShardIteratorType{kinesisapi.ShardIteratorTypeAtSequenceNumber, kinesisapi.ShardIteratorTypeAfterSequenceNumber} {
			if got, want := gsi(kinesisapi.GetShardIteratorInput{ShardIteratorType: typ}), combo+string(typ)+" and no StartingSequenceNumber."; got != want {
				h.Errorf("%s without a number: %q, want %q", typ, got, want)
			}
		}
		now := h.Clock.Now().Wire()
		for _, typ := range []kinesisapi.ShardIteratorType{kinesisapi.ShardIteratorTypeTrimHorizon, kinesisapi.ShardIteratorTypeLatest, kinesisapi.ShardIteratorTypeAtTimestamp} {
			in := kinesisapi.GetShardIteratorInput{ShardIteratorType: typ, StartingSequenceNumber: new(s1)}
			if typ == kinesisapi.ShardIteratorTypeAtTimestamp {
				in.Timestamp = &now
			}
			if got, want := gsi(in), combo+string(typ)+" and also a StartingSequenceNumber."; got != want {
				h.Errorf("%s with a number: %q, want %q", typ, got, want)
			}
		}
		if got, want := gsi(kinesisapi.GetShardIteratorInput{ShardIteratorType: kinesisapi.ShardIteratorTypeAtTimestamp}),
			"Must specify timestampInMillis parameter for iterator of type AT_TIMESTAMP. Current request has no timestamp parameter."; got != want {
			h.Errorf("AT_TIMESTAMP without one: %q, want %q", got, want)
		}
		future := now.Add(time.Hour)
		if got, want := gsi(kinesisapi.GetShardIteratorInput{ShardIteratorType: kinesisapi.ShardIteratorTypeAtTimestamp, Timestamp: &future}),
			fmt.Sprintf("The timestampInMillis parameter cannot be greater than the currentTimestampInMillis. timestampInMillis: %d, currentTimestampInMillis: %d", future.UnixMilli(), now.UnixMilli()); got != want {
			h.Errorf("future AT_TIMESTAMP: %q, want %q", got, want)
		}
		invalidArg(h, "malformed number", fail(h, kinesisapi.OpGetShardIterator, get(kinesisapi.GetShardIteratorInput{ShardIteratorType: kinesisapi.ShardIteratorTypeAtSequenceNumber, StartingSequenceNumber: new("123")})),
			"StartingSequenceNumber 123 used in GetShardIterator on shard "+shardID(0)+" in stream s under account "+account+" is invalid.")
		invalidArg(h, "another shard's number", fail(h, kinesisapi.OpGetShardIterator, get(kinesisapi.GetShardIteratorInput{ShardIteratorType: kinesisapi.ShardIteratorTypeAtSequenceNumber, StartingSequenceNumber: new(s1)})),
			"Invalid StartingSequenceNumber. It encodes "+shardID(1)+", while it was used in a call to a shard with "+shardID(0))
	})
	// Under decimal-56 every number the model's SequenceNumber pattern admits
	// decodes, so a non-number is refused by the generated validator with
	// ValidationException before the handler (kinesis.md 3.4, 13.3; identity
	// UNVERIFIED, KIN-07).
	run(t, kinesis.Options{SequenceLayout: kinesis.LayoutDecimal56}, func(h *harness) {
		h.create("s", 1)
		wantErr(h, "not a number", fail(h, kinesisapi.OpGetShardIterator, &kinesisapi.GetShardIteratorInput{
			StreamName: new("s"), ShardId: shardID(0),
			ShardIteratorType: kinesisapi.ShardIteratorTypeAtSequenceNumber, StartingSequenceNumber: new("12x"),
		}), "ValidationException", 400,
			"1 validation error detected: Value '12x' at 'startingSequenceNumber' failed to satisfy constraint: Member must satisfy regular expression pattern: (0|([1-9]\\d{0,128}))")
	})
}

// ---- GetRecords (kinesis.md 5.5, 5.6, 5.10) ----.

// Records come back in put order with their data, key and arrival to the
// millisecond (aws-doc 5.10 "millisecond precision"; ApproximateArrival on
// the emulator clock, kinesis.md 13.5 item 5), no EncryptionType (golden
// 3, test_kinesis.snapshot.json:L58-L88); an empty shard gives Records []
// (third-party-recorded, golden 5: localstack@8b9a79f
// tests/aws/services/kinesis/test_kinesis.py:L552-L575, validated
// 2024-07-04); every open-shard page has a fresh NextShardIterator
// (aws-doc 5.5; distinct from earlier ones, R-weak 3.5); GetRecords never
// blocks (aws-doc 5.5). Golden 4 (test_kinesis.py:L506-L549, JSON half,
// validated 2024-07-04): put then read from TRIM_HORIZON returns it.
func TestGetRecordsReadsInOrder(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		it := h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")
		empty := h.records(it, 0)
		if empty.Records == nil || len(empty.Records) != 0 || empty.NextShardIterator == nil || deref(empty.MillisBehindLatest) != int64(0) || empty.ChildShards != nil {
			h.Errorf("empty shard page = %+v", empty)
		}
		type put struct {
			seq string
			at  time.Time
		}
		puts := make([]put, 0, 5)
		for i := range 5 {
			out := h.put("s", fmt.Sprintf("k%d", i), fmt.Sprintf("payload-%d", i))
			puts = append(puts, put{out.SequenceNumber, h.Clock.Now().Wire()})
			h.advance(1500 * time.Microsecond)
		}
		page := h.records(it, 0)
		if len(page.Records) != 5 {
			h.Fatalf("read %d records, want 5", len(page.Records))
		}
		for i, r := range page.Records {
			if r.SequenceNumber != puts[i].seq || string(r.Data) != fmt.Sprintf("payload-%d", i) || deref(r.PartitionKey) != fmt.Sprintf("k%d", i) || r.EncryptionType != nil {
				h.Errorf("record %d = %+v", i, r)
			}
			if r.ApproximateArrivalTimestamp == nil || r.ApproximateArrivalTimestamp.UnixMilli() != puts[i].at.UnixMilli() {
				h.Errorf("record %d arrival %v, want %v to the millisecond", i, r.ApproximateArrivalTimestamp, puts[i].at)
			}
		}
		if page.NextShardIterator == nil || *page.NextShardIterator == it || *page.NextShardIterator == *empty.NextShardIterator {
			h.Errorf("NextShardIterator %v is missing or not fresh", deref(page.NextShardIterator))
		}
		// Limit pages, then the tip: an empty page with a NextShardIterator.
		two := h.records(it, 2)
		if len(two.Records) != 2 || two.Records[1].SequenceNumber != puts[1].seq {
			h.Errorf("Limit 2 = %d records", len(two.Records))
		}
		next := h.records(*two.NextShardIterator, 0)
		if len(next.Records) != 3 || next.Records[0].SequenceNumber != puts[2].seq {
			h.Errorf("after Limit 2: %d records", len(next.Records))
		}
		tip := h.records(*next.NextShardIterator, 0)
		if len(tip.Records) != 0 || tip.NextShardIterator == nil {
			h.Errorf("tip page = %+v", tip)
		}
		// The iterator is reusable until it expires, "even after it is used"
		// (aws-doc, kinesis.md 3.5).
		if again := h.records(it, 0); len(again.Records) != 5 {
			h.Errorf("reusing the first iterator reads %d records", len(again.Records))
		}
	})
}

// The five iterator types position as documented (aws-doc kinesis.md 5.4):
// AT and AFTER a sequence number, LATEST "just after the most recent
// record" resolved at issue, AT_TIMESTAMP at the first record at or after
// it, and TRIM_HORIZON at the oldest untrimmed record, resolved at each
// read (contract 3.5).
func TestIteratorTypesPosition(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		seqs := make([]string, 0, 4)
		ats := make([]time.Time, 0, 4)
		for i := range 4 {
			ats = append(ats, h.Clock.Now().Wire())
			seqs = append(seqs, h.put("s", "k", strconv.Itoa(i)).SequenceNumber)
			h.advance(time.Second)
		}
		latest := h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeLatest, "")
		dataOf := func(it string) []string {
			h.Helper()
			rs := h.readAll(it)
			out := make([]string, 0, len(rs))
			for _, r := range rs {
				out = append(out, string(r.Data))
			}
			return out
		}
		if got := dataOf(h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeAtSequenceNumber, seqs[1])); !slices.Equal(got, []string{"1", "2", "3"}) {
			h.Errorf("AT seq 1 = %v", got)
		}
		if got := dataOf(h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeAfterSequenceNumber, seqs[1])); !slices.Equal(got, []string{"2", "3"}) {
			h.Errorf("AFTER seq 1 = %v", got)
		}
		at := ats[2]
		ts := call(h, kinesisapi.OpGetShardIterator, &kinesisapi.GetShardIteratorInput{StreamName: new("s"), ShardId: shardID(0), ShardIteratorType: kinesisapi.ShardIteratorTypeAtTimestamp, Timestamp: &at}).ShardIterator
		if got := dataOf(str(ts)); !slices.Equal(got, []string{"2", "3"}) {
			h.Errorf("AT_TIMESTAMP of record 2 = %v", got)
		}
		between := ats[2].Add(time.Millisecond)
		ts = call(h, kinesisapi.OpGetShardIterator, &kinesisapi.GetShardIteratorInput{StreamName: new("s"), ShardId: shardID(0), ShardIteratorType: kinesisapi.ShardIteratorTypeAtTimestamp, Timestamp: &between}).ShardIterator
		if got := dataOf(str(ts)); !slices.Equal(got, []string{"3"}) {
			h.Errorf("AT_TIMESTAMP between records 2 and 3 = %v (the next later record, aws-doc)", got)
		}
		old := ats[0].Add(-time.Hour)
		ts = call(h, kinesisapi.OpGetShardIterator, &kinesisapi.GetShardIteratorInput{StreamName: new("s"), ShardId: shardID(0), ShardIteratorType: kinesisapi.ShardIteratorTypeAtTimestamp, Timestamp: &old}).ShardIterator
		if got := dataOf(str(ts)); len(got) != 4 {
			h.Errorf("AT_TIMESTAMP before the trim horizon = %v, want TRIM_HORIZON's 4", got)
		}
		h.put("s", "k", "4")
		if got := dataOf(latest); !slices.Equal(got, []string{"4"}) {
			h.Errorf("LATEST issued before record 4 = %v", got)
		}
	})
}

// GetRecords' refusals in contract order (contract 3.5): DryRun 501; a
// Limit above 10000 is InvalidArgumentException ("If you specify a value
// that is greater than 10,000, GetRecords throws InvalidArgumentException",
// aws-doc 5.5; text u, KIN-07); an iterator that does not verify is
// kinesalite's "Invalid ShardIterator." (k); one pair of surrounding
// quotes is stripped (R-weak, kinesis.md 3.5); a StreamARN that is not the
// iterator's stream is refused; a deleted stream is not found; a
// re-created one refuses its predecessor's iterators (kinesis.md 13.5 item
// 3).
func TestGetRecordsRefusals(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		h.create("t", 1)
		h.put("s", "k", "x")
		it := h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")
		wantNotImplemented(h, "DryRun", fail(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: it, DryRun: new(true)}), "DryRun", "GetRecords")
		invalidArg(h, "Limit 10001", fail(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: it, Limit: new(int32(10001))}),
			"1 validation error detected: Value '10001' at 'limit' failed to satisfy constraint: Member must have value less than or equal to 10000")
		call(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: it, Limit: new(int32(10000))})
		flipped := []byte(it)
		flipped[len(flipped)/2] ^= 0x01
		for name, tok := range map[string]string{"flipped": string(flipped), "documented-shape junk": "AAAAAAAAAAEnotarealiterator"} {
			invalidArg(h, name, fail(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: tok}), "Invalid ShardIterator.")
		}
		if got := call(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: `"` + it + `"`}); len(got.Records) != 1 {
			h.Errorf("a quoted iterator reads %d records, want 1", len(got.Records))
		}
		invalidArg(h, "another stream's ARN", fail(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: it, StreamARN: new(arnOf("t"))}), "Invalid ShardIterator.")
		call(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: it, StreamARN: new(arnOf("s"))})

		call(h, kinesisapi.OpDeleteStream, &kinesisapi.DeleteStreamInput{StreamName: new("s")})
		wantErr(h, "deleted stream", fail(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: it}), "ResourceNotFoundException", 400, notFound("s", account))
		h.advance(time.Second)
		h.create("s", 1)
		invalidArg(h, "re-created stream", fail(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: it}), "Invalid ShardIterator.")
	})
}

// An iterator reads until 1 ms before its 5 minutes end and has expired at
// them, on the emulator clock, with kinesalite's text and its time format
// (k, kinesis.md 5.6; KIN-11; GetRecords note).
func TestIteratorExpiresAtFiveMinutes(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		issued := h.Clock.Now()
		it := h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")
		h.advance(iteratorTTL - time.Millisecond)
		h.records(it, 0)
		h.advance(time.Millisecond)
		const layout = "Mon Jan 02 15:04:05 UTC 2006"
		wantErr(h, "at 300 s", fail(h, kinesisapi.OpGetRecords, &kinesisapi.GetRecordsInput{ShardIterator: it}), "ExpiredIteratorException", 400,
			"Iterator expired. The iterator was created at time "+issued.Wire().UTC().Format(layout)+" while right now it is "+h.Clock.Now().Wire().UTC().Format(layout)+
				" which is further in the future than the tolerated delay of 300000 milliseconds.")
	})
}

// MillisBehindLatest per kinesis.get-records.millis-behind-latest, the
// scenario of kinesis.md 14.10: A, 30 s, B, 10 s, then GetRecords Limit 1
// and the rest; each value's formula is computed from the arrivals as the
// switch names it, never written out as a number; 0 at the tip ("A value
// of zero indicates that record processing is caught up", aws-doc 5.10).
func TestMillisBehindLatestFollowsTheSwitch(t *testing.T) {
	for _, m := range []kinesis.MillisBehind{kinesis.BehindNextUnread, kinesis.BehindLastReturned, kinesis.BehindTipLastReturned} {
		run(t, kinesis.Options{MillisBehind: m}, func(h *harness) {
			h.create("s", 1)
			it := h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")
			a := h.Clock.Now()
			h.put("s", "a", "A")
			h.advance(30 * time.Second)
			b := h.Clock.Now()
			h.put("s", "a", "B")
			h.advance(10 * time.Second)
			now := h.Clock.Now()
			var want time.Duration
			for _, e := range h.svc.Switches() {
				if e.Name != "kinesis.get-records.millis-behind-latest" {
					continue
				}
				switch e.Value {
				case "now-minus-next-unread-arrival":
					want = now.Sub(b)
				case "now-minus-last-returned-arrival":
					want = now.Sub(a)
				case "tip-minus-last-returned-arrival":
					want = b.Sub(a)
				default:
					h.Fatalf("unknown switch value %q", e.Value)
				}
			}
			if want == 0 {
				h.Fatal("Switches lists no kinesis.get-records.millis-behind-latest")
			}
			first := h.records(it, 1)
			if len(first.Records) != 1 || deref(first.MillisBehindLatest) != want.Milliseconds() {
				h.Errorf("%s: Limit 1 MillisBehindLatest = %v, want %d", m, deref(first.MillisBehindLatest), want.Milliseconds())
			}
			rest := h.records(*first.NextShardIterator, 0)
			if len(rest.Records) != 1 || deref(rest.MillisBehindLatest) != int64(0) {
				h.Errorf("%s: the rest MillisBehindLatest = %v, want 0", m, deref(rest.MillisBehindLatest))
			}
		})
	}
}

// ---- Retention on the emulator clock (kinesis.md 4.7, 5.7, 13.5 item 4) ----.

// A record arriving at t is readable until t+retention and gone at it
// ("a record is gone when arrival plus retention is not after now", the
// aws-doc kinesis.retention.trim of 13.2); a trimmed number is refused by
// GetShardIterator AT (contract 3.5, u text, KIN-12) while a TRIM_HORIZON
// read starts at the oldest untrimmed record; Increase does not bring back
// expired data and Decrease trims at once (aws-doc 4.7).
func TestRetentionTrimsAtExpiry(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		first := h.put("s", "k", "old").SequenceNumber
		h.advance(time.Hour)
		h.put("s", "k", "new")
		h.advance(retention24 - time.Hour - time.Millisecond)
		th := h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")
		if got := h.readAll(th); len(got) != 2 {
			h.Fatalf("1 ms before expiry: %d records, want 2", len(got))
		}
		h.advance(time.Millisecond)
		if got := h.readAll(th); len(got) != 1 || string(got[0].Data) != "new" {
			h.Errorf("at expiry the TRIM_HORIZON iterator reads %d records, want only the new one", len(got))
		}
		invalidArg(h, "AT a trimmed number", fail(h, kinesisapi.OpGetShardIterator, &kinesisapi.GetShardIteratorInput{
			StreamName: new("s"), ShardId: shardID(0),
			ShardIteratorType: kinesisapi.ShardIteratorTypeAtSequenceNumber, StartingSequenceNumber: new(first),
		}),
			"StartingSequenceNumber "+first+" used in GetShardIterator on shard "+shardID(0)+" in stream s under account "+account+" is invalid because it has been trimmed.")
		call(h, kinesisapi.OpIncreaseStreamRetentionPeriod, &kinesisapi.IncreaseStreamRetentionPeriodInput{StreamName: new("s"), RetentionPeriodHours: 48})
		if got := h.readAll(h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")); len(got) != 1 {
			h.Errorf("after Increase: %d records, want the trimmed one to stay gone", len(got))
		}
		h.advance(10 * time.Hour) // "new" is 34 h old, within 48 h
		if got := h.readAll(h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")); len(got) != 1 {
			h.Errorf("34 h under 48 h retention: %d records, want 1", len(got))
		}
		call(h, kinesisapi.OpDecreaseStreamRetentionPeriod, &kinesisapi.DecreaseStreamRetentionPeriodInput{StreamName: new("s"), RetentionPeriodHours: 24})
		if got := h.readAll(h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")); len(got) != 0 {
			h.Errorf("after Decrease to 24 h: %d records, want none", len(got))
		}
	})
}

// ---- rapid properties over the service (spec B3) ----.

type putOp struct {
	key  string
	data []byte
	gap  time.Duration
}

func putOpsGen() *rapid.Generator[[]putOp] {
	return rapid.SliceOfN(rapid.Custom(func(t *rapid.T) putOp {
		return putOp{
			key:  rapid.StringMatching(`[a-z0-9]{1,8}`).Draw(t, "key"),
			data: rapid.SliceOfN(rapid.Byte(), 0, 32).Draw(t, "data"),
			gap:  time.Duration(rapid.Int64Range(0, int64(2*time.Second)).Draw(t, "gap")),
		}
	}), 1, 40)
}

// Every put lands in the shard whose range holds its key's hash, and
// sequence numbers strictly increase per shard (spec B3; kinesis.md 3.4
// "Sequence numbers increase over time and are specific to a shard",
// aws-doc), under any switch setting.
func TestPutRecordPlacementAndOrderProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		shards := rapid.Int32Range(1, 6).Draw(rt, "shards")
		n := int(shards)
		ops := putOpsGen().Draw(rt, "puts")
		o := kinesis.Options{
			EvenSplit:      rapid.SampledFrom([]kinesis.EvenSplit{kinesis.SplitIFloor, kinesis.SplitFloorI}).Draw(rt, "split"),
			HashByteOrder:  rapid.SampledFrom([]kinesis.HashByteOrder{kinesis.HashBigEndianUTF8, kinesis.HashLittleEndianUTF8}).Draw(rt, "order"),
			SequenceLayout: rapid.SampledFrom([]kinesis.SequenceLayout{kinesis.LayoutV2, kinesis.LayoutDecimal56}).Draw(rt, "layout"),
		}
		var problems []string
		run(t, o, func(h *harness) {
			h.create("s", shards)
			last := map[string]shardlog.Seq{}
			for i, op := range ops {
				h.advance(op.gap)
				out := call(h, kinesisapi.OpPutRecord, &kinesisapi.PutRecordInput{StreamName: new("s"), PartitionKey: new(op.key), Data: op.data})
				if want := h.shardOf(n, op.key); out.ShardId != want {
					problems = append(problems, fmt.Sprintf("put %d key %q -> %s, want %s", i, op.key, out.ShardId, want))
				}
				seq := shardlog.Seq(out.SequenceNumber)
				if prev, ok := last[out.ShardId]; ok && seq.Compare(prev) <= 0 {
					problems = append(problems, fmt.Sprintf("put %d on %s: %s does not follow %s", i, out.ShardId, seq, prev))
				}
				last[out.ShardId] = seq
			}
		})
		if len(problems) > 0 {
			rt.Fatal(strings.Join(problems, "\n"))
		}
	})
}

// GetRecords pages of any Limit concatenate to the shard's log: the
// records put to it, in put order, nothing repeated or skipped (spec B3).
func TestGetRecordsPagesConcatenateProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		ops := putOpsGen().Draw(rt, "puts")
		limits := rapid.SliceOfN(rapid.Int32Range(1, 7), 1, 50).Draw(rt, "limits")
		var problem string
		run(t, kinesis.Options{}, func(h *harness) {
			h.create("s", 1)
			want := make([][]byte, 0, len(ops))
			for _, op := range ops {
				h.advance(op.gap)
				h.put("s", op.key, string(op.data))
				want = append(want, op.data)
			}
			it := h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, "")
			var got [][]byte
			for i := 0; len(got) < len(want) && i < 1000; i++ {
				page := h.records(it, limits[i%len(limits)])
				if len(page.Records) > int(limits[i%len(limits)]) {
					problem = fmt.Sprintf("page %d has %d records over Limit %d", i, len(page.Records), limits[i%len(limits)])
					return
				}
				for _, r := range page.Records {
					got = append(got, r.Data)
				}
				it = *page.NextShardIterator
			}
			if !slices.EqualFunc(got, want, bytes.Equal) {
				problem = fmt.Sprintf("pages concatenate to %q, want %q", got, want)
			}
		})
		if problem != "" {
			rt.Fatal(problem)
		}
	})
}

// Nothing older than the retention period is returned, wherever the clock
// stops: a read at now returns exactly the records whose arrival plus
// retention is after now (spec B3; kinesis.md 13.2 kinesis.retention.trim
// aws-doc, 5.7).
func TestNothingOlderThanRetentionProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		hours := rapid.Int32Range(24, 72).Draw(rt, "retentionHours")
		n := rapid.IntRange(1, 20).Draw(rt, "records")
		gaps := rapid.SliceOfN(rapid.Int64Range(0, int64(10*time.Hour)), n, n).Draw(rt, "gaps")
		tail := time.Duration(rapid.Int64Range(0, int64(80*time.Hour)).Draw(rt, "tail"))
		var problem string
		run(t, kinesis.Options{}, func(h *harness) {
			h.create("s", 1)
			if hours > 24 {
				call(h, kinesisapi.OpIncreaseStreamRetentionPeriod, &kinesisapi.IncreaseStreamRetentionPeriodInput{StreamName: new("s"), RetentionPeriodHours: hours})
			}
			retention := time.Duration(hours) * time.Hour
			arrivals := make([]time.Time, 0, n)
			for i := range n {
				h.advance(time.Duration(gaps[i]))
				arrivals = append(arrivals, h.Clock.Now().Wire())
				h.put("s", "k", strconv.Itoa(i))
			}
			h.advance(tail)
			now := h.Clock.Now().Wire()
			var want []string
			for i, at := range arrivals {
				if at.Add(retention).After(now) {
					want = append(want, strconv.Itoa(i))
				}
			}
			rs := h.readAll(h.iterator("s", shardID(0), kinesisapi.ShardIteratorTypeTrimHorizon, ""))
			got := make([]string, 0, len(rs))
			for _, r := range rs {
				got = append(got, string(r.Data))
			}
			if !slices.Equal(got, want) {
				problem = fmt.Sprintf("read %v at %v, want %v", got, now, want)
			}
		})
		if problem != "" {
			rt.Fatal(problem)
		}
	})
}

// ---- Snapshot and reset (contract 3.9, B4) ----.

// Export then Reset drops the streams; Import restores them with their
// records, tags and settings, and an iterator issued before the save still
// reads until it expires (contract 3.9; kinesis.md 13.5 items 3 and 7).
// Import refuses another version.
func TestSnapshotRoundTrip(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 2)
		call(h, kinesisapi.OpAddTagsToStream, &kinesisapi.AddTagsToStreamInput{StreamName: new("s"), Tags: map[string]string{"a": "b"}})
		call(h, kinesisapi.OpIncreaseStreamRetentionPeriod, &kinesisapi.IncreaseStreamRetentionPeriodInput{StreamName: new("s"), RetentionPeriodHours: 48})
		h.put("s", "k", "payload")
		shard := h.shardOf(2, "k")
		it := h.iterator("s", shard, kinesisapi.ShardIteratorTypeTrimHorizon, "")
		before := h.summary("s")

		sections, err := h.svc.Export()
		if err != nil || len(sections) == 0 {
			h.Fatalf("Export = %d sections, %v", len(sections), err)
		}
		h.svc.Reset(kernel.ResetScope{})
		wantErr(h, "after reset", fail(h, kinesisapi.OpDescribeStreamSummary, &kinesisapi.DescribeStreamSummaryInput{StreamName: new("s")}), "ResourceNotFoundException", 400, notFound("s", account))
		if err := h.svc.Import(sections); err != nil {
			h.Fatalf("Import: %v", err)
		}
		after := h.summary("s")
		if after.RetentionPeriodHours != 48 || after.OpenShardCount != 2 || !after.StreamCreationTimestamp.Equal(before.StreamCreationTimestamp) {
			h.Errorf("restored summary = %+v, want %+v", after, before)
		}
		if got := h.readAll(it); len(got) != 1 || string(got[0].Data) != "payload" {
			h.Errorf("a pre-save iterator reads %d records after load", len(got))
		}
		if tags := call(h, kinesisapi.OpListTagsForStream, &kinesisapi.ListTagsForStreamInput{StreamName: new("s")}); len(tags.Tags) != 1 {
			h.Errorf("restored tags = %v", tagPairs(tags.Tags))
		}
		bad := slices.Clone(sections)
		bad[0] = store.Section{Path: bad[0].Path, Version: 99, Data: bad[0].Data}
		if err := h.svc.Import(bad); err == nil {
			h.Error("Import accepted a version 99 section")
		}
	})
}
