package apigatewayv2_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// CreateApi with only the required members answers the defaults AWS's
// create-api example shows, ApiKeySelectionExpression
// $request.header.x-api-key and RouteSelectionExpression
// $request.method $request.path (apigateway-http.md 3.1, create-api.rst
// L13-L19), plus ipAddressType ipv4 and disableExecuteApiEndpoint false as
// Terraform's acceptance test reads them back from AWS (3.1,
// api_test.go L87-L92), and none of Description, Version or
// CorsConfiguration, which Terraform needs absent for an empty second plan
// (9.1). CreatedDate is the emulator clock's (ARCHITECTURE.md 9), and
// ApiEndpoint names the host the client addressed, never amazonaws.com
// (apigateway-http.md 11.3, ADR 0013). GetApi answers the same.
func TestCreateApiDefaults(t *testing.T) {
	run(t, func(h *harness) {
		if _, err := h.Clock.Advance(90 * time.Minute); err != nil {
			t.Fatal(err)
		}
		now := h.Clock.Now().Wire()
		out := h.createAPI("t")
		id := *out.ApiId
		if got := deref(out.ApiKeySelectionExpression); got != "$request.header.x-api-key" {
			t.Errorf("ApiKeySelectionExpression = %q, want $request.header.x-api-key", got)
		}
		if got := deref(out.RouteSelectionExpression); got != "$request.method $request.path" {
			t.Errorf("RouteSelectionExpression = %q, want $request.method $request.path", got)
		}
		if out.ProtocolType == nil || *out.ProtocolType != apigatewayv2api.ProtocolTypeHttp || deref(out.Name) != "t" {
			t.Errorf("ProtocolType %v, Name %q; want HTTP and t", out.ProtocolType, deref(out.Name))
		}
		if out.IpAddressType == nil || *out.IpAddressType != apigatewayv2api.IpAddressTypeIpv4 {
			t.Errorf("IpAddressType = %v, want ipv4", out.IpAddressType)
		}
		if out.DisableExecuteApiEndpoint == nil || *out.DisableExecuteApiEndpoint {
			t.Errorf("DisableExecuteApiEndpoint = %v, want false", out.DisableExecuteApiEndpoint)
		}
		if out.Description != nil || out.Version != nil || out.CorsConfiguration != nil {
			t.Errorf("Description %v, Version %v, CorsConfiguration %+v; want all absent", out.Description, out.Version, out.CorsConfiguration)
		}
		if out.CreatedDate == nil || out.CreatedDate.Sub(now).Abs() >= time.Second {
			t.Errorf("CreatedDate = %v, want the emulator clock's %v", out.CreatedDate, now)
		}
		if want := "http://" + id + ".execute-api." + region + ".localhost:4566"; deref(out.ApiEndpoint) != want {
			t.Errorf("ApiEndpoint = %q, want %q", deref(out.ApiEndpoint), want)
		}
		got := must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id})
		if deref(got.ApiId) != id || deref(got.ApiEndpoint) != deref(out.ApiEndpoint) ||
			deref(got.RouteSelectionExpression) != deref(out.RouteSelectionExpression) ||
			deref(got.ApiKeySelectionExpression) != deref(out.ApiKeySelectionExpression) || deref(got.Name) != "t" {
			t.Errorf("GetApi = %+v, want what CreateApi answered, %+v", got, out)
		}
	})
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// Every API gets its own id, and GetApis lists every API of the caller.
func TestCreateApiIDsAreDistinct(t *testing.T) {
	run(t, func(h *harness) {
		made := map[string]bool{}
		for i := range 20 {
			id := h.apiID("api" + strconv.Itoa(i))
			if made[id] {
				t.Fatalf("ApiId %q handed out twice", id)
			}
			made[id] = true
		}
		listed := map[string]bool{}
		for _, a := range h.listAPIs("") {
			listed[deref(a.ApiId)] = true
		}
		if !maps.Equal(listed, made) {
			t.Errorf("GetApis lists %v, want %v", slices.Sorted(maps.Keys(listed)), slices.Sorted(maps.Keys(made)))
		}
	})
}

