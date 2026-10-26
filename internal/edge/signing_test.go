package edge_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/server"
)

// route is one row of a synthetic REST table: a Smithy http trait's method
// and URI, and the operation it binds.
type route struct{ method, uri, op string }

// restDescriptor builds a REST descriptor in protocol p signing as signing,
// with one modeled operation per distinct route operation, numbered in
// name order as the generator numbers them.
func restDescriptor(t *testing.T, id ref.ServiceID, signing string, p ref.ProtocolID, routes ...route) *registry.Service {
	t.Helper()
	svc := &registry.Service{ID: id, SigningName: signing, IAMPrefix: string(id), ARNNamespace: string(id), Protocols: []ref.ProtocolID{p}}
	var names []string
	for _, r := range routes {
		if !slices.Contains(names, r.op) {
			names = append(names, r.op)
		}
	}
	slices.Sort(names)
	for i, name := range names {
		svc.Operations = append(svc.Operations, &registry.Operation{Service: svc, ID: ref.OperationID{Service: id, Name: name}, Ordinal: i, Kind: registry.Modeled})
	}
	svc.Routes = &registry.RouteTable{}
	for _, r := range routes {
		b, err := registry.ParseHTTPBinding(r.method, r.uri)
		if err != nil {
			t.Fatalf("ParseHTTPBinding(%s %s): %v", r.method, r.uri, err)
		}
		op, ok := svc.Operation(r.op)
		if !ok {
			t.Fatalf("%s has no operation %s", id, r.op)
		}
		svc.Routes.Routes = append(svc.Routes.Routes, &registry.Route{Method: b.Method, Path: b.Path, Query: b.Query, Operation: op})
	}
	return svc
}

// The two API Gateway models sign as apigateway and share operation names
// such as CreateDeployment; v2's URIs are all under /v2/ (apigateway-http.md
// finding 3 and 2.2), v1's under /restapis
// (https://docs.aws.amazon.com/apigateway/latest/api/API_CreateDeployment.html).
func apiGatewayV1(t *testing.T) *registry.Service {
	t.Helper()
	return restDescriptor(t, "apigateway", "apigateway", ref.RESTJSON1,
		route{http.MethodPost, "/restapis/{restapi_id}/deployments", "CreateDeployment"},
		route{http.MethodGet, "/restapis/{restapi_id}/deployments/{deployment_id}", "GetDeployment"},
	)
}

func apiGatewayV2(t *testing.T) *registry.Service {
	t.Helper()
	return restDescriptor(t, "apigatewayv2", "apigateway", ref.RESTJSON1,
		route{http.MethodPost, "/v2/apis/{ApiId}/deployments", "CreateDeployment"},
		route{http.MethodGet, "/v2/apis/{ApiId}/deployments/{DeploymentId}", "GetDeployment"},
	)
}

// calls returns the operations of the journaled API calls, in order.
func (f *front) calls(t *testing.T) []ref.OperationID {
	t.Helper()
	events := f.journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events
	ops := make([]ref.OperationID, 0, len(events))
	for i := range events {
		call, ok := events[i].Detail.(journal.APICall)
		if !ok {
			t.Fatalf("event %+v is not an APICall", events[i].Detail)
		}
		ops = append(ops, call.Op)
	}
	return ops
}

// lastCall returns the operation of the latest journaled API call.
func (f *front) lastCall(t *testing.T) ref.OperationID {
	t.Helper()
	ops := f.calls(t)
	if len(ops) == 0 {
		t.Fatal("no API call was journaled")
	}
	return ops[len(ops)-1]
}

