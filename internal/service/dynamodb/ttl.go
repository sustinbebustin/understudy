package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Time to live runs in the shim on the emulator clock; Local never learns
// the specification, so its wall-clock sweeper stays off (dynamodb-local.md
// 5.2, Implications 6).
//
// Each table with TTL enabled has one sweep timer, armed for the earliest
// expiry the shim knows of. A sweep due at T scans for items whose TTL
// attribute lies in T's window, from five years before T up to T
// (expiryFloor), following LastEvaluatedKey,
// deletes each with DeleteItem conditioned on the attribute still lying
// there, so a refresh that lands first wins, then scans for the earliest
// expiry after T and arms the timer for it. A write that can set an
// earlier expiry lowers the timer to that expiry, or to its admission
// instant when the expiry has passed: PutItem and the puts of
// BatchWriteItem and TransactWriteItems by the item they put, UpdateItem
// and a transaction's updates by the item's expiry read back after Local
// accepts the write, and PartiQL INSERT and UPDATE, whose keys the shim
// cannot know, by their admission instant. So an item is deleted at
// exactly the instant its TTL attribute names, under Advance as under a
// running clock. The sweep's calls run through the pipeline as
// dynamodb.amazonaws.com, with Origin Internal{Via: DynamoDBTTL} and the
// timer's due instant; an update's read-back is part of the write's own
// handling and reaches Local directly (readExpiry).

// ttlTimer is the kind of the sweep timers.
const ttlTimer kernel.TimerKind = "dynamodb.ttl"

// ttlPrincipal is the service principal TTL deletes run as; AWS's stream
// records name it in userIdentity (dynamodb-local.md 5.1).
const ttlPrincipal = "dynamodb.amazonaws.com"

// sweepRetry is how long after a failed sweep the next one is due, in
// emulator time; the items stay until a sweep succeeds.
const sweepRetry = time.Minute

func (s *Service) updateTimeToLive(ctx context.Context, c *kernel.Call, in *dynamodbapi.UpdateTimeToLiveInput) (smithy.Serializable, *awserr.Error) {
	a := tableAnswer(in.TableName)
	if err := inScope(c.Scope(), []string{in.TableName}, a); err != nil {
		return nil, err
	}
	name := tableName(in.TableName)
	missing := dynamodbapi.ErrResourceNotFoundException(awsNotFound(name))
	if _, err := s.recorded(ctx, c, name, missing); err != nil {
		return nil, err
	}
	spec := in.TimeToLiveSpecification
	_, err := s.alter(c, name, missing, func(t *Table, _ clock.Instant) *awserr.Error {
		switch {
		case spec.Enabled && t.TTL != nil && t.TTL.Attribute == spec.AttributeName:
			return dynamodbapi.ErrValidationException("TimeToLive is already enabled")
		case spec.Enabled && t.TTL != nil:
			return dynamodbapi.ErrValidationException("TimeToLive is active on a different AttributeName: current AttributeName is " + t.TTL.Attribute)
		case spec.Enabled:
			t.TTL = &TTLSpec{Attribute: spec.AttributeName}
		case t.TTL == nil:
			return dynamodbapi.ErrValidationException("TimeToLive is already disabled")
		default:
			t.TTL = nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	key := tableKey{scope: c.Scope(), table: name}
	if spec.Enabled {
		s.sweeps.lower(key, c.Admitted())
	} else {
		s.sweeps.forget(key)
	}
	return &dynamodbapi.UpdateTimeToLiveOutput{TimeToLiveSpecification: &spec}, nil
}

// describeTimeToLive leaves AttributeName out while TTL is disabled, as AWS
// does (dynamodb-local.md 5.1).
func (s *Service) describeTimeToLive(ctx context.Context, c *kernel.Call, in *dynamodbapi.DescribeTimeToLiveInput) (smithy.Serializable, *awserr.Error) {
	a := tableAnswer(in.TableName)
	if err := inScope(c.Scope(), []string{in.TableName}, a); err != nil {
		return nil, err
	}
	name := tableName(in.TableName)
	t, err := s.recorded(ctx, c, name, dynamodbapi.ErrResourceNotFoundException(awsNotFound(name)))
	if err != nil {
		return nil, err
	}
	desc := &dynamodbapi.TimeToLiveDescription{TimeToLiveStatus: new(dynamodbapi.TimeToLiveStatusDisabled)}
	if t.TTL != nil {
		desc = &dynamodbapi.TimeToLiveDescription{TimeToLiveStatus: new(dynamodbapi.TimeToLiveStatusEnabled), AttributeName: new(t.TTL.Attribute)}
	}
	return &dynamodbapi.DescribeTimeToLiveOutput{TimeToLiveDescription: desc}, nil
}

// sweeper holds the sweep timers. The due instant of each armed timer is
// kept beside the scheduler, which cannot be asked, so a write lowers a
// timer only when its expiry is earlier. It is not state: Rearm rebuilds
// it, as the timers themselves, from the tables with TTL enabled.
type sweeper struct {
	sched *clock.Scheduler
	kind  kernel.HandledKind

	mu  sync.Mutex
	due map[tableKey]clock.Instant
	// waiting are the admin sweeps (SweepTTL) waiting for the next sweep of
	// a table to finish.
	waiting map[tableKey][]*round
}

// round is one admin sweep: the tables it waits for and what their sweeps
// deleted.
type round struct {
	pending int
	deleted int
	errs    []error
	done    chan struct{}
}

func newSweeper(sched *clock.Scheduler, kind kernel.HandledKind) *sweeper {
	return &sweeper{sched: sched, kind: kind, due: map[tableKey]clock.Instant{}, waiting: map[tableKey][]*round{}}
}

// lower arms k's sweep for at unless it is already due no later.
func (w *sweeper) lower(k tableKey, at clock.Instant) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cur, ok := w.due[k]; ok && !at.Before(cur) {
		return
	}
	w.due[k] = at
	w.sched.Arm(w.kind.Key(k.path()), at)
}

// forget disarms k's sweep, for a table whose TTL was disabled or which was
// deleted; an admin sweep waiting for it counts it as swept.
func (w *sweeper) forget(k tableKey) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.due, k)
	w.sched.Disarm(w.kind.Key(k.path()))
	w.finishLocked(w.waiting[k], 0, nil)
	delete(w.waiting, k)
}

