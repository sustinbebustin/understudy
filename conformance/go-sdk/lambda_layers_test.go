package gosdk_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Research: docs/research/lambda-destinations-layers.md.

// zipOf is a zip archive of files, by name.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		ok(t, err, "zip Create")
		_, err = io.WriteString(f, body)
		ok(t, err, "zip Write")
	}
	ok(t, w.Close(), "zip Close")
	return buf.Bytes()
}

// greetLayer is a layer archive with a Python module in python/.
func greetLayer(t *testing.T) []byte {
	t.Helper()
	return zipOf(t, map[string]string{"python/greet.py": "def hello():\n    return 'hello from the layer'\n"})
}

// publishLayer publishes archive as a version of layerName and deletes
// every version it published when the test ends.
func publishLayer(t *testing.T, client *lambda.Client, layerName string, archive []byte) *lambda.PublishLayerVersionOutput {
	t.Helper()
	out, err := client.PublishLayerVersion(t.Context(), &lambda.PublishLayerVersionInput{LayerName: aws.String(layerName), Content: &types.LayerVersionContentInput{ZipFile: archive}})
	ok(t, err, "PublishLayerVersion "+layerName)
	t.Cleanup(func() {
		_, _ = client.DeleteLayerVersion(context.Background(), &lambda.DeleteLayerVersionInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(out.Version)})
	})
	return out
}

// A layer's lifecycle: publish answers version 1 with the archive's
// SHA-256 and size, Description "" and no license (6.3:
// test_lambda_api.snapshot.json:L8271-L8292); Get by name and by version
// ARN answer the same; Content.Location serves the archive; versions list
// newest first (6.5: L8761-L8794); a deleted version is not found, its
// delete repeats without error (6.6: L8390-L8413), and numbers are never
// reused (finding 9: L8865-L8887).
func TestLambdaLayerLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	layerName := name(t, "layer")
	archive := greetLayer(t)
	first := publishLayer(t, client, layerName, archive)
	layerArn := "arn:aws:lambda:" + region() + ":" + account(t) + ":layer:" + layerName
	sum := sha256.Sum256(archive)
	if first.Version != 1 || aws.ToString(first.LayerArn) != layerArn || aws.ToString(first.LayerVersionArn) != layerArn+":1" ||
		first.Content == nil || aws.ToString(first.Content.CodeSha256) != base64.StdEncoding.EncodeToString(sum[:]) || first.Content.CodeSize != int64(len(archive)) ||
		first.Description == nil || aws.ToString(first.Description) != "" || first.LicenseInfo != nil {
		t.Errorf("PublishLayerVersion = %+v, want version 1 of %s with the archive's SHA-256 and size, Description \"\" and no LicenseInfo", first, layerArn)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000-0700", aws.ToString(first.CreatedDate)); err != nil {
		t.Errorf("CreatedDate %q, want milliseconds and +0000: %v", aws.ToString(first.CreatedDate), err)
	}

	byName, err := client.GetLayerVersion(ctx, &lambda.GetLayerVersionInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(1)})
	ok(t, err, "GetLayerVersion")
	byArn, err := client.GetLayerVersionByArn(ctx, &lambda.GetLayerVersionByArnInput{Arn: aws.String(layerArn + ":1")})
	ok(t, err, "GetLayerVersionByArn")
	for what, got := range map[string]*lambda.GetLayerVersionOutput{"GetLayerVersion": byName, "GetLayerVersionByArn": (*lambda.GetLayerVersionOutput)(byArn)} {
		if got.Version != 1 || aws.ToString(got.LayerVersionArn) != layerArn+":1" || aws.ToString(got.Content.CodeSha256) != aws.ToString(first.Content.CodeSha256) {
			t.Errorf("%s = %+v, want what PublishLayerVersion answered", what, got)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, aws.ToString(first.Content.Location), http.NoBody)
	ok(t, err, "building the Content.Location request")
	if r := send(t, http.DefaultClient, req, "GET Content.Location"); r.status != http.StatusOK || !bytes.Equal(r.body, archive) {
		t.Errorf("GET Content.Location = %d with %d bytes, want 200 with the archive", r.status, len(r.body))
	}

	publishLayer(t, client, layerName, archive)
	list, err := client.ListLayerVersions(ctx, &lambda.ListLayerVersionsInput{LayerName: aws.String(layerName)})
	ok(t, err, "ListLayerVersions")
	if len(list.LayerVersions) != 2 || list.LayerVersions[0].Version != 2 || list.LayerVersions[1].Version != 1 {
		t.Errorf("ListLayerVersions = %+v, want versions 2 then 1", list.LayerVersions)
	}
	layers, err := client.ListLayers(ctx, &lambda.ListLayersInput{})
	ok(t, err, "ListLayers")
	found := false
	for _, l := range layers.Layers {
		if aws.ToString(l.LayerName) == layerName {
			found = true
			if l.LatestMatchingVersion == nil || l.LatestMatchingVersion.Version != 2 {
				t.Errorf("ListLayers entry %+v, want version 2 as the latest", l)
			}
		}
	}
	if !found {
		t.Errorf("ListLayers does not list %s", layerName)
	}

	for range 2 {
		_, err = client.DeleteLayerVersion(ctx, &lambda.DeleteLayerVersionInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(1)})
		ok(t, err, "DeleteLayerVersion")
	}
	_, err = client.GetLayerVersion(ctx, &lambda.GetLayerVersionInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(1)})
	errAs[*types.ResourceNotFoundException](t, err, "GetLayerVersion of a deleted version")
	wantMessage(t, err, "GetLayerVersion of a deleted version", "The resource you requested does not exist.")
	_, err = client.DeleteLayerVersion(ctx, &lambda.DeleteLayerVersionInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(2)})
	ok(t, err, "DeleteLayerVersion 2")
	if third := publishLayer(t, client, layerName, archive); third.Version != 3 {
		t.Errorf("publish after deleting every version = %d, want 3", third.Version)
	}
}

