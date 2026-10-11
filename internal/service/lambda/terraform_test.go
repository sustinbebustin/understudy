package lambda_test

import (
	"crypto/sha256"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// functionRead is what Terraform's aws_lambda_function read calls return
// (tf-compute.md 2.1): GetFunction, ListVersionsByFunction and
// GetFunctionCodeSigningConfig.
type functionRead struct {
	get      *lambdaapi.GetFunctionOutput
	versions *lambdaapi.ListVersionsByFunctionOutput
	signing  *lambdaapi.GetFunctionCodeSigningConfigOutput
}

func (h *harness) readFunction(name string) functionRead {
	h.Helper()
	return functionRead{
		get:      call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: name}),
		versions: call(h, lambdaapi.OpListVersionsByFunction, &lambdaapi.ListVersionsByFunctionInput{FunctionName: name, MaxItems: new(int32(10000))}),
		signing:  call(h, lambdaapi.OpGetFunctionCodeSigningConfig, &lambdaapi.GetFunctionCodeSigningConfigInput{FunctionName: name}),
	}
}

// stable reads the function twice, as the read after apply and the second
// plan do, and fails unless both reads are equal: a read that differs
// between two plans is a perpetual diff.
func (h *harness) stable(name string) functionRead {
	h.Helper()
	first, second := h.readFunction(name), h.readFunction(name)
	if !reflect.DeepEqual(first, second) {
		h.Fatalf("two reads of %s differ:\n%+v\n%+v", name, first.get.Configuration, second.get.Configuration)
	}
	return first
}

