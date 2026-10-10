package lambdaapi

import (
	"net/url"
	"testing"

	"github.com/sustinbebustin/understudy/internal/registry"
)

// TestRoutesFollowSmithySpecificity: a literal segment beats a label, more
// segments beat fewer, and a query literal beats none (wire-rest.md 1.1,
// lambda-api.md 4.1). Function names arrive percent-encoded when they are
// ARNs.
func TestRoutesFollowSmithySpecificity(t *testing.T) {
	if _, err := registry.New(Service()); err != nil {
		t.Fatal(err)
	}
	arn := url.PathEscape("arn:aws:lambda:us-east-1:123456789012:function:fn")
	tests := []struct{ method, target, want string }{
		{"GET", "/2015-03-31/functions", "ListFunctions"},
		{"GET", "/2015-03-31/functions/", "ListFunctions"},
		{"POST", "/2015-03-31/functions", "CreateFunction"},
		{"GET", "/2015-03-31/functions/fn", "GetFunction"},
		{"GET", "/2015-03-31/functions/" + arn, "GetFunction"},
		{"DELETE", "/2015-03-31/functions/fn?Qualifier=1", "DeleteFunction"},
		{"GET", "/2015-03-31/functions/fn/configuration", "GetFunctionConfiguration"},
		{"PUT", "/2015-03-31/functions/fn/configuration", "UpdateFunctionConfiguration"},
		{"PUT", "/2015-03-31/functions/fn/code", "UpdateFunctionCode"},
		{"POST", "/2015-03-31/functions/" + arn + "/invocations", "Invoke"},
		{"GET", "/2015-03-31/functions/fn/policy", "GetPolicy"},
		{"DELETE", "/2015-03-31/functions/fn/policy/sid-1", "RemovePermission"},
		{"POST", "/2015-03-31/functions/fn/aliases", "CreateAlias"},
		{"GET", "/2015-03-31/functions/fn/aliases/live", "GetAlias"},
		{"POST", "/2015-03-31/functions/fn/versions", "PublishVersion"},
		{"GET", "/2015-03-31/functions/fn/versions", "ListVersionsByFunction"},
		{"POST", "/2015-03-31/event-source-mappings", "CreateEventSourceMapping"},
		{"GET", "/2015-03-31/event-source-mappings/uuid-1", "GetEventSourceMapping"},
		{"DELETE", "/2015-03-31/event-source-mappings/uuid-1", "DeleteEventSourceMapping"},
		{"GET", "/2019-09-30/functions/fn/provisioned-concurrency?List=ALL", "ListProvisionedConcurrencyConfigs"},
		{"GET", "/2019-09-30/functions/fn/provisioned-concurrency?Qualifier=1", "GetProvisionedConcurrencyConfig"},
		{"PUT", "/2017-10-31/functions/fn/concurrency", "PutFunctionConcurrency"},
		{"GET", "/2021-10-31/functions/fn/url", "GetFunctionUrlConfig"},
		{"POST", "/2021-10-31/functions/fn/url", "CreateFunctionUrlConfig"},
		{"GET", "/2017-03-31/tags/" + arn, "ListTags"},
		{"POST", "/2017-03-31/tags/" + arn, "TagResource"},
		{"GET", "/2016-08-19/account-settings", "GetAccountSettings"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			u, err := url.Parse(tt.target)
			if err != nil {
				t.Fatal(err)
			}
			r, ok := Service().Routes.Match(tt.method, u.EscapedPath(), u.Query(), nil)
			if !ok {
				t.Fatalf("no route; want %s", tt.want)
			}
			if r.Operation.ID.Name != tt.want {
				t.Fatalf("routed to %s, want %s", r.Operation.ID.Name, tt.want)
			}
		})
	}
}

// TestInvokeFunctionUrlIsARawOperation: the function URL data plane is a
// rawOperations entry (ARCHITECTURE.md 5.3): a Raw descriptor with its IAM
// action, no schema, no route row and a BindRawHTTP binder method.
func TestInvokeFunctionUrlIsARawOperation(t *testing.T) {
	op, ok := Service().Operation("InvokeFunctionUrl")
	if !ok {
		t.Fatal("no InvokeFunctionUrl descriptor")
	}
	if op.Kind != registry.Raw || op.Input != nil || op.Output != nil || len(op.IAM) != 1 || op.IAM[0].String() != "lambda:InvokeFunctionUrl" {
		t.Fatalf("InvokeFunctionUrl = %+v", op)
	}
	for _, r := range Service().Routes.Routes {
		if r.Operation == op {
			t.Fatalf("InvokeFunctionUrl has a route row %+v; the edge claims it by host and prefix", r.HTTPBinding)
		}
	}
	_ = Binder.InvokeFunctionUrl
}

// TestCreateFunctionAcceptsTheRecordedBodyCap: CreateFunction's cap is
// 70,167,211 bytes (error-matrix.md 5.6 T9).
func TestCreateFunctionAcceptsTheRecordedBodyCap(t *testing.T) {
	op, ok := Service().Operation("CreateFunction")
	if !ok {
		t.Fatal("no CreateFunction descriptor")
	}
	if op.MaxRequestBytes != 70167211 || op.SuccessCode != 201 {
		t.Fatalf("CreateFunction MaxRequestBytes, SuccessCode = %d, %d; want 70167211, 201", op.MaxRequestBytes, op.SuccessCode)
	}
}