// fired takes k's timer, due at at, off the books and returns the admin
// sweeps the sweep it starts will finish.
func (w *sweeper) fired(k tableKey, at clock.Instant) []*round {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cur, ok := w.due[k]; ok && cur.Compare(at) == 0 {
		delete(w.due, k)
	}
	rs := w.waiting[k]
	delete(w.waiting, k)
	return rs
}

// finish reports one sweep to the admin sweeps that waited for it.
func (w *sweeper) finish(rs []*round, deleted int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.finishLocked(rs, deleted, err)
}

func (w *sweeper) finishLocked(rs []*round, deleted int, err error) {
	for _, r := range rs {
		r.deleted += deleted
		if err != nil {
			r.errs = append(r.errs, err)
		}
		r.pending--
		if r.pending == 0 {
			close(r.done)
		}
	}
}

// rearm rebuilds the timers after the scheduler was cleared: a sweep due
// at now for every table in enabled, which finds the next expiry itself.
// Admin sweeps waiting for a table no longer listed count it as swept.
func (w *sweeper) rearm(enabled []tableKey, now clock.Instant) {
	w.mu.Lock()
	defer w.mu.Unlock()
	clear(w.due)
	listed := make(map[tableKey]bool, len(enabled))
	for _, k := range enabled {
		listed[k] = true
		w.due[k] = now
		w.sched.Arm(w.kind.Key(k.path()), now)
	}
	for k, rs := range w.waiting {
		if !listed[k] {
			w.finishLocked(rs, 0, nil)
			delete(w.waiting, k)
		}
	}
}

// await sweeps the tables of keys at now and waits for every sweep to
// finish, returning the items deleted.
func (w *sweeper) await(ctx context.Context, keys []tableKey, now clock.Instant) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	r := &round{pending: len(keys), done: make(chan struct{})}
	w.mu.Lock()
	for _, k := range keys {
		w.waiting[k] = append(w.waiting[k], r)
	}
	w.mu.Unlock()
	for _, k := range keys {
		w.lower(k, now)
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		return 0, fmt.Errorf("waiting for the TTL sweeps of %d tables: %w", len(keys), ctx.Err())
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return r.deleted, errors.Join(r.errs...)
}

