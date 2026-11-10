package gosdk_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

// Research: docs/research/apigateway-http.md (sections 2 to 5, 9 and 11).
// Error texts are not asserted: API Gateway v2's are UNVERIFIED
// (apigateway-http.md 2.3), so each error check is its type and status.

// createHTTPAPI creates an HTTP API and deletes it when the test ends.
func createHTTPAPI(t *testing.T, client *apigatewayv2.Client, apiName string) *apigatewayv2.CreateApiOutput {
	t.Helper()
	out, err := client.CreateApi(t.Context(), &apigatewayv2.CreateApiInput{Name: aws.String(apiName), ProtocolType: agtypes.ProtocolTypeHttp})
	ok(t, err, "CreateApi "+apiName)
	t.Cleanup(func() {
		_, _ = client.DeleteApi(context.Background(), &apigatewayv2.DeleteApiInput{ApiId: out.ApiId})
	})
	return out
}

// CreateApi answers AWS's create defaults (apigateway-http.md 3.1:
// ApiKeySelectionExpression $request.header.x-api-key and
// RouteSelectionExpression $request.method $request.path) and an
// ApiEndpoint on the emulator's host, never amazonaws.com (11.3);
// GetApi, GetApis, UpdateApi and DeleteApi act on it, and a deleted API
// is NotFoundException 404 (2.2, 2.3).
func TestAPIGatewayV2Apis(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigatewayv2.NewFromConfig(awsCfg)
	created := createHTTPAPI(t, client, name(t, "conf-api"))
	id := aws.ToString(created.ApiId)
	if aws.ToString(created.ApiKeySelectionExpression) != "$request.header.x-api-key" || aws.ToString(created.RouteSelectionExpression) != "$request.method $request.path" {
		t.Errorf("CreateApi selection expressions %q, %q; want $request.header.x-api-key and $request.method $request.path",
			aws.ToString(created.ApiKeySelectionExpression), aws.ToString(created.RouteSelectionExpression))
	}
	endpointRE := regexp.MustCompile(`^https?://` + regexp.QuoteMeta(id) + `\.execute-api\.` + regexp.QuoteMeta(region()) + `\.[^/]+$`)
	if ep := aws.ToString(created.ApiEndpoint); !endpointRE.MatchString(ep) || strings.Contains(ep, "amazonaws.com") {
		t.Errorf("ApiEndpoint = %q, want <scheme>://%s.execute-api.%s.<emulator host> with no amazonaws.com", ep, id, region())
	}
	got, err := client.GetApi(ctx, &apigatewayv2.GetApiInput{ApiId: aws.String(id)})
	ok(t, err, "GetApi")
	if aws.ToString(got.Name) != aws.ToString(created.Name) || got.ProtocolType != agtypes.ProtocolTypeHttp || aws.ToString(got.ApiEndpoint) != aws.ToString(created.ApiEndpoint) {
		t.Errorf("GetApi = %+v, want what CreateApi answered", got)
	}
	_, err = client.UpdateApi(ctx, &apigatewayv2.UpdateApiInput{ApiId: aws.String(id), Description: aws.String("updated")})
	ok(t, err, "UpdateApi")
	listed := false
	pager := apigatewayv2.GetApisInput{MaxResults: aws.String("50")}
	for range 100 {
		page, err := client.GetApis(ctx, &pager)
		ok(t, err, "GetApis")
		for _, a := range page.Items {
			if aws.ToString(a.ApiId) == id {
				listed = true
				if aws.ToString(a.Description) != "updated" {
					t.Errorf("GetApis lists %s with Description %q, want updated", id, aws.ToString(a.Description))
				}
			}
		}
		if page.NextToken == nil {
			break
		}
		pager.NextToken = page.NextToken
	}
	if !listed {
		t.Errorf("GetApis does not list %s", id)
	}

	_, err = client.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(id)})
	ok(t, err, "DeleteApi")
	_, err = client.GetApi(ctx, &apigatewayv2.GetApiInput{ApiId: aws.String(id)})
	errAs[*agtypes.NotFoundException](t, err, "GetApi after DeleteApi")
	wantStatus(t, err, "GetApi after DeleteApi", http.StatusNotFound)
}

