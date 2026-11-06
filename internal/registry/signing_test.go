package registry_test

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// restRoute is one row of a synthetic route table: a Smithy http trait's
// method and URI, and the operation it binds.
type restRoute struct{ method, uri, op string }

// restService builds a restJson1 descriptor signing as signing whose
// operations are the routes' operations, sorted and numbered as the
// generator numbers them.
func restService(t *testing.T, id, signing string, routes ...restRoute) *registry.Service {
	t.Helper()
	var names []string
	for _, r := range routes {
		if !slices.Contains(names, r.op) {
			names = append(names, r.op)
		}
	}
	slices.Sort(names)
	s := service(id, names...)
	s.SigningName = signing
	s.Protocols = []ref.ProtocolID{ref.RESTJSON1}
	s.Routes = &registry.RouteTable{}
	for _, r := range routes {
		b, err := registry.ParseHTTPBinding(r.method, r.uri)
		if err != nil {
			t.Fatalf("ParseHTTPBinding(%s %s): %v", r.method, r.uri, err)
		}
		op, ok := s.Operation(r.op)
		if !ok {
			t.Fatalf("service %s has no operation %s", id, r.op)
		}
		s.Routes.Routes = append(s.Routes.Routes, &registry.Route{Method: b.Method, Path: b.Path, Query: b.Query, Operation: op})
	}
	return s
}

// apiGatewayV1 and apiGatewayV2 stand for the two API Gateway models: both
// sign as apigateway on one host and both name an operation
// CreateDeployment (apigateway-http.md finding 3 and 2.2; v1's URIs from
// https://docs.aws.amazon.com/apigateway/latest/api/API_CreateDeployment.html
// and API_TagResource.html, under /restapis and /tags). Every v2 URI is
// under /v2/, and no v1 URI is.
func apiGatewayV1(t *testing.T) *registry.Service {
	t.Helper()
	return restService(t, "apigateway", "apigateway",
		restRoute{http.MethodPost, "/restapis/{restapi_id}/deployments", "CreateDeployment"},
		restRoute{http.MethodGet, "/restapis/{restapi_id}/deployments/{deployment_id}", "GetDeployment"},
		restRoute{http.MethodPut, "/tags/{resource_arn}", "TagResource"},
	)
}

func apiGatewayV2(t *testing.T) *registry.Service {
	t.Helper()
	return restService(t, "apigatewayv2", "apigateway",
		restRoute{http.MethodPost, "/v2/apis/{ApiId}/deployments", "CreateDeployment"},
		restRoute{http.MethodGet, "/v2/apis/{ApiId}/deployments/{DeploymentId}", "GetDeployment"},
		restRoute{http.MethodPost, "/v2/tags/{ResourceArn}", "TagResource"},
	)
}

// wantDescriptorError checks that err is a *DescriptorError with problem p
// for the service added second, naming the signing name and the earlier
// service it collides with, so the reader can find both descriptors.
func wantDescriptorError(t *testing.T, err error, p registry.DescriptorProblem, added ref.ServiceID, signing string, other ref.ServiceID) {
	t.Helper()
	var target *registry.DescriptorError
	if !errors.As(err, &target) {
		t.Fatalf("New error = %v, want a *DescriptorError with problem %d", err, p)
	}
	if target.Problem != p || target.Service != added {
		t.Errorf("New error = problem %d for %s (%v), want problem %d for %s", target.Problem, target.Service, err, p, added)
	}
	for _, want := range []string{fmt.Sprintf("%q", signing), string(other)} {
		if !strings.Contains(target.Detail, want) {
			t.Errorf("Detail %q does not name %s", target.Detail, want)
		}
	}
}