// SweepTTL runs the TTL sweep of every table now, as the admin API's
// POST /_understudy/v1/dynamodb/ttl-sweep does, and returns the items it
// deleted. The sweeps run as the timer's would, through the pipeline.
func (s *Service) SweepTTL(ctx context.Context) (int, error) {
	return s.sweeps.await(ctx, s.enabledTTL(), s.kernel.Clock().Now())
}

// enabledTTL lists the tables with TTL enabled, in path order, so timers
// armed together fire in the same order on every run.
func (s *Service) enabledTTL() []tableKey {
	var keys []tableKey
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		keys = append(keys, store.Read(cell, func(st *State) []tableKey {
			var out []tableKey
			for _, name := range slices.Sorted(maps.Keys(st.Tables)) {
				if st.Tables[name].TTL != nil {
					out = append(out, tableKey{scope: scope, table: name})
				}
			}
			return out
		})...)
	}
	return keys
}

// onSweep is the sweep timer's handler: it starts the sweep as tracked
// work, which never blocks the clock.
func (s *Service) onSweep(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	k, err := parseTableKey(key.ID)
	if err != nil {
		s.kernel.Log().LogAttrs(context.Background(), slog.LevelError, "dropped a DynamoDB TTL sweep timer", slog.String("error", err.Error()))
		return
	}
	rounds := s.sweeps.fired(k, due)
	t, ok := s.lookup(k.scope, k.table)
	if !ok || t.TTL == nil {
		s.sweeps.finish(rounds, 0, nil)
		return
	}
	attribute := t.TTL.Attribute
	s.kernel.Go(kernel.InvocationRunning, func(ctx context.Context) {
		deleted, err := s.sweep(ctx, c, k, attribute, due)
		if err != nil {
			s.kernel.Log().LogAttrs(ctx, slog.LevelWarn, "a DynamoDB TTL sweep failed; it runs again a minute of emulator time later",
				slog.String("table", tableARN(k.scope, k.table).String()), slog.Int("deleted", deleted), slog.String("error", err.Error()))
			s.sweeps.lower(k, due.Add(sweepRetry))
		}
		s.sweeps.finish(rounds, deleted, err)
	})
}

// sweep deletes the items of k whose attribute lies in due's window, then
// arms the next sweep for the earliest later expiry. A table deleted
// meanwhile ends it quietly.
func (s *Service) sweep(ctx context.Context, parent *kernel.Call, k tableKey, attribute string, due clock.Instant) (int, error) {
	x := invoker{s: s, parent: parent, target: kernel.Target{
		Scope: k.scope, Via: ref.DynamoDBTTL, At: due,
		As: kernel.AsService{Name: ttlPrincipal, Source: tableARN(k.scope, k.table)},
	}}
	var described dynamodbapi.DescribeTableOutput
	if err := x.describe(ctx, k.table, &described); err != nil {
		return 0, gone(err)
	}
	if described.Table == nil {
		return 0, &AnswerError{Op: dynamodbapi.OpDescribeTableRaw.Descriptor().ID, Reason: "the answer has no Table"}
	}
	keys := make([]string, 0, len(described.Table.KeySchema))
	for _, e := range described.Table.KeySchema {
		keys = append(keys, e.AttributeName)
	}
	expired := windowAt(due)
	deleted := 0
	err := x.scan(ctx, expiredScan(k.table, attribute, keys, expired), func(item map[string]dynamodbapi.AttributeValue) error {
		ok, err := x.deleteExpired(ctx, k.table, attribute, keyOf(item, keys), expired)
		if ok {
			deleted++
		}
		return err
	})
	if err != nil {
		return deleted, gone(err)
	}
	var next *clock.Instant
	err = x.scan(ctx, pendingScan(k.table, attribute, expired.now), func(item map[string]dynamodbapi.AttributeValue) error {
		if at, ok := expiryOf(item[attribute], due); ok && (next == nil || at.Before(*next)) {
			next = &at
		}
		return nil
	})
	if err != nil {
		return deleted, gone(err)
	}
	if next != nil {
		s.sweeps.lower(k, *next)
	}
	return deleted, nil
}

