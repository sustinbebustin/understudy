package e2e_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// lockItem reads the lock item of message id; ok is false when absent.
func lockItem(t *testing.T, id string) (item map[string]ddbtypes.AttributeValue, ok bool) {
	t.Helper()
	out, err := env.ddb.GetItem(t.Context(), &dynamodb.GetItemInput{
		TableName:      aws.String(lockTable),
		Key:            map[string]ddbtypes.AttributeValue{"LockID": &ddbtypes.AttributeValueMemberS{Value: "lock#" + id}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("dynamodb GetItem lock#%s: %v", id, err)
	}
	return out.Item, len(out.Item) > 0
}

func stringAttr(item map[string]ddbtypes.AttributeValue, name string) string {
	switch v := item[name].(type) {
	case *ddbtypes.AttributeValueMemberS:
		return v.Value
	case *ddbtypes.AttributeValueMemberN:
		return v.Value
	default:
		return ""
	}
}

// TestDynamoDBWorkersTakeAConditionalLock checks the lock the Go worker
// wrote: owner, message and an expiry of SentTimestamp plus the TTL. A
// second conditional write while the lock is unexpired fails with
// ConditionalCheckFailedException, so a duplicate message is skipped and
// the first record stays.
func TestDynamoDBWorkersTakeAConditionalLock(t *testing.T) {
	pipe(t)
	id := newID(t)
	publish(t, message{ID: id, Note: "first"}, map[string]string{"kind": "order"})
	rec := waitProcessed(t, goWorker, id)

	item, ok := lockItem(t, id)
	if !ok {
		t.Fatalf("no lock item lock#%s after the worker processed the message", id)
	}
	sent, err := strconv.ParseInt(rec.Record.Attributes["SentTimestamp"], 10, 64)
	if err != nil {
		t.Fatalf("SentTimestamp %q: %v", rec.Record.Attributes["SentTimestamp"], err)
	}
	wantExpiry := strconv.FormatInt(sent/1000+lockTTL, 10)
	if stringAttr(item, "Owner") != goWorker || stringAttr(item, "MessageId") != rec.Record.MessageID || stringAttr(item, "ExpiresAt") != wantExpiry {
		t.Errorf("lock item %v, want Owner %s, MessageId %s, ExpiresAt %s", item, goWorker, rec.Record.MessageID, wantExpiry)
	}

	now := emuNow(t).Unix()
	_, err = env.ddb.PutItem(t.Context(), &dynamodb.PutItemInput{
		TableName: aws.String(lockTable),
		Item: map[string]ddbtypes.AttributeValue{
			"LockID":    &ddbtypes.AttributeValueMemberS{Value: "lock#" + id},
			"ExpiresAt": &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now+lockTTL, 10)},
			"Owner":     &ddbtypes.AttributeValueMemberS{Value: "intruder"},
		},
		ConditionExpression:       aws.String("attribute_not_exists(LockID) OR ExpiresAt < :now"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":now": &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)}},
	})
	wantError(t, "PutItem over a held lock", err, "ConditionalCheckFailedException", 400)

	publish(t, message{ID: id, Note: "duplicate"}, map[string]string{"kind": "order"})
	waitLines(t, lambdaLogGroup(goWorker), eventPattern("locked", id), 1)
	again := waitProcessed(t, goWorker, id)
	if again.Record.MessageID != rec.Record.MessageID {
		t.Errorf("the record of %s was rewritten by message %s; the lock holder %s must keep it", id, again.Record.MessageID, rec.Record.MessageID)
	}
}

// TestDynamoDBTTLExpiresLocksOnTheEmulatorClock moves the clock to one
// second before a lock's ExpiresAt: a TTL sweep must keep it, since TTL
// never deletes an unexpired item. One second after expiry the sweep
// deletes it (dynamodb-local.md 5.1).
func TestDynamoDBTTLExpiresLocksOnTheEmulatorClock(t *testing.T) {
	pipe(t)
	ttl, err := env.ddb.DescribeTimeToLive(t.Context(), &dynamodb.DescribeTimeToLiveInput{TableName: aws.String(lockTable)})
	if err != nil {
		t.Fatalf("dynamodb DescribeTimeToLive: %v", err)
	}
	if d := ttl.TimeToLiveDescription; d == nil || d.TimeToLiveStatus != ddbtypes.TimeToLiveStatusEnabled || aws.ToString(d.AttributeName) != "ExpiresAt" {
		t.Errorf("TimeToLiveDescription %+v, want ENABLED on ExpiresAt", ttl.TimeToLiveDescription)
	}

	id := newID(t)
	publish(t, message{ID: id}, map[string]string{"kind": "order"})
	waitProcessed(t, goWorker, id)
	item, ok := lockItem(t, id)
	if !ok {
		t.Fatalf("no lock item lock#%s", id)
	}
	expiry, err := strconv.ParseInt(stringAttr(item, "ExpiresAt"), 10, 64)
	if err != nil {
		t.Fatalf("ExpiresAt %q: %v", stringAttr(item, "ExpiresAt"), err)
	}
	if d := time.Unix(expiry-1, 0).Sub(emuNow(t)); d > 0 {
		advance(t, d)
	}
	if _, err := env.admin.SweepTTL(t.Context()); err != nil {
		t.Fatalf("admin TTL sweep: %v", err)
	}
	if _, ok := lockItem(t, id); !ok {
		t.Fatalf("the TTL sweep deleted lock#%s one second before its ExpiresAt %d", id, expiry)
	}
	advance(t, 2*time.Second)
	if _, err := env.admin.SweepTTL(t.Context()); err != nil {
		t.Fatalf("admin TTL sweep: %v", err)
	}
	if item, ok := lockItem(t, id); ok {
		t.Errorf("lock#%s survived a TTL sweep one second after its ExpiresAt %d: %v", id, expiry, item)
	}
}