// A request signed for a name several REST services share goes to the one
// whose route table matches it (ARCHITECTURE.md 4.1 rule 8, ADR 0019), so
// v1's and v2's CreateDeployment never answer each other's requests.
func TestRESTCandidatesAreTriedInRegistrationOrder(t *testing.T) {
	cases := []struct {
		method, path string
		want         ref.OperationID
	}{
		{http.MethodPost, "/v2/apis/a1b2c3/deployments", ref.OperationID{Service: "apigatewayv2", Name: "CreateDeployment"}},
		{http.MethodGet, "/v2/apis/a1b2c3/deployments/d1", ref.OperationID{Service: "apigatewayv2", Name: "GetDeployment"}},
		{http.MethodPost, "/restapis/r1s2t3/deployments", ref.OperationID{Service: "apigateway", Name: "CreateDeployment"}},
		{http.MethodGet, "/restapis/r1s2t3/deployments/d1", ref.OperationID{Service: "apigateway", Name: "GetDeployment"}},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			f := newFront(t, server.Codecs(), apiGatewayV1(t), apiGatewayV2(t))
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, request(t, c.method, c.path, http.Header{"Content-Type": {"application/json"}}, `{}`, "apigateway"))
			// Nothing binds the synthetic operations, so the routed call is
			// the unbound operation's 501 (ADR 0003).
			if rec.Code != http.StatusNotImplemented {
				t.Errorf("status = %d %s, want the unbound operation's 501", rec.Code, rec.Body)
			}
			if got := f.lastCall(t); got != c.want {
				t.Errorf("the request reached %s, want %s", got, c.want)
			}
			if page := f.journal.Query(journal.Filter{Kind: journal.KindUnclaimed}); len(page.Events) != 0 {
				t.Errorf("a routed request was journaled as Unclaimed: %+v", page.Events[0].Detail)
			}
		})
	}
}

// A request no candidate's table matches is the first REST candidate's
// unknown operation, as a single REST service answers one today.
func TestUnmatchedRequestIsTheFirstRESTCandidatesUnknownOperation(t *testing.T) {
	for _, order := range [][]ref.ServiceID{{"apigateway", "apigatewayv2"}, {"apigatewayv2", "apigateway"}} {
		t.Run(string(order[0])+" first", func(t *testing.T) {
			byID := map[ref.ServiceID]*registry.Service{"apigateway": apiGatewayV1(t), "apigatewayv2": apiGatewayV2(t)}
			f := newFront(t, server.Codecs(), byID[order[0]], byID[order[1]])
			f.serve(t, request(t, http.MethodGet, "/domainnames", nil, "", "apigateway"))
			if got := f.lastCall(t); got.Service != order[0] {
				t.Errorf("the unmatched request was %s's, want the first registered candidate %s's", got, order[0])
			}
		})
	}
}

// One REST service signed for a name other than lambda or s3 is routed by
// its table too, not answered as its unknown operation.
func TestASingleRESTServiceIsRoutedByItsTable(t *testing.T) {
	f := newFront(t, server.Codecs(), apiGatewayV2(t))
	f.serve(t, request(t, http.MethodPost, "/v2/apis/a1b2c3/deployments", http.Header{"Content-Type": {"application/json"}}, `{}`, "apigateway"))
	if got, want := f.lastCall(t), (ref.OperationID{Service: "apigatewayv2", Name: "CreateDeployment"}); got != want {
		t.Errorf("the request reached %s, want %s", got, want)
	}
}

// A candidate whose codec is not registered is skipped; with none left the
// request is unclaimed: the AWS-shaped 400 and the journaled reasons
// (ARCHITECTURE.md 4.1 rule 9).
func TestRESTCandidatesWithoutACodecAreUnclaimed(t *testing.T) {
	f := newFront(t, nil, apiGatewayV1(t), apiGatewayV2(t))
	got := f.serve(t, request(t, http.MethodPost, "/v2/apis/a1b2c3/deployments", http.Header{"Content-Type": {"application/json"}}, `{}`, "apigateway"))
	if !strings.HasPrefix(got, "400\n") {
		t.Errorf("answer = %s, want rule 9's 400", got)
	}
	reasons := f.unclaimedReasons(t)
	for _, svc := range []string{"apigateway", "apigatewayv2"} {
		want := "rule 8: " + svc + ": no " + string(ref.RESTJSON1) + " codec is registered"
		if !slices.Contains(reasons, want) {
			t.Errorf("reasons = %q, want %q", reasons, want)
		}
	}
	if ops := f.calls(t); len(ops) != 0 {
		t.Errorf("journaled calls = %v, want none", ops)
	}
}