// TestTerraformFunctionLifecycle replays aws_lambda_function's create,
// read, update and delete (tf-compute.md 2.1, tf-remaining.md 4.4) on a
// frozen clock: every state is final when the call returns, so no waiter
// needs the clock to move.
func TestTerraformFunctionLifecycle(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		in := zipFunction(t, "fn")
		created := call(h, lambdaapi.OpCreateFunction, in)
		sum := sha256.Sum256(in.Code.ZipFile)
		if *created.CodeSha256 != base64.StdEncoding.EncodeToString(sum[:]) || created.CodeSize != int64(len(in.Code.ZipFile)) {
			t.Errorf("CodeSha256, CodeSize = %s, %d; want the base64 SHA-256 and length of the zip", *created.CodeSha256, created.CodeSize)
		}
		if *created.FunctionArn != "arn:aws:lambda:us-east-1:000000000000:function:fn" || *created.Version != "$LATEST" {
			t.Errorf("FunctionArn, Version = %s, %s", *created.FunctionArn, *created.Version)
		}
		call(h, lambdaapi.OpPutFunctionConcurrency, &lambdaapi.PutFunctionConcurrencyInput{FunctionName: "fn", ReservedConcurrentExecutions: 0})

		r := h.stable("fn")
		c := r.get.Configuration
		switch {
		case *c.State != lambdaapi.StateActive || *c.LastUpdateStatus != lambdaapi.LastUpdateStatusSuccessful || c.StateReason != nil:
			t.Errorf("State, LastUpdateStatus, StateReason = %s, %s, %v; want Active and Successful without a reason", *c.State, *c.LastUpdateStatus, c.StateReason)
		case c.Environment != nil || c.ImageConfigResponse != nil || c.VpcConfig != nil || c.DeadLetterConfig != nil || c.KMSKeyArn != nil:
			t.Errorf("unset blocks are reported: %+v", c)
		case *c.Description != "" || *c.Timeout != 3 || *c.MemorySize != 128 || *c.PackageType != lambdaapi.PackageTypeZip:
			t.Errorf("Description, Timeout, MemorySize, PackageType = %q, %d, %d, %s", *c.Description, *c.Timeout, *c.MemorySize, *c.PackageType)
		case *c.TracingConfig.Mode != lambdaapi.TracingModePassThrough || c.EphemeralStorage.Size != 512 || c.Architectures[0] != lambdaapi.ArchitectureX8664:
			t.Errorf("defaults = %+v", c)
		case *c.LoggingConfig.LogFormat != lambdaapi.LogFormatText || *c.LoggingConfig.LogGroup != "/aws/lambda/fn":
			t.Errorf("LoggingConfig = %+v", c.LoggingConfig)
		case r.get.Code == nil || *r.get.Code.RepositoryType != "S3":
			t.Errorf("Code = %+v", r.get.Code)
		case r.get.Concurrency == nil || *r.get.Concurrency.ReservedConcurrentExecutions != 0:
			t.Errorf("Concurrency = %+v; reserved 0 must be reported, or Terraform reads -1", r.get.Concurrency)
		case len(r.versions.Versions) != 1 || *r.versions.Versions[0].Version != "$LATEST" || r.versions.Versions[0].State != nil:
			t.Errorf("ListVersionsByFunction = %+v; want only $LATEST, without State", r.versions.Versions)
		case r.signing.FunctionName != "fn" || r.signing.CodeSigningConfigArn != "":
			t.Errorf("GetFunctionCodeSigningConfig = %+v", r.signing)
		}

		updated := call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{
			FunctionName: "fn", Timeout: new(int32(10)), Environment: &lambdaapi.Environment{Variables: map[string]string{"STAGE": "1"}},
		})
		if *updated.LastUpdateStatus != lambdaapi.LastUpdateStatusSuccessful || updated.Environment.Variables["STAGE"] != "1" || *updated.RevisionId == *created.RevisionId {
			t.Errorf("UpdateFunctionConfiguration = %+v", updated)
		}
		cleared := call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{
			FunctionName: "fn", Environment: &lambdaapi.Environment{Variables: map[string]string{}},
		})
		if cleared.Environment != nil {
			t.Errorf("Environment after Variables {} = %+v; want none", cleared.Environment)
		}
		code := call(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ZipFile: zipOf(t, "v2")})
		if *code.LastUpdateStatus != lambdaapi.LastUpdateStatusSuccessful || *code.CodeSha256 == *created.CodeSha256 {
			t.Errorf("UpdateFunctionCode = %+v", code)
		}
		call(h, lambdaapi.OpDeleteFunctionConcurrency, &lambdaapi.DeleteFunctionConcurrencyInput{FunctionName: "fn"})
		published := call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		waited := call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: *published.FunctionArn, Qualifier: published.Version})
		if *waited.Version != "1" || *waited.LastUpdateStatus != lambdaapi.LastUpdateStatusSuccessful || *waited.FunctionArn != "arn:aws:lambda:us-east-1:000000000000:function:fn:1" {
			t.Errorf("published version = %s %s %s", *waited.Version, *waited.LastUpdateStatus, *waited.FunctionArn)
		}
		r = h.stable("fn")
		if last := r.versions.Versions[len(r.versions.Versions)-1]; *last.Version != "1" || r.get.Concurrency != nil {
			t.Errorf("last version, Concurrency = %s, %+v; want 1 and none", *last.Version, r.get.Concurrency)
		}
		again := call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		if *again.Version != "1" || *again.RevisionId != *published.RevisionId {
			t.Errorf("a publish without changes = version %s; want version 1 again", *again.Version)
		}

		call(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn"})
		errorIs(t, fail(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"}),
			"ResourceNotFoundException", "Function not found: arn:aws:lambda:us-east-1:000000000000:function:fn")
	})
}

