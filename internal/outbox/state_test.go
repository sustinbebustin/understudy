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
				ARN:        genARN("sqs").Draw(t, "queue"),
				Body:       rapid.String().Draw(t, "body"),
				Attributes: genAttributes().Draw(t, "attributes"),
				GroupID:    rapid.StringMatching(`[a-z]{0,4}`).Draw(t, "group"),
				DedupID:    rapid.StringMatching(`[a-z]{0,4}`).Draw(t, "dedup"),
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
	)
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
// delivers to the service of its target.
func genEntry() *rapid.Generator[Entry] {
	return rapid.Custom(func(t *rapid.T) Entry {
		at := clock.FromWire(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
		var dlq *aws.ARN
		if rapid.Bool().Draw(t, "dlq") {
			a := genARN("sqs").Draw(t, "dlqARN")
			dlq = &a
		}
		target := genTarget().Draw(t, "target")
		return Entry{
			ID: ids.DeliveryID(rapid.StringMatching(`[0-9a-f]{8}`).Draw(t, "id")),
			Request: delivery.Request{
				Source:    genARN("sns").Draw(t, "source"),
				Mechanism: genMechanismInto(target).Draw(t, "mechanism"),
				Target:    target,
				Policy:    genPolicy().Draw(t, "policy"),
				DLQ:       dlq,
			},
			Attempt:  rapid.IntRange(0, 100).Draw(t, "attempt"),
			NextAt:   at.Add(time.Duration(rapid.IntRange(0, 1e6).Draw(t, "next")) * time.Millisecond),
			Cause:    ref.Cause{Parent: ref.EventID(rapid.Uint64Range(0, 1e6).Draw(t, "parent")), Trace: ref.EventID(rapid.Uint64Range(0, 1e6).Draw(t, "trace"))},
			Accepted: at,
			Seq:      rapid.Uint64().Draw(t, "seq"),
			Jitter:   rapid.Uint64().Draw(t, "jitter"),
		}
	})
}

// genMechanismInto draws a mechanism that delivers to target's service.
func genMechanismInto(target delivery.Target) *rapid.Generator[delivery.Mechanism] {
	arn, _ := targetARN(target)
	var into []delivery.Mechanism
	for _, m := range delivery.Mechanisms() {
		if string(m.Target()) == arn.Service {
			into = append(into, m)
		}
	}
	return rapid.SampledFrom(into)
}