// gone drops the error of a sweep whose table was deleted under it.
func gone(err error) error {
	if le, ok := errors.AsType[*LocalError](err); ok && le.is("ResourceNotFoundException") {
		return nil
	}
	return err
}

// expiryFloor is the oldest TTL value that still expires at at: "To be
// considered for expiry and deletion, the TTL can't be more
// than five years in the past" (docs.aws.amazon.com/amazondynamodb/latest/
// developerguide/time-to-live-ttl-before-you-start.html). The five years
// are calendar years back from the instant (AddDate), so a leap day never
// moves the bound; an item older than that is kept, such as a lock
// released by setting its expiry to 0.
func expiryFloor(at clock.Instant) clock.Instant {
	return clock.FromWire(at.Wire().AddDate(-5, 0, 0))
}

// window is the TTL values a sweep at one instant deletes, as DynamoDB
// Numbers: from five years before it (expiryFloor) up to it.
type window struct{ floor, now string }

func windowAt(at clock.Instant) window {
	return window{floor: epochText(expiryFloor(at)), now: epochText(at)}
}

// expiredCondition holds for an item whose TTL attribute, #ttl, lies in
// the window :floor to :now.
const expiredCondition = "#ttl BETWEEN :floor AND :now"

func (w window) values() map[string]dynamodbapi.AttributeValue {
	return map[string]dynamodbapi.AttributeValue{
		":floor": dynamodbapi.AttributeValueMemberN{Value: w.floor},
		":now":   dynamodbapi.AttributeValueMemberN{Value: w.now},
	}
}

// expiredScan reads the key and expiry of every item in the window. A key
// attribute that is the TTL attribute is projected once, as "#ttl": Local
// refuses a projection whose paths overlap.
func expiredScan(table, attribute string, keys []string, expired window) *dynamodbapi.ScanInput {
	names := map[string]string{"#ttl": attribute}
	projection := make([]string, 0, 1+len(keys))
	projection = append(projection, "#ttl")
	for i, k := range keys {
		if k == attribute {
			continue
		}
		ph := fmt.Sprintf("#k%d", i)
		names[ph] = k
		projection = append(projection, ph)
	}
	return &dynamodbapi.ScanInput{
		TableName: table, ConsistentRead: new(true),
		FilterExpression:          new(expiredCondition),
		ProjectionExpression:      new(strings.Join(projection, ", ")),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: expired.values(),
	}
}

// pendingScan reads the expiry of every item that expires after now.
func pendingScan(table, attribute, now string) *dynamodbapi.ScanInput {
	return &dynamodbapi.ScanInput{
		TableName: table, ConsistentRead: new(true),
		FilterExpression:          new("#ttl > :now"),
		ProjectionExpression:      new("#ttl"),
		ExpressionAttributeNames:  map[string]string{"#ttl": attribute},
		ExpressionAttributeValues: map[string]dynamodbapi.AttributeValue{":now": dynamodbapi.AttributeValueMemberN{Value: now}},
	}
}

// keyOf is the primary key of item.
func keyOf(item map[string]dynamodbapi.AttributeValue, keys []string) map[string]dynamodbapi.AttributeValue {
	out := make(map[string]dynamodbapi.AttributeValue, len(keys))
	for _, k := range keys {
		out[k] = item[k]
	}
	return out
}

// expiryOf is the instant a TTL attribute value expires at, seen at now:
// its epochInstant, unless that lies more than five years before now
// (expiryFloor), which never expires.
func expiryOf(v dynamodbapi.AttributeValue, now clock.Instant) (clock.Instant, bool) {
	at, ok := epochInstant(v)
	if !ok || at.Before(expiryFloor(now)) {
		return clock.Instant{}, false
	}
	return at, true
}

