package asl_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// firstError is the first ERROR diagnostic: the one CreateStateMachine
// renders.
func firstError(diags []asl.Diagnostic) (asl.Diagnostic, bool) {
	for _, d := range diags {
		if d.Severity == asl.SeverityError {
			return d, true
		}
	}
	return asl.Diagnostic{}, false
}

// wantRefused fails unless def is refused with exactly one leading ERROR
// diagnostic matching code, message and location.
func wantRefused(t *testing.T, def string, accept asl.Features, code, message, location string) asl.Diagnostic {
	t.Helper()
	d, diags := asl.Parse(def, accept)
	first, ok := firstError(diags)
	if d != nil || !ok {
		t.Fatalf("Parse accepted\n%s\ndiagnostics %+v; want %s", def, diags, code)
	}
	if first.Code != code || first.Message != message || first.Location != location {
		t.Fatalf("first error = %+v\nwant {%s %q %q}", first, code, message, location)
	}
	return first
}

// The recorded ValidateStateMachineDefinition diagnostics
// (stepfunctions-asl.md 3.6; stepfunctions-api.md 5.6; aws-recorded
// localstack@8b9a79f v2/test_sfn_api_validation.snapshot.json, 2025-10-19,
// and v2/test_sfn_api.snapshot.json:L27).
func TestParseRecordedDiagnostics(t *testing.T) {
	tests := []struct {
		name, def, code, message, location, source string
	}{
		{"blank", " ", "SCHEMA_VALIDATION_FAILED", "Definition must be a valid JSON object", "", "test_sfn_api_validation.snapshot.json:L2"},
		{
			"not JSON", "not a definition", "INVALID_JSON_DESCRIPTION",
			"Unrecognized token 'not': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')\n at [Source: (String)\"not a definition\"; line: 1, column: 4]",
			"[Source: (String)\"not a definition\"; line: 1, column: 4]", "test_sfn_api_validation.snapshot.json:L22",
		},
		{"empty object", "{}", "SCHEMA_VALIDATION_FAILED", "These fields are required: [States, StartAt]", "/", "test_sfn_api_validation.snapshot.json:L43"},
		{"no StartAt", `{"States":{"P":{"Type":"Pass","End":true}}}`, "SCHEMA_VALIDATION_FAILED", "These fields are required: [StartAt]", "/", "test_sfn_api_validation.snapshot.json:L78"},
		{"no States", `{"StartAt":"P"}`, "SCHEMA_VALIDATION_FAILED", "These fields are required: [States]", "/", "test_sfn_api.snapshot.json:L27"},
		{
			"unknown Type", `{"StartAt":"ExistingButInvalidState","States":{"ExistingButInvalidState":{"Type":"Nope","End":true}}}`,
			"SCHEMA_VALIDATION_FAILED", "The field 'Type' should have one of these values: [Task, Wait, Pass, Succeed, Fail, Choice, Parallel, Map]",
			"/States/ExistingButInvalidState/Type", "localstack@8b9a79f v2/test_state/test_test_state_machine_scenarios.snapshot.json:L355",
		},
		{
			"empty Branches", `{"StartAt":"StateName","States":{"StateName":{"Type":"Parallel","Branches":[],"End":true}}}`,
			"SCHEMA_VALIDATION_FAILED", "Value cannot be empty", "/States/StateName/Branches",
			"localstack@8b9a79f v2/test_state/test_state_mock_validation.snapshot.json:L248",
		},
		{
			"backslash quote in an intrinsic", `{"StartAt":"IntrinsicEscape","States":{"IntrinsicEscape":{"Type":"Pass","Parameters":{"parsed.$":"States.Format('\\\"')"},"End":true}}}`,
			"SCHEMA_VALIDATION_FAILED", "The value for the field 'parsed.$' must be a valid JSONPath or a valid intrinsic function call",
			"/States/IntrinsicEscape/Parameters", "localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L27838, L27847",
		},
		{
			"unknown dynamodb action", `{"StartAt":"Query","States":{"Query":{"Type":"Task","Resource":"arn:aws:states:::dynamodb:query","End":true}}}`,
			"SCHEMA_VALIDATION_FAILED", "The resource provided arn:aws:states:::dynamodb:query is not recognized. The value is not a valid resource ARN, or the resource is not available in this region.",
			"/States/Query/Resource", "localstack@8b9a79f v2/services/test_dynamodb_task_service.snapshot.json:L2169",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := wantRefused(t, tt.def, allFeatures, tt.code, tt.message, tt.location)
			if first.Severity.String() != "ERROR" {
				t.Errorf("severity = %q, want ERROR (%s)", first.Severity.String(), tt.source)
			}
		})
	}
}

