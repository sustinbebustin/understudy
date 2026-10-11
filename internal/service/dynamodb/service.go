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
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
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
}

// The service is the snapshot and reset view of its shim state.
var _ kernel.Stateful = (*Service)(nil)

// New binds every operation DynamoDB Local serves and those the shims
// answer. The rest, Local's gaps, stay unbound: they answer 501 before
// Local sees them, and coverage lists them.
func New(d kernel.ServiceDeps, o Options) *Service {
	codec := jsonx.NewCodec()
	s := &Service{
		local:  o.Local,
		cells:  store.NewPartitioned(d.Kernel.Gate(), store.Codec{Marshal: codec.Marshal, Unmarshal: codec.Unmarshal}, newState),
		kernel: d.Kernel,
	}
	if s.local == nil {
		s.local = unlicensed{}
	}
	timers := d.Kernel.Timers()
	s.sweeps = newSweeper(timers.Scheduler(), timers.Handle(ttlTimer, s.onSweep))
	b := dynamodbapi.NewBinder(d.Binder)
	s.bindTables(b)
	s.bindItems(b)
	s.bindStatements(b)
	s.bindShims(b)
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
	// which do not send TransactionConflictException.
	noTxConflicts = "aws-differs: dynamodb-local: transactions never conflict; AWS cancels a conflicting transaction with TransactionCanceledException " +
		"and the cancellation reason TransactionConflict, and a fault rule cannot set cancellation reasons yet"
	caseTwins      = "aws-differs: dynamodb-local: table names are case-insensitive, so a name that differs from an existing table's only in case is refused with ResourceInUseException"
	statementTwins = "aws-differs: dynamodb-local: table names inside statements resolve case-insensitively, so a statement naming a case twin of a table acts on that table"
	parallelScan   = "aws-differs: dynamodb-local: Segment and TotalSegments are ignored, so a parallel scan reads sequentially"
	statementLimit = "aws-differs: dynamodb-local: Limit is ignored"
	// streamLabels goes on the operations whose answers carry
	// LatestStreamLabel (ADR 0009 amendment, ARCHITECTURE.md 15).
	streamLabels = "aws-differs: dynamodb-local: LatestStreamLabel is minted by DynamoDB Local; a table re-created by snapshot load or reset carries a new one"
)

// notes joins coverage notes into one Partial.
func notes(ns ...string) registry.Partial { return registry.Partial{Note: strings.Join(ns, "; ")} }

func (s *Service) bindTables(b dynamodbapi.Binder) {
	createTable := named(func(in *dynamodbapi.CreateTableInput) string { return in.TableName })
	b.CreateTableRaw(s.createTable, notes(caseTwins, streamLabels), tables(createTable))
	deleteTable := named(func(in *dynamodbapi.DeleteTableInput) string { return in.TableName })
	b.DeleteTableRaw(s.deleteTable, registry.Full{}, tables(deleteTable))
	describeTable := named(func(in *dynamodbapi.DescribeTableInput) string { return in.TableName })
	b.DescribeTableRaw(s.describeTable, notes(streamLabels), tables(describeTable))
	updateTable := named(func(in *dynamodbapi.UpdateTableInput) string { return in.TableName })
	b.UpdateTableRaw(s.updateTable, notes(streamLabels), tables(updateTable))
	b.ListTablesRaw(passthrough(s, noTables, plainAnswer[dynamodbapi.ListTablesInput]), registry.Full{})
	b.DescribeLimitsRaw(passthrough(s, noTables, plainAnswer[dynamodbapi.DescribeLimitsInput]), registry.Full{})
}

