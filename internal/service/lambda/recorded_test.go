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

// TestURLConfigInvokeModeEcho replays LocalStack's AWS-validated
// test_url_config_lifecycle (lambda-events.md 7.1): Create and Update
// answer InvokeMode only once a request has set it, while Get reports the
// BUFFERED default.
func TestURLConfigInvokeModeEcho(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		created := call(h, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{FunctionName: "fn", AuthType: lambdaapi.FunctionUrlAuthTypeNone})
		if created.InvokeMode != nil {
			t.Errorf("CreateFunctionUrlConfig without InvokeMode answered InvokeMode %s; want it absent", *created.InvokeMode)
		}
		got := call(h, lambdaapi.OpGetFunctionUrlConfig, &lambdaapi.GetFunctionUrlConfigInput{FunctionName: "fn"})
		if got.InvokeMode == nil || *got.InvokeMode != lambdaapi.InvokeModeBuffered {
			t.Errorf("GetFunctionUrlConfig InvokeMode = %v; want BUFFERED", got.InvokeMode)
		}
		updated := call(h, lambdaapi.OpUpdateFunctionUrlConfig, &lambdaapi.UpdateFunctionUrlConfigInput{FunctionName: "fn", AuthType: new(lambdaapi.FunctionUrlAuthTypeAwsIam)})
		if updated.InvokeMode != nil {
			t.Errorf("UpdateFunctionUrlConfig of AuthType answered InvokeMode %s; want it absent", *updated.InvokeMode)
		}
		streamed := call(h, lambdaapi.OpUpdateFunctionUrlConfig, &lambdaapi.UpdateFunctionUrlConfigInput{FunctionName: "fn", InvokeMode: new(lambdaapi.InvokeModeResponseStream)})
		if streamed.InvokeMode == nil || *streamed.InvokeMode != lambdaapi.InvokeModeResponseStream {
			t.Errorf("UpdateFunctionUrlConfig of InvokeMode answered InvokeMode %v; want RESPONSE_STREAM", streamed.InvokeMode)
		}
	})
}

// TestPublishVersionAfterDescriptionChange: an update of $LATEST that
// changes only its Description is a change PublishVersion publishes, and
// the Description PublishVersion gave the last version is not one
// (lambda-api.md 7.1; LocalStack's AWS-validated provider publishes after
// any update of $LATEST).
func TestPublishVersionAfterDescriptionChange(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		v1 := call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn", Description: new("release one")})
		again := call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		if *again.Version != *v1.Version {
			t.Fatalf("a publish without changes after a publish with a Description = version %s; want %s again", *again.Version, *v1.Version)
		}
		call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Description: new("second")})
		v2 := call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		if *v2.Version != "2" || *v2.Description != "second" {
			t.Errorf("PublishVersion after a Description change = version %s, Description %q; want 2, second", *v2.Version, *v2.Description)
		}
	})
}

// TestPolicyRevisionIsItsTargets: a resource policy has the RevisionId of
// its target, $LATEST for the unqualified function, else the version or
// the alias; AddPermission and RemovePermission check against it and
// change it (test_lambda_permission_revisions,
// test_lambda_permission_fn_versioning).
func TestPolicyRevisionIsItsTargets(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		const stale = "The Revision Id provided does not match the latest Revision Id. Call the GetPolicy API to retrieve the latest Revision Id"
		revision := func(qualifier *string) string {
			h.Helper()
			return *call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn", Qualifier: qualifier}).Configuration.RevisionId
		}
		policyRevision := func(qualifier *string) string {
			h.Helper()
			return *call(h, lambdaapi.OpGetPolicy, &lambdaapi.GetPolicyInput{FunctionName: "fn", Qualifier: qualifier}).RevisionId
		}
		permission := func(sid string, qualifier, rev *string) *lambdaapi.AddPermissionInput {
			return &lambdaapi.AddPermissionInput{FunctionName: "fn", Qualifier: qualifier, StatementId: sid, Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", RevisionId: rev}
		}

		h.createFunction("fn")
		created := revision(nil)
		call(h, lambdaapi.OpAddPermission, permission("first", nil, new(created)))
		latest := revision(nil)
		if latest == created || policyRevision(nil) != latest {
			t.Errorf("after the first statement: function %s (was %s), policy %s; want a new function RevisionId that the policy shares", latest, created, policyRevision(nil))
		}
		errorIs(t, fail(h, lambdaapi.OpAddPermission, permission("second", nil, new(created))), "PreconditionFailedException", stale)

		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		if got, want := policyRevision(nil), revision(nil); got != want {
			t.Errorf("policy RevisionId after a publish = %s; want $LATEST's %s", got, want)
		}

		v1 := revision(new("1"))
		call(h, lambdaapi.OpAddPermission, permission("v1", new("1"), new(v1)))
		if got, want := policyRevision(new("1")), revision(new("1")); got != want || got == v1 {
			t.Errorf("version 1's policy RevisionId = %s, version's %s (was %s); want the version's new one", got, want, v1)
		}

		alias := call(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: "fn", Name: "live", FunctionVersion: "1"})
		call(h, lambdaapi.OpAddPermission, permission("live", new("live"), alias.RevisionId))
		aliasNow := *call(h, lambdaapi.OpGetAlias, &lambdaapi.GetAliasInput{FunctionName: "fn", Name: "live"}).RevisionId
		if got := policyRevision(new("live")); got != aliasNow || got == *alias.RevisionId {
			t.Errorf("alias policy RevisionId = %s, alias's %s (was %s); want the alias's new one", got, aliasNow, *alias.RevisionId)
		}

		before := revision(nil)
		errorIs(t, fail(h, lambdaapi.OpRemovePermission, &lambdaapi.RemovePermissionInput{FunctionName: "fn", StatementId: "first", RevisionId: new(created)}),
			"PreconditionFailedException", stale)
		call(h, lambdaapi.OpRemovePermission, &lambdaapi.RemovePermissionInput{FunctionName: "fn", StatementId: "first", RevisionId: new(before)})
		if revision(nil) == before {
			t.Error("RemovePermission left $LATEST's RevisionId as it was")
		}
	})
}