// TestTerraformPermission replays aws_lambda_permission (tf-compute.md
// 2.2): the statement GetPolicy returns is the one AddPermission returned,
// with scalar Action and Resource and the request's conditions only.
func TestTerraformPermission(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		added := call(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
			FunctionName: "fn", StatementId: "sns", Action: "lambda:InvokeFunction", Principal: "sns.amazonaws.com",
			SourceArn: new("arn:aws:sns:us-east-1:000000000000:t"), SourceAccount: new("000000000000"),
		})
		const statement = `{"Sid":"sns","Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"lambda:InvokeFunction",` +
			`"Resource":"arn:aws:lambda:us-east-1:000000000000:function:fn",` +
			`"Condition":{"ArnLike":{"AWS:SourceArn":"arn:aws:sns:us-east-1:000000000000:t"},"StringEquals":{"AWS:SourceAccount":"000000000000"}}}`
		if *added.Statement != statement {
			t.Fatalf("Statement = %s\nwant        %s", *added.Statement, statement)
		}
		account := call(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{FunctionName: "fn", StatementId: "acct", Action: "lambda:InvokeFunction", Principal: "111111111111"})
		if !strings.Contains(*account.Statement, `"Principal":{"AWS":"arn:aws:iam::111111111111:root"}`) || strings.Contains(*account.Statement, "Condition") {
			t.Errorf("account principal statement = %s", *account.Statement)
		}
		policy := call(h, lambdaapi.OpGetPolicy, &lambdaapi.GetPolicyInput{FunctionName: "fn"})
		if want := `{"Version":"2012-10-17","Id":"default","Statement":[` + statement + "," + *account.Statement + `]}`; *policy.Policy != want {
			t.Errorf("Policy = %s\nwant     %s", *policy.Policy, want)
		}
		errorIs(t, fail(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{FunctionName: "fn", StatementId: "sns", Action: "lambda:InvokeFunction", Principal: "*"}),
			"ResourceConflictException", "The statement id (sns) provided already exists. Please provide a new statement id, or remove the existing statement.")
		call(h, lambdaapi.OpRemovePermission, &lambdaapi.RemovePermissionInput{FunctionName: "fn", StatementId: "sns"})
		call(h, lambdaapi.OpRemovePermission, &lambdaapi.RemovePermissionInput{FunctionName: "fn", StatementId: "acct"})
		errorIs(t, fail(h, lambdaapi.OpGetPolicy, &lambdaapi.GetPolicyInput{FunctionName: "fn"}),
			"ResourceNotFoundException", "The resource you requested does not exist.")
	})
}

// TestTerraformEventSourceMapping replays aws_lambda_event_source_mapping
// (tf-compute.md 2.3): Enabled when Create returns, unset blocks absent,
// removal by empty objects, tags by the mapping's ARN, gone after delete.
func TestTerraformEventSourceMapping(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		queue := h.queue("q")
		created := call(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{
			EventSourceArn: new(queue), FunctionName: "fn", Enabled: new(true), BatchSize: new(int32(10)), MaximumBatchingWindowInSeconds: new(int32(5)),
			FunctionResponseTypes: []lambdaapi.FunctionResponseType{lambdaapi.FunctionResponseTypeReportBatchItemFailures},
			ScalingConfig:         &lambdaapi.ScalingConfig{MaximumConcurrency: new(int32(2))},
			FilterCriteria:        &lambdaapi.FilterCriteria{Filters: []lambdaapi.Filter{{Pattern: new(`{"body":{"kind":["a"]}}`)}}},
			Tags:                  map[string]string{"team": "x"},
		})
		if *created.State != "Enabled" || *created.FunctionArn != "arn:aws:lambda:us-east-1:000000000000:function:fn" || *created.StateTransitionReason != "USER_INITIATED" {
			t.Errorf("created = %s %s %s", *created.State, *created.FunctionArn, *created.StateTransitionReason)
		}
		got := call(h, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *created.UUID})
		if *got.FilterCriteria.Filters[0].Pattern != `{"body":{"kind":["a"]}}` || *got.ScalingConfig.MaximumConcurrency != 2 || got.FunctionResponseTypes[0] != lambdaapi.FunctionResponseTypeReportBatchItemFailures {
			t.Errorf("GetEventSourceMapping = %+v", got)
		}
		tags := call(h, lambdaapi.OpListTags, &lambdaapi.ListTagsInput{Resource: *created.EventSourceMappingArn})
		if tags.Tags["team"] != "x" {
			t.Errorf("ListTags(mapping ARN) = %v", tags.Tags)
		}
		updated := call(h, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID: *created.UUID, FilterCriteria: &lambdaapi.FilterCriteria{}, ScalingConfig: &lambdaapi.ScalingConfig{},
		})
		if *updated.State != "Enabled" || updated.FilterCriteria != nil || updated.ScalingConfig != nil {
			t.Errorf("after clearing = %s %+v %+v; want Enabled without FilterCriteria or ScalingConfig", *updated.State, updated.FilterCriteria, updated.ScalingConfig)
		}
		plain := call(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{EventSourceArn: new(h.queue("plain")), FunctionName: "fn", Enabled: new(false)})
		if *plain.State != "Disabled" || *plain.BatchSize != 10 || *plain.MaximumBatchingWindowInSeconds != 0 || plain.FunctionResponseTypes == nil || len(plain.FunctionResponseTypes) != 0 {
			t.Errorf("default mapping = %+v", plain)
		}
		deleted := call(h, lambdaapi.OpDeleteEventSourceMapping, &lambdaapi.DeleteEventSourceMappingInput{UUID: *created.UUID})
		if *deleted.State != "Deleting" {
			t.Errorf("DeleteEventSourceMapping State = %s, want Deleting", *deleted.State)
		}
		errorIs(t, fail(h, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *created.UUID}),
			"ResourceNotFoundException", "The resource you requested does not exist.")
	})
}

