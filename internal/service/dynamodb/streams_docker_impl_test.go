//go:build docker

package dynamodb_test

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// sImage is an image of string attributes, the only kind the goldens
// below hold; any other attribute fails the test.
type sImage map[string]string

func sImageOf(t *testing.T, im map[string]dynamodbstreamsapi.AttributeValue) sImage {
	t.Helper()
	if im == nil {
		return nil
	}
	out := make(sImage, len(im))
	for k, v := range im {
		s, ok := v.(dynamodbstreamsapi.AttributeValueMemberS)
		if !ok {
			t.Fatalf("attribute %s = %#v, want a string", k, v)
		}
		out[k] = s.Value
	}
	return out
}

// goldenRecord is what a golden compares of a GetRecords record: the
// parts AWS recorded that are not placeholders.
type goldenRecord struct {
	Event              string
	Keys, New, Old     sImage
	SizeBytes          int64
	ViewType           string
	HasUserIdentityKey bool
}

func goldenOf(t *testing.T, rs []dynamodbstreamsapi.Record) []goldenRecord {
	t.Helper()
	out := make([]goldenRecord, 0, len(rs))
	for i := range rs {
		r := &rs[i]
		if r.EventName == nil || r.Dynamodb == nil || r.Dynamodb.SizeBytes == nil || r.Dynamodb.StreamViewType == nil {
			t.Fatalf("record %d lacks a member: %+v", i, r)
		}
		out = append(out, goldenRecord{
			Event: string(*r.EventName), Keys: sImageOf(t, r.Dynamodb.Keys), New: sImageOf(t, r.Dynamodb.NewImage), Old: sImageOf(t, r.Dynamodb.OldImage),
			SizeBytes: *r.Dynamodb.SizeBytes, ViewType: string(*r.Dynamodb.StreamViewType), HasUserIdentityKey: r.UserIdentity != nil,
		})
	}
	return out
}

// byItem groups rs by item key, keeping their order: AWS orders a stream's
// records per item only, "not across an entire partition"
// (dynamodb-streams.md 7.7), and one BatchWriteItem's records of distinct
// items come out of DynamoDB Local in another order than AWS recorded.
func byItem(rs []goldenRecord) map[string][]goldenRecord {
	out := map[string][]goldenRecord{}
	for _, r := range rs {
		k := fmt.Sprint(r.Keys)
		out[k] = append(out[k], r)
	}
	return out
}

// mustSend sends body as op and fails the test on anything but a 200.
func mustSend[In any, P interface {
	*In
	smithy.Deserializable
}](t *testing.T, k *kernel.Kernel, scope aws.Scope, op kernel.Op[In, kernel.RawOutput], body string) {
	t.Helper()
	resp, aerr := send[In, P](t, k, scope, op, body)
	if aerr != nil || resp.Status != http.StatusOK {
		t.Fatalf("%s %s = %d %s %v", op.Descriptor().ID.Name, body, resp.Status, resp.Body, errText(aerr))
	}
}

func errText(e *awserr.Error) string {
	if e == nil {
		return ""
	}
	return e.ID.JSONType + " " + e.Message
}

// createTableBody is a CreateTable of table keyed by the string key, with a
// stream of view when view is not empty.
func createTableBody(table, key, view string) string {
	body := `{"TableName":"` + table + `","AttributeDefinitions":[{"AttributeName":"` + key + `","AttributeType":"S"}],` +
		`"KeySchema":[{"AttributeName":"` + key + `","KeyType":"HASH"}],"ProvisionedThroughput":{"ReadCapacityUnits":5,"WriteCapacityUnits":5}`
	if view != "" {
		body += `,"StreamSpecification":{"StreamEnabled":true,"StreamViewType":"` + view + `"}`
	}
	return body + "}"
}

