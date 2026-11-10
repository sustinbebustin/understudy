//go:build docker

package dynamodb_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
)

var streamsEpoch = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// newStreamsKernel is newRealKernel with DynamoDB Streams served from the
// DynamoDB service's log.
func newStreamsKernel(t *testing.T, local dynamodb.Local) (*kernel.Kernel, *dynamodb.Service) {
	t.Helper()
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Start: clock.FromWire(streamsEpoch), Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(dynamodbapi.Service(), dynamodbstreamsapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	src := ids.NewSeeded(1)
	k, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	idStore := identity.New(src, aws.DefaultAccount())
	svc := dynamodb.New(kernel.ServiceDeps{Kernel: k, Binder: k.Binder(dynamodbapi.Service()), Clock: clk, IDs: src, Identity: idStore}, dynamodb.Options{Local: local})
	dynamodb.NewStreams(kernel.ServiceDeps{Kernel: k, Binder: k.Binder(dynamodbstreamsapi.Service()), Clock: clk, IDs: src, Identity: idStore}, svc)
	if err := k.BindErr(); err != nil {
		t.Fatal(err)
	}
	running(t, k)
	return k, svc
}

// sendAt is send with the call admitted at at.
func sendAt(t *testing.T, k *kernel.Kernel, scope aws.Scope, op kernel.Op[dynamodbapi.PutItemInput, kernel.RawOutput], body string, at clock.Instant) (kernel.RawResponse, *awserr.Error) {
	t.Helper()
	in := new(dynamodbapi.PutItemInput)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body))
	if err := (awsjson.Codec{Version: awsjson.V10}).Decode(r, op.Descriptor(), in); err != nil {
		t.Errorf("decoding %s: %s", body, err.Message)
		return kernel.RawResponse{}, nil
	}
	c := k.NewCall(kernel.ExternalCall{
		RequestID: k.IDs().RequestID(), Operation: op.Descriptor().ID, Scope: scope,
		Principal: identity.Root{Account: scope.Account},
		Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
		Admitted:  at, Endpoint: kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
	})
	raw := kernel.RawRequest{Method: http.MethodPost, Path: "/", Header: target(op.Descriptor().ID.Name), Body: []byte(body)}
	switch res := k.Dispatch(t.Context(), c, kernel.Ok{In: in, Wire: kernel.FromClient{Request: raw}}).(type) {
	case kernel.Output:
		out, ok := res.Out.(*kernel.RawOutput)
		if !ok {
			t.Errorf("output %T", res.Out)
			return kernel.RawResponse{}, nil
		}
		return out.Response, nil
	case kernel.Failure:
		return kernel.RawResponse{}, res.Err
	case kernel.StatusOKFailure:
		return kernel.RawResponse{}, res.Err
	}
	t.Error("Dispatch returned no result")
	return kernel.RawResponse{}, nil
}

// streamsCall runs a typed Streams operation as a client request in scope.
func streamsCall[In, Out any](t *testing.T, k *kernel.Kernel, scope aws.Scope, op kernel.Op[In, Out], in *In) *Out {
	t.Helper()
	c := k.NewCall(kernel.ExternalCall{
		RequestID: k.IDs().RequestID(), Operation: op.Descriptor().ID, Scope: scope,
		Principal: identity.Root{Account: scope.Account},
		Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
		Admitted:  k.Clock().Now(), Endpoint: kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
	})
	switch res := k.Dispatch(t.Context(), c, kernel.Ok{In: in}).(type) {
	case kernel.Output:
		out, ok := res.Out.(*Out)
		if !ok {
			t.Fatalf("output %T", res.Out)
		}
		return out
	case kernel.Failure:
		t.Fatalf("%s: %s %s", op.Descriptor().ID.Name, res.Err.ID.JSONType, res.Err.Message)
	case kernel.StatusOKFailure:
		t.Fatalf("%s: %s %s", op.Descriptor().ID.Name, res.Err.ID.JSONType, res.Err.Message)
	}
	t.Fatal("Dispatch returned no result")
	return nil
}

// allStreamRecords reads the table's latest stream from TRIM_HORIZON.
func allStreamRecords(t *testing.T, k *kernel.Kernel, scope aws.Scope, table string) []dynamodbstreamsapi.Record {
	t.Helper()
	streams := streamsCall(t, k, scope, dynamodbstreamsapi.OpListStreams, &dynamodbstreamsapi.ListStreamsInput{TableName: new(table)})
	if len(streams.Streams) == 0 {
		t.Fatalf("%s has no stream", table)
	}
	arn := *streams.Streams[0].StreamArn
	d := streamsCall(t, k, scope, dynamodbstreamsapi.OpDescribeStream, &dynamodbstreamsapi.DescribeStreamInput{StreamArn: arn})
	it := streamsCall(t, k, scope, dynamodbstreamsapi.OpGetShardIterator, &dynamodbstreamsapi.GetShardIteratorInput{
		StreamArn: arn, ShardId: *d.StreamDescription.Shards[0].ShardId, ShardIteratorType: dynamodbstreamsapi.ShardIteratorTypeTrimHorizon,
	}).ShardIterator
	var out []dynamodbstreamsapi.Record
	for it != nil {
		page := streamsCall(t, k, scope, dynamodbstreamsapi.OpGetRecords, &dynamodbstreamsapi.GetRecordsInput{ShardIterator: *it})
		out = append(out, page.Records...)
		if len(page.Records) == 0 {
			break
		}
		it = page.NextShardIterator
	}
	return out
}