// listAPIs pages through GetApis with maxResults, "" for none.
func (h *harness) listAPIs(maxResults string) []apigatewayv2api.Api {
	h.Helper()
	in := apigatewayv2api.GetApisInput{}
	if maxResults != "" {
		in.MaxResults = new(maxResults)
	}
	var out []apigatewayv2api.Api
	for range 200 {
		page := must(h, apigatewayv2api.OpGetApis, &in)
		if n, err := strconv.Atoi(maxResults); err == nil && len(page.Items) > n {
			h.Fatalf("GetApis answered %d items for maxResults %s", len(page.Items), maxResults)
		}
		out = append(out, page.Items...)
		if page.NextToken == nil {
			return out
		}
		in.NextToken = page.NextToken
	}
	h.Fatal("GetApis did not stop paging")
	return nil
}

// maxResults bounds a page, and following nextToken lists every API once
// (apigateway-http.md 2.2: the Get lists page through maxResults and
// nextToken). maxResults is a string member; one that is not a number is
// a bad request.
func TestGetApisPages(t *testing.T) {
	run(t, func(h *harness) {
		made := make([]string, 0, 5)
		for i := range 5 {
			made = append(made, h.apiID("p"+strconv.Itoa(i)))
		}
		listed := make([]string, 0, len(made))
		for _, a := range h.listAPIs("2") {
			listed = append(listed, deref(a.ApiId))
		}
		slices.Sort(made)
		slices.Sort(listed)
		if !slices.Equal(listed, made) {
			t.Errorf("paged GetApis = %v, want each of %v once", listed, made)
		}
		wantError(t, fail(h, apigatewayv2api.OpGetApis, &apigatewayv2api.GetApisInput{MaxResults: new("two")}), badRequest)
	})
}

// routeSelectionExpression "must be ${request.method} ${request.path}"
// for HTTP APIs (the apis-apiid API reference, apigateway-http.md 2.4),
// while AWS returns and Terraform sends $request.method $request.path
// (12 item 5): both are accepted, the unbraced form echoed byte for byte
// for Terraform's empty plan (9.1), and anything else refused.
func TestRouteSelectionExpression(t *testing.T) {
	run(t, func(h *harness) {
		for _, expr := range []string{"$request.method $request.path", "${request.method} ${request.path}"} {
			out := must(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, RouteSelectionExpression: new(expr)})
			if out.RouteSelectionExpression == nil {
				t.Errorf("RouteSelectionExpression %q: none answered", expr)
			}
			if expr[1] == 'r' && deref(out.RouteSelectionExpression) != expr {
				t.Errorf("RouteSelectionExpression = %q, want %q as sent", deref(out.RouteSelectionExpression), expr)
			}
		}
		err := fail(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, RouteSelectionExpression: new("$request.body.action")})
		wantError(t, err, badRequest)
	})
}

// WebSocket APIs and quick create are valid requests understudy does not
// emulate, so each answers NotImplemented naming its member and changes
// nothing (apigateway-http.md 3.1 describes quick create); a plain HTTP
// API is still created.
func TestWebSocketAndQuickCreateAreNotImplemented(t *testing.T) {
	run(t, func(h *harness) {
		kept := h.apiID("kept")
		base := func() *apigatewayv2api.CreateApiInput {
			return &apigatewayv2api.CreateApiInput{Name: "q", ProtocolType: apigatewayv2api.ProtocolTypeHttp}
		}
		ws := base()
		ws.ProtocolType = apigatewayv2api.ProtocolTypeWebsocket
		ws.RouteSelectionExpression = new("$request.body.action")
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateApi, ws), "protocolType")
		target := base()
		target.Target = new("arn:aws:lambda:us-east-1:000000000000:function:fn")
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateApi, target), "Target")
		key := base()
		key.RouteKey = new("GET /x")
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateApi, key), "RouteKey")
		creds := base()
		creds.CredentialsArn = new("arn:aws:iam::000000000000:role/r")
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateApi, creds), "CredentialsArn")
		wantNotImplemented(t, fail(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: kept, Target: new("arn:aws:lambda:us-east-1:000000000000:function:fn")}), "Target")
		if apis := h.listAPIs(""); len(apis) != 1 || deref(apis[0].ApiId) != kept {
			t.Errorf("GetApis = %+v, want only %s: a refused call creates nothing", apis, kept)
		}
	})
}

