package codegen_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// closedColor makes the fixture's Color member a validated enum again (the
// fixture overlay opens it), so its enum violation is generated.
const closedColor = `"com.example.golden#PutKindsRequest$Color": {"type": "apply", "replace": true, "traits": {
	"dev.understudy#validationError": {"enumPolicy": "closed"}, ` + evidence + `}}`

// TestEnumOrderSetsTheViolationOrder: dev.understudy#enumOrder lists an
// enum's values in the order AWS's violation text does, which need not be
// the model's (API Gateway's Op, test_apigateway_api.snapshot.json:L4809).
func TestEnumOrderSetsTheViolationOrder(t *testing.T) {
	files, err := codegen.Generate(goldenInputs(t, overlay(closedColor+`,
		"com.example.golden#Color": {"type": "apply", "traits": {"dev.understudy#enumOrder": ["dark-blue", "red"], `+evidence+`}}`)))
	if err != nil {
		t.Fatal(err)
	}
	if want := `awserr.Enum{Values: []string{"dark-blue", "red"}}`; !bytes.Contains(files["validate_gen.go"], []byte(want)) {
		t.Fatalf("validate_gen.go lacks %s:\n%s", want, files["validate_gen.go"])
	}
}

// TestEnumOrderMustBeAPermutation: an order that drops, adds or repeats a
// value fails at IR naming the shape.
func TestEnumOrderMustBeAPermutation(t *testing.T) {
	for _, order := range []string{`["red"]`, `["red", "dark-blue", "green"]`, `["red", "red"]`} {
		_, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#Color": {"type": "apply", "traits": {
			"dev.understudy#enumOrder": `+order+`, `+evidence+`}}`)))
		if err == nil || !strings.Contains(err.Error(), "com.example.golden#Color") || !strings.Contains(err.Error(), "not a permutation") {
			t.Errorf("enumOrder %s: Generate = %v, want an IR failure naming the shape", order, err)
		}
	}
}

// TestEnumOrderSitsOnEnumShapes: on a string shape it is refused at the
// overlay stage.
func TestEnumOrderSitsOnEnumShapes(t *testing.T) {
	_, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#String": {"type": "apply", "traits": {
		"dev.understudy#enumOrder": ["a"], `+evidence+`}}`)))
	if err == nil || !strings.Contains(err.Error(), "dev.understudy#enumOrder") {
		t.Fatalf("Generate = %v, want a site failure", err)
	}
}

// TestValidationInputRootNamesTheInput: with dev.understudy#validationInputRoot
// every operation's policy carries <operation>Input as its root (API
// Gateway's updateRestApiInput, test_apigateway_api.snapshot.json:L4809).
// The fixture is awsJson, so no member is a URI label and none is
// unrooted.
func TestValidationInputRootNamesTheInput(t *testing.T) {
	files, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#Golden": {"type": "apply", "traits": {
		"dev.understudy#validationInputRoot": {}, `+evidence+`}}`)))
	if err != nil {
		t.Fatal(err)
	}
	src := files["validate_gen.go"]
	if want := regexp.MustCompile(`\troot:\s+"putKindsInput",`); !want.Match(src) {
		t.Fatalf("validate_gen.go lacks %s:\n%s", want, src)
	}
	if bytes.Contains(src, []byte("unrooted:")) {
		t.Fatalf("validate_gen.go names unrooted members of an awsJson service:\n%s", src)
	}
}
