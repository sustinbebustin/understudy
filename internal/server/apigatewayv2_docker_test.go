//go:build docker

package server_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/server"
)

// echoRuntime is a provided.al2023 runtime that answers each event with
// {"echo": <event>}, output with no statusCode, which API Gateway sends as
// a 200 application/json body (apigateway-http.md 5.3).
const echoRuntime = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
while true; do
  HEADERS=$(mktemp)
  EVENT=$(curl -sS -D "$HEADERS" "$API/invocation/next")
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  curl -sS -X POST "$API/invocation/$ID/response" -d "{\"echo\":$EVENT}" >/dev/null
done
`

// dockerConfig is the Docker Engine serve would use, resolved as serve
// resolves it.
func dockerConfig(t *testing.T) server.Docker {
	t.Helper()
	host, err := config.LoadHost(nil, os.Environ(), os.ReadFile)
	if err != nil {
		t.Fatalf("resolve the Docker socket as serve does: %v", err)
	}
	switch d := host.Docker.(type) {
	case config.DockerSocket:
		return server.DockerSocket{Path: d.Path}
	case config.DockerTCP:
		return server.DockerTCP{Addr: d.Addr}
	case config.DockerTLS:
		return server.DockerTLS{Addr: d.Addr, CertDir: d.CertDir}
	case config.DockerDisabled:
	}
	t.Fatalf("docker-tagged tests need a Docker Engine, but the configuration turns Docker off (warnings: %v)", host.Warnings)
	return nil
}

// signedJSON sends a JSON request signed for service as the default
// account's root and returns the status and body.
func signedJSON(t *testing.T, client *http.Client, method, url, service, body string) (status int, out []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	if service != "" {
		req.Header.Set("Content-Type", "application/json")
		auth.Sign(req, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: aws.DefaultRegion(), Service: service}, time.Now())
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

// The tracer bullet against a real function (the unit's spec): an HTTP API
// with a Lambda proxy integration of payload format 2.0, a GET /hello route
// and a $default stage that deploys automatically. Without a permission
// for apigateway.amazonaws.com the function never runs and the client gets
// the documented {"message":"Internal Server Error"} (apigateway-http.md
// 5.5, 8.4); with one, a GET through the path fallback reaches the
// function with the 2.0 event and answers with its output.
func TestAPIGatewayV2TracerBulletReachesAFunction(t *testing.T) {
	r := launch(t, server.Config{Docker: dockerConfig(t)})
	r.stopOnCleanup(t)
	client := &http.Client{Timeout: 3 * time.Minute}
	call := func(method, path, service, body string, want int, v any) {
		t.Helper()
		status, out := signedJSON(t, client, method, r.endpoint+path, service, body)
		if status != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, status, out, want)
		}
		if err := json.Unmarshal(out, v); err != nil {
			t.Fatalf("%s %s answered %s: %v", method, path, out, err)
		}
	}

	var pkg bytes.Buffer
	zw := zip.NewWriter(&pkg)
	fh := &zip.FileHeader{Name: "bootstrap", Method: zip.Deflate}
	fh.SetMode(0o755)
	f, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(echoRuntime)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var fn struct{ FunctionArn string }
	call(http.MethodPost, "/2015-03-31/functions", "lambda", `{"FunctionName":"echo","Runtime":"provided.al2023","Handler":"bootstrap",`+
		`"Role":"arn:aws:iam::000000000000:role/r","Code":{"ZipFile":"`+base64.StdEncoding.EncodeToString(pkg.Bytes())+`"}}`, http.StatusCreated, &fn)

	var api struct {
		APIID string `json:"apiId"`
	}
	call(http.MethodPost, "/v2/apis", "apigateway", `{"name":"t","protocolType":"HTTP"}`, http.StatusCreated, &api)
	var integration struct {
		IntegrationID string `json:"integrationId"`
	}
	call(http.MethodPost, "/v2/apis/"+api.APIID+"/integrations", "apigateway",
		`{"integrationType":"AWS_PROXY","integrationUri":"`+fn.FunctionArn+`","payloadFormatVersion":"2.0"}`, http.StatusCreated, &integration)
	var ignored struct{}
	call(http.MethodPost, "/v2/apis/"+api.APIID+"/routes", "apigateway", `{"routeKey":"GET /hello","target":"integrations/`+integration.IntegrationID+`"}`, http.StatusCreated, &ignored)
	call(http.MethodPost, "/v2/apis/"+api.APIID+"/stages", "apigateway", `{"stageName":"$default","autoDeploy":true}`, http.StatusCreated, &ignored)

	var denied struct{ Message string }
	call(http.MethodGet, "/_understudy/execute-api/"+api.APIID+"/hello", "", "", http.StatusInternalServerError, &denied)
	if denied.Message != "Internal Server Error" {
		t.Errorf("without a permission = %+v, want the message Internal Server Error", denied)
	}
	call(http.MethodPost, "/2015-03-31/functions/echo/policy", "lambda", `{"StatementId":"apigateway","Action":"lambda:InvokeFunction",`+
		`"Principal":"apigateway.amazonaws.com","SourceArn":"arn:aws:execute-api:us-east-1:000000000000:`+api.APIID+`/*/*"}`, http.StatusCreated, &ignored)

	var got struct {
		Echo struct {
			Version, RouteKey, RawPath string
			RequestContext             struct{ Stage, APIID string }
		}
	}
	call(http.MethodGet, "/_understudy/execute-api/"+api.APIID+"/hello", "", "", http.StatusOK, &got)
	if e := got.Echo; e.Version != "2.0" || e.RouteKey != "GET /hello" || e.RawPath != "/hello" || e.RequestContext.Stage != "$default" || e.RequestContext.APIID != api.APIID {
		t.Errorf("the function saw %+v, want the 2.0 event of GET /hello on the $default stage of %s", e, api.APIID)
	}
}
