package sfn_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
)

// A state machine created with no optional member describes with the
// recorded defaults: logging {includeExecutionData false, level OFF},
// tracing {enabled false}, status ACTIVE, encryption AWS_OWNED_KEY and no
// revisionId (stepfunctions-api.md 5.2; aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L620-L647 and
// v2/test_sfn_api_variable_references.snapshot.json:L31-L33). The
// definition comes back as sent, which Terraform's empty second plan needs
// (stepfunctions-asl.md 2 finding 7, 13).
func TestCreateAndDescribeDefaults(t *testing.T) {
	run(t, func(h *harness) {
		created := must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "m", Definition: resultMachine, RoleArn: h.roleArn()})
		if created.StateMachineArn != h.machineArn("m") || !created.CreationDate.Equal(h.now()) || created.StateMachineVersionArn != nil {
			h.Errorf("CreateStateMachine = %+v, want the ARN of stepfunctions-api.md 4.1 created now, no version ARN", created)
		}
		d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: created.StateMachineArn})
		if d.StateMachineArn != created.StateMachineArn || d.Name != "m" || d.Definition != resultMachine || d.RoleArn != h.roleArn() ||
			d.Type != sfnapi.StateMachineTypeStandard || !d.CreationDate.Equal(created.CreationDate) {
			h.Errorf("DescribeStateMachine = %+v", d)
		}
		if d.Status == nil || *d.Status != sfnapi.StateMachineStatusActive {
			h.Errorf("status = %v, want ACTIVE", d.Status)
		}
		if l := d.LoggingConfiguration; l == nil || l.Level == nil || *l.Level != sfnapi.LogLevelOff || l.IncludeExecutionData || len(l.Destinations) != 0 {
			h.Errorf("loggingConfiguration = %+v, want {includeExecutionData: false, level: OFF}", l)
		}
		if tr := d.TracingConfiguration; tr == nil || tr.Enabled {
			h.Errorf("tracingConfiguration = %+v, want {enabled: false}", tr)
		}
		if e := d.EncryptionConfiguration; e == nil || e.Type != sfnapi.EncryptionTypeAwsOwnedKey {
			h.Errorf("encryptionConfiguration = %+v, want {type: AWS_OWNED_KEY}", e)
		}
		if d.RevisionId != nil {
			h.Errorf("revisionId = %q before any update, want none", *d.RevisionId)
		}
	})
}

// An exact duplicate CreateStateMachine answers the same ARN; a changed
// definition, even only re-indented, answers StateMachineAlreadyExists; a
// different roleArn alone is ignored and not applied (stepfunctions-api.md
// 5.1; aws-recorded localstack@8b9a79f v2/test_sfn_api.snapshot.json:L48-L275,
// L211; aws-doc api/API_CreateStateMachine).
func TestCreateIdempotency(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.create("m", resultMachine)
		if again := h.create("m", resultMachine); again != arn {
			h.Errorf("duplicate create = %s, want %s", again, arn)
		}
		other := must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "m", Definition: resultMachine, RoleArn: fmt.Sprintf("arn:aws:iam::%s:role/other", h.Scope.Account)})
		if other.StateMachineArn != arn {
			h.Errorf("create with another role = %s, want the idempotent %s", other.StateMachineArn, arn)
		}
		if d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: arn}); d.RoleArn != h.roleArn() {
			h.Errorf("roleArn = %s after an idempotent create with another role, want it unchanged", d.RoleArn)
		}
		reindented := strings.Replace(resultMachine, `"States":`, "\n  \"States\": ", 1)
		_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "m", Definition: reindented, RoleArn: h.roleArn()})
		wantError(h, err, "StateMachineAlreadyExists", 400, "State Machine Already Exists: '"+arn+"'")
	})
}

