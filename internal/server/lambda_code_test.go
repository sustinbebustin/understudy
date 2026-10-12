package server_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// GetFunction's Code.Location is a URL on the host the client addressed
// that serves the function's zip package without credentials, as AWS's
// presigned S3 URL does (lambda-api.md 6.1); a link to a package the
// function no longer runs is not found.
func TestLambdaCodeLocationServesThePackage(t *testing.T) {
	srv, _ := build(t, server.Config{})
	lambda := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "http://localhost:4566"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	pkg := zipped(t, "first")
	create := `{"FunctionName":"fn","Runtime":"provided.al2023","Handler":"bootstrap","Role":"arn:aws:iam::000000000000:role/r","Code":{"ZipFile":"` + base64.StdEncoding.EncodeToString(pkg) + `"}}`
	if rec := lambda(http.MethodPost, "/2015-03-31/functions", create); rec.Code != http.StatusCreated {
		t.Fatalf("CreateFunction = %d %s", rec.Code, rec.Body)
	}
	location := func() string {
		t.Helper()
		rec := lambda(http.MethodGet, "/2015-03-31/functions/fn", "")
		var out struct {
			Code struct{ Location, RepositoryType string }
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("GetFunction = %d %s", rec.Code, rec.Body)
		}
		if out.Code.RepositoryType != "S3" || !strings.HasPrefix(out.Code.Location, "http://localhost:4566/_understudy/lambda-code/") {
			t.Fatalf("Code = %+v, want RepositoryType S3 and a link on the addressed host", out.Code)
		}
		return out.Code.Location
	}
	first := location()
	rec := lambda(http.MethodGet, strings.TrimPrefix(first, "http://localhost:4566"), "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" || !bytes.Equal(rec.Body.Bytes(), pkg) {
		t.Fatalf("GET Code.Location = %d %q (%d bytes), want 200 application/zip with the package", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	update := `{"ZipFile":"` + base64.StdEncoding.EncodeToString(zipped(t, "second")) + `"}`
	if rec := lambda(http.MethodPut, "/2015-03-31/functions/fn/code", update); rec.Code != http.StatusOK {
		t.Fatalf("UpdateFunctionCode = %d %s", rec.Code, rec.Body)
	}
	if location() == first {
		t.Fatal("Code.Location did not change with the package")
	}
	if rec := lambda(http.MethodGet, strings.TrimPrefix(first, "http://localhost:4566"), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET the first package's link after an update = %d %s, want 404", rec.Code, rec.Body)
	}
}

// CreateFunction takes its package from S3 by bucket and key, reading it
// as the caller; a missing bucket or key and an object that is no zip
// answer AWS's recorded InvalidParameterValueException texts
// (lambda-api.md 4.3).
func TestLambdaCodeFromS3(t *testing.T) {
	srv, _ := build(t, server.Config{})
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "http://localhost:4566"+path, strings.NewReader(body))
		if strings.HasPrefix(path, "/2015-03-31/") {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	pkg := zipped(t, "from s3")
	for _, step := range []struct{ method, path, body string }{
		{http.MethodPut, "/code-bucket", ""},
		{http.MethodPut, "/code-bucket/fn.zip", string(pkg)},
		{http.MethodPut, "/code-bucket/corrupt.zip", "this is not a zip archive"},
	} {
		if rec := do(step.method, step.path, step.body); rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", step.method, step.path, rec.Code, rec.Body)
		}
	}
	create := func(name, bucket, key string) *httptest.ResponseRecorder {
		t.Helper()
		return do(http.MethodPost, "/2015-03-31/functions", `{"FunctionName":"`+name+`","Runtime":"provided.al2023","Handler":"bootstrap","Role":"arn:aws:iam::000000000000:role/r","Code":{"S3Bucket":"`+bucket+`","S3Key":"`+key+`"}}`)
	}
	rec := create("fn", "code-bucket", "fn.zip")
	var created struct{ CodeSha256 string }
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &created) != nil {
		t.Fatalf("CreateFunction from S3 = %d %s", rec.Code, rec.Body)
	}
	sum := sha256.Sum256(pkg)
	if created.CodeSha256 != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatalf("CodeSha256 = %q, want the SHA-256 of the object", created.CodeSha256)
	}
	for _, tc := range []struct{ name, bucket, key, message string }{
		{"no-bucket", "missing-bucket", "fn.zip", "Error occurred while GetObject. S3 Error Code: NoSuchBucket. S3 Error Message: The specified bucket does not exist"},
		{"no-key", "code-bucket", "missing.zip", "Error occurred while GetObject. S3 Error Code: NoSuchKey. S3 Error Message: The specified key does not exist."},
		{"corrupt", "code-bucket", "corrupt.zip", "Could not unzip uploaded file. Please check your file, then try to upload again."},
	} {
		rec := create(tc.name, tc.bucket, tc.key)
		var out struct{ Message string }
		if rec.Code != http.StatusBadRequest || rec.Header().Get("X-Amzn-Errortype") != "InvalidParameterValueException" ||
			json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Message != tc.message {
			t.Errorf("CreateFunction %s = %d %s %s, want 400 InvalidParameterValueException %q", tc.name, rec.Code, rec.Header().Get("X-Amzn-Errortype"), rec.Body, tc.message)
		}
	}
}

// zipped is a zip archive holding bootstrap with body.
func zipped(t *testing.T, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create("bootstrap")
	if err == nil {
		_, err = io.WriteString(f, body)
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
