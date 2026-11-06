package dynamodb

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

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

// fed is h with the feed for a write whose tables names lists: PutItem,
// UpdateItem, DeleteItem, BatchWriteItem and TransactWriteItems
// (dynamodb-streams.md 11.1 item 2).
func fed[In any](s *Service, h kernel.RawHandler[In], names func(*In) []string) kernel.RawHandler[In] {
	return func(ctx context.Context, c *kernel.Call, in *In, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
		wanted := names(in)
		streamed := s.streamedTables(c.Scope(), func(t string) bool {
			return slices.ContainsFunc(wanted, func(n string) bool { return tableName(n) == t })
		})
		return s.feeding(ctx, c, streamed, func() (kernel.RawResponse, *awserr.Error) { return h(ctx, c, in, w) })
	}
}

// fedScope is h with the feed for a PartiQL call, whose tables are inside
// its statements: every table of the caller's scope with an enabled stream
// is locked and drained, so no statement is parsed (dynamodb-streams.md
// 11.1 item 2).
func fedScope[In any](s *Service, h kernel.RawHandler[In]) kernel.RawHandler[In] {
	return func(ctx context.Context, c *kernel.Call, in *In, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
		streamed := s.streamedTables(c.Scope(), func(string) bool { return true })
		return s.feeding(ctx, c, streamed, func() (kernel.RawResponse, *awserr.Error) { return h(ctx, c, in, w) })
	}
}

// feeding runs forward, a write to the tables streamed, under their stream
// locks, taken in sorted name order so writes to overlapping tables never
// deadlock. A table whose last drain failed is drained first, so its
// records keep the failed write's instant; once Local has answered forward,
// success or failure, each table is drained with c's. A call understudy
// refused before Local saw it drains nothing after it.
func (s *Service) feeding(ctx context.Context, c *kernel.Call, streamed []string, forward func() (kernel.RawResponse, *awserr.Error)) (kernel.RawResponse, *awserr.Error) {
	if len(streamed) == 0 {
		return forward()
	}
	scope := c.Scope()
	defer s.feedLocks.lock(scope, streamed)()
	// A failed drain is kept pending, retried and journaled by drain; the
	// write's answer stays as it was (dynamodb-streams.md 11.1 item 3).
	for _, t := range streamed {
		s.drainFailed(ctx, c, scope, t)
	}
	resp, err := forward()
	if err != nil {
		return resp, err
	}
	for _, t := range streamed {
		s.drain(ctx, c, scope, t, stampOf(c))
	}
	return resp, err
}

// streamedTables are the tables of scope with an enabled stream that
// wanted keeps, in name order.
func (s *Service) streamedTables(scope aws.Scope, wanted func(table string) bool) []string {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []string {
		var out []string
		for _, l := range st.Streams {
			if l.Enabled && l.TableDeleted == nil && wanted(l.Table) && !slices.Contains(out, l.Table) {
				out = append(out, l.Table)
			}
		}
		slices.Sort(out)
		return out
	})
}

// writeStamp is what a write gives the records it made: its admission
// instant, and whether it was the TTL sweeper's delete, whose record
// carries userIdentity (dynamodb-streams.md 7.5, 11.1 item 4).
type writeStamp struct {
	at  clock.Instant
	ttl bool
}

// stampOf is c's writeStamp.
func stampOf(c *kernel.Call) writeStamp {
	o, isInternal := c.Origin().(ref.Internal)
	return writeStamp{at: c.Admitted(), ttl: isInternal && o.Via == ref.DynamoDBTTL}
}

// drainTimer is the kind of the timers that retry a failed drain.
const drainTimer kernel.TimerKind = "dynamodb.stream-drain"

// drainRetry is how long after a failed drain it is retried without a
// write: a short emulator-clock timer (dynamodb-streams.md 11.1 item 3).
const drainRetry = time.Second