// epochInstant is the instant a TTL attribute value names: a Number of
// Unix epoch seconds, rounded up to the nanosecond so that a sweep at that
// instant finds it expired. Any other value, and a number past the year
// 9999, names none (dynamodb-local.md 5.1).
func epochInstant(v dynamodbapi.AttributeValue) (clock.Instant, bool) {
	n, ok := v.(dynamodbapi.AttributeValueMemberN)
	if !ok {
		return clock.Instant{}, false
	}
	r, ok := new(big.Rat).SetString(n.Value)
	if !ok || r.Cmp(big.NewRat(endExpirySeconds, 1)) >= 0 || r.Cmp(big.NewRat(minExpirySeconds, 1)) < 0 {
		return clock.Instant{}, false
	}
	nanos := new(big.Rat).Mul(r, big.NewRat(int64(time.Second), 1))
	ceil := new(big.Int).Quo(nanos.Num(), nanos.Denom())
	if nanos.Sign() > 0 && !nanos.IsInt() {
		ceil.Add(ceil, big.NewInt(1))
	}
	sec, rem := new(big.Int).QuoRem(ceil, big.NewInt(int64(time.Second)), new(big.Int))
	return clock.FromWire(time.Unix(sec.Int64(), rem.Int64())), true
}

// The epoch seconds an expiry may name: years 1 to 9999, which an Instant
// holds; endExpirySeconds is the first second past them.
const (
	minExpirySeconds = -62135596800 // 0001-01-01T00:00:00Z
	endExpirySeconds = 253402300800 // 10000-01-01T00:00:00Z
)

// epochText is i as a DynamoDB Number of Unix epoch seconds, exact to the
// nanosecond.
func epochText(i clock.Instant) string {
	t := i.Wire()
	r := big.NewRat(t.Unix(), 1)
	r.Add(r, big.NewRat(int64(t.Nanosecond()), int64(time.Second)))
	s := strings.TrimRight(r.FloatString(9), "0")
	return strings.TrimSuffix(s, ".")
}

// written is one item a write leaves in a table.
type written struct {
	table string
	item  writtenItem
}

// writtenItem is what the shim knows of a written item.
//
//sumtype:decl
type writtenItem interface{ isWrittenItem() }

// putItem is the whole item a put wrote.
type putItem map[string]dynamodbapi.AttributeValue

// updatedKey is the key of an item an update expression changed, whose
// result the shim reads back from Local.
type updatedKey map[string]dynamodbapi.AttributeValue

func (putItem) isWrittenItem()    {}
func (updatedKey) isWrittenItem() {}

// ttlWrites wraps the handler of an item write: once Local accepts it, the
// sweep of each table it wrote that has TTL enabled is lowered to the
// expiry the write left on each item.
func ttlWrites[In any](s *Service, h kernel.RawHandler[In], writes func(*In) []written) kernel.RawHandler[In] {
	return func(ctx context.Context, c *kernel.Call, in *In, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
		resp, err := h(ctx, c, in, w)
		if err == nil && resp.Status == http.StatusOK {
			s.watch(ctx, c, writes(in))
		}
		return resp, err
	}
}

// watch lowers the sweep of each written table with TTL enabled to the
// expiry its item now has, or to c's admission instant when that has
// passed. An update whose item cannot be read back sweeps the table at
// c's admission instant, which finds whatever the update set.
func (s *Service) watch(ctx context.Context, c *kernel.Call, ws []written) {
	for _, w := range ws {
		name := tableName(w.table)
		t, ok := s.lookup(c.Scope(), name)
		if !ok || t.TTL == nil {
			continue
		}
		k := tableKey{scope: c.Scope(), table: name}
		var value dynamodbapi.AttributeValue
		switch it := w.item.(type) {
		case putItem:
			value = it[t.TTL.Attribute]
		case updatedKey:
			v, err := s.readExpiry(ctx, c, k, t.TTL.Attribute, it)
			if err != nil {
				s.sweeps.lower(k, c.Admitted())
				continue
			}
			value = v
		}
		at, ok := expiryOf(value, c.Admitted())
		if !ok {
			continue
		}
		if at.Before(c.Admitted()) {
			at = c.Admitted()
		}
		s.sweeps.lower(k, at)
	}
}

