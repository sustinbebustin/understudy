package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// creation is the record of the table a CreateTable in creates, after
// refusing what AWS refuses and Local accepts: tags past AWS's limits
// (checkTags) and a KMS key KMS does not find (encryption).
func (s *Service) creation(ctx context.Context, c *kernel.Call, in *dynamodbapi.CreateTableInput) (Table, *awserr.Error) {
	if err := checkTags(nil, in.Tags); err != nil {
		return Table{}, err
	}
	var sse *SSE
	if enablesSSE(in.SSESpecification) {
		var err *awserr.Error
		if sse, err = s.encryption(ctx, c, in.SSESpecification.KMSMasterKeyId); err != nil {
			return Table{}, err
		}
	}
	return newTable(c, in, sse), nil
}

// recorded returns the shim's record of the table name in c's scope. A
// table Local holds that the shim never saw created (one Local kept under
// --persist without the archive, written in the last 30 s before a crash)
// is adopted: its record is built from Local's description, with Local's
// CreationDateTime and a TableId derived from the table's path, so the
// shim operations answer for it as for any table. missing is the answer
// when neither holds it, and when the scope holds only a case twin, which
// Local would describe in its place.
func (s *Service) recorded(ctx context.Context, c *kernel.Call, name string, missing *awserr.Error) (Table, *awserr.Error) {
	if t, ok := s.lookup(c.Scope(), name); ok {
		return t, nil
	}
	if cell, ok := s.cells.Lookup(c.Scope()); ok && store.Read(cell, func(st *State) bool { return st.onlyTwinOf(name) }) {
		return Table{}, missing
	}
	describe := dynamodbapi.OpDescribeTableRaw.Descriptor()
	body, err := encodeInput(describe, &dynamodbapi.DescribeTableInput{TableName: name})
	if err != nil {
		return Table{}, err
	}
	endpoint, err := s.ready(ctx)
	if err != nil {
		return Table{}, err
	}
	resp, err := s.send(ctx, endpoint, exchange{scope: c.Scope(), op: describe.ID, at: c.Admitted()}, body)
	if err != nil {
		return Table{}, err
	}
	var out dynamodbapi.DescribeTableOutput
	if derr := decodeAnswer(describe, resp, &out); derr != nil {
		if le, ok := errors.AsType[*LocalError](derr); ok {
			switch {
			case le.is("ResourceNotFoundException"):
				return Table{}, missing
			case le.Status == http.StatusBadRequest && le.is("ValidationException"):
				// A name outside AWS's naming rules, which the model's
				// TableArn constraint lets through: the client's mistake,
				// refused in Local's words as AWS refuses it.
				return Table{}, dynamodbapi.ErrValidationException(le.Message)
			}
		}
		return Table{}, awserr.Internalf(fmt.Errorf("asking DynamoDB Local whether it holds table %s, which the shim does not record: %w", name, derr))
	}
	if out.Table == nil || out.Table.TableName == nil || *out.Table.TableName != name {
		return Table{}, missing
	}
	k := tableKey{scope: c.Scope(), table: name}
	adopted := Table{ID: adoptedID(k)}
	if out.Table.CreationDateTime != nil {
		adopted.Created = clock.FromWire(*out.Table.CreationDateTime)
	}
	adopted.Billing = adoptedBilling(out.Table.BillingModeSummary, adopted.Created)
	return kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, tx *kernel.Tx) (Table, *awserr.Error) {
		if t, ok := st.Tables[name]; ok {
			tx.Unchanged()
			return t.clone(), nil
		}
		st.Tables[name] = adopted
		return adopted.clone(), nil
	})
}

