package gosdk_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	gwtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

// Research: docs/research/apigateway-rest.md (sections 1 to 6). Error
// texts are asserted only where LocalStack's AWS-validated snapshots
// record them (apigateway-rest.md 2.4).

// createRestAPI creates a REST API and deletes it when the test ends.
func createRestAPI(t *testing.T, client *apigateway.Client, in *apigateway.CreateRestApiInput) *apigateway.CreateRestApiOutput {
	t.Helper()
	out, err := client.CreateRestApi(t.Context(), in)
	ok(t, err, "CreateRestApi "+aws.ToString(in.Name))
	t.Cleanup(func() {
		_, _ = client.DeleteRestApi(context.Background(), &apigateway.DeleteRestApiInput{RestApiId: out.Id})
	})
	return out
}

// createRestResource creates the path part under parent and returns its id.
func createRestResource(t *testing.T, client *apigateway.Client, apiID, parent, part string) string {
	t.Helper()
	out, err := client.CreateResource(t.Context(), &apigateway.CreateResourceInput{RestApiId: aws.String(apiID), ParentId: aws.String(parent), PathPart: aws.String(part)})
	ok(t, err, "CreateResource "+part)
	return aws.ToString(out.Id)
}

// restAPIResponse is one data-plane answer.
type restAPIResponse struct {
	status int
	header http.Header
	body   []byte
}

// callRestAPI sends method to path of the API through the path fallback
// /_understudy/execute-api/<api-id>/<stage>/<rest> (apigateway-rest.md 4.1).
func callRestAPI(t *testing.T, method, apiID, path string) restAPIResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, endpoint.String()+"/_understudy/execute-api/"+apiID+path, nil)
	ok(t, err, "building the request")
	resp, err := http.DefaultClient.Do(req)
	ok(t, err, "calling "+method+" "+path)
	return restAPIResponse{status: resp.StatusCode, header: resp.Header, body: readAll(t, resp.Body, "the API's response")}
}

// CreateRestApi answers AWS's create defaults (apigateway-rest.md 2.3:
// apiKeySource HEADER, disableExecuteApiEndpoint false, an EDGE endpoint)
// and makes the root resource "/" with no parentId (3.1); UpdateRestApi
// applies a patch, GetRestApis lists it, and an upper-cased or deleted id
// is NotFoundException 404 with the recorded text (2.4).
func TestAPIGatewayRestApis(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigateway.NewFromConfig(awsCfg)
	created := createRestAPI(t, client, &apigateway.CreateRestApiInput{Name: aws.String(name(t, "conf-rest")), Description: aws.String("first")})
	id := aws.ToString(created.Id)
	if created.ApiKeySource != gwtypes.ApiKeySourceTypeHeader || created.DisableExecuteApiEndpoint ||
		created.EndpointConfiguration == nil || len(created.EndpointConfiguration.Types) != 1 || created.EndpointConfiguration.Types[0] != gwtypes.EndpointTypeEdge {
		t.Errorf("CreateRestApi = apiKeySource %q, disableExecuteApiEndpoint %v, endpointConfiguration %+v; want HEADER, false, [EDGE]",
			created.ApiKeySource, created.DisableExecuteApiEndpoint, created.EndpointConfiguration)
	}
	resources, err := client.GetResources(ctx, &apigateway.GetResourcesInput{RestApiId: aws.String(id)})
	ok(t, err, "GetResources")
	if len(resources.Items) != 1 || aws.ToString(resources.Items[0].Id) != aws.ToString(created.RootResourceId) ||
		aws.ToString(resources.Items[0].Path) != "/" || resources.Items[0].ParentId != nil {
		t.Errorf("GetResources = %+v, want only the root resource %s at / with no parentId", resources.Items, aws.ToString(created.RootResourceId))
	}
	updated, err := client.UpdateRestApi(ctx, &apigateway.UpdateRestApiInput{
		RestApiId: aws.String(id), PatchOperations: []gwtypes.PatchOperation{{Op: gwtypes.OpReplace, Path: aws.String("/description"), Value: aws.String("second")}},
	})
	ok(t, err, "UpdateRestApi")
	if aws.ToString(updated.Description) != "second" {
		t.Errorf("UpdateRestApi description = %q, want second", aws.ToString(updated.Description))
	}
	listed, err := client.GetRestApis(ctx, &apigateway.GetRestApisInput{Limit: aws.Int32(500)})
	ok(t, err, "GetRestApis")
	found := false
	for _, a := range listed.Items {
		if aws.ToString(a.Id) == id {
			found = aws.ToString(a.Description) == "second"
		}
	}
	if !found {
		t.Errorf("GetRestApis does not list %s with description second", id)
	}

	if upper := strings.ToUpper(id); upper != id {
		_, err = client.GetRestApi(ctx, &apigateway.GetRestApiInput{RestApiId: aws.String(upper)})
		errAs[*gwtypes.NotFoundException](t, err, "GetRestApi upper-cased")
		wantStatus(t, err, "GetRestApi upper-cased", http.StatusNotFound)
		wantMessage(t, err, "GetRestApi upper-cased", "Invalid API identifier specified "+account(t)+":"+upper)
	}
	_, err = client.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: aws.String(id)})
	ok(t, err, "DeleteRestApi")
	_, err = client.GetRestApi(ctx, &apigateway.GetRestApiInput{RestApiId: aws.String(id)})
	errAs[*gwtypes.NotFoundException](t, err, "GetRestApi after DeleteRestApi")
	wantMessage(t, err, "GetRestApi after DeleteRestApi", "Invalid API identifier specified "+account(t)+":"+id)
}