// TagResource, GetTags and UntagResource take the API's ARN as a path
// label, which the SDK percent-encodes (apigateway-http.md 2.2); tags given
// at create are GetApi's (3.6).
func TestAPIGatewayV2Tags(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigatewayv2.NewFromConfig(awsCfg)
	created, err := client.CreateApi(ctx, &apigatewayv2.CreateApiInput{Name: aws.String(name(t, "conf-tags")), ProtocolType: agtypes.ProtocolTypeHttp, Tags: map[string]string{"team": "a"}})
	ok(t, err, "CreateApi with tags")
	t.Cleanup(func() {
		_, _ = client.DeleteApi(context.Background(), &apigatewayv2.DeleteApiInput{ApiId: created.ApiId})
	})
	arn := "arn:aws:apigateway:" + region() + "::/apis/" + aws.ToString(created.ApiId)
	_, err = client.TagResource(ctx, &apigatewayv2.TagResourceInput{ResourceArn: aws.String(arn), Tags: map[string]string{"env": "dev"}})
	ok(t, err, "TagResource")
	tags, err := client.GetTags(ctx, &apigatewayv2.GetTagsInput{ResourceArn: aws.String(arn)})
	ok(t, err, "GetTags")
	if !maps.Equal(tags.Tags, map[string]string{"team": "a", "env": "dev"}) {
		t.Errorf("GetTags = %v, want team=a env=dev", tags.Tags)
	}
	_, err = client.UntagResource(ctx, &apigatewayv2.UntagResourceInput{ResourceArn: aws.String(arn), TagKeys: []string{"team"}})
	ok(t, err, "UntagResource")
	got, err := client.GetApi(ctx, &apigatewayv2.GetApiInput{ApiId: created.ApiId})
	ok(t, err, "GetApi")
	if !maps.Equal(got.Tags, map[string]string{"env": "dev"}) {
		t.Errorf("GetApi Tags = %v, want env=dev", got.Tags)
	}
}

