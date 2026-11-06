package route_test

import (
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2/route"
)

// matching is the matcher part of the apigatewayv2.route.matching switch,
// the value the data plane matches with. What it picks is UNVERIFIED
// (apigateway-http.md 4.4, APIGW2-05); tests of those choices read it.
func matching() route.Matching { return apigatewayv2.RouteMatchingSwitch().Value.Matching }

func lit(v string) route.Segment    { return route.Literal{Value: v} }
func pvar(n string) route.Segment   { return route.Var{Name: n} }
func greedy(n string) route.Segment { return route.Greedy{Name: n} }

func keyed(m route.Method, segs ...route.Segment) route.Key {
	return route.Keyed{Method: m, Segments: segs}
}

// petsTable is the HTTP API guide's route selection table, in the order it
// lists them (apigateway-http.md 4.3,
// https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html).
func petsTable() route.Table {
	return route.Table{
		{ID: "dog1", Key: keyed(route.MethodGet, lit("pets"), lit("dog"), lit("1"))},
		{ID: "dogid", Key: keyed(route.MethodGet, lit("pets"), lit("dog"), pvar("id"))},
		{ID: "petsproxy", Key: keyed(route.MethodGet, lit("pets"), greedy("proxy"))},
		{ID: "anyproxy", Key: keyed(route.MethodAny, greedy("proxy"))},
		{ID: "default", Key: route.Default{}},
	}
}

func wantMatch(t *testing.T, tbl route.Table, method, path, id string, params map[string]string) {
	t.Helper()
	got, ok := tbl.Match(method, path, matching())
	if !ok {
		t.Errorf("%s %s matched nothing, want %s", method, path, id)
		return
	}
	if got.RouteID != id || !maps.Equal(got.PathParameters, params) {
		t.Errorf("%s %s = %s %v, want %s %v", method, path, got.RouteID, got.PathParameters, id, params)
	}
}

// The guide's table: static beats variable, a full match beats a greedy
// one, ANY "matches all methods that you haven't defined for a route", and
// a greedy {proxy+} binds the rest of the path under the key proxy
// (apigateway-http.md 4.3, 5.1).
func TestMatchSelectsAsTheGuideTable(t *testing.T) {
	tbl := petsTable()
	wantMatch(t, tbl, http.MethodGet, "/pets/dog/1", "dog1", nil)
	wantMatch(t, tbl, http.MethodGet, "/pets/dog/2", "dogid", map[string]string{"id": "2"})
	wantMatch(t, tbl, http.MethodGet, "/pets/cat/1", "petsproxy", map[string]string{"proxy": "cat/1"})
	wantMatch(t, tbl, http.MethodPost, "/test/5", "anyproxy", map[string]string{"proxy": "test/5"})
}

// The $default route catches what no other route matches, and with no
// route at all nothing matches, which the gateway answers Not Found
// (apigateway-http.md 4.3).
func TestMatchFallsBackToDefault(t *testing.T) {
	var tbl route.Table
	for _, r := range petsTable() {
		if r.ID != "anyproxy" {
			tbl = append(tbl, r)
		}
	}
	wantMatch(t, tbl, http.MethodPost, "/test/5", "default", nil)
	if got, ok := (route.Table{}).Match(http.MethodGet, "/pets", matching()); ok {
		t.Errorf("an empty table matched %+v", got)
	}
	if got, ok := (route.Table{{ID: "get", Key: keyed(route.MethodGet, lit("pets"))}}).Match(http.MethodPost, "/pets", matching()); ok {
		t.Errorf("POST /pets matched %+v against GET /pets alone", got)
	}
}

// A method's own route beats ANY on the same path (apigateway-http.md 4.3:
// ANY matches the methods "you haven't defined for a route"), whichever is
// listed first.
func TestMatchPrefersTheMethodOverAny(t *testing.T) {
	get := route.Route{ID: "get", Key: keyed(route.MethodGet, lit("orders"), pvar("id"))}
	anyR := route.Route{ID: "any", Key: keyed(route.MethodAny, lit("orders"), pvar("id"))}
	for _, tbl := range []route.Table{{get, anyR}, {anyR, get}} {
		wantMatch(t, tbl, http.MethodGet, "/orders/7", "get", map[string]string{"id": "7"})
		wantMatch(t, tbl, http.MethodDelete, "/orders/7", "any", map[string]string{"id": "7"})
	}
}

// "API Gateway decodes URL-encoded request parameters before passing them
// to your backend integration" (apigateway-http.md 4.4): path variables
// bind the decoded segment.
func TestMatchDecodesPathParameters(t *testing.T) {
	tbl := route.Table{{ID: "pet", Key: keyed(route.MethodGet, lit("pets"), pvar("name"))}}
	wantMatch(t, tbl, http.MethodGet, "/pets/rex%20the%20dog", "pet", map[string]string{"name": "rex the dog"})
	wantMatch(t, tbl, http.MethodGet, "/pets/caf%C3%A9", "pet", map[string]string{"name": "café"})
}

