package route_test

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2/route"
)

func reversed(tbl route.Table) route.Table {
	r := slices.Clone(tbl)
	slices.Reverse(r)
	return r
}

// Inside apigateway-http.md 4.3's tiers the order is inferred: the guide
// leaves path specificity against method specificity, two greedy routes
// and literal against variable segments open (APIGW2-05). These vectors
// pin the choices Table.Match documents, in both table orders, so that
// recording APIGW2-05 lands here.
func TestMatchOrdersInsideATierAsInferred(t *testing.T) {
	for _, c := range []struct {
		what   string
		tbl    route.Table
		path   string
		id     string
		params map[string]string
	}{
		{"a full match on ANY before a greedy match on the method", route.Table{
			{ID: "greedy", Key: keyed(route.MethodGet, lit("pets"), greedy("proxy"))},
			{ID: "any", Key: keyed(route.MethodAny, lit("pets"), lit("bird"), lit("1"))},
		}, "/pets/bird/1", "any", nil},
		{"the greedy route with more segments before its variable", route.Table{
			{ID: "root", Key: keyed(route.MethodGet, greedy("proxy"))},
			{ID: "pets", Key: keyed(route.MethodGet, lit("pets"), greedy("proxy"))},
		}, "/pets/a/b", "pets", map[string]string{"proxy": "a/b"}},
		{"a literal before a variable where the keys first differ", route.Table{
			{ID: "var-first", Key: keyed(route.MethodGet, pvar("a"), lit("b"))},
			{ID: "literal-first", Key: keyed(route.MethodGet, lit("a"), pvar("b"))},
		}, "/a/b", "literal-first", map[string]string{"b": "b"}},
		{"a literal before a variable among greedy routes", route.Table{
			{ID: "var", Key: keyed(route.MethodGet, pvar("x"), greedy("proxy"))},
			{ID: "literal", Key: keyed(route.MethodGet, lit("a"), greedy("proxy"))},
		}, "/a/b", "literal", map[string]string{"proxy": "b"}},
		{"the path before the method", route.Table{
			{ID: "get", Key: keyed(route.MethodGet, pvar("a"), lit("b"))},
			{ID: "any", Key: keyed(route.MethodAny, lit("a"), lit("b"))},
		}, "/a/b", "any", nil},
	} {
		for _, tbl := range []route.Table{c.tbl, reversed(c.tbl)} {
			got, ok := tbl.Match(http.MethodGet, c.path, matching())
			if !ok || got.RouteID != c.id || !maps.Equal(got.PathParameters, c.params) {
				t.Errorf("%s: GET %s in %v = %+v, %v; want %s %v", c.what, c.path, tbl, got, ok, c.id, c.params)
			}
		}
	}
}

// With HeadMatchesGet, a HEAD request takes a HEAD route first, then a GET
// route, then ANY (inferred, APIGW2-05).
func TestMatchRanksGetForHeadBetweenHeadAndAny(t *testing.T) {
	m := route.Matching{CaseSensitive: true, TrailingSlashDistinct: true, HeadMatchesGet: true}
	get := route.Route{ID: "get", Key: keyed(route.MethodGet, lit("x"))}
	anyR := route.Route{ID: "any", Key: keyed(route.MethodAny, lit("x"))}
	head := route.Route{ID: "head", Key: keyed(route.MethodHead, lit("x"))}
	for _, c := range []struct {
		tbl route.Table
		id  string
	}{{route.Table{get, anyR}, "get"}, {route.Table{get, anyR, head}, "head"}} {
		for _, tbl := range []route.Table{c.tbl, reversed(c.tbl)} {
			if got, ok := tbl.Match(http.MethodHead, "/x", m); !ok || got.RouteID != c.id {
				t.Errorf("HEAD /x in %v = %+v, %v; want %s", tbl, got, ok, c.id)
			}
		}
	}
}