// readExpiry reads the TTL attribute of the item key of k back from Local,
// consistently; nil when the item or the attribute is gone. The read is
// part of c's own handling, as AWS makes none, so it reaches Local
// directly: it never matches a fault rule meant for a client's GetItem,
// and the journal does not record it.
func (s *Service) readExpiry(ctx context.Context, c *kernel.Call, k tableKey, attribute string, key updatedKey) (dynamodbapi.AttributeValue, error) {
	get := dynamodbapi.OpGetItemRaw.Descriptor()
	body, aerr := encodeInput(get, &dynamodbapi.GetItemInput{
		TableName: k.table, Key: key, ConsistentRead: new(true),
		ProjectionExpression: new("#ttl"), ExpressionAttributeNames: map[string]string{"#ttl": attribute},
	})
	if aerr != nil {
		return nil, aerr.AsError()
	}
	endpoint, aerr := s.ready(ctx)
	if aerr != nil {
		return nil, aerr.AsError()
	}
	resp, aerr := s.send(ctx, endpoint, exchange{scope: k.scope, op: get.ID, at: c.Admitted()}, body)
	if aerr != nil {
		return nil, aerr.AsError()
	}
	var out dynamodbapi.GetItemOutput
	if err := decodeAnswer(get, resp, &out); err != nil {
		return nil, err
	}
	return out.Item[attribute], nil
}

// ttlStatements wraps the handler of a PartiQL operation, whose tables are
// inside its statements: once Local accepts one that inserts or updates,
// every table of the scope with TTL enabled is swept.
func ttlStatements[In any](s *Service, h kernel.RawHandler[In], statements func(*In) []string) kernel.RawHandler[In] {
	return func(ctx context.Context, c *kernel.Call, in *In, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
		resp, err := h(ctx, c, in, w)
		if err != nil || resp.Status != http.StatusOK || !writesItems(statements(in)) {
			return resp, err
		}
		for _, k := range s.enabledTTL() {
			if k.scope == c.Scope() {
				s.sweeps.lower(k, c.Admitted())
			}
		}
		return resp, err
	}
}

// writesItems reports whether a statement can set an attribute: an INSERT
// or an UPDATE, whatever white space follows the verb.
func writesItems(statements []string) bool {
	for _, st := range statements {
		words := strings.Fields(st)
		if len(words) == 0 {
			continue
		}
		if strings.EqualFold(words[0], "INSERT") || strings.EqualFold(words[0], "UPDATE") {
			return true
		}
	}
	return false
}

func putItemWrites(in *dynamodbapi.PutItemInput) []written {
	return []written{{table: in.TableName, item: putItem(in.Item)}}
}

func updateItemWrites(in *dynamodbapi.UpdateItemInput) []written {
	return []written{{table: in.TableName, item: updatedKey(in.Key)}}
}

func batchWrites(in *dynamodbapi.BatchWriteItemInput) []written {
	var out []written
	for table, requests := range in.RequestItems {
		for _, r := range requests {
			if r.PutRequest != nil {
				out = append(out, written{table: table, item: putItem(r.PutRequest.Item)})
			}
		}
	}
	return out
}

func transactWrites(in *dynamodbapi.TransactWriteItemsInput) []written {
	var out []written
	for _, it := range in.TransactItems {
		switch {
		case it.Put != nil:
			out = append(out, written{table: it.Put.TableName, item: putItem(it.Put.Item)})
		case it.Update != nil:
			out = append(out, written{table: it.Update.TableName, item: updatedKey(it.Update.Key)})
		}
	}
	return out
}

func statementOf(in *dynamodbapi.ExecuteStatementInput) []string { return []string{in.Statement} }

func batchStatements(in *dynamodbapi.BatchExecuteStatementInput) []string {
	out := make([]string, 0, len(in.Statements))
	for _, st := range in.Statements {
		out = append(out, st.Statement)
	}
	return out
}

func transactionStatements(in *dynamodbapi.ExecuteTransactionInput) []string {
	out := make([]string, 0, len(in.TransactStatements))
	for _, st := range in.TransactStatements {
		out = append(out, st.Statement)
	}
	return out
}