// The recorded refusals of the control plane (apigateway-rest.md 2.4): an
// empty description, a duplicate sibling, a child of a greedy resource, an
// unknown integration type; ImportRestApi is a valid request understudy
// does not emulate (the unit's conflict row 4).
func TestAPIGatewayRestRefusals(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigateway.NewFromConfig(awsCfg)
	_, err := client.CreateRestApi(ctx, &apigateway.CreateRestApiInput{Name: aws.String(name(t, "conf-rest")), Description: aws.String("")})
	errAs[*gwtypes.BadRequestException](t, err, "CreateRestApi with an empty description")
	wantMessage(t, err, "CreateRestApi with an empty description", "Description cannot be an empty string")

	created := createRestAPI(t, client, &apigateway.CreateRestApiInput{Name: aws.String(name(t, "conf-rest"))})
	id, root := aws.ToString(created.Id), aws.ToString(created.RootResourceId)
	createRestResource(t, client, id, root, "pets")
	_, err = client.CreateResource(ctx, &apigateway.CreateResourceInput{RestApiId: aws.String(id), ParentId: aws.String(root), PathPart: aws.String("pets")})
	errAs[*gwtypes.ConflictException](t, err, "CreateResource pets again")
	wantStatus(t, err, "CreateResource pets again", http.StatusConflict)
	wantMessage(t, err, "CreateResource pets again", "Another resource with the same parent already has this name: pets")
	greedy := createRestResource(t, client, id, root, "{proxy+}")
	_, err = client.CreateResource(ctx, &apigateway.CreateResourceInput{RestApiId: aws.String(id), ParentId: aws.String(greedy), PathPart: aws.String("child")})
	errAs[*gwtypes.BadRequestException](t, err, "CreateResource under {proxy+}")
	wantMessage(t, err, "CreateResource under {proxy+}", "Cannot create a child of a resource with a greedy path variable: {proxy+}")

	_, err = client.PutMethod(ctx, &apigateway.PutMethodInput{RestApiId: aws.String(id), ResourceId: aws.String(root), HttpMethod: aws.String("GET"), AuthorizationType: aws.String("NONE")})
	ok(t, err, "PutMethod")
	_, err = client.PutIntegration(ctx, &apigateway.PutIntegrationInput{RestApiId: aws.String(id), ResourceId: aws.String(root), HttpMethod: aws.String("GET"), Type: gwtypes.IntegrationType("HTTPS_PROXY")})
	apiErr(t, err, "PutIntegration HTTPS_PROXY", "ValidationException")
	wantStatus(t, err, "PutIntegration HTTPS_PROXY", http.StatusBadRequest)

	_, err = client.ImportRestApi(ctx, &apigateway.ImportRestApiInput{Body: []byte(`{"openapi":"3.0.1","info":{"title":"t","version":"1"},"paths":{}}`)})
	apiErr(t, err, "ImportRestApi", "NotImplemented")
	wantStatus(t, err, "ImportRestApi", http.StatusNotImplemented)
}

