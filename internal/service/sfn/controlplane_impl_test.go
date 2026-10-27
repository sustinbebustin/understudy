package sfn_test

import (
	"fmt"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// endMachine is a second definition of one Pass state, for updates while
// Wait is not built.
const endMachine = `{"StartAt":"Q","States":{"Q":{"Type":"Pass","End":true}}}`

// UpdateStateMachine mints a revisionId on a real change only, returns the
// existing one when nothing changed and none before the first effective
// update; running and finished executions keep the machine they started
// with (stepfunctions-api.md 5.3; aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L609-L705; aws-doc api/API_UpdateStateMachine).
func TestUpdateStateMachineRevisions(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.create("m", resultMachine)
		unchanged := must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, Definition: new(resultMachine)})
		if unchanged.RevisionId != nil || !unchanged.UpdateDate.Equal(h.now()) {
			h.Errorf("no-change update before any revision = %+v, want no revisionId", unchanged)
		}
		owned := must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{
			StateMachineArn: arn, EncryptionConfiguration: &sfnapi.EncryptionConfiguration{Type: sfnapi.EncryptionTypeAwsOwnedKey},
		})
		if owned.RevisionId != nil {
			h.Errorf("AWS_OWNED_KEY update = %+v, want no change", owned)
		}
		before := h.start(arn, "before", nil)

		first := must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, Definition: new(endMachine)})
		if first.RevisionId == nil {
			h.Fatalf("update = %+v, want a revisionId", first)
		}
		if again := must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, Definition: new(endMachine)}); val(again.RevisionId) != *first.RevisionId {
			h.Errorf("no-change update revisionId = %s, want %s", val(again.RevisionId), *first.RevisionId)
		}
		role := fmt.Sprintf("arn:aws:iam::%s:role/other", h.Scope.Account)
		second := must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, RoleArn: new(role)})
		if second.RevisionId == nil || *second.RevisionId == *first.RevisionId {
			h.Errorf("role change revisionId = %s, want a new one", val(second.RevisionId))
		}
		d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: arn})
		if d.Definition != endMachine || d.RoleArn != role || val(d.RevisionId) != *second.RevisionId || !d.CreationDate.Equal(h.now()) {
			h.Errorf("DescribeStateMachine after updates = %+v", d)
		}

		ran := must(h, sfnapi.OpDescribeStateMachineForExecution, &sfnapi.DescribeStateMachineForExecutionInput{ExecutionArn: before})
		if ran.Definition != resultMachine || ran.RoleArn != h.roleArn() || ran.RevisionId != nil {
			h.Errorf("machine for an execution started before the updates = %+v, want the original", ran)
		}
		after := h.start(arn, "after", nil)
		ran = must(h, sfnapi.OpDescribeStateMachineForExecution, &sfnapi.DescribeStateMachineForExecutionInput{ExecutionArn: after})
		if ran.Definition != endMachine || ran.RoleArn != role || val(ran.RevisionId) != *second.RevisionId {
			h.Errorf("machine for an execution started after the updates = %+v", ran)
		}
	})
}

// UpdateStateMachine answers the recorded not-found and bad-ARN texts
// (stepfunctions-api.md 3.2; aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L392, L1515), and a refused update changes
// nothing.
func TestUpdateStateMachineErrors(t *testing.T) {
	run(t, func(h *harness) {
		missing := h.machineArn("nope")
		_, err := call(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: missing, Definition: new(endMachine)})
		wantError(h, err, "StateMachineDoesNotExist", 400, "State Machine Does Not Exist: '"+missing+"'")
		_, err = call(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: "not-an-arn", Definition: new(endMachine)})
		wantError(h, err, "InvalidArn", 400, "Invalid Arn: 'Invalid ARN prefix: not-an-arn'")

		arn := h.create("m", resultMachine)
		all := sfnapi.LogLevelAll
		_, err = call(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, LoggingConfiguration: &sfnapi.LoggingConfiguration{Level: &all}})
		wantError(h, err, "InvalidLoggingConfiguration", 400, "Invalid Logging Configuration: Must specify exactly one Log Destination.")
		if d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: arn}); d.RevisionId != nil || *d.LoggingConfiguration.Level != sfnapi.LogLevelOff {
			h.Errorf("a refused update changed the machine: %+v", d)
		}
	})
}