// TestTerraformFunctionURL replays aws_lambda_function_url (tf-compute.md
// 2.4): the two AddPermission calls for AuthType NONE, the duplicate text
// the provider tolerates, a URL readable at once, and Cors {} clearing.
func TestTerraformFunctionURL(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		created := call(h, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{
			FunctionName: "fn", AuthType: lambdaapi.FunctionUrlAuthTypeNone, InvokeMode: new(lambdaapi.InvokeModeBuffered),
			Cors: &lambdaapi.Cors{AllowOrigins: []string{"*"}},
		})
		id, _, _ := strings.Cut(strings.TrimPrefix(created.FunctionUrl, "http://"), ".")
		if len(id) != 32 || !strings.HasSuffix(created.FunctionUrl, ".lambda-url.us-east-1.localhost/") && !strings.Contains(created.FunctionUrl, ".lambda-url.us-east-1.localhost:") {
			t.Errorf("FunctionUrl = %s", created.FunctionUrl)
		}
		for _, p := range []lambdaapi.AddPermissionInput{
			{FunctionName: "fn", StatementId: "FunctionURLAllowPublicAccess", Action: "lambda:InvokeFunctionUrl", Principal: "*", FunctionUrlAuthType: new(lambdaapi.FunctionUrlAuthTypeNone)},
			{FunctionName: "fn", StatementId: "FunctionURLAllowInvokeAction", Action: "lambda:InvokeFunction", Principal: "*", InvokedViaFunctionUrl: new(true)},
		} {
			call(h, lambdaapi.OpAddPermission, &p)
			err := fail(h, lambdaapi.OpAddPermission, &p)
			if !strings.Contains(err.Message, "The statement id ("+p.StatementId+") provided already exists") {
				t.Errorf("duplicate %s = %s", p.StatementId, err.Message)
			}
		}
		got := call(h, lambdaapi.OpGetFunctionUrlConfig, &lambdaapi.GetFunctionUrlConfigInput{FunctionName: "fn"})
		if got.FunctionUrl != created.FunctionUrl || *got.InvokeMode != lambdaapi.InvokeModeBuffered || got.Cors == nil {
			t.Errorf("GetFunctionUrlConfig = %+v", got)
		}
		updated := call(h, lambdaapi.OpUpdateFunctionUrlConfig, &lambdaapi.UpdateFunctionUrlConfigInput{FunctionName: "fn", Cors: &lambdaapi.Cors{}})
		if updated.Cors != nil || updated.FunctionUrl != created.FunctionUrl {
			t.Errorf("after Cors {} = %+v", updated)
		}
		errorIs(t, fail(h, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{FunctionName: "fn", AuthType: lambdaapi.FunctionUrlAuthTypeNone}),
			"ResourceConflictException", "Failed to create function url config for [functionArn = arn:aws:lambda:us-east-1:000000000000:function:fn]. Error message:  FunctionUrlConfig exists for this Lambda function")
		call(h, lambdaapi.OpDeleteFunctionUrlConfig, &lambdaapi.DeleteFunctionUrlConfigInput{FunctionName: "fn"})
		errorIs(t, fail(h, lambdaapi.OpGetFunctionUrlConfig, &lambdaapi.GetFunctionUrlConfigInput{FunctionName: "fn"}),
			"ResourceNotFoundException", "The resource you requested does not exist.")
	})
}