// Several services may share a signing name when a claim rule tells their
// requests apart (ADR 0019): BySigningName returns them all, in the order
// New was given them.
func TestNewAcceptsSigningNamesAClaimRuleTellsApart(t *testing.T) {
	jsonService := func(id, signing, prefix string) *registry.Service {
		s := service(id, "ListStreams")
		s.SigningName, s.JSONTargetPrefix = signing, prefix
		s.Protocols = []ref.ProtocolID{ref.AWSJSON10}
		return s
	}
	queryService := func(id, signing, version string, ops ...string) *registry.Service {
		s := service(id, ops...)
		s.SigningName, s.QueryVersion = signing, version
		s.Protocols = []ref.ProtocolID{ref.AWSQuery}
		return s
	}
	cases := []struct {
		name  string
		build func(t *testing.T) []*registry.Service
	}{
		{
			// Two restJson1 models with the same operation names and route
			// tables no request can match both of.
			"two REST services with disjoint route tables",
			func(t *testing.T) []*registry.Service {
				t.Helper()
				return []*registry.Service{apiGatewayV1(t), apiGatewayV2(t)}
			},
		},
		{
			// DynamoDB Streams signs as dynamodb
			// (testdata/parity/dynamodb/R11/07-streams-signed-dynamodb.json);
			// the X-Amz-Target prefix tells it from DynamoDB (dynamodb-streams.md 3.2).
			"two awsJson services with different target prefixes",
			func(*testing.T) []*registry.Service {
				return []*registry.Service{
					jsonService("dynamodb", "dynamodb", "DynamoDB_20120810"),
					jsonService("dynamodbstreams", "dynamodb", "DynamoDBStreams_20120810"),
				}
			},
		},
		{
			// SES v1 is awsQuery at Version 2010-12-01 and v2 is restJson1
			// under /v2/email/; both sign as ses (ses.md 2.1, 3.4).
			"an awsQuery and a REST service",
			func(t *testing.T) []*registry.Service {
				t.Helper()
				return []*registry.Service{
					queryService("ses", "ses", "2010-12-01", "SendEmail"),
					restService(t, "sesv2", "ses", restRoute{http.MethodPost, "/v2/email/outbound-emails", "SendEmail"}),
				}
			},
		},
		{
			"two awsQuery services at different Versions",
			func(*testing.T) []*registry.Service {
				return []*registry.Service{
					queryService("one", "shared", "2010-12-01", "SendEmail"),
					queryService("two", "shared", "2019-09-27", "SendEmail"),
				}
			},
		},
		{
			"two rpcv2Cbor services with different shapes",
			func(*testing.T) []*registry.Service {
				a, b := service("one", "Get"), service("two", "Get")
				a.SigningName, b.SigningName = "shared", "shared"
				a.Shape, b.Shape = "com.example#One", "com.example#Two"
				a.Protocols, b.Protocols = []ref.ProtocolID{ref.RPCv2CBOR}, []ref.ProtocolID{ref.RPCv2CBOR}
				return []*registry.Service{a, b}
			},
		},
		{
			// No protocol family in common: the request's shape picks the rule.
			"an awsJson and a REST service",
			func(t *testing.T) []*registry.Service {
				t.Helper()
				return []*registry.Service{
					jsonService("one", "shared", "One_20240101"),
					restService(t, "two", "shared", restRoute{http.MethodGet, "/{Name}", "Get"}),
				}
			},
		},
		{
			"an awsJson and an awsQuery service",
			func(*testing.T) []*registry.Service {
				return []*registry.Service{
					jsonService("one", "shared", "One_20240101"),
					queryService("two", "shared", "2010-12-01", "Get"),
				}
			},
		},
		{
			// Every family both speak passes its row: awsJson by prefix,
			// awsQuery by Version.
			"two services speaking awsJson and awsQuery, both told apart",
			func(*testing.T) []*registry.Service {
				a, b := jsonService("one", "shared", "One_20240101"), jsonService("two", "shared", "Two_20240101")
				a.Protocols, b.Protocols = []ref.ProtocolID{ref.AWSJSON10, ref.AWSQuery}, []ref.ProtocolID{ref.AWSJSON10, ref.AWSQuery}
				a.QueryVersion, b.QueryVersion = "2024-01-01", "2024-06-01"
				return []*registry.Service{a, b}
			},
		},
		{
			"three services, each pair told apart",
			func(t *testing.T) []*registry.Service {
				t.Helper()
				return []*registry.Service{
					apiGatewayV1(t),
					queryService("apigatewayquery", "apigateway", "2015-07-09", "CreateDeployment"),
					apiGatewayV2(t),
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svcs := c.build(t)
			r, err := registry.New(svcs...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			signing := svcs[0].SigningName
			if got := r.BySigningName(signing); !slices.Equal(got, svcs) {
				t.Errorf("BySigningName(%q) = %v, want %v in New's order", signing, ids(got), ids(svcs))
			}
			reversed := slices.Clone(svcs)
			slices.Reverse(reversed)
			r, err = registry.New(reversed...)
			if err != nil {
				t.Fatalf("New in reverse order: %v", err)
			}
			if got := r.BySigningName(signing); !slices.Equal(got, reversed) {
				t.Errorf("BySigningName(%q) = %v, want %v in New's order", signing, ids(got), ids(reversed))
			}
		})
	}
}

func ids(svcs []*registry.Service) []ref.ServiceID {
	out := make([]ref.ServiceID, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, s.ID)
	}
	return out
}

// A pair no claim rule can tell apart is refused with the problem that says
// why, against the earlier service of the name (ADR 0019).
func TestNewRefusesSigningNamesNoClaimRuleTellsApart(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T) (a, b *registry.Service)
		want  registry.DescriptorProblem
	}{
		{
			// The same first literal: POST /restapis/x/deployments matches both.
			"two REST services sharing operation names and a route prefix",
			func(t *testing.T) (a, b *registry.Service) {
				t.Helper()
				return apiGatewayV1(t), restService(t, "apigatewayv2", "apigateway",
					restRoute{http.MethodPost, "/restapis/{ApiId}/deployments", "CreateDeployment"})
			},
			registry.OverlappingRouteTables,
		},
		{
			// A label first segment matches every first segment the other
			// table can start with.
			"a REST table whose routes start with a label",
			func(t *testing.T) (a, b *registry.Service) {
				t.Helper()
				return apiGatewayV2(t), restService(t, "buckets", "apigateway",
					restRoute{http.MethodGet, "/{Bucket}", "ListObjects"})
			},
			registry.OverlappingRouteTables,
		},
		{
			"a REST table whose route starts with a greedy label",
			func(t *testing.T) (a, b *registry.Service) {
				t.Helper()
				return restService(t, "one", "shared", restRoute{http.MethodGet, "/{Key+}", "Get"}),
					restService(t, "two", "shared", restRoute{http.MethodGet, "/v2/things", "List"})
			},
			registry.OverlappingRouteTables,
		},
		{
			// The check is over path prefixes: two methods on one first
			// literal still overlap.
			"two REST tables sharing a first literal under different methods",
			func(t *testing.T) (a, b *registry.Service) {
				t.Helper()
				return restService(t, "one", "shared", restRoute{http.MethodGet, "/v2/apis", "GetApis"}),
					restService(t, "two", "shared", restRoute{http.MethodPost, "/v2/apis/{ApiId}/deployments", "CreateDeployment"})
			},
			registry.OverlappingRouteTables,
		},
		{
			"a REST service without a route table",
			func(t *testing.T) (a, b *registry.Service) {
				t.Helper()
				b = apiGatewayV2(t)
				b.Routes = nil
				return apiGatewayV1(t), b
			},
			registry.DuplicateSigningName,
		},
		{
			"two awsJson services without target prefixes",
			func(*testing.T) (a, b *registry.Service) {
				a, b = service("one", "Get"), service("two", "Get")
				a.SigningName, b.SigningName = "shared", "shared"
				a.Protocols, b.Protocols = []ref.ProtocolID{ref.AWSJSON11}, []ref.ProtocolID{ref.AWSJSON10}
				return a, b
			},
			registry.DuplicateSigningName,
		},
		{
			// An equal non-empty prefix is refused as it is without a shared
			// signing name.
			"two awsJson services with one target prefix",
			func(*testing.T) (a, b *registry.Service) {
				a, b = service("one", "Get"), service("two", "Get")
				a.SigningName, b.SigningName = "shared", "shared"
				a.JSONTargetPrefix, b.JSONTargetPrefix = "Shared_20240101", "Shared_20240101"
				a.Protocols, b.Protocols = []ref.ProtocolID{ref.AWSJSON10}, []ref.ProtocolID{ref.AWSJSON10}
				return a, b
			},
			registry.DuplicateJSONTarget,
		},
		{
			"two awsQuery services at one Version",
			func(*testing.T) (a, b *registry.Service) {
				a, b = service("one", "SendEmail"), service("two", "VerifyEmailIdentity")
				a.SigningName, b.SigningName = "shared", "shared"
				a.QueryVersion, b.QueryVersion = "2010-12-01", "2010-12-01"
				a.Protocols, b.Protocols = []ref.ProtocolID{ref.AWSQuery}, []ref.ProtocolID{ref.AWSQuery}
				return a, b
			},
			registry.DuplicateSigningName,
		},
		{
			// A shared Action at that Version is the query index's refusal.
			"two awsQuery services sharing an action at one Version",
			func(*testing.T) (a, b *registry.Service) {
				a, b = service("one", "SendEmail"), service("two", "SendEmail")
				a.SigningName, b.SigningName = "shared", "shared"
				a.QueryVersion, b.QueryVersion = "2010-12-01", "2010-12-01"
				a.Protocols, b.Protocols = []ref.ProtocolID{ref.AWSQuery}, []ref.ProtocolID{ref.AWSQuery}
				return a, b
			},
			registry.DuplicateQueryAction,
		},
		{
			"two rpcv2Cbor services with one shape",
			func(*testing.T) (a, b *registry.Service) {
				a, b = service("one", "Get"), service("two", "Get")
				a.SigningName, b.SigningName = "shared", "shared"
				a.Shape, b.Shape = "com.example#Shared", "com.example#Shared"
				a.Protocols, b.Protocols = []ref.ProtocolID{ref.RPCv2CBOR}, []ref.ProtocolID{ref.RPCv2CBOR}
				return a, b
			},
			registry.DuplicateSigningName,
		},
		{
			"a service that declares no protocol",
			func(t *testing.T) (a, b *registry.Service) {
				t.Helper()
				b = service("two", "Get")
				b.SigningName = "apigateway"
				return apiGatewayV2(t), b
			},
			registry.DuplicateSigningName,
		},
		{
			// Every family both speak must pass: their awsJson prefixes
			// differ, but they share an awsQuery Version.
			"two services told apart in one family but not the other",
			func(*testing.T) (a, b *registry.Service) {
				a, b = service("one", "Get"), service("two", "List")
				a.SigningName, b.SigningName = "shared", "shared"
				a.JSONTargetPrefix, b.JSONTargetPrefix = "One_20240101", "Two_20240101"
				a.QueryVersion, b.QueryVersion = "2024-01-01", "2024-01-01"
				a.Protocols, b.Protocols = []ref.ProtocolID{ref.AWSJSON10, ref.AWSQuery}, []ref.ProtocolID{ref.AWSJSON10, ref.AWSQuery}
				return a, b
			},
			registry.DuplicateSigningName,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, b := c.build(t)
			_, err := registry.New(a, b)
			if c.want == registry.DuplicateJSONTarget || c.want == registry.DuplicateQueryAction {
				// Refused by the index the pair collides in, which predates
				// shared signing names; only the problem is checked.
				wantProblem(t, err, c.want)
				return
			}
			wantDescriptorError(t, err, c.want, b.ID, a.SigningName, a.ID)
		})
	}
}

