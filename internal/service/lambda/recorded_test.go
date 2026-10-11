package lambda_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// fnARN is the unqualified ARN of the function name in the test scope.
func fnARN(name string) string { return "arn:aws:lambda:us-east-1:000000000000:function:" + name }

// TestNotFoundRows: a missing function, version, alias or configuration
// answers with the text AWS recorded for each operation
// (test_lambda_eventinvokeconfig_exceptions, test_ops_on_nonexisting_*,
// test_url_config_exceptions, test_permission_exceptions,
// test_function_concurrency_exceptions).
func TestNotFoundRows(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		call(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: "fn", Name: "live", FunctionVersion: "1"})
		const (
			missing      = "The resource you requested does not exist."
			noEventFn    = "The function doesn't exist."
			noURLFn      = "Function does not exist"
			notFoundCode = "ResourceNotFoundException"
		)
		noEventConfig := func(arn string) string { return "The function " + arn + " doesn't have an EventInvokeConfig" }
		permission := func(name string, qualifier *string) *lambdaapi.AddPermissionInput {
			return &lambdaapi.AddPermissionInput{FunctionName: name, Qualifier: qualifier, StatementId: "s", Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com"}
		}
		rows := []struct {
			golden
			call func() *awserr.Error
		}{
			{
				golden{"update event invoke config without one", notFoundCode, 404, noEventConfig(fnARN("fn") + ":$LATEST"), true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpUpdateFunctionEventInvokeConfig, &lambdaapi.UpdateFunctionEventInvokeConfigInput{FunctionName: fnARN("fn"), MaximumRetryAttempts: new(int32(0))})
				},
			},
			{
				golden{"update event invoke config of an alias without one", notFoundCode, 404, noEventConfig(fnARN("fn") + ":live"), true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpUpdateFunctionEventInvokeConfig, &lambdaapi.UpdateFunctionEventInvokeConfigInput{FunctionName: fnARN("fn") + ":live", MaximumRetryAttempts: new(int32(0))})
				},
			},
			{
				golden{"update event invoke config of a missing function", notFoundCode, 404, noEventFn, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpUpdateFunctionEventInvokeConfig, &lambdaapi.UpdateFunctionEventInvokeConfigInput{FunctionName: "doesnotexist", MaximumRetryAttempts: new(int32(0))})
				},
			},
			{
				golden{"get event invoke config of a missing function", notFoundCode, 404, noEventConfig(fnARN("doesnotexist") + ":$LATEST"), true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpGetFunctionEventInvokeConfig, &lambdaapi.GetFunctionEventInvokeConfigInput{FunctionName: "doesnotexist"})
				},
			},
			{
				golden{"get event invoke config of a missing qualifier", notFoundCode, 404, noEventConfig(fnARN("fn") + ":doesnotexist"), true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpGetFunctionEventInvokeConfig, &lambdaapi.GetFunctionEventInvokeConfigInput{FunctionName: "fn", Qualifier: new("doesnotexist")})
				},
			},
			{
				golden{"get event invoke config of a missing version", notFoundCode, 404, noEventConfig(fnARN("fn") + ":1221"), true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpGetFunctionEventInvokeConfig, &lambdaapi.GetFunctionEventInvokeConfigInput{FunctionName: "fn", Qualifier: new("1221")})
				},
			},
			{
				golden{"delete event invoke config of a missing function", notFoundCode, 404, noEventConfig(fnARN("doesnotexist") + ":$LATEST"), true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpDeleteFunctionEventInvokeConfig, &lambdaapi.DeleteFunctionEventInvokeConfigInput{FunctionName: "doesnotexist"})
				},
			},
			{
				golden{"list event invoke configs of a missing function", notFoundCode, 404, noEventFn, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpListFunctionEventInvokeConfigs, &lambdaapi.ListFunctionEventInvokeConfigsInput{FunctionName: "doesnotexist"})
				},
			},
			{
				golden{"create url config of a missing function", notFoundCode, 404, noURLFn, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{FunctionName: "doesnotexist", AuthType: lambdaapi.FunctionUrlAuthTypeNone})
				},
			},
			{
				golden{"get url config by missing name", notFoundCode, 404, missing, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpGetFunctionUrlConfig, &lambdaapi.GetFunctionUrlConfigInput{FunctionName: "doesnotexist"})
				},
			},
			{
				golden{"get url config by missing ARN", notFoundCode, 404, missing, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpGetFunctionUrlConfig, &lambdaapi.GetFunctionUrlConfigInput{FunctionName: fnARN("doesnotexist")})
				},
			},
			{
				golden{"get url config of a missing alias", notFoundCode, 404, missing, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpGetFunctionUrlConfig, &lambdaapi.GetFunctionUrlConfigInput{FunctionName: "fn", Qualifier: new("doesnotexist")})
				},
			},
			{
				golden{"delete url config by missing name", notFoundCode, 404, missing, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpDeleteFunctionUrlConfig, &lambdaapi.DeleteFunctionUrlConfigInput{FunctionName: "doesnotexist"})
				},
			},
			{
				golden{"delete url config by missing ARN", notFoundCode, 404, missing, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpDeleteFunctionUrlConfig, &lambdaapi.DeleteFunctionUrlConfigInput{FunctionName: fnARN("doesnotexist")})
				},
			},
			{
				golden{"delete url config of a missing alias", notFoundCode, 404, missing, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpDeleteFunctionUrlConfig, &lambdaapi.DeleteFunctionUrlConfigInput{FunctionName: "fn", Qualifier: new("doesnotexist")})
				},
			},
			{
				golden{"add permission to a missing function", notFoundCode, 404, "Function not found: " + fnARN("doesnotexist"), true},
				func() *awserr.Error { return fail(h, lambdaapi.OpAddPermission, permission("doesnotexist", nil)) },
			},
			{
				golden{"add permission to a missing alias", notFoundCode, 404, "Cannot find alias arn: " + fnARN("fn") + ":alias-doesnotexist", true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpAddPermission, permission("fn", new("alias-doesnotexist")))
				},
			},
			{
				golden{"add permission to a missing version", notFoundCode, 404, "Function not found: " + fnARN("fn") + ":42", true},
				func() *awserr.Error { return fail(h, lambdaapi.OpAddPermission, permission("fn", new("42"))) },
			},
			{
				golden{"add permission to a qualifier no alias can have", notFoundCode, 404, "Function not found: " + fnARN("fn") + ":valid-with-$-but-doesnotexist", true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpAddPermission, permission("fn", new("valid-with-$-but-doesnotexist")))
				},
			},
			{
				golden{"remove permission from a missing function", notFoundCode, 404, "No policy found for: " + fnARN("doesnotexist"), true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpRemovePermission, &lambdaapi.RemovePermissionInput{FunctionName: "doesnotexist", StatementId: "s3"})
				},
			},
			{
				golden{"remove permission from a missing alias", notFoundCode, 404, "Cannot find alias arn: " + fnARN("fn") + ":alias-doesnotexist", true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpRemovePermission, &lambdaapi.RemovePermissionInput{FunctionName: "fn", StatementId: "s3", Qualifier: new("alias-doesnotexist")})
				},
			},
			{
				golden{"remove permission without a policy", notFoundCode, 404, "No policy is associated with the given resource.", true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpRemovePermission, &lambdaapi.RemovePermissionInput{FunctionName: "fn", StatementId: "s3"})
				},
			},
			{
				golden{"get policy of a missing function", notFoundCode, 404, missing, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpGetPolicy, &lambdaapi.GetPolicyInput{FunctionName: "doesnotexist"})
				},
			},
			{
				golden{"get policy of a missing version", notFoundCode, 404, missing, true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpGetPolicy, &lambdaapi.GetPolicyInput{FunctionName: "fn", Qualifier: new("77")})
				},
			},
			{
				golden{"put concurrency on a missing function", notFoundCode, 404, "Function not found: " + fnARN("doesnotexist") + ":$LATEST", true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpPutFunctionConcurrency, &lambdaapi.PutFunctionConcurrencyInput{FunctionName: "doesnotexist", ReservedConcurrentExecutions: 1})
				},
			},
			{
				golden{"get concurrency of a missing function", notFoundCode, 404, "Function not found: " + fnARN("doesnotexist"), true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpGetFunctionConcurrency, &lambdaapi.GetFunctionConcurrencyInput{FunctionName: "doesnotexist"})
				},
			},
			{
				golden{"delete a version of a missing function", notFoundCode, 404, "Function not found: " + fnARN("doesnotexist"), true},
				func() *awserr.Error {
					return fail(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "doesnotexist", Qualifier: new("1233")})
				},
			},
		}
		for _, row := range rows {
			row.check(t, row.call())
		}
	})
}