// shimOnly reports whether an UpdateTable changes nothing but what the shim
// owns: SSESpecification, TableClass and WarmThroughput, which Local
// refuses alone with "Nothing to update" (dynamodb-local.md 4.2). Every
// other member of UpdateTableInput is listed, so a member the model adds
// later is forwarded.
func shimOnly(in *dynamodbapi.UpdateTableInput) bool {
	owned := in.SSESpecification != nil || in.TableClass != nil || in.WarmThroughput != nil
	others := len(in.AttributeDefinitions) > 0 || in.BillingMode != nil || in.ProvisionedThroughput != nil ||
		len(in.GlobalSecondaryIndexUpdates) > 0 || in.StreamSpecification != nil || len(in.ReplicaUpdates) > 0 ||
		in.DeletionProtectionEnabled != nil || in.MultiRegionConsistency != nil || len(in.GlobalTableWitnessUpdates) > 0 ||
		in.OnDemandThroughput != nil || in.GlobalTableSettingsReplicationMode != nil || len(in.VectorIndexUpdates) > 0
	return owned && !others
}

// updateTable applies an UpdateTable. Local holds the table's schema and
// throughput, the shim the members it owns: an update of only those is
// answered here with Local's description, which Local would refuse; any
// other update is forwarded as the client sent it, and the shim's members
// it carries are recorded once Local accepts it. A table Local holds that
// the shim does not record is adopted first (recorded), so an update of
// only the shim's members succeeds on it too. Either answer says UPDATING,
// as AWS's does while the update runs, where Local's says ACTIVE;
// DescribeTable then reports Local's ACTIVE, which the waiters accept.
func (s *Service) updateTable(ctx context.Context, c *kernel.Call, in *dynamodbapi.UpdateTableInput, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
	name := tableName(in.TableName)
	a := tableAnswer(in.TableName)
	if err := preForward(c); err != nil {
		return kernel.RawResponse{}, err
	}
	if err := inScope(c.Scope(), []string{in.TableName}, a); err != nil {
		return kernel.RawResponse{}, err
	}
	current, err := s.recorded(ctx, c, name, dynamodbapi.ErrResourceNotFoundException(awsNotFound(a.table)))
	if err != nil {
		return kernel.RawResponse{}, err
	}
	var sse *SSE
	if sseChangeOf(in.SSESpecification) == sseKMS {
		if sse, err = s.encryption(ctx, c, in.SSESpecification.KMSMasterKeyId); err != nil {
			return kernel.RawResponse{}, err
		}
	}
	// The answer and the record carry the same instant, c's admission, so
	// the UpdateTable answer and the next DescribeTable agree.
	next := current.updated(in, c.Admitted(), sse)
	a.shim = &shimmed{table: &next, status: dynamodbapi.TableStatusUpdating}
	var resp kernel.RawResponse
	if shimOnly(in) {
		a.shim.under = "TableDescription"
		resp, err = s.describeLocally(ctx, c, in.TableName, a)
	} else {
		resp, err = s.forward(ctx, c, in, w, a, []string{in.TableName})
	}
	if err != nil || resp.Status != http.StatusOK {
		return resp, err
	}
	_, err = s.alter(c, name, dynamodbapi.ErrResourceNotFoundException(awsNotFound(name)), func(t *Table, _ clock.Instant) *awserr.Error {
		*t = t.updated(in, c.Admitted(), sse)
		return nil
	})
	return resp, err
}

// describeLocally answers c with Local's description of table, completed
// and rewritten as a says. The caller has run the checks a forwarded call
// gets.
func (s *Service) describeLocally(ctx context.Context, c *kernel.Call, table string, a answer) (kernel.RawResponse, *awserr.Error) {
	describe := dynamodbapi.OpDescribeTableRaw.Descriptor()
	body, err := encodeInput(describe, &dynamodbapi.DescribeTableInput{TableName: table})
	if err != nil {
		return kernel.RawResponse{}, err
	}
	endpoint, err := s.ready(ctx)
	if err != nil {
		return kernel.RawResponse{}, err
	}
	local, err := s.send(ctx, endpoint, exchange{scope: c.Scope(), op: describe.ID, at: c.Admitted()}, body)
	if err != nil {
		return kernel.RawResponse{}, err
	}
	return rewrite(c, a, local)
}