// SES v1 (awsQuery, Version 2010-12-01) and v2 (restJson1 under /v2/email/)
// both sign as ses (ses.md 2.1, 3.4): rule 7 claims the form request by
// (Action, Version) and rule 8 the REST one by v2's table.
func TestQueryAndRESTServicesShareASigningName(t *testing.T) {
	ses := func() *registry.Service {
		svc := &registry.Service{
			ID: "ses", SigningName: "ses", IAMPrefix: "ses", ARNNamespace: "ses",
			Protocols: []ref.ProtocolID{ref.AWSQuery}, QueryVersion: "2010-12-01", XMLNamespace: "http://ses.amazonaws.com/doc/2010-12-01/",
		}
		svc.Operations = []*registry.Operation{{Service: svc, ID: ref.OperationID{Service: "ses", Name: "SendEmail"}, Kind: registry.Modeled}}
		return svc
	}
	sesv2 := func(t *testing.T) *registry.Service {
		t.Helper()
		return restDescriptor(t, "sesv2", "ses", ref.RESTJSON1, route{http.MethodPost, "/v2/email/outbound-emails", "SendEmail"})
	}
	form := http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}
	cases := []struct {
		name, method, path, body string
		header                   http.Header
		want                     ref.OperationID
	}{
		{"query", http.MethodPost, "/", "Action=SendEmail&Version=2010-12-01", form, ref.OperationID{Service: "ses", Name: "SendEmail"}},
		{"rest", http.MethodPost, "/v2/email/outbound-emails", `{}`, http.Header{"Content-Type": {"application/json"}}, ref.OperationID{Service: "sesv2", Name: "SendEmail"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFront(t, server.Codecs(), ses(), sesv2(t))
			f.serve(t, request(t, c.method, c.path, c.header, c.body, "ses"))
			if got := f.lastCall(t); got != c.want {
				t.Errorf("the request reached %s, want %s", got, c.want)
			}
		})
	}
	t.Run("rest without a matching route", func(t *testing.T) {
		f := newFront(t, server.Codecs(), ses(), sesv2(t))
		f.serve(t, request(t, http.MethodGet, "/v2/email/nowhere", nil, "", "ses"))
		if got := f.lastCall(t); got.Service != "sesv2" {
			t.Errorf("the request reached %s, want the REST candidate sesv2's unknown operation", got)
		}
	})
}

// dynamoDBStreams stands for DynamoDB Streams, whose SDKs sign as dynamodb
// (testdata/parity/dynamodb/R11/07-streams-signed-dynamodb.json) under its
// own X-Amz-Target prefix (dynamodb-streams.md 3.2).
func dynamoDBStreams() *registry.Service {
	svc := &registry.Service{
		ID: "dynamodbstreams", Shape: "com.amazonaws.dynamodbstreams#DynamoDBStreams_20120810", SigningName: "dynamodb",
		IAMPrefix: "dynamodb", ARNNamespace: "dynamodb", Protocols: []ref.ProtocolID{ref.AWSJSON10}, JSONTargetPrefix: "DynamoDBStreams_20120810",
	}
	svc.Operations = []*registry.Operation{{Service: svc, ID: ref.OperationID{Service: "dynamodbstreams", Name: "ListStreams"}, Kind: registry.Modeled}}
	return svc
}

// Rule 6 finds the service by the target prefix and keeps the signing-name
// cross-check against that service's name.
func TestJSONServicesShareASigningName(t *testing.T) {
	cases := []struct {
		name, target, signedFor string
		want                    ref.ServiceID
	}{
		{"streams", "DynamoDBStreams_20120810.ListStreams", "dynamodb", "dynamodbstreams"},
		{"dynamodb", "DynamoDB_20120810.ListTables", "dynamodb", "dynamodb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFront(t, nil, dynamoDBStreams())
			f.serve(t, request(t, http.MethodPost, "/", jsonHeader(c.target, "application/x-amz-json-1.0"), `{}`, c.signedFor))
			if got := f.lastCall(t); got.Service != c.want {
				t.Errorf("the request reached %s, want %s", got, c.want)
			}
		})
	}
	// A Streams target signed for sqs is SQS's to answer, as it is with
	// Streams unregistered: sqs.md 2.3's UnknownOperationException.
	t.Run("signed for another service", func(t *testing.T) {
		f := newFront(t, nil, dynamoDBStreams())
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/", jsonHeader("DynamoDBStreams_20120810.ListStreams", "application/x-amz-json-1.0"), `{}`, "sqs"))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "UnknownOperationException") {
			t.Errorf("answer = %d %s, want 400 UnknownOperationException", rec.Code, rec.Body)
		}
		if slices.ContainsFunc(f.calls(t), func(op ref.OperationID) bool { return op.Service == "dynamodbstreams" }) {
			t.Error("a request signed for sqs reached dynamodbstreams")
		}
	})
}
