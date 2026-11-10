//go:build docker

package server_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// restProxyRuntime is a provided.al2023 runtime for REST proxy events: a
// request to /throw makes the function fail, /malformed answers a bare
// string, /mapped answers a response with headers and multiValueHeaders,
// /slow answers after two seconds, and any other request answers with its
// event, base64-encoded, as the body.
const restProxyRuntime = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
while true; do
  HEADERS=$(mktemp)
  EVENT=$(curl -sS -D "$HEADERS" "$API/invocation/next")
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  case "$EVENT" in
  *'"path":"/throw"'*)
    curl -sS -X POST "$API/invocation/$ID/error" -d '{"errorMessage":"boom","errorType":"Error"}' >/dev/null
    continue
    ;;
  *'"path":"/malformed"'*)
    RESPONSE='"justAString"'
    ;;
  *'"path":"/mapped"'*)
    RESPONSE='{"statusCode":"202","headers":{"x-solo":"one"},"multiValueHeaders":{"x-many":["a","b"]},"body":"mapped"}'
    ;;
  *'"path":"/slow"'*)
    sleep 2
    RESPONSE='{"statusCode":200,"body":"late"}'
    ;;
  *)
    BODY=$(printf '%s' "$EVENT" | base64 | tr -d '\n')
    RESPONSE="{\"statusCode\":200,\"body\":\"$BODY\"}"
    ;;
  esac
  curl -sS -X POST "$API/invocation/$ID/response" -d "$RESPONSE" >/dev/null