// alter changes the record of the table name in c's scope with fn, under
// the cell's lock, and returns the record as fn left it; missing is the
// answer when the shim records no such table.
func (s *Service) alter(c *kernel.Call, name string, missing *awserr.Error, fn func(t *Table, now clock.Instant) *awserr.Error) (Table, *awserr.Error) {
	return kernel.Write(c, s.cells, c.Scope(), func(st *State, now clock.Instant, _ *kernel.Tx) (Table, *awserr.Error) {
		t, ok := st.Tables[name]
		if !ok {
			return Table{}, missing
		}
		t = t.clone()
		if err := fn(&t, now); err != nil {
			return Table{}, err
		}
		st.Tables[name] = t
		return t.clone(), nil
	})
}

// answered is the handler of an operation the shims answer from their own
// state: after the checks a forwarded call gets and the licence gate, the
// output of fn is the answer, with the x-amz-crc32 header every DynamoDB
// answer carries. Local never sees the call.
func answered[In any](s *Service, fn func(ctx context.Context, c *kernel.Call, in *In) (smithy.Serializable, *awserr.Error)) kernel.RawHandler[In] {
	return func(ctx context.Context, c *kernel.Call, in *In, _ kernel.Wire) (kernel.RawResponse, *awserr.Error) {
		if err := preForward(c); err != nil {
			return kernel.RawResponse{}, err
		}
		if _, err := s.ready(ctx); err != nil {
			return kernel.RawResponse{}, err
		}
		out, err := fn(ctx, c, in)
		if err != nil {
			return kernel.RawResponse{}, err
		}
		resp, err := awsjson.Codec{Version: awsjson.V10}.Encode(c.Op(), string(c.ID()), out, http.StatusOK)
		if err != nil {
			return kernel.RawResponse{}, err
		}
		return rewrite(c, answer{}, kernel.RawResponse{Status: http.StatusOK, Header: resp.Header, Body: resp.Body})
	}
}

// Coverage notes of the operations the shims answer.
const (
	ttlNote = "aws-differs: understudy: an expired item is deleted on the emulator clock as soon as it expires, where AWS deletes within days; " +
		"the one change per hour AWS allows is not enforced"
	pitrNote = "point-in-time recovery is recorded and described; no backups are taken, and the restore operations are not implemented"
)

// bindShims binds the operations the shims answer without Local: tags,
// time to live and continuous backups.
func (s *Service) bindShims(b dynamodbapi.Binder) {
	b.TagResourceRaw(answered(s, s.tagResource), registry.Full{}, taggedTable(func(in *dynamodbapi.TagResourceInput) string { return in.ResourceArn }))
	b.UntagResourceRaw(answered(s, s.untagResource), registry.Full{}, taggedTable(func(in *dynamodbapi.UntagResourceInput) string { return in.ResourceArn }))
	b.ListTagsOfResourceRaw(answered(s, s.listTags), registry.Full{}, taggedTable(func(in *dynamodbapi.ListTagsOfResourceInput) string { return in.ResourceArn }))
	b.UpdateTimeToLiveRaw(answered(s, s.updateTimeToLive), registry.Partial{Note: ttlNote},
		tables(named(func(in *dynamodbapi.UpdateTimeToLiveInput) string { return in.TableName })))
	b.DescribeTimeToLiveRaw(answered(s, s.describeTimeToLive), registry.Partial{Note: ttlNote},
		tables(named(func(in *dynamodbapi.DescribeTimeToLiveInput) string { return in.TableName })))
	b.UpdateContinuousBackupsRaw(answered(s, s.updateContinuousBackups), registry.StoredOnly{Note: pitrNote},
		tables(named(func(in *dynamodbapi.UpdateContinuousBackupsInput) string { return in.TableName })))
	b.DescribeContinuousBackupsRaw(answered(s, s.describeContinuousBackups), registry.StoredOnly{Note: pitrNote},
		tables(named(func(in *dynamodbapi.DescribeContinuousBackupsInput) string { return in.TableName })))
}
