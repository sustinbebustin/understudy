package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/server"
)

// apigatewayResource is one operation's entry in the apigateway-http.md
// 8.1 table: the method its action names and the resource path after
// arn:aws:apigateway:<region>::, with its <placeholders>.
type apigatewayResource struct {
	method, path string
}

// apigatewayTable reads the apigateway-http.md 8.1 table, the record of
// which action and resource each HTTP API control-plane call needs, so the
// test asserts against the research rather than a copy of it. A row such
// as "UpdateApi / GetApi, DeleteApi | PATCH / GET, DELETE" pairs its
// operations with its methods in order.
func apigatewayTable(t *testing.T) map[string]apigatewayResource {
	t.Helper()
	data, err := os.ReadFile("../../docs/research/apigateway-http.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start, end := strings.Index(text, "### 8.1 "), strings.Index(text, "### 8.2 ")
	if start < 0 || end < start {
		t.Fatal("apigateway-http.md has no 8.1 section before 8.2")
	}
	split := regexp.MustCompile(`\s*[/,]\s*`)
	out := map[string]apigatewayResource{}
	for line := range strings.SplitSeq(text[start:end], "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 6 || !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| Operations ") {
			continue
		}
		ops, methods := split.Split(strings.TrimSpace(cells[1]), -1), split.Split(strings.TrimSpace(cells[2]), -1)
		if methods[0] == "none" {
			continue // InvokeApi declares no action (8.2).
		}
		if len(ops) != len(methods) {
			t.Fatalf("8.1 row %q pairs %d operations with %d methods", line, len(ops), len(methods))
		}
		path := strings.Trim(strings.TrimSpace(cells[4]), "`")
		for i, op := range ops {
			out[op] = apigatewayResource{method: methods[i], path: path}
		}
	}
	if len(out) == 0 {
		t.Fatal("the 8.1 table has no operation rows")
	}
	return out
}

// apigatewayCall sends one restJson1 API Gateway v2 request signed as the
// default account's root and returns the status and decoded body.
func apigatewayCall(t *testing.T, method, target, body string) (status int, out map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	auth.Sign(req, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: aws.DefaultRegion(), Service: "apigateway"}, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("%s %s answered %d %s: %v", method, target, resp.StatusCode, data, err)
		}
	}
	return resp.StatusCode, out
}

