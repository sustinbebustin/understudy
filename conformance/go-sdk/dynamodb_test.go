package gosdk_test

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Research: docs/research/phase1/dynamodb-local.md, error-matrix.md 5.5.

// ddbLicenceEnv is how the caller accepts the Amazon DynamoDB Local
// License Agreement. understudy runs DynamoDB on DynamoDB Local, which it
// pulls only after that acceptance, so without it these tests skip rather
// than accept the licence on the caller's behalf.
const ddbLicenceEnv = "UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE"

func requireDynamoDB(t *testing.T) {
	t.Helper()
	if os.Getenv(ddbLicenceEnv) != "1" {
		t.Skip(ddbLicenceEnv + "=1 is not set: DynamoDB runs on DynamoDB Local, whose licence the caller must accept (see README.md)")
	}
}

// createLockTable creates an on-demand table keyed by LockID, waits until
// it is ACTIVE and deletes it when the test ends.
func createLockTable(t *testing.T, tableName string) *types.TableDescription {
	t.Helper()
	ctx := t.Context()
	client := dynamodb.NewFromConfig(awsCfg)
	out, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(tableName),
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("LockID"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("LockID"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
		Tags:                 []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	ok(t, err, "CreateTable "+tableName)
	t.Cleanup(func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})
	})
	err = dynamodb.NewTableExistsWaiter(client).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)}, 3*time.Minute)
	ok(t, err, "waiting for "+tableName+" to be ACTIVE")
	return out.TableDescription
}