// A version's permissions: no policy at first (6.8:
// test_lambda_api.snapshot.json:L8915-L8926), an account principal rendered
// as {"AWS":"arn:aws:iam::<account>:root"} (6.7, 9.4; the Smithy model's
// example), the policy collecting statements, and the recorded refusals
// for a duplicate statement and a stale RevisionId (L8951-L8974).
func TestLambdaLayerPermissions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	layerName := name(t, "layer-policy")
	version := aws.ToString(publishLayer(t, client, layerName, greetLayer(t)).LayerVersionArn)
	acct := account(t)

	_, err := client.GetLayerVersionPolicy(ctx, &lambda.GetLayerVersionPolicyInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(1)})
	errAs[*types.ResourceNotFoundException](t, err, "GetLayerVersionPolicy before any statement")
	wantMessage(t, err, "GetLayerVersionPolicy before any statement", "No policy is associated with the given resource.")

	added, err := client.AddLayerVersionPermission(ctx, &lambda.AddLayerVersionPermissionInput{
		LayerName: aws.String(layerName), VersionNumber: aws.Int64(1), StatementId: aws.String("account"), Action: aws.String("lambda:GetLayerVersion"), Principal: aws.String(acct),
	})
	ok(t, err, "AddLayerVersionPermission")
	statement := `{"Sid":"account","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + acct + `:root"},"Action":"lambda:GetLayerVersion","Resource":"` + version + `"}`
	if aws.ToString(added.Statement) != statement || aws.ToString(added.RevisionId) == "" {
		t.Errorf("AddLayerVersionPermission = %s rev %q, want %s and a RevisionId", aws.ToString(added.Statement), aws.ToString(added.RevisionId), statement)
	}
	policy, err := client.GetLayerVersionPolicy(ctx, &lambda.GetLayerVersionPolicyInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(1)})
	ok(t, err, "GetLayerVersionPolicy")
	if want := `{"Version":"2012-10-17","Id":"default","Statement":[` + statement + `]}`; aws.ToString(policy.Policy) != want {
		t.Errorf("GetLayerVersionPolicy = %s, want %s", aws.ToString(policy.Policy), want)
	}

	_, err = client.AddLayerVersionPermission(ctx, &lambda.AddLayerVersionPermissionInput{
		LayerName: aws.String(layerName), VersionNumber: aws.Int64(1), StatementId: aws.String("account"), Action: aws.String("lambda:GetLayerVersion"), Principal: aws.String("*"),
	})
	errAs[*types.ResourceConflictException](t, err, "AddLayerVersionPermission with an existing statement id")
	wantStatus(t, err, "AddLayerVersionPermission with an existing statement id", 409)
	wantMessage(t, err, "AddLayerVersionPermission with an existing statement id",
		"The statement id (account) provided already exists. Please provide a new statement id, or remove the existing statement.")
	_, err = client.AddLayerVersionPermission(ctx, &lambda.AddLayerVersionPermissionInput{
		LayerName: aws.String(layerName), VersionNumber: aws.Int64(1), StatementId: aws.String("other"), Action: aws.String("lambda:GetLayerVersion"), Principal: aws.String("*"), RevisionId: aws.String("wrong"),
	})
	errAs[*types.PreconditionFailedException](t, err, "AddLayerVersionPermission with a stale RevisionId")
	wantStatus(t, err, "AddLayerVersionPermission with a stale RevisionId", 412)

	_, err = client.RemoveLayerVersionPermission(ctx, &lambda.RemoveLayerVersionPermissionInput{
		LayerName: aws.String(layerName), VersionNumber: aws.Int64(1), StatementId: aws.String("account"), RevisionId: policy.RevisionId,
	})
	ok(t, err, "RemoveLayerVersionPermission")
	_, err = client.RemoveLayerVersionPermission(ctx, &lambda.RemoveLayerVersionPermissionInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(1), StatementId: aws.String("account")})
	errAs[*types.ResourceNotFoundException](t, err, "RemoveLayerVersionPermission of a removed statement")
	wantMessage(t, err, "RemoveLayerVersionPermission of a removed statement", "Statement account is not found in resource policy.")
}