// The recorded not-found and bad-ARN texts (stepfunctions-api.md 3.2;
// aws-recorded localstack@8b9a79f v2/test_sfn_api.snapshot.json:L392, L1515,
// L1588, L1519-L1526).
func TestNotFoundAndInvalidArnTexts(t *testing.T) {
	run(t, func(h *harness) {
		missing := h.machineArn("nope")
		wantError(h, errOf(call(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: missing})),
			"StateMachineDoesNotExist", 400, "State Machine Does Not Exist: '"+missing+"'")
		wantError(h, errOf(call(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{StateMachineArn: new(missing)})),
			"StateMachineDoesNotExist", 400, "State Machine Does Not Exist: '"+missing+"'")
		wantError(h, errOf(call(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: missing})),
			"StateMachineDoesNotExist", 400, "State Machine Does Not Exist: '"+missing+"'")
		exec := h.executionArn("nope", "x")
		wantError(h, errOf(call(h, sfnapi.OpDescribeExecution, &sfnapi.DescribeExecutionInput{ExecutionArn: exec})),
			"ExecutionDoesNotExist", 400, "Execution Does Not Exist: '"+exec+"'")
		wantError(h, errOf(call(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: "not-an-arn"})),
			"InvalidArn", 400, "Invalid Arn: 'Invalid ARN prefix: not-an-arn'")
		wantError(h, errOf(call(h, sfnapi.OpDescribeExecution, &sfnapi.DescribeExecutionInput{ExecutionArn: "not-an-arn"})),
			"InvalidArn", 400, "Invalid Arn: 'Invalid ARN prefix: not-an-arn'")
	})
}

// Deleting a state machine that does not exist answers 200; deleting an
// idle one removes it (stepfunctions-api.md 5.4; aws-recorded
// localstack@8b9a79f v2/test_sfn_api.snapshot.json:L37-L46).
func TestDeleteStateMachine(t *testing.T) {
	run(t, func(h *harness) {
		must(h, sfnapi.OpDeleteStateMachine, &sfnapi.DeleteStateMachineInput{StateMachineArn: h.machineArn("nope")})
		arn := h.create("m", resultMachine)
		must(h, sfnapi.OpDeleteStateMachine, &sfnapi.DeleteStateMachineInput{StateMachineArn: arn})
		h.settled()
		wantError(h, errOf(call(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: arn})),
			"StateMachineDoesNotExist", 400, "State Machine Does Not Exist: '"+arn+"'")
		if again := h.create("m", waitMachine); again != arn {
			h.Errorf("re-created machine ARN = %s, want %s", again, arn)
		}
	})
}

// ListStateMachines orders by name as a string, not by creation
// (stepfunctions-api.md 5.5; aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L1800-L1830), and pages with maxResults.
func TestListStateMachinesOrderAndPaging(t *testing.T) {
	run(t, func(h *harness) {
		if got := must(h, sfnapi.OpListStateMachines, &sfnapi.ListStateMachinesInput{}).StateMachines; len(got) != 0 {
			h.Fatalf("empty account lists %v", got)
		}
		for _, n := range []string{"statemachine_2_x", "statemachine_0_x", "statemachine_12_x", "statemachine_1_x", "statemachine_10_x", "statemachine_11_x"} {
			h.create(n, resultMachine)
		}
		want := []string{"statemachine_0_x", "statemachine_10_x", "statemachine_11_x", "statemachine_12_x", "statemachine_1_x", "statemachine_2_x"}
		var got []string
		var token *string
		for range 10 {
			out := must(h, sfnapi.OpListStateMachines, &sfnapi.ListStateMachinesInput{MaxResults: 4, NextToken: token})
			if len(out.StateMachines) > 4 {
				h.Fatalf("page of %d with maxResults 4", len(out.StateMachines))
			}
			for _, sm := range out.StateMachines {
				got = append(got, sm.Name)
				if sm.StateMachineArn != h.machineArn(sm.Name) || sm.Type != sfnapi.StateMachineTypeStandard {
					h.Errorf("item %+v", sm)
				}
			}
			if token = out.NextToken; token == nil {
				break
			}
		}
		if !slices.Equal(got, want) {
			h.Errorf("names = %v, want %v", got, want)
		}
		wantError(h, errOf(call(h, sfnapi.OpListStateMachines, &sfnapi.ListStateMachinesInput{MaxResults: 1001})),
			"ValidationException", 400, "1 validation error detected: Value '1001' at 'maxResults' failed to satisfy constraint: Member must have value less than or equal to 1000")
	})
}

