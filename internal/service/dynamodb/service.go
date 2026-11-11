package dynamodb

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is Amazon DynamoDB: a proxy to DynamoDB Local with one shim state
// cell per account and region.
type Service struct {
	local  Local
	cells  *store.Partitioned[aws.Scope, State]
	kernel *kernel.Kernel
	// sweeps are the TTL sweep timers.
	sweeps *sweeper
	// streamSeq is the dynamodb.streams.sequence-number switch.
	streamSeq StreamSequence
	// iteratorReuse is the dynamodb.streams.iterator-reuse switch.
	iteratorReuse IteratorReuse
	// feedLocks are the stream locks of the tables, each held across a
	// write and the drain that follows it (dynamodb-streams.md 11.1 item 2).
	feedLocks feedLocks
	// drains are the timers that retry a failed drain.
	drains kernel.HandledKind
	// relinks are the tables the last ImportLate re-created in Local with a
	// stream, for the Import that follows it to map (State.relink).
	relinks relinks
}

// The service is the snapshot and reset view of its shim state.
var _ kernel.Stateful = (*Service)(nil)

// New binds every operation DynamoDB Local serves and those the shims
// answer. The rest, Local's gaps, stay unbound: they answer 501 before
// Local sees them, and coverage lists them.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{
		local:         o.Local,
		cells:         store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState),
		kernel:        d.Kernel,
		streamSeq:     o.StreamSequence,
		iteratorReuse: o.IteratorReuse,
	}
	if s.local == nil {
		s.local = noLocal{}
	}
	timers := d.Kernel.Timers()
	s.sweeps = newSweeper(timers.Scheduler(), timers.Handle(ttlTimer, s.onSweep))
	s.drains = timers.Handle(drainTimer, s.onDrainRetry)
	b := dynamodbapi.NewBinder(d.Binder)
	s.bindTables(b)
	s.bindItems(b)
	s.bindStatements(b)
	s.bindShims(b)
	declareActions(d.Binder)
	return s
}

// Coverage notes of Local's gaps (dynamodb-local.md 4), each an aws-differs
// entry in the coverage report (ADR 0014). A note that offers a fault rule
// names only errors the operations it is attached to send, as the rule's
// shape must be written, so the rule it describes is one the fault store
// accepts for them. ThrottlingException takes its full id: DynamoDB's
// operations send both the modeled one and Coral's generic one.
const (
	noThroughput = "aws-differs: dynamodb-local: provisioned throughput is never enforced; inject ProvisionedThroughputExceededException or " +
		"com.amazonaws.dynamodb#ThrottlingException with a fault rule"
	// noThrottling is noThroughput for an operation that never sends
	// ProvisionedThroughputExceededException.
	noThrottling = "aws-differs: dynamodb-local: requests are never throttled; inject com.amazonaws.dynamodb#ThrottlingException with a fault rule"
	noConflicts  = "aws-differs: dynamodb-local: a write never conflicts with a transaction in progress; inject TransactionConflictException with a fault rule"
	// noTxConflicts is how a conflict shows on the transaction operations,
	// which do not send TransactionConflictException. A fault rule sets the
	// reasons through its members, one per item of the call, which the
	// bindings count (smithy-protocol-tests.md 14.9, 14.10). The injected
	// error's message key, None reasons and ExecuteTransaction form are
	// AWS's, recorded (12.4: PROTO-01, PROTO-02 and PROTO-05).
	noTxConflicts = "aws-differs: dynamodb-local: transactions never conflict; AWS cancels a conflicting transaction with TransactionCanceledException " +
		"and the cancellation reason TransactionConflict; inject it with a fault rule whose members set CancellationReasons " +
		"(smithy-protocol-tests.md 12.4)"
	caseTwins      = "aws-differs: dynamodb-local: table names are case-insensitive, so a name that differs from an existing table's only in case is refused with ResourceInUseException"
	statementTwins = "aws-differs: dynamodb-local: table names inside statements resolve case-insensitively, so a statement naming a case twin of a table acts on that table"
	parallelScan   = "aws-differs: dynamodb-local: Segment and TotalSegments are ignored, so a parallel scan reads sequentially"
	statementLimit = "aws-differs: dynamodb-local: Limit is ignored"
)

// notes joins coverage notes into one Partial.
func notes(ns ...string) registry.Partial { return registry.Partial{Note: strings.Join(ns, "; ")} }