// A MOCK method with a literal request template selects its status and a
// literal response template is the body (apigateway-rest.md 6.3, spec 6b
// VTL decision), with MOCK's recorded defaults (2.3); an unknown path or
// method is 403 Missing Authentication Token (finding 3); a deployment a
// stage points at cannot be deleted (finding 12); stages answer their
// recorded defaults (2.3).
func TestAPIGatewayRestMockAndDeployments(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigateway.NewFromConfig(awsCfg)
	created := createRestAPI(t, client, &apigateway.CreateRestApiInput{Name: aws.String(name(t, "conf-rest"))})
	id := aws.ToString(created.Id)
	items := createRestResource(t, client, id, aws.ToString(created.RootResourceId), "items")
	get := aws.String("GET")
	_, err := client.PutMethod(ctx, &apigateway.PutMethodInput{RestApiId: aws.String(id), ResourceId: aws.String(items), HttpMethod: get, AuthorizationType: aws.String("NONE")})
	ok(t, err, "PutMethod")
	_, err = client.PutMethodResponse(ctx, &apigateway.PutMethodResponseInput{RestApiId: aws.String(id), ResourceId: aws.String(items), HttpMethod: get, StatusCode: aws.String("200")})
	ok(t, err, "PutMethodResponse")
	integ, err := client.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId: aws.String(id), ResourceId: aws.String(items), HttpMethod: get, Type: gwtypes.IntegrationTypeMock,
		RequestTemplates: map[string]string{"application/json": `{"statusCode": 200}`},
	})
	ok(t, err, "PutIntegration MOCK")
	if integ.HttpMethod != nil || aws.ToString(integ.PassthroughBehavior) != "WHEN_NO_MATCH" || integ.TimeoutInMillis != 29000 {
		t.Errorf("PutIntegration MOCK = httpMethod %v, passthroughBehavior %q, timeoutInMillis %d; want none, WHEN_NO_MATCH, 29000",
			integ.HttpMethod, aws.ToString(integ.PassthroughBehavior), integ.TimeoutInMillis)
	}
	_, err = client.PutIntegrationResponse(ctx, &apigateway.PutIntegrationResponseInput{
		RestApiId: aws.String(id), ResourceId: aws.String(items), HttpMethod: get, StatusCode: aws.String("200"),
		ResponseTemplates: map[string]string{"application/json": `{"mock": true}`},
	})
	ok(t, err, "PutIntegrationResponse")
	dep, err := client.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{RestApiId: aws.String(id), StageName: aws.String("s")})
	ok(t, err, "CreateDeployment")
	stages, err := client.GetStages(ctx, &apigateway.GetStagesInput{RestApiId: aws.String(id)})
	ok(t, err, "GetStages")
	if len(stages.Item) != 1 || aws.ToString(stages.Item[0].StageName) != "s" || aws.ToString(stages.Item[0].DeploymentId) != aws.ToString(dep.Id) ||
		stages.Item[0].CacheClusterStatus != gwtypes.CacheClusterStatusNotAvailable || stages.Item[0].TracingEnabled || stages.Item[0].MethodSettings == nil {
		t.Errorf("GetStages = %+v, want stage s on %s with cacheClusterStatus NOT_AVAILABLE, tracingEnabled false and methodSettings {}", stages.Item, aws.ToString(dep.Id))
	}

	got := callRestAPI(t, http.MethodGet, id, "/s/items")
	var mock map[string]bool
	if got.status != http.StatusOK || json.Unmarshal(got.body, &mock) != nil || !maps.Equal(mock, map[string]bool{"mock": true}) {
		t.Errorf("GET /s/items = %d %s, want 200 {\"mock\": true}", got.status, got.body)
	}
	for _, c := range []struct{ method, path string }{{http.MethodGet, "/s/nothing"}, {http.MethodPost, "/s/items"}} {
		got := callRestAPI(t, c.method, id, c.path)
		var body map[string]string
		if got.status != http.StatusForbidden || json.Unmarshal(got.body, &body) != nil || body["message"] != "Missing Authentication Token" ||
			got.header.Get("x-amzn-ErrorType") != "MissingAuthenticationTokenException" {
			t.Errorf("%s %s = %d %s (x-amzn-ErrorType %q), want 403 Missing Authentication Token",
				c.method, c.path, got.status, got.body, got.header.Get("x-amzn-ErrorType"))
		}
	}

	_, err = client.DeleteDeployment(ctx, &apigateway.DeleteDeploymentInput{RestApiId: aws.String(id), DeploymentId: dep.Id})
	errAs[*gwtypes.BadRequestException](t, err, "DeleteDeployment of a live deployment")
	wantMessage(t, err, "DeleteDeployment of a live deployment", "Active stages pointing to this deployment must be moved or deleted")
	_, err = client.DeleteStage(ctx, &apigateway.DeleteStageInput{RestApiId: aws.String(id), StageName: aws.String("s")})
	ok(t, err, "DeleteStage")
	_, err = client.DeleteDeployment(ctx, &apigateway.DeleteDeploymentInput{RestApiId: aws.String(id), DeploymentId: dep.Id})
	ok(t, err, "DeleteDeployment after DeleteStage")
}