// UpdateStateMachine mints a revisionId only on a real change, returns the
// same one when nothing changed, and Describe shows it; an update with
// nothing to change answers MissingRequiredParameter (stepfunctions-api.md
// 5.3; aws-recorded localstack@8b9a79f v2/test_sfn_api.snapshot.json:L609-L705,
// L990-L1100, L1036).
func TestUpdateStateMachine(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.create("m", resultMachine)
		_, err := call(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn})
		wantError(h, err, "MissingRequiredParameter", 400, "Either the definition, the role ARN, the LoggingConfiguration, or the TracingConfiguration must be specified")

		first := must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, Definition: new(waitMachine)})
		if first.RevisionId == nil || *first.RevisionId == "" || !first.UpdateDate.Equal(h.now()) {
			h.Fatalf("update = %+v, want a revisionId and updateDate now", first)
		}
		d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: arn})
		if d.Definition != waitMachine || d.RevisionId == nil || *d.RevisionId != *first.RevisionId {
			h.Errorf("after update: definition %q revisionId %v, want the new definition and %s", d.Definition, d.RevisionId, *first.RevisionId)
		}
		same := must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, Definition: new(waitMachine)})
		if same.RevisionId == nil || *same.RevisionId != *first.RevisionId {
			h.Errorf("no-change update revisionId = %v, want the existing %s", same.RevisionId, *first.RevisionId)
		}
		second := must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, TracingConfiguration: &sfnapi.TracingConfiguration{Enabled: true}})
		if second.RevisionId == nil || *second.RevisionId == *first.RevisionId {
			h.Errorf("tracing change revisionId = %v, want a new one", second.RevisionId)
		}
	})
}

// The refusals with 501 and the message form of a member understudy does
// not implement (spec "Definition grammar"; contract section 7): nothing is
// created or changed. publish false, which Terraform always sends
// (stepfunctions-api.md 11.1), is accepted; versionDescription without
// publish keeps AWS's recorded ValidationException (stepfunctions-api.md
// 5.1; aws-recorded localstack@8b9a79f v2/test_sfn_api_versioning.snapshot.json:L15-L30).
func TestUnimplementedMembersAnswer501(t *testing.T) {
	run(t, func(h *harness) {
		express := sfnapi.StateMachineTypeExpress
		cmk := &sfnapi.EncryptionConfiguration{Type: sfnapi.EncryptionTypeCustomerManagedKmsKey, KmsKeyId: new("alias/k")}
		for prefix, in := range map[string]*sfnapi.CreateStateMachineInput{
			"CreateStateMachine with type":                    {Name: "x1", Definition: resultMachine, RoleArn: h.roleArn(), Type: &express},
			"CreateStateMachine with publish":                 {Name: "x2", Definition: resultMachine, RoleArn: h.roleArn(), Publish: true},
			"CreateStateMachine with publish ":                {Name: "x3", Definition: resultMachine, RoleArn: h.roleArn(), Publish: true, VersionDescription: new("v")},
			"CreateStateMachine with encryptionConfiguration": {Name: "x4", Definition: resultMachine, RoleArn: h.roleArn(), EncryptionConfiguration: cmk},
		} {
			_, err := call(h, sfnapi.OpCreateStateMachine, in)
			if err == nil || err.ID.Status != 501 || err.ID.Shape.Name() != "NotImplemented" || !strings.HasPrefix(err.Message, strings.TrimSpace(prefix)) {
				h.Errorf("%+v = %+v, want 501 NotImplemented starting %q", in, err, strings.TrimSpace(prefix))
			}
			if _, err := call(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: h.machineArn(in.Name)}); err == nil {
				h.Errorf("refused create of %s left a machine behind", in.Name)
			}
		}
		_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "x5", Definition: resultMachine, RoleArn: h.roleArn(), VersionDescription: new("v")})
		wantError(h, err, "ValidationException", 400, "Version description can only be set when publish is true")
		_, err = call(h, sfnapi.OpValidateStateMachineDefinition, &sfnapi.ValidateStateMachineDefinitionInput{Definition: resultMachine, Type: &express})
		if err == nil || err.ID.Status != 501 || !strings.HasPrefix(err.Message, "ValidateStateMachineDefinition with type") {
			h.Errorf("Validate EXPRESS = %+v, want 501", err)
		}

		arn := h.create("m", resultMachine)
		for prefix, in := range map[string]*sfnapi.UpdateStateMachineInput{
			"UpdateStateMachine with publish":                 {StateMachineArn: arn, Definition: new(waitMachine), Publish: true},
			"UpdateStateMachine with versionDescription":      {StateMachineArn: arn, Definition: new(waitMachine), VersionDescription: new("v")},
			"UpdateStateMachine with encryptionConfiguration": {StateMachineArn: arn, EncryptionConfiguration: cmk},
		} {
			_, err := call(h, sfnapi.OpUpdateStateMachine, in)
			if err == nil || err.ID.Status != 501 || err.ID.Shape.Name() != "NotImplemented" || !strings.HasPrefix(err.Message, prefix) {
				h.Errorf("%s: %+v, want 501 NotImplemented", prefix, err)
			}
		}
		if d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: arn}); d.Definition != resultMachine || d.RevisionId != nil {
			h.Errorf("refused updates changed the machine: %+v", d)
		}
		withFalse := must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "tf", Definition: resultMachine, RoleArn: h.roleArn(), Publish: false})
		if withFalse.StateMachineArn != h.machineArn("tf") {
			h.Errorf("publish false create = %+v", withFalse)
		}
	})
}