// A missing API, and another account's, is NotFoundException 404 (the
// model's errors for GetApi, apigateway-http.md 2.2; the REST text names
// the caller's account, 2.3, so ids are scoped to it).
func TestGetApiOfNoAPIIsNotFound(t *testing.T) {
	run(t, func(h *harness) {
		mine := h.apiID("mine")
		wantError(t, fail(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: "zzzzzzzzzz"}), notFound)
		other, err := aws.ParseAccountID("111111111111")
		if err != nil {
			t.Fatal(err)
		}
		h.Scope.Account = other
		wantError(t, fail(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: mine}), notFound)
	})
}

// UpdateApi replaces the members it carries and keeps the rest; a new
// CORS configuration replaces the old one, and DeleteCorsConfiguration
// removes it so GetApi answers none, as Terraform needs after the block is
// removed (apigateway-http.md 3.7, 9.1).
func TestUpdateApiAndCORS(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{
			Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, Description: new("first"),
			CorsConfiguration: &apigatewayv2api.Cors{AllowOrigins: []string{"https://a.example"}, MaxAge: new(int32(5))},
		})
		id := *out.ApiId
		if out.CorsConfiguration == nil || !slices.Equal(out.CorsConfiguration.AllowOrigins, []string{"https://a.example"}) {
			t.Errorf("CorsConfiguration = %+v, want it stored and returned", out.CorsConfiguration)
		}
		must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{
			ApiId: id, Name: new("renamed"), CorsConfiguration: &apigatewayv2api.Cors{AllowOrigins: []string{"https://b.example"}},
		})
		got := must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id})
		if deref(got.Name) != "renamed" || deref(got.Description) != "first" {
			t.Errorf("after UpdateApi: Name %q, Description %q; want renamed and first", deref(got.Name), deref(got.Description))
		}
		if got.CorsConfiguration == nil || !slices.Equal(got.CorsConfiguration.AllowOrigins, []string{"https://b.example"}) {
			t.Errorf("CorsConfiguration = %+v, want the update's origins", got.CorsConfiguration)
		}
		must(h, apigatewayv2api.OpDeleteCorsConfiguration, &apigatewayv2api.DeleteCorsConfigurationInput{ApiId: id})
		if got := must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}); got.CorsConfiguration != nil {
			t.Errorf("CorsConfiguration after DeleteCorsConfiguration = %+v, want none", got.CorsConfiguration)
		}
		wantError(t, fail(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: "zzzzzzzzzz", Name: new("x")}), notFound)
	})
}

// CORS MaxAge is -1..86400 by the model's @range (apigateway-http.md 2.4).
func TestCORSMaxAgeRange(t *testing.T) {
	run(t, func(h *harness) {
		for _, age := range []int32{-1, 0, 86400} {
			must(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: "ok", ProtocolType: apigatewayv2api.ProtocolTypeHttp, CorsConfiguration: &apigatewayv2api.Cors{MaxAge: new(age)}})
		}
		for _, age := range []int32{-2, 86401} {
			err := fail(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: "bad", ProtocolType: apigatewayv2api.ProtocolTypeHttp, CorsConfiguration: &apigatewayv2api.Cors{MaxAge: new(age)}})
			wantError(t, err, badRequest)
		}
	})
}