// A function names its layers by version ARN: the configuration lists
// them as {Arn, CodeSize} (7.2: test_lambda_api.snapshot.json:L8631-L8636),
// and the function imports a module only the layer holds, which Lambda
// extracts under /opt where the Python runtime searches (8.1, 8.2). The
// recorded refusals of 7.1 apply.
func TestLambdaFunctionWithALayer(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	layerName := name(t, "greet")
	archive := greetLayer(t)
	layer := publishLayer(t, client, layerName, archive)
	fnName := name(t, "layered")
	role := createRole(t, fnName+"-role", lambdaTrustPolicy)
	created, err := client.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName), Role: role.Arn, Runtime: types.RuntimePython313, Handler: aws.String("handler.handler"), Timeout: aws.Int32(5),
		Code:   &types.FunctionCode{ZipFile: zipOf(t, map[string]string{"handler.py": "import greet\n\n\ndef handler(event, context):\n    return {'greeting': greet.hello()}\n"})},
		Layers: []string{aws.ToString(layer.LayerVersionArn)},
	})
	ok(t, err, "CreateFunction with a layer")
	t.Cleanup(func() {
		_, _ = client.DeleteFunction(context.Background(), &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})
	if len(created.Layers) != 1 || aws.ToString(created.Layers[0].Arn) != aws.ToString(layer.LayerVersionArn) || created.Layers[0].CodeSize != int64(len(archive)) {
		t.Errorf("CreateFunction Layers = %+v, want the layer version with CodeSize %d", created.Layers, len(archive))
	}
	err = lambda.NewFunctionActiveV2Waiter(client).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)}, 2*time.Minute)
	ok(t, err, "waiting for "+fnName+" to be Active")
	out, err := client.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName), Payload: []byte(`{}`)})
	ok(t, err, "Invoke")
	var got struct{ Greeting string }
	if out.FunctionError != nil || json.Unmarshal(out.Payload, &got) != nil || got.Greeting != "hello from the layer" {
		t.Errorf("Invoke = %v %s, want the layer module's greeting", out.FunctionError, out.Payload)
	}

	layerArn := strings.TrimSuffix(aws.ToString(layer.LayerVersionArn), ":1")
	for _, c := range []struct {
		what   string
		layers []string
		want   string
	}{
		{"a version that does not exist", []string{layerArn + ":9"}, "Layer version " + layerArn + ":9 does not exist."},
		{
			"one version twice",
			[]string{layerArn + ":1", layerArn + ":1"},
			"Two different versions of the same layer are not allowed to be referenced in the same function. " + layerArn + ":1 and " + layerArn + ":1 are versions of the same layer.",
		},
	} {
		_, err := client.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{FunctionName: aws.String(fnName), Layers: c.layers})
		errAs[*types.InvalidParameterValueException](t, err, "UpdateFunctionConfiguration with "+c.what)
		wantMessage(t, err, "UpdateFunctionConfiguration with "+c.what, c.want)
	}
}

