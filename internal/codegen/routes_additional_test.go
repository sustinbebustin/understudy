package codegen_test

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// routesInputs reads the REST fixture under testdata/routes, whose service
// config adds GET /things beside ListThings's modeled POST /list-things.
func routesInputs(t *testing.T) codegen.Inputs {
	t.Helper()
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile("testdata/routes/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cfg, err := codegen.ParseServiceConfig(bytes.NewReader(read("routes.service.json")), "routes")
	if err != nil {
		t.Fatal(err)
	}
	return codegen.Inputs{
		Config:   cfg,
		Origin:   "api-models-aws@fixture",
		IAM:      codegen.ActionsTable{Source: codegen.Source{Name: "testdata/routes/actions.json", Content: read("actions.json")}},
		Model:    codegen.Source{Name: "testdata/routes/routes.json", Content: read("routes.json")},
		Overlays: []codegen.Source{{Name: "testdata/routes/overlay.json", Content: read("overlay.json")}},
	}
}

// TestAdditionalRouteGolden: the added route is a row of the dispatch table
// beside the modeled one, pointing at the same descriptor, and the
// descriptor carries it with its query bindings, so the REST binder reads
// them from the query when a request arrives by that route.
func TestAdditionalRouteGolden(t *testing.T) {
	files, err := codegen.Generate(routesInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"routes_gen.go", "ops_gen.go"} {
		golden := "testdata/routes/" + name + ".golden"
		if *update {
			if err := os.WriteFile(golden, files[name], 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(files[name], want) {
			t.Errorf("%s differs from %s:\n%s\nIf the change is intended, run go test ./internal/codegen -run TestAdditionalRouteGolden -update and review the diff.", name, golden, files[name])
		}
	}
}

// TestAdditionalRouteFailures: an entry is refused when its route collides
// with another row, when it binds a member the input lacks or one the
// modeled route already reads from the head, when it names no modeled
// operation, and on a service that is not REST.
func TestAdditionalRouteFailures(t *testing.T) {
	const config = "understudy-gen ir: codegen/services/routes.json: "
	entry := func(method, uri string, query ...codegen.QueryBinding) []codegen.AdditionalRoute {
		return []codegen.AdditionalRoute{{
			Operation: "ListThings", Method: method, URI: uri, Query: query,
			Reason: "test", Evidence: codegen.Evidence{Label: "inferred", Source: "test"},
		}}
	}
	tests := []struct {
		name   string
		routes []codegen.AdditionalRoute
		want   string
	}{
		{
			name:   "a route another operation serves",
			routes: entry("POST", "/things"),
			want: config + "additionalRoutes ListThings POST /things matches the same requests as CreateThing POST /things; " +
				"an added route must reach requests no other row matches",
		},
		{
			name:   "the operation's own modeled route",
			routes: entry("POST", "/list-things"),
			want: config + "additionalRoutes ListThings POST /list-things matches the same requests as ListThings POST /list-things; " +
				"an added route must reach requests no other row matches",
		},
		{
			name:   "a member the input lacks",
			routes: entry("GET", "/things", codegen.QueryBinding{Param: "limit", Member: "Limit"}),
			want: config + "additionalRoutes ListThings binds Limit, which com.example.routes#ListThingsRequest does not have; " +
				"its members are NextToken, PageSize, Filter, Caller",
		},
		{
			name:   "a member the modeled route reads from the head",
			routes: entry("GET", "/things", codegen.QueryBinding{Param: "caller", Member: "Caller"}),
			want: config + "additionalRoutes ListThings binds Caller, which the modeled route already binds with an HTTP trait; " +
				"bind only members it reads from the body document",
		},
		{
			name: "no modeled operation",
			routes: []codegen.AdditionalRoute{{
				Operation: "ListThing", Method: "GET", URI: "/things", Reason: "test", Evidence: codegen.Evidence{Label: "inferred", Source: "test"},
			}},
			want: config + "additionalRoutes name [ListThing], which are not modeled operations of com.example.routes#Routes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := routesInputs(t)
			in.Config.AdditionalRoutes = tt.routes
			_, err := codegen.Generate(in)
			var gerr *codegen.Error
			if !errors.As(err, &gerr) || gerr.Stage != codegen.StageIR || err.Error() != tt.want {
				t.Fatalf("Generate = %v\nwant %s", err, tt.want)
			}
		})
	}
	t.Run("a service that is not REST", func(t *testing.T) {
		in := goldenInputs(t)
		in.Config.AdditionalRoutes = entry("GET", "/things")
		_, err := codegen.Generate(in)
		const want = "understudy-gen ir: codegen/services/golden.json: additionalRoutes apply to REST services; com.example.golden#Golden speaks [awsjson1_0 awsquery]"
		if err == nil || err.Error() != want {
			t.Fatalf("Generate = %v\nwant %s", err, want)
		}
	})
}

// TestServiceConfigAdditionalRouteEntries: an additionalRoutes entry names
// the operation, the method and URI of the route added beside the modeled
// one, the query parameters it binds onto input members, the reason and the
// evidence in the overlay evidence form; one operation has one entry.
func TestServiceConfigAdditionalRouteEntries(t *testing.T) {
	const head = `{"service": "com.amazonaws.sesv2#SimpleEmailService_v2", "package": "sesv2api", "model": "sesv2", "iamPrefix": "ses", "additionalRoutes": [`
	const ev = `"evidence": {"label": "aws-recorded", "source": "SES-14"}`
	const query = `"query": [{"param": "NextToken", "member": "NextToken"}, {"param": "PageSize", "member": "PageSize"}]`
	const entry = `{"operation": "ListEmailIdentities", "method": "GET", "uri": "/v2/email/identities", ` + query + `, "reason": "old SDKs", ` + ev + `}`
	const prefix = "understudy-gen load: codegen/services/sesv2.json: additionalRoutes[0]: "
	const needs = `an entry needs "operation", "method", "uri", the "reason" the modeled route does not serve and its "evidence"`
	tests := []struct{ name, entries, want string }{
		{"no reason", `{"operation": "ListEmailIdentities", "method": "GET", "uri": "/v2/email/identities", ` + ev + `}`, prefix + needs},
		{"no evidence", `{"operation": "ListEmailIdentities", "method": "GET", "uri": "/v2/email/identities", "reason": "r"}`, prefix + needs},
		{"no method", `{"operation": "ListEmailIdentities", "uri": "/v2/email/identities", "reason": "r", ` + ev + `}`, prefix + needs},
		{
			"an unknown key", `{"operation": "ListEmailIdentities", "method": "GET", "uri": "/v2/email/identities", "headers": [], "reason": "r", ` + ev + `}`,
			prefix + `json: unknown field "headers"`,
		},
		{
			"a malformed uri", `{"operation": "ListEmailIdentities", "method": "GET", "uri": "v2/email/identities", "reason": "r", ` + ev + `}`,
			prefix + `URI pattern "v2/email/identities" is invalid: it does not start with /`,
		},
		{
			"evidence in the list form", `{"operation": "ListEmailIdentities", "method": "GET", "uri": "/v2/email/identities", "reason": "r", "evidence": [{"label": "inferred", "source": "s"}]}`,
			prefix + `evidence: give one {label, source, validated?} object; the list form belongs to overlays`,
		},
		{
			"a query binding without a member", `{"operation": "ListEmailIdentities", "method": "GET", "uri": "/v2/email/identities", "query": [{"param": "NextToken"}], "reason": "r", ` + ev + `}`,
			prefix + `query[0]: a binding needs the query "param" and the input "member" it binds`,
		},
		{
			"a parameter bound twice", `{"operation": "ListEmailIdentities", "method": "GET", "uri": "/v2/email/identities", "query": [{"param": "P", "member": "NextToken"}, {"param": "P", "member": "PageSize"}], "reason": "r", ` + ev + `}`,
			prefix + "query[1]: the parameter P is bound twice; remove one",
		},
		{
			"a member bound twice", `{"operation": "ListEmailIdentities", "method": "GET", "uri": "/v2/email/identities", "query": [{"param": "A", "member": "NextToken"}, {"param": "B", "member": "NextToken"}], "reason": "r", ` + ev + `}`,
			prefix + "query[1]: the member NextToken is bound twice; remove one",
		},
		{
			"a parameter the uri already requires", `{"operation": "ListEmailIdentities", "method": "GET", "uri": "/v2/email/identities?NextToken", "query": [{"param": "NextToken", "member": "NextToken"}], "reason": "r", ` + ev + `}`,
			prefix + "query[0]: the uri already requires the parameter NextToken as a literal; a literal is a matching condition, not a binding",
		},
		{
			"two entries for one operation", entry + ", " + entry,
			"understudy-gen load: codegen/services/sesv2.json: additionalRoutes[1]: ListEmailIdentities has two entries; merge them into one",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := codegen.ParseServiceConfig(strings.NewReader(head+tt.entries+"]}"), "sesv2")
			if err == nil || err.Error() != tt.want {
				t.Fatalf("ParseServiceConfig = %v\nwant %s", err, tt.want)
			}
		})
	}
	cfg, err := codegen.ParseServiceConfig(strings.NewReader(head+entry+"]}"), "sesv2")
	want := []codegen.AdditionalRoute{{
		Operation: "ListEmailIdentities", Method: "GET", URI: "/v2/email/identities",
		Query:  []codegen.QueryBinding{{Param: "NextToken", Member: "NextToken"}, {Param: "PageSize", Member: "PageSize"}},
		Reason: "old SDKs", Evidence: codegen.Evidence{Label: "aws-recorded", Source: "SES-14"},
	}}
	if err != nil || !reflect.DeepEqual(cfg.AdditionalRoutes, want) {
		t.Fatalf("ParseServiceConfig = %+v, %v; want additionalRoutes %+v", cfg.AdditionalRoutes, err, want)
	}
}