// Literal case, trailing slashes and HEAD are matched as the Matching
// parameter says; which of each AWS does is the route.matching switch's
// UNVERIFIED choice (apigateway-http.md 4.4, APIGW2-05), so each side of
// each flag is checked here and the switch's side by the data plane.
func TestMatchFollowsItsParameter(t *testing.T) {
	pets := route.Table{{ID: "pets", Key: keyed(route.MethodGet, lit("pets"))}}
	for _, sensitive := range []bool{true, false} {
		_, ok := pets.Match(http.MethodGet, "/Pets", route.Matching{CaseSensitive: sensitive, TrailingSlashDistinct: true})
		if ok == sensitive {
			t.Errorf("CaseSensitive %v: GET /Pets matched GET /pets = %v", sensitive, ok)
		}
	}
	for _, distinct := range []bool{true, false} {
		_, ok := pets.Match(http.MethodGet, "/pets/", route.Matching{CaseSensitive: true, TrailingSlashDistinct: distinct})
		if ok == distinct {
			t.Errorf("TrailingSlashDistinct %v: GET /pets/ matched GET /pets = %v", distinct, ok)
		}
	}
	for _, headGet := range []bool{true, false} {
		_, ok := pets.Match(http.MethodHead, "/pets", route.Matching{CaseSensitive: true, TrailingSlashDistinct: true, HeadMatchesGet: headGet})
		if ok != headGet {
			t.Errorf("HeadMatchesGet %v: HEAD /pets matched GET /pets = %v", headGet, ok)
		}
	}
	head := route.Table{{ID: "head", Key: keyed(route.MethodHead, lit("pets"))}, {ID: "any", Key: keyed(route.MethodAny, lit("x"))}}
	for _, headGet := range []bool{true, false} {
		m := route.Matching{CaseSensitive: true, TrailingSlashDistinct: true, HeadMatchesGet: headGet}
		if got, ok := head.Match(http.MethodHead, "/pets", m); !ok || got.RouteID != "head" {
			t.Errorf("HeadMatchesGet %v: HEAD /pets = %+v, %v; want the HEAD route", headGet, got, ok)
		}
		if got, ok := head.Match(http.MethodHead, "/x", m); !ok || got.RouteID != "any" {
			t.Errorf("HeadMatchesGet %v: HEAD /x = %+v, %v; want the ANY route", headGet, got, ok)
		}
	}
}

// A route without variables binds none.
func TestMatchBindsNothingWithoutVariables(t *testing.T) {
	got, ok := petsTable().Match(http.MethodGet, "/pets/dog/1", matching())
	if !ok || len(got.PathParameters) != 0 {
		t.Errorf("GET /pets/dog/1 = %+v, %v; want no path parameters", got, ok)
	}
}

// genRoute draws a route over a small path alphabet so routes overlap:
// literal and variable segments, a greedy variable last, or $default.
func genRoute(id string) *rapid.Generator[route.Route] {
	return rapid.Custom(func(t *rapid.T) route.Route {
		if rapid.IntRange(0, 9).Draw(t, "default") == 0 {
			return route.Route{ID: id, Key: route.Default{}}
		}
		k := route.Keyed{Method: rapid.SampledFrom([]route.Method{route.MethodGet, route.MethodPost, route.MethodAny, route.MethodHead}).Draw(t, "method")}
		n := rapid.IntRange(1, 3).Draw(t, "segments")
		for i := range n {
			if rapid.Bool().Draw(t, fmt.Sprintf("var %d", i)) {
				k.Segments = append(k.Segments, pvar(fmt.Sprintf("v%d", i)))
			} else {
				k.Segments = append(k.Segments, lit(rapid.SampledFrom([]string{"a", "b"}).Draw(t, fmt.Sprintf("lit %d", i))))
			}
		}
		if rapid.IntRange(0, 3).Draw(t, "greedy") == 0 {
			k.Segments = append(k.Segments, greedy("proxy"))
		}
		return route.Route{ID: id, Key: k}
	})
}

func genTable() *rapid.Generator[route.Table] {
	return rapid.Custom(func(t *rapid.T) route.Table {
		n := rapid.IntRange(0, 6).Draw(t, "routes")
		tbl := make(route.Table, n)
		for i := range tbl {
			tbl[i] = genRoute(fmt.Sprintf("r%d", i)).Draw(t, fmt.Sprintf("route %d", i))
		}
		return tbl
	})
}

type request struct{ method, path string }

func genRequest() *rapid.Generator[request] {
	return rapid.Custom(func(t *rapid.T) request {
		segs := rapid.SliceOfN(rapid.SampledFrom([]string{"a", "b", "c"}), 0, 4).Draw(t, "segments")
		return request{
			method: rapid.SampledFrom([]string{http.MethodGet, http.MethodPost, http.MethodHead, http.MethodDelete}).Draw(t, "method"),
			path:   "/" + strings.Join(segs, "/"),
		}
	})
}