// CreateStateMachine and UpdateStateMachine answer InvalidDefinition with
// the envelope of the first problem (aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L26-L32); a refused feature uses the same
// envelope with code UNSUPPORTED_BY_UNDERSTUDY (stepfunctions-asl.md 10.4;
// the JSONata refusal check of spec "Done checks").
func TestInvalidDefinition(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "m", Definition: `{"StartAt":"P"}`, RoleArn: h.roleArn()})
		wantError(h, err, "InvalidDefinition", 400, "Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: These fields are required: [States] at /'")

		jsonata := `{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`
		_, err = call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "m", Definition: jsonata, RoleArn: h.roleArn()})
		if err == nil || err.ID.Shape.Name() != "InvalidDefinition" || err.ID.Status != 400 ||
			!strings.HasPrefix(err.Message, "Invalid State Machine Definition: 'UNSUPPORTED_BY_UNDERSTUDY: understudy does not implement ") {
			h.Errorf("JSONata create = %+v, want InvalidDefinition UNSUPPORTED_BY_UNDERSTUDY", err)
		}
		arn := h.create("ok", resultMachine)
		_, err = call(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, Definition: new(`{"StartAt":"P"}`)})
		wantError(h, err, "InvalidDefinition", 400, "Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: These fields are required: [States] at /'")
	})
}

