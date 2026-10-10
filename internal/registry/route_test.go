package registry_test

import (
	"errors"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

func TestParseHTTPBinding(t *testing.T) {
	b, err := registry.ParseHTTPBinding("PUT", "/{Bucket}/{Key+}?partNumber&uploadId&x-id=UploadPart")
	if err != nil {
		t.Fatal(err)
	}
	want := registry.HTTPBinding{
		Method: "PUT",
		Path:   []registry.Segment{registry.Label{Name: "Bucket"}, registry.Greedy{Name: "Key"}},
		Query: []registry.QueryMatch{
			registry.QueryPresent{Key: "partNumber"},
			registry.QueryPresent{Key: "uploadId"},
			registry.QueryEquals{Key: "x-id", Value: "UploadPart"},
		},
	}
	if !reflect.DeepEqual(b, want) {
		t.Errorf("ParseHTTPBinding = %+v, want %+v", b, want)
	}
	root, err := registry.ParseHTTPBinding("GET", "/")
	if err != nil || len(root.Path) != 0 || len(root.Query) != 0 {
		t.Errorf("ParseHTTPBinding(GET /) = %+v, %v", root, err)
	}
	escaped, err := registry.ParseHTTPBinding("GET", "/a%20b")
	if err != nil || !reflect.DeepEqual(escaped.Path, []registry.Segment{registry.Literal{Value: "a b"}}) {
		t.Errorf("ParseHTTPBinding(GET /a%%20b) = %+v, %v, want the literal decoded", escaped, err)
	}
	if got := match(t, table(t, "GET /a%20b"), "GET", "/a%20b"); got != "GET /a%20b" {
		t.Errorf("Match(/a%%20b) = %q; a literal with an escape must match its own URI", got)
	}
	for _, uri := range []string{
		"", "x", "/a//b", "/a/", "/{a}b", "/a{b}", "/{}", "/{a+}/{b+}", "/{a+}/{b}", "/a?", "/a?=b",
		"/{a}/{a}", "/{a}/{a+}", "/{+}", "/a/./b", "/a/../b", "/..",
		"/a?b&b", "/a?b&b=c", "/a?b=c&b=d", "/a%2Fb", "/a%zz", "/%2E%2E",
	} {
		var target *registry.PatternError
		if _, err := registry.ParseHTTPBinding("GET", uri); !errors.As(err, &target) {
			t.Errorf("ParseHTTPBinding(GET %q) error = %v, want *PatternError", uri, err)
		}
	}
	if _, err := registry.ParseHTTPBinding("", "/"); err == nil {
		t.Error("ParseHTTPBinding with no method succeeded")
	}
}

// table builds a route table whose operations are named after their row.
func table(t *testing.T, rows ...string) *registry.RouteTable {
	t.Helper()
	s := &registry.Service{ID: "svc"}
	rt := &registry.RouteTable{}
	for i, row := range rows {
		spec, headers, _ := strings.Cut(row, " +")
		method, uri, _ := strings.Cut(spec, " ")
		b, err := registry.ParseHTTPBinding(method, uri)
		if err != nil {
			t.Fatal(err)
		}
		op := &registry.Operation{Service: s, ID: ref.OperationID{Service: s.ID, Name: row}, Ordinal: i, Kind: registry.Modeled}
		r := &registry.Route{HTTPBinding: b, Operation: op}
		if headers != "" {
			r.Headers = strings.Split(headers, ",")
		}
		rt.Routes = append(rt.Routes, r)
	}
	return rt
}

func match(t *testing.T, rt *registry.RouteTable, method, target string, headers ...string) string {
	t.Helper()
	u, err := url.ParseRequestURI(target)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for _, name := range headers {
		h.Set(name, "x")
	}
	r, ok := rt.Match(method, u.EscapedPath(), u.Query(), h)
	if !ok {
		return ""
	}
	return r.Operation.ID.Name
}

func TestMatchSmithySpecificityExamples(t *testing.T) {
	// The three routing examples of the Smithy http-bindings specification.
	one := table(t, "GET /abc/bcd/{xyz}", "GET /abc/{xyz}/cde", "GET /{xyz}/bcd/cde")
	two := table(t, "GET /abc/bcd/{xyz}", "GET /abc/{xyz}/cde", "GET /{xyz}/bcd/cde?def=efg")
	three := table(t, "GET /abc/{xyz+}/bcd", "GET /abc/{xyz+}")
	tests := []struct {
		rt     *registry.RouteTable
		target string
		want   string
	}{
		{one, "/abc/bcd/cde", "GET /abc/bcd/{xyz}"},
		{one, "/abc/foo/cde", "GET /abc/{xyz}/cde"},
		{one, "/foo/bcd/cde", "GET /{xyz}/bcd/cde"},
		{two, "/abc/bcd/cde?def=efg", "GET /abc/bcd/{xyz}"},
		{two, "/abc/foo/cde?def=efg", "GET /abc/{xyz}/cde"},
		{two, "/foo/bcd/cde?def=efg", "GET /{xyz}/bcd/cde?def=efg"},
		{two, "/foo/bcd/cde", ""},
		{three, "/abc/foo/bar/bcd", "GET /abc/{xyz+}/bcd"},
		{three, "/abc/foo/bar/bcd/", "GET /abc/{xyz+}/bcd"},
		{three, "/abc/foo/bar/baz", "GET /abc/{xyz+}"},
		{three, "/abc", ""},
	}
	for _, tt := range tests {
		if got := match(t, tt.rt, "GET", tt.target); got != tt.want {
			t.Errorf("Match(%s) = %q, want %q", tt.target, got, tt.want)
		}
	}
}

func TestMatchLambdaRoutes(t *testing.T) {
	rt := table(t,
		"GET /2018-10-31/layers",
		"GET /2018-10-31/layers?find=LayerVersion",
		"GET /2015-03-31/functions/{FunctionName}",
		"DELETE /2015-03-31/functions/{FunctionName}",
		"GET /2019-09-30/functions/{FunctionName}/provisioned-concurrency",
		"GET /2019-09-30/functions/{FunctionName}/provisioned-concurrency?List=ALL",
		"GET /2017-03-31/tags/{Resource}",
	)
	tests := []struct{ method, target, want string }{
		{"GET", "/2018-10-31/layers", "GET /2018-10-31/layers"},
		{"GET", "/2018-10-31/layers?find=LayerVersion&Arn=x", "GET /2018-10-31/layers?find=LayerVersion"},
		{"GET", "/2018-10-31/layers?find=Other", "GET /2018-10-31/layers"},
		{"GET", "/2019-09-30/functions/f/provisioned-concurrency?List=ALL", "GET /2019-09-30/functions/{FunctionName}/provisioned-concurrency?List=ALL"},
		{"GET", "/2019-09-30/functions/f/provisioned-concurrency?Qualifier=1", "GET /2019-09-30/functions/{FunctionName}/provisioned-concurrency"},
		// Go and botocore percent-encode the colon of name:qualifier.
		{"GET", "/2015-03-31/functions/worker%3Alive", "GET /2015-03-31/functions/{FunctionName}"},
		{"DELETE", "/2015-03-31/functions/worker/", "DELETE /2015-03-31/functions/{FunctionName}"},
		// An encoded slash stays inside its label.
		{"GET", "/2017-03-31/tags/arn%3Aaws%3Alambda%3Aus-east-1%3A000000000000%3Afunction%2Fx", "GET /2017-03-31/tags/{Resource}"},
		{"PUT", "/2015-03-31/functions/f", ""},
		{"GET", "/2015-03-31/functions/", ""},
	}
	for _, tt := range tests {
		if got := match(t, rt, tt.method, tt.target); got != tt.want {
			t.Errorf("Match(%s %s) = %q, want %q", tt.method, tt.target, got, tt.want)
		}
	}
}

func TestMatchS3Rows(t *testing.T) {
	rt := table(t,
		"GET /",
		"GET /{Bucket}",
		"GET /{Bucket}?list-type=2",
		"GET /{Bucket}?analytics",
		"GET /{Bucket}?analytics&id",
		"PUT /{Bucket}/{Key+}",
		"PUT /{Bucket}/{Key+} +x-amz-copy-source",
		"PUT /{Bucket}/{Key+}?partNumber&uploadId",
		"PUT /{Bucket}/{Key+}?partNumber&uploadId +x-amz-copy-source",
		"GET /{Bucket}/{Key+}",
		"GET /{Bucket}/{Key+}?attributes +x-amz-object-attributes",
	)
	tests := []struct {
		method, target string
		headers        []string
		want           string
	}{
		{"GET", "/", nil, "GET /"},
		{"GET", "/bucket", nil, "GET /{Bucket}"},
		{"GET", "/bucket/", nil, "GET /{Bucket}"},
		{"GET", "/bucket?list-type=2&prefix=a", nil, "GET /{Bucket}?list-type=2"},
		{"GET", "/bucket?list-type=1", nil, "GET /{Bucket}"},
		{"GET", "/bucket?analytics", nil, "GET /{Bucket}?analytics"},
		{"GET", "/bucket?analytics=&id=a", nil, "GET /{Bucket}?analytics&id"},
		{"PUT", "/bucket/a/b.txt", nil, "PUT /{Bucket}/{Key+}"},
		{"PUT", "/bucket/dir/", nil, "PUT /{Bucket}/{Key+}"},
		{"PUT", "/bucket//", nil, "PUT /{Bucket}/{Key+}"},
		{"PUT", "/bucket/a/../b", nil, "PUT /{Bucket}/{Key+}"},
		{"PUT", "/bucket/k", []string{"X-Amz-Copy-Source"}, "PUT /{Bucket}/{Key+} +x-amz-copy-source"},
		{"PUT", "/bucket/k?partNumber=1&uploadId=u", nil, "PUT /{Bucket}/{Key+}?partNumber&uploadId"},
		{"PUT", "/bucket/k?partNumber=1&uploadId=u", []string{"x-amz-copy-source"}, "PUT /{Bucket}/{Key+}?partNumber&uploadId +x-amz-copy-source"},
		{"PUT", "/bucket/k?uploadId=u", nil, "PUT /{Bucket}/{Key+}"},
		{"GET", "/bucket/k?attributes", nil, "GET /{Bucket}/{Key+}"},
		{"GET", "/bucket/k?attributes", []string{"x-amz-object-attributes"}, "GET /{Bucket}/{Key+}?attributes +x-amz-object-attributes"},
		{"PUT", "/bucket", nil, ""},
	}
	for _, tt := range tests {
		if got := match(t, rt, tt.method, tt.target, tt.headers...); got != tt.want {
			t.Errorf("Match(%s %s %v) = %q, want %q", tt.method, tt.target, tt.headers, got, tt.want)
		}
	}
}

// word draws a path segment that needs no escaping.
func word() *rapid.Generator[string] { return rapid.StringMatching(`[a-z][a-z0-9-]{0,6}`) }

// routesOf builds a table over bindings in a random order and returns it with
// the operation name of each binding's row.
func routesOf(t *rapid.T, bindings map[string]registry.HTTPBinding) *registry.RouteTable {
	s := &registry.Service{ID: "svc"}
	names := rapid.Permutation(slices.Sorted(maps.Keys(bindings))).Draw(t, "order")
	rt := &registry.RouteTable{}
	for i, name := range names {
		op := &registry.Operation{Service: s, ID: ref.OperationID{Service: s.ID, Name: name}, Ordinal: i, Kind: registry.Modeled}
		rt.Routes = append(rt.Routes, &registry.Route{HTTPBinding: bindings[name], Operation: op})
	}
	return rt
}

func literals(words []string) []registry.Segment {
	out := make([]registry.Segment, len(words))
	for i, w := range words {
		out[i] = registry.Literal{Value: w}
	}
	return out
}

func concat(parts ...[]registry.Segment) []registry.Segment {
	var out []registry.Segment
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func matchRapid(t *rapid.T, rt *registry.RouteTable, path string, query url.Values) string {
	r, ok := rt.Match("GET", path, query, http.Header{})
	if !ok {
		t.Fatalf("no route matched %s", path)
	}
	return r.Operation.ID.Name
}

// At the first segment where routes differ, a literal beats a label and a
// label beats a greedy label, whatever the row order.
func TestMatchPrefersLiteralOverLabelOverGreedy(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		prefix := rapid.SliceOfN(word(), 0, 3).Draw(t, "prefix")
		w := word().Draw(t, "segment")
		suffix := rapid.SliceOfN(word(), 0, 3).Draw(t, "suffix")
		all := map[string]registry.HTTPBinding{
			"literal": {Method: "GET", Path: concat(literals(prefix), literals([]string{w}), literals(suffix))},
			"label":   {Method: "GET", Path: concat(literals(prefix), []registry.Segment{registry.Label{Name: "L"}}, literals(suffix))},
			"greedy":  {Method: "GET", Path: concat(literals(prefix), []registry.Segment{registry.Greedy{Name: "G"}}, literals(suffix))},
		}
		want := "literal"
		if !rapid.Bool().Draw(t, "with literal") {
			delete(all, "literal")
			want = "label"
		}
		path := "/" + strings.Join(append(append(append([]string{}, prefix...), w), suffix...), "/")
		if got := matchRapid(t, routesOf(t, all), path, url.Values{}); got != want {
			t.Fatalf("Match(%s) = %s, want %s", path, got, want)
		}
	})
}