// TestPermissionValidationRows: a qualifier outside the model's pattern
// and a principal that is no account, IAM ARN or AWS service answer with
// the text AWS recorded (test_permission_exceptions).
func TestPermissionValidationRows(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		permission := func(qualifier *string, principal string) *lambdaapi.AddPermissionInput {
			return &lambdaapi.AddPermissionInput{FunctionName: "fn", Qualifier: qualifier, StatementId: "s", Action: "lambda:InvokeFunction", Principal: principal}
		}
		rows := []struct {
			golden
			in *lambdaapi.AddPermissionInput
		}{
			{
				golden{
					"qualifier with a character no version or alias has", "ValidationException", 400,
					`1 validation error detected: Value 'invalid-qualifier-with-?-char' at 'qualifier' failed to satisfy constraint: Member must satisfy regular expression pattern: \$(LATEST(\.PUBLISHED)?)|[a-zA-Z0-9-_$]+`,
					false,
				},
				permission(new("invalid-qualifier-with-?-char"), "s3.amazonaws.com"),
			},
			{
				golden{
					"principal outside every AWS service domain", "InvalidParameterValueException", 400,
					"The provided principal was invalid. Please check the principal and try again.",
					true,
				},
				permission(nil, "invalid.nonaws.com"),
			},
		}
		for _, row := range rows {
			row.check(t, fail(h, lambdaapi.OpAddPermission, row.in))
		}
		call(h, lambdaapi.OpAddPermission, permission(nil, "alexa-appkit.amazon.com"))
	})
}