// TestTerraformAlias replays aws_lambda_alias (tf-remaining.md 4.1): the
// RoutingConfig {} Terraform always sends reads back as absent.
func TestTerraformAlias(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		created := call(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{
			FunctionName: "fn", Name: "live", FunctionVersion: "1", Description: new("live"), RoutingConfig: &lambdaapi.AliasRoutingConfiguration{},
		})
		if *created.AliasArn != "arn:aws:lambda:us-east-1:000000000000:function:fn:live" || created.RoutingConfig != nil {
			t.Errorf("CreateAlias = %+v", created)
		}
		call(h, lambdaapi.OpUpdateAlias, &lambdaapi.UpdateAliasInput{FunctionName: "fn", Name: "live", FunctionVersion: new("1"), Description: new("live two"), RoutingConfig: &lambdaapi.AliasRoutingConfiguration{}})
		got := call(h, lambdaapi.OpGetAlias, &lambdaapi.GetAliasInput{FunctionName: "fn", Name: "live"})
		if *got.Description != "live two" || got.RoutingConfig != nil {
			t.Errorf("GetAlias = %+v", got)
		}
		call(h, lambdaapi.OpDeleteAlias, &lambdaapi.DeleteAliasInput{FunctionName: "fn", Name: "live"})
		errorIs(t, fail(h, lambdaapi.OpGetAlias, &lambdaapi.GetAliasInput{FunctionName: "fn", Name: "live"}),
			"ResourceNotFoundException", "Cannot find alias arn: arn:aws:lambda:us-east-1:000000000000:function:fn:live")
	})
}

// TestTerraformEventInvokeConfig: configurations are keyed by qualifier,
// unset limits are left out and both destination sides are always present
// (tf-remaining.md 4.2).
func TestTerraformEventInvokeConfig(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		call(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: "fn", Name: "live", FunctionVersion: "1"})
		put := call(h, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", MaximumRetryAttempts: new(int32(2))})
		if *put.FunctionArn != "arn:aws:lambda:us-east-1:000000000000:function:fn:$LATEST" || put.MaximumEventAgeInSeconds != nil ||
			put.DestinationConfig.OnSuccess == nil || put.DestinationConfig.OnFailure == nil {
			t.Errorf("PutFunctionEventInvokeConfig = %+v", put)
		}
		call(h, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", Qualifier: new("live"), MaximumRetryAttempts: new(int32(0)), MaximumEventAgeInSeconds: new(int32(300))})
		got := call(h, lambdaapi.OpGetFunctionEventInvokeConfig, &lambdaapi.GetFunctionEventInvokeConfigInput{FunctionName: "fn"})
		if *got.MaximumRetryAttempts != 2 || got.MaximumEventAgeInSeconds != nil {
			t.Errorf("the alias configuration overwrote the function's: %+v", got)
		}
		errorIs(t, fail(h, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn"}),
			"InvalidParameterValueException", "You must specify at least one of error handling or destination setting.")
		call(h, lambdaapi.OpDeleteFunctionEventInvokeConfig, &lambdaapi.DeleteFunctionEventInvokeConfigInput{FunctionName: "fn"})
		errorIs(t, fail(h, lambdaapi.OpGetFunctionEventInvokeConfig, &lambdaapi.GetFunctionEventInvokeConfigInput{FunctionName: "fn"}),
			"ResourceNotFoundException", "The function arn:aws:lambda:us-east-1:000000000000:function:fn:$LATEST doesn't have an EventInvokeConfig")
	})
}

