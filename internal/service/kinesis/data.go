package kinesis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

const (
	// maxPutRecordsBytes is PutRecords' request limit over data plus
	// partition keys: "up to 10 MiB, including partition keys" (aws-doc
	// kinesis.md 5.2).
	maxPutRecordsBytes = 10 << 20
	// iteratorTTL: "A shard iterator expires 5 minutes after it is
	// returned to the requester" (aws-doc kinesis.md 3.5).
	iteratorTTL = 5 * time.Minute
	// defaultRecordsLimit is GetRecords' default and largest Limit, and
	// maxRecordsBytes the most data one call returns (aws-doc kinesis.md
	// 5.5).
	defaultRecordsLimit = 10000
	maxRecordsBytes     = 10 << 20
)

// bindData binds the data plane: PutRecord, PutRecords, GetShardIterator
// and GetRecords.
func (s *Service) bindData(b kinesisapi.Binder) {
	b.PutRecord(s.putRecord, registry.Partial{Note: "per-shard write limits (1 MiB/s, 1,000 records/s) are not enforced and ProvisionedThroughputExceededException comes only from fault injection; DryRun answers 501 NotImplemented; argument and record-size error texts follow kinesalite or are understudy's and are UNVERIFIED (kinesis.md 13.3)"})
	b.PutRecords(s.putRecords, registry.Partial{Note: "per-shard write limits (1 MiB/s, 1,000 records/s) are not enforced; no entry fails: a fault rule fails the whole call, and entry-level fault injection is not built; argument, record-size and request-size error texts follow kinesalite or are understudy's and are UNVERIFIED (kinesis.md 13.3)"})
	b.GetShardIterator(s.getShardIterator, registry.Partial{Note: "the five calls per second per shard limit is not enforced; DryRun answers 501 NotImplemented; argument error texts follow kinesalite and are UNVERIFIED (kinesis.md 5.4, 13.3)"})
	b.GetRecords(s.getRecords, registry.Partial{Note: "per-shard read limits (5 calls/s, 2 MiB/s and the 5-second wait after a 10 MiB read) are not enforced; DryRun answers 501 NotImplemented; the Limit and expired-iterator texts follow kinesalite or are understudy's and are UNVERIFIED (kinesis.md 5.6, 13.3)"})
}

// dryRun reports a DryRun: true, which slice 1 answers with 501 rather
// than pretend to check permissions (kinesis.md 13.9; contract 3.7).
func dryRun(p *bool) bool { return p != nil && *p }

// putEntry is one record a PutRecord or PutRecords call sends.
type putEntry struct {
	key  *string
	data []byte
	ehk  *string
}

// placed is an entry checked against its stream: the shard it goes to.
type placed struct {
	entry putEntry
	shard int
}

// appended is where an entry landed.
type appended struct {
	shard string
	seq   shardlog.Seq
}

func (s *Service) putRecord(ctx context.Context, c *kernel.Call, in *kinesisapi.PutRecordInput) (*kinesisapi.PutRecordOutput, *awserr.Error) {
	if dryRun(in.DryRun) {
		return nil, errNotImplemented("DryRun", "PutRecord")
	}
	// SequenceNumberForOrdering is accepted and needs nothing: numbers
	// already strictly increase within a shard (kinesis.md 3.4).
	out, err := s.put(ctx, c, in.StreamName, in.StreamARN, []putEntry{{key: in.PartitionKey, data: in.Data, ehk: in.ExplicitHashKey}}, false)
	if err != nil {
		return nil, err
	}
	return &kinesisapi.PutRecordOutput{ShardId: out[0].shard, SequenceNumber: string(out[0].seq)}, nil
}

func (s *Service) putRecords(ctx context.Context, c *kernel.Call, in *kinesisapi.PutRecordsInput) (*kinesisapi.PutRecordsOutput, *awserr.Error) {
	if dryRun(in.DryRun) {
		return nil, errNotImplemented("DryRun", "PutRecords")
	}
	entries := make([]putEntry, len(in.Records))
	for i, r := range in.Records {
		entries[i] = putEntry{key: r.PartitionKey, data: r.Data, ehk: r.ExplicitHashKey}
	}
	out, err := s.put(ctx, c, in.StreamName, in.StreamARN, entries, true)
	if err != nil {
		return nil, err
	}
	// No entry fails on its own: the call succeeds or fails whole
	// (PutRecords note; kinesis.md 5.2 example).
	res := &kinesisapi.PutRecordsOutput{FailedRecordCount: new(int32(0)), Records: make([]kinesisapi.PutRecordsResultEntry, len(out))}
	for i, a := range out {
		res.Records[i] = kinesisapi.PutRecordsResultEntry{ShardId: new(a.shard), SequenceNumber: new(string(a.seq))}
	}
	return res, nil
}

