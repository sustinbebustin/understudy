//go:build docker

package server_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/server"
)

// proxyRuntime is a provided.al2023 runtime that answers each event with a
// REST proxy response whose body is the event, base64-encoded so the
// shell need not escape it; a REST API does not decode it
// (apigateway-rest.md 5.2).
const proxyRuntime = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
while true; do
  HEADERS=$(mktemp)
  EVENT=$(curl -sS -D "$HEADERS" "$API/invocation/next")
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  BODY=$(printf '%s' "$EVENT" | base64 | tr -d '\n')
  curl -sS -X POST "$API/invocation/$ID/response" -d "{\"statusCode\":200,\"body\":\"$BODY\"}" >/dev/null
done
`

// The REST tracer bullet against a real function (spec 6b): a REST API
// whose root resource has GET by a Lambda proxy integration, deployed to
// stage s. Without a permission for apigateway.amazonaws.com the function
// never runs and the client gets the 500 of switch
// apigateway.lambda.permission (apigateway-rest.md 5.4); with one, GET /s/
// through the path fallback reaches the function with the REST 1.0 event
// and answers its statusCode and body.
func TestAPIGatewayRESTTracerBulletReachesAFunction(t *testing.T) {
	r := launch(t, server.Config{Docker: dockerConfig(t)})
	r.stopOnCleanup(t)
	client := &http.Client{Timeout: 3 * time.Minute}
	call := func(method, path, service, body string, want int, v any) []byte {
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
		return out
	}

	var pkg bytes.Buffer
	zw := zip.NewWriter(&pkg)
	fh := &zip.FileHeader{Name: "bootstrap", Method: zip.Deflate}
	fh.SetMode(0o755)
	f, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(proxyRuntime)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var fn struct{ FunctionArn string }
	call(http.MethodPost, "/2015-03-31/functions", "lambda", `{"FunctionName":"proxy","Runtime":"provided.al2023","Handler":"bootstrap",`+
		`"Role":"arn:aws:iam::000000000000:role/r","Code":{"ZipFile":"`+base64.StdEncoding.EncodeToString(pkg.Bytes())+`"}}`, http.StatusCreated, &fn)

	var api struct {
		ID             string `json:"id"`
		RootResourceID string `json:"rootResourceId"`
	}
	call(http.MethodPost, "/restapis", "apigateway", `{"name":"t"}`, http.StatusCreated, &api)
	method := "/restapis/" + api.ID + "/resources/" + api.RootResourceID + "/methods/GET"
	call(http.MethodPut, method, "apigateway", `{"authorizationType":"NONE"}`, http.StatusCreated, nil)
	call(http.MethodPut, method+"/integration", "apigateway",
		`{"type":"AWS_PROXY","httpMethod":"POST","uri":"arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/`+fn.FunctionArn+`/invocations"}`, http.StatusCreated, nil)
	var deployment struct{ ID string }
	call(http.MethodPost, "/restapis/"+api.ID+"/deployments", "apigateway", `{"stageName":"s"}`, http.StatusCreated, &deployment)

	if out := call(http.MethodGet, "/_understudy/execute-api/"+api.ID+"/s/", "", "", http.StatusInternalServerError, nil); string(out) != `{"message": "Internal server error"}` {
		t.Errorf("without a permission = %s, want the permission switch's denial", out)
	}
	call(http.MethodPost, "/2015-03-31/functions/proxy/policy", "lambda", `{"StatementId":"apigateway","Action":"lambda:InvokeFunction",`+
		`"Principal":"apigateway.amazonaws.com","SourceArn":"arn:aws:execute-api:us-east-1:000000000000:`+api.ID+`/*/*"}`, http.StatusCreated, nil)

	out := call(http.MethodGet, "/_understudy/execute-api/"+api.ID+"/s/", "", "", http.StatusOK, nil)
	raw, err := base64.StdEncoding.DecodeString(string(out))
	if err != nil {
		t.Fatalf("GET /s/ answered %s, want the function's base64 event: %v", out, err)
	}
	var ev struct {
		Resource, Path, HTTPMethod string
		RequestContext             struct{ Stage, APIID, ResourceID, DeploymentID string }
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("the function saw %s: %v", raw, err)
	}
	if ev.Resource != "/" || ev.Path != "/" || ev.HTTPMethod != http.MethodGet || ev.RequestContext.Stage != "s" ||
		ev.RequestContext.APIID != api.ID || ev.RequestContext.ResourceID != api.RootResourceID || ev.RequestContext.DeploymentID != deployment.ID {
		t.Errorf("the function saw %+v, want the REST 1.0 event of GET / on stage s of %s", ev, api.ID)
	}
}
