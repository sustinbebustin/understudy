package server_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// wire sends one request to srv on the host clients address and returns
// the answer.
func wire(t *testing.T, srv *server.Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, "http://localhost:4566"+path, strings.NewReader(body))
	if strings.HasPrefix(path, "/2015-03-31/") || strings.HasPrefix(path, "/2018-10-31/") {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// members decodes a JSON object answer into its members.
func members(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("answer %d %s is not a JSON object: %v", rec.Code, rec.Body, err)
	}
	return m
}

// hasKeys reports which of names m holds, as "name" or "-name".
func hasKeys(m map[string]json.RawMessage, names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		if _, ok := m[n]; ok {
			out[i] = n
		} else {
			out[i] = "-" + n
		}
	}
	return out
}

// The layer routes answer with the model's statuses (201 for a publish and
// a permission, 204 for the deletes; lambda-destinations-layers.md 6.1),
// echo only the members sent (6.3: test_lambda_api.snapshot.json:L8271-L8292),
// and refuse with Lambda's error casing: Message and Type for a 404,
// lowercase message and Type for a 400 (0; lambda-api.md 2.1:
// test_lambda_api.snapshot.json:L8320-L8355). Content.Location serves the
// archive as a package link does, and a deleted version's link is gone
// (11.5.3).
func TestLayerRoutesOnTheWire(t *testing.T) {
	srv, _ := build(t, server.Config{})
	archive := zipped(t, "layer content")
	publish := `{"Content":{"ZipFile":"` + base64.StdEncoding.EncodeToString(archive) + `"},"CompatibleRuntimes":["python3.12"]}`
	rec := wire(t, srv, http.MethodPost, "/2018-10-31/layers/shared/versions", publish)
	if rec.Code != http.StatusCreated {
		t.Fatalf("PublishLayerVersion = %d %s, want 201", rec.Code, rec.Body)
	}
	m := members(t, rec)
	if got := strings.Join(hasKeys(m, "Content", "LayerArn", "LayerVersionArn", "Description", "CreatedDate", "Version", "CompatibleRuntimes", "CompatibleArchitectures", "LicenseInfo"), " "); got !=
		"Content LayerArn LayerVersionArn Description CreatedDate Version CompatibleRuntimes -CompatibleArchitectures -LicenseInfo" {
		t.Errorf("PublishLayerVersion members = %s, want no CompatibleArchitectures or LicenseInfo, which were not sent", got)
	}
	var out struct {
		Content struct {
			Location, CodeSha256 string
			CodeSize             int64
		}
		LayerVersionArn, Description string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	if out.Content.CodeSha256 != base64.StdEncoding.EncodeToString(sum[:]) || out.Content.CodeSize != int64(len(archive)) || out.Description != "" {
		t.Errorf("Content = %+v, Description %q; want the archive's SHA-256 and size and \"\"", out.Content, out.Description)
	}
	const linkPrefix = "http://localhost:4566/_understudy/lambda-code/layer/us-east-1/000000000000/shared/1/"
	if !strings.HasPrefix(out.Content.Location, linkPrefix) {
		t.Fatalf("Content.Location = %q, want a link under %s", out.Content.Location, linkPrefix)
	}
	link := strings.TrimPrefix(out.Content.Location, "http://localhost:4566")
	got := wire(t, srv, http.MethodGet, link, "")
	if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "application/zip" || !bytes.Equal(got.Body.Bytes(), archive) ||
		got.Header().Get("X-Content-Type-Options") != "nosniff" || got.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("GET Content.Location = %d %v (%d bytes), want 200 application/zip with the archive, nosniff and a sandbox policy", got.Code, got.Header(), got.Body.Len())
	}

	if rec := wire(t, srv, http.MethodGet, "/2018-10-31/layers/shared/versions/1", ""); rec.Code != http.StatusOK {
		t.Errorf("GetLayerVersion = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := wire(t, srv, http.MethodGet, "/2018-10-31/layers?find=LayerVersion&Arn="+url.QueryEscape(out.LayerVersionArn), ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), out.LayerVersionArn) {
		t.Errorf("GetLayerVersionByArn = %d %s, want 200 with the version", rec.Code, rec.Body)
	}
	if rec := wire(t, srv, http.MethodGet, "/2018-10-31/layers/doesnotexist/versions", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"LayerVersions":[]}` {
		t.Errorf("ListLayerVersions of a layer that never existed = %d %s, want 200 {\"LayerVersions\":[]}", rec.Code, rec.Body)
	}

	missing := wire(t, srv, http.MethodGet, "/2018-10-31/layers/shared/versions/2", "")
	mm := members(t, missing)
	if missing.Code != http.StatusNotFound || missing.Header().Get("X-Amzn-Errortype") != "ResourceNotFoundException" ||
		string(mm["Message"]) != `"The resource you requested does not exist."` || string(mm["Type"]) != `"User"` {
		t.Errorf("GetLayerVersion of a missing version = %d %s %s, want 404 ResourceNotFoundException with Message and Type User", missing.Code, missing.Header().Get("X-Amzn-Errortype"), missing.Body)
	}
	zero := wire(t, srv, http.MethodGet, "/2018-10-31/layers/shared/versions/0", "")
	zm := members(t, zero)
	if zero.Code != http.StatusBadRequest || zero.Header().Get("X-Amzn-Errortype") != "InvalidParameterValueException" ||
		string(zm["message"]) != `"Layer Version Cannot be less than 1"` || string(zm["Type"]) != `"User"` {
		t.Errorf("GetLayerVersion of version 0 = %d %s %s, want 400 InvalidParameterValueException with message and Type User", zero.Code, zero.Header().Get("X-Amzn-Errortype"), zero.Body)
	}

	if rec := wire(t, srv, http.MethodPost, "/2018-10-31/layers/shared/versions/1/policy", `{"StatementId":"s1","Action":"lambda:GetLayerVersion","Principal":"*"}`); rec.Code != http.StatusCreated {
		t.Errorf("AddLayerVersionPermission = %d %s, want 201", rec.Code, rec.Body)
	}
	if rec := wire(t, srv, http.MethodDelete, "/2018-10-31/layers/shared/versions/1/policy/s1", ""); rec.Code != http.StatusNoContent {
		t.Errorf("RemoveLayerVersionPermission = %d %s, want 204", rec.Code, rec.Body)
	}
	if rec := wire(t, srv, http.MethodDelete, "/2018-10-31/layers/shared/versions/1", ""); rec.Code != http.StatusNoContent {
		t.Errorf("DeleteLayerVersion = %d %s, want 204", rec.Code, rec.Body)
	}
	if rec := wire(t, srv, http.MethodGet, link, ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET a deleted version's Content.Location = %d, want 404", rec.Code)
	}
}

// A function's Layers render as [{"Arn", "CodeSize"}], and a function
// without layers has no Layers key (lambda-destinations-layers.md 7.2:
// test_lambda_api.snapshot.json:L8469-L8519, L8631-L8636).
func TestFunctionLayersOnTheWire(t *testing.T) {
	srv, _ := build(t, server.Config{})
	archive := zipped(t, "layer content")
	if rec := wire(t, srv, http.MethodPost, "/2018-10-31/layers/shared/versions", `{"Content":{"ZipFile":"`+base64.StdEncoding.EncodeToString(archive)+`"}}`); rec.Code != http.StatusCreated {
		t.Fatalf("PublishLayerVersion = %d %s", rec.Code, rec.Body)
	}
	code := base64.StdEncoding.EncodeToString(zipped(t, "#!/bin/sh\n"))
	create := func(name, layers string) *httptest.ResponseRecorder {
		return wire(t, srv, http.MethodPost, "/2015-03-31/functions", `{"FunctionName":"`+name+`","Runtime":"provided.al2023","Handler":"bootstrap",`+
			`"Role":"arn:aws:iam::000000000000:role/r","Code":{"ZipFile":"`+code+`"}`+layers+`}`)
	}
	plain := create("plain", "")
	if plain.Code != http.StatusCreated {
		t.Fatalf("CreateFunction = %d %s", plain.Code, plain.Body)
	}
	if _, ok := members(t, plain)["Layers"]; ok {
		t.Errorf("a function without layers answers %s, want no Layers key", plain.Body)
	}
	layered := create("layered", `,"Layers":["arn:aws:lambda:us-east-1:000000000000:layer:shared:1"]`)
	if layered.Code != http.StatusCreated {
		t.Fatalf("CreateFunction with a layer = %d %s", layered.Code, layered.Body)
	}
	var layers []map[string]json.RawMessage
	if err := json.Unmarshal(members(t, layered)["Layers"], &layers); err != nil || len(layers) != 1 {
		t.Fatalf("Layers = %s, want one entry", members(t, layered)["Layers"])
	}
	if string(layers[0]["Arn"]) != `"arn:aws:lambda:us-east-1:000000000000:layer:shared:1"` || string(layers[0]["CodeSize"]) != itoa(len(archive)) || len(layers[0]) != 2 {
		t.Errorf("Layers[0] = %v, want Arn and CodeSize %d alone", layers[0], len(archive))
	}
}

func itoa(n int) string {
	b, err := json.Marshal(n)
	if err != nil {
		return ""
	}
	return string(b)
}

// A layer's archive may come from S3 by bucket and key, read as the
// caller; a key with a leading slash is taken
// (lambda-destinations-layers.md 6.3: test_lambda_api.snapshot.json:L9201-L9221).
func TestPublishLayerVersionFromS3(t *testing.T) {
	srv, _ := build(t, server.Config{})
	archive := zipped(t, "from s3")
	for _, step := range []struct{ method, path, body string }{
		{http.MethodPut, "/layer-bucket", ""},
		{http.MethodPut, "/layer-bucket//layercontent.zip", string(archive)},
	} {
		if rec := wire(t, srv, step.method, step.path, step.body); rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", step.method, step.path, rec.Code, rec.Body)
		}
	}
	rec := wire(t, srv, http.MethodPost, "/2018-10-31/layers/from-s3/versions", `{"Content":{"S3Bucket":"layer-bucket","S3Key":"/layercontent.zip"}}`)
	var out struct{ Content struct{ CodeSha256 string } }
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("PublishLayerVersion from S3 = %d %s", rec.Code, rec.Body)
	}
	sum := sha256.Sum256(archive)
	if out.Content.CodeSha256 != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Errorf("CodeSha256 = %q, want the SHA-256 of the object", out.Content.CodeSha256)
	}
}
