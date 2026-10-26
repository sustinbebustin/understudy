//go:build docker

package dynamodb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// newFaultKernel is newRealKernel with a fault store over the kernel's
// catalogue, as the server wires one.
func newFaultKernel(t *testing.T, local dynamodb.Local) (*kernel.Kernel, *fault.Store) {
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
	seed := uint64(1)
	src := ids.NewSeeded(seed)
	faults := fault.New(&seed, src, kernel.NewCatalogue(reg, nil))
	k, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j, Faults: faults})
	if err != nil {
		t.Fatal(err)
	}
	dynamodb.New(kernel.ServiceDeps{Kernel: k, Binder: k.Binder(dynamodbapi.Service()), Clock: clk, IDs: src, Identity: identity.New(src, aws.DefaultAccount())},
		dynamodb.Options{Local: local})
	if err := k.BindErr(); err != nil {
		t.Fatal(err)
	}
	return k, faults
}

// TestTransactionCanceledRuleAgainstLocal runs the pinned DynamoDB Local
// behind the real binding: a TransactionCanceledException rule giving two
// reasons lets a one-item TransactWriteItems reach Local and write its
// item, and cancels a three-item one before Local with the reasons padded
// to the items, so none of its items is written (smithy-protocol-tests.md
// 12.2, 12.4, 14.9).
func TestTransactionCanceledRuleAgainstLocal(t *testing.T) {
	d := engine(t)
	m, err := ddblocal.New(ddblocal.Config{Docker: d, Labels: docker.Labels{Ownership: ownership(t), Test: true}})
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
	k, faults := newFaultKernel(t, m)
	scope := scopeOf(t, "111111111111", "us-east-1")
	resp, aerr := send(t, k, scope, dynamodbapi.OpCreateTableRaw,
		`{"AttributeDefinitions":[{"AttributeName":"pk","AttributeType":"S"}],"TableName":"Orders","KeySchema":[{"AttributeName":"pk","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`)
	if aerr != nil || resp.Status != http.StatusOK {
		t.Fatalf("CreateTable = %d %s %v", resp.Status, resp.Body, aerr)
	}
	if _, rerr := faults.Add(fault.Rule{
		ID:      "conflict",
		Match:   fault.Match{Operations: []ref.OperationID{dynamodbapi.OpTransactWriteItemsRaw.Descriptor().ID}},
		Phase:   fault.Request,
		Action:  fault.Error{Shape: "TransactionCanceledException", Members: ruleMembers(t, `{"CancellationReasons":[{"Code":"None"},`+conflictReason+`]}`)},
		Trigger: fault.Always{},
	}); rerr != nil {
		t.Fatal(rerr)
	}
	put := func(keys ...string) string {
		items := make([]string, len(keys))
		for i, key := range keys {
			items[i] = `{"Put":{"TableName":"Orders","Item":{"pk":{"S":"` + key + `"}}}}`
		}
		return `{"TransactItems":[` + strings.Join(items, ",") + `]}`
	}
	if resp, aerr := send(t, k, scope, dynamodbapi.OpTransactWriteItemsRaw, put("single")); aerr != nil || resp.Status != http.StatusOK {
		t.Fatalf("a 1-item TransactWriteItems = %d %s %v, want Local's answer", resp.Status, resp.Body, aerr)
	}
	_, aerr = send(t, k, scope, dynamodbapi.OpTransactWriteItemsRaw, put("a", "b", "c"))
	if aerr == nil {
		t.Fatal("a 3-item TransactWriteItems reached Local; want it cancelled")
	}
	id := dynamodbapi.IdentityTransactionCanceledException()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", http.NoBody)
	wire := awsjson.Codec{Version: awsjson.V10}.EncodeError(r, dynamodbapi.Service(), "rid", aerr)
	// The message key is UNVERIFIED (smithy-protocol-tests.md 12.4,
	// PROTO-01); the identity's own key is asserted.
	key := map[awserr.MessageKey]string{awserr.MessageUpper: "Message", awserr.MessageLower: "message"}[id.MessageKey]
	want := `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","` + key + `":` +
		`"Transaction cancelled, please refer cancellation reasons for specific reasons [None, TransactionConflict, None]",` +
		`"CancellationReasons":[{"Code":"None"},` + conflictReason + `,{"Code":"None"}]}`
	if wire.Status != http.StatusBadRequest || string(wire.Body) != want {
		t.Errorf("the cancelled call answers %d %s\nwant 400 %s", wire.Status, wire.Body, want)
	}
	for key, written := range map[string]bool{"single": true, "a": false, "b": false, "c": false} {
		resp, aerr := send(t, k, scope, dynamodbapi.OpGetItemRaw, `{"TableName":"Orders","Key":{"pk":{"S":"`+key+`"}}}`)
		if aerr != nil || resp.Status != http.StatusOK {
			t.Fatalf("GetItem %s = %d %s %v", key, resp.Status, resp.Body, aerr)
		}
		if found := strings.Contains(string(resp.Body), `"Item"`); found != written {
			t.Errorf("GetItem %s found the item = %t, want %t: %s", key, found, written, resp.Body)
		}
	}
}