// ValidateStateMachineDefinition answers 200 with result and diagnostics,
// never an error, with the recorded diagnostics (stepfunctions-api.md 5.6;
// aws-recorded localstack@8b9a79f v2/test_sfn_api_validation.snapshot.json:L2-L77).
func TestValidateStateMachineDefinition(t *testing.T) {
	run(t, func(h *harness) {
		ok := must(h, sfnapi.OpValidateStateMachineDefinition, &sfnapi.ValidateStateMachineDefinitionInput{Definition: resultMachine})
		if ok.Result != sfnapi.ValidateStateMachineDefinitionResultCodeOk || len(ok.Diagnostics) != 0 || ok.Truncated == nil || *ok.Truncated {
			h.Errorf("valid Pass = %+v, want OK, no diagnostics, truncated false (L64-L77)", ok)
		}
		tests := []struct {
			def, code, message string
			location           *string
		}{
			{" ", "SCHEMA_VALIDATION_FAILED", "Definition must be a valid JSON object", nil},
			{"{}", "SCHEMA_VALIDATION_FAILED", "These fields are required: [States, StartAt]", new("/")},
		}
		for _, tt := range tests {
			out := must(h, sfnapi.OpValidateStateMachineDefinition, &sfnapi.ValidateStateMachineDefinitionInput{Definition: tt.def})
			if out.Result != sfnapi.ValidateStateMachineDefinitionResultCodeFail || len(out.Diagnostics) == 0 {
				h.Fatalf("Validate(%q) = %+v, want FAIL with diagnostics", tt.def, out)
			}
			d := out.Diagnostics[0]
			if d.Severity != "ERROR" || d.Code != tt.code || d.Message != tt.message || val(d.Location) != val(tt.location) {
				h.Errorf("Validate(%q) diagnostic = %+v %s, want ERROR %s %q at %s", tt.def, d, val(d.Location), tt.code, tt.message, val(tt.location))
			}
		}
		refused := must(h, sfnapi.OpValidateStateMachineDefinition, &sfnapi.ValidateStateMachineDefinitionInput{
			Definition: `{"StartAt":"P","States":{"P":{"Type":"Pass","QueryLanguage":"JSONata","End":true}}}`,
		})
		if refused.Result != sfnapi.ValidateStateMachineDefinitionResultCodeFail || len(refused.Diagnostics) == 0 ||
			refused.Diagnostics[0].Code != "UNSUPPORTED_BY_UNDERSTUDY" || val(refused.Diagnostics[0].Location) != "/States/P/QueryLanguage" {
			h.Errorf("JSONata state = %+v, want an UNSUPPORTED_BY_UNDERSTUDY ERROR at /States/P/QueryLanguage (stepfunctions-asl.md 10.4)", refused)
		}
	})
}

// Logging: a level other than OFF needs exactly one destination, the log
// group must exist, an empty configuration stores the defaults
// (stepfunctions-api.md 5.1; aws-recorded localstack@8b9a79f
// v2/test_sfn_api_logs.snapshot.json:L418-L552). A valid destination is
// echoed.
func TestLoggingConfiguration(t *testing.T) {
	run(t, func(h *harness) {
		must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "/aws/vendedlogs/states/m"})
		groupArn := fmt.Sprintf("arn:aws:logs:%s:%s:log-group:/aws/vendedlogs/states/m:*", h.Scope.Region, h.Scope.Account)
		gone := fmt.Sprintf("arn:aws:logs:%s:%s:log-group:/aws/vendedlogs/states/gone:*", h.Scope.Region, h.Scope.Account)
		dest := func(arn string) sfnapi.LogDestination {
			return sfnapi.LogDestination{CloudWatchLogsLogGroup: &sfnapi.CloudWatchLogsLogGroup{LogGroupArn: new(arn)}}
		}
		all := sfnapi.LogLevelAll
		for name, cfg := range map[string]*sfnapi.LoggingConfiguration{
			"no destination":   {Level: &all},
			"two destinations": {Level: &all, Destinations: []sfnapi.LogDestination{dest(groupArn), dest(groupArn)}},
		} {
			_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "m", Definition: resultMachine, RoleArn: h.roleArn(), LoggingConfiguration: cfg})
			if err == nil || err.ID.Shape.Name() != "InvalidLoggingConfiguration" || err.Message != "Invalid Logging Configuration: Must specify exactly one Log Destination." {
				h.Errorf("%s: %+v, want InvalidLoggingConfiguration (L509-L552)", name, err)
			}
		}
		_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{
			Name: "m", Definition: resultMachine, RoleArn: h.roleArn(),
			LoggingConfiguration: &sfnapi.LoggingConfiguration{Level: &all, Destinations: []sfnapi.LogDestination{dest(gone)}},
		})
		wantError(h, err, "InvalidLoggingConfiguration", 400, "Invalid Logging Configuration: Log Destination not found.")

		empty := must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "e", Definition: resultMachine, RoleArn: h.roleArn(), LoggingConfiguration: &sfnapi.LoggingConfiguration{}})
		d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: empty.StateMachineArn})
		if l := d.LoggingConfiguration; l == nil || l.Level == nil || *l.Level != sfnapi.LogLevelOff || l.IncludeExecutionData {
			h.Errorf("empty loggingConfiguration stored as %+v, want {includeExecutionData: false, level: OFF} (L418-L507)", l)
		}

		logged := must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{
			Name: "l", Definition: resultMachine, RoleArn: h.roleArn(),
			LoggingConfiguration: &sfnapi.LoggingConfiguration{Level: &all, IncludeExecutionData: true, Destinations: []sfnapi.LogDestination{dest(groupArn)}},
		})
		d = must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: logged.StateMachineArn})
		l := d.LoggingConfiguration
		if l == nil || l.Level == nil || *l.Level != all || !l.IncludeExecutionData || len(l.Destinations) != 1 ||
			l.Destinations[0].CloudWatchLogsLogGroup == nil || val(l.Destinations[0].CloudWatchLogsLogGroup.LogGroupArn) != groupArn {
			h.Errorf("loggingConfiguration = %+v, want it echoed", l)
		}
	})
}