func createStreamed(t *testing.T, k *kernel.Kernel, scope aws.Scope, table, extra string) {
	t.Helper()
	resp, aerr := send(t, k, scope, dynamodbapi.OpCreateTableRaw, `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"`+table+
		`","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST","StreamSpecification":{"StreamEnabled":true,"StreamViewType":"NEW_IMAGE"}`+extra+`}`)
	if aerr != nil || resp.Status != http.StatusOK {
		t.Fatalf("CreateTable = %d %s %v", resp.Status, resp.Body, aerr)
	}
}

// Concurrent writers to one stream-enabled table each find their own
// record stamped with their own admission instant: the table's stream lock
// spans the forward and the drain, so Local's records are attributed to the
// write that caused them (dynamodb-streams.md 11.1 item 2, 11.9 Docker
// test).
func TestStreamsAttributeConcurrentWritesDocker(t *testing.T) {
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
				t.Errorf("writer %d: %d %s %v", i, resp.Status, resp.Body, aerr)
			}
		})
	}
	wg.Wait()
	records := allStreamRecords(t, k, scope, "Orders")
	if len(records) != writers {
		t.Fatalf("%d records, want %d", len(records), writers)
	}
	for _, r := range records {
		key, ok := r.Dynamodb.Keys["id"].(dynamodbstreamsapi.AttributeValueMemberS)
		if !ok {
			t.Fatalf("record key %#v", r.Dynamodb.Keys)
		}
		i, err := strconv.Atoi(key.Value)
		if err != nil {
			t.Fatal(err)
		}
		if want := streamsEpoch.Add(time.Duration(i+1) * time.Second); r.Dynamodb.ApproximateCreationDateTime == nil || !r.Dynamodb.ApproximateCreationDateTime.Equal(want) {
			t.Errorf("record of writer %d at %v, want its write's %v", i, r.Dynamodb.ApproximateCreationDateTime, want)
		}
		if r.UserIdentity != nil {
			t.Errorf("record of writer %d carries userIdentity", i)
		}
	}
}

// A TTL sweep's delete is a REMOVE with userIdentity
// {"PrincipalId":"dynamodb.amazonaws.com","Type":"Service"}, and an
// ordinary DeleteItem has none (dynamodb-streams.md 7.5, 11.9 Docker test).
func TestStreamsTagOnlySweeperDeletesDocker(t *testing.T) {
	k, svc := newStreamsKernel(t, startLocal(t))
	scope := scopeOf(t, "111111111111", "us-east-1")
	createStreamed(t, k, scope, "Locks", "")
	if resp, aerr := send(t, k, scope, dynamodbapi.OpUpdateTimeToLiveRaw, `{"TableName":"Locks","TimeToLiveSpecification":{"AttributeName":"exp","Enabled":true}}`); aerr != nil || resp.Status != http.StatusOK {
		t.Fatalf("UpdateTimeToLive = %d %s %v", resp.Status, resp.Body, aerr)
	}
	expired := strconv.FormatInt(streamsEpoch.Add(-time.Minute).Unix(), 10)
	for _, body := range []string{
		`{"TableName":"Locks","Item":{"id":{"S":"swept"},"exp":{"N":"` + expired + `"}}}`,
		`{"TableName":"Locks","Item":{"id":{"S":"deleted"}}}`,
	} {
		if resp, aerr := send(t, k, scope, dynamodbapi.OpPutItemRaw, body); aerr != nil || resp.Status != http.StatusOK {
			t.Fatalf("PutItem = %d %s %v", resp.Status, resp.Body, aerr)
		}
	}
	if resp, aerr := send(t, k, scope, dynamodbapi.OpDeleteItemRaw, `{"TableName":"Locks","Key":{"id":{"S":"deleted"}}}`); aerr != nil || resp.Status != http.StatusOK {
		t.Fatalf("DeleteItem = %d %s %v", resp.Status, resp.Body, aerr)
	}
	// The running kernel sweeps an expiry already past when the write
	// lands; Settle waits for it, and SweepTTL deletes whatever is left.
	if _, err := k.Settle(t.Context(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SweepTTL(t.Context()); err != nil {
		t.Fatalf("SweepTTL: %v", err)
	}
	removes := map[string]*dynamodbstreamsapi.Identity{}
	for _, r := range allStreamRecords(t, k, scope, "Locks") {
		if r.EventName != nil && *r.EventName == dynamodbstreamsapi.OperationTypeRemove {
			key, ok := r.Dynamodb.Keys["id"].(dynamodbstreamsapi.AttributeValueMemberS)
			if !ok {
				t.Fatalf("record key %#v", r.Dynamodb.Keys)
			}
			removes[key.Value] = r.UserIdentity
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
