package events

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// A rule's ARN is rule/<name> on the default bus and rule/<bus>/<name> on a
// custom bus (eventbridge-buses.md 5.2), and parseRuleARN reads each back
// to the rule it names. No rule has an ARN that names the default bus, an
// empty part or a third segment.
func TestRuleARNsRoundTrip(t *testing.T) {
	t.Parallel()
	scope := aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
	const prefix = "arn:aws:events:us-east-1:000000000000:"
	for _, want := range []struct {
		ref ruleRef
		arn string
	}{
		{ruleRef{scope: scope, bus: defaultBus, name: "r"}, prefix + "rule/r"},
		{ruleRef{scope: scope, bus: "orders", name: "r"}, prefix + "rule/orders/r"},
	} {
		if got := want.ref.arn().String(); got != want.arn {
			t.Errorf("%+v.arn() = %s, want %s", want.ref, got, want.arn)
		}
		if got, ok := parseRuleARN(want.arn); !ok || got != want.ref {
			t.Errorf("parseRuleARN(%s) = %+v, %t; want %+v", want.arn, got, ok, want.ref)
		}
	}
	for _, resource := range []string{"rule/", "rule/default/r", "rule//r", "rule/orders/", "rule/orders/r/x", "event-bus/orders"} {
		if got, ok := parseRuleARN(prefix + resource); ok {
			t.Errorf("parseRuleARN(%s) = %+v, want no rule", prefix+resource, got)
		}
	}
}