// Path specificity beats any number of query conditions.
func TestMatchPrefersPathOverQuery(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		prefix := rapid.SliceOfN(word(), 0, 3).Draw(t, "prefix")
		w := word().Draw(t, "segment")
		keys := rapid.SliceOfNDistinct(word(), 1, 4, rapid.ID[string]).Draw(t, "keys")
		query := url.Values{}
		conds := make([]registry.QueryMatch, 0, len(keys))
		for _, k := range keys {
			query.Set(k, "v")
			conds = append(conds, registry.QueryPresent{Key: k})
		}
		all := map[string]registry.HTTPBinding{
			"path":  {Method: "GET", Path: concat(literals(prefix), literals([]string{w}))},
			"query": {Method: "GET", Path: concat(literals(prefix), []registry.Segment{registry.Label{Name: "L"}}), Query: conds},
		}
		path := "/" + strings.Join(append(append([]string{}, prefix...), w), "/")
		if got := matchRapid(t, routesOf(t, all), path, query); got != "path" {
			t.Fatalf("Match(%s?%s) = %s, want the more specific path", path, query.Encode(), got)
		}
	})
}

// With equal segments up to the shorter pattern, more segments win; with
// equal paths, more query conditions win.
func TestMatchPrefersMoreSegmentsThenMoreQuery(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		head := word().Draw(t, "head")
		middle := rapid.SliceOfN(word(), 1, 3).Draw(t, "greedy part")
		tail := rapid.SliceOfN(word(), 1, 3).Draw(t, "tail")
		fewer := rapid.IntRange(0, len(tail)-1).Draw(t, "shorter tail")
		greedy := []registry.Segment{registry.Literal{Value: head}, registry.Greedy{Name: "G"}}
		all := map[string]registry.HTTPBinding{
			"longer":  {Method: "GET", Path: concat(greedy, literals(tail))},
			"shorter": {Method: "GET", Path: concat(greedy, literals(tail[len(tail)-fewer:]))},
		}
		path := "/" + strings.Join(append(append([]string{head}, middle...), tail...), "/")
		if got := matchRapid(t, routesOf(t, all), path, url.Values{}); got != "longer" {
			t.Fatalf("Match(%s) = %s, want the pattern with more segments", path, got)
		}

		keys := rapid.SliceOfNDistinct(word(), 2, 4, rapid.ID[string]).Draw(t, "keys")
		query := url.Values{}
		conds := make([]registry.QueryMatch, 0, len(keys))
		for _, k := range keys {
			query.Set(k, "v")
			conds = append(conds, registry.QueryEquals{Key: k, Value: "v"})
		}
		split := rapid.IntRange(0, len(conds)-1).Draw(t, "fewer conditions")
		same := concat(literals([]string{head}), []registry.Segment{registry.Label{Name: "L"}})
		all = map[string]registry.HTTPBinding{
			"more":  {Method: "GET", Path: same, Query: conds},
			"fewer": {Method: "GET", Path: same, Query: conds[:split]},
		}
		path = "/" + head + "/" + middle[0]
		if got := matchRapid(t, routesOf(t, all), path, query); got != "more" {
			t.Fatalf("Match(%s?%s) = %s, want the pattern with more query conditions", path, query.Encode(), got)
		}
	})
}
