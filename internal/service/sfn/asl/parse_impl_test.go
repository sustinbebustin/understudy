package asl_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// Every construct of the closed grammar, in one machine, parses with every
// feature and no diagnostic (spec "Definition grammar"; stepfunctions-asl.md
// 3.3, 6.2, 7.1, 8.2-8.4, 9.1-9.2).
func TestParseAcceptsTheWholeGrammar(t *testing.T) {
	def := `{
	"Comment": "all", "Version": "1.0", "QueryLanguage": "JSONPath", "TimeoutSeconds": 60, "StartAt": "P",
	"States": {
		"P": {"Type": "Pass", "InputPath": "$.a[*]", "Parameters": {"x.$": "$.b", "c.$": "$$.Execution.Id", "u.$": "States.UUID()", "lit": [1, {"k.$": "kept"}], "n": {"m.$": "$.m"}},
			"ResultPath": "$.r", "OutputPath": null, "Next": "C"},
		"C": {"Type": "Choice", "Choices": [
			{"Variable": "$.v", "NumericEquals": 1, "Next": "W"},
			{"And": [{"Variable": "$.s", "StringMatches": "a*"}, {"Not": {"Variable": "$.t", "IsPresent": true}}], "Next": "T"},
			{"Or": [{"Variable": "$$.State.Name", "StringEqualsPath": "$.name"}, {"Variable": "$.ts", "TimestampLessThan": "2016-03-14T01:59:00Z"}], "Next": "X"}
		], "Default": "F"},
		"W": {"Type": "Wait", "TimestampPath": "$.when", "Next": "M"},
		"T": {"Type": "Task", "Resource": "arn:aws:states:::lambda:invoke", "Parameters": {"FunctionName.$": "$.fn", "Payload.$": "$", "InvocationType.$": "$.type"},
			"ResultSelector": {"p.$": "$.Payload"}, "TimeoutSecondsPath": "$.timeout",
			"Retry": [{"ErrorEquals": ["Lambda.TooManyRequestsException"], "IntervalSeconds": 2, "MaxAttempts": 0, "BackoffRate": 1.5, "MaxDelaySeconds": 5, "JitterStrategy": "FULL"}, {"ErrorEquals": ["States.ALL"]}],
			"Catch": [{"ErrorEquals": ["States.TaskFailed"], "ResultPath": "$.err", "Next": "F"}, {"ErrorEquals": ["States.ALL"], "ResultPath": null, "Next": "S"}],
			"End": true},
		"X": {"Type": "Parallel", "Branches": [{"StartAt": "B1", "States": {"B1": {"Type": "Pass", "End": true}}}, {"Comment": "two", "StartAt": "B2", "States": {"B2": {"Type": "Succeed"}}}],
			"ResultPath": "$.branches", "End": true},
		"M": {"Type": "Map", "ItemsPath": "$.items", "ItemSelector": {"v.$": "$$.Map.Item.Value"}, "MaxConcurrencyPath": "$.max",
			"ItemProcessor": {"ProcessorConfig": {"Mode": "INLINE"}, "StartAt": "I", "States": {"I": {"Type": "Task", "Resource": "` + functionArn + `:live", "TimeoutSeconds": 5, "End": true}}},
			"Next": "L"},
		"L": {"Type": "Map", "Iterator": {"StartAt": "J", "States": {"J": {"Type": "Pass", "End": true}}}, "Parameters": {"x": 1}, "MaxConcurrency": 0, "End": true},
		"F": {"Type": "Fail", "ErrorPath": "$.e", "CausePath": "States.Format('{} failed', $.name)"},
		"S": {"Type": "Succeed", "InputPath": "$.out"}
	}}`
	if d, diags := asl.Parse(def, allFeatures); d == nil || len(diags) != 0 {
		t.Fatalf("Parse = %v, %+v; want accepted with no diagnostics", d != nil, diags)
	}
}

