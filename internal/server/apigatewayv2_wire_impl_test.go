package server_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/server"
)

// HTTP APIs are wired: a request signed for apigateway, the signing name
// v1 and v2 share (ADR 0019), on a /v2 path reaches apigatewayv2's route
// table, and an operation left unbound (VPC links) answers 501
// naming it rather than an unknown service (CLAUDE.md invariant 3).
func TestAPIGatewayV2IsWired(t *testing.T) {
	_, endpoint := adminServer(t, server.Config{})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+"/v2/vpclinks", http.NoBody)
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if want := "understudy does not implement apigatewayv2.GetVpcLinks"; resp.StatusCode != http.StatusNotImplemented || !bytes.Contains(body, []byte(want)) {
		t.Errorf("GET /v2/vpclinks = %d %s, want 501 containing %q", resp.StatusCode, body, want)
	}
}
