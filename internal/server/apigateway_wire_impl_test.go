package server_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/server"
)

// signedAPIGatewayGet sends GET path signed for apigateway, the signing
// name REST and HTTP APIs share (ADR 0019), and returns the status and
// body.
func signedAPIGatewayGet(t *testing.T, endpoint, path string) (status int, body []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	auth.Sign(req, nil, auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "apigateway"}, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

// REST APIs are wired: a request signed for apigateway on a /restapis
// path reaches apigateway's route table, and an operation the unit leaves
// unbound answers 501 naming it rather than an unknown service (CLAUDE.md
// invariant 3).
func TestAPIGatewayIsWired(t *testing.T) {
	_, endpoint := adminServer(t, server.Config{})
	status, body := signedAPIGatewayGet(t, endpoint, "/restapis")
	if want := "understudy does not implement apigateway.GetRestApis"; status != http.StatusNotImplemented || !bytes.Contains(body, []byte(want)) {
		t.Errorf("GET /restapis = %d %s, want 501 containing %q", status, body, want)
	}
}

// The two services of signing name apigateway are tried in registration
// order and a path neither route table matches is the first one's unknown
// operation (ADR 0019 rule 8). wire.go registers HTTP APIs before REST
// APIs, so the call is journaled as apigatewayv2's and never apigateway's.
func TestAPIGatewayUnmatchedPathIsV2Unknown(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	status, body := signedAPIGatewayGet(t, endpoint, "/understudy-no-such-path")
	if status == http.StatusNotImplemented {
		t.Fatalf("GET /understudy-no-such-path = %d %s, want an unknown operation, not 501", status, body)
	}
	for svc, want := range map[string]int{"apigatewayv2": 1, "apigateway": 0} {
		page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Service: svc, Kind: "api-call"})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(page.Events); got != want {
			t.Errorf("%s api-call events = %d, want %d (the first registered candidate answers)", svc, got, want)
		}
	}
}
