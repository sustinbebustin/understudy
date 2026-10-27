package gosdk_test

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
)

// The control-plane operations apigatewayv2_test.go does not call: the
// lists, updates and deletes of routes, integrations, stages and
// deployments, and the three deletes of an API's or stage's settings.
// Each delete answers 204 and the deleted resource is then
// NotFoundException 404 (apigateway-http.md 2.2, 2.3). Error texts are not
// asserted: they are UNVERIFIED (2.3).

// Routes and integrations are listed, updated and deleted (2.2); a route's
// key and target change on UpdateRoute (3.2).
func TestAPIGatewayV2RouteAndIntegrationLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigatewayv2.NewFromConfig(awsCfg)
	id := createHTTPAPI(t, client, name("conf-lifecycle")).ApiId
	fnARN := "arn:aws:lambda:" + region() + ":" + account(t) + ":function:" + name("conf-target")
	integ, err := client.CreateIntegration(ctx, &apigatewayv2.CreateIntegrationInput{
		ApiId: id, IntegrationType: agtypes.IntegrationTypeAwsProxy, IntegrationUri: aws.String(fnARN), PayloadFormatVersion: aws.String("2.0"),
	})
	ok(t, err, "CreateIntegration")
	got, err := client.GetIntegration(ctx, &apigatewayv2.GetIntegrationInput{ApiId: id, IntegrationId: integ.IntegrationId})
	ok(t, err, "GetIntegration")
	if aws.ToString(got.IntegrationUri) != fnARN || aws.ToString(got.PayloadFormatVersion) != "2.0" {
		t.Errorf("GetIntegration = uri %q, payload %q; want %s and 2.0", aws.ToString(got.IntegrationUri), aws.ToString(got.PayloadFormatVersion), fnARN)
	}
	updated, err := client.UpdateIntegration(ctx, &apigatewayv2.UpdateIntegrationInput{ApiId: id, IntegrationId: integ.IntegrationId, PayloadFormatVersion: aws.String("1.0"), TimeoutInMillis: aws.Int32(5000)})
	ok(t, err, "UpdateIntegration")
	if aws.ToString(updated.PayloadFormatVersion) != "1.0" || aws.ToInt32(updated.TimeoutInMillis) != 5000 {
		t.Errorf("UpdateIntegration = payload %q, timeout %d; want 1.0 and 5000", aws.ToString(updated.PayloadFormatVersion), aws.ToInt32(updated.TimeoutInMillis))
	}
	integs, err := client.GetIntegrations(ctx, &apigatewayv2.GetIntegrationsInput{ApiId: id})
	ok(t, err, "GetIntegrations")
	if len(integs.Items) != 1 || aws.ToString(integs.Items[0].IntegrationId) != aws.ToString(integ.IntegrationId) {
		t.Errorf("GetIntegrations = %+v, want the one integration %s", integs.Items, aws.ToString(integ.IntegrationId))
	}

	target := "integrations/" + aws.ToString(integ.IntegrationId)
	route, err := client.CreateRoute(ctx, &apigatewayv2.CreateRouteInput{ApiId: id, RouteKey: aws.String("GET /pets")})
	ok(t, err, "CreateRoute")
	moved, err := client.UpdateRoute(ctx, &apigatewayv2.UpdateRouteInput{ApiId: id, RouteId: route.RouteId, RouteKey: aws.String("POST /cats/{name}"), Target: aws.String(target)})
	ok(t, err, "UpdateRoute")
	if aws.ToString(moved.RouteKey) != "POST /cats/{name}" || aws.ToString(moved.Target) != target || aws.ToString(moved.RouteId) != aws.ToString(route.RouteId) {
		t.Errorf("UpdateRoute = id %q, key %q, target %q; want %s, POST /cats/{name}, %s",
			aws.ToString(moved.RouteId), aws.ToString(moved.RouteKey), aws.ToString(moved.Target), aws.ToString(route.RouteId), target)
	}
	routes, err := client.GetRoutes(ctx, &apigatewayv2.GetRoutesInput{ApiId: id})
	ok(t, err, "GetRoutes")
	if len(routes.Items) != 1 || aws.ToString(routes.Items[0].RouteKey) != "POST /cats/{name}" {
		t.Errorf("GetRoutes = %+v, want the one route POST /cats/{name}", routes.Items)
	}

	_, err = client.DeleteRoute(ctx, &apigatewayv2.DeleteRouteInput{ApiId: id, RouteId: route.RouteId})
	ok(t, err, "DeleteRoute")
	_, err = client.GetRoute(ctx, &apigatewayv2.GetRouteInput{ApiId: id, RouteId: route.RouteId})
	errAs[*agtypes.NotFoundException](t, err, "GetRoute after DeleteRoute")
	wantStatus(t, err, "GetRoute after DeleteRoute", http.StatusNotFound)
	_, err = client.DeleteIntegration(ctx, &apigatewayv2.DeleteIntegrationInput{ApiId: id, IntegrationId: integ.IntegrationId})
	ok(t, err, "DeleteIntegration")
	_, err = client.GetIntegration(ctx, &apigatewayv2.GetIntegrationInput{ApiId: id, IntegrationId: integ.IntegrationId})
	errAs[*agtypes.NotFoundException](t, err, "GetIntegration after DeleteIntegration")
	wantStatus(t, err, "GetIntegration after DeleteIntegration", http.StatusNotFound)
}