// onDrainRetry is the retry timer's handler: it drains the table under its
// stream lock as tracked work, which never blocks the clock.
func (s *Service) onDrainRetry(c *kernel.Call, key clock.TimerKey, _ clock.Instant) {
	k, err := parseTableKey(key.ID)
	if err != nil {
		s.kernel.Log().LogAttrs(context.Background(), slog.LevelError, "dropped a DynamoDB stream drain timer", slog.String("error", err.Error()))
		return
	}
	s.kernel.Go(kernel.InvocationRunning, func(ctx context.Context) {
		defer s.feedLocks.lock(k.scope, []string{k.table})()
		// A drain that fails again arms the next retry itself.
		s.drainFailed(ctx, c, k.scope, k.table)
	})
}

// drainFailed drains table's stream when its last drain failed; the
// records take the failed write's stamp. It reports whether the log caught
// up, as drain does.
func (s *Service) drainFailed(ctx context.Context, c *kernel.Call, scope aws.Scope, table string) (caughtUp bool) {
	if t, ok := s.enabledStream(scope, table); ok && t.feed.failed != nil {
		return s.drain(ctx, c, scope, table, *t.feed.failed)
	}
	return true
}

// closingStream takes table's stream lock for a call that closes its
// stream, an UpdateTable disabling it or a DeleteTable, and first drains a
// pending failed drain: with the lock held no write is between its forward
// and its drain, so once the drain succeeds Local holds no record the log
// lacks, and closing the shard drops nothing (dynamodb-streams.md 11.1
// item 3). The caller holds the lock until the stream is closed in the
// state. A drain that fails again refuses the call before it reaches
// Local, as the closed shard would otherwise lack those records; the
// table, its stream and the pending records are unchanged.
func (s *Service) closingStream(ctx context.Context, c *kernel.Call, table string) (release func(), err *awserr.Error) {
	scope := c.Scope()
	if _, ok := s.enabledStream(scope, table); !ok {
		return func() {}, nil
	}
	release = s.feedLocks.lock(scope, []string{table})
	if !s.drainFailed(ctx, c, scope, table) {
		release()
		return nil, errLocalUnavailable("copy the stream records DynamoDB Local still holds for table "+table+" before closing its stream, so the call was not sent and the table and stream are unchanged", errDrainFailed)
	}
	return release, nil
}

// errDrainFailed is a drain that failed; drain logs its cause.
var errDrainFailed = errors.New("reading DynamoDB Local's stream failed (the server log has the cause)")

// feedTarget is what a drain needs of a table's enabled stream, copied out
// of the state: its label, which with the table names it, its ARN, the ARN
// of Local's stream, and the feed's position.
type feedTarget struct {
	label    clock.Instant
	arn      aws.ARN
	localARN string
	feed     localFeed
}

// enabledStream is the enabled stream of table in scope as a feedTarget.
func (s *Service) enabledStream(scope aws.Scope, table string) (feedTarget, bool) {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return feedTarget{}, false
	}
	type found struct {
		t   feedTarget
		arn string
		ok  bool
	}
	f := store.Read(cell, func(st *State) found {
		l, ok := st.latestStream(table)
		if !ok || !l.Enabled {
			return found{}
		}
		return found{t: feedTarget{label: l.Label, localARN: l.localARN(), feed: l.log.feed}, arn: l.arn(scope), ok: true}
	})
	if !f.ok {
		return feedTarget{}, false
	}
	arn, err := aws.ParseARN(f.arn)
	if err != nil {
		return feedTarget{}, false
	}
	f.t.arn = arn
	return f.t, true
}

// enabledLog is the log of table's enabled stream labeled label, false when
// that stream is no longer enabled or no longer in the state, as after a
// snapshot load or a reset.
func (st *State) enabledLog(table string, label clock.Instant) (*streamLog, bool) {
	l, ok := st.latestStream(table)
	if !ok || !l.Enabled || l.Label.Compare(label) != 0 {
		return nil, false
	}
	return l.log, true
}