// TestClockTransitions: with lambda.stateTransitions clock, Pending and
// InProgress are observable until the clock moves past the settle delay,
// and the calls that land inside the window get ResourceConflictException
// (lambda-api.md 5.2) or ResourceInUseException for a mapping.
func TestClockTransitions(t *testing.T) {
	run(t, lambda.Options{StateTransitions: lambda.TransitionsClock}, func(h *harness) {
		created := h.createFunction("fn")
		if *created.State != lambdaapi.StatePending || *created.StateReasonCode != lambdaapi.StateReasonCodeCreating || created.LastUpdateStatus != nil {
			t.Fatalf("created = %s %v %v; want Pending, Creating and no LastUpdateStatus", *created.State, created.StateReasonCode, created.LastUpdateStatus)
		}
		errorIs(t, fail(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Timeout: new(int32(5))}),
			"ResourceConflictException", "The operation cannot be performed at this time. The function is currently in the following state: Pending")
		h.passSettleDelay()
		active := call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"}).Configuration
		if *active.State != lambdaapi.StateActive || *active.LastUpdateStatus != lambdaapi.LastUpdateStatusSuccessful {
			t.Fatalf("after the settle delay = %s %s", *active.State, *active.LastUpdateStatus)
		}
		updating := call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Timeout: new(int32(5))})
		if *updating.LastUpdateStatus != lambdaapi.LastUpdateStatusInProgress || *updating.LastUpdateStatusReason != "The function is being created." {
			t.Errorf("update = %s %v", *updating.LastUpdateStatus, updating.LastUpdateStatusReason)
		}
		errorIs(t, fail(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ZipFile: zipOf(t, "v2")}),
			"ResourceConflictException", "The operation cannot be performed at this time. An update is in progress for resource: arn:aws:lambda:us-east-1:000000000000:function:fn")
		h.passSettleDelay()

		mapping := call(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{FunctionName: "fn", EventSourceArn: new(h.queue("q"))})
		if *mapping.State != "Creating" {
			t.Errorf("mapping State = %s, want Creating", *mapping.State)
		}
		errorIs(t, fail(h, lambdaapi.OpDeleteEventSourceMapping, &lambdaapi.DeleteEventSourceMappingInput{UUID: *mapping.UUID}),
			"ResourceInUseException", "The event source mapping "+*mapping.UUID+" is Creating; retry once it is Enabled or Disabled.")
		h.passSettleDelay()
		if got := call(h, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *mapping.UUID}); *got.State != "Enabled" {
			t.Errorf("mapping after the settle delay = %s, want Enabled", *got.State)
		}
		if deleted := call(h, lambdaapi.OpDeleteEventSourceMapping, &lambdaapi.DeleteEventSourceMappingInput{UUID: *mapping.UUID}); *deleted.State != "Deleting" {
			t.Errorf("delete = %s", *deleted.State)
		}
		call(h, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *mapping.UUID})
		h.passSettleDelay()
		fail(h, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *mapping.UUID})
	})
}

// passSettleDelay moves the emulator clock past the 10 ms settle delay of
// lambda.stateTransitions: clock and waits for the timers it fires.
func (h *harness) passSettleDelay() {
	h.Helper()
	if _, err := h.Clock.Advance(10 * time.Millisecond); err != nil {
		h.Fatal(err)
	}
	h.settle()
}
