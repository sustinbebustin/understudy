//go:build docker

package dynamodb_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// A save and load on the pinned DynamoDB Local keep the stream's label and
// records; the load's own writes to the re-created table emit nothing, and
// the next write is the only new record, numbered after the restored ones
// (ADR 0009 amendment of 2026-10-27; dynamodb-streams.md 11.1 item 7).
func TestStreamsSnapshotKeepsLabelAndRecordsDocker(t *testing.T) {
	k, svc := newStreamsKernel(t, startLocal(t))
	scope := scopeOf(t, "111111111111", "us-east-1")
	createStreamed(t, k, scope, "Orders", "")
	for _, id := range []string{"a", "b"} {
		mustSend[dynamodbapi.PutItemInput](t, k, scope, dynamodbapi.OpPutItemRaw, `{"TableName":"Orders","Item":{"id":{"S":"`+id+`"}}}`)
	}
	streams := func() []dynamodbstreamsapi.Stream {
		return streamsCall(t, k, scope, dynamodbstreamsapi.OpListStreams, &dynamodbstreamsapi.ListStreamsInput{}).Streams
	}
	listed := streams()
	before := allStreamRecords(t, k, scope, "Orders")
	if len(listed) != 1 || len(before) != 2 {
		t.Fatalf("before the save: %d streams and %d records, want 1 and 2", len(listed), len(before))
	}

	items, err := svc.ExportLate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cells, err := svc.Export()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetLate(t.Context(), kernel.ResetScope{}); err != nil {
		t.Fatal(err)
	}
	svc.Reset(kernel.ResetScope{})
	if err := svc.ImportLate(t.Context(), items); err != nil {
		t.Fatal(err)
	}
	if err := svc.Import(cells); err != nil {
		t.Fatal(err)
	}
	svc.Rearm(k.Clock().Now())

	after := streams()
	if len(after) != 1 || *after[0].StreamArn != *listed[0].StreamArn || *after[0].StreamLabel != *listed[0].StreamLabel {
		t.Fatalf("streams after the load = %+v, want %+v", after, listed)
	}
	resp, aerr := send(t, k, scope, dynamodbapi.OpDescribeTableRaw, `{"TableName":"Orders"}`)
	if aerr != nil || resp.Status != http.StatusOK {
		t.Fatalf("DescribeTable = %d %s %v", resp.Status, resp.Body, aerr)
	}
	var desc struct {
		Table struct{ LatestStreamLabel string }
	}
	if err := json.Unmarshal(resp.Body, &desc); err != nil {
		t.Fatal(err)
	}
	if desc.Table.LatestStreamLabel != *listed[0].StreamLabel {
		t.Errorf("DescribeTable's LatestStreamLabel after the load = %q, want %q", desc.Table.LatestStreamLabel, *listed[0].StreamLabel)
	}
	got := allStreamRecords(t, k, scope, "Orders")
	if len(got) != len(before) {
		t.Fatalf("%d records after the load, want the %d saved and none for the load's writes", len(got), len(before))
	}
	for i := range got {
		if *got[i].EventID != *before[i].EventID || *got[i].Dynamodb.SequenceNumber != *before[i].Dynamodb.SequenceNumber {
			t.Errorf("record %d after the load = %s %s, want %s %s", i,
				*got[i].EventID, *got[i].Dynamodb.SequenceNumber, *before[i].EventID, *before[i].Dynamodb.SequenceNumber)
		}
	}
	mustSend[dynamodbapi.PutItemInput](t, k, scope, dynamodbapi.OpPutItemRaw, `{"TableName":"Orders","Item":{"id":{"S":"c"}}}`)
	got = allStreamRecords(t, k, scope, "Orders")
	if len(got) != len(before)+1 {
		t.Fatalf("%d records after a write following the load, want %d", len(got), len(before)+1)
	}
	key, ok := got[len(got)-1].Dynamodb.Keys["id"].(dynamodbstreamsapi.AttributeValueMemberS)
	last, restored := shardlog.Seq(*got[len(got)-1].Dynamodb.SequenceNumber), shardlog.Seq(*before[len(before)-1].Dynamodb.SequenceNumber)
	if !ok || key.Value != "c" || last.Compare(restored) <= 0 {
		t.Errorf("the write after the load gave %+v, want c numbered after the restored records", got[len(got)-1])
	}
}
