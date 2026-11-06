package dynamodb

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Each PartiQL statement needs the action of its verb
// (iam-enforcement-surfaces.md 10.2: ExecuteStatement, BatchExecuteStatement
// and ExecuteTransaction list PartiQLDelete, PartiQLInsert, PartiQLSelect
// and PartiQLUpdate); dynamodb:PartiQLSelect is the operation's primary,
// recorded for every call, so a SELECT declares nothing more.
func TestPartiQLActionsFollowEachStatementsVerb(t *testing.T) {
	got := partiQLActions([]string{
		`SELECT * FROM "t"`,
		"insert\tINTO \"t\" VALUE {'pk':'a'}",
		`  UPDATE "t" SET v = 1 WHERE pk = 'a'`,
		`Delete FROM "t" WHERE pk = 'a'`,
		``,
		`EXISTS(SELECT * FROM "t" WHERE pk = 'a')`,
	})
	want := []registry.IAMAction{dynamoAction("PartiQLInsert"), dynamoAction("PartiQLUpdate"), dynamoAction("PartiQLDelete")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("partiQLActions = %v, want %v", got, want)
	}
}

// A transaction's Update, Delete and ConditionCheck items each name the
// table their own action applies to (iam-enforcement-surfaces.md 12.6); a
// Put item needs the operation's primary, dynamodb:PutItem.
func TestTransactKindsNameTheTablesOfTheirItems(t *testing.T) {
	in := &dynamodbapi.TransactWriteItemsInput{TransactItems: []dynamodbapi.TransactWriteItem{
		{Put: &dynamodbapi.Put{TableName: "orders"}},
		{Update: &dynamodbapi.Update{TableName: "stock"}},
		{ConditionCheck: &dynamodbapi.ConditionCheck{TableName: "limits"}},
		{Update: &dynamodbapi.Update{TableName: "ledger"}},
	}}
	got := map[string][]string{}
	for _, kind := range transactKinds() {
		got[kind.action.Name] = kind.tables(in)
	}
	want := map[string][]string{"UpdateItem": {"stock", "ledger"}, "DeleteItem": nil, "ConditionCheckItem": {"limits"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tables per kind = %v, want %v", got, want)
	}
}
