package gosdk_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Research: docs/research/phase1/lambda-api.md, lambda-events.md,
// lambda-runtime-contract.md, error-matrix.md 5.6.

// handlerSource is the Python module every test function runs. Each
// handler is one behavior a test needs.
const handlerSource = `import json, os, time
import boto3

def echo(event, context):
    return event

def fail(event, context):
    raise Exception("Handler fails")

def sleep(event, context):
    time.sleep(3)
    return {"slept": True}

def env(event, context):
    return {k: os.environ.get(k) for k in ("FOO", "AWS_LAMBDA_FUNCTION_NAME", "AWS_REGION", "AWS_LAMBDA_FUNCTION_VERSION")}

def forward(event, context):
    boto3.client("sqs").send_message(QueueUrl=os.environ["FORWARD_QUEUE_URL"], MessageBody=json.dumps(event))
    records = event.get("Records", []) if isinstance(event, dict) else []
    return {"batchItemFailures": [{"itemIdentifier": r["messageId"]} for r in records if "fail" in r.get("body", "")]}

def url(event, context):
    return {
        "statusCode": 201,
        "headers": {"content-type": "application/json", "x-conformance": "yes"},
        "body": json.dumps({
            "version": event.get("version"),
            "rawPath": event.get("rawPath"),
            "rawQueryString": event.get("rawQueryString"),
            "method": event["requestContext"]["http"]["method"],
            "body": event.get("body"),
        }),
    }
`

// functionZip returns a deployment package holding handler.py, with
// version varying its content so code updates change CodeSha256.
func functionZip(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("handler.py")
	ok(t, err, "zip Create")
	_, err = io.WriteString(f, handlerSource+"\nVERSION = "+fmt.Sprintf("%q", version)+"\n")
	ok(t, err, "zip Write")
	ok(t, w.Close(), "zip Close")
	return buf.Bytes()
}

// deployFunction creates a python3.13 function running handler, waits
// until it is Active, and deletes it and its role when the test ends.
func deployFunction(t *testing.T, fnName, handler string, env map[string]string) *lambda.GetFunctionConfigurationOutput {
	t.Helper()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	role := createRole(t, fnName+"-role", lambdaTrustPolicy)
	in := &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName), Role: role.Arn, Runtime: types.RuntimePython313, Handler: aws.String(handler),
		Code: &types.FunctionCode{ZipFile: functionZip(t, "1")}, Timeout: aws.Int32(5),
	}
	if env != nil {
		in.Environment = &types.Environment{Variables: env}
	}
	_, err := client.CreateFunction(ctx, in)
	ok(t, err, "CreateFunction "+fnName)
	t.Cleanup(func() {
		_, _ = client.DeleteFunction(context.Background(), &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})
	err = lambda.NewFunctionActiveV2Waiter(client).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)}, 2*time.Minute)
	ok(t, err, "waiting for "+fnName+" to be Active")
	out, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(fnName)})
	ok(t, err, "GetFunctionConfiguration")
	return out
}

func TestLambdaFunctionLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	acct := account(t)
	fnName := name("fn")
	role := createRole(t, fnName+"-role", lambdaTrustPolicy)
	code := functionZip(t, "1")
	sum := sha256.Sum256(code)
	fnArn := "arn:aws:lambda:" + region() + ":" + acct + ":function:" + fnName

	created, err := client.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName), Role: role.Arn, Runtime: types.RuntimePython313, Handler: aws.String("handler.env"),
		Code: &types.FunctionCode{ZipFile: code}, Description: aws.String("conformance"),
		Environment: &types.Environment{Variables: map[string]string{"FOO": "bar"}},
		Tags:        map[string]string{"team": "platform"},
	})
	ok(t, err, "CreateFunction")
	t.Cleanup(func() {
		_, _ = client.DeleteFunction(context.Background(), &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})

	t.Run("CreateFunction response", func(t *testing.T) {
		c := created
		if aws.ToString(c.FunctionArn) != fnArn || aws.ToString(c.FunctionName) != fnName || aws.ToString(c.Version) != "$LATEST" {
			t.Errorf("FunctionArn %q, FunctionName %q, Version %q", aws.ToString(c.FunctionArn), aws.ToString(c.FunctionName), aws.ToString(c.Version))
		}
		if aws.ToString(c.CodeSha256) != base64.StdEncoding.EncodeToString(sum[:]) || c.CodeSize != int64(len(code)) {
			t.Errorf("CodeSha256 %q, CodeSize %d; want the base64 SHA-256 and the size of the zip", aws.ToString(c.CodeSha256), c.CodeSize)
		}
		if !slices.Equal(c.Architectures, []types.Architecture{types.ArchitectureX8664}) || c.PackageType != types.PackageTypeZip ||
			aws.ToInt32(c.Timeout) != 3 || aws.ToInt32(c.MemorySize) != 128 || aws.ToInt32(c.EphemeralStorage.Size) != 512 ||
			c.TracingConfig == nil || c.TracingConfig.Mode != types.TracingModePassThrough {
			t.Errorf("defaults: Architectures %v, PackageType %s, Timeout %d, MemorySize %d, EphemeralStorage %+v, TracingConfig %+v",
				c.Architectures, c.PackageType, aws.ToInt32(c.Timeout), aws.ToInt32(c.MemorySize), c.EphemeralStorage, c.TracingConfig)
		}
		if c.LoggingConfig == nil || c.LoggingConfig.LogFormat != types.LogFormatText || aws.ToString(c.LoggingConfig.LogGroup) != "/aws/lambda/"+fnName {
			t.Errorf("LoggingConfig = %+v, want Text format and log group /aws/lambda/%s", c.LoggingConfig, fnName)
		}
		if !uuidPattern.MatchString(aws.ToString(c.RevisionId)) {
			t.Errorf("RevisionId = %q, want a UUID", aws.ToString(c.RevisionId))
		}
		if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+\+0000$`).MatchString(aws.ToString(c.LastModified)) {
			t.Errorf("LastModified = %q, want the YYYY-MM-DDThh:mm:ss.sss+0000 form", aws.ToString(c.LastModified))
		}
		// A new function is Pending while Lambda provisions it.
		if c.State != types.StatePending || aws.ToString(c.StateReason) != "The function is being created." || c.StateReasonCode != types.StateReasonCodeCreating {
			t.Errorf("CreateFunction State %q, StateReason %q, StateReasonCode %q; want Pending, \"The function is being created.\", Creating",
				c.State, aws.ToString(c.StateReason), c.StateReasonCode)
		}
	})

	err = lambda.NewFunctionActiveV2Waiter(client).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)}, 2*time.Minute)
	ok(t, err, "FunctionActiveV2 waiter")

	t.Run("CreateFunction errors", func(t *testing.T) {
		_, err := client.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName: aws.String(fnName), Role: role.Arn, Runtime: types.RuntimePython313,
			Handler: aws.String("handler.echo"), Code: &types.FunctionCode{ZipFile: code},
		})
		errAs[*types.ResourceConflictException](t, err, "CreateFunction of an existing name")
		wantStatus(t, err, "CreateFunction of an existing name", 409)
		wantMessage(t, err, "CreateFunction of an existing name", "Function already exist: "+fnName)

		_, err = client.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName: aws.String(name("badrole")), Role: aws.String("r1"), Runtime: types.RuntimePython313,
			Handler: aws.String("handler.echo"), Code: &types.FunctionCode{ZipFile: code},
		})
		apiErr(t, err, "CreateFunction with role r1", "ValidationException")
		wantStatus(t, err, "CreateFunction with role r1", 400)
		wantMessageContains(t, err, "CreateFunction with role r1", "Value 'r1' at 'role' failed to satisfy constraint: Member must satisfy regular expression pattern: arn:(aws[a-zA-Z-]*)?:iam::\\d{12}:role/?[a-zA-Z_0-9+=,.@\\-_/]+")

		_, err = client.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName: aws.String(name("old")), Role: role.Arn, Runtime: types.RuntimePython37,
			Handler: aws.String("handler.echo"), Code: &types.FunctionCode{ZipFile: code},
		})
		errAs[*types.InvalidParameterValueException](t, err, "CreateFunction with python3.7")
		wantMessage(t, err, "CreateFunction with python3.7", "The runtime parameter of python3.7 is no longer supported for creating or updating AWS Lambda functions. We recommend you use a supported runtime while creating or updating functions.")

		_, err = client.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName: aws.String(name("badzip")), Role: role.Arn, Runtime: types.RuntimePython313,
			Handler: aws.String("handler.echo"), Code: &types.FunctionCode{ZipFile: []byte("not a zip file")},
		})
		errAs[*types.InvalidParameterValueException](t, err, "CreateFunction with a corrupt zip")
		wantMessage(t, err, "CreateFunction with a corrupt zip", "Could not unzip uploaded file. Please check your file, then try to upload again.")
	})

	t.Run("reads", func(t *testing.T) {
		got, err := client.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
		ok(t, err, "GetFunction")
		c := got.Configuration
		if c.State != types.StateActive || c.LastUpdateStatus != types.LastUpdateStatusSuccessful || c.StateReason != nil {
			t.Errorf("active function State %q, LastUpdateStatus %q, StateReason %v", c.State, c.LastUpdateStatus, c.StateReason)
		}
		if got.Code == nil || aws.ToString(got.Code.RepositoryType) != "S3" {
			t.Errorf("GetFunction Code = %+v, want RepositoryType S3", got.Code)
		}
		if got.Tags["team"] != "platform" {
			t.Errorf("GetFunction Tags = %v", got.Tags)
		}
		if got.Concurrency != nil {
			t.Errorf("GetFunction Concurrency = %+v without reserved concurrency", got.Concurrency)
		}
		if aws.ToString(got.Code.Location) == "" {
			t.Error("GetFunction Code.Location is empty; Lambda returns a presigned URL to download the package")
		}
		cfg, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(fnArn)})
		ok(t, err, "GetFunctionConfiguration by ARN")
		if aws.ToString(cfg.Description) != "conformance" || cfg.Environment == nil || cfg.Environment.Variables["FOO"] != "bar" {
			t.Errorf("GetFunctionConfiguration Description %q, Environment %+v", aws.ToString(cfg.Description), cfg.Environment)
		}

		list, err := client.ListFunctions(ctx, &lambda.ListFunctionsInput{})
		ok(t, err, "ListFunctions")
		if !slices.ContainsFunc(list.Functions, func(f types.FunctionConfiguration) bool { return aws.ToString(f.FunctionArn) == fnArn }) {
			t.Errorf("ListFunctions does not list %s with its unqualified ARN", fnArn)
		}
		all, err := client.ListFunctions(ctx, &lambda.ListFunctionsInput{FunctionVersion: types.FunctionVersionAll})
		ok(t, err, "ListFunctions ALL")
		if !slices.ContainsFunc(all.Functions, func(f types.FunctionConfiguration) bool { return aws.ToString(f.FunctionArn) == fnArn+":$LATEST" }) {
			t.Errorf("ListFunctions(FunctionVersion ALL) does not list %s:$LATEST", fnArn)
		}

		missing := name("missing")
		_, err = client.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(missing)})
		errAs[*types.ResourceNotFoundException](t, err, "GetFunction of a missing function")
		wantStatus(t, err, "GetFunction of a missing function", 404)
		wantMessage(t, err, "GetFunction of a missing function", "Function not found: arn:aws:lambda:"+region()+":"+acct+":function:"+missing)

		cs, err := client.GetFunctionCodeSigningConfig(ctx, &lambda.GetFunctionCodeSigningConfigInput{FunctionName: aws.String(fnName)})
		ok(t, err, "GetFunctionCodeSigningConfig")
		if aws.ToString(cs.FunctionName) != fnName {
			t.Errorf("GetFunctionCodeSigningConfig FunctionName = %q", aws.ToString(cs.FunctionName))
		}
	})

	t.Run("UpdateFunctionConfiguration", func(t *testing.T) {
		before, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(fnName)})
		ok(t, err, "GetFunctionConfiguration")
		_, err = client.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{FunctionName: aws.String(fnName), Timeout: aws.Int32(9), RevisionId: aws.String("00000000-0000-0000-0000-000000000000")})
		errAs[*types.PreconditionFailedException](t, err, "UpdateFunctionConfiguration with a stale RevisionId")
		wantStatus(t, err, "UpdateFunctionConfiguration with a stale RevisionId", 412)

		up, err := client.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
			FunctionName: aws.String(fnName), Timeout: aws.Int32(9),
			Environment: &types.Environment{Variables: map[string]string{"OTHER": "x"}}, RevisionId: before.RevisionId,
		})
		ok(t, err, "UpdateFunctionConfiguration")
		if up.LastUpdateStatus != types.LastUpdateStatusInProgress {
			t.Errorf("UpdateFunctionConfiguration LastUpdateStatus = %q, want InProgress", up.LastUpdateStatus)
		}
		err = lambda.NewFunctionUpdatedV2Waiter(client).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)}, time.Minute)
		ok(t, err, "FunctionUpdatedV2 waiter")
		after, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(fnName)})
		ok(t, err, "GetFunctionConfiguration")
		if aws.ToInt32(after.Timeout) != 9 || after.Environment == nil || len(after.Environment.Variables) != 1 || after.Environment.Variables["OTHER"] != "x" {
			t.Errorf("after the update Timeout %d, Environment %+v; want 9 and the variables replaced wholesale by {OTHER: x}", aws.ToInt32(after.Timeout), after.Environment)
		}
		if aws.ToString(after.RevisionId) == aws.ToString(before.RevisionId) {
			t.Error("RevisionId did not change on update")
		}
	})

	t.Run("versions and aliases", func(t *testing.T) {
		v1, err := client.PublishVersion(ctx, &lambda.PublishVersionInput{FunctionName: aws.String(fnName), Description: aws.String("first")})
		ok(t, err, "PublishVersion")
		if aws.ToString(v1.Version) != "1" || aws.ToString(v1.FunctionArn) != fnArn+":1" {
			t.Errorf("PublishVersion Version %q, FunctionArn %q; want 1 and %s:1", aws.ToString(v1.Version), aws.ToString(v1.FunctionArn), fnArn)
		}
		again, err := client.PublishVersion(ctx, &lambda.PublishVersionInput{FunctionName: aws.String(fnName)})
		ok(t, err, "PublishVersion without changes")
		if aws.ToString(again.Version) != "1" {
			t.Errorf("PublishVersion without changes = version %q, want the existing 1", aws.ToString(again.Version))
		}
		_, err = client.PublishVersion(ctx, &lambda.PublishVersionInput{FunctionName: aws.String(fnName), CodeSha256: aws.String("wrong")})
		errAs[*types.InvalidParameterValueException](t, err, "PublishVersion with a wrong CodeSha256")

		versions, err := client.ListVersionsByFunction(ctx, &lambda.ListVersionsByFunctionInput{FunctionName: aws.String(fnName)})
		ok(t, err, "ListVersionsByFunction")
		var names []string
		for _, v := range versions.Versions {
			names = append(names, aws.ToString(v.Version))
		}
		if !slices.Equal(names, []string{"$LATEST", "1"}) {
			t.Errorf("ListVersionsByFunction = %v, want [$LATEST 1] oldest first", names)
		}

		aliasArn := fnArn + ":live"
		alias, err := client.CreateAlias(ctx, &lambda.CreateAliasInput{FunctionName: aws.String(fnName), Name: aws.String("live"), FunctionVersion: aws.String("1")})
		ok(t, err, "CreateAlias")
		if aws.ToString(alias.AliasArn) != aliasArn || aws.ToString(alias.FunctionVersion) != "1" || !uuidPattern.MatchString(aws.ToString(alias.RevisionId)) {
			t.Errorf("CreateAlias = %+v", alias)
		}
		_, err = client.CreateAlias(ctx, &lambda.CreateAliasInput{FunctionName: aws.String(fnName), Name: aws.String("live"), FunctionVersion: aws.String("1")})
		errAs[*types.ResourceConflictException](t, err, "CreateAlias of an existing alias")
		wantMessage(t, err, "CreateAlias of an existing alias", "Alias already exists: "+aliasArn)
		_, err = client.UpdateAlias(ctx, &lambda.UpdateAliasInput{FunctionName: aws.String(fnName), Name: aws.String("live"), Description: aws.String("production")})
		ok(t, err, "UpdateAlias")
		got, err := client.GetAlias(ctx, &lambda.GetAliasInput{FunctionName: aws.String(fnName), Name: aws.String("live")})
		ok(t, err, "GetAlias")
		if aws.ToString(got.Description) != "production" {
			t.Errorf("GetAlias Description = %q", aws.ToString(got.Description))
		}
		list, err := client.ListAliases(ctx, &lambda.ListAliasesInput{FunctionName: aws.String(fnName)})
		ok(t, err, "ListAliases")
		if len(list.Aliases) != 1 || aws.ToString(list.Aliases[0].Name) != "live" {
			t.Errorf("ListAliases = %+v", list.Aliases)
		}
		_, err = client.GetAlias(ctx, &lambda.GetAliasInput{FunctionName: aws.String(fnName), Name: aws.String("nope")})
		errAs[*types.ResourceNotFoundException](t, err, "GetAlias of a missing alias")
		wantMessage(t, err, "GetAlias of a missing alias", "Cannot find alias arn: "+fnArn+":nope")

		// A published version is Pending until Lambda activates it, and
		// Lambda refuses to invoke a Pending version (lambda-api.md 5.1, 7.1).
		err = lambda.NewPublishedVersionActiveWaiter(client).Wait(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(fnName), Qualifier: aws.String("1")}, 2*time.Minute)
		ok(t, err, "PublishedVersionActive waiter")
		inv, err := client.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName), Qualifier: aws.String("live")})
		ok(t, err, "Invoke through the alias")
		if aws.ToString(inv.ExecutedVersion) != "1" {
			t.Errorf("Invoke through the alias ExecutedVersion = %q, want 1", aws.ToString(inv.ExecutedVersion))
		}
		var env map[string]any
		_ = json.Unmarshal(inv.Payload, &env)
		if env["FOO"] != nil || env["AWS_LAMBDA_FUNCTION_VERSION"] != "1" || env["AWS_LAMBDA_FUNCTION_NAME"] != fnName || env["AWS_REGION"] != region() {
			t.Errorf("version 1 sees environment %v; want the published configuration (no FOO), version 1, its name and region", env)
		}

		_, err = client.DeleteAlias(ctx, &lambda.DeleteAliasInput{FunctionName: aws.String(fnName), Name: aws.String("live")})
		ok(t, err, "DeleteAlias")
		_, err = client.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName), Qualifier: aws.String("$LATEST")})
		errAs[*types.InvalidParameterValueException](t, err, "DeleteFunction of $LATEST")
		wantMessage(t, err, "DeleteFunction of $LATEST", "$LATEST version cannot be deleted without deleting the function.")
	})

	t.Run("UpdateFunctionCode", func(t *testing.T) {
		newCode := functionZip(t, "2")
		newSum := sha256.Sum256(newCode)
		up, err := client.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{FunctionName: aws.String(fnName), ZipFile: newCode, Publish: true})
		ok(t, err, "UpdateFunctionCode")
		if aws.ToString(up.CodeSha256) != base64.StdEncoding.EncodeToString(newSum[:]) {
			t.Errorf("UpdateFunctionCode CodeSha256 = %q, want the new package's", aws.ToString(up.CodeSha256))
		}
		if aws.ToString(up.Version) != "2" {
			t.Errorf("UpdateFunctionCode with Publish returned version %q, want the new version 2", aws.ToString(up.Version))
		}
		// TagResource fails while an update is in progress (lambda-api.md
		// 5.1), so the next step waits for the update to finish.
		err = lambda.NewFunctionUpdatedV2Waiter(client).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)}, time.Minute)
		ok(t, err, "FunctionUpdatedV2 waiter")
	})

	t.Run("tags", func(t *testing.T) {
		_, err := client.TagResource(ctx, &lambda.TagResourceInput{Resource: aws.String(fnArn), Tags: map[string]string{"env": "test"}})
		ok(t, err, "TagResource")
		tags, err := client.ListTags(ctx, &lambda.ListTagsInput{Resource: aws.String(fnArn)})
		ok(t, err, "ListTags")
		if tags.Tags["env"] != "test" || tags.Tags["team"] != "platform" {
			t.Errorf("ListTags = %v", tags.Tags)
		}
		_, err = client.UntagResource(ctx, &lambda.UntagResourceInput{Resource: aws.String(fnArn), TagKeys: []string{"env", "never-set"}})
		ok(t, err, "UntagResource")
		tags, err = client.ListTags(ctx, &lambda.ListTagsInput{Resource: aws.String(fnArn)})
		ok(t, err, "ListTags")
		if _, present := tags.Tags["env"]; present {
			t.Errorf("after UntagResource ListTags = %v", tags.Tags)
		}
		_, err = client.TagResource(ctx, &lambda.TagResourceInput{Resource: aws.String(fnArn + ":1"), Tags: map[string]string{"env": "test"}})
		if err == nil {
			t.Fatal("TagResource of a version ARN succeeded")
		}
		wantStatus(t, err, "TagResource of a version ARN", 400)
		wantMessage(t, err, "TagResource of a version ARN", "Tags on function aliases and versions are not supported. Please specify a function ARN.")
	})

	t.Run("concurrency", func(t *testing.T) {
		_, err := client.PutFunctionConcurrency(ctx, &lambda.PutFunctionConcurrencyInput{FunctionName: aws.String(fnName), ReservedConcurrentExecutions: aws.Int32(5)})
		ok(t, err, "PutFunctionConcurrency")
		got, err := client.GetFunctionConcurrency(ctx, &lambda.GetFunctionConcurrencyInput{FunctionName: aws.String(fnName)})
		ok(t, err, "GetFunctionConcurrency")
		if aws.ToInt32(got.ReservedConcurrentExecutions) != 5 {
			t.Errorf("GetFunctionConcurrency = %d, want 5", aws.ToInt32(got.ReservedConcurrentExecutions))
		}
		fn, err := client.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
		ok(t, err, "GetFunction")
		if fn.Concurrency == nil || aws.ToInt32(fn.Concurrency.ReservedConcurrentExecutions) != 5 {
			t.Errorf("GetFunction Concurrency = %+v, want 5", fn.Concurrency)
		}
		_, err = client.PutFunctionConcurrency(ctx, &lambda.PutFunctionConcurrencyInput{FunctionName: aws.String(fnName + ":1"), ReservedConcurrentExecutions: aws.Int32(5)})
		errAs[*types.InvalidParameterValueException](t, err, "PutFunctionConcurrency on a version")
		wantMessage(t, err, "PutFunctionConcurrency on a version", "This operation is permitted on Lambda functions only. Aliases and versions do not support this operation. Please specify either a function name or an unqualified function ARN.")
		_, err = client.DeleteFunctionConcurrency(ctx, &lambda.DeleteFunctionConcurrencyInput{FunctionName: aws.String(fnName)})
		ok(t, err, "DeleteFunctionConcurrency")
		got, err = client.GetFunctionConcurrency(ctx, &lambda.GetFunctionConcurrencyInput{FunctionName: aws.String(fnName)})
		ok(t, err, "GetFunctionConcurrency")
		if got.ReservedConcurrentExecutions != nil {
			t.Errorf("after DeleteFunctionConcurrency GetFunctionConcurrency = %d, want none", aws.ToInt32(got.ReservedConcurrentExecutions))
		}
	})

	t.Run("GetAccountSettings", func(t *testing.T) {
		out, err := client.GetAccountSettings(ctx, &lambda.GetAccountSettingsInput{})
		ok(t, err, "GetAccountSettings")
		l := out.AccountLimit
		if l == nil || l.CodeSizeUnzipped != 262144000 || l.CodeSizeZipped != 52428800 || l.ConcurrentExecutions != 1000 {
			t.Errorf("AccountLimit = %+v, want CodeSizeUnzipped 262144000, CodeSizeZipped 52428800, ConcurrentExecutions 1000", l)
		}
		if out.AccountUsage == nil || out.AccountUsage.FunctionCount < 1 {
			t.Errorf("AccountUsage = %+v, want at least this function counted", out.AccountUsage)
		}
	})

	t.Run("DeleteFunction", func(t *testing.T) {
		_, err := client.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
		ok(t, err, "DeleteFunction")
		_, err = client.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
		errAs[*types.ResourceNotFoundException](t, err, "GetFunction after DeleteFunction")
	})
}

func TestLambdaPermissions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	acct := account(t)
	fn := deployFunction(t, name("perm"), "handler.echo", nil)
	fnName := aws.ToString(fn.FunctionName)
	topic := "arn:aws:sns:" + region() + ":" + acct + ":t"

	_, err := client.GetPolicy(ctx, &lambda.GetPolicyInput{FunctionName: aws.String(fnName)})
	errAs[*types.ResourceNotFoundException](t, err, "GetPolicy of a function without a policy")

	added, err := client.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(fnName), StatementId: aws.String("sns"),
		Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("sns.amazonaws.com"), SourceArn: aws.String(topic), SourceAccount: aws.String(acct),
	})
	ok(t, err, "AddPermission")
	wantJSONEqual(t, "AddPermission Statement", aws.ToString(added.Statement), `{"Sid":"sns","Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},`+
		`"Action":"lambda:InvokeFunction","Resource":"`+aws.ToString(fn.FunctionArn)+`",`+
		`"Condition":{"ArnLike":{"AWS:SourceArn":"`+topic+`"},"StringEquals":{"AWS:SourceAccount":"`+acct+`"}}}`)

	_, err = client.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(fnName), StatementId: aws.String("account"),
		Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("111111111111"),
	})
	ok(t, err, "AddPermission for an account")

	pol, err := client.GetPolicy(ctx, &lambda.GetPolicyInput{FunctionName: aws.String(fnName)})
	ok(t, err, "GetPolicy")
	var doc struct {
		Version   string
		Id        string
		Statement []map[string]any
	}
	if err := json.Unmarshal([]byte(aws.ToString(pol.Policy)), &doc); err != nil {
		t.Fatalf("GetPolicy Policy is not JSON: %v", err)
	}
	if doc.Version != "2012-10-17" || doc.Id != "default" || len(doc.Statement) != 2 || doc.Statement[0]["Sid"] != "sns" || doc.Statement[1]["Sid"] != "account" {
		t.Errorf("GetPolicy = %s, want version 2012-10-17, Id default and the two statements in order", aws.ToString(pol.Policy))
	}
	if p, _ := doc.Statement[1]["Principal"].(map[string]any); p["AWS"] != "arn:aws:iam::111111111111:root" {
		t.Errorf("an account principal renders as %v, want {\"AWS\": \"arn:aws:iam::111111111111:root\"}", doc.Statement[1]["Principal"])
	}
	if _, present := doc.Statement[1]["Condition"]; present {
		t.Error("a statement added without condition inputs carries a Condition")
	}
	if !uuidPattern.MatchString(aws.ToString(pol.RevisionId)) {
		t.Errorf("GetPolicy RevisionId = %q", aws.ToString(pol.RevisionId))
	}

	_, err = client.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(fnName), StatementId: aws.String("sns"),
		Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("sns.amazonaws.com"),
	})
	errAs[*types.ResourceConflictException](t, err, "AddPermission with an existing statement id")
	wantMessage(t, err, "AddPermission with an existing statement id", "The statement id (sns) provided already exists. Please provide a new statement id, or remove the existing statement.")
	_, err = client.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(fnName), StatementId: aws.String("latest"), Qualifier: aws.String("$LATEST"),
		Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("sns.amazonaws.com"),
	})
	errAs[*types.InvalidParameterValueException](t, err, "AddPermission on $LATEST")
	wantMessage(t, err, "AddPermission on $LATEST", "We currently do not support adding policies for $LATEST.")
	_, err = client.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(fnName), StatementId: aws.String("example.com"),
		Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("sns.amazonaws.com"),
	})
	apiErr(t, err, "AddPermission with statement id example.com", "ValidationException")

	_, err = client.RemovePermission(ctx, &lambda.RemovePermissionInput{FunctionName: aws.String(fnName), StatementId: aws.String("non-existent")})
	errAs[*types.ResourceNotFoundException](t, err, "RemovePermission of an unknown statement")
	wantMessage(t, err, "RemovePermission of an unknown statement", "Statement non-existent is not found in resource policy.")
	for _, sid := range []string{"sns", "account"} {
		_, err = client.RemovePermission(ctx, &lambda.RemovePermissionInput{FunctionName: aws.String(fnName), StatementId: aws.String(sid)})
		ok(t, err, "RemovePermission "+sid)
	}
	_, err = client.GetPolicy(ctx, &lambda.GetPolicyInput{FunctionName: aws.String(fnName)})
	errAs[*types.ResourceNotFoundException](t, err, "GetPolicy after removing every statement")
}