// TestPublishChangesLatestRevision: publishing gives $LATEST a new
// RevisionId, so one read before the publish is stale after it
// (test_function_revisions_version_and_alias).
func TestPublishChangesLatestRevision(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		rev := *h.createFunction("fn").RevisionId
		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		errorIs(t, fail(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Timeout: new(int32(5)), RevisionId: new(rev)}),
			"PreconditionFailedException", "The Revision Id provided does not match the latest Revision Id. Call the GetFunction/GetAlias API to retrieve the latest Revision Id")
	})
}

// TestSettlingChangesRevision: under TransitionsClock, a function becoming
// Active and an update completing each give $LATEST a new RevisionId
// (test_function_revisions_basic).
func TestSettlingChangesRevision(t *testing.T) {
	run(t, lambda.Options{StateTransitions: lambda.TransitionsClock}, func(h *harness) {
		created := *h.createFunction("fn").RevisionId
		h.passSettleDelay()
		active := *call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"}).Configuration.RevisionId
		if active == created {
			t.Errorf("RevisionId after becoming Active = %s; want a new one", active)
		}
		updating := *call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Timeout: new(int32(5))}).RevisionId
		h.passSettleDelay()
		if updated := *call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"}).Configuration.RevisionId; updated == updating {
			t.Errorf("RevisionId after the update completed = %s; want a new one", updated)
		}
	})
}

// TestCreateFunctionPublishAnswersTheUnqualifiedARN: CreateFunction with
// Publish reports version 1 under the unqualified function ARN
// (test_publish_version_on_create).
func TestCreateFunctionPublishAnswersTheUnqualifiedARN(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		in := zipFunction(t, "fn")
		in.Publish = true
		out := call(h, lambdaapi.OpCreateFunction, in)
		if *out.FunctionArn != fnARN("fn") || *out.Version != "1" {
			t.Errorf("CreateFunction with Publish = FunctionArn %s, Version %s; want %s, 1", *out.FunctionArn, *out.Version, fnARN("fn"))
		}
	})
}

// TestDeleteLatestIsRefusedBeforeTheLookup: DeleteFunction of $LATEST is
// InvalidParameterValueException even for a function that does not exist
// (test_ops_with_arn_qualifier_mismatch[delete_function]).
func TestDeleteLatestIsRefusedBeforeTheLookup(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		errorIs(t, fail(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: fnARN("missing") + ":$LATEST", Qualifier: new("$LATEST")}),
			"InvalidParameterValueException", "$LATEST version cannot be deleted without deleting the function.")
	})
}

// TestDeleteAliasQualifierIsRefusedBeforeTheLookup: DeleteFunction of an
// alias, by Qualifier or in the ARN, is InvalidParameterValueException
// even for a function that does not exist; a qualifier of no alias's form
// is a missing version
// (test_function_name_and_qualifier_validation[*-delete_function]).
func TestDeleteAliasQualifierIsRefusedBeforeTheLookup(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		const refused = "Deletion of aliases is not currently supported."
		for _, in := range []*lambdaapi.DeleteFunctionInput{
			{FunctionName: "missing", Qualifier: new("prod")},
			{FunctionName: fnARN("missing") + ":prod"},
		} {
			golden{"delete " + in.FunctionName + " qualified by an alias", "InvalidParameterValueException", 400, refused, true}.check(t, fail(h, lambdaapi.OpDeleteFunction, in))
		}
		golden{"delete $latest of a missing function", "ResourceNotFoundException", 404, "Function not found: " + fnARN("missing"), true}.
			check(t, fail(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "missing", Qualifier: new("$latest")}))
		h.createFunction("fn")
		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		call(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: "fn", Name: "live", FunctionVersion: "1"})
		golden{"delete an existing alias", "InvalidParameterValueException", 400, refused, true}.
			check(t, fail(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn", Qualifier: new("live")}))
		call(h, lambdaapi.OpGetAlias, &lambdaapi.GetAliasInput{FunctionName: "fn", Name: "live"})
	})
}

// TestAccountSettingsReportTheCodeStorageQuota: TotalCodeSize is the 75 GB
// default AWS reports (get-account-settings).
func TestAccountSettingsReportTheCodeStorageQuota(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		out := call(h, lambdaapi.OpGetAccountSettings, &lambdaapi.GetAccountSettingsInput{})
		if got := out.AccountLimit.TotalCodeSize; got != 80530636800 {
			t.Errorf("TotalCodeSize = %d; want 80530636800", got)
		}
	})
}

// TestEmptyEnvironmentAtCreateIsEchoed: Environment {"Variables":{}} sent
// to CreateFunction reads back as such, from Create and from GetFunction
// (test_function_revisions_basic).
func TestEmptyEnvironmentAtCreateIsEchoed(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		in := zipFunction(t, "fn")
		in.Environment = &lambdaapi.Environment{Variables: map[string]string{}}
		created := call(h, lambdaapi.OpCreateFunction, in)
		got := call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"}).Configuration
		for name, c := range map[string]*lambdaapi.FunctionConfiguration{"CreateFunction": created, "GetFunction": got} {
			if c.Environment == nil || c.Environment.Variables == nil || len(c.Environment.Variables) != 0 {
				t.Errorf("%s Environment = %+v; want Variables {}", name, c.Environment)
			}
		}
	})
}
