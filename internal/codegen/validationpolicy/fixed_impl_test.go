package validationpolicy

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// TestFixedStyleSendsTheReasonAsTheMessage: a fixed rule answers with its
// reason whole, whatever the violation, as S3 answers a configuration
// without its required rule with its MalformedXML text (s3.md 4.2.2).
func TestFixedStyleSendsTheReasonAsTheMessage(t *testing.T) {
	const fixed = "The XML you provided was not well-formed or did not validate against our published schema"
	p := &validationPolicy{
		paths: []validationPath{
			{segments: []string{"config"}, param: "Config"},
			{segments: []string{"config", "rules"}, param: "Rules", rules: []validationRule{
				{kinds: kindRequired, id: memberCode, style: styleFixed, reason: fixed},
			}},
		},
		service: []validationRule{{id: serviceCode, style: styleCoral}},
	}
	err := p.render([]awserr.Violation{{Path: awserr.Path{"config", "rules"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}}})
	if err.ID != memberCode() || err.Message != fixed {
		t.Fatalf("render = %s %q, want %s %q", err.ID.Shape, err.Message, memberCode().Shape, fixed)
	}
	other := p.render([]awserr.Violation{{Path: awserr.Path{"config"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}}})
	if other.ID != serviceCode() {
		t.Fatalf("a violation the fixed rule does not cover = %s, want the service code %s", other.ID.Shape, serviceCode().Shape)
	}
}

// TestLengthMaxRuleLeavesTheMinimumToTheNextRule: a rule filtered to the
// length maximum answers an over-long value with its fixed text, as Logs
// answers a 513-character log group name (secrets-logs-events-alarms.md
// 3.1), while a too-short value falls through to the service's Coral rule.
func TestLengthMaxRuleLeavesTheMinimumToTheNextRule(t *testing.T) {
	const fixed = "Invalid LogGroup or LogStream, both must have length less than or equal to 512."
	p := &validationPolicy{
		paths: []validationPath{
			{segments: []string{"logGroupName"}, param: "logGroupName", rules: []validationRule{
				{kinds: kindLengthMax, id: memberCode, style: styleFixed, reason: fixed},
			}},
		},
		service: []validationRule{{id: serviceCode, style: styleCoral}},
	}
	long := p.render([]awserr.Violation{{Path: awserr.Path{"logGroupName"}, Value: text("x"), Constraint: awserr.LengthMax{Max: 512}}})
	if long.ID != memberCode() || long.Message != fixed {
		t.Fatalf("over-long = %s %q, want %s %q", long.ID.Shape, long.Message, memberCode().Shape, fixed)
	}
	short := p.render([]awserr.Violation{{Path: awserr.Path{"logGroupName"}, Value: text(""), Constraint: awserr.LengthMin{Min: 1}}})
	want := "1 validation error detected: Value '' at 'logGroupName' failed to satisfy constraint: Member must have length greater than or equal to 1"
	if short.ID != serviceCode() || short.Message != want {
		t.Fatalf("too short = %s %q, want %s %q", short.ID.Shape, short.Message, serviceCode().Shape, want)
	}
}