// put checks the entries against the stream (contract 3.5: the stream,
// then for PutRecords the request size, then each entry in order, the
// first failing one refusing the whole call), stores their data and
// appends them in request order at the write's instant, waking the
// stream's readers once.
func (s *Service) put(ctx context.Context, c *kernel.Call, name, arn *string, entries []putEntry, batch bool) ([]appended, *awserr.Error) {
	r, err := streamOf(c, name, arn)
	if err != nil {
		return nil, err
	}
	str, err := s.readStream(c, r)
	if err != nil {
		return nil, err
	}
	plan, err := s.plan(str, entries, batch)
	if err != nil {
		return nil, err
	}
	refs, err := s.storeData(ctx, plan)
	if err != nil {
		return nil, err
	}
	scope := c.Scope()
	type result struct {
		out      []appended
		released []blob.Ref
	}
	res, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, tx *kernel.Tx) (result, *awserr.Error) {
		cur, ok := st.Streams[r.name]
		if !ok || cur.Created != str.Created {
			// Deleted, or deleted and created again, since it was read.
			return result{}, r.notFound()
		}
		released := expire(cur, now)
		out := make([]appended, len(plan))
		for i, p := range plan {
			e, aerr := cur.Shards[p.shard].log.Append(now, Record{
				PartitionKey: deref(p.entry.key), Data: refs[i], Size: len(p.entry.data), ExplicitHashKey: deref(p.entry.ehk),
			})
			if aerr != nil {
				// Slice 1 never closes a shard.
				return result{}, awserr.Internalf(aerr)
			}
			out[i] = appended{shard: shardID(p.shard), seq: e.Seq}
		}
		tx.Wake(shardlog.WaitKey(streamARN(scope, r.name)))
		s.armRetention(tx, scope, cur)
		return result{out: out, released: released}, nil
	})
	if err != nil {
		s.release(ctx, refs)
		return nil, err
	}
	s.release(ctx, res.released)
	c.Touch(streamARN(scope, r.name))
	return res.out, nil
}

// plan checks a call's entries against str: for PutRecords the request
// size first, then each entry in order, a refused one's text prefixed
// "Record <i>: " (contract 3.5).
func (s *Service) plan(str Stream, entries []putEntry, batch bool) ([]placed, *awserr.Error) {
	if batch {
		total := 0
		for _, e := range entries {
			total += len(e.data) + len(deref(e.key))
		}
		if total > maxPutRecordsBytes {
			return nil, errPutRecordsTooLarge(total)
		}
	}
	ranges := make([]HashRange, len(str.Shards))
	for i, sh := range str.Shards {
		ranges[i] = sh.Range
	}
	plan := make([]placed, len(entries))
	for i, e := range entries {
		shard, err := s.place(str, ranges, e)
		if err != nil {
			if batch {
				err.Message = fmt.Sprintf("Record %d: %s", i, err.Message)
			}
			return nil, err
		}
		plan[i] = placed{entry: e, shard: shard}
	}
	return plan, nil
}

