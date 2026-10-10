package journal

import (
	"bytes"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func roundTrip(tb rapid.TB, events ...Event) []Event {
	tb.Helper()
	var buf bytes.Buffer
	for i := range events {
		if err := EncodeNDJSON(&buf, events[i]); err != nil {
			tb.Fatalf("EncodeNDJSON(%s): %v", detailName(events[i].Detail), err)
		}
	}
	var got []Event
	for e, err := range DecodeNDJSON(&buf) {
		if err != nil {
			tb.Fatalf("DecodeNDJSON: %v\ninput: %s", err, buf.String())
		}
		got = append(got, e)
	}
	return got
}

func TestNDJSONRoundTripsRandomEvents(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		events := rapid.SliceOfN(genEvent(), 1, 5).Draw(t, "events")
		got := roundTrip(t, events...)
		if !reflect.DeepEqual(got, events) {
			t.Fatalf("decode(encode(events)) differs\n got %#v\nwant %#v", got, events)
		}
	})
}

// TestNDJSONRoundTripsEveryVariant names each Detail variant and each variant
// of the sums nested in them, so a variant the encoder forgets fails here
// even if the random generator rarely draws it.
func TestNDJSONRoundTripsEveryVariant(t *testing.T) {
	queue := arn("sqs", "orders")
	topic := arn("sns", "events")
	fn := arn("lambda", "function:worker")
	at := epoch.Add(90 * time.Second)
	input, err := NewRedacted([]byte(`{ "QueueUrl": "http://localhost:4566/000000000000/orders", "MessageBody": "hi" }`))
	if err != nil {
		t.Fatal(err)
	}
	external := ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain, RemoteAddr: netip.MustParseAddrPort("127.0.0.1:50211"), UserAgent: "aws-sdk-go-v2/1.36"}
	call := func(o Outcome, origin ref.Origin) APICall {
		return APICall{
			Op: ref.OperationID{Service: "sqs", Name: "SendMessage"}, RequestID: "7a62c49f-347e-4fc4-9331-6e8e7a96aa73",
			Origin: origin, Principal: "arn:aws:iam::000000000000:root", Mode: "caller",
			Resources: []aws.ARN{queue}, Outcome: o, Latency: 1500 * time.Microsecond, Input: input,
		}
	}
	traced := call(OK{}, external)
	traced.TraceHeader = "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1"
	details := []Detail{
		call(Pending{}, external),
		traced,
		call(OK{}, external),
		call(Failed{Identity: ErrorIdentity{Shape: "com.amazonaws.sqs#QueueDoesNotExist"}, Status: 400, Message: "The specified queue does not exist."}, external),
		call(Injected{Rule: "throttle-sends", Identity: ErrorIdentity{Shape: "com.amazonaws.sqs#RequestThrottled"}}, ref.Internal{Via: ref.SNSDelivery}),
		call(OK{}, ref.External{Protocol: ref.AWSQuery, Listener: ref.ListenerContainer}),
		Delivery{From: topic, To: queue, Mechanism: delivery.SNSToSQS, Attempt: 1, Outcome: Delivered{}},
		Delivery{From: topic, To: queue, Mechanism: delivery.SNSToSQS, Attempt: 1, Outcome: Filtered{Rule: `{"store":["example_corp"]}`}},
		Delivery{From: topic, To: fn, Mechanism: delivery.SNSToLambda, Attempt: 2, Outcome: Retrying{Next: at}},
		Delivery{From: topic, To: fn, Mechanism: delivery.SNSToLambda, Attempt: 3, Outcome: DeadLettered{DLQ: arn("sqs", "orders-dlq")}},
		Delivery{From: arn("events", "rule/nightly"), To: queue, Mechanism: delivery.EventsToSQS, Attempt: 185, Outcome: Dropped{Reason: "maximum event age exceeded"}},
		Invocation{Function: fn, Version: "$LATEST", Env: "env-1", Cold: true, Init: 80 * time.Millisecond, Duration: 12 * time.Millisecond, Outcome: InvocationOK{}},
		Invocation{Function: fn, Version: "1", Env: "env-1", Outcome: FunctionError{Type: "Unhandled"}},
		Invocation{Function: fn, Outcome: Timeout{}},
		Invocation{Function: fn, Outcome: ExitError{Code: 2}},
		Invocation{Function: fn, Outcome: OOM{}},
		Invocation{Function: fn, Outcome: Throttled{}},
		TimerFired{Key: clock.TimerKey{Kind: "sqs.visibility", ID: queue.String() + "/m-1"}},
		StateChange{Resource: fn, From: "Pending", To: "Active"},
		FaultInjected{Rule: "throttle-sends", Hit: 2, Of: 3},
		ClockChanged{From: clock.Running{Offset: -time.Hour}, To: clock.Frozen{At: at}},
		ClockChanged{From: clock.Frozen{At: at}, To: clock.Scaled{Factor: 60, AnchorWall: wall0, AnchorEmu: at}},
		Advisory{Kind: AdvisoryPolicyWouldDeny, Resource: queue, Text: "the queue policy has no sqs:SendMessage statement for the topic"},
		Advisory{Kind: AdvisoryUnmintedCredentials, Resource: fn, Text: "credentials not minted for this environment"},
		Advisory{Kind: AdvisoryShadowedCodeUpdate, Resource: fn, Text: "hot reload overlay shadows the update"},
		Unclaimed{Method: "GET", Host: "example.localhost:4566", Path: "/nowhere", Reasons: []string{"no X-Amz-Target", "no bucket named example"}},
		Gap{Missed: 42},
	}
	for _, d := range details {
		e := Event{ID: 7, Cause: ref.Cause{Parent: 3, Trace: 1}, Links: []ref.EventID{4, 5}, At: at, Wall: wall0, Scope: scope(), Detail: d}
		got := roundTrip(t, e)
		if len(got) != 1 || !reflect.DeepEqual(got[0], e) {
			t.Errorf("round trip of %s\n got %#v\nwant %#v", detailName(d), got, e)
		}
	}
}

