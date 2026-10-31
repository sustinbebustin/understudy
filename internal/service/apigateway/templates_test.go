package apigateway_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// Velocity interprets only $ references, # directives and \ escapes (the
// Velocity 1.7 VTL reference,
// https://velocity.apache.org/engine/1.7/vtl-reference-guide.html, the
// dialect apigateway-rest.md 7.1 infers), so a template with none of the
// three renders unchanged and is the only kind understudy runs
// (apigateway-rest.md 12.7, the literal-only rule).
func TestIsLiteralTemplateVectors(t *testing.T) {
	for in, want := range map[string]bool{
		"":                       true,
		`{"statusCode": 200}`:    true,
		`{statusCode: 200}`:      true,
		"plain text\nwith lines": true,
		`{"statusCode": 200, "id": $input.params().path.id}`: false, // test_apigateway_basic.py:L1697-L1724
		"#set($x = 1)": false,
		`a\b`:          false,
		"$":            false,
		"#":            false,
		`\`:            false,
	} {
		if got := apigateway.IsLiteralTemplate(in); got != want {
			t.Errorf("IsLiteralTemplate(%q) = %v, want %v", in, got, want)
		}
	}
}

// The boundary as a property: any string without '$', '#' and '\' is
// literal, and any string containing one of them is not (spec 6b, VTL
// decision: "a literal template renders unchanged, and a template with one
// of the three characters is refused").
func TestIsLiteralTemplateProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "template")
		want := !strings.ContainsAny(s, `$#\`)
		if got := apigateway.IsLiteralTemplate(s); got != want {
			t.Fatalf("IsLiteralTemplate(%q) = %v, want %v", s, got, want)
		}
	})
	rapid.Check(t, func(t *rapid.T) {
		pre := rapid.String().Draw(t, "prefix")
		c := rapid.SampledFrom([]string{"$", "#", `\`}).Draw(t, "special")
		post := rapid.String().Draw(t, "suffix")
		if apigateway.IsLiteralTemplate(pre + c + post) {
			t.Fatalf("IsLiteralTemplate(%q) = true, want false (it holds %q)", pre+c+post, c)
		}
	})
}

// A MOCK request template selects the status it renders; the parse is
// lenient, so {statusCode: 200} (an unquoted key) works as
// {"statusCode": 200} does (aws-recorded, localstack@8b9a79f
// tests/aws/services/apigateway/test_apigateway_basic.snapshot.json:L154-L161
// and test_apigateway_integrations.snapshot.json:L1062-L1083;
// apigateway-rest.md 6.3).
func TestMockStatus(t *testing.T) {
	for in, want := range map[string]int{
		`{"statusCode": 200}`:             200,
		`{"statusCode":200}`:              200,
		`{statusCode: 200}`:               200,
		`{"statusCode": 404, "other": 1}`: 404,
		"{\n  \"statusCode\" : 500\n}":    500,
	} {
		if got, ok := apigateway.MockStatus(in); !ok || got != want {
			t.Errorf("MockStatus(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	// A template without a statusCode selects nothing (contract 7.5; the
	// out-of-range rule there is inferred and not asserted).
	for _, in := range []string{"", "{}", `{"code": 200}`} {
		if got, ok := apigateway.MockStatus(in); ok {
			t.Errorf("MockStatus(%q) = %d, true; want no status", in, got)
		}
	}
}
