//go:build docker

package dynamodb_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// engine connects to the local daemon named by DOCKER_HOST (or the
// default socket).
func engine(t *testing.T) *docker.Client {
	t.Helper()
	socket, err := docker.ParseDockerHost(os.Getenv("DOCKER_HOST"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := docker.New(docker.Options{Socket: socket})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// ownership is unique per run, so names and owner labels never collide
// with other agents' containers on the same daemon.
func ownership(t *testing.T) docker.Ownership {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(b)
	return docker.Ownership{Instance: "t33-" + id, Owner: "t33-" + id}
}

// newRealKernel is a kernel on a frozen clock with the DynamoDB service
// over local. It runs outside a synctest bubble, because the calls go over
// the network to a real container.
func newRealKernel(t *testing.T, local dynamodb.Local) *kernel.Kernel {
	t.Helper()
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Start: clock.FromWire(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)), Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(dynamodbapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	src := ids.NewSeeded(1)
	k, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	dynamodb.New(kernel.ServiceDeps{Kernel: k, Binder: k.Binder(dynamodbapi.Service()), Clock: clk, IDs: src, Identity: identity.New(src, aws.DefaultAccount())},
		dynamodb.Options{Local: local})
	if err := k.BindErr(); err != nil {
		t.Fatal(err)
	}
	return k
}

// send runs a client request for op with body in scope, its original bytes
// handed to the raw binding as FromClient, and returns the answer.
func send[In any, P interface {
	*In
	smithy.Deserializable
}](t *testing.T, k *kernel.Kernel, scope aws.Scope, op kernel.Op[In, kernel.RawOutput], body string) (kernel.RawResponse, *awserr.Error) {
	t.Helper()
	in := P(new(In))
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body))
	if err := (awsjson.Codec{Version: awsjson.V10}).Decode(r, op.Descriptor(), in); err != nil {
		t.Fatalf("decoding %s: %s", body, err.Message)
	}
	c := k.NewCall(kernel.ExternalCall{
		RequestID: k.IDs().RequestID(),
		Operation: op.Descriptor().ID,
		Scope:     scope,
		Principal: identity.Root{Account: scope.Account},
		Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
		Admitted:  k.Clock().Now(),
		Endpoint:  kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
	})
	raw := kernel.RawRequest{Method: http.MethodPost, Path: "/", Header: target(op.Descriptor().ID.Name), Body: []byte(body)}
	switch res := k.Dispatch(t.Context(), c, kernel.Ok{In: (*In)(in), Wire: kernel.FromClient{Request: raw}}).(type) {
	case kernel.Output:
		out, ok := res.Out.(*kernel.RawOutput)
		if !ok {
			t.Fatalf("output %T", res.Out)
		}
		return out.Response, nil
	case kernel.Failure:
		return kernel.RawResponse{}, res.Err
	case kernel.StatusOKFailure:
		return kernel.RawResponse{}, res.Err
	}
	t.Fatal("Dispatch returned no result")
	return kernel.RawResponse{}, nil
}

func scopeOf(t *testing.T, account, region string) aws.Scope {
	t.Helper()
	a, err := aws.ParseAccountID(account)
	if err != nil {
		t.Fatal(err)
	}
	r, err := aws.KnownRegions().Parse(region)
	if err != nil {
		t.Fatal(err)
	}
	return aws.Scope{Partition: aws.PartitionAWS(), Account: a, Region: r}
}

// TestLocalIsolatesAccountsAndRegions runs the pinned image: tables and
// items created through the proxy in one account and region are invisible
// from the others, ARNs name the caller's scope, and the container carries
// understudy's labels until Stop removes it (ADR 0014).
func TestLocalIsolatesAccountsAndRegions(t *testing.T) {
	d := engine(t)
	own := ownership(t)
	m, err := ddblocal.New(ddblocal.Config{
		Docker: d, Licence: ddblocal.Accepted{Digest: ddblocal.Digest},
		Labels: docker.Labels{Ownership: own, Test: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	selector := map[string]string{docker.LabelManaged: "true", docker.LabelOwner: own.Owner}
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		if err := m.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
		left, err := d.ContainerList(ctx, selector)
		if err != nil || len(left) != 0 {
			t.Errorf("containers left after Stop: %v, %v", left, err)
		}
	})
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	running, err := d.ContainerList(t.Context(), selector)
	if err != nil || len(running) != 1 {
		t.Fatalf("labeled containers = %v, %v; want one", running, err)
	}
	for k, v := range map[string]string{docker.LabelInstance: own.Instance, docker.LabelTest: "1"} {
		if running[0].Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, running[0].Labels[k], v)
		}
	}

	rk := newRealKernel(t, m)
	scopes := []aws.Scope{
		scopeOf(t, "111111111111", "us-east-1"), scopeOf(t, "111111111111", "eu-west-1"),
		scopeOf(t, "222222222222", "us-east-1"), scopeOf(t, "222222222222", "eu-west-1"),
	}
	key := func(s aws.Scope) string { return s.Account.String() + "/" + s.Region.String() }
	for _, s := range scopes {
		resp, aerr := send(t, rk, s, dynamodbapi.OpCreateTableRaw,
			`{"AttributeDefinitions":[{"AttributeName":"LockID","AttributeType":"S"}],"TableName":"Locks","KeySchema":[{"AttributeName":"LockID","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`)
		if aerr != nil || resp.Status != http.StatusOK {
			t.Fatalf("CreateTable in %s = %d %s %v", key(s), resp.Status, resp.Body, aerr)
		}
		wantARN := `"TableArn":"arn:aws:dynamodb:` + s.Region.String() + ":" + s.Account.String() + `:table/Locks"`
		if !strings.Contains(string(resp.Body), wantARN) {
			t.Errorf("CreateTable in %s answered %s, want %s", key(s), resp.Body, wantARN)
		}
		resp, aerr = send(t, rk, s, dynamodbapi.OpPutItemRaw, `{"TableName":"Locks","Item":{"LockID":{"S":"`+key(s)+`"}}}`)
		if aerr != nil || resp.Status != http.StatusOK {
			t.Fatalf("PutItem in %s = %d %s %v", key(s), resp.Status, resp.Body, aerr)
		}
	}
	for _, s := range scopes {
		for _, other := range scopes {
			resp, aerr := send(t, rk, s, dynamodbapi.OpGetItemRaw, `{"TableName":"Locks","Key":{"LockID":{"S":"`+key(other)+`"}}}`)
			if aerr != nil || resp.Status != http.StatusOK {
				t.Fatalf("GetItem in %s = %d %s %v", key(s), resp.Status, resp.Body, aerr)
			}
			var got struct{ Item map[string]map[string]string }
			if err := json.Unmarshal(resp.Body, &got); err != nil {
				t.Fatal(err)
			}
			if found := got.Item != nil; found != (s == other) {
				t.Errorf("GetItem in %s of %s's item found = %v, want %v", key(s), key(other), found, s == other)
			}
		}
	}
	resp, aerr := send(t, rk, scopeOf(t, "333333333333", "us-east-1"), dynamodbapi.OpListTablesRaw, `{}`)
	if aerr != nil || string(resp.Body) != `{"TableNames":[]}` {
		t.Errorf("ListTables in an untouched account = %s %v, want no tables", resp.Body, aerr)
	}
	resp, aerr = send(t, rk, scopes[0], dynamodbapi.OpDescribeTableRaw, `{"TableName":"Missing"}`)
	if aerr != nil || resp.Status != http.StatusBadRequest ||
		string(resp.Body) != `{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"Requested resource not found: Table: Missing not found"}` {
		t.Errorf("DescribeTable of a missing table = %d %s %v", resp.Status, resp.Body, aerr)
	}
}