// A literal Timestamp must be RFC 3339 with T and Z or an offset
// (aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L26194-L26239).
func TestParseRefusesBadLiteralTimestamps(t *testing.T) {
	for _, ts := range []string{
		"2016-12-05 21:29:29Z", "2016-12-05T21:29:29", "2016-13-05T21:29:29Z", "2016-12-05T25:29:29Z", "05-12-2016T21:29:29Z",
	} {
		t.Run(ts, func(t *testing.T) {
			def := `{"StartAt":"WaitUntil","States":{"WaitUntil":{"Type":"Wait","Timestamp":"` + ts + `","End":true}}}`
			wantRefused(t, def, allFeatures, "SCHEMA_VALIDATION_FAILED", "String does not match RFC3339 timestamp", "/States/WaitUntil/Timestamp")
		})
	}
	ok := `{"StartAt":"W","States":{"W":{"Type":"Wait","Timestamp":"2016-03-14T01:59:00Z","End":true}}}`
	if d, diags := asl.Parse(ok, allFeatures); d == nil {
		t.Errorf("Parse refused a valid Timestamp: %+v", diags)
	}
}

// A message ending in a space keeps it before " at" (aws-recorded
// localstack@8b9a79f v2/test_sfn_api_express.snapshot.json:L171,
// stepfunctions-api.md 5.1); the envelope is stepfunctions-asl.md 3.6.
func TestInvalidDefinitionEnvelope(t *testing.T) {
	tests := []struct {
		d    asl.Diagnostic
		want string
	}{
		{
			asl.Diagnostic{Severity: asl.SeverityError, Code: "SCHEMA_VALIDATION_FAILED", Message: "These fields are required: [States]", Location: "/"},
			"Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: These fields are required: [States] at /'",
		},
		{
			asl.Diagnostic{Severity: asl.SeverityError, Code: "SCHEMA_VALIDATION_FAILED", Message: "Express state machine does not support '.waitForTaskToken' service integration ", Location: "/States/Publish/Resource"},
			"Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: Express state machine does not support '.waitForTaskToken' service integration  at /States/Publish/Resource'",
		},
		{
			asl.Diagnostic{Severity: asl.SeverityError, Code: "SCHEMA_VALIDATION_FAILED", Message: "The resource provided arn:aws:states:::dynamodb:query is not recognized. The value is not a valid resource ARN, or the resource is not available in this region.", Location: "/States/Query/Resource"},
			"Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: The resource provided arn:aws:states:::dynamodb:query is not recognized. The value is not a valid resource ARN, or the resource is not available in this region. at /States/Query/Resource'",
		},
	}
	for _, tt := range tests {
		if got := asl.InvalidDefinition([]asl.Diagnostic{tt.d}); got != tt.want {
			t.Errorf("InvalidDefinition() = %q\nwant %q", got, tt.want)
		}
	}
	if asl.SeverityError.String() != "ERROR" || asl.SeverityWarning.String() != "WARNING" {
		t.Errorf("severities = %q, %q; want ERROR, WARNING ([S] com.amazonaws.sfn#ValidateStateMachineDefinitionSeverity)", asl.SeverityError.String(), asl.SeverityWarning.String())
	}
}

const (
	unsupportedPrefix = "understudy does not implement "
	unsupportedSuffix = "Run 'understudy coverage --service sfn' for what is supported."
)

const (
	lambdaTask = `"Type":"Task","Resource":"` + functionArn + `"`
	// terminalP is a Pass state named P that ends its scope.
	terminalP = `"P":{"Type":"Pass","End":true}`
)