func TestLambdaInvoke(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	acct := account(t)
	echo := deployFunction(t, name("echo"), "handler.echo", nil)
	echoName := aws.ToString(echo.FunctionName)

	t.Run("RequestResponse", func(t *testing.T) {
		out, err := client.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(echoName), Payload: []byte(`{"hello":"world","n":1}`), LogType: types.LogTypeTail})
		ok(t, err, "Invoke")
		if out.StatusCode != 200 || out.FunctionError != nil || aws.ToString(out.ExecutedVersion) != "$LATEST" {
			t.Errorf("Invoke StatusCode %d, FunctionError %v, ExecutedVersion %q", out.StatusCode, out.FunctionError, aws.ToString(out.ExecutedVersion))
		}
		wantJSONEqual(t, "Invoke payload", string(out.Payload), `{"hello":"world","n":1}`)
		logs, err := base64.StdEncoding.DecodeString(aws.ToString(out.LogResult))
		if err != nil {
			t.Fatalf("LogResult is not base64: %v", err)
		}
		for _, line := range []string{"START RequestId:", "END RequestId:", "REPORT RequestId:"} {
			if !strings.Contains(string(logs), line) {
				t.Errorf("LogResult tail lacks %q: %s", line, logs)
			}
		}
	})

	t.Run("Event and DryRun", func(t *testing.T) {
		ev, err := client.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(echoName), InvocationType: types.InvocationTypeEvent, Payload: []byte(`{}`)})
		ok(t, err, "Invoke Event")
		if ev.StatusCode != 202 || len(ev.Payload) != 0 {
			t.Errorf("Invoke Event StatusCode %d with %d payload bytes, want 202 and none", ev.StatusCode, len(ev.Payload))
		}
		dry, err := client.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(echoName), InvocationType: types.InvocationTypeDryRun})
		ok(t, err, "Invoke DryRun")
		if dry.StatusCode != 204 || len(dry.Payload) != 0 {
			t.Errorf("Invoke DryRun StatusCode %d with %d payload bytes, want 204 and none", dry.StatusCode, len(dry.Payload))
		}
	})

	t.Run("errors before the function runs", func(t *testing.T) {
		_, err := client.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(echoName), Payload: []byte(`{not json`)})
		errAs[*types.InvalidRequestContentException](t, err, "Invoke with a non-JSON payload")
		wantStatus(t, err, "Invoke with a non-JSON payload", 400)
		wantMessageContains(t, err, "Invoke with a non-JSON payload", "Could not parse request body into json")
		missing := name("missing")
		_, err = client.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(missing)})
		errAs[*types.ResourceNotFoundException](t, err, "Invoke of a missing function")
		wantMessage(t, err, "Invoke of a missing function", "Function not found: arn:aws:lambda:"+region()+":"+acct+":function:"+missing)
	})

	t.Run("function error", func(t *testing.T) {
		fn := deployFunction(t, name("fail"), "handler.fail", nil)
		out, err := client.Invoke(ctx, &lambda.InvokeInput{FunctionName: fn.FunctionName})
		ok(t, err, "Invoke of a failing handler")
		if out.StatusCode != 200 || aws.ToString(out.FunctionError) != "Unhandled" {
			t.Errorf("failing Invoke StatusCode %d, FunctionError %q; want 200 and Unhandled", out.StatusCode, aws.ToString(out.FunctionError))
		}
		var payload struct {
			ErrorMessage string   `json:"errorMessage"`
			ErrorType    string   `json:"errorType"`
			RequestID    string   `json:"requestId"`
			StackTrace   []string `json:"stackTrace"`
		}
		if err := json.Unmarshal(out.Payload, &payload); err != nil || payload.ErrorMessage != "Handler fails" || payload.ErrorType != "Exception" || len(payload.StackTrace) == 0 {
			t.Errorf("failing Invoke payload = %s, want the Python error object", out.Payload)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		fn := deployFunction(t, name("slow"), "handler.sleep", nil)
		_, err := client.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{FunctionName: fn.FunctionName, Timeout: aws.Int32(1)})
		ok(t, err, "UpdateFunctionConfiguration Timeout 1")
		ok(t, lambda.NewFunctionUpdatedV2Waiter(client).Wait(ctx, &lambda.GetFunctionInput{FunctionName: fn.FunctionName}, time.Minute), "FunctionUpdatedV2 waiter")
		out, err := client.Invoke(ctx, &lambda.InvokeInput{FunctionName: fn.FunctionName})
		ok(t, err, "Invoke of a handler that outlives its timeout")
		if aws.ToString(out.FunctionError) != "Unhandled" {
			t.Errorf("timed-out Invoke FunctionError = %q, want Unhandled", aws.ToString(out.FunctionError))
		}
		var payload struct {
			ErrorMessage string `json:"errorMessage"`
			ErrorType    string `json:"errorType"`
		}
		_ = json.Unmarshal(out.Payload, &payload)
		if payload.ErrorType != "Sandbox.Timedout" || !strings.Contains(payload.ErrorMessage, "Task timed out after 1.00 seconds") {
			t.Errorf("timed-out Invoke payload = %s, want errorType Sandbox.Timedout and \"Task timed out after 1.00 seconds\"", out.Payload)
		}
	})

	t.Run("reserved concurrency 0 throttles", func(t *testing.T) {
		_, err := client.PutFunctionConcurrency(ctx, &lambda.PutFunctionConcurrencyInput{FunctionName: aws.String(echoName), ReservedConcurrentExecutions: aws.Int32(0)})
		ok(t, err, "PutFunctionConcurrency 0")
		_, err = client.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(echoName)}, func(o *lambda.Options) { o.RetryMaxAttempts = 1 })
		e := typedErr[*types.TooManyRequestsException](t, err, "Invoke with reserved concurrency 0")
		wantStatus(t, err, "Invoke with reserved concurrency 0", 429)
		if e.Reason != types.ThrottleReasonReservedFunctionConcurrentInvocationLimitExceeded {
			t.Errorf("Reason = %q, want ReservedFunctionConcurrentInvocationLimitExceeded", e.Reason)
		}
		wantMessage(t, err, "Invoke with reserved concurrency 0", "Rate Exceeded.")
		_, err = client.DeleteFunctionConcurrency(ctx, &lambda.DeleteFunctionConcurrencyInput{FunctionName: aws.String(echoName)})
		ok(t, err, "DeleteFunctionConcurrency")
	})

	t.Run("logs reach CloudWatch Logs", func(t *testing.T) {
		logs := cloudwatchlogs.NewFromConfig(awsCfg)
		group := "/aws/lambda/" + echoName
		eventually(t, 30*time.Second, 500*time.Millisecond, "the function's START line in "+group, func() (bool, string) {
			out, err := logs.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group), FilterPattern: aws.String(`"START RequestId"`)})
			if err != nil {
				return false, err.Error()
			}
			return len(out.Events) > 0, fmt.Sprintf("%d matching events", len(out.Events))
		})
	})
}