// drain copies the records Local appended to its stream of table in scope
// since the feed's saved position into the log, each stamped with stamp's
// instant, a new eventID and, for a TTL sweeper's delete, userIdentity
// (dynamodb-streams.md 11.1 item 4, 7.5), and wakes the stream's readers.
// The caller holds the table's stream lock.
//
// A drain that fails appends nothing, keeps the stamp of the first write
// whose records it left in Local, is logged, journaled as the stream moving
// from Draining to DrainFailed, and is retried at the next write to the
// table or drainRetry later (dynamodb-streams.md 11.1 item 3); the write's
// answer stays as it was. While a failed drain is pending, every drain
// stamps with the failed write's stamp: Local's records carry nothing that
// tells which of several writes made them. drain reports whether the log
// caught up: false only for a failed drain, true when it appended, or when
// the table has no enabled stream.
func (s *Service) drain(ctx context.Context, c *kernel.Call, scope aws.Scope, table string, stamp writeStamp) (caughtUp bool) {
	t, ok := s.enabledStream(scope, table)
	if !ok {
		return true
	}
	if t.feed.failed != nil {
		stamp = *t.feed.failed
	}
	p, err := s.pull(ctx, c.Admitted(), scope, t.localARN, t.feed)
	if err == nil {
		err = s.appendPulled(c, scope, table, t, p, stamp)
	}
	if err != nil {
		s.kernel.Log().LogAttrs(ctx, slog.LevelWarn, "draining DynamoDB Local's stream failed; its records stay in Local and are drained at the next write to the table or a second of emulator time later",
			slog.String("stream", t.arn.String()), slog.String("error", err.Error()))
		s.failDrain(c, scope, table, t.label, stamp)
		s.kernel.Timers().Scheduler().Arm(s.drains.Key(tableKey{scope: scope, table: table}.path()), s.kernel.Clock().Now().Add(drainRetry))
		if t.feed.failed == nil {
			// Only the first failure is a change of state; a retry that
			// fails again leaves the stream DrainFailed.
			c.Record(ctx, scope, journal.StateChange{Resource: t.arn, From: "Draining", To: "DrainFailed"})
		}
		return false
	}
	if t.feed.failed != nil {
		c.Record(ctx, scope, journal.StateChange{Resource: t.arn, From: "DrainFailed", To: "Draining"})
	}
	return true
}

// appendPulled appends p's records to the log of t, table's enabled
// stream, stamped with stamp, moves the feed's saved position past them and
// clears a pending failed drain, after applying the stream lifetimes
// (State.expire). A stream no longer enabled in the state, as after a
// snapshot load or a reset, takes nothing: a load maps its restored stream
// to Local's tip.
func (s *Service) appendPulled(c *kernel.Call, scope aws.Scope, table string, t feedTarget, p pulled, stamp writeStamp) error {
	_, werr := kernel.Write(c, s.cells, scope, func(state *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		expired := state.expire(now)
		log, ok := state.enabledLog(table, t.label)
		if !ok || len(p.records) == 0 {
			if ok {
				log.feed.shard, log.feed.failed = p.shard, nil
			}
			if !expired {
				tx.Unchanged()
			}
			return struct{}{}, nil
		}
		// An enabled stream's shard is open (disableStream and
		// tableDeleted close it as they disable it), so Append cannot
		// refuse; log.feed.last follows each record appended.
		log.feed.shard = p.shard
		for _, r := range p.records {
			v := r.value
			v.EventID = s.kernel.IDs().StreamEventID()
			v.TTL = stamp.ttl
			if _, err := log.shard.Append(stamp.at, v); err != nil {
				return struct{}{}, awserr.Internalf(err)
			}
			log.feed.last = r.seq
		}
		log.feed.failed = nil
		tx.Wake(shardlog.WaitKey(t.arn))
		return struct{}{}, nil
	})
	if werr != nil {
		return werr.AsError()
	}
	return nil
}