// Every construct understudy refuses is an ERROR diagnostic with code
// UNSUPPORTED_BY_UNDERSTUDY at the field's location, with every feature
// accepted: nothing is accepted and ignored (stepfunctions-asl.md 10.4
// message form). Where the nesting
// form of a location is understudy's (ASL-01), only the state's prefix is
// asserted.
func TestParseRefusalList(t *testing.T) {
	pass := func(extra string) string {
		return `{"StartAt":"P","States":{"P":{"Type":"Pass",` + extra + `"End":true}}}`
	}
	task := func(fields string) string {
		return `{"StartAt":"T","States":{"T":{` + fields + `,"End":true}}}`
	}
	resource := func(arn string) string {
		return task(`"Type":"Task","Resource":"` + arn + `"`)
	}
	mapState := func(extra string) string {
		return `{"StartAt":"M","States":{"M":{"Type":"Map",` + extra + `"ItemProcessor":{"StartAt":"I","States":{"I":{"Type":"Pass","End":true}}},"End":true}}}`
	}
	tests := []struct {
		name, def, location string
		exact               bool
	}{
		{"JSONata machine", `{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`, "/", true},
		{"JSONata state", pass(`"QueryLanguage":"JSONata",`), "/States/P/QueryLanguage", true},
		{"Arguments", task(lambdaTask + `,"Arguments":{}`), "/States/T/Arguments", true},
		{"Output", pass(`"Output":{},`), "/States/P/Output", true},
		{"Condition", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Condition":true,"Next":"P"}]},"P":{"Type":"Pass","End":true}}}`, "/States/C", false},
		{"Items", mapState(`"Items":[1],`), "/States/M/Items", true},
		{"Assign", pass(`"Assign":{"x":1},`), "/States/P/Assign", true},
		{"variable reference", pass(`"Parameters":{"x.$":"$myVar"},`), "/States/P", false},
		{"distributed Map", `{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"STANDARD"},"StartAt":"I","States":{"I":{"Type":"Pass","End":true}}},"End":true}}}`, "/States/M", false},
		{"ItemReader", mapState(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","Parameters":{}},`), "/States/M/ItemReader", true},
		{"ItemBatcher", mapState(`"ItemBatcher":{"MaxItemsPerBatch":2},`), "/States/M/ItemBatcher", true},
		{"ResultWriter", mapState(`"ResultWriter":{"Resource":"arn:aws:states:::s3:putObject","Parameters":{}},`), "/States/M/ResultWriter", true},
		{"ToleratedFailurePercentage", mapState(`"ToleratedFailurePercentage":5,`), "/States/M/ToleratedFailurePercentage", true},
		{"ToleratedFailurePercentagePath", mapState(`"ToleratedFailurePercentagePath":"$.p",`), "/States/M/ToleratedFailurePercentagePath", true},
		{"ToleratedFailureCount", mapState(`"ToleratedFailureCount":5,`), "/States/M/ToleratedFailureCount", true},
		{"ToleratedFailureCountPath", mapState(`"ToleratedFailureCountPath":"$.c",`), "/States/M/ToleratedFailureCountPath", true},
		{"Label", mapState(`"Label":"lbl",`), "/States/M/Label", true},
		{"waitForTaskToken", resource("arn:aws:states:::lambda:invoke.waitForTaskToken"), "/States/T/Resource", true},
		{"sync", resource("arn:aws:states:::states:startExecution.sync"), "/States/T/Resource", true},
		{"sync:2", resource("arn:aws:states:::states:startExecution.sync:2"), "/States/T/Resource", true},
		{"activity", resource("arn:aws:states:us-east-1:000000000000:activity:act"), "/States/T/Resource", true},
		{"aws-sdk", resource("arn:aws:states:::aws-sdk:s3:listBuckets"), "/States/T/Resource", true},
		{"http", resource("arn:aws:states:::http:invoke"), "/States/T/Resource", true},
		{"sqs", resource("arn:aws:states:::sqs:sendMessage"), "/States/T/Resource", true},
		{"sns", resource("arn:aws:states:::sns:publish"), "/States/T/Resource", true},
		{"events", resource("arn:aws:states:::events:putEvents"), "/States/T/Resource", true},
		{"states", resource("arn:aws:states:::states:startExecution"), "/States/T/Resource", true},
		{"dynamodb getItem", resource("arn:aws:states:::dynamodb:getItem"), "/States/T/Resource", true},
		{"dynamodb putItem", resource("arn:aws:states:::dynamodb:putItem"), "/States/T/Resource", true},
		{"dynamodb updateItem", resource("arn:aws:states:::dynamodb:updateItem"), "/States/T/Resource", true},
		{"dynamodb deleteItem", resource("arn:aws:states:::dynamodb:deleteItem"), "/States/T/Resource", true},
		{"HeartbeatSeconds", task(lambdaTask + `,"HeartbeatSeconds":5`), "/States/T/HeartbeatSeconds", true},
		{"HeartbeatSecondsPath", task(lambdaTask + `,"HeartbeatSecondsPath":"$.h"`), "/States/T/HeartbeatSecondsPath", true},
		{"Credentials", task(lambdaTask + `,"Credentials":{"RoleArn":"` + roleArn + `"}`), "/States/T/Credentials", true},
		{"InvocationType Event", task(`"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":"` + functionArn + `","InvocationType":"Event"}`), "/States/T", false},
		{"InvocationType DryRun", task(`"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":"` + functionArn + `","InvocationType":"DryRun"}`), "/States/T", false},
		{"JSONPath function", pass(`"InputPath":"$.a.length()",`), "/States/P", false},
		{"multi-clause filter", pass(`"InputPath":"$.a[?(@.b == 1 && @.c == 2)]",`), "/States/P", false},
		{"filter script", pass(`"Parameters":{"x.$":"$.a[(@.length-1)]"},`), "/States/P", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, diags := asl.Parse(tt.def, allFeatures)
			first, ok := firstError(diags)
			if d != nil || !ok || first.Code != asl.CodeUnsupported {
				t.Fatalf("Parse(%s) = %v, %+v; want a leading %s error", tt.def, d != nil, diags, asl.CodeUnsupported)
			}
			if tt.exact && first.Location != tt.location || !tt.exact && !strings.HasPrefix(first.Location, tt.location) {
				t.Errorf("location = %q, want %q (exact %v)", first.Location, tt.location, tt.exact)
			}
			if !strings.HasPrefix(first.Message, unsupportedPrefix) || !strings.HasSuffix(first.Message, unsupportedSuffix) {
				t.Errorf("message = %q, want %q ... %q (stepfunctions-asl.md 10.4)", first.Message, unsupportedPrefix, unsupportedSuffix)
			}
			if got := asl.InvalidDefinition(diags); !strings.HasPrefix(got, "Invalid State Machine Definition: '"+asl.CodeUnsupported+": ") {
				t.Errorf("InvalidDefinition = %q", got)
			}
		})
	}
}

// A literal lambda:invoke InvocationType RequestResponse, and one given by
// a path, are accepted with FeatLambdaTask.
func TestParseAcceptsRequestResponseInvocation(t *testing.T) {
	for _, params := range []string{
		`{"FunctionName":"` + functionArn + `","InvocationType":"RequestResponse"}`,
		`{"FunctionName":"` + functionArn + `","InvocationType.$":"$.type"}`,
		`{"FunctionName":"` + functionArn + `:live","Payload.$":"$"}`,
	} {
		def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":` + params + `,"End":true}}}`
		if d, diags := asl.Parse(def, allFeatures); d == nil {
			t.Errorf("Parse refused %s: %+v", params, diags)
		}
	}
	qualified := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"` + functionArn + `:live","End":true}}}`
	if d, diags := asl.Parse(qualified, allFeatures); d == nil {
		t.Errorf("Parse refused a qualified function ARN: %+v", diags)
	}
}

// Each feature's constructs are accepted with the feature and refused with
// UNSUPPORTED_BY_UNDERSTUDY without it, so a construct is accepted only
// once its runtime has landed.
func TestFeatureGating(t *testing.T) {
	tests := []struct {
		feature asl.Feature
		need    []asl.Feature
		def     string
	}{
		{asl.FeatPass, nil, `{"StartAt":"P","States":{` + terminalP + `}}`},
		{asl.FeatSucceed, nil, `{"StartAt":"S","States":{"S":{"Type":"Succeed"}}}`},
		{asl.FeatFail, nil, `{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"E"}}}`},
		{asl.FeatChoice, []asl.Feature{asl.FeatPass}, `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.v","NumericEquals":1,"Next":"P"}],"Default":"P"},` + terminalP + `}}`},
		{asl.FeatWait, nil, `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":1,"End":true}}}`},
		{asl.FeatTimeout, []asl.Feature{asl.FeatPass}, `{"StartAt":"P","TimeoutSeconds":10,"States":{` + terminalP + `}}`},
		{asl.FeatPayloadTemplates, []asl.Feature{asl.FeatPass}, `{"StartAt":"P","States":{"P":{"Type":"Pass","InputPath":"$.a","End":true}}}`},
		{asl.FeatPathDialect, []asl.Feature{asl.FeatPass, asl.FeatPayloadTemplates}, `{"StartAt":"P","States":{"P":{"Type":"Pass","InputPath":"$.a[*]","End":true}}}`},
		{asl.FeatIntrinsics, []asl.Feature{asl.FeatPass, asl.FeatPayloadTemplates}, `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"x.$":"States.UUID()"},"End":true}}}`},
		{asl.FeatLambdaTask, nil, `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"End":true}}}`},
		{asl.FeatRetryCatch, []asl.Feature{asl.FeatLambdaTask}, `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Retry":[{"ErrorEquals":["States.ALL"]}],"End":true}}}`},
		{asl.FeatParallel, []asl.Feature{asl.FeatPass}, `{"StartAt":"X","States":{"X":{"Type":"Parallel","Branches":[{"StartAt":"P","States":{` + terminalP + `}}],"End":true}}}`},
		{asl.FeatMap, []asl.Feature{asl.FeatPass}, `{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"StartAt":"P","States":{` + terminalP + `}},"End":true}}}`},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.feature), func(t *testing.T) {
			with := asl.NewFeatures(append([]asl.Feature{tt.feature}, tt.need...)...)
			if d, diags := asl.Parse(tt.def, with); d == nil || len(diags) != 0 {
				t.Fatalf("with feature %v: Parse = %v, %+v; want accepted with no diagnostics", tt.feature, d != nil, diags)
			}
			without := asl.NewFeatures(tt.need...)
			d, diags := asl.Parse(tt.def, without)
			first, ok := firstError(diags)
			if d != nil || !ok || first.Code != asl.CodeUnsupported {
				t.Fatalf("without feature %v: Parse = %v, %+v; want %s", tt.feature, d != nil, diags, asl.CodeUnsupported)
			}
		})
	}
}