func TestLambdaEventInvokeConfig(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	fn := deployFunction(t, name("async"), "handler.echo", nil)
	fnName := aws.ToString(fn.FunctionName)
	latestArn := aws.ToString(fn.FunctionArn) + ":$LATEST"

	_, err := client.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{FunctionName: aws.String(fnName)})
	errAs[*types.InvalidParameterValueException](t, err, "PutFunctionEventInvokeConfig with no settings")
	wantMessage(t, err, "PutFunctionEventInvokeConfig with no settings", "You must specify at least one of error handling or destination setting.")

	put, err := client.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{FunctionName: aws.String(fnName), MaximumRetryAttempts: aws.Int32(0), MaximumEventAgeInSeconds: aws.Int32(60)})
	ok(t, err, "PutFunctionEventInvokeConfig")
	if aws.ToString(put.FunctionArn) != latestArn || aws.ToInt32(put.MaximumRetryAttempts) != 0 || aws.ToInt32(put.MaximumEventAgeInSeconds) != 60 || put.LastModified == nil {
		t.Errorf("PutFunctionEventInvokeConfig = %+v, want FunctionArn %s and the settings", put, latestArn)
	}
	up, err := client.UpdateFunctionEventInvokeConfig(ctx, &lambda.UpdateFunctionEventInvokeConfigInput{FunctionName: aws.String(fnName), MaximumRetryAttempts: aws.Int32(1)})
	ok(t, err, "UpdateFunctionEventInvokeConfig")
	if aws.ToInt32(up.MaximumRetryAttempts) != 1 || aws.ToInt32(up.MaximumEventAgeInSeconds) != 60 {
		t.Errorf("UpdateFunctionEventInvokeConfig = retries %d, age %d; want a partial update to 1, keeping 60", aws.ToInt32(up.MaximumRetryAttempts), aws.ToInt32(up.MaximumEventAgeInSeconds))
	}
	got, err := client.GetFunctionEventInvokeConfig(ctx, &lambda.GetFunctionEventInvokeConfigInput{FunctionName: aws.String(fnName)})
	ok(t, err, "GetFunctionEventInvokeConfig")
	if aws.ToInt32(got.MaximumRetryAttempts) != 1 {
		t.Errorf("GetFunctionEventInvokeConfig MaximumRetryAttempts = %d", aws.ToInt32(got.MaximumRetryAttempts))
	}
	list, err := client.ListFunctionEventInvokeConfigs(ctx, &lambda.ListFunctionEventInvokeConfigsInput{FunctionName: aws.String(fnName)})
	ok(t, err, "ListFunctionEventInvokeConfigs")
	if len(list.FunctionEventInvokeConfigs) != 1 {
		t.Errorf("ListFunctionEventInvokeConfigs = %d configs, want 1", len(list.FunctionEventInvokeConfigs))
	}
	_, err = client.DeleteFunctionEventInvokeConfig(ctx, &lambda.DeleteFunctionEventInvokeConfigInput{FunctionName: aws.String(fnName)})
	ok(t, err, "DeleteFunctionEventInvokeConfig")
	_, err = client.GetFunctionEventInvokeConfig(ctx, &lambda.GetFunctionEventInvokeConfigInput{FunctionName: aws.String(fnName)})
	errAs[*types.ResourceNotFoundException](t, err, "GetFunctionEventInvokeConfig after delete")
	wantMessage(t, err, "GetFunctionEventInvokeConfig after delete", "The function "+latestArn+" doesn't have an EventInvokeConfig")
}