// LocalStack goldens 1-5 (spec A4): the record sequences AWS returned for
// each test's writes, compared at the parsed level, item by item (byItem),
// against understudy's stream fed by the pinned DynamoDB Local. Sources, each
// localstack@8b9a79f tests/aws/services/dynamodb/test_dynamodb.snapshot.json
// with the steps of the matching test_dynamodb.py:
//  1. L666-L856 test_dynamodb_stream_stream_view_type, records L743-L853, validated 2025-10-08
//  2. L466-L661 test_batch_write_items_streaming, validated 2025-10-08
//  3. L883-L1154 test_transact_write_items_streaming, validated 2025-10-08
//  4. L1155-L1292 test_transact_write_items_streaming_for_different_tables, validated 2025-10-08
//  5. L1293-L1444 test_dynamodb_stream_records_with_update_item, validated 2024-06-26
//
// No recorded record carries a userIdentity key (dynamodb-streams.md 7.5).
func TestStreamsLocalStackGoldensDocker(t *testing.T) {
	const nai = "NEW_AND_OLD_IMAGES"
	fred, newKey := sImage{"id": "Fred"}, sImage{"id": "NewKey"}
	newKeyAttrs := sImage{"id": "NewKey", "attr1": "value1", "attr2": "value2"}
	fredNamed := sImage{"id": "Fred", "name": "Fred"}
	cases := []struct {
		name  string
		setup []string // CreateTable bodies; the first table's stream is read
		steps func(t *testing.T, k *kernel.Kernel, scope aws.Scope)
		want  []goldenRecord
	}{
		{
			name:  "1 stream view type",
			setup: []string{createTableBody("Gold1", "Username", "KEYS_ONLY")},
			steps: func(t *testing.T, k *kernel.Kernel, scope aws.Scope) {
				t.Helper()
				mustSend(t, k, scope, dynamodbapi.OpPutItemRaw, `{"TableName":"Gold1","Item":{"Username":{"S":"Fred"}}}`)
				mustSend(t, k, scope, dynamodbapi.OpPutItemRaw, `{"TableName":"Gold1","Item":{"Username":{"S":"Fred"}}}`)
				mustSend(t, k, scope, dynamodbapi.OpUpdateItemRaw, `{"TableName":"Gold1","Key":{"Username":{"S":"Fred"}},"UpdateExpression":"set S=:r","ExpressionAttributeValues":{":r":{"S":"Fred_Modified"}},"ReturnValues":"UPDATED_NEW"}`)
				mustSend(t, k, scope, dynamodbapi.OpDeleteItemRaw, `{"TableName":"Gold1","Key":{"Username":{"S":"Fred"}}}`)
				mustSend(t, k, scope, dynamodbapi.OpExecuteStatementRaw, `{"Statement":"INSERT INTO Gold1 VALUE {'Username': 'Alice'}"}`)
				mustSend(t, k, scope, dynamodbapi.OpExecuteStatementRaw, `{"Statement":"UPDATE Gold1 SET partiql=1 WHERE Username='Alice'"}`)
				mustSend(t, k, scope, dynamodbapi.OpExecuteStatementRaw, `{"Statement":"DELETE FROM Gold1 WHERE Username='Alice'"}`)
			},
			want: []goldenRecord{
				{Event: "INSERT", Keys: sImage{"Username": "Fred"}, SizeBytes: 12, ViewType: "KEYS_ONLY"},
				{Event: "MODIFY", Keys: sImage{"Username": "Fred"}, SizeBytes: 12, ViewType: "KEYS_ONLY"},
				{Event: "REMOVE", Keys: sImage{"Username": "Fred"}, SizeBytes: 12, ViewType: "KEYS_ONLY"},
				{Event: "INSERT", Keys: sImage{"Username": "Alice"}, SizeBytes: 13, ViewType: "KEYS_ONLY"},
				{Event: "MODIFY", Keys: sImage{"Username": "Alice"}, SizeBytes: 13, ViewType: "KEYS_ONLY"},
				{Event: "REMOVE", Keys: sImage{"Username": "Alice"}, SizeBytes: 13, ViewType: "KEYS_ONLY"},
			},
		},
		{
			name:  "2 batch write",
			setup: []string{createTableBody("Gold2", "id", nai)},
			steps: func(t *testing.T, k *kernel.Kernel, scope aws.Scope) {
				t.Helper()
				mustSend(t, k, scope, dynamodbapi.OpPutItemRaw, `{"TableName":"Gold2","Item":{"id":{"S":"Fred"}}}`)
				mustSend(t, k, scope, dynamodbapi.OpBatchWriteItemRaw, `{"RequestItems":{"Gold2":[{"PutRequest":{"Item":{"id":{"S":"Fred"}}}},{"PutRequest":{"Item":{"id":{"S":"NewKey"}}}}]}}`)
				mustSend(t, k, scope, dynamodbapi.OpBatchWriteItemRaw, `{"RequestItems":{"Gold2":[{"DeleteRequest":{"Key":{"id":{"S":"NewKey"}}}},{"PutRequest":{"Item":{"id":{"S":"Fred"},"name":{"S":"Fred"}}}}]}}`)
			},
			want: []goldenRecord{
				{Event: "INSERT", Keys: fred, New: fred, SizeBytes: 12, ViewType: nai},
				{Event: "INSERT", Keys: newKey, New: newKey, SizeBytes: 16, ViewType: nai},
				{Event: "REMOVE", Keys: newKey, Old: newKey, SizeBytes: 16, ViewType: nai},
				{Event: "MODIFY", Keys: fred, New: fredNamed, Old: fred, SizeBytes: 26, ViewType: nai},
			},
		},
		{
			name:  "3 transact write",
			setup: []string{createTableBody("Gold3", "id", nai)},
			steps: func(t *testing.T, k *kernel.Kernel, scope aws.Scope) {
				t.Helper()
				mustSend(t, k, scope, dynamodbapi.OpPutItemRaw, `{"TableName":"Gold3","Item":{"id":{"S":"Fred"}}}`)
				mustSend(t, k, scope, dynamodbapi.OpTransactWriteItemsRaw, `{"TransactItems":[{"Put":{"TableName":"Gold3","Item":{"id":{"S":"Fred"}}}},{"Put":{"TableName":"Gold3","Item":{"id":{"S":"NewKey"}}}}]}`)
				mustSend(t, k, scope, dynamodbapi.OpTransactWriteItemsRaw, `{"TransactItems":[{"Update":{"TableName":"Gold3","Key":{"id":{"S":"NewKey"}},"UpdateExpression":"SET attr1 = :v1, attr2 = :v2","ExpressionAttributeValues":{":v1":{"S":"value1"},":v2":{"S":"value2"}}}}]}`)
				mustSend(t, k, scope, dynamodbapi.OpTransactWriteItemsRaw, `{"TransactItems":[{"Update":{"TableName":"Gold3","Key":{"id":{"S":"NonExistentKey"}},"UpdateExpression":"SET attr1 = :v1","ExpressionAttributeValues":{":v1":{"S":"value1"}}}}]}`)
				mustSend(t, k, scope, dynamodbapi.OpTransactWriteItemsRaw, `{"TransactItems":[{"Delete":{"TableName":"Gold3","Key":{"id":{"S":"NewKey"}}}},`+
					`{"Put":{"TableName":"Gold3","Item":{"id":{"S":"Fred"},"name":{"S":"Fred"}}}},`+
					`{"Update":{"TableName":"Gold3","Key":{"id":{"S":"NonExistentKey"}},"UpdateExpression":"SET attr1 = :v1","ExpressionAttributeValues":{":v1":{"S":"value1"}}}}]}`)
			},
			want: []goldenRecord{
				{Event: "INSERT", Keys: fred, New: fred, SizeBytes: 12, ViewType: nai},
				{Event: "INSERT", Keys: newKey, New: newKey, SizeBytes: 16, ViewType: nai},
				{Event: "MODIFY", Keys: newKey, New: newKeyAttrs, Old: newKey, SizeBytes: 46, ViewType: nai},
				{Event: "INSERT", Keys: sImage{"id": "NonExistentKey"}, New: sImage{"id": "NonExistentKey", "attr1": "value1"}, SizeBytes: 43, ViewType: nai},
				{Event: "REMOVE", Keys: newKey, Old: newKeyAttrs, SizeBytes: 38, ViewType: nai},
				{Event: "MODIFY", Keys: fred, New: fredNamed, Old: fred, SizeBytes: 26, ViewType: nai},
			},
		},
		{
			name:  "4 transact write to two tables",
			setup: []string{createTableBody("Gold4", "id", nai), createTableBody("Gold4Plain", "id", "")},
			steps: func(t *testing.T, k *kernel.Kernel, scope aws.Scope) {
				t.Helper()
				mustSend(t, k, scope, dynamodbapi.OpTransactWriteItemsRaw, `{"TransactItems":[{"Put":{"TableName":"Gold4Plain","Item":{"id":{"S":"Fred"}}}},{"Put":{"TableName":"Gold4","Item":{"id":{"S":"Fred"}}}}]}`)
			},
			want: []goldenRecord{{Event: "INSERT", Keys: fred, New: fred, SizeBytes: 12, ViewType: nai}},
		},
		{
			name:  "5 update item",
			setup: []string{createTableBody("Gold5", "id", nai)},
			steps: func(t *testing.T, k *kernel.Kernel, scope aws.Scope) {
				t.Helper()
				set := func(v1, v2 string) string {
					return `{"TableName":"Gold5","Key":{"id":{"S":"my-item-id"}},"UpdateExpression":"SET attr1 = :v1, attr2 = :v2","ExpressionAttributeValues":{":v1":{"S":"` + v1 + `"},":v2":{"S":"` + v2 + `"}}}`
				}
				mustSend(t, k, scope, dynamodbapi.OpUpdateItemRaw, set("value1", "value2"))
				mustSend(t, k, scope, dynamodbapi.OpUpdateItemRaw, set("value1", "value2"))
				mustSend(t, k, scope, dynamodbapi.OpUpdateItemRaw, set("value2", "value3"))
			},
			want: []goldenRecord{
				{Event: "INSERT", Keys: sImage{"id": "my-item-id"}, New: sImage{"id": "my-item-id", "attr1": "value1", "attr2": "value2"}, SizeBytes: 46, ViewType: nai},
				{
					Event: "MODIFY", Keys: sImage{"id": "my-item-id"}, New: sImage{"id": "my-item-id", "attr1": "value2", "attr2": "value3"},
					Old: sImage{"id": "my-item-id", "attr1": "value1", "attr2": "value2"}, SizeBytes: 80, ViewType: nai,
				},
			},
		},
	}
	k, _ := newStreamsKernel(t, startLocal(t))
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One account per case keeps the tables of the cases apart in
			// the one Local.
			scope := scopeOf(t, "1111111111"+strconv.Itoa(10+i), "us-east-1")
			for _, body := range tc.setup {
				mustSend(t, k, scope, dynamodbapi.OpCreateTableRaw, body)
			}
			tc.steps(t, k, scope)
			streams := streamsCall(t, k, scope, dynamodbstreamsapi.OpListStreams, &dynamodbstreamsapi.ListStreamsInput{})
			if len(streams.Streams) != 1 {
				t.Fatalf("%d streams, want the one stream-enabled table's", len(streams.Streams))
			}
			got := goldenOf(t, allStreamRecords(t, k, scope, *streams.Streams[0].TableName))
			if d := cmp.Diff(byItem(tc.want), byItem(got)); d != "" {
				t.Errorf("records per item (-AWS +understudy):\n%s", d)
			}
		})
	}
}