func TestEncodeNDJSONWritesErrorOutcomesInCamelCase(t *testing.T) {
	identity := ErrorIdentity{Shape: "com.amazonaws.sqs#QueueDoesNotExist"}
	cases := map[string]struct {
		outcome Outcome
		want    string
	}{
		"failed": {
			Failed{Identity: identity, Status: 400, Message: "The specified queue does not exist."},
			`"outcome":{"kind":"failed","identity":{"shape":"com.amazonaws.sqs#QueueDoesNotExist"},"status":400,"message":"The specified queue does not exist."}`,
		},
		"injected": {
			Injected{Rule: "missing-queue", Identity: identity},
			`"outcome":{"kind":"injected","identity":{"shape":"com.amazonaws.sqs#QueueDoesNotExist"},"rule":"missing-queue"}`,
		},
	}
	for name, c := range cases {
		var buf bytes.Buffer
		e := Event{ID: 1, At: epoch, Wall: wall0, Scope: scope(), Detail: APICall{
			Op: ref.OperationID{Service: "sqs", Name: "SendMessage"}, Origin: ref.Internal{Via: ref.SNSDelivery}, Outcome: c.outcome,
		}}
		if err := EncodeNDJSON(&buf, e); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(buf.String(), c.want) {
			t.Errorf("%s: line\n%s\ndoes not contain\n%s", name, buf.String(), c.want)
		}
	}
}