func TestLambdaFunctionURL(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	fn := deployFunction(t, name("url"), "handler.url", nil)
	fnName := aws.ToString(fn.FunctionName)

	created, err := client.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{FunctionName: aws.String(fnName), AuthType: types.FunctionUrlAuthTypeNone})
	ok(t, err, "CreateFunctionUrlConfig")
	u := aws.ToString(created.FunctionUrl)
	// InvokeMode defaults to BUFFERED; whether the response echoes the
	// default when the request omits it is not recorded, so only a
	// different value is a failure.
	if created.AuthType != types.FunctionUrlAuthTypeNone || (created.InvokeMode != "" && created.InvokeMode != types.InvokeModeBuffered) ||
		aws.ToString(created.FunctionArn) != aws.ToString(fn.FunctionArn) || created.CreationTime == nil {
		t.Errorf("CreateFunctionUrlConfig AuthType %q, InvokeMode %q, FunctionArn %q, CreationTime %v; want NONE, BUFFERED, the function ARN and a time",
			created.AuthType, created.InvokeMode, aws.ToString(created.FunctionArn), created.CreationTime)
	}
	if !strings.Contains(u, ".lambda-url."+region()+".") || !strings.HasSuffix(u, "/") {
		t.Errorf("FunctionUrl = %q, want <url-id>.lambda-url.%s.<domain>/", u, region())
	}

	_, err = client.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{FunctionName: aws.String(fnName), AuthType: types.FunctionUrlAuthTypeNone})
	errAs[*types.ResourceConflictException](t, err, "CreateFunctionUrlConfig twice")
	_, err = client.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{FunctionName: aws.String(fnName), AuthType: types.FunctionUrlAuthTypeNone, Qualifier: aws.String("$LATEST")})
	apiErr(t, err, "CreateFunctionUrlConfig with Qualifier $LATEST", "ValidationException")

	got, err := client.GetFunctionUrlConfig(ctx, &lambda.GetFunctionUrlConfigInput{FunctionName: aws.String(fnName)})
	ok(t, err, "GetFunctionUrlConfig")
	if aws.ToString(got.FunctionUrl) != u || got.LastModifiedTime == nil {
		t.Errorf("GetFunctionUrlConfig = %+v", got)
	}
	upd, err := client.UpdateFunctionUrlConfig(ctx, &lambda.UpdateFunctionUrlConfigInput{FunctionName: aws.String(fnName), Cors: &types.Cors{AllowOrigins: []string{"https://example.com"}, MaxAge: aws.Int32(60)}})
	ok(t, err, "UpdateFunctionUrlConfig")
	if upd.Cors == nil || !slices.Equal(upd.Cors.AllowOrigins, []string{"https://example.com"}) {
		t.Errorf("UpdateFunctionUrlConfig Cors = %+v", upd.Cors)
	}
	list, err := client.ListFunctionUrlConfigs(ctx, &lambda.ListFunctionUrlConfigsInput{FunctionName: aws.String(fnName)})
	ok(t, err, "ListFunctionUrlConfigs")
	if len(list.FunctionUrlConfigs) != 1 || aws.ToString(list.FunctionUrlConfigs[0].FunctionUrl) != u {
		t.Errorf("ListFunctionUrlConfigs = %+v", list.FunctionUrlConfigs)
	}

	call := func() (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u+"some/path?a=1", strings.NewReader("ping"))
		ok(t, err, "building the URL request")
		// A text content type keeps the body as text in the event; a
		// binary one would arrive base64-encoded.
		req.Header.Set("Content-Type", "text/plain")
		resp, err := http.DefaultClient.Do(req)
		ok(t, err, "calling the function URL "+u)
		b, err := io.ReadAll(resp.Body)
		ok(t, err, "reading the function URL response")
		ok(t, resp.Body.Close(), "closing the function URL response")
		return resp, b
	}

	t.Run("no resource policy is forbidden", func(t *testing.T) {
		resp, body := call()
		if resp.StatusCode != 403 {
			t.Errorf("a URL with AuthType NONE and no resource policy answered %d %s, want 403", resp.StatusCode, body)
		}
		wantJSONEqual(t, "403 body", string(body), `{"Message":"Forbidden. For troubleshooting Function URL authorization issues, see: https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html"}`)
	})

	t.Run("public URL invokes the function", func(t *testing.T) {
		_, err := client.AddPermission(ctx, &lambda.AddPermissionInput{
			FunctionName: aws.String(fnName), StatementId: aws.String("FunctionURLAllowPublicAccess"),
			Action: aws.String("lambda:InvokeFunctionUrl"), Principal: aws.String("*"), FunctionUrlAuthType: types.FunctionUrlAuthTypeNone,
		})
		ok(t, err, "AddPermission lambda:InvokeFunctionUrl")
		_, err = client.AddPermission(ctx, &lambda.AddPermissionInput{
			FunctionName: aws.String(fnName), StatementId: aws.String("FunctionURLInvokeAllowPublicAccess"),
			Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("*"), InvokedViaFunctionUrl: aws.Bool(true),
		})
		ok(t, err, "AddPermission lambda:InvokeFunction via function URL")
		resp, body := call()
		if resp.StatusCode != 201 || resp.Header.Get("x-conformance") != "yes" {
			t.Fatalf("the public URL answered %d (x-conformance %q): %s; want the function's 201", resp.StatusCode, resp.Header.Get("x-conformance"), body)
		}
		wantJSONEqual(t, "the function's view of the request", string(body), `{"version":"2.0","rawPath":"/some/path","rawQueryString":"a=1","method":"POST","body":"ping"}`)
	})

	_, err = client.DeleteFunctionUrlConfig(ctx, &lambda.DeleteFunctionUrlConfigInput{FunctionName: aws.String(fnName)})
	ok(t, err, "DeleteFunctionUrlConfig")
	_, err = client.GetFunctionUrlConfig(ctx, &lambda.GetFunctionUrlConfigInput{FunctionName: aws.String(fnName)})
	errAs[*types.ResourceNotFoundException](t, err, "GetFunctionUrlConfig after delete")
	wantMessage(t, err, "GetFunctionUrlConfig after delete", "The resource you requested does not exist.")
}

