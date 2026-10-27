package route

import (
	"net/url"
	"slices"
	"strings"
)

// Matching is how Match decides what AWS leaves unrecorded
// (apigateway-http.md 4.4, APIGW2-05): the apigatewayv2.route.matching
// switch's value.
type Matching struct {
	// CaseSensitive compares literal segments byte for byte; false folds
	// case.
	CaseSensitive bool
	// TrailingSlashDistinct keeps /a and /a/ apart; false drops one
	// trailing empty segment from the request and from each key.
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
// matches. escapedPath is the path after stage selection, escaped as
// sent; each segment is compared unescaped, so %2F stays inside one
// segment, and a path with a segment that does not unescape matches
// nothing.
//
// A {name} variable matches one non-empty segment. A greedy {name+}
// matches the rest of the path when it is non-empty, so /{proxy+} does
// not match / and /pets/{proxy+} does not match /pets/ (inferred,
// APIGW2-05).
//
// The precedence is apigateway-http.md 4.3's tiers: a full match, then a
// match with a greedy variable, then $default. The order inside a tier is
// inferred (4.3 leaves it open, APIGW2-05): among greedy matches the key
// with more segments before its greedy variable first; then, comparing
// segment by segment from the left, a literal before a variable where
// the keys first differ; then the request's own method before GET for a
// HEAD request (when m allows it) before ANY; then the earlier route of
// t. So a full match on ANY beats a greedy match on the request's method.
func (t Table) Match(method, escapedPath string, m Matching) (Match, bool) {
	segs, ok := requestSegments(escapedPath)
	if !ok {
		return Match{}, false
	}
	if !m.TrailingSlashDistinct {
		segs = dropTrailingEmpty(segs, func(s string) bool { return s == "" })
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
	return Match{RouteID: t[best].ID, PathParameters: t[best].bind(segs)}, true
}

// The tiers of apigateway-http.md 4.3, most specific first.
const (
	tierFull = iota
	tierGreedy
	tierDefault
)

// The method ranks: the request's own method, GET for a HEAD request, ANY.
const (
	methodExact = iota
	methodHeadAsGet
	methodAny
)

// The kinds of the segments a key compares before any greedy variable,
// in the order Match prefers them.
const (
	kindLiteral = iota
	kindVar
)

// rank orders the routes that match one request.
type rank struct {
	tier int
	// kinds are the key's segments before any greedy variable, as
	// kindLiteral or kindVar. Two full matches of one request have as many
	// as the request has segments; greedy matches compare their count
	// first.
	kinds  []int
	method int
}

func (a rank) before(b rank) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	if len(a.kinds) != len(b.kinds) {
		return len(a.kinds) > len(b.kinds)
	}
	if c := slices.Compare(a.kinds, b.kinds); c != 0 {
		return c < 0
	}
	return a.method < b.method
}

// rank reports whether r matches the request's segments and how
// specifically.
func (r Route) rank(method string, segs []string, m Matching) (rank, bool) {
	switch k := r.Key.(type) {
	case Default:
		return rank{tier: tierDefault}, true
	case Keyed:
		mr, ok := methodRank(k.Method, method, m)
		if !ok {
			return rank{}, false
		}
		prefix, greedy, ok := k.fit(segs, m)
		if !ok {
			return rank{}, false
		}
		rk := rank{tier: tierFull, kinds: make([]int, len(prefix)), method: mr}
		if greedy {
			rk.tier = tierGreedy
		}
		for i, s := range prefix {
			if _, v := s.(Var); v {
				rk.kinds[i] = kindVar
			}
		}
		return rk, true
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

// fit reports whether the key's path matches the request's segments. It
// returns the key's segments before any greedy variable, with a trailing
// empty one dropped when m folds trailing slashes, and whether the key
// ends in a greedy variable.
func (k Keyed) fit(segs []string, m Matching) (prefix []Segment, greedy, ok bool) {
	if n := len(k.Segments); n > 0 {
		if _, g := k.Segments[n-1].(Greedy); g {
			prefix = k.Segments[:n-1]
			ok = len(segs) > len(prefix) && segmentsMatch(prefix, segs, m) && strings.Join(segs[len(prefix):], "/") != ""
			return prefix, true, ok
		}
	}
	prefix = k.Segments
	if !m.TrailingSlashDistinct {
		prefix = dropTrailingEmpty(prefix, func(s Segment) bool { return s == Literal{} })
	}
	return prefix, false, len(prefix) == len(segs) && segmentsMatch(prefix, segs, m)
}

// segmentsMatch reports whether each key segment, none of them greedy,
// matches the request segment at its position: a literal one equal to it
// (folding case unless m is case-sensitive), a variable one non-empty.
func segmentsMatch(key []Segment, segs []string, m Matching) bool {
	for i, s := range key {
		switch s := s.(type) {
		case Literal:
			if m.CaseSensitive && s.Value != segs[i] || !m.CaseSensitive && !strings.EqualFold(s.Value, segs[i]) {
				return false
			}
		case Var:
			if segs[i] == "" {
				return false
			}
		case Greedy:
			return false
		}
	}
	return true
}

// bind is the path variables of r, a route that matched the request's
// segments: each {name} to its segment, a greedy {name+} to the rest of
// the segments joined by /. A matched key's variables all sit at
// positions the request has (fit), and a trailing empty segment folding
// drops is a literal.
func (r Route) bind(segs []string) map[string]string {
	k, ok := r.Key.(Keyed)
	if !ok {
		return nil
	}
	var params map[string]string
	set := func(name, value string) {
		if params == nil {
			params = map[string]string{}
		}
		params[name] = value
	}
	for i, s := range k.Segments {
		switch s := s.(type) {
		case Literal:
		case Var:
			set(s.Name, segs[i])
		case Greedy:
			set(s.Name, strings.Join(segs[i:], "/"))
		}
	}
	return params
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