// GET /iam/policy grants each HTTP API control-plane call apigateway:<its
// method> on the resource the apigateway-http.md 8.1 table gives it (the
// collection for creates and lists, the item otherwise, the tagged
// resource for the tag operations) and nothing else of apigateway.
// The calls are the ones conformance/aws-cli/tests/apigatewayv2.sh makes,
// the failing ones included, because the access record keeps a handled
// failure: the application needs its permission too (ARCHITECTURE.md, the
// /iam/policy row of the admin API table).
func TestIAMPolicyPairsAPIGatewayCallsWithThe81Resources(t *testing.T) {
	table := apigatewayTable(t)
	c, endpoint := adminServer(t, server.Config{})
	const (
		root   = "arn:aws:iam::000000000000:root"
		prefix = "arn:aws:apigateway:us-east-1::"
	)
	// want holds what the 8.1 table says each call needs.
	want := map[[2]string]bool{}
	send := func(op, method, path, body string, status int, labels map[string]string) map[string]any {
		t.Helper()
		row, ok := table[op]
		if !ok {
			t.Fatalf("the 8.1 table has no row for %s", op)
		}
		res := row.path
		for name, value := range labels {
			res = strings.ReplaceAll(res, "<"+name+">", value)
		}
		if strings.Contains(res, "<") {
			t.Fatalf("%s: the 8.1 resource %s has a placeholder the call does not fill (%s)", op, row.path, res)
		}
		want[[2]string{"apigateway:" + row.method, rendered(t, prefix+res)}] = true
		got, out := apigatewayCall(t, method, endpoint+path, body)
		if got != status {
			t.Fatalf("%s %s %s = %d %v, want %d", op, method, path, got, out, status)
		}
		return out
	}
	idOf := func(out map[string]any, key string) string {
		t.Helper()
		s, ok := out[key].(string)
		if !ok || s == "" {
			t.Fatalf("answer has no %s: %v", key, out)
		}
		return s
	}
	createAPI := func(body string) string {
		t.Helper()
		return idOf(send("CreateApi", http.MethodPost, "/v2/apis", body, http.StatusCreated, nil), "apiId")
	}

	// test_create_api_defaults
	id := createAPI(`{"name":"a","protocolType":"HTTP"}`)
	api := map[string]string{"api-id": id}
	send("GetApi", http.MethodGet, "/v2/apis/"+id, "", http.StatusOK, api)
	// AWS answers UpdateApi 201 (APIGW2-03, recorded 2026-11-03).
	send("UpdateApi", http.MethodPatch, "/v2/apis/"+id, `{"description":"updated"}`, http.StatusCreated, api)
	send("GetApis", http.MethodGet, "/v2/apis", "", http.StatusOK, nil)
	send("DeleteApi", http.MethodDelete, "/v2/apis/"+id, "", http.StatusNoContent, api)
	send("GetApi", http.MethodGet, "/v2/apis/"+id, "", http.StatusNotFound, api)

	// test_tags_take_the_escaped_arn: the CLI percent-encodes the ARN
	// label (apigateway-http.md 2.2); the calls need the tagged API itself
	// (8.1, APIGW2-15).
	id = createAPI(`{"name":"b","protocolType":"HTTP","tags":{"team":"a"}}`)
	tagged := map[string]string{"api-id": id}
	tags := "/v2/tags/" + url.QueryEscape(prefix+"/apis/"+id)
	send("TagResource", http.MethodPost, tags, `{"tags":{"env":"dev"}}`, http.StatusCreated, tagged)
	send("GetTags", http.MethodGet, tags, "", http.StatusOK, tagged)
	send("UntagResource", http.MethodDelete, tags+"?tagKeys=team", "", http.StatusNoContent, tagged)
	send("GetApi", http.MethodGet, "/v2/apis/"+id, "", http.StatusOK, map[string]string{"api-id": id})

	// test_routes_integrations_stages_and_deployments
	id = createAPI(`{"name":"c","protocolType":"HTTP"}`)
	api = map[string]string{"api-id": id}
	integration := idOf(send("CreateIntegration", http.MethodPost, "/v2/apis/"+id+"/integrations",
		`{"integrationType":"AWS_PROXY","integrationUri":"arn:aws:lambda:us-east-1:000000000000:function:t","payloadFormatVersion":"2.0"}`,
		http.StatusCreated, api), "integrationId")
	send("CreateRoute", http.MethodPost, "/v2/apis/"+id+"/routes", `{"routeKey":"GET /pets/{id}","target":"integrations/`+integration+`"}`, http.StatusCreated, api)
	send("CreateRoute", http.MethodPost, "/v2/apis/"+id+"/routes", `{"routeKey":"GET /pets/{id}"}`, http.StatusConflict, api)
	send("GetRoute", http.MethodGet, "/v2/apis/"+id+"/routes/zzzzzzz", "", http.StatusNotFound, map[string]string{"api-id": id, "route-id": "zzzzzzz"})
	// The script's UpdateIntegration sends timeoutInMillis 30001, which
	// validation refuses at step 7, before the call names a resource
	// (kernel.WithResources resolves only a valid input), so the record
	// grants it on "*" whatever the 8.1 table says. A valid update stands
	// in for it.
	send("UpdateIntegration", http.MethodPatch, "/v2/apis/"+id+"/integrations/"+integration, `{"timeoutInMillis":29000}`, http.StatusOK,
		map[string]string{"api-id": id, "integration-id": integration})
	send("CreateStage", http.MethodPost, "/v2/apis/"+id+"/stages", `{"stageName":"$default","autoDeploy":true}`, http.StatusCreated, api)
	send("GetDeployments", http.MethodGet, "/v2/apis/"+id+"/deployments", "", http.StatusOK, api)
	send("CreateStage", http.MethodPost, "/v2/apis/"+id+"/stages", `{"stageName":"prod"}`, http.StatusCreated, api)
	send("CreateDeployment", http.MethodPost, "/v2/apis/"+id+"/deployments", `{"stageName":"prod","description":"v1"}`, http.StatusCreated, api)
	send("GetStage", http.MethodGet, "/v2/apis/"+id+"/stages/prod", "", http.StatusOK, map[string]string{"api-id": id, "stage-name": "prod"})

	policy, err := c.IAMPolicy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := apigatewayGrants(t, policy.Document)
	for pair := range want {
		if !got[pair] {
			t.Errorf("the policy does not grant %s on %s\npolicy: %s", pair[0], pair[1], policy.Document)
		}
	}
	for pair := range got {
		if !want[pair] {
			t.Errorf("the policy grants %s on %s, which no call needs by the 8.1 table\npolicy: %s", pair[0], pair[1], policy.Document)
		}
	}
}

// rendered is the policy Resource of the resource arn: the minted API,
// route, integration and deployment ids as their wildcards
// (access.MintedPattern, ARCHITECTURE.md 17), else the exact ARN.
func rendered(t *testing.T, arn string) string {
	t.Helper()
	a, err := aws.ParseARN(arn)
	if err != nil {
		t.Fatalf("resource %s: %v", arn, err)
	}
	if pattern, minted := access.MintedPattern(access.Direct{}, a); minted {
		return pattern
	}
	return policydoc.Literal(arn)
}

// apigatewayGrants lists the (apigateway action, resource) pairs a policy
// document allows.
func apigatewayGrants(t *testing.T, doc json.RawMessage) map[[2]string]bool {
	t.Helper()
	var parsed struct {
		Statement []struct {
			Action   json.RawMessage
			Resource json.RawMessage
		}
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("policy document %s: %v", doc, err)
	}
	strs := func(raw json.RawMessage) []string {
		var one string
		if json.Unmarshal(raw, &one) == nil {
			return []string{one}
		}
		var many []string
		if err := json.Unmarshal(raw, &many); err != nil {
			t.Fatalf("policy element %s is neither a string nor a list: %v", raw, err)
		}
		return many
	}
	out := map[[2]string]bool{}
	for _, st := range parsed.Statement {
		for _, action := range strs(st.Action) {
			if !strings.HasPrefix(action, "apigateway:") {
				continue
			}
			for _, res := range strs(st.Resource) {
				out[[2]string{action, res}] = true
			}
		}
	}
	return out
}