// storeData puts each entry's data in the blob store, one reference each,
// which the caller hands to the state or releases.
func (s *Service) storeData(ctx context.Context, plan []placed) ([]blob.Ref, *awserr.Error) {
	refs := make([]blob.Ref, 0, len(plan))
	for _, p := range plan {
		ref, _, err := s.blobs.Put(ctx, bytes.NewReader(p.entry.data), int64(len(p.entry.data)))
		if err != nil {
			s.release(ctx, refs)
			return nil, awserr.Internalf(fmt.Errorf("store a Kinesis record's data in the blob store: %w", err))
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// place checks one entry (contract 3.5: ExplicitHashKey in range, a
// partition key, data plus UTF-8 key within MaxRecordSizeInKiB) and returns
// the index of its shard: the one whose range holds its ExplicitHashKey,
// which "overrides the partition key to shard mapping", else MD5 of its
// partition key (aws-doc kinesis.md 5.3).
func (s *Service) place(str Stream, ranges []HashRange, e putEntry) (int, *awserr.Error) {
	var (
		h      HashKey
		hasEHK bool
	)
	if e.ehk != nil {
		k, err := ParseHashKey(*e.ehk)
		if err != nil {
			return 0, errExplicitHashKey(*e.ehk)
		}
		h, hasEHK = k, true
	}
	if e.key == nil {
		return 0, errNoPartitionKey()
	}
	maxBytes := int(str.MaxRecordSizeKiB) * 1024
	if n := len(e.data) + len(*e.key); n > maxBytes {
		return 0, errRecordTooLarge(n, maxBytes)
	}
	if !hasEHK {
		h = PartitionHash(*e.key, s.opts.HashByteOrder)
	}
	i := ShardFor(ranges, h)
	if i < 0 {
		return 0, awserr.Internalf(fmt.Errorf("hash key %s is in no shard of stream %s: the shards' ranges do not cover [0, 2^128-1]", h, str.Name))
	}
	return i, nil
}

// answer is a result computed under a cell's lock.
type answer[T any] struct {
	v   T
	err *awserr.Error
}

// shardIndex is the index of the shard named id in str; false when str
// has none.
func shardIndex(str Stream, id string) (int, bool) {
	for i := range str.Shards {
		if shardID(i) == id {
			return i, true
		}
	}
	return 0, false
}

func (s *Service) getShardIterator(ctx context.Context, c *kernel.Call, in *kinesisapi.GetShardIteratorInput) (*kinesisapi.GetShardIteratorOutput, *awserr.Error) {
	if dryRun(in.DryRun) {
		return nil, errNotImplemented("DryRun", "GetShardIterator")
	}
	r, err := streamOf(c, in.StreamName, in.StreamARN)
	if err != nil {
		return nil, err
	}
	if r.foreign {
		return nil, r.notFound()
	}
	scope := c.Scope()
	// Trimmed first, so the checks below see the shard as of now.
	if err := s.trimStream(ctx, c, scope, r.name); err != nil {
		return nil, err
	}
	now := c.Admitted()
	got := readState(s, scope, func(st *State) answer[string] {
		str, ok := st.Streams[r.name]
		if !ok {
			return answer[string]{err: r.notFound()}
		}
		i, ok := shardIndex(str, in.ShardId)
		if !ok {
			return answer[string]{err: errShardNotFound(in.ShardId, str.Name, r.account)}
		}
		pos, err := iteratorStart(in, str, i, r.account, now)
		if err != nil {
			return answer[string]{err: err}
		}
		it := shardlog.Iterator{Stream: streamARN(scope, str.Name), Created: str.Created, Shard: in.ShardId, Pos: pos, Issued: now}
		return answer[string]{v: string(shardlog.NewCodec(st.TokenKey, shardlog.Opaque{}).Encode(it))}
	})
	if got.err != nil {
		return nil, got.err
	}
	c.Touch(streamARN(scope, r.name))
	return &kinesisapi.GetShardIteratorOutput{ShardIterator: new(got.v)}, nil
}

// iteratorStart checks the type and its arguments (contract 3.5, KIN-12)
// and returns where the iterator starts: TRIM_HORIZON stays TrimHorizon,
// resolved at each read; LATEST and AT_TIMESTAMP resolve now, a timestamp
// at or before the oldest record being TRIM_HORIZON (aws-doc kinesis.md
// 5.4); AT and AFTER_SEQUENCE_NUMBER start at the number given.
func iteratorStart(in *kinesisapi.GetShardIteratorInput, str Stream, i int, account string, now clock.Instant) (shardlog.Position, *awserr.Error) {
	typ := in.ShardIteratorType
	sh := str.Shards[i]
	switch typ {
	case kinesisapi.ShardIteratorTypeAtSequenceNumber, kinesisapi.ShardIteratorTypeAfterSequenceNumber:
		if in.StartingSequenceNumber == nil {
			return nil, errIteratorNoNumber(typ)
		}
		seq, err := startingSequence(*in.StartingSequenceNumber, sh.Layout, i, str.Name, account)
		if err != nil {
			return nil, err
		}
		var pos shardlog.Position = shardlog.At{Seq: seq}
		if typ == kinesisapi.ShardIteratorTypeAfterSequenceNumber {
			pos = shardlog.After{Seq: seq}
		}
		if _, _, rerr := sh.log.Read(pos, 1, math.MaxInt, Record.bytes); rerr != nil {
			if _, trimmed := errors.AsType[*shardlog.TrimmedError](rerr); trimmed {
				return nil, errSequenceInvalid(string(seq), shardID(i), str.Name, account, " because it has been trimmed")
			}
			return nil, awserr.Internalf(rerr)
		}
		return pos, nil
	case kinesisapi.ShardIteratorTypeTrimHorizon, kinesisapi.ShardIteratorTypeLatest, kinesisapi.ShardIteratorTypeAtTimestamp:
	}
	if in.StartingSequenceNumber != nil {
		return nil, errIteratorExtraNumber(typ)
	}
	switch typ {
	case kinesisapi.ShardIteratorTypeLatest:
		return sh.log.Resolve(shardlog.Latest{}), nil
	case kinesisapi.ShardIteratorTypeAtTimestamp:
		if in.Timestamp == nil {
			return nil, errIteratorNoTimestamp()
		}
		if ts, cur := in.Timestamp.UnixMilli(), now.Wire().UnixMilli(); ts > cur {
			return nil, errIteratorFutureTimestamp(ts, cur)
		}
		return sh.log.Resolve(shardlog.AtTime{At: clock.FromWire(*in.Timestamp)}), nil
	case kinesisapi.ShardIteratorTypeTrimHorizon, kinesisapi.ShardIteratorTypeAtSequenceNumber, kinesisapi.ShardIteratorTypeAfterSequenceNumber:
	}
	return shardlog.TrimHorizon{}, nil
}

// startingSequence reads a StartingSequenceNumber under the shard's
// layout: under v2-56-digit it must decode as a V2 number of this shard,
// under decimal-56 it must be a number (contract 3.5, understudy's texts,
// KIN-12).
func startingSequence(text string, layout SequenceLayout, i int, name, account string) (shardlog.Seq, *awserr.Error) {
	invalid := errSequenceInvalid(text, shardID(i), name, account, "")
	seq, err := shardlog.ParseSeq(text)
	if err != nil {
		return "", invalid
	}
	switch layout {
	case LayoutV2:
		f, ok := DecodeV2(seq)
		if !ok {
			return "", invalid
		}
		if f.ShardIndex != i {
			return "", errSequenceOtherShard(f.ShardIndex, shardID(i))
		}
	case LayoutDecimal56:
	}
	return seq, nil
}

// page is one GetRecords answer read under the cell's lock.
type page struct {
	records []kinesisapi.Record
	next    string
	behind  int64
}

func (s *Service) getRecords(ctx context.Context, c *kernel.Call, in *kinesisapi.GetRecordsInput) (*kinesisapi.GetRecordsOutput, *awserr.Error) {
	if dryRun(in.DryRun) {
		return nil, errNotImplemented("DryRun", "GetRecords")
	}
	scope := c.Scope()
	now := c.Admitted()
	it, name, err := s.iteratorOf(scope, in.ShardIterator, now)
	if err != nil {
		return nil, err
	}
	if in.StreamARN != nil && *in.StreamARN != it.Stream.String() {
		return nil, errInvalidIterator()
	}
	if err := s.trimStream(ctx, c, scope, name); err != nil {
		return nil, err
	}
	limit := defaultRecordsLimit
	if in.Limit != nil {
		limit = int(*in.Limit)
	}
	account := scope.Account.String()
	got := readState(s, scope, func(st *State) answer[page] {
		str, ok := st.Streams[name]
		if !ok {
			return answer[page]{err: errStreamNotFound(name, account)}
		}
		if str.Created.Compare(it.Created) != 0 {
			// A stream created again under the name refuses its
			// predecessor's iterators (kinesis.md 13.5 item 3).
			return answer[page]{err: errInvalidIterator()}
		}
		i, ok := shardIndex(str, it.Shard)
		if !ok {
			return answer[page]{err: errShardGone(it.Shard, name, account)}
		}
		p, err := s.readPage(str.Shards[i].log, it.Pos, limit, now)
		if err != nil {
			return answer[page]{err: err}
		}
		next := shardlog.Iterator{Stream: it.Stream, Created: it.Created, Shard: it.Shard, Pos: p.nextPos, Issued: now}
		p.page.next = string(shardlog.NewCodec(st.TokenKey, shardlog.Opaque{}).Encode(next))
		return answer[page]{v: p.page}
	})
	if got.err != nil {
		return nil, got.err
	}
	c.Touch(it.Stream)
	return &kinesisapi.GetRecordsOutput{Records: got.v.records, NextShardIterator: new(got.v.next), MillisBehindLatest: new(got.v.behind)}, nil
}

// iteratorOf decodes a ShardIterator sent to GetRecords, without one pair
// of surrounding double quotes (R-weak, kinesis.md 3.5): one that does not
// decode or verify under the scope's key, or names a stream of another
// scope, is kinesalite's "Invalid ShardIterator." (KIN-12); one at or past
// its 5 minutes is ExpiredIteratorException (KIN-11).
func (s *Service) iteratorOf(scope aws.Scope, text string, now clock.Instant) (shardlog.Iterator, string, *awserr.Error) {
	if len(text) >= 2 && strings.HasPrefix(text, `"`) && strings.HasSuffix(text, `"`) {
		text = text[1 : len(text)-1]
	}
	key := readState(s, scope, func(st *State) []byte { return st.TokenKey })
	if key == nil {
		return shardlog.Iterator{}, "", errInvalidIterator()
	}
	it, err := shardlog.NewCodec(key, shardlog.Opaque{}).Decode([]byte(text), now, iteratorTTL)
	if expired, ok := errors.AsType[*shardlog.ExpiredError](err); ok {
		return shardlog.Iterator{}, "", errIteratorExpired(expired.Issued, expired.Now)
	}
	if err != nil {
		return shardlog.Iterator{}, "", errInvalidIterator()
	}
	name := strings.TrimPrefix(it.Stream.Resource, "stream/")
	if streamARN(scope, name) != it.Stream {
		return shardlog.Iterator{}, "", errInvalidIterator()
	}
	return it, name, nil
}

// pageRead is a page and the position its NextShardIterator carries.
type pageRead struct {
	page    page
	nextPos shardlog.Position
}

// readPage reads up to limit records, and at most 10 MiB of data plus
// keys (the first record always), from pos. GetRecords never fails for
// trimming: a position behind the trim point reads from the oldest
// untrimmed record (contract 3.5).
func (s *Service) readPage(log *shardlog.Shard[Record], pos shardlog.Position, limit int, now clock.Instant) (pageRead, *awserr.Error) {
	es, next, err := log.Read(pos, limit, maxRecordsBytes, Record.bytes)
	if _, trimmed := errors.AsType[*shardlog.TrimmedError](err); trimmed {
		es, next, err = log.Read(shardlog.TrimHorizon{}, limit, maxRecordsBytes, Record.bytes)
	}
	if err != nil {
		return pageRead{}, awserr.Internalf(err)
	}
	records := make([]kinesisapi.Record, 0, len(es))
	for _, e := range es {
		data, err := s.readData(e.Value)
		if err != nil {
			return pageRead{}, err
		}
		records = append(records, kinesisapi.Record{
			SequenceNumber: string(e.Seq),
			// Millisecond precision (aws-doc kinesis.md 5.10).
			ApproximateArrivalTimestamp: new(millis(e.Arrival).Wire()),
			Data:                        data,
			PartitionKey:                new(e.Value.PartitionKey),
		})
	}
	return pageRead{page: page{records: records, behind: s.millisBehind(log, es, next, now)}, nextPos: next}, nil
}

// readData returns a record's bytes from the blob store.
func (s *Service) readData(r Record) ([]byte, *awserr.Error) {
	f, err := s.blobs.Open(r.Data)
	if err != nil {
		return nil, awserr.Internalf(fmt.Errorf("read a Kinesis record's data from the blob store: %w", err))
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, awserr.Internalf(fmt.Errorf("read a Kinesis record's data from the blob store: %w", err))
	}
	return b, nil
}

// millisBehind is MillisBehindLatest per
// kinesis.get-records.millis-behind-latest (contract 3.8; inferred,
// KIN-10): 0 when no record lies after the page; otherwise now minus the
// first unread record's arrival, now minus the last returned one's, or the
// newest record's arrival minus the last returned one's. An empty page
// with records after it measures from the first unread record in all
// three. Floored to the millisecond, never negative.
func (s *Service) millisBehind(log *shardlog.Shard[Record], returned []shardlog.Entry[Record], next shardlog.Position, now clock.Instant) int64 {
	unread, _, err := log.Read(next, 1, math.MaxInt, Record.bytes)
	if err != nil || len(unread) == 0 {
		return 0
	}
	first := unread[0].Arrival
	from := first
	if len(returned) > 0 {
		from = returned[len(returned)-1].Arrival
	}
	var d time.Duration
	switch s.opts.MillisBehind {
	case BehindNextUnread:
		d = now.Sub(first)
	case BehindLastReturned:
		d = now.Sub(from)
	case BehindTipLastReturned:
		d = newest(log, first).Sub(from)
	}
	return max(d.Milliseconds(), 0)
}

// newest is the arrival of the shard's newest record; fallback when it
// cannot be read.
func newest(log *shardlog.Shard[Record], fallback clock.Instant) clock.Instant {
	last, ok := log.Resolve(shardlog.Latest{}).(shardlog.After)
	if !ok {
		return fallback
	}
	es, _, err := log.Read(shardlog.At(last), 1, math.MaxInt, Record.bytes)
	if err != nil || len(es) == 0 {
		return fallback
	}
	return es[0].Arrival
}

// deref reads an optional string as "" when it is absent.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