func (s *Service) bindTables(b dynamodbapi.Binder) {
	createTable := named(func(in *dynamodbapi.CreateTableInput) string { return in.TableName })
	b.CreateTableRaw(s.createTable, notes(caseTwins), tables(createTable))
	deleteTable := named(func(in *dynamodbapi.DeleteTableInput) string { return in.TableName })
	b.DeleteTableRaw(s.deleteTable, registry.Full{}, tables(deleteTable))
	describeTable := named(func(in *dynamodbapi.DescribeTableInput) string { return in.TableName })
	b.DescribeTableRaw(s.describeTable, registry.Full{}, tables(describeTable))
	updateTable := named(func(in *dynamodbapi.UpdateTableInput) string { return in.TableName })
	// The stream enable and disable refusals carry AWS's texts
	// (dynamodb-streams.md 5.1; DDBS-06).
	b.UpdateTableRaw(s.updateTable, registry.Full{}, tables(updateTable))
	b.ListTablesRaw(passthrough(s, noTables, plainAnswer[dynamodbapi.ListTablesInput]), registry.Full{})
	b.DescribeLimitsRaw(passthrough(s, noTables, plainAnswer[dynamodbapi.DescribeLimitsInput]), registry.Full{})
}

func (s *Service) bindItems(b dynamodbapi.Binder) {
	getItem := named(func(in *dynamodbapi.GetItemInput) string { return in.TableName })
	b.GetItemRaw(passthrough(s, getItem, echoed(getItem)), notes(noThroughput), tables(getItem))
	putItem := named(func(in *dynamodbapi.PutItemInput) string { return in.TableName })
	b.PutItemRaw(ttlWrites(s, fed(s, passthrough(s, putItem, echoed(putItem)), putItem), putItemWrites), notes(noThroughput, noConflicts), tables(putItem))
	updateItem := named(func(in *dynamodbapi.UpdateItemInput) string { return in.TableName })
	b.UpdateItemRaw(ttlWrites(s, fed(s, passthrough(s, updateItem, echoed(updateItem)), updateItem), updateItemWrites), notes(noThroughput, noConflicts), tables(updateItem))
	deleteItem := named(func(in *dynamodbapi.DeleteItemInput) string { return in.TableName })
	b.DeleteItemRaw(fed(s, passthrough(s, deleteItem, echoed(deleteItem)), deleteItem), notes(noThroughput, noConflicts), tables(deleteItem))
	query := named(func(in *dynamodbapi.QueryInput) string { return in.TableName })
	b.QueryRaw(passthrough(s, query, echoed(query)), notes(noThroughput), tables(query))
	scan := named(func(in *dynamodbapi.ScanInput) string { return in.TableName })
	b.ScanRaw(passthrough(s, scan, echoed(scan)), notes(noThroughput, parallelScan), tables(scan))
	batchGet := func(in *dynamodbapi.BatchGetItemInput) []string { return slices.Sorted(maps.Keys(in.RequestItems)) }
	b.BatchGetItemRaw(passthrough(s, batchGet, echoed(batchGet, "Responses", "UnprocessedKeys")), notes(noThroughput), tables(batchGet))
	batchWrite := func(in *dynamodbapi.BatchWriteItemInput) []string { return slices.Sorted(maps.Keys(in.RequestItems)) }
	b.BatchWriteItemRaw(ttlWrites(s, fed(s, passthrough(s, batchWrite, echoed(batchWrite, "UnprocessedItems", "ItemCollectionMetrics")), batchWrite), batchWrites),
		notes(noThroughput), tables(batchWrite))
	// A cancellation reason lines up with each requested item
	// (smithy-protocol-tests.md 12.2), so fault matching counts them.
	b.TransactGetItemsRaw(passthrough(s, transactGetTables, echoed(transactGetTables)), notes(noThroughput, noTxConflicts), tables(transactGetTables),
		kernel.WithItemCount(func(in *dynamodbapi.TransactGetItemsInput) int { return len(in.TransactItems) }))
	b.TransactWriteItemsRaw(ttlWrites(s, fed(s, passthrough(s, transactWriteTables, echoed(transactWriteTables, "ItemCollectionMetrics")), transactWriteTables), transactWrites),
		notes(noThroughput, noTxConflicts, transactPutAlways), tables(transactWriteTables),
		kernel.WithItemCount(func(in *dynamodbapi.TransactWriteItemsInput) int { return len(in.TransactItems) }))
}

