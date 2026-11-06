package edge_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// R1: AWS serves SSM's legacy Query protocol, a POST signed ssm with the
// parameters in the query string and no body, answering GetParameter with
// the parameter (localstack@8b9a79f
// tests/aws/services/apigateway/test_apigateway_ssm.snapshot.json:L33-L71,
// last_validated_date 2024-07-12; request
// tests/aws/services/apigateway/test_apigateway_ssm.py:L110-L158;
// ssm-parameters.md 2.3). understudy does not serve it: such a request
// answers 501 NotImplemented in SSM's JSON 1.1 envelope, naming the legacy
// Query protocol, the action and the X-Amz-Target alternative, never an
// unknown operation and never a parameter, and it is journaled as
// Unclaimed (spec conflict 7; ssm-parameters.md 12.1).
func TestSSMLegacyQueryIsNotImplemented(t *testing.T) {
	f := newFrontWith(t, nil, func(o *edge.Options) { o.LegacyQuery = []ref.ServiceID{"ssm"} }, ssmapi.Service())
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/?Action=GetParameter&Name=param-test-123", nil, "", "ssm"))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-amz-json-1.1" {
		t.Errorf("Content-Type = %q, want application/x-amz-json-1.1", got)
	}
	var body struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	if body.Type != "NotImplemented" {
		t.Errorf("__type = %q, want NotImplemented", body.Type)
	}
	for _, want := range []string{"Query", "GetParameter", "X-Amz-Target", "AmazonSSM.GetParameter"} {
		if !strings.Contains(body.Message, want) {
			t.Errorf("message %q does not name %q", body.Message, want)
		}
	}
	if strings.Contains(rec.Body.String(), "UnknownOperation") || strings.Contains(rec.Body.String(), "Parameter\"") {
		t.Errorf("body %s is an unknown operation or a parameter", rec.Body)
	}
	if reasons := f.unclaimedReasons(t); len(reasons) == 0 {
		t.Error("the Unclaimed event lists no reasons")
	}
}

// A service not in LegacyQuery keeps today's answer to the same shape of
// request: DynamoDB's unknown operation (error-matrix.md finding 6), not
// the legacy-Query 501.
func TestLegacyQueryIsPerService(t *testing.T) {
	f := newFrontWith(t, nil, func(o *edge.Options) { o.LegacyQuery = []ref.ServiceID{"ssm"} }, ssmapi.Service())
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/?Action=ListTables", nil, "", "dynamodb"))
	if rec.Code == http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "UnknownOperationException") {
		t.Errorf("answer = %d %s, want DynamoDB's UnknownOperationException", rec.Code, rec.Body)
	}
	if reasons := f.unclaimedReasons(t); slices.ContainsFunc(reasons, func(r string) bool { return strings.Contains(r, "legacy Query") }) {
		t.Errorf("reasons %q name the legacy Query rule for dynamodb", reasons)
	}
}
