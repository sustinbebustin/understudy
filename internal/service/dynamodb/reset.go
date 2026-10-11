package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// ResetLate deletes, in the running Local, every table of the accounts and
// regions rs covers that hold shim state; Reset then drops that state
// (ARCHITECTURE.md 13). A scope the shim never recorded a table in holds
// none, so a reset starts no Local that is not running.
func (s *Service) ResetLate(ctx context.Context, rs kernel.ResetScope) error {
	var scopes []aws.Scope
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		if rs.Covers(scope) {
			scopes = append(scopes, scope)
		}
	}
	return s.dropTables(ctx, scopes)
}

// dropTables deletes every table Local holds in scopes, whether or not the
// shim records it, turning deletion protection off first where it is on.
func (s *Service) dropTables(ctx context.Context, scopes []aws.Scope) error {
	for _, scope := range scopes {
		names, err := s.localTables(ctx, scope)
		if err != nil {
			return fmt.Errorf("listing the DynamoDB tables of account %s in %s: %w", scope.Account, scope.Region, err)
		}
		for _, name := range names {
			if err := s.dropTable(ctx, tableKey{scope: scope, table: name}); err != nil {
				return fmt.Errorf("deleting DynamoDB table %s: %w", tableARN(scope, name), err)
			}
		}
	}
	return nil
}

// localTables lists the tables Local holds in scope, following
// LastEvaluatedTableName.
func (s *Service) localTables(ctx context.Context, scope aws.Scope) ([]string, error) {
	var names []string
	in := &dynamodbapi.ListTablesInput{}
	for {
		var out dynamodbapi.ListTablesOutput
		if err := s.direct(ctx, scope, dynamodbapi.OpListTablesRaw.Descriptor(), in, &out); err != nil {
			return nil, err
		}
		names = append(names, out.TableNames...)
		if out.LastEvaluatedTableName == nil {
			return names, nil
		}
		in = &dynamodbapi.ListTablesInput{ExclusiveStartTableName: out.LastEvaluatedTableName}
	}
}

// dropTable deletes k. Local refuses to delete a protected table with
// ValidationException (dynamodb-local.md 4.2), so protection is turned
// off and the delete sent again; a table already gone is no error.
func (s *Service) dropTable(ctx context.Context, k tableKey) error {
	del := dynamodbapi.OpDeleteTableRaw.Descriptor()
	err := s.direct(ctx, k.scope, del, &dynamodbapi.DeleteTableInput{TableName: k.table}, nil)
	if le, ok := errors.AsType[*LocalError](err); ok && le.is("ValidationException") && strings.Contains(le.Message, "protected against deletion") {
		unprotect := &dynamodbapi.UpdateTableInput{TableName: k.table, DeletionProtectionEnabled: new(false)}
		if err := s.direct(ctx, k.scope, dynamodbapi.OpUpdateTableRaw.Descriptor(), unprotect, nil); err != nil {
			return err
		}
		err = s.direct(ctx, k.scope, del, &dynamodbapi.DeleteTableInput{TableName: k.table}, nil)
	}
	if le, ok := errors.AsType[*LocalError](err); ok && le.is("ResourceNotFoundException") {
		return nil
	}
	return err
}
