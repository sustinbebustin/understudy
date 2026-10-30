package outbox

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Every pending delivery survives a snapshot: State encodes and decodes to
// an equal value, whatever target, policy and attributes it holds, and
// encodes to the same bytes every time.
func TestStateRoundTrip(t *testing.T) {
	c := codec()
	rapid.Check(t, func(t *rapid.T) {
		st := newState(aws.Scope{})
		for _, e := range rapid.SliceOfN(genEntry(), 0, 4).Draw(t, "entries") {
			st.Entries[e.ID] = e
		}
		data, err := c.Marshal(&st)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		again, err := c.Marshal(&st)
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("Marshal is not deterministic:\n%s\n%s", data, again)
		}
		got := newState(aws.Scope{})
		if err := c.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if !reflect.DeepEqual(got, st) {
			t.Fatalf("round trip of %s:\ngot  %+v\nwant %+v", data, got, st)
		}
	})
}

func TestTimerIDRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		scope := genScope().Draw(t, "scope")
		id := ids.DeliveryID(rapid.StringMatching(`[0-9a-f-]{1,36}`).Draw(t, "id"))
		gotScope, gotID, ok := parseTimerID(timerID(scope, id))
		if !ok || gotScope != scope || gotID != id {
			t.Fatalf("parseTimerID(timerID(%v, %q)) = %v, %q, %v", scope, id, gotScope, gotID, ok)
		}
	})
	for _, bad := range []string{"", "nodelimiter", "000000000000/us-east-1/", "bad/us-east-1/x"} {
		if _, _, ok := parseTimerID(bad); ok {
			t.Errorf("parseTimerID(%q) succeeded", bad)
		}
	}
}

func genScope() *rapid.Generator[aws.Scope] {
	return rapid.Custom(func(t *rapid.T) aws.Scope {
		account, err := aws.ParseAccountID(rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"))
		if err != nil {
			t.Fatal(err)
		}
		var region aws.Region
		if err := region.UnmarshalText([]byte(rapid.SampledFrom([]string{"us-east-1", "eu-west-2", "ap-southeast-1"}).Draw(t, "region"))); err != nil {
			t.Fatal(err)
		}
		return aws.Scope{Partition: aws.PartitionAWS(), Account: account, Region: region}
	})
}

func genARN(service string) *rapid.Generator[aws.ARN] {
	return rapid.Custom(func(t *rapid.T) aws.ARN {
		return aws.ARN{
			Partition: "aws",
			Service:   service,
			Region:    "us-east-1",
			Account:   rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"),
			Resource:  rapid.StringMatching(`[a-z][a-z0-9-]{0,20}`).Draw(t, "name"),
		}
	})
}

func genAttributes() *rapid.Generator[[]delivery.MessageAttribute] {
	value := rapid.OneOf(
		rapid.Map(rapid.String(), func(s string) delivery.AttributeValue { return delivery.StringAttribute{Value: s} }),
		rapid.Map(rapid.StringMatching(`-?[0-9]{1,6}(\.[0-9]{1,3})?`), func(s string) delivery.AttributeValue {
			return delivery.NumberAttribute{Label: "float", Value: s}
		}),
		rapid.Map(rapid.SliceOfN(rapid.Byte(), 1, 8), func(b []byte) delivery.AttributeValue { return delivery.BinaryAttribute{Value: b} }),
	)
	return rapid.Custom(func(t *rapid.T) []delivery.MessageAttribute {
		n := rapid.IntRange(0, 3).Draw(t, "attributes")
		if n == 0 {
			return nil
		}
		out := make([]delivery.MessageAttribute, n)
		for i := range out {
			out[i] = delivery.MessageAttribute{Name: rapid.StringMatching(`[A-Za-z]{1,8}`).Draw(t, "name"), Value: value.Draw(t, "value")}
		}
		return out
	})
}

func genTarget() *rapid.Generator[delivery.Target] {
	return rapid.OneOf(
		rapid.Custom(func(t *rapid.T) delivery.Target {
			return delivery.SQSQueue{
				ARN:         genARN("sqs").Draw(t, "queue"),
				Body:        rapid.String().Draw(t, "body"),
				Attributes:  genAttributes().Draw(t, "attributes"),
				GroupID:     rapid.StringMatching(`[a-z]{0,4}`).Draw(t, "group"),
				DedupID:     rapid.StringMatching(`[a-z]{0,4}`).Draw(t, "dedup"),
				TraceHeader: rapid.StringMatching(`(Root=1-[0-9a-f]{8}-[0-9a-f]{24};Sampled=[01])?`).Draw(t, "traceHeader"),
			}
		}),
		rapid.Custom(func(t *rapid.T) delivery.Target {
			return delivery.S3Object{
				Bucket: rapid.StringMatching(`[a-z0-9][a-z0-9.-]{2,20}`).Draw(t, "bucket"),
				Key:    rapid.StringMatching(`aws/lambda/async/[a-z]{1,8}/[0-9/T.-]{1,20}`).Draw(t, "key"),
				Body:   rapid.SliceOfN(rapid.Byte(), 1, 16).Draw(t, "body"),
			}
		}),
		rapid.Custom(func(t *rapid.T) delivery.Target {
			return delivery.LambdaFunction{
				ARN:        genARN("lambda").Draw(t, "function"),
				Qualifier:  rapid.SampledFrom([]string{"", "$LATEST", "live"}).Draw(t, "qualifier"),
				Event:      []byte(rapid.StringMatching(`\{"n":[0-9]{1,3}\}`).Draw(t, "event")),
				Invocation: rapid.SampledFrom([]delivery.InvocationType{delivery.InvocationEvent, delivery.InvocationRequestResponse}).Draw(t, "invocation"),
			}
		}),
		rapid.Custom(func(t *rapid.T) delivery.Target {
			return delivery.SNSTopic{
				ARN:        genARN("sns").Draw(t, "topic"),
				Message:    rapid.String().Draw(t, "message"),
				Attributes: genAttributes().Draw(t, "attributes"),
				Subject:    rapid.StringMatching(`[A-Za-z ]{0,10}`).Draw(t, "subject"),
			}
		}),
		rapid.Custom(func(t *rapid.T) delivery.Target {
			// An event bus target forwards one matched event with its
			// envelope (eventbridge-buses.md 10.4.12). Its Detail is a JSON
			// object: AWS refuses any other as MalformedDetail
			// (secrets-logs-events-alarms.md 4.7), so no leading zeros.
			bus := genARN("events").Draw(t, "bus")
			bus.Resource = "event-bus/" + bus.Resource
			entry := delivery.EventEntry{
				Source:     rapid.StringMatching(`[a-z.]{1,10}`).Draw(t, "source"),
				DetailType: rapid.StringMatching(`[A-Za-z ]{1,10}`).Draw(t, "detailType"),
				Detail:     rapid.StringMatching(`\{"n":(0|[1-9][0-9]{0,2})\}`).Draw(t, "detail"),
				Forwarded:  genForwarded().Draw(t, "forwarded"),
			}
			if n := rapid.IntRange(0, 2).Draw(t, "resources"); n > 0 {
				entry.Resources = rapid.SliceOfN(rapid.StringMatching(`arn:aws:s3:::[a-z]{1,8}`), n, n).Draw(t, "resource")
			}
			return delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{entry}}
		}),
	)
}