// ddbS is a DynamoDB string attribute value.
func ddbS(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

// ddbN is a DynamoDB number attribute value.
func ddbN(v int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}

func TestDynamoDBTable(t *testing.T) {
	t.Parallel()
	requireDynamoDB(t)
	ctx := t.Context()
	client := dynamodb.NewFromConfig(awsCfg)
	acct := account(t)
	tableName := name("locks")
	created := createLockTable(t, tableName)
	tableArn := "arn:aws:dynamodb:" + region() + ":" + acct + ":table/" + tableName

	t.Run("CreateTable and DescribeTable", func(t *testing.T) {
		if aws.ToString(created.TableArn) != tableArn {
			t.Errorf("CreateTable TableArn = %q, want %q", aws.ToString(created.TableArn), tableArn)
		}
		out, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)})
		ok(t, err, "DescribeTable")
		d := out.Table
		if d.TableStatus != types.TableStatusActive || aws.ToString(d.TableArn) != tableArn || d.BillingModeSummary == nil ||
			d.BillingModeSummary.BillingMode != types.BillingModePayPerRequest || aws.ToString(d.TableId) == "" {
			t.Errorf("DescribeTable = status %q, arn %q, billing %+v, id %q", d.TableStatus, aws.ToString(d.TableArn), d.BillingModeSummary, aws.ToString(d.TableId))
		}
		list, err := client.ListTables(ctx, &dynamodb.ListTablesInput{})
		ok(t, err, "ListTables")
		if !slices.Contains(list.TableNames, tableName) {
			t.Errorf("ListTables does not list %s", tableName)
		}
	})

	t.Run("errors", func(t *testing.T) {
		_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName:            aws.String(tableName),
			AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("LockID"), AttributeType: types.ScalarAttributeTypeS}},
			KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("LockID"), KeyType: types.KeyTypeHash}},
			BillingMode:          types.BillingModePayPerRequest,
		})
		errAs[*types.ResourceInUseException](t, err, "CreateTable with an existing name")
		missing := name("missing")
		_, err = client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(missing)})
		errAs[*types.ResourceNotFoundException](t, err, "DescribeTable missing")
		wantStatus(t, err, "DescribeTable missing", 400)
		_, err = client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(missing), Key: map[string]types.AttributeValue{"LockID": ddbS("x")}})
		errAs[*types.ResourceNotFoundException](t, err, "GetItem on a missing table")
		_, err = client.DescribeContinuousBackups(ctx, &dynamodb.DescribeContinuousBackupsInput{TableName: aws.String(missing)})
		errAs[*types.TableNotFoundException](t, err, "DescribeContinuousBackups on a missing table")
		_, err = client.ListTables(ctx, &dynamodb.ListTablesInput{Limit: aws.Int32(101)})
		apiErr(t, err, "ListTables Limit 101", "ValidationException")
		wantMessageContains(t, err, "ListTables Limit 101", "Member must have value less than or equal to 100")
		_, err = client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tableName), Item: map[string]types.AttributeValue{"LockID": ddbS("big"), "Blob": ddbS(strings.Repeat("x", 410*1024))}})
		apiErr(t, err, "PutItem over 400 KB", "ValidationException")
		wantMessageContains(t, err, "PutItem over 400 KB", "Item size has exceeded the maximum allowed size")
		_, err = client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tableName), Item: map[string]types.AttributeValue{"Other": ddbS("x")}})
		apiErr(t, err, "PutItem without the key attribute", "ValidationException")
	})

	t.Run("tags", func(t *testing.T) {
		out, err := client.ListTagsOfResource(ctx, &dynamodb.ListTagsOfResourceInput{ResourceArn: aws.String(tableArn)})
		ok(t, err, "ListTagsOfResource")
		if len(out.Tags) != 1 || aws.ToString(out.Tags[0].Key) != "team" || aws.ToString(out.Tags[0].Value) != "platform" {
			t.Errorf("tags from CreateTable = %+v", out.Tags)
		}
		_, err = client.TagResource(ctx, &dynamodb.TagResourceInput{ResourceArn: aws.String(tableArn), Tags: []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}}})
		ok(t, err, "TagResource")
		_, err = client.UntagResource(ctx, &dynamodb.UntagResourceInput{ResourceArn: aws.String(tableArn), TagKeys: []string{"team"}})
		ok(t, err, "UntagResource")
		out, err = client.ListTagsOfResource(ctx, &dynamodb.ListTagsOfResourceInput{ResourceArn: aws.String(tableArn)})
		ok(t, err, "ListTagsOfResource")
		if len(out.Tags) != 1 || aws.ToString(out.Tags[0].Key) != "env" {
			t.Errorf("tags after tag and untag = %+v, want env=test", out.Tags)
		}
	})

	t.Run("TTL", func(t *testing.T) {
		before, err := client.DescribeTimeToLive(ctx, &dynamodb.DescribeTimeToLiveInput{TableName: aws.String(tableName)})
		ok(t, err, "DescribeTimeToLive")
		if d := before.TimeToLiveDescription; d == nil || d.TimeToLiveStatus != types.TimeToLiveStatusDisabled || d.AttributeName != nil {
			t.Errorf("TTL of a new table = %+v, want DISABLED with no attribute name", d)
		}
		up, err := client.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{TableName: aws.String(tableName), TimeToLiveSpecification: &types.TimeToLiveSpecification{AttributeName: aws.String("ExpiresAt"), Enabled: aws.Bool(true)}})
		ok(t, err, "UpdateTimeToLive")
		if spec := up.TimeToLiveSpecification; spec == nil || aws.ToString(spec.AttributeName) != "ExpiresAt" || !aws.ToBool(spec.Enabled) {
			t.Errorf("UpdateTimeToLive = %+v", spec)
		}
		after, err := client.DescribeTimeToLive(ctx, &dynamodb.DescribeTimeToLiveInput{TableName: aws.String(tableName)})
		ok(t, err, "DescribeTimeToLive")
		if d := after.TimeToLiveDescription; d == nil || (d.TimeToLiveStatus != types.TimeToLiveStatusEnabled && d.TimeToLiveStatus != types.TimeToLiveStatusEnabling) ||
			aws.ToString(d.AttributeName) != "ExpiresAt" {
			t.Errorf("TTL after enabling = %+v, want ENABLING or ENABLED on ExpiresAt", d)
		}
	})

	t.Run("continuous backups", func(t *testing.T) {
		out, err := client.DescribeContinuousBackups(ctx, &dynamodb.DescribeContinuousBackupsInput{TableName: aws.String(tableName)})
		ok(t, err, "DescribeContinuousBackups")
		d := out.ContinuousBackupsDescription
		if d == nil || d.ContinuousBackupsStatus != types.ContinuousBackupsStatusEnabled || d.PointInTimeRecoveryDescription == nil ||
			d.PointInTimeRecoveryDescription.PointInTimeRecoveryStatus != types.PointInTimeRecoveryStatusDisabled {
			t.Errorf("DescribeContinuousBackups of a new table = %+v, want backups ENABLED and PITR DISABLED", d)
		}
		up, err := client.UpdateContinuousBackups(ctx, &dynamodb.UpdateContinuousBackupsInput{TableName: aws.String(tableName), PointInTimeRecoverySpecification: &types.PointInTimeRecoverySpecification{PointInTimeRecoveryEnabled: aws.Bool(true)}})
		ok(t, err, "UpdateContinuousBackups")
		p := up.ContinuousBackupsDescription.PointInTimeRecoveryDescription
		if p == nil || p.PointInTimeRecoveryStatus != types.PointInTimeRecoveryStatusEnabled || p.EarliestRestorableDateTime == nil || p.LatestRestorableDateTime == nil {
			t.Errorf("PITR after enabling = %+v, want ENABLED with restorable times", p)
		}
	})
}