// Each service is checked against every earlier candidate of its name: a
// third service that collides with the second only is refused naming it.
func TestNewChecksEachServiceAgainstEveryEarlierCandidate(t *testing.T) {
	third := restService(t, "third", "apigateway", restRoute{http.MethodGet, "/v2/domainnames", "GetDomainNames"})
	_, err := registry.New(apiGatewayV1(t), apiGatewayV2(t), third)
	wantDescriptorError(t, err, registry.OverlappingRouteTables, "third", "apigateway", "apigatewayv2")
}

func TestBySigningNameReturnsACopy(t *testing.T) {
	v1, v2 := apiGatewayV1(t), apiGatewayV2(t)
	r, err := registry.New(v1, v2)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := r.BySigningName("apigateway")
	if len(got) != 2 {
		t.Fatalf("BySigningName(apigateway) = %v, want both API Gateway services", ids(got))
	}
	got[0], got[1] = nil, nil
	if again := r.BySigningName("apigateway"); !slices.Equal(again, []*registry.Service{v1, v2}) {
		t.Errorf("after mutating the returned slice, BySigningName(apigateway) = %v", again)
	}
}

// A service without a signing name is never indexed, so no request's
// credential scope can name it.
func TestBySigningNameIgnoresEmptyNames(t *testing.T) {
	a, b := service("one", "Get"), service("two", "Get")
	a.SigningName, b.SigningName = "", ""
	r, err := registry.New(a, b)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.BySigningName(""); got != nil {
		t.Errorf(`BySigningName("") = %v, want nil`, ids(got))
	}
}