// Concurrent writers each find their own record stamped with their own
// admission instant, and no record carries userIdentity
// (dynamodb-streams.md 11.1 item 2, 7.5). TestStreamsAttributeConcurrentWritesDocker
// asserts the same but reads the key as the AWS SDK's pointer variant,
// which the generated union never holds.
func TestStreamsConcurrentWritesStampedDocker(t *testing.T) {
	k, _ := newStreamsKernel(t, startLocal(t))
	scope := scopeOf(t, "111111111111", "us-east-1")
	createStreamed(t, k, scope, "Orders", "")
	const writers = 12
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			at := clock.FromWire(streamsEpoch.Add(time.Duration(i+1) * time.Second))
			resp, aerr := sendAt(t, k, scope, dynamodbapi.OpPutItemRaw, `{"TableName":"Orders","Item":{"id":{"S":"`+strconv.Itoa(i)+`"}}}`, at)
			if aerr != nil || resp.Status != http.StatusOK {
				t.Errorf("writer %d: %d %s %s", i, resp.Status, resp.Body, errText(aerr))
			}
		})
	}
	wg.Wait()
	records := allStreamRecords(t, k, scope, "Orders")
	if len(records) != writers {
		t.Fatalf("%d records, want %d", len(records), writers)
	}
	seen := make([]int, 0, writers)
	for i := range records {
		r := &records[i]
		i, err := strconv.Atoi(sImageOf(t, r.Dynamodb.Keys)["id"])
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, i)
		if want := streamsEpoch.Add(time.Duration(i+1) * time.Second); r.Dynamodb.ApproximateCreationDateTime == nil || !r.Dynamodb.ApproximateCreationDateTime.Equal(want) {
			t.Errorf("record of writer %d at %v, want its write's %v", i, r.Dynamodb.ApproximateCreationDateTime, want)
		}
		if r.UserIdentity != nil {
			t.Errorf("record of writer %d carries userIdentity", i)
		}
	}
	slices.Sort(seen)
	if len(slices.Compact(seen)) != writers {
		t.Errorf("writers %v, want each once", seen)
	}
}