func TestDynamoDBItems(t *testing.T) {
	t.Parallel()
	requireDynamoDB(t)
	ctx := t.Context()
	client := dynamodb.NewFromConfig(awsCfg)
	tableName := name("items")
	createLockTable(t, tableName)
	table := aws.String(tableName)

	t.Run("lock acquisition with a conditional write", func(t *testing.T) {
		const cond = "attribute_not_exists(LockID) OR ExpiresAt < :now"
		acquire := func(owner string, now, expires int64) error {
			_, err := client.PutItem(ctx, &dynamodb.PutItemInput{
				TableName: table, Item: map[string]types.AttributeValue{"LockID": ddbS("job-1"), "Owner": ddbS(owner), "ExpiresAt": ddbN(expires)},
				ConditionExpression: aws.String(cond), ExpressionAttributeValues: map[string]types.AttributeValue{":now": ddbN(now)},
			})
			return err
		}
		ok(t, acquire("a", 1000, 1060), "first acquisition")
		err := acquire("b", 1030, 1090)
		cf := typedErr[*types.ConditionalCheckFailedException](t, err, "acquiring a held lock")
		if cf.ErrorMessage() != "The conditional request failed" {
			t.Errorf("ConditionalCheckFailedException message = %q", cf.ErrorMessage())
		}
		wantStatus(t, err, "acquiring a held lock", 400)
		ok(t, acquire("b", 1061, 1120), "acquiring an expired lock")
		got, err := client.GetItem(ctx, &dynamodb.GetItemInput{TableName: table, Key: map[string]types.AttributeValue{"LockID": ddbS("job-1")}, ConsistentRead: aws.Bool(true)})
		ok(t, err, "GetItem")
		if owner, isS := got.Item["Owner"].(*types.AttributeValueMemberS); !isS || owner.Value != "b" {
			t.Errorf("lock owner = %#v, want b", got.Item["Owner"])
		}
		_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: table, Item: map[string]types.AttributeValue{"LockID": ddbS("job-1"), "Owner": ddbS("c")},
			ConditionExpression: aws.String("attribute_not_exists(LockID)"), ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureAllOld,
		})
		cf = typedErr[*types.ConditionalCheckFailedException](t, err, "conditional put with ReturnValuesOnConditionCheckFailure")
		if owner, isS := cf.Item["Owner"].(*types.AttributeValueMemberS); !isS || owner.Value != "b" {
			t.Errorf("ConditionalCheckFailedException Item = %#v, want the current item", cf.Item)
		}
	})

	t.Run("UpdateItem and DeleteItem", func(t *testing.T) {
		out, err := client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: table, Key: map[string]types.AttributeValue{"LockID": ddbS("counter")},
			UpdateExpression: aws.String("ADD Hits :one SET Label = :label"), ReturnValues: types.ReturnValueAllNew,
			ExpressionAttributeValues: map[string]types.AttributeValue{":one": ddbN(1), ":label": ddbS("x")},
		})
		ok(t, err, "UpdateItem")
		if hits, isN := out.Attributes["Hits"].(*types.AttributeValueMemberN); !isN || hits.Value != "1" {
			t.Errorf("UpdateItem ALL_NEW Hits = %#v, want 1", out.Attributes["Hits"])
		}
		del, err := client.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: table, Key: map[string]types.AttributeValue{"LockID": ddbS("counter")}, ReturnValues: types.ReturnValueAllOld})
		ok(t, err, "DeleteItem")
		if _, has := del.Attributes["Label"]; !has {
			t.Errorf("DeleteItem ALL_OLD = %v", del.Attributes)
		}
		got, err := client.GetItem(ctx, &dynamodb.GetItemInput{TableName: table, Key: map[string]types.AttributeValue{"LockID": ddbS("counter")}})
		ok(t, err, "GetItem after delete")
		if got.Item != nil {
			t.Errorf("GetItem after delete = %v, want no item", got.Item)
		}
	})

	t.Run("batch, scan and query", func(t *testing.T) {
		var writes []types.WriteRequest
		for i := range 5 {
			writes = append(writes, types.WriteRequest{PutRequest: &types.PutRequest{Item: map[string]types.AttributeValue{"LockID": ddbS("batch-" + strconv.Itoa(i)), "N": ddbN(int64(i))}}})
		}
		out, err := client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: map[string][]types.WriteRequest{tableName: writes}})
		ok(t, err, "BatchWriteItem")
		if len(out.UnprocessedItems) != 0 {
			t.Errorf("BatchWriteItem UnprocessedItems = %v", out.UnprocessedItems)
		}
		get, err := client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: map[string]types.KeysAndAttributes{tableName: {Keys: []map[string]types.AttributeValue{
			{"LockID": ddbS("batch-0")}, {"LockID": ddbS("batch-4")}, {"LockID": ddbS("batch-missing")},
		}}}})
		ok(t, err, "BatchGetItem")
		if len(get.Responses[tableName]) != 2 {
			t.Errorf("BatchGetItem returned %d items, want 2", len(get.Responses[tableName]))
		}
		scan, err := client.Scan(ctx, &dynamodb.ScanInput{
			TableName: table, FilterExpression: aws.String("begins_with(LockID, :p) AND N >= :min"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":p": ddbS("batch-"), ":min": ddbN(3)},
		})
		ok(t, err, "Scan")
		if scan.Count != 2 {
			t.Errorf("Scan with a filter = Count %d, want 2", scan.Count)
		}
		q, err := client.Query(ctx, &dynamodb.QueryInput{
			TableName: table, KeyConditionExpression: aws.String("LockID = :k"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":k": ddbS("batch-2")},
		})
		ok(t, err, "Query")
		if q.Count != 1 {
			t.Errorf("Query Count = %d, want 1", q.Count)
		}
	})

	t.Run("transactions", func(t *testing.T) {
		_, err := client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: table, Item: map[string]types.AttributeValue{"LockID": ddbS("tx-a")}}},
			{Put: &types.Put{TableName: table, Item: map[string]types.AttributeValue{"LockID": ddbS("batch-0")}, ConditionExpression: aws.String("attribute_not_exists(LockID)")}},
		}})
		tc := typedErr[*types.TransactionCanceledException](t, err, "TransactWriteItems with a failing condition")
		if len(tc.CancellationReasons) != 2 || aws.ToString(tc.CancellationReasons[0].Code) != "None" || aws.ToString(tc.CancellationReasons[1].Code) != "ConditionalCheckFailed" {
			t.Errorf("CancellationReasons = %+v, want [None ConditionalCheckFailed]", tc.CancellationReasons)
		}
		got, err := client.GetItem(ctx, &dynamodb.GetItemInput{TableName: table, Key: map[string]types.AttributeValue{"LockID": ddbS("tx-a")}})
		ok(t, err, "GetItem")
		if got.Item != nil {
			t.Errorf("a cancelled transaction wrote tx-a")
		}
		read, err := client.TransactGetItems(ctx, &dynamodb.TransactGetItemsInput{TransactItems: []types.TransactGetItem{
			{Get: &types.Get{TableName: table, Key: map[string]types.AttributeValue{"LockID": ddbS("batch-1")}}},
			{Get: &types.Get{TableName: table, Key: map[string]types.AttributeValue{"LockID": ddbS("tx-a")}}},
		}})
		ok(t, err, "TransactGetItems")
		if len(read.Responses) != 2 || read.Responses[0].Item == nil || read.Responses[1].Item != nil {
			t.Errorf("TransactGetItems = %+v, want batch-1 and an empty second response", read.Responses)
		}
	})

	t.Run("PartiQL", func(t *testing.T) {
		_, err := client.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
			Statement: aws.String(`INSERT INTO "` + tableName + `" VALUE {'LockID': ?, 'Holder': ?}`), Parameters: []types.AttributeValue{ddbS("pq-1"), ddbS("p")},
		})
		ok(t, err, "ExecuteStatement INSERT")
		sel, err := client.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
			Statement: aws.String(`SELECT * FROM "` + tableName + `" WHERE LockID = ?`), Parameters: []types.AttributeValue{ddbS("pq-1")},
		})
		ok(t, err, "ExecuteStatement SELECT")
		if len(sel.Items) != 1 {
			t.Errorf("ExecuteStatement SELECT = %d items, want 1", len(sel.Items))
		}
		batch, err := client.BatchExecuteStatement(ctx, &dynamodb.BatchExecuteStatementInput{Statements: []types.BatchStatementRequest{
			{Statement: aws.String(`SELECT * FROM "` + tableName + `" WHERE LockID = ?`), Parameters: []types.AttributeValue{ddbS("pq-1")}},
		}})
		ok(t, err, "BatchExecuteStatement")
		if len(batch.Responses) != 1 || batch.Responses[0].Item == nil {
			t.Errorf("BatchExecuteStatement = %+v", batch.Responses)
		}
		_, err = client.ExecuteTransaction(ctx, &dynamodb.ExecuteTransactionInput{TransactStatements: []types.ParameterizedStatement{
			{Statement: aws.String(`UPDATE "` + tableName + `" SET Holder = ? WHERE LockID = ?`), Parameters: []types.AttributeValue{ddbS("q"), ddbS("pq-1")}},
		}})
		ok(t, err, "ExecuteTransaction")
	})

	t.Run("UpdateTable and DescribeLimits", func(t *testing.T) {
		out, err := client.UpdateTable(ctx, &dynamodb.UpdateTableInput{TableName: table, DeletionProtectionEnabled: aws.Bool(false), TableClass: types.TableClassStandardInfrequentAccess})
		ok(t, err, "UpdateTable")
		if out.TableDescription == nil || aws.ToString(out.TableDescription.TableName) != tableName {
			t.Errorf("UpdateTable = %+v", out.TableDescription)
		}
		err = dynamodb.NewTableExistsWaiter(client).Wait(ctx, &dynamodb.DescribeTableInput{TableName: table}, 3*time.Minute)
		ok(t, err, "waiting for the table to be ACTIVE again")
		desc, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: table})
		ok(t, err, "DescribeTable")
		if desc.Table.TableClassSummary == nil || desc.Table.TableClassSummary.TableClass != types.TableClassStandardInfrequentAccess {
			t.Errorf("TableClassSummary after UpdateTable = %+v", desc.Table.TableClassSummary)
		}
		limits, err := client.DescribeLimits(ctx, &dynamodb.DescribeLimitsInput{})
		ok(t, err, "DescribeLimits")
		if aws.ToInt64(limits.TableMaxReadCapacityUnits) <= 0 || aws.ToInt64(limits.AccountMaxReadCapacityUnits) <= 0 {
			t.Errorf("DescribeLimits = %+v", limits)
		}
	})
}
