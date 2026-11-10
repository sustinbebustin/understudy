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
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// formatRuntime is a provided.al2023 runtime that answers in the payload
// format of the event it gets: a 1.0 event with the documented 1.0
// response, multiValueHeaders included, and a 2.0 event with a structured
// 2.0 response carrying cookies (apigateway-http.md 5.3, 5.4). A request to
// /throw makes the function fail, and one to /slow answers after two
// seconds.
const formatRuntime = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
while true; do
  HEADERS=$(mktemp)
  EVENT=$(curl -sS -D "$HEADERS" "$API/invocation/next")
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  case "$EVENT" in
  *'"rawPath":"/throw"'* | *'"path":"/throw"'*)
    curl -sS -X POST "$API/invocation/$ID/error" -d '{"errorMessage":"boom","errorType":"Error"}' >/dev/null
    continue
    ;;
  *'"rawPath":"/slow"'*)
    sleep 2
    ;;
  esac
  case "$EVENT" in
  *'"version":"1.0"'*)
    RESPONSE='{"statusCode":202,"multiValueHeaders":{"x-format":["1.0"],"x-many":["a","b"]},"body":"one","isBase64Encoded":false}'
    ;;
  *)
    RESPONSE='{"statusCode":201,"headers":{"x-format":"2.0","content-type":"text/plain"},"cookies":["a=1","b=2"],"body":"two"}'
    ;;
  esac
  curl -sS -X POST "$API/invocation/$ID/response" -d "$RESPONSE" >/dev/null
done
`

// A Lambda proxy integration per payload format against a real function:
// the function gets the event of its
// integration's format and its response in that format is the client's
// (apigateway-http.md 5.1-5.4); a function error answers the
// apigatewayv2.lambda.failure-answers switch's FunctionError, and an
// invocation past the integration's TimeoutInMillis its Timeout
// (UNVERIFIED, 5.6, APIGW2-10).
func TestAPIGatewayV2PayloadFormatsReachAFunction(t *testing.T) {
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
	get := func(path string) (int, http.Header, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, r.endpoint+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header, string(body)
	}

	var pkg bytes.Buffer
	zw := zip.NewWriter(&pkg)
	fh := &zip.FileHeader{Name: "bootstrap", Method: zip.Deflate}
	fh.SetMode(0o755)
	f, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(formatRuntime)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var fn struct{ FunctionArn string }
	call(http.MethodPost, "/2015-03-31/functions", "lambda", `{"FunctionName":"formats","Runtime":"provided.al2023","Handler":"bootstrap",`+
		`"Role":"arn:aws:iam::000000000000:role/r","Code":{"ZipFile":"`+base64.StdEncoding.EncodeToString(pkg.Bytes())+`"}}`, http.StatusCreated, &fn)

	var api struct {
		APIID string `json:"apiId"`
	}
	call(http.MethodPost, "/v2/apis", "apigateway", `{"name":"formats","protocolType":"HTTP"}`, http.StatusCreated, &api)
	integration := func(body string) string {
		t.Helper()
		var out struct {
			IntegrationID string `json:"integrationId"`
		}
		call(http.MethodPost, "/v2/apis/"+api.APIID+"/integrations", "apigateway", body, http.StatusCreated, &out)
		return out.IntegrationID
	}
	v2 := integration(`{"integrationType":"AWS_PROXY","integrationUri":"` + fn.FunctionArn + `","payloadFormatVersion":"2.0"}`)
	v1 := integration(`{"integrationType":"AWS_PROXY","integrationUri":"` + fn.FunctionArn + `","payloadFormatVersion":"1.0"}`)
	short := integration(`{"integrationType":"AWS_PROXY","integrationUri":"` + fn.FunctionArn + `","payloadFormatVersion":"2.0","timeoutInMillis":1000}`)
	var ignored struct{}
	for key, target := range map[string]string{"GET /two": v2, "GET /one": v1, "GET /throw": v2, "GET /slow": short} {
		call(http.MethodPost, "/v2/apis/"+api.APIID+"/routes", "apigateway", `{"routeKey":"`+key+`","target":"integrations/`+target+`"}`, http.StatusCreated, &ignored)
	}
	call(http.MethodPost, "/v2/apis/"+api.APIID+"/stages", "apigateway", `{"stageName":"$default","autoDeploy":true}`, http.StatusCreated, &ignored)
	call(http.MethodPost, "/2015-03-31/functions/formats/policy", "lambda", `{"StatementId":"apigateway","Action":"lambda:InvokeFunction",`+
		`"Principal":"apigateway.amazonaws.com","SourceArn":"arn:aws:execute-api:us-east-1:000000000000:`+api.APIID+`/*/*"}`, http.StatusCreated, &ignored)
	base := "/_understudy/execute-api/" + api.APIID

	status, header, body := get(base + "/two")
	if status != http.StatusCreated || body != "two" || header.Get("X-Format") != "2.0" || header.Get("Content-Type") != "text/plain" {
		t.Errorf("GET /two (2.0) = %d %v %q, want 201 text/plain x-format: 2.0, two", status, header, body)
	}
	if got := header.Values("Set-Cookie"); !slices.Equal(got, []string{"a=1", "b=2"}) {
		t.Errorf("GET /two (2.0) Set-Cookie = %q, want one per cookie", got)
	}

	status, header, body = get(base + "/one")
	if status != http.StatusAccepted || body != "one" || header.Get("X-Format") != "1.0" || !slices.Equal(header.Values("X-Many"), []string{"a", "b"}) {
		t.Errorf("GET /one (1.0) = %d %v %q, want 202 x-format: 1.0, x-many: a and b, one", status, header, body)
	}

	gateway := func(path string, want apigatewayv2.GatewayAnswer) {
		t.Helper()
		status, _, body := get(base + path)
		var got struct{ Message string }
		if err := json.Unmarshal([]byte(body), &got); err != nil || status != want.Status || got.Message != want.Message {
			t.Errorf("GET %s = %d %s, want %d {\"message\":%q}", path, status, body, want.Status, want.Message)
		}
	}
	failures := apigatewayv2.FailureAnswersSwitch().Value
	gateway("/throw", failures.FunctionError)
	gateway("/slow", failures.Timeout)
}
