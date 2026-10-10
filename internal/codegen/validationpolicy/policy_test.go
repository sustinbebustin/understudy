package validationpolicy

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The tests stand catalogue identities in for generated ones: what matters
// is which rule answers, not the code's wire spelling.
var (
	serviceCode   = awserr.SerializationException
	memberCode    = awserr.InvalidAction
	operationCode = awserr.AccessDeniedException
)

func text(s string) awserr.Rendered { return awserr.RenderedText{Text: s} }

// sqsLike is a policy shaped like SQS's: a service Coral rule, a parameter
// rule on queueName for length and pattern, a missingParameter rule on
// queueUrl, and a parameter rule on the elements of a list.
func sqsLike() *validationPolicy {
	return &validationPolicy{
		paths: []validationPath{
			{segments: []string{"queueName"}, param: "QueueName", rules: []validationRule{
				{kinds: kindLength | kindPattern, id: memberCode, style: styleParameter, reason: "Can only include alphanumeric characters."},
			}},
			{segments: []string{"queueUrl"}, param: "QueueUrl", rules: []validationRule{
				{kinds: kindRequired, id: memberCode, style: styleMissingParameter},
			}},
			{segments: []string{"delaySeconds"}, param: "DelaySeconds"},
			{segments: []string{"tags"}, param: "Tags"},
			{segments: []string{"tags", "*", "member"}, param: "Tags", rules: []validationRule{
				{kinds: kindLength, id: memberCode, style: styleParameter, reason: "Tags are not empty."},
			}},
		},
		service: []validationRule{{id: serviceCode, style: styleCoral}},
	}
}

func TestRenderOfNoViolationsIsNil(t *testing.T) {
	if err := sqsLike().render(nil); err != nil {
		t.Fatalf("render(nil) = %+v", err)
	}
}

// TestTheFirstViolationPicksTheCodeAndStyle: SQS answers a bad queue name
// with its hand text naming the first violation only.
func TestTheFirstViolationPicksTheCodeAndStyle(t *testing.T) {
	vs := []awserr.Violation{
		{Path: awserr.Path{"queueName"}, Value: text("a b"), Constraint: awserr.Pattern{Echo: "[a-z]+"}},
		{Path: awserr.Path{"delaySeconds"}, Value: text("901"), Constraint: awserr.RangeMax{Max: "900"}},
	}
	err := sqsLike().render(vs)
	want := "Value a b for parameter QueueName is invalid. Reason: Can only include alphanumeric characters."
	if err.ID != memberCode() || err.Message != want {
		t.Fatalf("render = %s %q\nwant %s %q", err.ID.Shape, err.Message, memberCode().Shape, want)
	}
	if o, ok := err.Origin.(awserr.Validation); !ok || !reflect.DeepEqual(o.Violations, vs) {
		t.Fatalf("origin = %#v, want Validation over every violation", err.Origin)
	}
}

// TestCoralListsOnlyViolationsUnderTheChosenCode: a Coral message counts
// the violations whose rule answers with the same code, not the others.
func TestCoralListsOnlyViolationsUnderTheChosenCode(t *testing.T) {
	vs := []awserr.Violation{
		{Path: awserr.Path{"delaySeconds"}, Value: text("901"), Constraint: awserr.RangeMax{Max: "900"}},
		{Path: awserr.Path{"queueName"}, Value: text("a b"), Constraint: awserr.Pattern{Echo: "[a-z]+"}},
		{Path: awserr.Path{"tags"}, Value: text("[a, b, c]"), Constraint: awserr.LengthMax{Max: 2}},
	}
	err := sqsLike().render(vs)
	want := "2 validation errors detected: " +
		"Value '901' at 'delaySeconds' failed to satisfy constraint: Member must have value less than or equal to 900; " +
		"Value '[a, b, c]' at 'tags' failed to satisfy constraint: Member must have length less than or equal to 2"
	if err.ID != serviceCode() || err.Message != want {
		t.Fatalf("render = %s %q\nwant %q", err.ID.Shape, err.Message, want)
	}
}