// A logging destination may name its log group by the ARN DescribeLogGroups
// gives, ending ":*", or without it; either is echoed as sent. AWS's answer
// for the form without ":*" is unrecorded.
func TestSFN07DestinationWithoutStar(t *testing.T) {
	// inferred, SFN-07
	run(t, func(h *harness) {
		must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "g"})
		bare := fmt.Sprintf("arn:aws:logs:%s:%s:log-group:g", h.Scope.Region, h.Scope.Account)
		all := sfnapi.LogLevelAll
		out := must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{
			Name: "m", Definition: resultMachine, RoleArn: h.roleArn(),
			LoggingConfiguration: &sfnapi.LoggingConfiguration{Level: &all, Destinations: []sfnapi.LogDestination{
				{CloudWatchLogsLogGroup: &sfnapi.CloudWatchLogsLogGroup{LogGroupArn: new(bare)}},
			}},
		})
		d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: out.StateMachineArn})
		if l := d.LoggingConfiguration; len(l.Destinations) != 1 || val(l.Destinations[0].CloudWatchLogsLogGroup.LogGroupArn) != bare {
			h.Errorf("loggingConfiguration = %+v, want %s echoed", l, bare)
		}
		for _, bad := range []string{"not-an-arn", fmt.Sprintf("arn:aws:sqs:%s:%s:g", h.Scope.Region, h.Scope.Account)} {
			_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{
				Name: "x", Definition: resultMachine, RoleArn: h.roleArn(),
				LoggingConfiguration: &sfnapi.LoggingConfiguration{Level: &all, Destinations: []sfnapi.LogDestination{
					{CloudWatchLogsLogGroup: &sfnapi.CloudWatchLogsLogGroup{LogGroupArn: new(bad)}},
				}},
			})
			wantError(h, err, "InvalidLoggingConfiguration", 400, "Invalid Logging Configuration: Log Destination not found.")
		}
	})
}

// A loggingConfiguration sent with "destinations": [] is echoed with
// "destinations": [], and one sent without destinations is echoed without
// them, also after a snapshot round trip (stepfunctions-api.md 5.1;
// aws-recorded localstack@8b9a79f v2/test_sfn_api_logs.snapshot.json:L418-L507, L490).
func TestEmptyLogDestinationsAreEchoed(t *testing.T) {
	run(t, func(h *harness) {
		off := sfnapi.LogLevelOff
		empty := must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{
			Name: "empty", Definition: resultMachine, RoleArn: h.roleArn(),
			LoggingConfiguration: &sfnapi.LoggingConfiguration{Level: &off, Destinations: []sfnapi.LogDestination{}},
		}).StateMachineArn
		absent := must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{
			Name: "absent", Definition: resultMachine, RoleArn: h.roleArn(),
			LoggingConfiguration: &sfnapi.LoggingConfiguration{},
		}).StateMachineArn
		check := func(when string) {
			h.Helper()
			if d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: empty}); d.LoggingConfiguration.Destinations == nil || len(d.LoggingConfiguration.Destinations) != 0 {
				h.Errorf("%s: destinations sent empty echoed as %#v, want []", when, d.LoggingConfiguration.Destinations)
			}
			if d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: absent}); d.LoggingConfiguration.Destinations != nil {
				h.Errorf("%s: destinations not sent echoed as %#v, want none", when, d.LoggingConfiguration.Destinations)
			}
		}
		check("after create")
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		check("after a snapshot round trip")
	})
}

// The role ARN is checked for syntax only, with understudy's InvalidArn
// texts in the recorded "Invalid Arn" forms: AWS's code and text for a
// malformed roleArn are unrecorded.
func TestSFN08RoleArnSyntax(t *testing.T) {
	// inferred, SFN-08
	run(t, func(h *harness) {
		for role, want := range map[string]string{
			"not-an-arn": "Invalid Arn: 'Invalid ARN prefix: not-an-arn'",
			"arn:aws:sqs:us-east-1:" + h.Scope.Account.String() + ":q": "Invalid Arn: 'Invalid ARN prefix: arn:aws:sqs:us-east-1:" + h.Scope.Account.String() + ":q'",
			"arn:aws:iam::" + h.Scope.Account.String() + ":user/u":     "Invalid Arn: 'Resource type not valid in this context: user'",
		} {
			_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "m", Definition: resultMachine, RoleArn: role})
			wantError(h, err, "InvalidArn", 400, want)
		}
		arn := h.create("m", resultMachine)
		_, err := call(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, RoleArn: new("not-an-arn")})
		wantError(h, err, "InvalidArn", 400, "Invalid Arn: 'Invalid ARN prefix: not-an-arn'")
		// A role that does not exist passes: only the syntax is checked.
		must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: arn, RoleArn: new("arn:aws:iam::" + h.Scope.Account.String() + ":role/path/nobody")})
	})
}

// The not-found and bad-ARN texts of the operations bound so far
// (stepfunctions-api.md 3.2; aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L392, L1515, L1588).
func TestNotFoundTextsOfBoundOperations(t *testing.T) {
	run(t, func(h *harness) {
		missing := h.machineArn("nope")
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
// idle one removes it and frees the name (stepfunctions-api.md 5.4;
// aws-recorded localstack@8b9a79f v2/test_sfn_api.snapshot.json:L37-L46).
func TestDeleteIdleStateMachine(t *testing.T) {
	run(t, func(h *harness) {
		must(h, sfnapi.OpDeleteStateMachine, &sfnapi.DeleteStateMachineInput{StateMachineArn: h.machineArn("nope")})
		arn := h.create("m", resultMachine)
		must(h, sfnapi.OpDeleteStateMachine, &sfnapi.DeleteStateMachineInput{StateMachineArn: arn})
		h.settled()
		wantError(h, errOf(call(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: arn})),
			"StateMachineDoesNotExist", 400, "State Machine Does Not Exist: '"+arn+"'")
		if again := h.create("m", endMachine); again != arn {
			h.Errorf("re-created machine ARN = %s, want %s", again, arn)
		}
	})
}
