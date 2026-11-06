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
