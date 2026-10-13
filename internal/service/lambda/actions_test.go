package lambda_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// rootUses returns the root's recorded actions of service with their
// resources.
func rootUses(h *harness, service string) []string {
	h.Helper()
	uses := h.Access.Usage(access.Filter{Principal: access.Root{Account: h.Scope.Account}, Service: service})
	out := make([]string, 0, len(uses))
	for i := range uses {
		on := "*"
		if r, ok := uses[i].Resource.(access.OnResource); ok {
			on = r.ARN.String()
		}
		out = append(out, uses[i].Action.String()+" on "+on)
	}
	return out
}

const functionARN = "arn:aws:lambda:us-east-1:000000000000:function:fn"

// CreateFunction passes its execution role: the record holds
// lambda:CreateFunction on the function and iam:PassRole on the role, and
// no secondary the input did not use.
func TestCreateFunctionRecordsPassRoleOnTheRole(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		if got, want := rootUses(h, "lambda"), []string{"lambda:CreateFunction on " + functionARN}; !slices.Equal(got, want) {
			t.Errorf("lambda uses = %v, want %v", got, want)
		}
		if got, want := rootUses(h, "iam"), []string{"iam:PassRole on " + role}; !slices.Equal(got, want) {
			t.Errorf("iam uses = %v, want %v", got, want)
		}
	})
}

// Every bound Lambda operation whose generated list holds more than one
// lambda action decides which of them its calls use, so none records the
// whole list.
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		if got := kerneltest.UndeclaredActions(h.Kernel, lambdaapi.Service()); len(got) != 0 {
			t.Errorf("operations without an actions decision: %v; declare them in declareAccess", got)
		}
	})
}

// A CreateFunction naming a capacity provider needs
// lambda:PassCapacityProvider on the provider, which AWS authorizes it
// against; the call touches the provider so the record carries that ARN.
func TestCreateFunctionRecordsPassCapacityProviderOnTheProvider(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		const provider = "arn:aws:lambda:us-east-1:000000000000:capacity-provider:fleet"
		in := zipFunction(h, "fn")
		in.CapacityProviderConfig = &lambdaapi.CapacityProviderConfig{
			LambdaManagedInstancesCapacityProviderConfig: lambdaapi.LambdaManagedInstancesCapacityProviderConfig{CapacityProviderArn: provider},
		}
		call(h, lambdaapi.OpCreateFunction, in)
		if got := rootUses(h, "lambda"); !slices.Contains(got, "lambda:PassCapacityProvider on "+provider) {
			t.Errorf("lambda uses = %v, want lambda:PassCapacityProvider on %s", got, provider)
		}
	})
}

// GetFunction records lambda:GetFunction alone: lambda:ListTags only
// decides whether the response carries the tags.
func TestGetFunctionRecordsGetFunctionAlone(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		h.Access.Reset()
		call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"})
		uses := h.Access.Usage(access.Filter{Principal: access.Root{Account: h.Scope.Account}, Service: "lambda"})
		for i := range uses {
			if got := uses[i].Action.String(); got != "lambda:GetFunction" {
				t.Errorf("GetFunction recorded %s, want lambda:GetFunction alone", got)
			}
		}
	})
}

// CreateFunction with tags also needs lambda:TagResource.
func TestCreateFunctionWithTagsRecordsTagResource(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		in := zipFunction(h, "fn")
		in.Tags = map[string]string{"team": "a"}
		call(h, lambdaapi.OpCreateFunction, in)
		want := []string{"lambda:CreateFunction on " + functionARN, "lambda:TagResource on " + functionARN}
		if got := rootUses(h, "lambda"); !slices.Equal(got, want) {
			t.Errorf("lambda uses = %v, want %v", got, want)
		}
	})
}

// An UpdateFunctionConfiguration that passes no role needs no
// iam:PassRole; one that passes a role needs it on that role.
func TestUpdateFunctionConfigurationRecordsPassRoleOnlyForARole(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		h.settle()
		h.Access.Reset()
		call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Description: new("d")})
		if got := rootUses(h, "iam"); len(got) != 0 {
			t.Errorf("iam uses without a role = %v, want none", got)
		}
		if got, want := rootUses(h, "lambda"), []string{"lambda:UpdateFunctionConfiguration on " + functionARN}; !slices.Equal(got, want) {
			t.Errorf("lambda uses = %v, want %v", got, want)
		}
		h.settle()
		const other = "arn:aws:iam::000000000000:role/other"
		call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Role: new(other)})
		if got, want := rootUses(h, "iam"), []string{"iam:PassRole on " + other}; !slices.Equal(got, want) {
			t.Errorf("iam uses with a role = %v, want %v", got, want)
		}
	})
}
