package validationpolicy

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// TestCoralRuleEchoAppliesToItsViolationsOnly: each violation in one Coral
// message renders under the echo of the rule that answers it. DynamoDB
// Streams' service rule omits the value (dynamodb-streams.md 6.5); a member
// rule under the same code that sets no echo keeps it.
func TestCoralRuleEchoAppliesToItsViolationsOnly(t *testing.T) {
	p := &validationPolicy{
		paths: []validationPath{
			{segments: []string{"limit"}, param: "Limit"},
			{segments: []string{"shardId"}, param: "ShardId", rules: []validationRule{
				{kinds: kindLength, id: serviceCode, style: styleCoral},
			}},
		},
		service: []validationRule{{id: serviceCode, style: styleCoral, echo: awserr.CoralEcho{Value: awserr.OmitValue}}},
	}
	err := p.render([]awserr.Violation{
		{Path: awserr.Path{"limit"}, Value: text("0"), Constraint: awserr.RangeMin{Min: "1"}},
		{Path: awserr.Path{"shardId"}, Value: text("short"), Constraint: awserr.LengthMin{Min: 28}},
	})
	const want = "2 validation errors detected: Value at 'limit' failed to satisfy constraint: Member must have value greater than or equal to 1; " +
		"Value 'short' at 'shardId' failed to satisfy constraint: Member must have length greater than or equal to 28"
	if err.ID != serviceCode() || err.Message != want {
		t.Fatalf("render = %s %q\nwant %s %q", err.ID.Shape, err.Message, serviceCode().Shape, want)
	}
}
