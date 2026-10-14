//go:build docker

package dynamodb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// startLocal starts the pinned image, labeled for this test, and removes
// it when the test ends.
func startLocal(t *testing.T) *ddblocal.Manager {
	t.Helper()
	d := engine(t)
	own := ownership(t)
	m, err := ddblocal.New(ddblocal.Config{Docker: d, Labels: docker.Labels{Ownership: own, Test: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Stop(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return m
}

// running runs k's timers and tracked work until the test ends.
func running(t *testing.T, k *kernel.Kernel) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- k.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-stopped; err != nil {
			t.Errorf("kernel.Run = %v", err)
		}
	})
}

// realCall sends op with body in scope and fails the test unless it
// answers 200.
func realCall[In any, P interface {
	*In
	smithy.Deserializable
}](t *testing.T, k *kernel.Kernel, scope aws.Scope, op kernel.Op[In, kernel.RawOutput], body string) string {
	t.Helper()
	resp, aerr := send[In, P](t, k, scope, op, body)
	if aerr != nil || resp.Status != http.StatusOK {
		t.Fatalf("%s in %s/%s = %d %s %v", op.Descriptor().ID.Name, scope.Account, scope.Region, resp.Status, resp.Body, aerr)
	}
	return string(resp.Body)
}

// TestLateExportResetImportOnTheRealImage: tables in two accounts and
// regions, with indexes, a stream, deletion protection, every attribute
// type, tags, TTL and point-in-time recovery, are exported through the
// pinned image, deleted by the reset and rebuilt by the import: every item
// and everything the shim records comes back, and a second export is the
// same bytes. The TTL sweep's expressions run on the real image too.
// Local mints a new stream label for the re-created table, which the test
// logs as the deviation ARCHITECTURE.md 15 lists.
func TestLateExportResetImportOnTheRealImage(t *testing.T) {
	m := startLocal(t)
	k, svc := newRealKernel(t, m)
	running(t, k)
	east, west := scopeOf(t, "111111111111", "us-east-1"), scopeOf(t, "222222222222", "eu-west-1")

	realCall(t, k, east, dynamodbapi.OpCreateTableRaw, `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"},{"AttributeName":"owner","AttributeType":"S"}],`+
		`"TableName":"Locks","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST",`+
		`"GlobalSecondaryIndexes":[{"IndexName":"ByOwner","KeySchema":[{"AttributeName":"owner","KeyType":"HASH"}],"Projection":{"ProjectionType":"ALL"}}],`+
		`"StreamSpecification":{"StreamEnabled":true,"StreamViewType":"NEW_AND_OLD_IMAGES"},"DeletionProtectionEnabled":true,`+
		`"Tags":[{"Key":"team","Value":"core"}],"TableClass":"STANDARD_INFREQUENT_ACCESS"}`)
	realCall(t, k, east, dynamodbapi.OpUpdateTimeToLiveRaw, `{"TableName":"Locks","TimeToLiveSpecification":{"AttributeName":"exp","Enabled":true}}`)
	realCall(t, k, east, dynamodbapi.OpUpdateContinuousBackupsRaw, `{"TableName":"Locks","PointInTimeRecoverySpecification":{"PointInTimeRecoveryEnabled":true}}`)
	realCall(t, k, west, dynamodbapi.OpCreateTableRaw, `{"AttributeDefinitions":[{"AttributeName":"q","AttributeType":"S"},{"AttributeName":"n","AttributeType":"N"}],`+
		`"TableName":"Jobs","KeySchema":[{"AttributeName":"q","KeyType":"HASH"},{"AttributeName":"n","KeyType":"RANGE"}],`+
		`"LocalSecondaryIndexes":[{"IndexName":"ByN","KeySchema":[{"AttributeName":"q","KeyType":"HASH"},{"AttributeName":"n","KeyType":"RANGE"}],"Projection":{"ProjectionType":"KEYS_ONLY"}}],`+
		`"ProvisionedThroughput":{"ReadCapacityUnits":3,"WriteCapacityUnits":2}}`)
	for i := range 30 {
		realCall(t, k, east, dynamodbapi.OpPutItemRaw, fmt.Sprintf(`{"TableName":"Locks","Item":{"id":{"S":"lock-%02d"},"owner":{"S":"w%d"},"exp":{"N":"99999999999"},`+
			`"meta":{"M":{"pid":{"N":"%d.25"},"tags":{"SS":["a","b"]},"sizes":{"NS":["1","2.5"]},"raw":{"BS":["AAE="]}}},"list":{"L":[{"BOOL":true},{"NULL":true},{"B":"AQID"}]}}}`, i, i%3, i))
		realCall(t, k, west, dynamodbapi.OpPutItemRaw, fmt.Sprintf(`{"TableName":"Jobs","Item":{"q":{"S":"main"},"n":{"N":"%d"},"body":{"S":"job %d"}}}`, i, i))
	}

	// The TTL sweep on the real image: an item that expired a minute before
	// the clock's 2026-10-10T12:00:00Z goes, one whose expiry lies more than
	// five years back (a released lock) and the rest stay.
	realCall(t, k, east, dynamodbapi.OpPutItemRaw, `{"TableName":"Locks","Item":{"id":{"S":"stale"},"owner":{"S":"w0"},"exp":{"N":"1791633540"}}}`)
	realCall(t, k, east, dynamodbapi.OpPutItemRaw, `{"TableName":"Locks","Item":{"id":{"S":"released"},"owner":{"S":"w0"},"exp":{"N":"0"}}}`)
	if _, err := k.Settle(t.Context(), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := realCall(t, k, east, dynamodbapi.OpGetItemRaw, `{"TableName":"Locks","Key":{"id":{"S":"stale"}}}`); got != `{}` {
		t.Errorf("GetItem of the expired item after the sweep = %s, want it deleted", got)
	}
	if deleted, err := svc.SweepTTL(t.Context()); err != nil || deleted != 0 {
		t.Errorf("SweepTTL with nothing expired = %d, %v", deleted, err)
	}
	if got := realCall(t, k, east, dynamodbapi.OpGetItemRaw, `{"TableName":"Locks","Key":{"id":{"S":"released"}}}`); got == `{}` {
		t.Error("the item whose expiry lies more than five years back was deleted")
	}
	realCall(t, k, east, dynamodbapi.OpDeleteItemRaw, `{"TableName":"Locks","Key":{"id":{"S":"released"}}}`)

	// A mixed update goes to Local as sent; one of the table class alone is
	// the shim's.
	realCall(t, k, west, dynamodbapi.OpUpdateTableRaw, `{"TableName":"Jobs","TableClass":"STANDARD_INFREQUENT_ACCESS","ProvisionedThroughput":{"ReadCapacityUnits":4,"WriteCapacityUnits":2}}`)
	realCall(t, k, west, dynamodbapi.OpUpdateTableRaw, `{"TableName":"Jobs","TableClass":"STANDARD"}`)

	reads := []func() string{
		func() string {
			return realCall(t, k, east, dynamodbapi.OpListTagsOfResourceRaw, `{"ResourceArn":"arn:aws:dynamodb:us-east-1:111111111111:table/Locks"}`)
		},
		func() string {
			return realCall(t, k, east, dynamodbapi.OpDescribeTimeToLiveRaw, `{"TableName":"Locks"}`)
		},
		func() string {
			return realCall(t, k, east, dynamodbapi.OpDescribeContinuousBackupsRaw, `{"TableName":"Locks"}`)
		},
	}
	describe := func(scope aws.Scope, table string) map[string]any {
		var out struct{ Table map[string]any }
		if err := json.Unmarshal([]byte(realCall(t, k, scope, dynamodbapi.OpDescribeTableRaw, `{"TableName":"`+table+`"}`)), &out); err != nil {
			t.Fatal(err)
		}
		return out.Table
	}
	readAll := func() []string {
		out := make([]string, 0, len(reads))
		for _, read := range reads {
			out = append(out, read())
		}
		return out
	}
	beforeReads := readAll()
	beforeLocks, beforeJobs := describe(east, "Locks"), describe(west, "Jobs")
	// The read increase above happened at the frozen 2026-10-10T12:00:00Z,
	// whatever Local's wall clock said.
	if pt := beforeJobs["ProvisionedThroughput"]; path(pt, "LastIncreaseDateTime") != 1791633600.0 || path(pt, "LastDecreaseDateTime") != nil {
		t.Errorf("Jobs ProvisionedThroughput = %v, want the increase at 1791633600 and no decrease", pt)
	}

	items, err := svc.ExportLate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cells, err := svc.Export()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || bytes.Count(items[0].Data, []byte("\n")) != 31 || bytes.Count(items[1].Data, []byte("\n")) != 31 {
		t.Fatalf("ExportLate = %d sections; want two, of a CreateTable request and 30 items each", len(items))
	}

	if err := svc.ResetLate(t.Context(), kernel.ResetScope{}); err != nil {
		t.Fatal(err)
	}
	svc.Reset(kernel.ResetScope{})
	for _, scope := range []aws.Scope{east, west} {
		if got := realCall(t, k, scope, dynamodbapi.OpListTablesRaw, `{}`); got != `{"TableNames":[]}` {
			t.Fatalf("ListTables in %s/%s after the reset = %s", scope.Account, scope.Region, got)
		}
	}

	if err := svc.ImportLate(t.Context(), items); err != nil {
		t.Fatal(err)
	}
	if err := svc.Import(cells); err != nil {
		t.Fatal(err)
	}
	svc.Rearm(k.Clock().Now())

	if after := readAll(); !slices.Equal(after, beforeReads) {
		t.Errorf("shim reads after the import =\n%v\nwant\n%v", after, beforeReads)
	}
	again, err := svc.ExportLate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range items {
		if !bytes.Equal(again[i].Data, items[i].Data) {
			t.Errorf("export of %s after the import =\n%s\nwant\n%s", items[i].Path, again[i].Data, items[i].Data)
		}
	}
	afterLocks, afterJobs := describe(east, "Locks"), describe(west, "Jobs")
	for _, member := range []string{"TableId", "CreationDateTime", "TableClassSummary", "DeletionProtectionEnabled", "WarmThroughput", "BillingModeSummary", "KeySchema", "StreamSpecification"} {
		if !equalJSON(afterLocks[member], beforeLocks[member]) {
			t.Errorf("Locks %s after the import = %v, want %v", member, afterLocks[member], beforeLocks[member])
		}
	}
	for _, member := range []string{"TableId", "TableClassSummary", "ProvisionedThroughput", "LocalSecondaryIndexes", "KeySchema"} {
		if !equalJSON(withoutSizes(afterJobs[member]), withoutSizes(beforeJobs[member])) {
			t.Errorf("Jobs %s after the import = %v, want %v", member, afterJobs[member], beforeJobs[member])
		}
	}
	if beforeLocks["LatestStreamLabel"] != afterLocks["LatestStreamLabel"] {
		t.Logf("deviation (aws-differs: dynamodb-local): LatestStreamLabel %v became %v across the reset and import", beforeLocks["LatestStreamLabel"], afterLocks["LatestStreamLabel"])
	}
}

// withoutSizes drops the members Local recomputes as it writes, which an
// import changes the timing of: index sizes and counts, and the decreases
// of the day. The throughput's increase and decrease instants are the
// shim's, so they stay.
func withoutSizes(v any) any {
	switch v := v.(type) {
	case []any:
		out := make([]any, 0, len(v))
		for _, x := range v {
			out = append(out, withoutSizes(x))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			if k != "IndexSizeBytes" && k != "ItemCount" && k != "NumberOfDecreasesToday" {
				out[k] = withoutSizes(x)
			}
		}
		return out
	}
	return v
}
