package resource_test

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/apigateway"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/resource"
)

// methods is the method part of switch apigateway.invoke.routing: which of
// a resource's explicit method and its ANY serves a request, and whether
// HEAD falls back to GET, are UNVERIFIED (apigateway-rest.md 4.4,
// APIGW1-08), so every test reads them from the switch.
func methods() resource.Methods { return apigateway.RoutingSwitch().Value.Methods }

// A path part is a literal, {name} or {name+} (apigateway-rest.md 3.2:
// "A greedy path variable is {name+}"; the literal alphabet is the
// known-issues page's "alphanumeric characters, underscores, hyphens,
// periods, commas, colons"); anything else is refused.
func TestParsePart(t *testing.T) {
	for in, want := range map[string]resource.Part{
		"pets":        resource.Literal{Value: "pets"},
		"a.b_c-d,e:f": resource.Literal{Value: "a.b_c-d,e:f"},
		"{id}":        resource.Var{Name: "id"},
		"{proxy+}":    resource.Greedy{Name: "proxy"},
		"{child+}":    resource.Greedy{Name: "child"},
	} {
		got, err := resource.ParsePart(in)
		if err != nil || got != want {
			t.Errorf("ParsePart(%q) = %#v, %v; want %#v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "a/b", "/", "{a", "a}", "{a}b", "x{a}", "{}", "{+}", "pets?"} {
		if got, err := resource.ParsePart(in); err == nil {
			t.Errorf("ParsePart(%q) = %#v, want a *PartError", in, got)
		} else if err.PathPart != in || err.Problem == "" || !strings.Contains(err.Error(), fmt.Sprintf("%q", in)) {
			t.Errorf("ParsePart(%q) error = %+v (%q), want it to name the part and the problem", in, err, err.Error())
		}
	}
}

// Normalization of the recorded proxy events on stage "stage"
// (apigateway-rest.md 4.2 table, localstack@8b9a79f
// tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json
// L5-L959 and L1238-L1355): leading duplicate slashes are dropped,
// interior ones kept, a trailing slash kept, nothing decoded.
func TestNormalize(t *testing.T) {
	encoded := "/proxy-value/api/user/test%2Balias@gmail.com/plus/test+alias@gmail.com"
	for in, want := range map[string]resource.Target{
		"/stage/proxy-value":               {Stage: "stage", Path: "/proxy-value", ContextPath: "/stage/proxy-value"},
		"/stage/proxy-value/":              {Stage: "stage", Path: "/proxy-value/", ContextPath: "/stage/proxy-value/"},
		"/stage/proxy-value//double-slash": {Stage: "stage", Path: "/proxy-value//double-slash", ContextPath: "/stage/proxy-value//double-slash"},
		"//stage/proxy-value":              {Stage: "stage", Path: "/proxy-value", ContextPath: "/stage/proxy-value"},
		"/stage//proxy-value":              {Stage: "stage", Path: "/proxy-value", ContextPath: "/stage//proxy-value"},
		"/stage//hardcoded":                {Stage: "stage", Path: "/hardcoded", ContextPath: "/stage//hardcoded"},
		"/stage" + encoded:                 {Stage: "stage", Path: encoded, ContextPath: "/stage" + encoded},
	} {
		if got := resource.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %+v\nwant %+v", in, got, want)
		}
	}
	// The stage root is the root resource path (apigateway-rest.md 4.1:
	// the first segment is the stage, the rest the resource path).
	for _, in := range []string{"/stage", "/stage/"} {
		if got := resource.Normalize(in); got.Stage != "stage" || got.Path != "/" {
			t.Errorf("Normalize(%q) = %+v, want stage %q and path /", in, got, "stage")
		}
	}
	for _, in := range []string{"/", ""} {
		if got := resource.Normalize(in); got.Stage != "" {
			t.Errorf("Normalize(%q) = %+v, want no stage", in, got)
		}
	}
}

func res(id, path string, ms ...string) resource.Resource {
	return resource.Resource{ID: id, Path: path, Methods: ms}
}

type findCase struct {
	method, path string
	want         string // resource path, "" for no match
	wantMethod   string
	params       map[string]string
}

// checkFind runs the cases through Find and through the reference
// enumerator below, so the property's reference is held to the same
// vectors.
func checkFind(t *testing.T, what string, rs []resource.Resource, cases []findCase) {
	t.Helper()
	checkFinder(t, what, resource.Find, rs, cases)
	checkFinder(t, what+" (reference enumerator)", reference, rs, cases)
}

func checkFinder(t *testing.T, what string, find func([]resource.Resource, string, string, resource.Methods) (resource.Match, bool), rs []resource.Resource, cases []findCase) {
	t.Helper()
	for _, c := range cases {
		got, ok := find(rs, c.method, c.path, methods())
		if c.want == "" {
			if ok {
				t.Errorf("%s: Find(%s %s) = %+v, want no match", what, c.method, c.path, got)
			}
			continue
		}
		if !ok || got.ResourcePath != c.want || got.Method != c.wantMethod || !maps.Equal(got.PathParameters, c.params) || (c.params == nil) != (got.PathParameters == nil) {
			t.Errorf("%s: Find(%s %s) = %+v, %v\nwant %s method %s params %v", what, c.method, c.path, got, ok, c.want, c.wantMethod, c.params)
			continue
		}
		if i := slices.IndexFunc(rs, func(r resource.Resource) bool { return r.Path == c.want }); i < 0 || got.ResourceID != rs[i].ID {
			t.Errorf("%s: Find(%s %s) resource id %q, want the id of %s", what, c.method, c.path, got.ResourceID, c.want)
		}
	}
}

// The documented precedence table: "a method request against a specific
// resource takes precedence over a method request against a generic
// resource at the same level" (apigateway-rest.md 4.3, aws-doc
// https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-method-settings-method-request.html).
func TestFindDocumentedTable(t *testing.T) {
	rs := []resource.Resource{res("r0", "/"), res("r1", "/{proxy+}", "ANY"), res("r2", "/pets"), res("r3", "/pets/{proxy+}", "GET"), res("r4", "/pets/dog", "GET")}
	checkFind(t, "documented table", rs, []findCase{
		{"GET", "/pets/dog", "/pets/dog", "GET", nil},
		{"GET", "/pets/cats", "/pets/{proxy+}", "GET", map[string]string{"proxy": "cats"}},
		{"GET", "/animals", "/{proxy+}", "ANY", map[string]string{"proxy": "animals"}},
	})
}

// Routing backtracks (apigateway-rest.md finding 4 and 4.3, aws-recorded
// assertions localstack@8b9a79f
// tests/aws/services/apigateway/test_apigateway_common.py:L1599-L1790).
func TestFindBacktracks(t *testing.T) {
	part1 := []resource.Resource{res("r0", "/"), res("r1", "/part1", "GET"), res("r2", "/part1/hardcoded-value", "GET"), res("r3", "/{param+}", "GET")}
	checkFind(t, "part1", part1, []findCase{
		{"GET", "/part1/random-value", "/{param+}", "GET", map[string]string{"param": "part1/random-value"}},
		{"GET", "/part1", "/part1", "GET", nil},
		{"GET", "/part1/hardcoded-value", "/part1/hardcoded-value", "GET", nil},
	})
	anyAPI := []resource.Resource{res("r0", "/"), res("r1", "/test", "POST"), res("r2", "/test/any", "ANY"), res("r3", "/{proxy+}", "OPTIONS")}
	checkFind(t, "test/any", anyAPI, []findCase{
		{"OPTIONS", "/test", "/{proxy+}", "OPTIONS", map[string]string{"proxy": "test"}},
		{"POST", "/test", "/test", "POST", nil},
		{"OPTIONS", "/test/any", "/test/any", "ANY", nil},
		{"GET", "/test/any", "/test/any", "ANY", nil},
	})
	checkFind(t, "existing", []resource.Resource{res("r0", "/"), res("r1", "/existing", "GET")}, []findCase{
		{"POST", "/existing", "", "", nil},
		{"GET", "/existing", "/existing", "GET", nil},
	})
}

// A greedy resource binds the rest of the path with interior empty
// segments kept and a trailing slash dropped; a literal resource binds
// nothing (apigateway-rest.md 4.2 table: pathParameters.proxy
// proxy-value//double-slash, proxy-value for /proxy-value/, null for
// /hardcoded); values stay percent-encoded as sent.
func TestFindBindings(t *testing.T) {
	rs := []resource.Resource{res("r0", "/"), res("r1", "/{proxy+}", "ANY"), res("r2", "/hardcoded", "GET"), res("r3", "/items"), res("r4", "/items/{id}", "GET")}
	checkFind(t, "bindings", rs, []findCase{
		{"GET", "/proxy-value", "/{proxy+}", "ANY", map[string]string{"proxy": "proxy-value"}},
		{"GET", "/proxy-value/", "/{proxy+}", "ANY", map[string]string{"proxy": "proxy-value"}},
		{"GET", "/proxy-value//double-slash", "/{proxy+}", "ANY", map[string]string{"proxy": "proxy-value//double-slash"}},
		{"GET", "/proxy-value/api/user/test%2Balias@gmail.com", "/{proxy+}", "ANY", map[string]string{"proxy": "proxy-value/api/user/test%2Balias@gmail.com"}},
		{"GET", "/hardcoded", "/hardcoded", "GET", nil},
		{"GET", "/items/7", "/items/{id}", "GET", map[string]string{"id": "7"}},
	})
}

// HEAD and ANY follow switch apigateway.invoke.routing (APIGW1-08,
// UNVERIFIED): the test asserts that Find obeys the switch's Methods,
// not a guessed AWS value.
func TestFindFollowsTheRoutingSwitch(t *testing.T) {
	m := methods()
	both := []resource.Resource{res("r0", "/", "GET", "ANY")}
	got, ok := resource.Find(both, "GET", "/", m)
	want := "ANY"
	if m.ExplicitBeforeAny {
		want = "GET"
	}
	if !ok || got.Method != want {
		t.Errorf("GET / on GET and ANY = %+v, %v; want method %s (switch ExplicitBeforeAny %v)", got, ok, want, m.ExplicitBeforeAny)
	}
	getOnly := []resource.Resource{res("r0", "/", "GET")}
	got, ok = resource.Find(getOnly, "HEAD", "/", m)
	if ok != m.HeadMatchesGet || (ok && got.Method != http.MethodGet) {
		t.Errorf("HEAD / on GET only = %+v, %v; want a match by GET exactly when switch HeadMatchesGet (%v)", got, ok, m.HeadMatchesGet)
	}
	headAny := []resource.Resource{res("r0", "/", "ANY")}
	if got, ok := resource.Find(headAny, "HEAD", "/", m); !ok || got.Method != "ANY" {
		t.Errorf("HEAD / on ANY = %+v, %v; want ANY (ANY covers HEAD, apigateway-rest.md 4.4)", got, ok)
	}
}

// The root resource serves the stage root (apigateway-rest.md 4.1: the
// first segment is the stage, the rest the resource path). Whether a root
// greedy resource also serves it is UNVERIFIED (APIGW1-07) and not
// asserted.
func TestRootServesTheStageRoot(t *testing.T) {
	rs := []resource.Resource{res("r0", "/", "GET"), res("r1", "/{proxy+}", "ANY")}
	checkFind(t, "root", rs, []findCase{{"GET", "/", "/", "GET", nil}})
}

// The reference enumerator below is the property's oracle
// (apigateway-rest.md 12.6.2).

type kind int

const (
	literal kind = iota
	variable
	greedy
)

type seg struct {
	k    kind
	text string // literal value or parameter name
}

func templateSegs(path string) []seg {
	if path == "/" {
		return nil
	}
	var out []seg
	for p := range strings.SplitSeq(path[1:], "/") {
		switch {
		case strings.HasPrefix(p, "{") && strings.HasSuffix(p, "+}"):
			out = append(out, seg{greedy, p[1 : len(p)-2]})
		case strings.HasPrefix(p, "{"):
			out = append(out, seg{variable, p[1 : len(p)-1]})
		default:
			out = append(out, seg{literal, p})
		}
	}
	return out
}

func requestSegs(path string) []string {
	if path == "/" {
		return nil
	}
	ss := strings.Split(path[1:], "/")
	if ss[len(ss)-1] == "" {
		ss = ss[:len(ss)-1]
	}
	return ss
}

// bind matches a template against request segments and returns the
// bindings.
func bind(t []seg, rs []string) (map[string]string, bool) {
	params := map[string]string{}
	for i, s := range t {
		switch s.k {
		case literal:
			if i >= len(rs) || rs[i] != s.text {
				return nil, false
			}
		case variable:
			if i >= len(rs) || rs[i] == "" {
				return nil, false
			}
			params[s.text] = rs[i]
		case greedy:
			rest := rs[min(i, len(rs)):]
			if !slices.ContainsFunc(rest, func(x string) bool { return x != "" }) {
				return nil, false
			}
			params[s.text] = strings.Join(rest, "/")
			return params, true
		}
	}
	if len(t) != len(rs) {
		return nil, false
	}
	if len(params) == 0 {
		return nil, true
	}
	return params, true
}

// precedes orders two templates segment by segment from the left: at the
// first position where their kinds differ, literal < variable < greedy.
func precedes(a, b []seg) bool {
	for i := range min(len(a), len(b)) {
		if a[i].k != b[i].k {
			return a[i].k < b[i].k
		}
	}
	return false
}

// serving is the method resource of r that serves verb under m.
func serving(r resource.Resource, verb string, m resource.Methods) (string, bool) {
	has := func(x string) bool { return slices.Contains(r.Methods, x) }
	if !m.ExplicitBeforeAny && has("ANY") {
		return "ANY", true
	}
	if has(verb) {
		return verb, true
	}
	if has("ANY") {
		return "ANY", true
	}
	if verb == "HEAD" && m.HeadMatchesGet && has("GET") {
		return "GET", true
	}
	return "", false
}

func reference(rs []resource.Resource, verb, path string, m resource.Methods) (resource.Match, bool) {
	type cand struct {
		r      resource.Resource
		t      []seg
		params map[string]string
	}
	req := requestSegs(path)
	var cs []cand
	for _, r := range rs {
		t := templateSegs(r.Path)
		if p, ok := bind(t, req); ok {
			cs = append(cs, cand{r, t, p})
		}
	}
	slices.SortStableFunc(cs, func(a, b cand) int {
		switch {
		case precedes(a.t, b.t):
			return -1
		case precedes(b.t, a.t):
			return 1
		}
		return 0
	})
	for _, c := range cs {
		if method, ok := serving(c.r, verb, m); ok {
			return resource.Match{ResourceID: c.r.ID, ResourcePath: c.r.Path, Method: method, PathParameters: c.params}, true
		}
	}
	return resource.Match{}, false
}

// tree draws a valid resource tree: distinct literal siblings, at most one
// variable child per resource, no child under a greedy one
// (apigateway-rest.md 2.4 texts and 3.2), each with a random method set.
func tree(t *rapid.T) []resource.Resource {
	verbs := []string{"GET", "POST", "HEAD", "OPTIONS", "ANY"}
	n := 0
	var out []resource.Resource
	var grow func(path string, depth int)
	add := func(path string) {
		ms := rapid.SliceOfNDistinct(rapid.SampledFrom(verbs), 0, 3, rapid.ID[string]).Draw(t, "methods "+path)
		out = append(out, resource.Resource{ID: fmt.Sprintf("r%d", n), Path: path, Methods: ms})
		n++
	}
	join := func(parent, part string) string {
		if parent == "/" {
			return "/" + part
		}
		return parent + "/" + part
	}
	grow = func(path string, depth int) {
		add(path)
		if depth == 3 || strings.HasSuffix(path, "+}") {
			return
		}
		for _, lit := range rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"a", "b", "c"}), 0, 2, rapid.ID[string]).Draw(t, "literals "+path) {
			grow(join(path, lit), depth+1)
		}
		switch rapid.IntRange(0, 2).Draw(t, "variable "+path) {
		case 1:
			grow(join(path, fmt.Sprintf("{v%d}", depth)), depth+1)
		case 2:
			grow(join(path, fmt.Sprintf("{g%d+}", depth)), depth+1)
		}
	}
	grow("/", 0)
	return out
}

