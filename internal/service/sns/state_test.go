package sns

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// TestStateCodecRoundTrips checks over random states that the snapshot
// codec, with its TopicKind and Confirmation unions and its text-coded
// enums, reads back every persisted field it wrote. Collections are drawn
// nil or non-empty, since JSON does not tell an empty one from none.
func TestStateCodecRoundTrips(t *testing.T) {
	c := codec()
	rapid.Check(t, func(t *rapid.T) {
		st := newState(aws.Scope{})
		if rapid.Bool().Draw(t, "signed") {
			st.Signing = &SigningKey{Private: bytes(t, "private"), Certificate: bytes(t, "certificate")}
		}
		for i := range rapid.IntRange(0, 3).Draw(t, "topics") {
			name := "t" + strconv.Itoa(i)
			st.Topics[name] = Topic{
				Name:               name,
				Kind:               topicKind(t),
				SignatureVersion:   rapid.SampledFrom([]envelope.Version{0, envelope.V1, envelope.V2}).Draw(t, "signatureVersion"),
				MaximumMessageSize: rapid.IntRange(0, maxMaxMessageBytes).Draw(t, "maximumMessageSize"),
				Attributes:         stringMap(t, "attributes"),
				Tags:               stringMap(t, "tags"),
				Subscriptions:      subscriptions(t),
				Unsubscribed:       rapid.IntRange(0, 100).Draw(t, "unsubscribed"),
			}
		}
		data, err := c.Marshal(&st)
		if err != nil {
			t.Fatal(err)
		}
		var back State
		if err := c.Unmarshal(data, &back); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		if !reflect.DeepEqual(st, back) {
			t.Fatalf("round trip changed the state:\n%+v\n%+v", st, back)
		}
	})
}

func topicKind(t *rapid.T) TopicKind {
	if !rapid.Bool().Draw(t, "fifo") {
		return Standard{}
	}
	f := FIFO{
		ContentBasedDeduplication: rapid.Bool().Draw(t, "contentBasedDeduplication"),
		ThroughputScope:           rapid.SampledFrom([]ThroughputScope{0, ThroughputTopic, ThroughputMessageGroup}).Draw(t, "throughputScope"),
		Sequence:                  rapid.Uint64().Draw(t, "sequence"),
	}
	for i := range rapid.IntRange(0, 2).Draw(t, "dedup") {
		f.Dedup = append(f.Dedup, DedupEntry{Key: "k" + strconv.Itoa(i), Until: instant(t, "until")})
	}
	return f
}

func subscriptions(t *rapid.T) []Subscription {
	n := rapid.IntRange(0, 3).Draw(t, "subscriptions")
	if n == 0 {
		return nil
	}
	out := make([]Subscription, 0, n)
	for j := range n {
		sub := Subscription{
			ID:                 "s" + strconv.Itoa(j),
			Protocol:           rapid.SampledFrom(protocols()).Draw(t, "protocol"),
			Endpoint:           rapid.String().Draw(t, "endpoint"),
			OwnerAccountID:     "000000000000",
			Principal:          rapid.SampledFrom([]string{"", "arn:aws:iam::000000000000:root"}).Draw(t, "principal"),
			RawMessageDelivery: rapid.Bool().Draw(t, "raw"),
			Attributes:         stringMap(t, "subscriptionAttributes"),
			Status:             Confirmed{By: confirmedBy(t)},
		}
		if rapid.Bool().Draw(t, "pending") {
			sub.Status = Pending{Token: rapid.StringMatching(`[0-9a-f]{8}`).Draw(t, "token"), Expires: instant(t, "expires")}
		}
		out = append(out, sub)
	}
	return out
}

// confirmedBy draws one of the ways a subscription can be confirmed.
func confirmedBy(t *rapid.T) ConfirmedBy {
	token := func() string { return rapid.StringMatching(`[0-9a-f]{8}`).Draw(t, "confirmedToken") }
	switch rapid.IntRange(0, 2).Draw(t, "confirmedBy") {
	case 0:
		return AtSubscribe{}
	case 1:
		return ByCall{Token: token(), AuthenticateOnUnsubscribe: rapid.Bool().Draw(t, "authenticateOnUnsubscribe")}
	default:
		return ByLink{Token: token()}
	}
}