// TestLicenceGateBeforeAnyPull: without acceptance the proxy answers 501
// with the notice naming the flag, and the daemon is never asked to pull
// or run anything.
func TestLicenceGateBeforeAnyPull(t *testing.T) {
	d := engine(t)
	own := ownership(t)
	m, err := ddblocal.New(ddblocal.Config{Docker: d, Licence: ddblocal.AwaitingLicence{}, Labels: docker.Labels{Ownership: own, Test: true}})
	if err != nil {
		t.Fatal(err)
	}
	rk := newRealKernel(t, m)
	_, aerr := send(t, rk, scopeOf(t, "111111111111", "us-east-1"), dynamodbapi.OpListTablesRaw, `{}`)
	if aerr == nil || aerr.ID.Status != http.StatusNotImplemented || aerr.Message != ddblocal.Notice() ||
		!strings.Contains(aerr.Message, "start understudy with --accept-dynamodb-local-license or set UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1") {
		t.Fatalf("ListTables = %+v, want 501 with the notice", aerr)
	}
	left, err := d.ContainerList(t.Context(), map[string]string{docker.LabelManaged: "true", docker.LabelOwner: own.Owner})
	if err != nil || len(left) != 0 {
		t.Errorf("containers = %v, %v; want none", left, err)
	}
}