// TagResource, GetTags and UntagResource take the REST API's ARN as a path
// label the SDK percent-encodes (apigateway-rest.md 3.9, 3.10); tags given
// at create are GetRestApi's.
func TestAPIGatewayRestTags(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigateway.NewFromConfig(awsCfg)
	created := createRestAPI(t, client, &apigateway.CreateRestApiInput{Name: aws.String(name(t, "conf-rest")), Tags: map[string]string{"team": "a"}})
	arn := "arn:aws:apigateway:" + region() + "::/restapis/" + aws.ToString(created.Id)
	_, err := client.TagResource(ctx, &apigateway.TagResourceInput{ResourceArn: aws.String(arn), Tags: map[string]string{"env": "dev"}})
	ok(t, err, "TagResource")
	tags, err := client.GetTags(ctx, &apigateway.GetTagsInput{ResourceArn: aws.String(arn)})
	ok(t, err, "GetTags")
	if !maps.Equal(tags.Tags, map[string]string{"team": "a", "env": "dev"}) {
		t.Errorf("GetTags = %v, want team=a env=dev", tags.Tags)
	}
	_, err = client.UntagResource(ctx, &apigateway.UntagResourceInput{ResourceArn: aws.String(arn), TagKeys: []string{"team"}})
	ok(t, err, "UntagResource")
	got, err := client.GetRestApi(ctx, &apigateway.GetRestApiInput{RestApiId: created.Id})
	ok(t, err, "GetRestApi")
	if !maps.Equal(got.Tags, map[string]string{"env": "dev"}) {
		t.Errorf("GetRestApi tags = %v, want env=dev", got.Tags)
	}
}

// restHandler answers an API Gateway Lambda proxy request with what it saw
// in the REST 1.0 event (apigateway-rest.md 5.1), as a proxy response (5.2).
const restHandler = `import json

def handler(event, context):
    ctx = event["requestContext"]
    return {
        "statusCode": 201,
        "headers": {"content-type": "application/json", "x-conformance": "yes"},
        "body": json.dumps({
            "resource": event["resource"],
            "path": event["path"],
            "method": event["httpMethod"],
            "stage": ctx["stage"],
            "id": event["pathParameters"]["id"],
        }),
    }
`