// A TTL sweep's delete is a REMOVE with userIdentity
// {"PrincipalId":"dynamodb.amazonaws.com","Type":"Service"}, and an
// ordinary DeleteItem has none (dynamodb-streams.md 7.5).
// TestStreamsTagOnlySweeperDeletesDocker asserts the same but reads keys
// as pointer variants.
func TestStreamsSweeperDeletesCarryUserIdentityDocker(t *testing.T) {
	k, svc := newStreamsKernel(t, startLocal(t))
	scope := scopeOf(t, "111111111111", "us-east-1")
	createStreamed(t, k, scope, "Locks", "")
	mustSend(t, k, scope, dynamodbapi.OpUpdateTimeToLiveRaw, `{"TableName":"Locks","TimeToLiveSpecification":{"AttributeName":"exp","Enabled":true}}`)
	expired := strconv.FormatInt(streamsEpoch.Add(-time.Minute).Unix(), 10)
	mustSend(t, k, scope, dynamodbapi.OpPutItemRaw, `{"TableName":"Locks","Item":{"id":{"S":"swept"},"exp":{"N":"`+expired+`"}}}`)
	mustSend(t, k, scope, dynamodbapi.OpPutItemRaw, `{"TableName":"Locks","Item":{"id":{"S":"deleted"}}}`)
	mustSend(t, k, scope, dynamodbapi.OpDeleteItemRaw, `{"TableName":"Locks","Key":{"id":{"S":"deleted"}}}`)
	if _, err := k.Settle(t.Context(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SweepTTL(t.Context()); err != nil {
		t.Fatalf("SweepTTL: %v", err)
	}
	removes := map[string]*dynamodbstreamsapi.Identity{}
	records := allStreamRecords(t, k, scope, "Locks")
	for i := range records {
		r := &records[i]
		if r.EventName != nil && *r.EventName == dynamodbstreamsapi.OperationTypeRemove {
			removes[sImageOf(t, r.Dynamodb.Keys)["id"]] = r.UserIdentity
		}
	}
	if len(removes) != 2 {
		t.Fatalf("REMOVE records %v, want the DeleteItem's and the sweep's", removes)
	}
	if removes["deleted"] != nil {
		t.Errorf("the DeleteItem's REMOVE carries userIdentity %+v", removes["deleted"])
	}
	if u := removes["swept"]; u == nil || u.PrincipalId == nil || *u.PrincipalId != "dynamodb.amazonaws.com" || u.Type == nil || *u.Type != "Service" {
		t.Errorf("the sweep's REMOVE userIdentity = %+v, want dynamodb.amazonaws.com Service", u)
	}
}