done
`

// AWS_PROXY in full against a real function: an ANY
// method on /{proxy+} gets the REST 1.0 event of a POST with a query and a
// body (apigateway-rest.md 5.1, 4.5); a response with headers and
// multiValueHeaders maps per 5.2; a function error and a malformed
// response answer the proxy failure 502 (5.3); an invocation past the
// integration's timeoutInMillis answers switch apigateway.proxy.unrecorded's
// Timeout (APIGW1-10).
func TestAPIGatewayRESTProxyReachesAFunction(t *testing.T) {
	r := launch(t, server.Config{Docker: dockerConfig(t)})
	r.stopOnCleanup(t)
	client := &http.Client{Timeout: 3 * time.Minute}
	call := func(method, path, service, body string, want int, v any) {
		t.Helper()
		status, out := signedJSON(t, client, method, r.endpoint+path, service, body)
		if status != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, status, out, want)
		}
		if v != nil {
			if err := json.Unmarshal(out, v); err != nil {
				t.Fatalf("%s %s answered %s: %v", method, path, out, err)
			}
		}
	}
	send := func(method, path, body string) (int, http.Header, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, r.endpoint+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header, string(out)
	}

	var pkg bytes.Buffer
	zw := zip.NewWriter(&pkg)
	fh := &zip.FileHeader{Name: "bootstrap", Method: zip.Deflate}
	fh.SetMode(0o755)
	f, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(restProxyRuntime)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var fn struct{ FunctionArn string }
	call(http.MethodPost, "/2015-03-31/functions", "lambda", `{"FunctionName":"restproxy","Runtime":"provided.al2023","Handler":"bootstrap",`+
		`"Role":"arn:aws:iam::000000000000:role/r","Code":{"ZipFile":"`+base64.StdEncoding.EncodeToString(pkg.Bytes())+`"}}`, http.StatusCreated, &fn)

	var api struct {
		ID             string `json:"id"`
		RootResourceID string `json:"rootResourceId"`
	}
	call(http.MethodPost, "/restapis", "apigateway", `{"name":"proxy"}`, http.StatusCreated, &api)
	uri := "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/" + fn.FunctionArn + "/invocations"
	proxy := func(part, verb, extra string) string {
		t.Helper()
		var res struct{ ID string }
		call(http.MethodPost, "/restapis/"+api.ID+"/resources/"+api.RootResourceID, "apigateway", `{"pathPart":"`+part+`"}`, http.StatusCreated, &res)
		method := "/restapis/" + api.ID + "/resources/" + res.ID + "/methods/" + verb
		call(http.MethodPut, method, "apigateway", `{"authorizationType":"NONE"}`, http.StatusCreated, nil)
		call(http.MethodPut, method+"/integration", "apigateway", `{"type":"AWS_PROXY","httpMethod":"POST","uri":"`+uri+`"`+extra+`}`, http.StatusCreated, nil)
		return res.ID
	}
	greedy := proxy("{proxy+}", "ANY", "")
	proxy("slow", http.MethodGet, `,"timeoutInMillis":1000`)
	var deployment struct{ ID string }
	call(http.MethodPost, "/restapis/"+api.ID+"/deployments", "apigateway", `{"stageName":"s"}`, http.StatusCreated, &deployment)
	call(http.MethodPost, "/2015-03-31/functions/restproxy/policy", "lambda", `{"StatementId":"apigateway","Action":"lambda:InvokeFunction",`+
		`"Principal":"apigateway.amazonaws.com","SourceArn":"arn:aws:execute-api:us-east-1:000000000000:`+api.ID+`/*/*"}`, http.StatusCreated, nil)
	base := "/_understudy/execute-api/" + api.ID + "/s"

	status, _, out := send(http.MethodPost, base+"/items/7?x=1&x=2", "hello")
	raw, err := base64.StdEncoding.DecodeString(out)
	if status != http.StatusOK || err != nil {
		t.Fatalf("POST /s/items/7 = %d %s, want 200 with the function's base64 event (%v)", status, out, err)
	}
	var ev struct {
		Resource, Path, HTTPMethod, Body string
		IsBase64Encoded                  bool
		PathParameters                   map[string]string
		QueryStringParameters            map[string]string
		MultiValueQueryStringParameters  map[string][]string
		RequestContext                   struct{ Stage, ResourcePath, HTTPMethod, APIID, ResourceID, DeploymentID string }
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("the function saw %s: %v", raw, err)
	}
	if ev.Resource != "/{proxy+}" || ev.Path != "/items/7" || ev.HTTPMethod != http.MethodPost || ev.Body != "hello" || ev.IsBase64Encoded ||
		ev.PathParameters["proxy"] != "items/7" || ev.QueryStringParameters["x"] != "2" || !slices.Equal(ev.MultiValueQueryStringParameters["x"], []string{"1", "2"}) {
		t.Errorf("the function saw %+v, want the REST 1.0 event of POST /items/7?x=1&x=2 on /{proxy+} with body hello", ev)
	}
	if rc := ev.RequestContext; rc.Stage != "s" || rc.ResourcePath != "/{proxy+}" || rc.HTTPMethod != http.MethodPost ||
		rc.APIID != api.ID || rc.ResourceID != greedy || rc.DeploymentID != deployment.ID {
		t.Errorf("the event's requestContext = %+v, want stage s, resource %s /{proxy+}, deployment %s", rc, greedy, deployment.ID)
	}

	status, header, out := send(http.MethodGet, base+"/mapped", "")
	if status != http.StatusAccepted || out != "mapped" || header.Get("X-Solo") != "one" || strings.Join(header.Values("X-Many"), ", ") != "a, b" {
		t.Errorf("GET /s/mapped = %d %v %q, want 202 x-solo: one, x-many: a, b, mapped", status, header, out)
	}

	gateway := func(path string, want apigateway.GatewayAnswer) {
		t.Helper()
		status, _, out := send(http.MethodGet, base+path, "")
		if status != want.Status || out != want.Body {
			t.Errorf("GET /s%s = %d %s, want %d %s", path, status, out, want.Status, want.Body)
		}
	}
	gateway("/throw", apigateway.ProxyFailure())
	gateway("/malformed", apigateway.ProxyFailure())
	gateway("/slow", apigateway.ProxyUnrecordedSwitch().Value.Timeout)
}
