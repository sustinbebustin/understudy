package route

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
// matches, by the precedence of apigateway-http.md 4.3. It is not
// implemented yet and matches nothing.
func (Table) Match(_, _ string, _ Matching) (Match, bool) {
	return Match{}, false
}