// genForwarded draws the envelope of a forwarded event, its time in UTC as
// a snapshot reads one back.
func genForwarded() *rapid.Generator[*delivery.Forwarded] {
	return rapid.Custom(func(t *rapid.T) *delivery.Forwarded {
		return &delivery.Forwarded{
			ID:      rapid.StringMatching(`[0-9a-f]{8}-[0-9a-f]{4}`).Draw(t, "eventID"),
			Account: rapid.StringMatching(`[0-9]{12}`).Draw(t, "sender"),
			Region:  rapid.SampledFrom([]string{"us-east-1", "eu-west-2"}).Draw(t, "origin"),
			Time:    time.Unix(rapid.Int64Range(0, 4e9).Draw(t, "time"), rapid.Int64Range(0, 999).Draw(t, "ms")*int64(time.Millisecond)).UTC(),
		}
	})
}

func genPolicy() *rapid.Generator[delivery.RetryPolicy] {
	return rapid.OneOf(
		rapid.Just[delivery.RetryPolicy](delivery.SNSManaged{}),
		rapid.Just[delivery.RetryPolicy](delivery.S3Notification{}),
		rapid.Just[delivery.RetryPolicy](delivery.None{}),
		rapid.Custom(func(t *rapid.T) delivery.RetryPolicy {
			return delivery.EventBridgeTarget{
				MaxAge:      time.Duration(rapid.IntRange(60, 86400).Draw(t, "age")) * time.Second,
				MaxAttempts: rapid.IntRange(0, 185).Draw(t, "attempts"),
			}
		}),
	)
}