func (s *Service) bindItems(b dynamodbapi.Binder) {
	getItem := named(func(in *dynamodbapi.GetItemInput) string { return in.TableName })
	b.GetItemRaw(passthrough(s, getItem, plainAnswer), notes(noThroughput), tables(getItem))
	putItem := named(func(in *dynamodbapi.PutItemInput) string { return in.TableName })
	b.PutItemRaw(ttlWrites(s, passthrough(s, putItem, plainAnswer), putItemWrites), notes(noThroughput, noConflicts), tables(putItem))
	updateItem := named(func(in *dynamodbapi.UpdateItemInput) string { return in.TableName })
	b.UpdateItemRaw(ttlWrites(s, passthrough(s, updateItem, plainAnswer), updateItemWrites), notes(noThroughput, noConflicts), tables(updateItem))
	deleteItem := named(func(in *dynamodbapi.DeleteItemInput) string { return in.TableName })
	b.DeleteItemRaw(passthrough(s, deleteItem, plainAnswer), notes(noThroughput, noConflicts), tables(deleteItem))
	query := named(func(in *dynamodbapi.QueryInput) string { return in.TableName })
	b.QueryRaw(passthrough(s, query, plainAnswer), notes(noThroughput), tables(query))
	scan := named(func(in *dynamodbapi.ScanInput) string { return in.TableName })
	b.ScanRaw(passthrough(s, scan, plainAnswer), notes(noThroughput, parallelScan), tables(scan))
	batchGet := func(in *dynamodbapi.BatchGetItemInput) []string { return slices.Sorted(maps.Keys(in.RequestItems)) }
	b.BatchGetItemRaw(passthrough(s, batchGet, plainAnswer), notes(noThroughput), tables(batchGet))
	batchWrite := func(in *dynamodbapi.BatchWriteItemInput) []string { return slices.Sorted(maps.Keys(in.RequestItems)) }
	b.BatchWriteItemRaw(ttlWrites(s, passthrough(s, batchWrite, plainAnswer), batchWrites), notes(noThroughput), tables(batchWrite))
	b.TransactGetItemsRaw(passthrough(s, transactGetTables, plainAnswer), notes(noThroughput, noTxConflicts), tables(transactGetTables))
	b.TransactWriteItemsRaw(ttlWrites(s, passthrough(s, transactWriteTables, plainAnswer), transactWrites),
		notes(noThroughput, noTxConflicts), tables(transactWriteTables))
}

// bindStatements binds PartiQL, whose tables are inside the statements.
func (s *Service) bindStatements(b dynamodbapi.Binder) {
	b.ExecuteStatementRaw(ttlStatements(s, passthrough(s, noTables, plainAnswer[dynamodbapi.ExecuteStatementInput]), statementOf),
		notes(noThroughput, noConflicts, statementLimit, statementTwins))
	b.BatchExecuteStatementRaw(ttlStatements(s, passthrough(s, noTables, plainAnswer[dynamodbapi.BatchExecuteStatementInput]), batchStatements),
		notes(noThrottling, statementTwins))
	b.ExecuteTransactionRaw(ttlStatements(s, passthrough(s, noTables, plainAnswer[dynamodbapi.ExecuteTransactionInput]), transactionStatements),
		notes(noThroughput, noTxConflicts, statementTwins))
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
// drops from the request (newTable), which its answer then describes.
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
	a.shim = &shimmed{table: &record}
	resp, err := s.forward(ctx, c, in, w, a, []string{in.TableName})
	if err != nil || resp.Status != http.StatusOK {
		return resp, err
	}
	_, err = kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		st.Tables[name] = record
		return struct{}{}, nil
	})
	return resp, err
}

// deleteTable forwards the delete of a table the name resolves to exactly,
// answers it DELETING as AWS does, and forgets the table Local deleted,
// with its TTL sweep.
func (s *Service) deleteTable(ctx context.Context, c *kernel.Call, in *dynamodbapi.DeleteTableInput, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
	a := tableAnswer(in.TableName)
	a.shim = s.described(c.Scope(), in.TableName, dynamodbapi.TableStatusDeleting)
	resp, err := s.forward(ctx, c, in, w, a, []string{in.TableName})
	if err != nil || resp.Status != http.StatusOK {
		return resp, err
	}
	name := tableName(in.TableName)
	_, err = kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Tables[name]; !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		delete(st.Tables, name)
		return struct{}{}, nil
	})
	s.sweeps.forget(tableKey{scope: c.Scope(), table: name})
	return resp, err
}