// receiveRecord waits for one message in queueURL and decodes it as an
// invocation record.
func receiveRecord(t *testing.T, queueURL string) map[string]json.RawMessage {
	t.Helper()
	client := sqs.NewFromConfig(awsCfg)
	var body string
	eventually(t, time.Minute, 500*time.Millisecond, "a record in "+queueURL, func() (bool, string) {
		out, err := client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
		})
		if err != nil {
			return false, err.Error()
		}
		if len(out.Messages) == 0 {
			return false, "no message yet"
		}
		body = aws.ToString(out.Messages[0].Body)
		return true, ""
	})
	var rec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &rec); err != nil {
		t.Fatalf("record %q is not JSON: %v", body, err)
	}
	return rec
}

// The event-invoke configuration takes SQS destinations and refuses a
// destination function that does not exist with AWS's recorded text
// (lambda-api.md 7.7). An asynchronous success then sends a Success record
// to the on-success queue, and a failure with MaximumRetryAttempts 0 a
// RetriesExhausted record to the on-failure queue (3.4:
// test_lambda_destinations.snapshot.json:L87-L175).
func TestLambdaEventInvokeDestinations(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := lambda.NewFromConfig(awsCfg)
	successURL := createQueue(t, name(t, "on-success"), nil)
	failureURL := createQueue(t, name(t, "on-failure"), nil)
	echo := deployFunction(t, name(t, "dest-echo"), "handler.echo", nil)
	failing := deployFunction(t, name(t, "dest-fail"), "handler.fail", nil)

	missing := "arn:aws:lambda:" + region() + ":" + account(t) + ":function:" + name(t, "doesnotexist")
	_, err := client.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{
		FunctionName: echo.FunctionName, DestinationConfig: &types.DestinationConfig{OnSuccess: &types.OnSuccess{Destination: aws.String(missing)}},
	})
	errAs[*types.InvalidParameterValueException](t, err, "PutFunctionEventInvokeConfig with a missing destination function")
	wantMessage(t, err, "PutFunctionEventInvokeConfig with a missing destination function", "The destination ARN "+missing+" is invalid.")

	for _, fn := range []*lambda.GetFunctionConfigurationOutput{echo, failing} {
		put, err := client.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{
			FunctionName: fn.FunctionName, MaximumRetryAttempts: aws.Int32(0),
			DestinationConfig: &types.DestinationConfig{
				OnSuccess: &types.OnSuccess{Destination: aws.String(queueArn(t, successURL))},
				OnFailure: &types.OnFailure{Destination: aws.String(queueArn(t, failureURL))},
			},
		})
		ok(t, err, "PutFunctionEventInvokeConfig with destinations")
		if put.DestinationConfig == nil || aws.ToString(put.DestinationConfig.OnSuccess.Destination) != queueArn(t, successURL) ||
			aws.ToString(put.DestinationConfig.OnFailure.Destination) != queueArn(t, failureURL) {
			t.Errorf("PutFunctionEventInvokeConfig = %+v, want both destinations", put.DestinationConfig)
		}
	}

	for _, c := range []struct {
		fn        *lambda.GetFunctionConfigurationOutput
		queueURL  string
		condition string
	}{
		{echo, successURL, `"Success"`},
		{failing, failureURL, `"RetriesExhausted"`},
	} {
		inv, err := client.Invoke(ctx, &lambda.InvokeInput{FunctionName: c.fn.FunctionName, InvocationType: types.InvocationTypeEvent, Payload: []byte(`{"n":1}`)})
		ok(t, err, "Invoke Event")
		if inv.StatusCode != http.StatusAccepted {
			t.Errorf("Invoke Event = %d, want 202", inv.StatusCode)
		}
		rec := receiveRecord(t, c.queueURL)
		var rc map[string]json.RawMessage
		if err := json.Unmarshal(rec["requestContext"], &rc); err != nil {
			t.Fatalf("requestContext %s: %v", rec["requestContext"], err)
		}
		if string(rc["condition"]) != c.condition || string(rc["approximateInvokeCount"]) != "1" ||
			string(rc["functionArn"]) != `"`+aws.ToString(c.fn.FunctionArn)+`:$LATEST"` || string(rec["requestPayload"]) != `{"n":1}` {
			t.Errorf("record to %s = %v / %v, want condition %s, count 1, the function's ARN with $LATEST and the event", c.queueURL, rec, rc, c.condition)
		}
	}
}