// requestPath draws a request path of one to four segments. The stage
// root and a trailing slash on a literal or variable resource are
// inferred (APIGW1-07), so the property leaves them out; the recorded
// greedy trailing-slash row is a vector above.
func requestPath(t *rapid.T) string {
	segs := rapid.SliceOfN(rapid.SampledFrom([]string{"a", "b", "c", "x"}), 1, 4).Draw(t, "segments")
	return "/" + strings.Join(segs, "/")
}

var verbs = []string{"DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"}

func sameMatch(a resource.Match, aok bool, b resource.Match, bok bool) bool {
	if aok != bok {
		return false
	}
	if !aok {
		return true
	}
	return a.ResourceID == b.ResourceID && a.ResourcePath == b.ResourcePath && a.Method == b.Method &&
		maps.Equal(a.PathParameters, b.PathParameters) && (a.PathParameters == nil) == (b.PathParameters == nil)
}

// Find equals the reference enumerator: every resource whose template
// matches, ordered by precedence, the first with a serving method
// (apigateway-rest.md 4.3, 4.4, 12.6.2); it is deterministic and returns a
// resource of rs.
func TestFindEqualsTheReference(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rs := tree(t)
		verb := rapid.SampledFrom(verbs).Draw(t, "verb")
		path := requestPath(t)
		got, ok := resource.Find(rs, verb, path, methods())
		want, wok := reference(rs, verb, path, methods())
		if !sameMatch(got, ok, want, wok) {
			t.Fatalf("Find(%s %s) = %+v, %v\nreference %+v, %v\nresources %+v", verb, path, got, ok, want, wok, rs)
		}
		again, aok := resource.Find(rs, verb, path, methods())
		if !sameMatch(got, ok, again, aok) {
			t.Fatalf("Find(%s %s) answered %+v, then %+v", verb, path, got, again)
		}
		if ok && !slices.ContainsFunc(rs, func(r resource.Resource) bool { return r.ID == got.ResourceID && r.Path == got.ResourcePath }) {
			t.Fatalf("Find(%s %s) = %+v, not a resource of %+v", verb, path, got, rs)
		}
	})
}

// Adding a resource whose template does not match a request never changes
// that request's match (apigateway-rest.md 12.6.2).
func TestFindIgnoresNonMatchingResources(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rs := tree(t)
		verb := rapid.SampledFrom(verbs).Draw(t, "verb")
		path := requestPath(t)
		extraSegs := rapid.SliceOfN(rapid.SampledFrom([]string{"a", "b", "x", "z", "{w}", "{q+}"}), 1, 4).Draw(t, "extra")
		extra := resource.Resource{ID: "extra", Path: "/" + strings.Join(extraSegs, "/"), Methods: []string{"ANY", verb}}
		if _, matches := bind(templateSegs(extra.Path), requestSegs(path)); matches {
			return // the property is about templates that do not match
		}
		before, bok := resource.Find(rs, verb, path, methods())
		after, aok := resource.Find(append(slices.Clone(rs), extra), verb, path, methods())
		if !sameMatch(before, bok, after, aok) {
			t.Fatalf("adding %s changed Find(%s %s) from %+v, %v to %+v, %v", extra.Path, verb, path, before, bok, after, aok)
		}
	})
}