// genTable draws a REST route table over a few first segments, with every
// route to its own operation.
func genTable(t *rapid.T, id string, firsts []string) *registry.Service {
	segment := rapid.OneOf(rapid.SampledFrom(firsts), rapid.Just("{L}"), rapid.Just("{G+}"))
	n := rapid.IntRange(1, 4).Draw(t, id+" routes")
	type row struct{ method, uri string }
	var rows []row
	seen := map[string]bool{}
	for i := range n {
		first := segment.Draw(t, fmt.Sprintf("%s route %d first", id, i))
		uri := "/" + first
		if !strings.HasSuffix(first, "+}") {
			var uriSb451 strings.Builder
			for j := range rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("%s route %d depth", id, i)) {
				uriSb451.WriteString("/" + rapid.SampledFrom([]string{"x", "y", fmt.Sprintf("{M%d}", j)}).Draw(t, fmt.Sprintf("%s route %d segment %d", id, i, j)))
			}
			uri += uriSb451.String()
		}
		method := rapid.SampledFrom([]string{http.MethodGet, http.MethodPost}).Draw(t, fmt.Sprintf("%s route %d method", id, i))
		b, err := registry.ParseHTTPBinding(method, uri)
		if err != nil {
			t.Fatalf("ParseHTTPBinding(%s %s): %v", method, uri, err)
		}
		sig := (&registry.Route{Method: b.Method, Path: b.Path}).Signature()
		if seen[sig] {
			continue
		}
		seen[sig] = true
		rows = append(rows, row{method, uri})
	}
	names := make([]string, len(rows))
	for i := range rows {
		names[i] = fmt.Sprintf("Op%02d", i)
	}
	s := service(id, names...)
	s.SigningName = "shared"
	s.Protocols = []ref.ProtocolID{ref.RESTJSON1}
	s.Routes = &registry.RouteTable{}
	for i, r := range rows {
		b, err := registry.ParseHTTPBinding(r.method, r.uri)
		if err != nil {
			t.Fatalf("ParseHTTPBinding(%s %s): %v", r.method, r.uri, err)
		}
		s.Routes.Routes = append(s.Routes.Routes, &registry.Route{Method: b.Method, Path: b.Path, Query: b.Query, Operation: s.Operations[i]})
	}
	return s
}