// Routes, integrations, stages and deployments: the defaults AWS's CLI
// examples and Terraform's acceptance tests show (apigateway-http.md 3.2:
// ApiKeyRequired false and AuthorizationType NONE; 3.3: IntegrationMethod
// POST, ConnectionType INTERNET, TimeoutInMillis 30000; 3.4: an automatic
// deployment's status message; 3.5: DEPLOYED deployments), and the errors:
// a duplicate route key ConflictException 409, a timeout outside 50..30000
// BadRequestException 400 (2.4), a missing route NotFoundException 404, a
// WebSocket API NotImplemented 501.
func TestAPIGatewayV2RoutesStagesDeployments(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigatewayv2.NewFromConfig(awsCfg)
	id := createHTTPAPI(t, client, name(t, "conf-routes")).ApiId
	fnARN := "arn:aws:lambda:" + region() + ":" + account(t) + ":function:" + name(t, "conf-target")
	integ, err := client.CreateIntegration(ctx, &apigatewayv2.CreateIntegrationInput{
		ApiId: id, IntegrationType: agtypes.IntegrationTypeAwsProxy, IntegrationUri: aws.String(fnARN), PayloadFormatVersion: aws.String("2.0"),
	})
	ok(t, err, "CreateIntegration")
	if aws.ToString(integ.IntegrationMethod) != "POST" || integ.ConnectionType != agtypes.ConnectionTypeInternet || aws.ToInt32(integ.TimeoutInMillis) != 30000 || aws.ToString(integ.IntegrationUri) != fnARN {
		t.Errorf("CreateIntegration = method %q, connection %q, timeout %d, uri %q; want POST, INTERNET, 30000, %s",
			aws.ToString(integ.IntegrationMethod), integ.ConnectionType, aws.ToInt32(integ.TimeoutInMillis), aws.ToString(integ.IntegrationUri), fnARN)
	}
	target := "integrations/" + aws.ToString(integ.IntegrationId)
	route, err := client.CreateRoute(ctx, &apigatewayv2.CreateRouteInput{ApiId: id, RouteKey: aws.String("GET /pets/{id}"), Target: aws.String(target)})
	ok(t, err, "CreateRoute")
	if aws.ToBool(route.ApiKeyRequired) || route.AuthorizationType != agtypes.AuthorizationTypeNone || aws.ToString(route.Target) != target {
		t.Errorf("CreateRoute = ApiKeyRequired %v, AuthorizationType %q, Target %q; want false, NONE, %s", aws.ToBool(route.ApiKeyRequired), route.AuthorizationType, aws.ToString(route.Target), target)
	}
	_, err = client.CreateRoute(ctx, &apigatewayv2.CreateRouteInput{ApiId: id, RouteKey: aws.String("GET /pets/{id}")})
	errAs[*agtypes.ConflictException](t, err, "CreateRoute with a key already in the API")
	wantStatus(t, err, "CreateRoute with a key already in the API", http.StatusConflict)
	_, err = client.GetRoute(ctx, &apigatewayv2.GetRouteInput{ApiId: id, RouteId: aws.String("zzzzzzz")})
	errAs[*agtypes.NotFoundException](t, err, "GetRoute of no route")
	_, err = client.UpdateIntegration(ctx, &apigatewayv2.UpdateIntegrationInput{ApiId: id, IntegrationId: integ.IntegrationId, TimeoutInMillis: aws.Int32(30001)})
	errAs[*agtypes.BadRequestException](t, err, "UpdateIntegration with TimeoutInMillis 30001")
	wantStatus(t, err, "UpdateIntegration with TimeoutInMillis 30001", http.StatusBadRequest)

	stage, err := client.CreateStage(ctx, &apigatewayv2.CreateStageInput{ApiId: id, StageName: aws.String("$default"), AutoDeploy: aws.Bool(true)})
	ok(t, err, "CreateStage $default with autoDeploy")
	dep := aws.ToString(stage.DeploymentId)
	if dep == "" || aws.ToString(stage.LastDeploymentStatusMessage) != "Successfully deployed stage with deployment ID '"+dep+"'" {
		t.Errorf("CreateStage DeploymentId %q, LastDeploymentStatusMessage %q; want an automatic deployment", dep, aws.ToString(stage.LastDeploymentStatusMessage))
	}
	deps, err := client.GetDeployments(ctx, &apigatewayv2.GetDeploymentsInput{ApiId: id})
	ok(t, err, "GetDeployments")
	if len(deps.Items) == 0 || !aws.ToBool(deps.Items[0].AutoDeployed) || deps.Items[0].DeploymentStatus != agtypes.DeploymentStatusDeployed {
		t.Errorf("GetDeployments = %+v, want an automatic DEPLOYED deployment", deps.Items)
	}
	_, err = client.CreateStage(ctx, &apigatewayv2.CreateStageInput{ApiId: id, StageName: aws.String("prod")})
	ok(t, err, "CreateStage prod")
	manual, err := client.CreateDeployment(ctx, &apigatewayv2.CreateDeploymentInput{ApiId: id, StageName: aws.String("prod"), Description: aws.String("v1")})
	ok(t, err, "CreateDeployment to prod")
	if aws.ToBool(manual.AutoDeployed) || manual.DeploymentStatus != agtypes.DeploymentStatusDeployed {
		t.Errorf("CreateDeployment = AutoDeployed %v, status %q; want false and DEPLOYED", aws.ToBool(manual.AutoDeployed), manual.DeploymentStatus)
	}
	prod, err := client.GetStage(ctx, &apigatewayv2.GetStageInput{ApiId: id, StageName: aws.String("prod")})
	ok(t, err, "GetStage prod")
	if aws.ToString(prod.DeploymentId) != aws.ToString(manual.DeploymentId) {
		t.Errorf("stage prod DeploymentId = %q, want %q", aws.ToString(prod.DeploymentId), aws.ToString(manual.DeploymentId))
	}

	_, err = client.CreateApi(ctx, &apigatewayv2.CreateApiInput{Name: aws.String(name(t, "conf-ws")), ProtocolType: agtypes.ProtocolTypeWebsocket, RouteSelectionExpression: aws.String("$request.body.action")})
	apiErr(t, err, "CreateApi WEBSOCKET", "NotImplemented")
	wantStatus(t, err, "CreateApi WEBSOCKET", http.StatusNotImplemented)
}

