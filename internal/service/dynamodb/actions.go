package dynamodb

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Coverage notes of the operations whose first listed action AWS does not
// always authorize: the access record keeps it, and step 8 asks about it,
// for every call (ARCHITECTURE.md 17, the primary action always), though
// the SAR lists no action of the operation's own name
// (iam-enforcement-surfaces.md 10.2).
const (
	transactPutAlways = "dynamodb:PutItem, the first action the operation lists, is recorded and authorized on every table of every call, " +
		"where AWS authorizes each item's own action (PutItem, UpdateItem, DeleteItem or ConditionCheckItem) on its own table"
	partiQLSelectAlways = "dynamodb:PartiQLSelect, the first action the operation lists, is recorded and authorized for every call, " +
		"and every PartiQL action on any resource, since the tables are inside the statements, " +
		"where AWS authorizes each statement's own action on its table"
)

// declareActions states, per input, the secondary IAM actions of every
// bound operation whose generated list holds more than one dynamodb action
// (iam-enforcement-surfaces.md 10.2, 10.4). Never declared: the
// replication actions (ReadDataForReplication, WriteDataForReplication,
// ReplicateSettings, the replica and witness actions), which authorize the
// copying DynamoDB does between a global table's replicas, and the data
// actions CreateTable and UpdateTable list for seeding a replica:
// DynamoDB Local holds no global tables, so no call makes them. A
// transaction's or statement's own actions are declared per item. It runs
// after the bind sites, whose bindings it extends.
func declareActions(b *kernel.Binder) {
	kernel.Declare(b, dynamodbapi.OpGetItemRaw, kernel.WithoutSecondaryActions())
	kernel.Declare(b, dynamodbapi.OpPutItemRaw, kernel.WithoutSecondaryActions())
	kernel.Declare(b, dynamodbapi.OpDeleteItemRaw, kernel.WithoutSecondaryActions())
	kernel.Declare(b, dynamodbapi.OpScanRaw, kernel.WithoutSecondaryActions())
	kernel.Declare(b, dynamodbapi.OpDescribeTableRaw, kernel.WithoutSecondaryActions())
	kernel.Declare(b, dynamodbapi.OpDescribeTimeToLiveRaw, kernel.WithoutSecondaryActions())
	kernel.Declare(b, dynamodbapi.OpUpdateTimeToLiveRaw, kernel.WithoutSecondaryActions())
	kernel.Declare(b, dynamodbapi.OpUpdateTableRaw, kernel.WithoutSecondaryActions())
	// A table created with tags needs dynamodb:TagResource, and one created
	// with a resource policy dynamodb:PutResourcePolicy: each applies when
	// its member is sent (10.4).
	kernel.Declare(b, dynamodbapi.OpCreateTableRaw, kernel.WithActions(func(_ *kernel.Call, in *dynamodbapi.CreateTableInput) []registry.IAMAction {
		var out []registry.IAMAction
		if len(in.Tags) > 0 {
			out = append(out, dynamoAction("TagResource"))
		}
		if in.ResourcePolicy != nil {
			out = append(out, dynamoAction("PutResourcePolicy"))
		}
		return out
	}))
	// Each item of a transaction needs its own action on its own table.
	// dynamodb:PutItem, the operation's primary, stays paired with every
	// table (transactPutAlways).
	kinds := transactKinds()
	transact := make([]kernel.BindOption, 0, 1+len(kinds))
	transact = append(transact, kernel.WithActions(func(_ *kernel.Call, in *dynamodbapi.TransactWriteItemsInput) []registry.IAMAction {
		var out []registry.IAMAction
		for _, kind := range kinds {
			if len(kind.tables(in)) > 0 {
				out = append(out, kind.action)
			}
		}
		return out
	}))
	for _, kind := range kinds {
		transact = append(transact, transactScoped(kind))
	}
	kernel.Declare(b, dynamodbapi.OpTransactWriteItemsRaw, transact...)
	kernel.Declare(b, dynamodbapi.OpExecuteStatementRaw, statementActions(statementOf))
	kernel.Declare(b, dynamodbapi.OpBatchExecuteStatementRaw, statementActions(batchStatements))
	kernel.Declare(b, dynamodbapi.OpExecuteTransactionRaw, statementActions(transactionStatements))
}

// dynamoAction is the dynamodb action name.
func dynamoAction(name string) registry.IAMAction {
	return registry.IAMAction{Prefix: "dynamodb", Name: name}
}

// transactKind is one kind of TransactWriteItems item besides Put: the
// action it needs and the tables its items of that kind name.
type transactKind struct {
	action registry.IAMAction
	tables func(in *dynamodbapi.TransactWriteItemsInput) []string
}

// transactKinds are the item kinds whose action is not the operation's
// primary.
func transactKinds() []transactKind {
	return []transactKind{
		{dynamoAction("UpdateItem"), func(in *dynamodbapi.TransactWriteItemsInput) []string {
			var out []string
			for _, it := range in.TransactItems {
				if it.Update != nil {
					out = append(out, it.Update.TableName)
				}
			}
			return out
		}},
		{dynamoAction("DeleteItem"), func(in *dynamodbapi.TransactWriteItemsInput) []string {
			var out []string
			for _, it := range in.TransactItems {
				if it.Delete != nil {
					out = append(out, it.Delete.TableName)
				}
			}
			return out
		}},
		{dynamoAction("ConditionCheckItem"), func(in *dynamodbapi.TransactWriteItemsInput) []string {
			var out []string
			for _, it := range in.TransactItems {
				if it.ConditionCheck != nil {
					out = append(out, it.ConditionCheck.TableName)
				}
			}
			return out
		}},
	}
}

// transactScoped pairs kind's action with the tables its items name
// (iam-enforcement-surfaces.md 12.6).
func transactScoped(kind transactKind) kernel.BindOption {
	return kernel.WithResourcesFor([]registry.IAMAction{kind.action}, func(c *kernel.Call, in *dynamodbapi.TransactWriteItemsInput) []aws.ARN {
		names := kind.tables(in)
		out := make([]aws.ARN, 0, len(names))
		for _, n := range names {
			out = append(out, tableARN(c.Scope(), n))
		}
		return out
	})
}

// statementActions declares the PartiQL actions an input's statements need
// besides dynamodb:PartiQLSelect, the operation's primary (partiQLActions).
func statementActions[In any](statements func(*In) []string) kernel.BindOption {
	return kernel.WithActions(func(_ *kernel.Call, in *In) []registry.IAMAction { return partiQLActions(statements(in)) })
}

// partiQLActions returns the PartiQL action each statement's verb needs
// besides dynamodb:PartiQLSelect, in statement order: INSERT
// dynamodb:PartiQLInsert, UPDATE dynamodb:PartiQLUpdate, DELETE
// dynamodb:PartiQLDelete, in any case and whatever white space follows the
// verb (writesItems reads it the same way). A statement with another verb
// adds nothing, and Local answers it.
func partiQLActions(statements []string) []registry.IAMAction {
	var out []registry.IAMAction
	for _, st := range statements {
		words := strings.Fields(st)
		if len(words) == 0 {
			continue
		}
		switch strings.ToUpper(words[0]) {
		case "INSERT":
			out = append(out, dynamoAction("PartiQLInsert"))
		case "UPDATE":
			out = append(out, dynamoAction("PartiQLUpdate"))
		case "DELETE":
			out = append(out, dynamoAction("PartiQLDelete"))
		}
	}
	return out
}