func TestLambdaSQSEventSourceMapping(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	sqsClient := sqs.NewFromConfig(awsCfg)
	acct := account(t)
	resultURL := createQueue(t, name("esm-result"), nil)
	fn := deployFunction(t, name("esm"), "handler.forward", map[string]string{"FORWARD_QUEUE_URL": resultURL})
	fnName := aws.ToString(fn.FunctionName)
	dlqURL := createQueue(t, name("esm-dlq"), nil)
	srcName := name("esm-src")
	srcURL := createQueue(t, srcName, map[string]string{
		"VisibilityTimeout": "5",
		"RedrivePolicy":     `{"deadLetterTargetArn":"` + queueArn(t, dlqURL) + `","maxReceiveCount":"1"}`,
	})
	srcArn := queueArn(t, srcURL)

	t.Run("validation", func(t *testing.T) {
		_, err := client.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{FunctionName: aws.String(fnName), EventSourceArn: aws.String(srcArn), BatchSize: aws.Int32(20)})
		errAs[*types.InvalidParameterValueException](t, err, "CreateEventSourceMapping BatchSize 20 without a window")
		wantMessage(t, err, "CreateEventSourceMapping BatchSize 20 without a window", "Maximum batch window in seconds must be greater than 0 if maximum batch size is greater than 10")
		_, err = client.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{FunctionName: aws.String(name("nofn")), EventSourceArn: aws.String(srcArn)})
		errAs[*types.InvalidParameterValueException](t, err, "CreateEventSourceMapping of a missing function")
		wantMessage(t, err, "CreateEventSourceMapping of a missing function", "Function does not exist")
		short := createQueue(t, name("esm-short"), map[string]string{"VisibilityTimeout": "2"})
		_, err = client.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{FunctionName: aws.String(fnName), EventSourceArn: aws.String(queueArn(t, short))})
		errAs[*types.InvalidParameterValueException](t, err, "CreateEventSourceMapping on a queue whose visibility timeout is below the function timeout")
		wantMessage(t, err, "CreateEventSourceMapping on a queue whose visibility timeout is below the function timeout", "Queue visibility timeout: 2 seconds is less than Function timeout: 5 seconds")
	})

	created, err := client.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
		FunctionName: aws.String(fnName), EventSourceArn: aws.String(srcArn), BatchSize: aws.Int32(10),
		FunctionResponseTypes: []types.FunctionResponseType{types.FunctionResponseTypeReportBatchItemFailures},
	})
	ok(t, err, "CreateEventSourceMapping")
	uuid := aws.ToString(created.UUID)
	t.Cleanup(func() {
		_, _ = client.DeleteEventSourceMapping(context.Background(), &lambda.DeleteEventSourceMappingInput{UUID: aws.String(uuid)})
	})
	if aws.ToString(created.State) != "Creating" || aws.ToString(created.StateTransitionReason) != "USER_INITIATED" || aws.ToInt32(created.BatchSize) != 10 ||
		aws.ToInt32(created.MaximumBatchingWindowInSeconds) != 0 || aws.ToString(created.EventSourceArn) != srcArn || aws.ToString(created.FunctionArn) != aws.ToString(fn.FunctionArn) ||
		aws.ToString(created.EventSourceMappingArn) != "arn:aws:lambda:"+region()+":"+acct+":event-source-mapping:"+uuid {
		t.Errorf("CreateEventSourceMapping = State %q, StateTransitionReason %q, BatchSize %d, window %d, EventSourceArn %q, FunctionArn %q, EventSourceMappingArn %q",
			aws.ToString(created.State), aws.ToString(created.StateTransitionReason), aws.ToInt32(created.BatchSize), aws.ToInt32(created.MaximumBatchingWindowInSeconds),
			aws.ToString(created.EventSourceArn), aws.ToString(created.FunctionArn), aws.ToString(created.EventSourceMappingArn))
	}
	eventually(t, time.Minute, 500*time.Millisecond, "the mapping to be Enabled", func() (bool, string) {
		got, err := client.GetEventSourceMapping(ctx, &lambda.GetEventSourceMappingInput{UUID: aws.String(uuid)})
		if err != nil {
			return false, err.Error()
		}
		return aws.ToString(got.State) == "Enabled", "State " + aws.ToString(got.State)
	})

	_, err = client.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{FunctionName: aws.String(fnName), EventSourceArn: aws.String(srcArn)})
	errAs[*types.ResourceConflictException](t, err, "CreateEventSourceMapping of the same function and queue")

	list, err := client.ListEventSourceMappings(ctx, &lambda.ListEventSourceMappingsInput{FunctionName: aws.String(fnName)})
	ok(t, err, "ListEventSourceMappings")
	if len(list.EventSourceMappings) != 1 || aws.ToString(list.EventSourceMappings[0].UUID) != uuid {
		t.Errorf("ListEventSourceMappings = %d mappings, want this one", len(list.EventSourceMappings))
	}

	t.Run("delivery and partial batch failure", func(t *testing.T) {
		good, err := sqsClient.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(srcURL), MessageBody: aws.String("all good")})
		ok(t, err, "SendMessage")
		bad, err := sqsClient.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(srcURL), MessageBody: aws.String("please fail")})
		ok(t, err, "SendMessage")

		type record struct {
			MessageID      string            `json:"messageId"`
			ReceiptHandle  string            `json:"receiptHandle"`
			Body           string            `json:"body"`
			Attributes     map[string]string `json:"attributes"`
			MD5OfBody      string            `json:"md5OfBody"`
			EventSource    string            `json:"eventSource"`
			EventSourceARN string            `json:"eventSourceARN"`
			AwsRegion      string            `json:"awsRegion"`
		}
		seen := map[string]record{}
		deadline := time.Now().Add(60 * time.Second)
		for len(seen) < 2 && time.Now().Before(deadline) {
			for _, m := range drain(t, resultURL, 2*time.Second) {
				var ev struct{ Records []record }
				if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &ev); err != nil {
					t.Fatalf("the function received %s, want an SQS event", aws.ToString(m.Body))
				}
				for _, r := range ev.Records {
					seen[r.MessageID] = r
				}
			}
		}
		for _, id := range []string{aws.ToString(good.MessageId), aws.ToString(bad.MessageId)} {
			r, found := seen[id]
			if !found {
				t.Errorf("message %s never reached the function", id)
				continue
			}
			if r.EventSource != "aws:sqs" || r.EventSourceARN != srcArn || r.AwsRegion != region() || r.ReceiptHandle == "" ||
				r.MD5OfBody != md5Hex([]byte(r.Body)) || r.Attributes["ApproximateReceiveCount"] == "" || r.Attributes["SentTimestamp"] == "" {
				t.Errorf("SQS record = %+v", r)
			}
		}
		// The reported failure returns to the queue and, with
		// maxReceiveCount 1, moves to the DLQ; the success is deleted.
		dead := drain(t, dlqURL, 30*time.Second)
		if len(dead) != 1 || aws.ToString(dead[0].MessageId) != aws.ToString(bad.MessageId) {
			t.Errorf("the DLQ received %v, want only the message the function reported as failed", bodies(dead))
		}
		attrs := queueAttrs(t, srcURL)
		if attrs["ApproximateNumberOfMessages"] != "0" || attrs["ApproximateNumberOfMessagesNotVisible"] != "0" {
			t.Errorf("the source queue still holds messages: visible %s, in flight %s", attrs["ApproximateNumberOfMessages"], attrs["ApproximateNumberOfMessagesNotVisible"])
		}
	})

	t.Run("update and delete", func(t *testing.T) {
		up, err := client.UpdateEventSourceMapping(ctx, &lambda.UpdateEventSourceMappingInput{UUID: aws.String(uuid), Enabled: aws.Bool(false)})
		ok(t, err, "UpdateEventSourceMapping Enabled false")
		if s := aws.ToString(up.State); s != "Disabling" && s != "Disabled" {
			t.Errorf("UpdateEventSourceMapping Enabled false State = %q, want Disabling", s)
		}
		eventually(t, 30*time.Second, 500*time.Millisecond, "the mapping to be Disabled", func() (bool, string) {
			got, err := client.GetEventSourceMapping(ctx, &lambda.GetEventSourceMappingInput{UUID: aws.String(uuid)})
			if err != nil {
				return false, err.Error()
			}
			return aws.ToString(got.State) == "Disabled", "State " + aws.ToString(got.State)
		})
		del, err := client.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{UUID: aws.String(uuid)})
		ok(t, err, "DeleteEventSourceMapping")
		if aws.ToString(del.State) != "Deleting" {
			t.Errorf("DeleteEventSourceMapping State = %q, want Deleting", aws.ToString(del.State))
		}
		eventually(t, 30*time.Second, 500*time.Millisecond, "the mapping to be gone", func() (bool, string) {
			_, err := client.GetEventSourceMapping(ctx, &lambda.GetEventSourceMappingInput{UUID: aws.String(uuid)})
			if err == nil {
				return false, "GetEventSourceMapping still succeeds"
			}
			var nf *types.ResourceNotFoundException
			return errors.As(err, &nf), err.Error()
		})
	})
}