// Whenever New accepts two REST services sharing a signing name, no request
// matches a route of both, so rule 8's first match is the only one (ADR
// 0019: no single (method, path) matches in both tables).
func TestAcceptedRouteTablesNeverMatchOneRequest(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genTable(t, "one", []string{"v2", "restapis", "tags"})
		b := genTable(t, "two", []string{"v2", "restapis", "tags"})
		if _, err := registry.New(a, b); err != nil {
			return
		}
		for i := range 20 {
			n := rapid.IntRange(0, 4).Draw(t, fmt.Sprintf("request %d depth", i))
			path := ""
			var pathSb498 strings.Builder
			for j := range n {
				pathSb498.WriteString("/" + rapid.SampledFrom([]string{"v2", "restapis", "tags", "x", "y", "%76%32", "z"}).Draw(t, fmt.Sprintf("request %d segment %d", i, j)))
			}
			path += pathSb498.String()
			if path == "" {
				path = "/"
			}
			method := rapid.SampledFrom([]string{http.MethodGet, http.MethodPost}).Draw(t, fmt.Sprintf("request %d method", i))
			ra, inA := a.Routes.Match(method, path, nil, nil)
			rb, inB := b.Routes.Match(method, path, nil, nil)
			if inA && inB {
				t.Fatalf("New accepted both tables, but %s %s matches %s (%s) and %s (%s)",
					method, path, ra.Operation.ID, ra.Signature(), rb.Operation.ID, rb.Signature())
			}
		}
	})
}

// Tables whose routes all start with literals from disjoint sets are
// always accepted, whatever follows the first segment.
func TestDisjointFirstLiteralsAreAccepted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genTable(t, "one", []string{"restapis", "tags", "vpclinks"})
		b := genTable(t, "two", []string{"v2"})
		literalFirst := func(s *registry.Service) bool {
			for _, r := range s.Routes.Routes {
				if _, ok := r.Path[0].(registry.Literal); !ok {
					return false
				}
			}
			return true
		}
		if !literalFirst(a) || !literalFirst(b) {
			return
		}
		if _, err := registry.New(a, b); err != nil {
			t.Fatalf("New refused tables starting with disjoint literals: %v", err)
		}
	})
}