// apigwHandler answers an API Gateway Lambda proxy request with what it
// saw, in payload format 1.0 or 2.0 (apigateway-http.md 5.1, 5.2), as a
// structured response both formats read (5.3, 5.4).
const apigwHandler = `import json

def handler(event, context):
    ctx = event.get("requestContext", {})
    method = event.get("httpMethod") or ctx.get("http", {}).get("method")
    return {
        "statusCode": 200,
        "headers": {"content-type": "application/json", "x-conformance": "yes"},
        "body": json.dumps({"version": event.get("version"), "method": method, "stage": ctx.get("stage")}),
    }
`

// deployAPIGatewayHandler creates a python3.13 function running
// apigwHandler and deletes it when the test ends.
func deployAPIGatewayHandler(t *testing.T, fnName string) string {
	t.Helper()
	ctx := t.Context()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("handler.py")
	ok(t, err, "zip Create")
	_, err = io.WriteString(f, apigwHandler)
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

// An HTTP API invokes a Lambda proxy integration in each payload format
// through the execute-api path fallback /_understudy/execute-api/<api-id>/
// (apigateway-http.md 11.3): the function sees the request in that format
// and its response is the client's (5.1 to 5.4). The function's resource
// policy must allow apigateway.amazonaws.com with the API's execute-api
// source ARN, "${execution_arn}/*/*" here (5.5, 9.6).
func TestAPIGatewayV2InvokesLambda(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"1.0", "2.0"} {
		t.Run("payload "+version, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			client := apigatewayv2.NewFromConfig(awsCfg)
			fnName := name(t, "conf-apigw")
			fnARN := deployAPIGatewayHandler(t, fnName)
			id := createHTTPAPI(t, client, name(t, "conf-invoke")).ApiId
			integ, err := client.CreateIntegration(ctx, &apigatewayv2.CreateIntegrationInput{
				ApiId: id, IntegrationType: agtypes.IntegrationTypeAwsProxy, IntegrationUri: aws.String(fnARN), PayloadFormatVersion: aws.String(version),
			})
			ok(t, err, "CreateIntegration")
			_, err = client.CreateRoute(ctx, &apigatewayv2.CreateRouteInput{ApiId: id, RouteKey: aws.String("GET /hello"), Target: aws.String("integrations/" + aws.ToString(integ.IntegrationId))})
			ok(t, err, "CreateRoute")
			_, err = client.CreateStage(ctx, &apigatewayv2.CreateStageInput{ApiId: id, StageName: aws.String("$default"), AutoDeploy: aws.Bool(true)})
			ok(t, err, "CreateStage")
			_, err = lambda.NewFromConfig(awsCfg).AddPermission(ctx, &lambda.AddPermissionInput{
				FunctionName: aws.String(fnName), StatementId: aws.String("apigateway"), Action: aws.String("lambda:InvokeFunction"),
				Principal: aws.String("apigateway.amazonaws.com"),
				SourceArn: aws.String("arn:aws:execute-api:" + region() + ":" + account(t) + ":" + aws.ToString(id) + "/*/*"),
			})
			ok(t, err, "AddPermission for API Gateway")

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String()+"/_understudy/execute-api/"+aws.ToString(id)+"/hello", nil)
			ok(t, err, "building the request")
			resp, err := http.DefaultClient.Do(req)
			ok(t, err, "calling the API")
			body := readAll(t, resp.Body, "the API's response")
			if resp.StatusCode != http.StatusOK || resp.Header.Get("x-conformance") != "yes" || resp.Header.Get("apigw-requestid") == "" {
				t.Fatalf("GET /hello = %d (x-conformance %q, apigw-requestid %q): %s; want the function's 200",
					resp.StatusCode, resp.Header.Get("x-conformance"), resp.Header.Get("apigw-requestid"), body)
			}
			var seen struct{ Version, Method, Stage string }
			ok(t, json.Unmarshal(body, &seen), "decoding the function's view")
			if seen.Version != version || seen.Method != http.MethodGet || seen.Stage != "$default" {
				t.Errorf("the function saw version %q, method %q, stage %q; want %s, GET, $default", seen.Version, seen.Method, seen.Stage, version)
			}
		})
	}
}