// Stages and deployments are listed, updated and deleted (2.2). Stage
// variables merge on UpdateStage and a variable sent as "" is removed
// (3.4); route settings are keyed by route key (3.4) and
// DeleteRouteSettings removes one key; DeleteAccessLogSettings removes the
// stage's access log settings (2.2).
func TestAPIGatewayV2StageAndDeploymentLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigatewayv2.NewFromConfig(awsCfg)
	id := createHTTPAPI(t, client, name("conf-stages")).ApiId
	_, err := client.CreateRoute(ctx, &apigatewayv2.CreateRouteInput{ApiId: id, RouteKey: aws.String("GET /pets")})
	ok(t, err, "CreateRoute")

	_, err = client.CreateStage(ctx, &apigatewayv2.CreateStageInput{ApiId: id, StageName: aws.String("dev"), StageVariables: map[string]string{"a": "1", "b": "2"}})
	ok(t, err, "CreateStage dev")
	_, err = client.CreateStage(ctx, &apigatewayv2.CreateStageInput{ApiId: id, StageName: aws.String("qa")})
	ok(t, err, "CreateStage qa")
	stages, err := client.GetStages(ctx, &apigatewayv2.GetStagesInput{ApiId: id})
	ok(t, err, "GetStages")
	var names []string
	for _, s := range stages.Items {
		names = append(names, aws.ToString(s.StageName))
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"dev", "qa"}) {
		t.Errorf("GetStages names = %v, want [dev qa]", names)
	}

	logGroup := "arn:aws:logs:" + region() + ":" + account(t) + ":log-group:" + name("conf-access")
	upd, err := client.UpdateStage(ctx, &apigatewayv2.UpdateStageInput{
		ApiId: id, StageName: aws.String("dev"), StageVariables: map[string]string{"b": "", "c": "3"},
		AccessLogSettings: &agtypes.AccessLogSettings{DestinationArn: aws.String(logGroup), Format: aws.String("$context.requestId")},
		RouteSettings:     map[string]agtypes.RouteSettings{"GET /pets": {ThrottlingBurstLimit: aws.Int32(100), ThrottlingRateLimit: aws.Float64(2000)}},
	})
	ok(t, err, "UpdateStage dev")
	if !maps.Equal(upd.StageVariables, map[string]string{"a": "1", "c": "3"}) {
		t.Errorf("UpdateStage StageVariables = %v, want a=1 c=3 (merged, b removed)", upd.StageVariables)
	}
	if upd.AccessLogSettings == nil || aws.ToString(upd.AccessLogSettings.DestinationArn) != logGroup {
		t.Errorf("UpdateStage AccessLogSettings = %+v, want destination %s", upd.AccessLogSettings, logGroup)
	}
	if _, found := upd.RouteSettings["GET /pets"]; !found {
		t.Errorf("UpdateStage RouteSettings = %v, want a GET /pets entry", upd.RouteSettings)
	}

	_, err = client.DeleteRouteSettings(ctx, &apigatewayv2.DeleteRouteSettingsInput{ApiId: id, StageName: aws.String("dev"), RouteKey: aws.String("GET /pets")})
	ok(t, err, "DeleteRouteSettings")
	_, err = client.DeleteAccessLogSettings(ctx, &apigatewayv2.DeleteAccessLogSettingsInput{ApiId: id, StageName: aws.String("dev")})
	ok(t, err, "DeleteAccessLogSettings")
	dev, err := client.GetStage(ctx, &apigatewayv2.GetStageInput{ApiId: id, StageName: aws.String("dev")})
	ok(t, err, "GetStage dev")
	if _, found := dev.RouteSettings["GET /pets"]; found {
		t.Errorf("GetStage RouteSettings after DeleteRouteSettings = %v, want no GET /pets entry", dev.RouteSettings)
	}
	if dev.AccessLogSettings != nil && (aws.ToString(dev.AccessLogSettings.DestinationArn) != "" || aws.ToString(dev.AccessLogSettings.Format) != "") {
		t.Errorf("GetStage AccessLogSettings after DeleteAccessLogSettings = %+v, want none", dev.AccessLogSettings)
	}

	// A deployment no stage uses, so deleting it depends on no rule
	// UNVERIFIED for HTTP APIs (3.5).
	dep, err := client.CreateDeployment(ctx, &apigatewayv2.CreateDeploymentInput{ApiId: id, Description: aws.String("v1")})
	ok(t, err, "CreateDeployment")
	read, err := client.GetDeployment(ctx, &apigatewayv2.GetDeploymentInput{ApiId: id, DeploymentId: dep.DeploymentId})
	ok(t, err, "GetDeployment")
	if aws.ToString(read.Description) != "v1" || read.DeploymentStatus != agtypes.DeploymentStatusDeployed {
		t.Errorf("GetDeployment = description %q, status %q; want v1 and DEPLOYED", aws.ToString(read.Description), read.DeploymentStatus)
	}
	redesc, err := client.UpdateDeployment(ctx, &apigatewayv2.UpdateDeploymentInput{ApiId: id, DeploymentId: dep.DeploymentId, Description: aws.String("v1 renamed")})
	ok(t, err, "UpdateDeployment")
	if aws.ToString(redesc.Description) != "v1 renamed" || aws.ToString(redesc.DeploymentId) != aws.ToString(dep.DeploymentId) {
		t.Errorf("UpdateDeployment = id %q, description %q; want %s and v1 renamed", aws.ToString(redesc.DeploymentId), aws.ToString(redesc.Description), aws.ToString(dep.DeploymentId))
	}
	_, err = client.DeleteDeployment(ctx, &apigatewayv2.DeleteDeploymentInput{ApiId: id, DeploymentId: dep.DeploymentId})
	ok(t, err, "DeleteDeployment")
	_, err = client.GetDeployment(ctx, &apigatewayv2.GetDeploymentInput{ApiId: id, DeploymentId: dep.DeploymentId})
	errAs[*agtypes.NotFoundException](t, err, "GetDeployment after DeleteDeployment")
	wantStatus(t, err, "GetDeployment after DeleteDeployment", http.StatusNotFound)

	_, err = client.DeleteStage(ctx, &apigatewayv2.DeleteStageInput{ApiId: id, StageName: aws.String("qa")})
	ok(t, err, "DeleteStage qa")
	_, err = client.GetStage(ctx, &apigatewayv2.GetStageInput{ApiId: id, StageName: aws.String("qa")})
	errAs[*agtypes.NotFoundException](t, err, "GetStage after DeleteStage")
	wantStatus(t, err, "GetStage after DeleteStage", http.StatusNotFound)
}

