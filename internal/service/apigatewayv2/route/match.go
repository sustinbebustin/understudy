package route

import (
	"net/url"
	"strings"
)

// Matching is how Match decides what AWS leaves unrecorded
// (apigateway-http.md 4.4, APIGW2-05): the apigatewayv2.route.matching
// switch's value.
type Matching struct {
	// CaseSensitive compares literal segments byte for byte; false folds
	// case.
	CaseSensitive bool
	// TrailingSlashDistinct keeps /a and /a/ apart; false matches both
	// against either.
	TrailingSlashDistinct bool
	// HeadMatchesGet lets a HEAD request match GET routes; false matches
	// it with HEAD and ANY routes only.
	HeadMatchesGet bool
}

// Route is one route of a Table.
type Route struct {
	ID  string
	Key Key
}

// Table is the routes a stage's deployment serves.
type Table []Route

// Match is the route a request selects and the path variables it binds,
// unescaped; PathParameters is nil when the route has none.
type Match struct {
	RouteID        string
	PathParameters map[string]string
}

// Match selects the route of t a request with method and escapedPath
// matches, by the precedence of apigateway-http.md 4.3: a keyed route
// before $default, then the request's own method before GET for a HEAD
// request (when m allows it) before ANY, then the earlier route of t.
// escapedPath is the path after stage selection, escaped as sent; each
// segment is compared unescaped, so %2F stays inside one segment, and a
// path with a segment that does not unescape matches nothing.
//
// Not implemented yet: a key with a path variable, {name} or {name+},
// never matches; the greedy tier and 4.3's literal-before-variable order
// come with variables. The service refuses such keys until then.
func (t Table) Match(method, escapedPath string, m Matching) (Match, bool) {
	segs, ok := requestSegments(escapedPath)
	if !ok {
		return Match{}, false
	}
	best, bestRank := -1, rank{}
	for i, r := range t {
		if rk, ok := r.rank(method, segs, m); ok && (best < 0 || rk.before(bestRank)) {
			best, bestRank = i, rk
		}
	}
	if best < 0 {
		return Match{}, false
	}
	return Match{RouteID: t[best].ID}, true
}

// The tiers of apigateway-http.md 4.3 that Match serves, most specific
// first.
const (
	tierFull = iota
	tierDefault
)

// The method ranks: the request's own method, GET for a HEAD request, ANY.
const (
	methodExact = iota
	methodHeadAsGet
	methodAny
)

// rank orders the routes that match one request.
type rank struct{ tier, method int }

func (a rank) before(b rank) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	return a.method < b.method
}

// rank reports whether r matches the request and how specifically.
func (r Route) rank(method string, segs []string, m Matching) (rank, bool) {
	switch k := r.Key.(type) {
	case Default:
		return rank{tier: tierDefault}, true
	case Keyed:
		mr, ok := methodRank(k.Method, method, m)
		if !ok || !literalsMatch(k.Segments, segs, m) {
			return rank{}, false
		}
		return rank{tier: tierFull, method: mr}, true
	}
	return rank{}, false
}

// methodRank reports whether a key's method matches a request's.
func methodRank(key Method, method string, m Matching) (int, bool) {
	switch {
	case string(key) == method:
		return methodExact, true
	case key == MethodGet && method == string(MethodHead) && m.HeadMatchesGet:
		return methodHeadAsGet, true
	case key == MethodAny:
		return methodAny, true
	}
	return 0, false
}

// literalsMatch reports whether a key of literal segments matches the
// request's unescaped segments.
func literalsMatch(key []Segment, segs []string, m Matching) bool {
	if !m.TrailingSlashDistinct {
		key = dropTrailingEmpty(key, func(s Segment) bool { return s == Literal{} })
		segs = dropTrailingEmpty(segs, func(s string) bool { return s == "" })
	}
	if len(key) != len(segs) {
		return false
	}
	for i, s := range key {
		lit, ok := s.(Literal)
		if !ok {
			return false
		}
		if m.CaseSensitive && lit.Value != segs[i] || !m.CaseSensitive && !strings.EqualFold(lit.Value, segs[i]) {
			return false
		}
	}
	return true
}

// dropTrailingEmpty drops one last element that is empty.
func dropTrailingEmpty[T any](s []T, empty func(T) bool) []T {
	if n := len(s); n > 0 && empty(s[n-1]) {
		return s[:n-1]
	}
	return s
}

// requestSegments splits an escaped path after its leading slash and
// unescapes each segment.
func requestSegments(escapedPath string) ([]string, bool) {
	rest, ok := strings.CutPrefix(escapedPath, "/")
	if !ok {
		return nil, false
	}
	parts := strings.Split(rest, "/")
	for i, p := range parts {
		u, err := url.PathUnescape(p)
		if err != nil {
			return nil, false
		}
		parts[i] = u
	}
	return parts, true
}