// A greedy variable binds the rest of the path only when it is non-empty,
// so /{proxy+} does not match / and /pets/{proxy+} does not match /pets/
// (inferred, apigateway-http.md 4.3, APIGW2-05).
func TestGreedyVariableNeedsANonEmptyRest(t *testing.T) {
	root := route.Table{{ID: "root", Key: keyed(route.MethodGet, greedy("proxy"))}}
	pets := route.Table{{ID: "pets", Key: keyed(route.MethodGet, lit("pets"), greedy("proxy"))}}
	for _, c := range []struct {
		tbl  route.Table
		path string
	}{{root, "/"}, {pets, "/pets"}, {pets, "/pets/"}} {
		if got, ok := c.tbl.Match(http.MethodGet, c.path, matching()); ok {
			t.Errorf("GET %s matched %+v in %v, want nothing", c.path, got, c.tbl)
		}
	}
	wantMatch(t, root, http.MethodGet, "/a", "root", map[string]string{"proxy": "a"})
	wantMatch(t, append(slices.Clone(pets), root...), http.MethodGet, "/pets/", "root", map[string]string{"proxy": "pets/"})
}

// Folding trailing slashes drops one empty last segment from the request
// before a greedy variable binds the rest too.
func TestGreedyVariableFollowsTheTrailingSlashParameter(t *testing.T) {
	pets := route.Table{{ID: "pets", Key: keyed(route.MethodGet, lit("pets"), greedy("proxy"))}}
	for distinct, want := range map[bool]string{true: "a/", false: "a"} {
		got, ok := pets.Match(http.MethodGet, "/pets/a/", route.Matching{CaseSensitive: true, TrailingSlashDistinct: distinct})
		if !ok || got.PathParameters["proxy"] != want {
			t.Errorf("TrailingSlashDistinct %v: GET /pets/a/ = %+v, %v; want proxy %q", distinct, got, ok, want)
		}
	}
}

// Each segment is compared and bound unescaped ("API Gateway decodes
// URL-encoded request parameters", apigateway-http.md 4.4), split before
// it is unescaped, so %2F stays inside one segment (how AWS matches %2F
// is UNVERIFIED, APIGW2-05); a path that does not unescape, or does not
// start with /, matches nothing, $default included.
func TestMatchUnescapesEachSegment(t *testing.T) {
	wantMatch(t, route.Table{{ID: "x", Key: keyed(route.MethodGet, lit("a"), pvar("x"))}}, http.MethodGet, "/a/b%2Fc", "x", map[string]string{"x": "b/c"})
	wantMatch(t, route.Table{{ID: "space", Key: keyed(route.MethodGet, lit("a b"))}}, http.MethodGet, "/a%20b", "space", nil)
	wantMatch(t, route.Table{{ID: "files", Key: keyed(route.MethodGet, lit("files"), greedy("p"))}}, http.MethodGet, "/files/a%20b/c%2Fd", "files", map[string]string{"p": "a b/c/d"})
	for _, c := range []struct {
		tbl  route.Table
		path string
	}{
		{route.Table{{ID: "abc", Key: keyed(route.MethodGet, lit("a"), lit("b"), lit("c"))}}, "/a/b%2Fc"},
		{route.Table{{ID: "default", Key: route.Default{}}}, "/a/%zz"},
		{route.Table{{ID: "default", Key: route.Default{}}}, "a"},
	} {
		if got, ok := c.tbl.Match(http.MethodGet, c.path, matching()); ok {
			t.Errorf("GET %s matched %+v in %v, want nothing", c.path, got, c.tbl)
		}
	}
}

// Tier 1 of apigateway-http.md 4.3 holds every full match, an ANY one
// included (that ANY's place is inferred, APIGW2-05): when any non-greedy
// route matches, the selected route is non-greedy, and when only greedy
// routes and $default match, a greedy one is selected.
func TestMatchPutsEveryFullMatchFirst(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tbl, q := genTable().Draw(t, "table"), genRequest().Draw(t, "request")
		var full, greedyMatch bool
		for _, r := range tbl {
			if !matchesAlone(r, q) || isDefault(r.Key) {
				continue
			}
			if isGreedy(r.Key) {
				greedyMatch = true
			} else {
				full = true
			}
		}
		got, ok := tbl.Match(q.method, q.path, matching())
		if !ok {
			return
		}
		sel, _ := routeByID(tbl, got.RouteID)
		if full && (isGreedy(sel.Key) || isDefault(sel.Key)) {
			t.Fatalf("%s %s selected %+v though a full match exists in %+v", q.method, q.path, sel, tbl)
		}
		if greedyMatch && !full && !isGreedy(sel.Key) {
			t.Fatalf("%s %s selected %+v though a greedy match exists in %+v", q.method, q.path, sel, tbl)
		}
	})
}