// failDrain keeps stamp as the pending failed drain of table's enabled
// stream labeled label, unless an earlier one is pending.
func (s *Service) failDrain(c *kernel.Call, scope aws.Scope, table string, label clock.Instant, stamp writeStamp) {
	// The feed's position is not saved state, so the write changes nothing
	// a snapshot holds; it only needs the cell's lock.
	_, _ = kernel.Write(c, s.cells, scope, func(state *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		tx.Unchanged()
		if log, ok := state.enabledLog(table, label); ok && log.feed.failed == nil {
			log.feed.failed = &stamp
		}
		return struct{}{}, nil
	})
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

// pull reads Local's stream localARN after feed's position, with requests
// stamped at (pullEach).
func (s *Service) pull(ctx context.Context, at clock.Instant, scope aws.Scope, localARN string, feed localFeed) (pulled, error) {
	var records []pulledRecord
	shard, err := s.pullEach(ctx, at, scope, localARN, feed, func(r *localPageRecord) error {
		v, err := r.value()
		if err != nil {
			return err
		}
		records = append(records, pulledRecord{seq: r.Dynamodb.SequenceNumber, value: v})
		return nil
	})
	if err != nil {
		return pulled{}, err
	}
	return pulled{shard: shard, records: records}, nil
}

// pullEach passes each record of Local's stream localARN after feed's
// position to each, in order, and returns the stream's shard id:
// DescribeStream once to learn the shard, GetShardIterator from the saved
// number (or TRIM_HORIZON before the first record), then GetRecords until a
// page is empty.
func (s *Service) pullEach(ctx context.Context, at clock.Instant, scope aws.Scope, localARN string, feed localFeed, each func(*localPageRecord) error) (string, error) {
	endpoint, ok := s.local.Endpoint()
	if !ok {
		return "", errStopped
	}
	call := func(op *registry.Operation, in, out any) error {
		return s.callLocalStreams(ctx, at, scope, endpoint, op, in, out)
	}
	shard := feed.shard
	if shard == "" {
		var d struct {
			StreamDescription struct {
				Shards []struct {
					ShardID string `json:"ShardId"`
				}
			}
		}
		if err := call(dynamodbstreamsapi.OpDescribeStream.Descriptor(), map[string]string{"StreamArn": localARN}, &d); err != nil {
			return "", err
		}
		shards := d.StreamDescription.Shards
		if len(shards) == 0 {
			return "", &AnswerError{Op: dynamodbstreamsapi.OpDescribeStream.Descriptor().ID, Reason: "the stream " + localARN + " has no shard"}
		}
		shard = shards[len(shards)-1].ShardID
	}
	type iteratorIn struct {
		StreamArn         string
		ShardID           string `json:"ShardId"`
		ShardIteratorType string
		SequenceNumber    string `json:",omitempty"`
	}
	in := iteratorIn{StreamArn: localARN, ShardID: shard, ShardIteratorType: "TRIM_HORIZON"}
	if feed.last != "" {
		in.ShardIteratorType, in.SequenceNumber = "AFTER_SEQUENCE_NUMBER", feed.last
	}
	var it struct{ ShardIterator *string }
	if err := call(dynamodbstreamsapi.OpGetShardIterator.Descriptor(), in, &it); err != nil {
		return "", err
	}
	for next := it.ShardIterator; next != nil; {
		type recordsIn struct {
			ShardIterator string
			Limit         int
		}
		var page localPage
		if err := call(dynamodbstreamsapi.OpGetRecords.Descriptor(), recordsIn{ShardIterator: *next, Limit: drainPage}, &page); err != nil {
			return "", err
		}
		if len(page.Records) == 0 {
			break
		}
		for i := range page.Records {
			if err := each(&page.Records[i]); err != nil {
				return "", err
			}
		}
		next = page.NextShardIterator
	}
	return shard, nil
}

// localTip is feed moved to the end of Local's stream localARN: its shard
// and the number of its last record, which Local's stream may not have
// yet (an empty last then reads from TRIM_HORIZON). A snapshot load maps a
// restored stream to the tip of the table's new Local stream, so the
// load's own writes emit nothing (ADR 0009 amendment of 2026-10-27).
func (s *Service) localTip(ctx context.Context, at clock.Instant, scope aws.Scope, localARN string, feed localFeed) (localFeed, error) {
	shard, err := s.pullEach(ctx, at, scope, localARN, feed, func(r *localPageRecord) error {
		feed.last = r.Dynamodb.SequenceNumber
		return nil
	})
	if err != nil {
		return localFeed{}, err
	}
	feed.shard = shard
	return feed, nil
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
// as its body, under scope's credential signed at at, and decodes its
// answer into out: an error answer is a *LocalError, one that does not
// decode an *AnswerError. The feed's reads are understudy reading its own
// backend, not a principal's call, so they skip the pipeline.
func (s *Service) callLocalStreams(ctx context.Context, at clock.Instant, scope aws.Scope, endpoint *url.URL, op *registry.Operation, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, aerr := s.send(ctx, endpoint, exchange{scope: scope, op: op.ID, at: at}, body)
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