func TestDecodeNDJSONReportsMalformedLinesAndReadsOn(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeNDJSON(&buf, Event{ID: 1, At: epoch, Wall: wall0, Detail: Gap{Missed: 1}}); err != nil {
		t.Fatal(err)
	}
	good := strings.TrimSpace(buf.String())
	cases := map[string]string{
		"unknown member": strings.Replace(good, `"id":1`, `"id":1,"extra":true`, 1),
		"unknown kind":   strings.Replace(good, `"kind":"gap"`, `"kind":"telemetry"`, 1),
		"bad detail":     strings.Replace(good, `"missed":1`, `"missed":"one"`, 1),
		"bad ARN": `{"id":2,"cause":{},"at":"2026-01-01T00:00:00Z","wall":"2026-01-01T00:00:00Z","scope":{"partition":"","account":"","region":""},` +
			`"kind":"state-change","detail":{"resource":"orders","from":"A","to":"B"}}`,
	}
	for name, line := range cases {
		input := good + "\n\n" + line + "\n" + good // a blank line, and no final newline
		var decoded int
		var badLines []int
		for _, err := range DecodeNDJSON(strings.NewReader(input)) {
			var decodeErr *DecodeError
			switch {
			case errors.As(err, &decodeErr):
				badLines = append(badLines, decodeErr.Line)
			case err != nil:
				t.Fatalf("%s: error %v is not a *DecodeError", name, err)
			default:
				decoded++
			}
		}
		if decoded != 2 || !slices.Equal(badLines, []int{3}) {
			t.Errorf("%s: decoded %d records with bad lines %v; want both good records and a *DecodeError for line 3", name, decoded, badLines)
		}
	}
}

func TestEncodeRefusesEventsWithoutWireForm(t *testing.T) {
	cases := map[string]Event{
		"no detail":     {ID: 1},
		"no origin":     {ID: 2, Detail: APICall{Outcome: OK{}}},
		"no outcome":    {ID: 3, Detail: APICall{Origin: ref.Internal{Via: ref.Timer}}},
		"zero listener": {ID: 4, Detail: APICall{Origin: ref.External{}, Outcome: OK{}}},
		"no mechanism":  {ID: 5, Detail: Delivery{Outcome: Delivered{}}},
		"nil mode":      {ID: 6, Detail: ClockChanged{From: clock.Running{}}},
	}
	for name, e := range cases {
		var encErr *EncodingError
		if err := EncodeNDJSON(&bytes.Buffer{}, e); !errors.As(err, &encErr) || encErr.ID != e.ID {
			t.Errorf("%s: EncodeNDJSON error = %v, want *EncodingError for event %d", name, err, e.ID)
		}
	}
}

func TestNDJSONRoundTripsInputThatJSONEscapes(t *testing.T) {
	// Message bodies and URLs hold characters encoding/json escapes.
	input, err := NewRedacted([]byte("{\"MessageBody\": \"<a&b>  \", \"QueueUrl\": \"http://h/q?x=1&y=2\"}"))
	if err != nil {
		t.Fatal(err)
	}
	e := Event{ID: 1, Cause: ref.Cause{Trace: 1}, At: epoch, Wall: wall0, Scope: scope(), Detail: APICall{
		Op: ref.OperationID{Service: "sqs", Name: "SendMessage"}, Origin: ref.Internal{Via: ref.SNSDelivery}, Outcome: OK{}, Input: input,
	}}
	if got := roundTrip(t, e); !reflect.DeepEqual(got, []Event{e}) {
		t.Fatalf("decode(encode(event)) differs\n got %#v\nwant %#v", got, []Event{e})
	}
}

func TestNewRedactedCompactsAndValidates(t *testing.T) {
	r, err := NewRedacted([]byte("{ \"a\" : [1, 2] }"))
	if err != nil || string(r.JSON()) != `{"a":[1,2]}` {
		t.Errorf("NewRedacted = %q, %v; want the compact document", r.JSON(), err)
	}
	var redErr *RedactedError
	if _, err := NewRedacted([]byte("{")); !errors.As(err, &redErr) {
		t.Errorf("NewRedacted(invalid) error = %v, want *RedactedError", err)
	}
	if r, err := NewRedacted(nil); err != nil || r != (Redacted{}) || r.JSON() != nil {
		t.Errorf("NewRedacted(nil) = %v, %v; want the zero value", r, err)
	}
}