// shape is a key with its variable names erased: two routes of one shape
// are equally specific for every request.
func shape(k route.Key) string {
	kk, ok := k.(route.Keyed)
	if !ok {
		return "$default"
	}
	parts := make([]string, len(kk.Segments))
	for i, s := range kk.Segments {
		switch s := s.(type) {
		case route.Literal:
			parts[i] = s.Value
		case route.Var:
			parts[i] = "{}"
		case route.Greedy:
			parts[i] = "{+}"
		}
	}
	return string(kk.Method) + " /" + strings.Join(parts, "/")
}

// The most specific match wins (apigateway-http.md 4.3), so the order of
// the table decides only between equally specific routes: reversing it
// selects a route of the same shape.
func TestMatchDependsOnSpecificityNotPosition(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tbl, q := genTable().Draw(t, "table"), genRequest().Draw(t, "request")
		a, aok := tbl.Match(q.method, q.path, matching())
		b, bok := reversed(tbl).Match(q.method, q.path, matching())
		if aok != bok {
			t.Fatalf("%s %s: matched %v, reversed %v", q.method, q.path, aok, bok)
		}
		if !aok {
			return
		}
		ra, _ := routeByID(tbl, a.RouteID)
		rb, _ := routeByID(tbl, b.RouteID)
		if shape(ra.Key) != shape(rb.Key) {
			t.Fatalf("%s %s selected %s, reversed %s, in %+v", q.method, q.path, shape(ra.Key), shape(rb.Key), tbl)
		}
	})
}

// A route alone matches every request built from its key by escaping
// each literal and each variable's value, and binds each variable to its
// value decoded (apigateway-http.md 4.4); a route without variables binds
// none (nil).
func TestMatchBindsTheDecodedValues(t *testing.T) {
	text := rapid.StringMatching(`[^{}]{1,6}`)
	rapid.Check(t, func(t *rapid.T) {
		method := rapid.SampledFrom([]string{http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut}).Draw(t, "method")
		key := route.Keyed{Method: route.Method(method)}
		if rapid.Bool().Draw(t, "any") {
			key.Method = route.MethodAny
		}
		var path []string
		var want map[string]string
		bind := func(name, value string) {
			if want == nil {
				want = map[string]string{}
			}
			want[name] = value
		}
		for i := range rapid.IntRange(1, 4).Draw(t, "segments") {
			if rapid.Bool().Draw(t, fmt.Sprintf("var %d", i)) {
				name, value := fmt.Sprintf("v%d", i), text.Draw(t, fmt.Sprintf("value %d", i))
				key.Segments = append(key.Segments, pvar(name))
				path = append(path, url.PathEscape(value))
				bind(name, value)
				continue
			}
			value := text.Draw(t, fmt.Sprintf("literal %d", i))
			key.Segments = append(key.Segments, lit(value))
			path = append(path, url.PathEscape(value))
		}
		if rapid.Bool().Draw(t, "greedy") {
			rest := rapid.SliceOfN(text, 1, 3).Draw(t, "rest")
			key.Segments = append(key.Segments, greedy("proxy"))
			for _, p := range rest {
				path = append(path, url.PathEscape(p))
			}
			bind("proxy", strings.Join(rest, "/"))
		}
		escaped := "/" + strings.Join(path, "/")
		got, ok := route.Table{{ID: "r", Key: key}}.Match(method, escaped, matching())
		if !ok || !maps.Equal(got.PathParameters, want) || (want == nil) != (got.PathParameters == nil) {
			t.Fatalf("%s %s against %s %s = %+v, %v; want %v", method, escaped, key.Method, key.Path(), got, ok, want)
		}
	})
}
