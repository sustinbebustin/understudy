package lambda

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

const kindQueue = "arn:aws:sqs:us-east-1:000000000000:orders"

func kindScope(t *testing.T) aws.Scope {
	t.Helper()
	a, err := aws.ParseARN(kindQueue)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := aws.ScopeOf(a)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

// kindState is a State holding one SQS mapping, as CreateEventSourceMapping
// stores it.
func kindState(t *testing.T) State {
	t.Helper()
	st := newState(kindScope(t))
	st.Mappings["u1"] = Mapping{
		UUID: "u1", Source: kindQueue, SourceKind: esm.SQS{Scaling: esm.Unbounded{}}, Function: "f",
		BatchSize: defaultBatchSize, Enabled: true,
	}
	return st
}

// withSourceKind re-encodes the section data with u1's sourceKind member
// replaced by raw.
func withSourceKind(t *testing.T, data []byte, raw json.RawMessage) []byte {
	t.Helper()
	var doc map[string]map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("section %s: %v", data, err)
	}
	doc["mappings"]["u1"]["sourceKind"] = raw
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A mapping's kind is persisted as the tagged lambda.EventSourceKind union,
// an SQS kind with its scaling (state version 3), and read back as the
// same variant.
func TestMappingSourceKindRoundTrips(t *testing.T) {
	codec := stateCodec()
	st := kindState(t)
	data, err := codec.Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `"sourceKind":{"kind":"sqs","scaling":{"kind":"unbounded"}}`; !bytes.Contains(data, []byte(want)) {
		t.Errorf("section %s does not hold %s", data, want)
	}
	got := newState(kindScope(t))
	if err := codec.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal %s: %v", data, err)
	}
	if _, ok := got.Mappings["u1"].SourceKind.(esm.SQS); !ok {
		t.Errorf("SourceKind = %#v, want esm.SQS", got.Mappings["u1"].SourceKind)
	}
}

// A section naming a kind this build does not know is refused rather than
// loaded as a mapping that would never poll.
func TestMappingUnknownSourceKindIsRefused(t *testing.T) {
	codec := stateCodec()
	st := kindState(t)
	data, err := codec.Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	future := withSourceKind(t, data, json.RawMessage(`{"kind":"kinesis"}`))
	got := newState(kindScope(t))
	if err := codec.Unmarshal(future, &got); err == nil {
		t.Errorf("Unmarshal %s = nil error, want the unknown kind refused", future)
	}
}

// CreateEventSourceMapping takes the kind from the source ARN: an SQS queue
// or a DynamoDB stream; a source of another service is refused 501 naming
// both (contract 8.2).
func TestEventSourceTakesTheKindFromTheARN(t *testing.T) {
	queue := kindQueue
	a, kind, err := eventSource(&queue)
	if err != nil {
		t.Fatalf("eventSource(%s): %v", queue, err)
	}
	if _, ok := kind.(esm.SQS); !ok || a.String() != queue {
		t.Errorf("eventSource(%s) = %s, %#v; want the queue and esm.SQS", queue, a, kind)
	}

	table := "arn:aws:dynamodb:us-east-1:000000000000:table/orders/stream/2026-10-25T21:13:37.075"
	if _, kind, err := eventSource(&table); err != nil {
		t.Errorf("eventSource(%s): %v", table, err)
	} else if _, ok := kind.(esm.DynamoDBStream); !ok {
		t.Errorf("eventSource(%s) kind = %#v, want esm.DynamoDBStream", table, kind)
	}

	stream := "arn:aws:kinesis:us-east-1:000000000000:stream/orders"
	_, kind, err = eventSource(&stream)
	want := "understudy implements event source mappings for Amazon SQS queues and DynamoDB streams only; " + stream +
		" is neither. Run 'understudy coverage --service lambda' for what is supported."
	if err == nil || err.Message != want || kind != nil {
		t.Fatalf("eventSource(%s) = %#v, %v; want no kind and %q", stream, kind, err, want)
	}
	if err.ID != errEventSourceUnsupported(stream).ID {
		t.Errorf("eventSource(%s) error id = %v, want NotImplemented's", stream, err.ID)
	}
}

// The poller dispatches on the mapping's kind: an SQS mapping polls its
// queue, and a mapping without a kind gets no poller.
func TestNewPollerDispatchesOnSourceKind(t *testing.T) {
	scope := kindScope(t)
	m := kindState(t).Mappings["u1"]
	pl, ok := newPoller(scope, "u1", m)
	if !ok || pl.queue.String() != kindQueue {
		t.Fatalf("newPoller(an SQS mapping) = %+v, %t; want a poller of %s", pl, ok, kindQueue)
	}
	m.SourceKind = nil
	if pl, ok := newPoller(scope, "u1", m); ok {
		t.Errorf("newPoller(a mapping without a kind) = %+v, want none", pl)
	}
}