// AWS's rules that the blind tests do not reach; each definition is
// refused with an ERROR that is not a refusal. Codes are asserted where AWS
// documents the code (api/API_ValidateStateMachineDefinitionDiagnostic);
// messages and locations are understudy's (ASL-01).
func TestParseRefusesStructuralProblems(t *testing.T) {
	branch := func(states string) string {
		return `{"StartAt":"X","States":{"X":{"Type":"Parallel","Branches":[{"StartAt":"B","States":{` + states + `}}],"Next":"Out"},"Out":{"Type":"Succeed"}}}`
	}
	tests := []struct{ name, def, code, source string }{
		{"Next out of a branch", branch(`"B":{"Type":"Pass","Next":"Out"}`), "MISSING_TRANSITION_TARGET", "stepfunctions-asl.md 3.2: branches transition among themselves"},
		{"branch without a terminal state", branch(`"B":{"Type":"Pass","Next":"C"},"C":{"Type":"Pass","Next":"B"}`), "MISSING_END_STATE", "stepfunctions-asl.md 3.2"},
		{"branch without StartAt", `{"StartAt":"X","States":{"X":{"Type":"Parallel","Branches":[{"States":{}}],"End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 3.3"},
		{"Default names no state", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.v","IsNull":true,"Next":"P"}],"Default":"Nope"},` + terminalP + `}}`, "MISSING_TRANSITION_TARGET", "stepfunctions-asl.md 3.2"},
		{"Catch Next names no state", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Nope"}],"End":true}}}`, "MISSING_TRANSITION_TARGET", "stepfunctions-asl.md 3.2, 8.4"},
		{"Catch without Next", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Catch":[{"ErrorEquals":["States.ALL"]}],"End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 8.4"},
		{"Retry without ErrorEquals", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Retry":[{"MaxAttempts":1}],"End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 8.2"},
		{"States.ALL not last", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Retry":[{"ErrorEquals":["States.ALL"]},{"ErrorEquals":["E"]}],"End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 8.1"},
		{"States.ALL with another name", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Retry":[{"ErrorEquals":["States.ALL","E"]}],"End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 8.1"},
		{"top-level rule without Next", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.v","IsNull":true}],"Default":"P"},` + terminalP + `}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 6.1"},
		{"nested rule with Next", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Not":{"Variable":"$.v","IsNull":true,"Next":"P"},"Next":"P"}]},` + terminalP + `}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 6.1"},
		{"rule with two operators", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.v","IsNull":true,"IsString":true,"Next":"P"}]},` + terminalP + `}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 6.1"},
		{"data test without Variable", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"NumericEquals":1,"Next":"P"}]},` + terminalP + `}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 6.1"},
		{"empty And", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"And":[],"Next":"P"}]},` + terminalP + `}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 6.1"},
		{"operand of the wrong type", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.v","NumericEquals":"1","Next":"P"}]},` + terminalP + `}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 6.2"},
		{"StringMatchesPath", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.v","StringMatchesPath":"$.p","Next":"P"}]},` + terminalP + `}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 6.2: StringMatches has no Path form"},
		{"Fail with Error and ErrorPath", `{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"E","ErrorPath":"$.e"}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 3.3"},
		{"Fail ErrorPath calling a non-string function", `{"StartAt":"F","States":{"F":{"Type":"Fail","ErrorPath":"States.Array(1)"}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 5.1"},
		{"lambda:invoke without FunctionName", `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"Payload.$":"$"},"End":true}}}`, "SCHEMA_VALIDATION_FAILED", "Lambda Invoke requires FunctionName; refusing it at create is understudy's choice (ASL-01)"},
		{"Resource that is no ARN", `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"my-function","End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 3.5"},
		{"template key twice after renaming", `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"a":1,"a.$":"$.b"},"End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 4.3"},
		{"'.$' value that is no path or call", `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"a.$":"b"},"End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 3.5"},
		{"variable in ResultPath", `{"StartAt":"P","States":{"P":{"Type":"Pass","ResultPath":"$x","End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 4.4: never a variable"},
		{"Wait with none of its fields", `{"StartAt":"W","States":{"W":{"Type":"Wait","End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 3.3"},
		{"Wait Seconds over the maximum", `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":100000000,"End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 3.7"},
		{"top-level TimeoutSeconds zero", `{"StartAt":"P","TimeoutSeconds":0,"States":{` + terminalP + `}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 3.1: positive integer"},
		{"Map without a processor", `{"StartAt":"M","States":{"M":{"Type":"Map","End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 3.3"},
		{"Map with ItemSelector and Parameters", `{"StartAt":"M","States":{"M":{"Type":"Map","ItemSelector":{},"Parameters":{},"Iterator":{"StartAt":"I","States":{"I":{"Type":"Pass","End":true}}},"End":true}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 3.3"},
		{"Succeed with Next", `{"StartAt":"S","States":{"S":{"Type":"Succeed","Next":"S"}}}`, "SCHEMA_VALIDATION_FAILED", "stepfunctions-asl.md 3.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, diags := asl.Parse(tt.def, allFeatures)
			first, ok := firstError(diags)
			if d != nil || !ok {
				t.Fatalf("Parse accepted it (%s): %+v", tt.source, diags)
			}
			if first.Code != tt.code {
				t.Errorf("first error = %+v, want code %s (%s)", first, tt.code, tt.source)
			}
		})
	}
}

// Refusals the blind refusal list does not reach: variables in a Choice
// Variable and in an intrinsic argument, Distributed Map's ExecutionType,
// and a JSONPath function inside an intrinsic argument (spec "Definition
// grammar").
func TestParseRefusesMoreUnsupportedConstructs(t *testing.T) {
	tests := []struct{ name, def, location string }{
		{"variable in a Choice", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$v","IsNull":true,"Next":"P"}]},` + terminalP + `}}`, "/States/C/Choices[0]/Variable"},
		{"variable in an intrinsic", `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"x.$":"States.Format('{}', $v)"},"End":true}}}`, "/States/P/Parameters"},
		{"ExecutionType", `{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"ProcessorConfig":{"ExecutionType":"STANDARD"},"StartAt":"I","States":{"I":{"Type":"Pass","End":true}}},"End":true}}}`, "/States/M/ItemProcessor/ProcessorConfig/ExecutionType"},
		{"JSONPath function in an intrinsic", `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"x.$":"States.Array($.a.length())"},"End":true}}}`, "/States/P/Parameters"},
		{"Catch Assign", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Catch":[{"ErrorEquals":["States.ALL"],"Assign":{},"Next":"P"}],"Next":"P"},` + terminalP + `}}`, "/States/T/Catch[0]/Assign"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, diags := asl.Parse(tt.def, allFeatures)
			first, ok := firstError(diags)
			if d != nil || !ok || first.Code != asl.CodeUnsupported || first.Location != tt.location {
				t.Fatalf("Parse = %v, %+v; want a leading %s at %s", d != nil, diags, asl.CodeUnsupported, tt.location)
			}
			if !strings.HasPrefix(first.Message, unsupportedPrefix) || !strings.HasSuffix(first.Message, unsupportedSuffix) {
				t.Errorf("message = %q (stepfunctions-asl.md 10.4)", first.Message)
			}
		})
	}
}

// A state whose type is refused reports one refusal, at the state, and
// still AWS's own verdicts on its fields; a refused field inside it is not
// reported again. Which problems a definition lists is understudy's choice.
func TestASL01RefusedStateKeepsAWSVerdicts(t *testing.T) {
	// inferred, ASL-01
	def := `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":-1,"Assign":{},"End":true}}}`
	_, diags := asl.Parse(def, asl.NewFeatures(asl.FeatPass))
	if len(diags) != 2 {
		t.Fatalf("diagnostics = %+v, want the refused type and the bad Seconds", diags)
	}
	if diags[0].Code != asl.CodeUnsupported || diags[0].Location != "/States/W" {
		t.Errorf("first = %+v, want %s at /States/W", diags[0], asl.CodeUnsupported)
	}
	if diags[1].Code != "SCHEMA_VALIDATION_FAILED" || diags[1].Location != "/States/W/Seconds" {
		t.Errorf("second = %+v, want SCHEMA_VALIDATION_FAILED at /States/W/Seconds", diags[1])
	}
}

// Nested states are located through their branch or processor, a branch
// by its index in brackets and an unknown field at its state (ASL-01,
// recorded 2026-11-03); the processor form is inferred from it.
func TestASL01NestedLocations(t *testing.T) {
	// inferred, ASL-01
	tests := []struct{ def, location string }{
		{`{"StartAt":"X","States":{"X":{"Type":"Parallel","Branches":[{"StartAt":"A","States":{"A":{"Type":"Pass","End":true}}},{"StartAt":"B","States":{"B":{"Type":"Pass","Bad":1,"End":true}}}],"End":true}}}`, "/States/X/Branches[1]/States/B"},
		{`{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"StartAt":"I","States":{"I":{"Type":"Pass","Bad":1,"End":true}}},"End":true}}}`, "/States/M/ItemProcessor/States/I"},
		{`{"StartAt":"M","States":{"M":{"Type":"Map","Iterator":{"StartAt":"I","States":{"I":{"Type":"Pass","Bad":1,"End":true}}},"End":true}}}`, "/States/M/Iterator/States/I"},
	}
	for _, tt := range tests {
		_, diags := asl.Parse(tt.def, allFeatures)
		first, ok := firstError(diags)
		if !ok || first.Location != tt.location {
			t.Errorf("Parse(%s) first error = %+v, want one at %s", tt.def, first, tt.location)
		}
	}
}

// Malformed JSON past the first token keeps AWS's code with understudy's
// wording; an unrecognized token on a later line is located as Jackson
// locates it (stepfunctions-asl.md 3.6, recorded for line 1).
func TestASL01InvalidJSON(t *testing.T) {
	// inferred, ASL-01
	_, diags := asl.Parse(`{"StartAt": "P",`, allFeatures)
	if len(diags) != 1 || diags[0].Code != "INVALID_JSON_DESCRIPTION" || diags[0].Location != "" {
		t.Fatalf("truncated definition = %+v, want one INVALID_JSON_DESCRIPTION with no location", diags)
	}
	_, diags = asl.Parse("\n  oops", allFeatures)
	want := "[Source: (String)\"\n  oops\"; line: 2, column: 7]"
	if len(diags) != 1 || diags[0].Code != "INVALID_JSON_DESCRIPTION" || diags[0].Location != want ||
		diags[0].Message != "Unrecognized token 'oops': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')\n at "+want {
		t.Fatalf("unrecognized token on line 2 = %+v, want location %q", diags, want)
	}
}