// deployRestHandler creates a python3.13 function running restHandler and
// deletes it when the test ends.
func deployRestHandler(t *testing.T, fnName string) string {
	t.Helper()
	ctx := t.Context()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("handler.py")
	ok(t, err, "zip Create")
	_, err = io.WriteString(f, restHandler)
	ok(t, err, "zip Write")
	ok(t, w.Close(), "zip Close")
	client := lambda.NewFromConfig(awsCfg)
	role := createRole(t, fnName+"-role", lambdaTrustPolicy)
	out, err := client.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName), Role: role.Arn, Runtime: types.RuntimePython313, Handler: aws.String("handler.handler"),
		Code: &types.FunctionCode{ZipFile: buf.Bytes()}, Timeout: aws.Int32(10),
	})
	ok(t, err, "CreateFunction "+fnName)
	t.Cleanup(func() {
		_, _ = client.DeleteFunction(context.Background(), &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})
	err = lambda.NewFunctionActiveV2Waiter(client).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)}, 2*time.Minute)
	ok(t, err, "waiting for "+fnName+" to be Active")
	return aws.ToString(out.FunctionArn)
}

// An AWS_PROXY method on /items/{id} reaches a real function through the
// path fallback, with the permission "${execution_arn}/*/*" for
// apigateway.amazonaws.com (apigateway-rest.md 5.4); the function sees the
// REST 1.0 event (5.1) and its proxy response is the client's (5.2).
func TestAPIGatewayRestInvokesLambda(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigateway.NewFromConfig(awsCfg)
	fnName := name(t, "conf-apigw1")
	fnARN := deployRestHandler(t, fnName)
	created := createRestAPI(t, client, &apigateway.CreateRestApiInput{Name: aws.String(name(t, "conf-rest"))})
	id := aws.ToString(created.Id)
	item := createRestResource(t, client, id, createRestResource(t, client, id, aws.ToString(created.RootResourceId), "items"), "{id}")
	_, err := client.PutMethod(ctx, &apigateway.PutMethodInput{RestApiId: aws.String(id), ResourceId: aws.String(item), HttpMethod: aws.String("GET"), AuthorizationType: aws.String("NONE")})
	ok(t, err, "PutMethod")
	_, err = client.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId: aws.String(id), ResourceId: aws.String(item), HttpMethod: aws.String("GET"), Type: gwtypes.IntegrationTypeAwsProxy,
		IntegrationHttpMethod: aws.String("POST"), Uri: aws.String("arn:aws:apigateway:" + region() + ":lambda:path/2015-03-31/functions/" + fnARN + "/invocations"),
	})
	ok(t, err, "PutIntegration AWS_PROXY")
	_, err = lambda.NewFromConfig(awsCfg).AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(fnName), StatementId: aws.String("apigateway"), Action: aws.String("lambda:InvokeFunction"),
		Principal: aws.String("apigateway.amazonaws.com"), SourceArn: aws.String("arn:aws:execute-api:" + region() + ":" + account(t) + ":" + id + "/*/*"),
	})
	ok(t, err, "AddPermission for API Gateway")
	_, err = client.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{RestApiId: aws.String(id), StageName: aws.String("live")})
	ok(t, err, "CreateDeployment")

	got := callRestAPI(t, http.MethodGet, id, "/live/items/42")
	if got.status != http.StatusCreated || got.header.Get("x-conformance") != "yes" || got.header.Get("x-amzn-RequestId") == "" {
		t.Fatalf("GET /live/items/42 = %d (x-conformance %q, x-amzn-RequestId %q): %s; want the function's 201",
			got.status, got.header.Get("x-conformance"), got.header.Get("x-amzn-RequestId"), got.body)
	}
	var seen struct{ Resource, Path, Method, Stage, ID string }
	ok(t, json.Unmarshal(got.body, &seen), "decoding the function's view")
	want := struct{ Resource, Path, Method, Stage, ID string }{"/items/{id}", "/items/42", http.MethodGet, "live", "42"}
	if seen != want {
		t.Errorf("the function saw %+v, want %+v", seen, want)
	}
}