// bindStatements binds PartiQL, whose tables are inside the statements.
func (s *Service) bindStatements(b dynamodbapi.Binder) {
	b.ExecuteStatementRaw(ttlStatements(s, fedScope(s, passthrough(s, noTables, plainAnswer[dynamodbapi.ExecuteStatementInput])), statementOf),
		notes(noThroughput, noConflicts, statementLimit, statementTwins, partiQLSelectAlways))
	b.BatchExecuteStatementRaw(ttlStatements(s, fedScope(s, passthrough(s, noTables, plainAnswer[dynamodbapi.BatchExecuteStatementInput])), batchStatements),
		notes(noThrottling, statementTwins, partiQLSelectAlways))
	b.ExecuteTransactionRaw(ttlStatements(s, fedScope(s, passthrough(s, noTables, plainAnswer[dynamodbapi.ExecuteTransactionInput])), transactionStatements),
		notes(noThroughput, noTxConflicts, statementTwins, partiQLSelectAlways),
		kernel.WithItemCount(func(in *dynamodbapi.ExecuteTransactionInput) int { return len(in.TransactStatements) }))
}

// named lists the one table a TableName member addresses.
func named[In any](name func(*In) string) func(*In) []string {
	return func(in *In) []string { return []string{name(in)} }
}

func transactGetTables(in *dynamodbapi.TransactGetItemsInput) []string {
	out := make([]string, 0, len(in.TransactItems))
	for _, it := range in.TransactItems {
		out = append(out, it.Get.TableName)
	}
	return out
}

func transactWriteTables(in *dynamodbapi.TransactWriteItemsInput) []string {
	out := make([]string, 0, len(in.TransactItems))
	for _, it := range in.TransactItems {
		switch {
		case it.Put != nil:
			out = append(out, it.Put.TableName)
		case it.Update != nil:
			out = append(out, it.Update.TableName)
		case it.Delete != nil:
			out = append(out, it.Delete.TableName)
		case it.ConditionCheck != nil:
			out = append(out, it.ConditionCheck.TableName)
		}
	}
	return out
}

// createTable refuses a name Local would confuse with an existing table,
// forwards the rest and records a table Local created, with what Local
// drops from the request (newTable), which its answer then describes. The
// answer says CREATING, as AWS's always does, where Local's says ACTIVE;
// DescribeTable then reports Local's ACTIVE, which the SDK waiters and
// Terraform wait for.
func (s *Service) createTable(ctx context.Context, c *kernel.Call, in *dynamodbapi.CreateTableInput, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
	name := tableName(in.TableName)
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		if twins := store.Read(cell, func(st *State) []string { return st.caseTwins(name) }); len(twins) > 0 {
			return kernel.RawResponse{}, errCaseTwin(name, twins[0])
		}
	}
	if err := preForward(c); err != nil {
		return kernel.RawResponse{}, err
	}
	record, err := s.creation(ctx, c, in)
	if err != nil {
		return kernel.RawResponse{}, err
	}
	a := tableAnswer(in.TableName)
	a.shim = &shimmed{table: &record, of: createAnswer}
	var minted *Stream
	if e, ok := streamChangeOf(in.StreamSpecification).(streamEnabled); ok {
		minted = new(s.mint(c.Scope(), name, e.viewType, c.Admitted()))
		a.shim.stream = minted.members(c.Scope())
		a.shim.seen = &localStreamSeen{}
	}
	resp, err := s.forward(ctx, c, in, w, a, []string{in.TableName})
	if err != nil || resp.Status != http.StatusOK {
		return resp, err
	}
	_, err = kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		st.Tables[name] = record
		if minted != nil {
			minted.adopt(a.shim.seen)
			st.Streams = append(st.Streams, *minted)
		}
		return struct{}{}, nil
	})
	return resp, err
}

// deleteTable forwards the delete of a table the name resolves to exactly,
// answers it DELETING as AWS does, and forgets the table Local deleted,
// with its TTL sweep. A table with an enabled stream is deleted under its
// stream lock once Local holds none of its records the log lacks
// (closingStream).
func (s *Service) deleteTable(ctx context.Context, c *kernel.Call, in *dynamodbapi.DeleteTableInput, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
	a := tableAnswer(in.TableName)
	a.shim = s.described(c.Scope(), in.TableName, deleteAnswer)
	name := tableName(in.TableName)
	release, err := s.closingStream(ctx, c, name)
	if err != nil {
		return kernel.RawResponse{}, err
	}
	defer release()
	resp, err := s.forward(ctx, c, in, w, a, []string{in.TableName})
	if err != nil || resp.Status != http.StatusOK {
		return resp, err
	}
	_, err = kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Tables[name]; !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		delete(st.Tables, name)
		st.tableDeleted(name, c.Admitted())
		return struct{}{}, nil
	})
	s.sweeps.forget(tableKey{scope: c.Scope(), table: name})
	return resp, err
}