func TestMissingParameterNamesTheMember(t *testing.T) {
	err := sqsLike().render([]awserr.Violation{{Path: awserr.Path{"queueUrl"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}}})
	if want := "The request must contain the parameter QueueUrl."; err.ID != memberCode() || err.Message != want {
		t.Fatalf("render = %s %q, want %q", err.ID.Shape, err.Message, want)
	}
}

// TestAListElementNamesItsCollection: an element path matches the "*"
// template and prints the member that holds the list.
func TestAListElementNamesItsCollection(t *testing.T) {
	err := sqsLike().render([]awserr.Violation{{Path: awserr.Path{"tags", "2", "member"}, Value: text(""), Constraint: awserr.LengthMin{Min: 1}}})
	if want := "Value  for parameter Tags is invalid. Reason: Tags are not empty."; err.Message != want {
		t.Fatalf("message = %q, want %q", err.Message, want)
	}
}

// TestAKindOutsideTheFilterFallsThrough: a member rule covers only its
// kinds; another kind falls to the operation's rule, then the service's.
func TestAKindOutsideTheFilterFallsThrough(t *testing.T) {
	p := sqsLike()
	v := awserr.Violation{Path: awserr.Path{"queueName"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}}
	if err := p.render([]awserr.Violation{v}); err.ID != serviceCode() {
		t.Fatalf("without an operation rule: %s, want the service's", err.ID.Shape)
	}
	p.operation = []validationRule{{id: operationCode, style: styleCoral}}
	err := p.render([]awserr.Violation{v})
	if want := "1 validation error detected: Value null at 'queueName' failed to satisfy constraint: Member must not be null"; err.ID != operationCode() || err.Message != want {
		t.Fatalf("with an operation rule: %s %q", err.ID.Shape, err.Message)
	}
}

// TestUnfilteredKindsMatchOnlyUnfilteredRules: a constraint no filter names
// (Encoding) skips every filtered rule.
func TestUnfilteredKindsMatchOnlyUnfilteredRules(t *testing.T) {
	err := sqsLike().render([]awserr.Violation{{Path: awserr.Path{"queueName"}, Value: text("x"), Constraint: awserr.Encoding{}}})
	if err.ID != serviceCode() {
		t.Fatalf("Encoding answered with %s, want the service's unfiltered rule", err.ID.Shape)
	}
}

// TestSNSParameterStyle: SNS prints the parameter and reason, not the value.
func TestSNSParameterStyle(t *testing.T) {
	p := &validationPolicy{
		paths:   []validationPath{{segments: []string{"topicArn"}, param: "TopicArn"}},
		service: []validationRule{{id: memberCode, style: styleSNSParameter, reason: "bad"}},
	}
	err := p.render([]awserr.Violation{{Path: awserr.Path{"topicArn"}, Value: text("x"), Constraint: awserr.Pattern{Echo: "arn:.*"}}})
	if want := "Invalid parameter: TopicArn Reason: bad"; err.Message != want {
		t.Fatalf("message = %q, want %q", err.Message, want)
	}
}

// TestHandTextRendersValues: values print unquoted, omitted ones empty and
// absent ones as null; an unmatched path prints its last segment.
func TestHandTextRendersValues(t *testing.T) {
	p := &validationPolicy{service: []validationRule{{id: memberCode, style: styleParameter, reason: "r"}}}
	for _, tt := range []struct {
		value awserr.Rendered
		want  string
	}{
		{text("v"), "Value v for parameter body is invalid. Reason: r"},
		{awserr.RenderedOmitted{}, "Value  for parameter body is invalid. Reason: r"},
		{awserr.RenderedNull{}, "Value null for parameter body is invalid. Reason: r"},
	} {
		err := p.render([]awserr.Violation{{Path: awserr.Path{"body"}, Value: tt.value, Constraint: awserr.Required{}}})
		if err.Message != tt.want {
			t.Errorf("message = %q, want %q", err.Message, tt.want)
		}
	}
}