// Tags round-trip; a version ARN answers ResourceNotFound with the recorded
// text (stepfunctions-api.md 5.8; aws-recorded localstack@8b9a79f
// v2/test_sfn_api_tagging.snapshot.json:L364); over 50 tags answers
// TooManyTags ([M] com.amazonaws.sfn#TagResource).
func TestTags(t *testing.T) {
	run(t, func(h *harness) {
		arn := must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{
			Name: "m", Definition: resultMachine, RoleArn: h.roleArn(),
			Tags: []sfnapi.Tag{{Key: new("team"), Value: new("a")}},
		}).StateMachineArn
		must(h, sfnapi.OpTagResource, &sfnapi.TagResourceInput{ResourceArn: arn, Tags: []sfnapi.Tag{{Key: new("env"), Value: new("dev")}}})
		must(h, sfnapi.OpUntagResource, &sfnapi.UntagResourceInput{ResourceArn: arn, TagKeys: []string{"team"}})
		tags := must(h, sfnapi.OpListTagsForResource, &sfnapi.ListTagsForResourceInput{ResourceArn: arn}).Tags
		if len(tags) != 1 || val(tags[0].Key) != "env" || val(tags[0].Value) != "dev" {
			h.Errorf("tags = %+v, want env=dev", tags)
		}
		version := arn + ":1"
		_, err := call(h, sfnapi.OpTagResource, &sfnapi.TagResourceInput{ResourceArn: version, Tags: []sfnapi.Tag{{Key: new("k"), Value: new("v")}}})
		wantError(h, err, "ResourceNotFound", 404, "Resource not found: '"+version+"'")
		many := make([]sfnapi.Tag, 51)
		for i := range many {
			many[i] = sfnapi.Tag{Key: new(fmt.Sprintf("k%d", i)), Value: new("v")}
		}
		_, err = call(h, sfnapi.OpTagResource, &sfnapi.TagResourceInput{ResourceArn: arn, Tags: many})
		if err == nil || err.ID.Shape.Name() != "TooManyTags" {
			h.Errorf("51 tags = %+v, want TooManyTags (stepfunctions-api.md 4.2: 50 per resource)", err)
		}
	})
}

// ListStateMachineVersions is always empty: publishing is refused
// (coverage note; stepfunctions-api.md 13.1).
func TestListStateMachineVersionsIsEmpty(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.create("m", resultMachine)
		if got := must(h, sfnapi.OpListStateMachineVersions, &sfnapi.ListStateMachineVersionsInput{StateMachineArn: arn}).StateMachineVersions; len(got) != 0 {
			h.Errorf("versions = %+v, want none", got)
		}
	})
}