// DeleteCorsConfiguration removes an API's CORS configuration (3.7), which
// GetApi then no longer returns.
func TestAPIGatewayV2DeleteCorsConfiguration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := apigatewayv2.NewFromConfig(awsCfg)
	created, err := client.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name: aws.String(name("conf-cors")), ProtocolType: agtypes.ProtocolTypeHttp,
		CorsConfiguration: &agtypes.Cors{AllowOrigins: []string{"https://example.com"}, MaxAge: aws.Int32(300)},
	})
	ok(t, err, "CreateApi with CORS")
	t.Cleanup(func() {
		_, _ = client.DeleteApi(context.Background(), &apigatewayv2.DeleteApiInput{ApiId: created.ApiId})
	})
	if created.CorsConfiguration == nil || aws.ToInt32(created.CorsConfiguration.MaxAge) != 300 {
		t.Errorf("CreateApi CorsConfiguration = %+v, want MaxAge 300", created.CorsConfiguration)
	}
	_, err = client.DeleteCorsConfiguration(ctx, &apigatewayv2.DeleteCorsConfigurationInput{ApiId: created.ApiId})
	ok(t, err, "DeleteCorsConfiguration")
	got, err := client.GetApi(ctx, &apigatewayv2.GetApiInput{ApiId: created.ApiId})
	ok(t, err, "GetApi")
	if got.CorsConfiguration != nil {
		t.Errorf("GetApi CorsConfiguration after DeleteCorsConfiguration = %+v, want none", got.CorsConfiguration)
	}
}
