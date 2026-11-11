package server_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"net/http"
	"net/url"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// The least-privilege loop (conformance/iam-loop, ARCHITECTURE.md 17) in
// miniature for Lambda: the policy /iam/policy renders from a user's calls
// under advise admits the same calls under enforce, and nothing is denied
// (iam-enforcement-surfaces.md 12.6). Step 8 asks about each call's action
// on the function or alias its input names, the resource the access record
// keeps, so the rendered statements, which name that resource, cover the
// questions; asked on "*", the writes were refused.
func TestTheRenderedPolicyAdmitsTheSameLambdaCallsUnderEnforce(t *testing.T) {
	adviseClient, adviseEndpoint, u := enforced(t, server.IAMEnforceAdvise)
	createLoopFunction(t, adviseEndpoint)
	advised := loopLambdaCalls(t, adviseEndpoint, u)
	policy, err := adviseClient.IAMPolicy(t.Context(), userU)
	if err != nil {
		t.Fatal(err)
	}

	enforceClient, enforceEndpoint, u := enforced(t, server.IAMEnforceEnforce)
	createLoopFunction(t, enforceEndpoint)
	putUserPolicy(t, enforceEndpoint, string(policy.Document))
	enforced := loopLambdaCalls(t, enforceEndpoint, u)
	for name, status := range advised {
		if enforced[name] != status {
			t.Errorf("%s under enforce with the rendered policy = %d, want %d as under advise", name, enforced[name], status)
		}
	}
	if denied := enforcementAdvisories(t, enforceClient, "access-denied"); len(denied) != 0 {
		t.Errorf("access-denied advisories under enforce with the rendered policy: %q", denied)
	}
	if t.Failed() {
		t.Logf("the rendered policy: %s", policy.Document)
	}
}

// loopFunction is the function the loop test's user calls, in the default
// account and region.
const loopFunction = "arn:aws:lambda:us-east-1:000000000000:function:loop"

// createLoopFunction creates the function loop as root, with an execution
// role that trusts Lambda.
func createLoopFunction(t *testing.T, endpoint string) {
	t.Helper()
	roleTrustingLambda(t, endpoint, "r")
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create("bootstrap")
	if err == nil {
		_, err = f.Write([]byte("#!/bin/sh\n"))
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	create := `{"FunctionName":"loop","Runtime":"provided.al2023","Handler":"bootstrap","Role":"arn:aws:iam::000000000000:role/r","Code":{"ZipFile":"` +
		base64.StdEncoding.EncodeToString(b.Bytes()) + `"}}`
	if got := lambdaCall(t, dummy, endpoint, http.MethodPost, "/2015-03-31/functions", create); got.status != http.StatusCreated {
		t.Fatalf("CreateFunction = %d %s", got.status, got.body)
	}
}

// loopLambdaCalls makes the user's calls on the function loop, a missing
// function and an alias, and returns each call's status by name.
func loopLambdaCalls(t *testing.T, endpoint string, u signer) map[string]int {
	t.Helper()
	tags := "/2017-03-31/tags/" + url.PathEscape(loopFunction)
	calls := []struct{ name, method, path, body string }{
		{"GetFunction", http.MethodGet, "/2015-03-31/functions/loop", ""},
		{"GetFunction of a missing function", http.MethodGet, "/2015-03-31/functions/ghost", ""},
		{"GetFunctionConfiguration", http.MethodGet, "/2015-03-31/functions/loop/configuration", ""},
		{"UpdateFunctionConfiguration", http.MethodPut, "/2015-03-31/functions/loop/configuration", `{"Description":"looped"}`},
		{"PutFunctionConcurrency", http.MethodPut, "/2017-10-31/functions/loop/concurrency", `{"ReservedConcurrentExecutions":1}`},
		{"GetFunctionConcurrency", http.MethodGet, "/2019-09-30/functions/loop/concurrency", ""},
		{"DeleteFunctionConcurrency", http.MethodDelete, "/2017-10-31/functions/loop/concurrency", ""},
		{"TagResource", http.MethodPost, tags, `{"Tags":{"team":"loop"}}`},
		{"ListTags", http.MethodGet, tags, ""},
		{"UntagResource", http.MethodDelete, tags + "?tagKeys=team", ""},
		{"PublishVersion", http.MethodPost, "/2015-03-31/functions/loop/versions", `{}`},
		{"CreateAlias", http.MethodPost, "/2015-03-31/functions/loop/aliases", `{"Name":"live","FunctionVersion":"1"}`},
		{"UpdateAlias", http.MethodPut, "/2015-03-31/functions/loop/aliases/live", `{"Description":"looped"}`},
	}
	out := make(map[string]int, len(calls))
	for _, c := range calls {
		got := lambdaCall(t, u, endpoint, c.method, c.path, c.body)
		out[c.name] = got.status
		if got.status == http.StatusForbidden {
			t.Logf("%s = %d %s", c.name, got.status, got.body)
		}
	}
	return out
}

// lambdaCall sends a signed restJson Lambda request.
func lambdaCall(t *testing.T, s signer, endpoint, method, path, body string) wire {
	t.Helper()
	req := newRequest(t, method, endpoint+path, body)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return signedCall(t, s, "lambda", req, body)
}