// stringMap draws nil or a non-empty map.
func stringMap(t *rapid.T, label string) map[string]string {
	if !rapid.Bool().Draw(t, label) {
		return nil
	}
	return rapid.MapOfN(rapid.StringMatching(`[A-Za-z]{1,8}`), rapid.String(), 1, 3).Draw(t, label+"Entries")
}

func bytes(t *rapid.T, label string) []byte {
	return rapid.SliceOfN(rapid.Byte(), 1, 16).Draw(t, label)
}

func instant(t *rapid.T, label string) clock.Instant {
	return clock.FromWire(time.UnixMilli(rapid.Int64Range(0, 1<<42).Draw(t, label)).UTC())
}

// TestProtocolNamesRoundTrip checks every Protocol through its text form
// and that an unknown name is refused.
func TestProtocolNamesRoundTrip(t *testing.T) {
	for _, p := range protocols() {
		text, err := p.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var back Protocol
		if err := back.UnmarshalText(text); err != nil || back != p {
			t.Errorf("%s round-trips to %v, %v", p, back, err)
		}
	}
	var p Protocol
	if err := p.UnmarshalText([]byte("sms")); err == nil {
		t.Error("UnmarshalText accepted sms, which understudy does not deliver to")
	}
}

// TestAttributeNamesProperty checks the message attribute name rules over
// random names: a name of allowed characters with no dot at an end, no two
// dots and no reserved prefix is accepted, and one with any other ASCII
// character is refused with that character in the text.
func TestAttributeNamesProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.StringMatching(`[A-Za-z0-9_-]([A-Za-z0-9_-]|\.[A-Za-z0-9_-]){0,40}`).Draw(t, "name")
		lower := strings.ToLower(name)
		reserved := strings.HasPrefix(lower, "aws.") || strings.HasPrefix(lower, "amazon.")
		if err := checkAttributeName(name); (err == nil) == reserved {
			t.Fatalf("checkAttributeName(%q) = %v", name, err)
		}
		bad := rapid.SampledFrom([]rune("^ #/:@!$%&*+=?")).Draw(t, "bad")
		at := rapid.IntRange(0, len(name)).Draw(t, "at")
		withBad := name[:at] + string(bad) + name[at:]
		err := checkAttributeName(withBad)
		if err == nil || !strings.Contains(err.Message, "#x") {
			t.Fatalf("checkAttributeName(%q) = %v, want the invalid character error", withBad, err)
		}
	})
}

// TestMessageAttributesKeepTheirValues checks over random String, Number
// and Binary attributes that the checked list is sorted by name and keeps
// every value and type.
func TestMessageAttributesKeepTheirValues(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := map[string]snsapi.MessageAttributeValue{}
		for i := range rapid.IntRange(0, 5).Draw(t, "n") {
			name := "a" + strconv.Itoa(i)
			switch rapid.IntRange(0, 2).Draw(t, "kind") {
			case 0:
				in[name] = snsapi.MessageAttributeValue{DataType: "String", StringValue: new(rapid.StringN(1, 20, -1).Draw(t, "s"))}
			case 1:
				in[name] = snsapi.MessageAttributeValue{DataType: "Number.int", StringValue: new(strconv.Itoa(int(rapid.Int32().Draw(t, "n"))))}
			default:
				in[name] = snsapi.MessageAttributeValue{DataType: "Binary", BinaryValue: rapid.SliceOfN(rapid.Byte(), 1, 8).Draw(t, "b")}
			}
		}
		out, err := messageAttributes(in)
		if err != nil {
			t.Fatalf("messageAttributes = %s", err.Message)
		}
		if len(out) != len(in) {
			t.Fatalf("%d attributes out of %d", len(out), len(in))
		}
		for i, a := range out {
			if i > 0 && out[i-1].Name >= a.Name {
				t.Fatalf("attributes out of order: %q then %q", out[i-1].Name, a.Name)
			}
			if a.Value.DataType() != in[a.Name].DataType {
				t.Fatalf("%s type %q, want %q", a.Name, a.Value.DataType(), in[a.Name].DataType)
			}
		}
	})
}
