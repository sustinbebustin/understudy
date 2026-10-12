//go:build smoke

package smoke_test

import (
	"bytes"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// The smoke principal: the access key "test" of the default account, whose
// root every signed request is.
const (
	accessKey = "test"
	account   = "000000000000"
	principal = "arn:aws:iam::" + account + ":root"
	region    = "us-east-1"
)

// request is one raw HTTP request, signed for service with SigV4 unless
// service is empty.
type request struct {
	method string
	// base is the endpoint; empty means the emulator's HTTP endpoint.
	base    string
	path    string
	service string
	header  map[string]string
	body    []byte
}

// response is what the emulator answered.
type response struct {
	status int
	header http.Header
	body   string
}

// send sends r to emu and reads the whole answer.
func send(t *testing.T, emu *emulator, r request) response {
	t.Helper()
	base := r.base
	if base == "" {
		base = emu.endpoint
	}
	method := r.method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(t.Context(), method, base+r.path, bytes.NewReader(r.body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	if r.service != "" {
		reg, err := aws.KnownRegions().Parse(region)
		if err != nil {
			t.Fatal(err)
		}
		auth.Sign(req, r.body, auth.Signer{AccessKeyID: accessKey, SecretAccessKey: "test", Region: reg, Service: r.service}, time.Now())
	}
	resp, err := emu.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, base+r.path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: reading the answer: %v", method, base+r.path, err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: string(data)}
}

// must fails the test unless resp has status want.
func must(t *testing.T, what string, resp response, want int) response {
	t.Helper()
	if resp.status != want {
		t.Fatalf("%s = %d %s, want %d", what, resp.status, resp.body, want)
	}
	return resp
}

// awsJSON sends an awsJson1_0 or awsJson1_1 call.
func awsJSON(t *testing.T, emu *emulator, version, service, target, body string) response {
	t.Helper()
	return send(t, emu, request{
		path:    "/",
		service: service,
		header:  map[string]string{"Content-Type": "application/x-amz-json-" + version, "X-Amz-Target": target},
		body:    []byte(body),
	})
}

// awsQuery sends an awsQuery call of action at version with params.
func awsQuery(t *testing.T, emu *emulator, service, action, version string, params url.Values) response {
	t.Helper()
	form := url.Values{"Action": {action}, "Version": {version}}
	maps.Copy(form, params)
	return send(t, emu, request{
		path:    "/",
		service: service,
		header:  map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=utf-8"},
		body:    []byte(form.Encode()),
	})
}

// element returns the text of the first <name> element in an XML body.
func element(t *testing.T, body, name string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, "<"+name+">")
	if !ok {
		t.Fatalf("no <%s> in %s", name, body)
	}
	value, _, ok := strings.Cut(rest, "</"+name+">")
	if !ok {
		t.Fatalf("no </%s> in %s", name, body)
	}
	return value
}
