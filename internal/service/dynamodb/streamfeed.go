package dynamodb

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/shardlog"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The feed (dynamodb-streams.md 11.1 items 2-3; ADR 0014 amendment of
// 2026-10-26): after a write to a table with an enabled stream, and before
// the write is answered, the records DynamoDB Local appended to its own
// stream of the table are copied into understudy's log, stamped with the
// write's admission instant. The table's stream lock is held from before
// the forward until the drain ends, so each record is attributed to the
// write that made it.

// feedLocks are the stream locks, one per table.
type feedLocks struct {
	mu    sync.Mutex
	locks map[tableKey]*sync.Mutex
}

// lock takes the locks of tables in scope, in sorted name order so two
// writes never wait on each other, and returns their release.
func (f *feedLocks) lock(scope aws.Scope, tables []string) func() {
	f.mu.Lock()
	if f.locks == nil {
		f.locks = map[tableKey]*sync.Mutex{}
	}
	held := make([]*sync.Mutex, 0, len(tables))
	for _, t := range slices.Sorted(slices.Values(tables)) {
		k := tableKey{scope: scope, table: t}
		m, ok := f.locks[k]
		if !ok {
			m = &sync.Mutex{}
			f.locks[k] = m
		}
		held = append(held, m)
	}
	f.mu.Unlock()
	for _, m := range held {
		m.Lock()
	}
	return func() {
		for _, m := range slices.Backward(held) {
			m.Unlock()
		}
	}
}

// fed is h with the feed: a write naming a table with an enabled stream
// holds the table's stream lock across h and drains Local's stream of it
// once Local has answered, success or failure. A call understudy refused
// before Local saw it drains nothing.
//
// Only PutItem, UpdateItem and DeleteItem feed so far; BatchWriteItem,
// TransactWriteItems and the PartiQL writes do not (the GetRecords note).
func fed[In any](s *Service, h kernel.RawHandler[In], names func(*In) []string) kernel.RawHandler[In] {
	return func(ctx context.Context, c *kernel.Call, in *In, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
		streamed := s.streamedTables(c.Scope(), names(in))
		if len(streamed) == 0 {
			return h(ctx, c, in, w)
		}
		defer s.feedLocks.lock(c.Scope(), streamed)()
		resp, err := h(ctx, c, in, w)
		if err != nil {
			return resp, err
		}
		for _, t := range streamed {
			s.drain(ctx, c, t)
		}
		return resp, err
	}
}

// streamedTables are the tables of names, by name, that have an enabled
// stream in scope.
func (s *Service) streamedTables(scope aws.Scope, names []string) []string {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []string {
		var out []string
		for _, n := range names {
			t := tableName(n)
			if l, ok := st.latestStream(t); ok && l.Enabled && !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
		return out
	})
}

// enabledStream is the enabled stream of table in scope with a copy of its
// feed position.
func (s *Service) enabledStream(scope aws.Scope, table string) (Stream, localFeed, bool) {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return Stream{}, localFeed{}, false
	}
	type found struct {
		st   Stream
		feed localFeed
		ok   bool
	}
	f := store.Read(cell, func(st *State) found {
		l, ok := st.latestStream(table)
		if !ok || !l.Enabled {
			return found{}
		}
		return found{st: l, feed: l.log.feed, ok: true}
	})
	return f.st, f.feed, f.ok
}

// drain copies the records Local appended to its stream of table since the
// feed's saved position into the log, each stamped with c's admission
// instant, a new eventID and, for a TTL sweeper's delete, userIdentity
// (dynamodb-streams.md 11.1 item 4, 7.5), and wakes the stream's readers.
//
// A drain that fails appends nothing and is journaled as the stream moving
// from Draining to DrainFailed; the write's answer stays as it was. The
// next write's drain then takes the records from the saved position, with
// that write's instant. Not built yet: keeping the failed write's instant
// for its records and retrying on a 1 s emulator timer
// (dynamodb-streams.md 11.1 item 3); the GetRecords note says so.
func (s *Service) drain(ctx context.Context, c *kernel.Call, table string) {
	scope := c.Scope()
	st, feed, ok := s.enabledStream(scope, table)
	if !ok {
		return
	}
	arn, err := aws.ParseARN(st.arn(scope))
	if err != nil {
		return
	}
	p, err := s.pull(ctx, c, st.localARN(), feed)
	if err != nil {
		s.kernel.Log().LogAttrs(ctx, slog.LevelWarn, "draining DynamoDB Local's stream failed; its records stay in Local until the next write to the table drains them",
			slog.String("stream", arn.String()), slog.String("error", err.Error()))
		c.Record(ctx, scope, journal.StateChange{Resource: arn, From: "Draining", To: "DrainFailed"})
		return
	}
	ttl := false
	if o, isInternal := c.Origin().(ref.Internal); isInternal && o.Via == ref.DynamoDBTTL {
		ttl = true
	}
	_, werr := kernel.Write(c, s.cells, scope, func(_ *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		log := st.log
		log.feed.shard = p.shard
		for _, r := range p.records {
			v := r.value
			v.EventID = s.kernel.IDs().StreamEventID()
			v.TTL = ttl
			if _, err := log.shard.Append(c.Admitted(), v); err != nil {
				return struct{}{}, awserr.Internalf(err)
			}
			log.feed.last = r.seq
		}
		if len(p.records) == 0 {
			tx.Unchanged()
			return struct{}{}, nil
		}
		tx.Wake(shardlog.WaitKey(arn))
		return struct{}{}, nil
	})
	if werr != nil {
		c.Record(ctx, scope, journal.StateChange{Resource: arn, From: "Draining", To: "DrainFailed"})
	}
}