func isGreedy(k route.Key) bool {
	kk, ok := k.(route.Keyed)
	if !ok || len(kk.Segments) == 0 {
		return false
	}
	_, g := kk.Segments[len(kk.Segments)-1].(route.Greedy)
	return g
}

func isDefault(k route.Key) bool {
	_, ok := k.(route.Default)
	return ok
}

func routeByID(tbl route.Table, id string) (route.Route, bool) {
	for _, r := range tbl {
		if r.ID == id {
			return r, true
		}
	}
	return route.Route{}, false
}

// matchesAlone reports whether r matches the request in a table of its own.
func matchesAlone(r route.Route, q request) bool {
	_, ok := route.Table{r}.Match(q.method, q.path, matching())
	return ok
}

// Match is deterministic and answers a route of its table.
func TestMatchIsDeterministicAndAnswersARouteOfTheTable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tbl, q := genTable().Draw(t, "table"), genRequest().Draw(t, "request")
		a, aok := tbl.Match(q.method, q.path, matching())
		b, bok := tbl.Match(q.method, q.path, matching())
		if aok != bok || a.RouteID != b.RouteID || !maps.Equal(a.PathParameters, b.PathParameters) {
			t.Fatalf("%s %s: %+v %v, then %+v %v", q.method, q.path, a, aok, b, bok)
		}
		if _, in := routeByID(tbl, a.RouteID); aok && !in {
			t.Fatalf("%s %s matched %q, not a route of the table", q.method, q.path, a.RouteID)
		}
	})
}

// Adding a route that does not match a request never changes that
// request's match: routes do not interact beyond precedence.
func TestMatchIgnoresRoutesThatDoNotMatch(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tbl, q := genTable().Draw(t, "table"), genRequest().Draw(t, "request")
		extra := genRoute("extra").Draw(t, "extra")
		if matchesAlone(extra, q) {
			return // only a route that does not match is under test
		}
		before, bok := tbl.Match(q.method, q.path, matching())
		after, aok := append(tbl, extra).Match(q.method, q.path, matching())
		if bok != aok || before.RouteID != after.RouteID || !maps.Equal(before.PathParameters, after.PathParameters) {
			t.Fatalf("%s %s: %+v %v before adding %+v, %+v %v after", q.method, q.path, before, bok, extra, after, aok)
		}
	})
}

// The documented priorities (apigateway-http.md 4.3): "1. Full match for a
// route and method. 2. Match for a route and method with a greedy path
// variable. 3. The $default route." When a non-greedy route of the
// request's own method matches, the selected route is non-greedy; when
// only greedy routes of the request's method and $default match, a greedy
// one is selected; anything matched selects something. Whether an ANY full
// match beats a greedy match on the request's method is UNVERIFIED
// (APIGW2-05), so ANY routes do not trigger these checks.
func TestMatchFollowsTheDocumentedPriorities(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tbl, q := genTable().Draw(t, "table"), genRequest().Draw(t, "request")
		var fullOwn, greedyOwn, anyMatch bool
		for _, r := range tbl {
			if !matchesAlone(r, q) {
				continue
			}
			anyMatch = true
			k, ok := r.Key.(route.Keyed)
			if !ok || string(k.Method) != q.method {
				continue
			}
			if isGreedy(r.Key) {
				greedyOwn = true
			} else {
				fullOwn = true
			}
		}
		got, ok := tbl.Match(q.method, q.path, matching())
		if ok != anyMatch {
			t.Fatalf("%s %s: matched %v, but a route of the table matches alone: %v", q.method, q.path, ok, anyMatch)
		}
		if !ok {
			return
		}
		sel, _ := routeByID(tbl, got.RouteID)
		if fullOwn && (isGreedy(sel.Key) || isDefault(sel.Key)) {
			t.Fatalf("%s %s selected %+v though a full match of the method exists in %+v", q.method, q.path, sel, tbl)
		}
		if greedyOwn && !fullOwn && isDefault(sel.Key) {
			t.Fatalf("%s %s selected $default though a greedy match of the method exists in %+v", q.method, q.path, tbl)
		}
	})
}

// With the switch's TrailingSlashDistinct, /a and /a/ never select the
// same non-greedy keyed route (apigateway-http.md 4.4; switch
// apigatewayv2.route.matching).
func TestTrailingSlashFollowsTheSwitch(t *testing.T) {
	m := matching()
	if !m.TrailingSlashDistinct {
		t.Skip("the switch folds trailing slashes")
	}
	rapid.Check(t, func(t *rapid.T) {
		tbl, q := genTable().Draw(t, "table"), genRequest().Draw(t, "request")
		if q.path == "/" {
			return
		}
		a, aok := tbl.Match(q.method, q.path, m)
		b, bok := tbl.Match(q.method, q.path+"/", m)
		if !aok || !bok || a.RouteID != b.RouteID {
			return
		}
		if r, _ := routeByID(tbl, a.RouteID); !isDefault(r.Key) && !isGreedy(r.Key) {
			t.Fatalf("%s %s and %s/ both selected %+v", q.method, q.path, q.path, r)
		}
	})
}