// DeleteApi removes the API with its routes; a second delete, as
// Terraform's refresh after a delete, is NotFoundException (apigateway-http.md
// 2.2, 9.1).
func TestDeleteApi(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("gone")
		h.createRoute(id, "GET /x", "")
		must(h, apigatewayv2api.OpDeleteApi, &apigatewayv2api.DeleteApiInput{ApiId: id})
		wantError(t, fail(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpGetRoutes, &apigatewayv2api.GetRoutesInput{ApiId: id}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpDeleteApi, &apigatewayv2api.DeleteApiInput{ApiId: id}), notFound)
	})
}

// Tags given at create are the API's tags, which GetApi returns (AWS's
// get-api example, apigateway-http.md 3.1, 3.6); TagResource, GetTags and
// UntagResource take the resource ARN of an API or a stage (2.5, 3.6);
// tags on a stage are GetStage's. An ARN of no API is NotFoundException,
// text that is no ARN a bad request.
func TestTags(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, Tags: map[string]string{"team": "a"}})
		id := *out.ApiId
		if !maps.Equal(out.Tags, map[string]string{"team": "a"}) {
			t.Errorf("CreateApi Tags = %v, want team=a", out.Tags)
		}
		arn := apiARN(id)
		must(h, apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: arn, Tags: map[string]string{"env": "dev"}})
		if got := must(h, apigatewayv2api.OpGetTags, &apigatewayv2api.GetTagsInput{ResourceArn: arn}).Tags; !maps.Equal(got, map[string]string{"team": "a", "env": "dev"}) {
			t.Errorf("GetTags = %v, want team=a env=dev", got)
		}
		must(h, apigatewayv2api.OpUntagResource, &apigatewayv2api.UntagResourceInput{ResourceArn: arn, TagKeys: []string{"team"}})
		if got := must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}).Tags; !maps.Equal(got, map[string]string{"env": "dev"}) {
			t.Errorf("GetApi Tags after untagging team = %v, want env=dev", got)
		}

		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", Tags: map[string]string{"tier": "1"}})
		stage := arn + "/stages/prod"
		must(h, apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: stage, Tags: map[string]string{"tier": "2", "x": "y"}})
		if got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "prod"}).Tags; !maps.Equal(got, map[string]string{"tier": "2", "x": "y"}) {
			t.Errorf("GetStage Tags = %v, want tier=2 x=y", got)
		}

		wantError(t, fail(h, apigatewayv2api.OpGetTags, &apigatewayv2api.GetTagsInput{ResourceArn: apiARN("zzzzzzzzzz")}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: "not an arn", Tags: map[string]string{"a": "b"}}), badRequest)
	})
}

// "Tags per stage: 50" (the HTTP API quotas page, apigateway-http.md 3.6):
// a stage holds 50 tags and refuses a 51st.
func TestStageTagQuota(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		tags := map[string]string{}
		for i := range 50 {
			tags["k"+strconv.Itoa(i)] = "v"
		}
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", Tags: tags})
		err := fail(h, apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: apiARN(id) + "/stages/prod", Tags: map[string]string{"one-more": "v"}})
		wantError(t, err, badRequest)
	})
}

// No URL understudy hands out names amazonaws.com: clients use what GetApi
// and the stage outputs say (apigateway-http.md 9.7, 11.1, 11.3).
func TestNoAmazonawsHostInOutputs(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod"})
		for name, out := range map[string]any{
			"GetApi":    must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}),
			"GetApis":   must(h, apigatewayv2api.OpGetApis, &apigatewayv2api.GetApisInput{}),
			"GetStage":  must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "prod"}),
			"GetStages": must(h, apigatewayv2api.OpGetStages, &apigatewayv2api.GetStagesInput{ApiId: id}),
		} {
			b, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "amazonaws.com") {
				t.Errorf("%s names amazonaws.com: %s", name, b)
			}
		}
	})
}
