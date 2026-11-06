package lambda_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// pairGate is an Authorizer that allows every call and keeps each pair it
// was asked about as "<action> on <resource>", the resource "*" for
// kernel.AnyResource.
type pairGate struct {
	mu    sync.Mutex
	asked []string
}

func (g *pairGate) Authorize(_ context.Context, _ *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range q.Pairs {
		on := "*"
		if r, ok := p.Resource.(kernel.ResourceARN); ok {
			on = r.ARN.String()
		}
		g.asked = append(g.asked, p.Action.String()+" on "+on)
	}
	return kernel.Allowed{}
}

// take returns the pairs asked since the last take and forgets them.
func (g *pairGate) take() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.asked
	g.asked = nil
	return out
}

// Step 8 asks about each Lambda operation's action on the function,
// version, alias or mapping its input addresses, built from the name as
// Invoke's is: AWS evaluates the call against that resource
// (iam-evaluation.md 2.1), whose ARN the name gives before any lookup
// (iam-enforcement-surfaces.md 5.3, switch iam.enforce.missing-resource).
// Asked on "*" instead, a policy granting the action on the function alone,
// as /iam/policy renders it from the access record (12.6), was refused
// under iam.enforce=enforce. The function here does not exist, so each
// pair is asked before the lookup that answers ResourceNotFoundException.
func TestLambdaOperationsAreAskedAboutTheResourceTheirInputAddresses(t *testing.T) {
	const (
		fn      = "arn:aws:lambda:us-east-1:000000000000:function:ghost"
		mapping = "arn:aws:lambda:us-east-1:000000000000:event-source-mapping:00000000-0000-0000-0000-000000000001"
		uuid    = "00000000-0000-0000-0000-000000000001"
	)
	queue := "arn:aws:sqs:us-east-1:000000000000:orders"
	cases := []struct {
		name string
		want string
		call func(h *harness)
	}{
		{"CreateFunction", "lambda:CreateFunction on arn:aws:lambda:us-east-1:000000000000:function:fresh", func(h *harness) {
			invoke(h, lambdaapi.OpCreateFunction, zipFunction(h, "fresh"))
		}},
		{"GetFunction", "lambda:GetFunction on " + fn + ":1", func(h *harness) {
			invoke(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "ghost", Qualifier: new("1")})
		}},
		{"GetFunctionConfiguration", "lambda:GetFunctionConfiguration on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: fn})
		}},
		{"UpdateFunctionConfiguration", "lambda:UpdateFunctionConfiguration on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "ghost", Description: new("d")})
		}},
		{"UpdateFunctionCode", "lambda:UpdateFunctionCode on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "ghost", ZipFile: zipOf(h, "#!/bin/sh\n")})
		}},
		{"DeleteFunction", "lambda:DeleteFunction on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "ghost"})
		}},
		{"GetFunctionCodeSigningConfig", "lambda:GetFunctionCodeSigningConfig on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpGetFunctionCodeSigningConfig, &lambdaapi.GetFunctionCodeSigningConfigInput{FunctionName: "ghost"})
		}},
		{"PublishVersion", "lambda:PublishVersion on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "ghost"})
		}},
		{"ListVersionsByFunction", "lambda:ListVersionsByFunction on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpListVersionsByFunction, &lambdaapi.ListVersionsByFunctionInput{FunctionName: "ghost"})
		}},
		{"CreateAlias", "lambda:CreateAlias on " + fn + ":live", func(h *harness) {
			invoke(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: "ghost", Name: "live", FunctionVersion: "1"})
		}},
		{"GetAlias", "lambda:GetAlias on " + fn + ":live", func(h *harness) {
			invoke(h, lambdaapi.OpGetAlias, &lambdaapi.GetAliasInput{FunctionName: "ghost", Name: "live"})
		}},
		{"UpdateAlias", "lambda:UpdateAlias on " + fn + ":live", func(h *harness) {
			invoke(h, lambdaapi.OpUpdateAlias, &lambdaapi.UpdateAliasInput{FunctionName: "ghost", Name: "live", FunctionVersion: new("1")})
		}},
		{"DeleteAlias", "lambda:DeleteAlias on " + fn + ":live", func(h *harness) {
			invoke(h, lambdaapi.OpDeleteAlias, &lambdaapi.DeleteAliasInput{FunctionName: "ghost", Name: "live"})
		}},
		{"ListAliases", "lambda:ListAliases on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpListAliases, &lambdaapi.ListAliasesInput{FunctionName: "ghost"})
		}},
		{"AddPermission", "lambda:AddPermission on " + fn + ":live", func(h *harness) {
			invoke(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
				FunctionName: "ghost", Qualifier: new("live"), StatementId: "s3", Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com",
			})
		}},
		{"GetPolicy", "lambda:GetPolicy on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpGetPolicy, &lambdaapi.GetPolicyInput{FunctionName: "ghost"})
		}},
		{"RemovePermission", "lambda:RemovePermission on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpRemovePermission, &lambdaapi.RemovePermissionInput{FunctionName: "ghost", StatementId: "s3"})
		}},
		{"PutFunctionConcurrency", "lambda:PutFunctionConcurrency on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpPutFunctionConcurrency, &lambdaapi.PutFunctionConcurrencyInput{FunctionName: "ghost", ReservedConcurrentExecutions: 1})
		}},
		{"GetFunctionConcurrency", "lambda:GetFunctionConcurrency on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpGetFunctionConcurrency, &lambdaapi.GetFunctionConcurrencyInput{FunctionName: "ghost"})
		}},
		{"DeleteFunctionConcurrency", "lambda:DeleteFunctionConcurrency on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpDeleteFunctionConcurrency, &lambdaapi.DeleteFunctionConcurrencyInput{FunctionName: "ghost"})
		}},
		{"PutFunctionEventInvokeConfig", "lambda:PutFunctionEventInvokeConfig on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "ghost", MaximumRetryAttempts: new(int32(1))})
		}},
		{"UpdateFunctionEventInvokeConfig", "lambda:UpdateFunctionEventInvokeConfig on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpUpdateFunctionEventInvokeConfig, &lambdaapi.UpdateFunctionEventInvokeConfigInput{FunctionName: "ghost", MaximumRetryAttempts: new(int32(1))})
		}},
		{"GetFunctionEventInvokeConfig", "lambda:GetFunctionEventInvokeConfig on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpGetFunctionEventInvokeConfig, &lambdaapi.GetFunctionEventInvokeConfigInput{FunctionName: "ghost"})
		}},
		{"DeleteFunctionEventInvokeConfig", "lambda:DeleteFunctionEventInvokeConfig on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpDeleteFunctionEventInvokeConfig, &lambdaapi.DeleteFunctionEventInvokeConfigInput{FunctionName: "ghost"})
		}},
		{"ListFunctionEventInvokeConfigs", "lambda:ListFunctionEventInvokeConfigs on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpListFunctionEventInvokeConfigs, &lambdaapi.ListFunctionEventInvokeConfigsInput{FunctionName: "ghost"})
		}},
		{"CreateFunctionUrlConfig", "lambda:CreateFunctionUrlConfig on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{FunctionName: "ghost", AuthType: lambdaapi.FunctionUrlAuthTypeNone})
		}},
		{"GetFunctionUrlConfig", "lambda:GetFunctionUrlConfig on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpGetFunctionUrlConfig, &lambdaapi.GetFunctionUrlConfigInput{FunctionName: "ghost"})
		}},
		{"UpdateFunctionUrlConfig", "lambda:UpdateFunctionUrlConfig on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpUpdateFunctionUrlConfig, &lambdaapi.UpdateFunctionUrlConfigInput{FunctionName: "ghost", AuthType: new(lambdaapi.FunctionUrlAuthTypeNone)})
		}},
		{"DeleteFunctionUrlConfig", "lambda:DeleteFunctionUrlConfig on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpDeleteFunctionUrlConfig, &lambdaapi.DeleteFunctionUrlConfigInput{FunctionName: "ghost"})
		}},
		{"ListFunctionUrlConfigs", "lambda:ListFunctionUrlConfigs on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpListFunctionUrlConfigs, &lambdaapi.ListFunctionUrlConfigsInput{FunctionName: "ghost"})
		}},
		{"TagResource", "lambda:TagResource on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpTagResource, &lambdaapi.TagResourceInput{Resource: fn, Tags: map[string]string{"team": "a"}})
		}},
		{"UntagResource", "lambda:UntagResource on " + mapping, func(h *harness) {
			invoke(h, lambdaapi.OpUntagResource, &lambdaapi.UntagResourceInput{Resource: mapping, TagKeys: []string{"team"}})
		}},
		{"ListTags", "lambda:ListTags on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpListTags, &lambdaapi.ListTagsInput{Resource: fn})
		}},
		{"CreateEventSourceMapping", "lambda:CreateEventSourceMapping on " + fn, func(h *harness) {
			invoke(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{FunctionName: "ghost", EventSourceArn: &queue})
		}},
		{"GetEventSourceMapping", "lambda:GetEventSourceMapping on " + mapping, func(h *harness) {
			invoke(h, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: uuid})
		}},
		{"UpdateEventSourceMapping", "lambda:UpdateEventSourceMapping on " + mapping, func(h *harness) {
			invoke(h, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: uuid, Enabled: new(false)})
		}},
		{"DeleteEventSourceMapping", "lambda:DeleteEventSourceMapping on " + mapping, func(h *harness) {
			invoke(h, lambdaapi.OpDeleteEventSourceMapping, &lambdaapi.DeleteEventSourceMappingInput{UUID: uuid})
		}},
	}
	gate := &pairGate{}
	run(t, lambda.Options{}, func(h *harness) {
		for _, tc := range cases {
			tc.call(h)
			if asked := gate.take(); !slices.Contains(asked, tc.want) {
				h.Errorf("%s: step 8 asked %q, want it to ask %q", tc.name, asked, tc.want)
			}
		}
	}, kerneltest.Authorizer(gate))
}

// A function name step 8 cannot read as an ARN of the caller's region is
// asked about on no resource, as Invoke's is, and the handler answers it.
func TestALambdaNameOfAnotherRegionNamesNoResource(t *testing.T) {
	gate := &pairGate{}
	run(t, lambda.Options{}, func(h *harness) {
		invoke(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "arn:aws:lambda:eu-west-1:000000000000:function:ghost"})
		if asked := gate.take(); !slices.Equal(asked, []string{"lambda:GetFunction on *"}) {
			h.Errorf("step 8 asked %q, want only lambda:GetFunction on *", asked)
		}
	}, kerneltest.Authorizer(gate))
}

// invoke runs op as a client request, whatever it answers.
func invoke[In, Out any](h *harness, op kernel.Op[In, Out], in *In) {
	h.Helper()
	_, _ = h.Invoke(h.Context(), op, in)
}