// TestDeletingWhatIsGoneSucceeds: deleting a published version or an
// alias that does not exist answers 204, every time
// (test_delete_on_nonexisting_version, test_non_existent_alias_deletion).
func TestDeletingWhatIsGoneSucceeds(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		for range 2 {
			call(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn", Qualifier: new("1233")})
			call(h, lambdaapi.OpDeleteAlias, &lambdaapi.DeleteAliasInput{FunctionName: "fn", Name: "non-existent"})
		}
		call(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn"})
		errorIs(t, fail(h, lambdaapi.OpDeleteAlias, &lambdaapi.DeleteAliasInput{FunctionName: "fn", Name: "non-existent"}),
			"ResourceNotFoundException", "Function not found: "+fnARN("fn"))
	})
}

// TestDeletedAliasLeavesNothingBehind: an alias created again under a
// deleted alias's name has no function URL, policy or asynchronous
// invocation configuration of the old one.
func TestDeletedAliasLeavesNothingBehind(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		alias := &lambdaapi.CreateAliasInput{FunctionName: "fn", Name: "live", FunctionVersion: "1"}
		call(h, lambdaapi.OpCreateAlias, alias)
		call(h, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{FunctionName: "fn", Qualifier: new("live"), AuthType: lambdaapi.FunctionUrlAuthTypeNone})
		call(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
			FunctionName: "fn", Qualifier: new("live"), StatementId: "url", Action: "lambda:InvokeFunctionUrl", Principal: "*",
		})
		call(h, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", Qualifier: new("live"), MaximumRetryAttempts: new(int32(1))})

		call(h, lambdaapi.OpDeleteAlias, &lambdaapi.DeleteAliasInput{FunctionName: "fn", Name: "live"})
		call(h, lambdaapi.OpCreateAlias, alias)

		errorIs(t, fail(h, lambdaapi.OpGetFunctionUrlConfig, &lambdaapi.GetFunctionUrlConfigInput{FunctionName: "fn", Qualifier: new("live")}),
			"ResourceNotFoundException", "The resource you requested does not exist.")
		errorIs(t, fail(h, lambdaapi.OpGetPolicy, &lambdaapi.GetPolicyInput{FunctionName: "fn", Qualifier: new("live")}),
			"ResourceNotFoundException", "The resource you requested does not exist.")
		errorIs(t, fail(h, lambdaapi.OpGetFunctionEventInvokeConfig, &lambdaapi.GetFunctionEventInvokeConfigInput{FunctionName: "fn", Qualifier: new("live")}),
			"ResourceNotFoundException", "The function "+fnARN("fn")+":live doesn't have an EventInvokeConfig")
	})
}