// Top-level Comment, Version "1.0" and QueryLanguage "JSONPath" are always
// accepted (stepfunctions-asl.md 3.1; aws-recorded localstack@8b9a79f
// v2/base/test_base.snapshot.json:L462 for Version).
func TestParseAcceptsTopLevelFields(t *testing.T) {
	def := `{"Comment":"c","Version":"1.0","QueryLanguage":"JSONPath","StartAt":"P","States":{"P":{"Type":"Pass","Comment":"x","QueryLanguage":"JSONPath","End":true}}}`
	if d, diags := asl.Parse(def, asl.NewFeatures(asl.FeatPass)); d == nil || len(diags) != 0 {
		t.Fatalf("Parse = %v, %+v; want accepted", d != nil, diags)
	}
}

// Structural and value rules: each definition is refused with an ERROR.
// Codes are asserted where AWS documents the code for the case
// (api/API_ValidateStateMachineDefinitionDiagnostic); messages and locations
// are understudy's wording (ASL-01), so only the severity is asserted
// elsewhere.
func TestParseRefusesInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name, def, code, source string
	}{
		{"Next and End", `{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"Q","End":true},"Q":{"Type":"Pass","End":true}}}`, "", "aws-doc dg/statemachine-structure: only one of Next or End"},
		{"missing Next target", `{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"Nope"}}}`, "MISSING_TRANSITION_TARGET", "stepfunctions-asl.md 3.2"},
		{"StartAt names no state", `{"StartAt":"Nope","States":{` + terminalP + `}}`, "", "spec#toplevelfields"},
		{"Choice with Next", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.v","NumericEquals":1,"Next":"P"}],"Next":"P"},` + terminalP + `}}`, "", "statelint j2119:L56-L57"},
		{"Choice with no Choices", `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[],"Default":"P"},` + terminalP + `}}`, "", "aws-doc dg/state-choice: Choices non-empty"},
		{"Task without Resource", `{"StartAt":"T","States":{"T":{"Type":"Task","End":true}}}`, "", "stepfunctions-asl.md 3.3"},
		{"Wait with two fields", `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":1,"SecondsPath":"$.s","End":true}}}`, "", "spec#jsonpath-wait-state"},
		{"Wait negative Seconds", `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":-1,"End":true}}}`, "", "aws-doc dg/state-wait: 0..99,999,999"},
		{"Map with ItemProcessor and Iterator", `{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"StartAt":"P","States":{` + terminalP + `}},"Iterator":{"StartAt":"P","States":{` + terminalP + `}},"End":true}}}`, "", "statelint j2119:L173"},
		{"Map with MaxConcurrency and its Path", `{"StartAt":"M","States":{"M":{"Type":"Map","MaxConcurrency":1,"MaxConcurrencyPath":"$.m","ItemProcessor":{"StartAt":"P","States":{` + terminalP + `}},"End":true}}}`, "", "aws-doc dg/state-map-inline"},
		{"Task TimeoutSeconds and its Path", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"TimeoutSeconds":5,"TimeoutSecondsPath":"$.t","End":true}}}`, "", "aws-doc dg/state-task"},
		{"BackoffRate below 1", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Retry":[{"ErrorEquals":["States.ALL"],"BackoffRate":0.5}],"End":true}}}`, "", "stepfunctions-asl.md 3.7"},
		{"MaxDelaySeconds over limit", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Retry":[{"ErrorEquals":["States.ALL"],"MaxDelaySeconds":31622401}],"End":true}}}`, "", "stepfunctions-asl.md 3.7"},
		{"MaxDelaySeconds zero", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Retry":[{"ErrorEquals":["States.ALL"],"MaxDelaySeconds":0}],"End":true}}}`, "", "stepfunctions-asl.md 3.7"},
		{"unknown JitterStrategy", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Retry":[{"ErrorEquals":["States.ALL"],"JitterStrategy":"HALF"}],"End":true}}}`, "", "stepfunctions-asl.md 3.7"},
		{"negative MaxAttempts", `{"StartAt":"T","States":{"T":{` + lambdaTask + `,"Retry":[{"ErrorEquals":["States.ALL"],"MaxAttempts":-1}],"End":true}}}`, "", "stepfunctions-asl.md 3.7"},
		{"Retry on Pass", `{"StartAt":"P","States":{"P":{"Type":"Pass","Retry":[{"ErrorEquals":["States.ALL"]}],"End":true}}}`, "", "stepfunctions-asl.md 3.3"},
		{"ResultSelector on Pass", `{"StartAt":"P","States":{"P":{"Type":"Pass","ResultSelector":{},"End":true}}}`, "", "stepfunctions-asl.md 3.3"},
		{"InputPath on Fail", `{"StartAt":"F","States":{"F":{"Type":"Fail","InputPath":"$"}}}`, "", "stepfunctions-asl.md 3.3"},
		{"ResultPath into the context", `{"StartAt":"P","States":{"P":{"Type":"Pass","ResultPath":"$$.x","End":true}}}`, "", "stepfunctions-asl.md 4.4"},
		{"state name over 80 characters", `{"StartAt":"` + strings.Repeat("n", 81) + `","States":{"` + strings.Repeat("n", 81) + `":{"Type":"Pass","End":true}}}`, "", "spec#states-fields"},
		{"duplicate state name across scopes", `{"StartAt":"X","States":{"X":{"Type":"Parallel","Branches":[{"StartAt":"X","States":{"X":{"Type":"Pass","End":true}}}],"End":true}}}`, "DUPLICATE_STATE_NAME", "stepfunctions-asl.md 3.2"},
		{"intrinsic nested 11 deep", `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"x.$":"` + strings.Repeat("States.Array(", 11) + "1" + strings.Repeat(")", 11) + `"},"End":true}}}`, "", "stepfunctions-asl.md 3.7"},
		{"Reference Path required for ResultPath", `{"StartAt":"P","States":{"P":{"Type":"Pass","ResultPath":"$.a[*]","End":true}}}`, "", "stepfunctions-asl.md 4.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, diags := asl.Parse(tt.def, allFeatures)
			first, ok := firstError(diags)
			if d != nil || !ok {
				t.Fatalf("Parse accepted it (%s): %+v", tt.source, diags)
			}
			if first.Code == asl.CodeUnsupported {
				t.Errorf("first error = %+v, want AWS's verdict, not a refusal (%s)", first, tt.source)
			}
			if tt.code != "" && first.Code != tt.code {
				t.Errorf("code = %q, want %q (%s)", first.Code, tt.code, tt.source)
			}
		})
	}
}