// pulled is what one drain read from Local's stream.
type pulled struct {
	shard   string
	records []pulledRecord
}

// pulledRecord is one of Local's records: its number in Local's stream
// and what the log keeps of it.
type pulledRecord struct {
	seq   string
	value streamRecord
}

// drainPage is GetRecords' largest Limit, so a drain takes as few calls as
// it can (dynamodb-streams.md 6.4).
const drainPage = 1000

// pull reads Local's stream localARN after feed's position: DescribeStream
// once to learn the shard, GetShardIterator from the saved number (or
// TRIM_HORIZON before the first record), then GetRecords until a page is
// empty.
func (s *Service) pull(ctx context.Context, c *kernel.Call, localARN string, feed localFeed) (pulled, error) {
	endpoint, ok := s.local.Endpoint()
	if !ok {
		return pulled{}, errStopped
	}
	call := func(op *registry.Operation, in, out any) error {
		return s.callLocalStreams(ctx, c, endpoint, op, in, out)
	}
	p := pulled{shard: feed.shard}
	if p.shard == "" {
		var d struct {
			StreamDescription struct {
				Shards []struct {
					ShardID string `json:"ShardId"`
				}
			}
		}
		if err := call(dynamodbstreamsapi.OpDescribeStream.Descriptor(), map[string]string{"StreamArn": localARN}, &d); err != nil {
			return pulled{}, err
		}
		shards := d.StreamDescription.Shards
		if len(shards) == 0 {
			return pulled{}, &AnswerError{Op: dynamodbstreamsapi.OpDescribeStream.Descriptor().ID, Reason: "the stream " + localARN + " has no shard"}
		}
		p.shard = shards[len(shards)-1].ShardID
	}
	type iteratorIn struct {
		StreamArn         string
		ShardID           string `json:"ShardId"`
		ShardIteratorType string
		SequenceNumber    string `json:",omitempty"`
	}
	in := iteratorIn{StreamArn: localARN, ShardID: p.shard, ShardIteratorType: "TRIM_HORIZON"}
	if feed.last != "" {
		in.ShardIteratorType, in.SequenceNumber = "AFTER_SEQUENCE_NUMBER", feed.last
	}
	var it struct{ ShardIterator *string }
	if err := call(dynamodbstreamsapi.OpGetShardIterator.Descriptor(), in, &it); err != nil {
		return pulled{}, err
	}
	for next := it.ShardIterator; next != nil; {
		type recordsIn struct {
			ShardIterator string
			Limit         int
		}
		var page localPage
		if err := call(dynamodbstreamsapi.OpGetRecords.Descriptor(), recordsIn{ShardIterator: *next, Limit: drainPage}, &page); err != nil {
			return pulled{}, err
		}
		if len(page.Records) == 0 {
			break
		}
		for _, r := range page.Records {
			v, err := r.value()
			if err != nil {
				return pulled{}, err
			}
			p.records = append(p.records, pulledRecord{seq: r.Dynamodb.SequenceNumber, value: v})
		}
		next = page.NextShardIterator
	}
	return p, nil
}

// localPage is a GetRecords answer of Local's, with the images as Local
// wrote them.
type localPage struct {
	Records           []localPageRecord
	NextShardIterator *string
}

// localPageRecord is one record of a localPage.
type localPageRecord struct {
	EventName string `json:"eventName"`
	Dynamodb  struct {
		Keys           image
		NewImage       image
		OldImage       image
		SequenceNumber string
		SizeBytes      int64
	} `json:"dynamodb"`
}

// value is what the log keeps of r, once every image parses.
func (r *localPageRecord) value() (streamRecord, error) {
	for _, im := range []image{r.Dynamodb.Keys, r.Dynamodb.NewImage, r.Dynamodb.OldImage} {
		if _, err := im.typed(); err != nil {
			return streamRecord{}, err
		}
	}
	d := r.Dynamodb
	return streamRecord{EventName: r.EventName, Keys: d.Keys, NewImage: d.NewImage, OldImage: d.OldImage, SizeBytes: d.SizeBytes}, nil
}

// callLocalStreams sends Local the DynamoDB Streams operation op with in
// as its body, under c's credential, and decodes its answer into out: an
// error answer is a *LocalError, one that does not decode an *AnswerError.
// The feed's reads are understudy reading its own backend, not a
// principal's call, so they skip the pipeline.
func (s *Service) callLocalStreams(ctx context.Context, c *kernel.Call, endpoint *url.URL, op *registry.Operation, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, aerr := s.send(ctx, endpoint, exchange{scope: c.Scope(), op: op.ID, at: c.Admitted()}, body)
	if aerr != nil {
		return aerr.AsError()
	}
	if resp.Status != http.StatusOK {
		return localError(op, resp)
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return &AnswerError{Op: op.ID, Reason: err.Error()}
	}
	return nil
}