// genEntry draws an entry Enqueue would accept: its mechanism is one that
// delivers to the service of its target. A Lambda delivery comes from a
// function, runs as its execution role carrying a record or, to a queue or
// topic, a dead letter, and has no retry, dead-letter queue or forwarded
// event (lambda-destinations-layers.md 4.6, 5.1, 11.3).
func genEntry() *rapid.Generator[Entry] {
	return rapid.Custom(func(t *rapid.T) Entry {
		at := clock.FromWire(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
		target := genTarget().Draw(t, "target")
		req := delivery.Request{
			Source:    genARN("sns").Draw(t, "source"),
			Mechanism: genMechanismInto(target).Draw(t, "mechanism"),
			Target:    target,
		}
		via := req.Mechanism.Via()
		if via == ref.LambdaDestination || via == ref.LambdaESM {
			req.Source = genARN("lambda").Draw(t, "function")
			req.Source.Resource = "function:" + req.Source.Resource
			req.Policy = delivery.None{}
			carries := []delivery.LambdaPayload{delivery.DestinationRecord}
			switch tg := target.(type) {
			case delivery.SQSQueue, delivery.SNSTopic:
				// An event source mapping sends on-failure records only
				// (lambda-stream-sources.md 5.6), from the mapping's ARN.
				if via == ref.LambdaESM {
					req.Source.Resource = "event-source-mapping:" + req.Source.Resource
					break
				}
				carries = append(carries, delivery.DeadLetter)
			case delivery.EventBus:
				for i := range tg.Entries {
					tg.Entries[i].Forwarded = nil
				}
				req.Target = tg
			case delivery.LambdaFunction, delivery.S3Object, delivery.HTTPEndpoint:
			}
			role := genARN("iam").Draw(t, "role")
			role.Region, role.Resource = "", "role/"+rapid.StringMatching(`([a-z]{1,8}/)?`).Draw(t, "path")+role.Resource
			req.Sender = &delivery.LambdaSender{Role: role, Carries: rapid.SampledFrom(carries).Draw(t, "carries")}
		} else {
			req.Policy = genPolicy().Draw(t, "policy")
			if rapid.Bool().Draw(t, "dlq") {
				a := genARN("sqs").Draw(t, "dlqARN")
				req.DLQ = &a
			}
		}
		return Entry{
			ID:       ids.DeliveryID(rapid.StringMatching(`[0-9a-f]{8}`).Draw(t, "id")),
			Request:  req,
			Attempt:  rapid.IntRange(0, 100).Draw(t, "attempt"),
			NextAt:   at.Add(time.Duration(rapid.IntRange(0, 1e6).Draw(t, "next")) * time.Millisecond),
			Cause:    ref.Cause{Parent: ref.EventID(rapid.Uint64Range(0, 1e6).Draw(t, "parent")), Trace: ref.EventID(rapid.Uint64Range(0, 1e6).Draw(t, "trace"))},
			Accepted: at,
			Seq:      rapid.Uint64().Draw(t, "seq"),
			Jitter:   rapid.Uint64().Draw(t, "jitter"),
		}
	})
}

// genMechanismInto draws a mechanism that delivers to target's service. A
// mechanism names its target by registry service ID, which for EventBridge
// is not its ARN namespace (internal/ref/ref.go). An S3 object is the
// target of lambda-to-s3 alone. The Scheduler mechanisms are left out:
// they need Request.As and Request.Invocation (checkScheduler), whose
// round trip scheduler_state_impl_test.go covers.
func genMechanismInto(target delivery.Target) *rapid.Generator[delivery.Mechanism] {
	service := "s3"
	if arn, ok := targetARN(target); ok {
		service = arn.Service
	}
	if service == "events" {
		service = "eventbridge"
	}
	var into []delivery.Mechanism
	for _, m := range delivery.Mechanisms() {
		if string(m.Target()) == service && m.Via() != ref.SchedulerTarget {
			into = append(into, m)
		}
	}
	return rapid.SampledFrom(into)
}

// A Lambda delivery's record keeps its sender, the execution role and what
// it carries by wire name, and a queue target its trace header; neither key
// is written when there is nothing to keep.
func TestLambdaEntryRecordWireShape(t *testing.T) {
	c := codec()
	role := aws.ARN{Partition: "aws", Service: "iam", Account: "000000000000", Resource: "role/worker"}
	queue := aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "records"}
	fn := aws.ARN{Partition: "aws", Service: "lambda", Region: "us-east-1", Account: "000000000000", Resource: "function:worker"}
	at := clock.FromWire(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	st := newState(aws.Scope{})
	st.Entries["d-1"] = Entry{ID: "d-1", Accepted: at, NextAt: at, Request: delivery.Request{
		Source: fn, Mechanism: delivery.LambdaToSQS, Policy: delivery.None{},
		Target: delivery.SQSQueue{ARN: queue, Body: "{}", TraceHeader: "Root=1-5759e988-bd862e3fe1be46a994272793"},
		Sender: &delivery.LambdaSender{Role: role, Carries: delivery.DeadLetter},
	}}
	st.Entries["d-2"] = Entry{ID: "d-2", Accepted: at, NextAt: at, Request: delivery.Request{
		Source:    aws.ARN{Partition: "aws", Service: "sns", Region: "us-east-1", Account: "000000000000", Resource: "orders"},
		Mechanism: delivery.SNSToSQS, Policy: delivery.SNSManaged{}, Target: delivery.SQSQueue{ARN: queue, Body: "{}"},
	}}
	data, err := c.Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{
		`"sender":{"carries":"dead-letter","role":"arn:aws:iam::000000000000:role/worker"}`,
		`"traceHeader":"Root=1-5759e988-bd862e3fe1be46a994272793"`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("snapshot\n%s\ndoes not hold %s", data, want)
		}
	}
	if n := bytes.Count(data, []byte(`"sender"`)); n != 1 {
		t.Errorf("snapshot holds %d senders, want d-1's alone:\n%s", n, data)
	}
	if n := bytes.Count(data, []byte(`"traceHeader"`)); n != 1 {
		t.Errorf("snapshot holds %d trace headers, want d-1's alone:\n%s", n, data)
	}
}
