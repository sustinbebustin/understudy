package sfn

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

func passRoleAction() registry.IAMAction { return registry.IAMAction{Prefix: "iam", Name: "PassRole"} }

func assumeRoleAction() registry.IAMAction {
	return registry.IAMAction{Prefix: "sts", Name: "AssumeRole"}
}

// logDeliveryActions are what CloudWatch Logs delivery needs of the
// execution role, on "*" (stepfunctions-api.md 9.5). That AWS checks them
// at CreateStateMachine and UpdateStateMachine is inferred from the
// documented failure text (stepfunctions-api.md 5.1).
func logDeliveryActions() []registry.IAMAction {
	names := []string{
		"CreateLogDelivery", "GetLogDelivery", "UpdateLogDelivery", "DeleteLogDelivery",
		"ListLogDeliveries", "PutResourcePolicy", "DescribeResourcePolicies", "DescribeLogGroups",
	}
	out := make([]registry.IAMAction, 0, len(names))
	for _, n := range names {
		out = append(out, registry.IAMAction{Prefix: "logs", Name: n})
	}
	return out
}

// passedToStates is the condition key a role passed to Step Functions
// carries (stepfunctions-api.md 5.1, 9.5).
func passedToStates() kernel.ConditionKeys {
	return kernel.ConditionKeys{"iam:PassedToService": {statesPrincipal}}
}

// errLogDestinationDenied is the documented text for an execution role
// that may not deliver logs (stepfunctions-api.md 5.1, aws-doc cw-logs).
const errLogDestinationDenied = "The state machine IAM Role is not authorized to access the Log Destination"

// passedRole is the execution role a request passes, which iam:PassRole
// acts on: nothing when it passes none or the value is not a role ARN,
// which the handler answers with InvalidArn.
func passedRole(raw *string) []aws.ARN {
	if raw == nil || checkRoleArn(*raw) != nil {
		return nil
	}
	a, err := aws.ParseARN(*raw)
	if err != nil {
		return nil
	}
	return []aws.ARN{a}
}

// createAuthorization declares what CreateStateMachine is asked about at
// step 8 beside states:CreateStateMachine: iam:PassRole on the execution
// role with iam:PassedToService states.amazonaws.com, and with tags
// states:TagResource (stepfunctions-api.md 9.5), never the rest of the
// generated list (states:PublishStateMachineVersion is refused).
func createAuthorization() []kernel.BindOption {
	return []kernel.BindOption{
		kernel.WithActions(func(_ *kernel.Call, in *sfnapi.CreateStateMachineInput) []registry.IAMAction {
			out := []registry.IAMAction{passRoleAction()}
			if len(in.Tags) > 0 {
				out = append(out, registry.IAMAction{Prefix: "states", Name: "TagResource"})
			}
			return out
		}),
		kernel.WithResourcesFor([]registry.IAMAction{passRoleAction()}, func(_ *kernel.Call, in *sfnapi.CreateStateMachineInput) []aws.ARN {
			return passedRole(&in.RoleArn)
		}),
		kernel.WithConditionKeys(func(*kernel.Call, *sfnapi.CreateStateMachineInput) kernel.ConditionKeys { return passedToStates() }),
	}
}

// updateAuthorization is UpdateStateMachine's createAuthorization: iam:PassRole
// when it passes a role.
func updateAuthorization() []kernel.BindOption {
	return []kernel.BindOption{
		kernel.WithActions(func(_ *kernel.Call, in *sfnapi.UpdateStateMachineInput) []registry.IAMAction {
			if in.RoleArn == nil {
				return nil
			}
			return []registry.IAMAction{passRoleAction()}
		}),
		kernel.WithResourcesFor([]registry.IAMAction{passRoleAction()}, func(_ *kernel.Call, in *sfnapi.UpdateStateMachineInput) []aws.ARN {
			return passedRole(in.RoleArn)
		}),
		kernel.WithConditionKeys(func(_ *kernel.Call, in *sfnapi.UpdateStateMachineInput) kernel.ConditionKeys {
			if in.RoleArn == nil {
				return nil
			}
			return passedToStates()
		}),
	}
}

// roleCheck is what a create or update asks of its execution role.
type roleCheck struct {
	// machine is the state machine the role is given to.
	machine machineRef
	// role is the execution role, already checked by checkRoleArn.
	role string
	// trust asks whether the role trusts states.amazonaws.com: the role is
	// being passed.
	trust bool
	// logging is the machine's logging configuration once the call is
	// done; a level other than OFF asks for the log-delivery actions.
	logging Logging
}

// checkExecutionRole asks the Authorizer, under iam.enforce advise and
// enforce, whether states.amazonaws.com may assume the role, then whether
// the role may deliver logs (spec "Enforce matrix"). Under off there is no
// Authorizer and nothing is asked. A denial is SFN's AccessDeniedException:
// the trust text is understudy's, since AWS checks no trust at create
// (SFN-08, recorded 2026-11-03), the logging text
// AWS's documented one. Call it before Write, holding no lock.
func checkExecutionRole(ctx context.Context, c *kernel.Call, rc roleCheck) *awserr.Error {
	roleARN, err := aws.ParseARN(rc.role)
	if err != nil {
		return nil
	}
	if rc.trust {
		// This trust check is understudy's own: AWS checks no trust at
		// create (SFN-08, recorded 2026-11-03). The aws:SourceArn and
		// aws:SourceAccount keys follow AWS's confused-deputy documentation
		// for the role's assumption at invocation.
		as := kernel.AsService{Name: statesPrincipal, Source: rc.machine.arn(), SourceAccount: rc.machine.scope.Account}
		q := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: assumeRoleAction(), Resource: kernel.ResourceARN{ARN: roleARN}}}}
		if err := kernel.AuthorizeAs(ctx, c, as, q); err != nil {
			return reworded(err, "Step Functions cannot assume the execution role "+rc.role+
				": it does not exist, or its trust policy does not allow "+statesPrincipal+" to call sts:AssumeRole")
		}
	}
	if rc.logging.Level == "" || rc.logging.Level == string(sfnapi.LogLevelOff) {
		return nil
	}
	actions := logDeliveryActions()
	q := kernel.AuthzQuery{Pairs: make([]kernel.Pair, 0, len(actions))}
	for _, a := range actions {
		q.Pairs = append(q.Pairs, kernel.Pair{Action: a, Resource: kernel.AnyResource{}})
	}
	if err := kernel.AuthorizeAs(ctx, c, kernel.AsExecutionRole{Role: roleARN}, q); err != nil {
		return reworded(err, errLogDestinationDenied)
	}
	return nil
}

// reworded is a denial with message in place of the Authorizer's, which
// names the principal asked about rather than the caller; any other error
// is returned as is.
func reworded(err *awserr.Error, message string) *awserr.Error {
	if _, denial := err.Origin.(awserr.Denial); !denial {
		return err
	}
	out := *err
	out.Message, out.Messages = message, nil
	return &out
}
