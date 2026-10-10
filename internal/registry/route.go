package registry

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// HTTPBinding is an operation's http trait: method and URI pattern.
type HTTPBinding struct {
	Method string
	Path   []Segment
	// Query are the literal query pieces of the URI: matching conditions,
	// not bindings.
	Query []QueryMatch
}

// Segment is one path segment of a URI pattern.
//
//sumtype:decl
type Segment interface{ isSegment() }

// Literal matches a segment equal to Value after percent-decoding.
type Literal struct{ Value string }

// Label matches one non-empty segment and binds it to the member Name.
type Label struct{ Name string }

// Greedy matches one or more segments, slashes included, and binds them to
// the member Name. A pattern has at most one, and only literals follow it.
type Greedy struct{ Name string }

func (Literal) isSegment() {}
func (Label) isSegment()   {}
func (Greedy) isSegment()  {}

// QueryMatch is a query condition a request must meet.
//
//sumtype:decl
type QueryMatch interface{ isQueryMatch() }

// QueryPresent requires Key with any value, empty included: ?uploads and
// ?uploads= are the same.
type QueryPresent struct{ Key string }

// QueryEquals requires Key with the value Value: ?list-type=2.
type QueryEquals struct{ Key, Value string }

func (QueryPresent) isQueryMatch() {}
func (QueryEquals) isQueryMatch()  {}

// PatternError reports a URI pattern that breaks the http trait's rules.
type PatternError struct {
	URI    string
	Reason string
}

func (e *PatternError) Error() string {
	return fmt.Sprintf("URI pattern %q is invalid: %s", e.URI, e.Reason)
}

// ParseHTTPBinding parses an http trait's method and uri, such as
// "GET" and "/{Bucket}/{Key+}?uploadId".
func ParseHTTPBinding(method, uri string) (HTTPBinding, error) {
	if method == "" {
		return HTTPBinding{}, &PatternError{URI: uri, Reason: "the method is empty"}
	}
	rawPath, rawQuery, hasQuery := strings.Cut(uri, "?")
	path, err := parsePath(rawPath)
	if err != nil {
		return HTTPBinding{}, &PatternError{URI: uri, Reason: err.Error()}
	}
	b := HTTPBinding{Method: method, Path: path}
	if hasQuery {
		if b.Query, err = parseQuery(rawQuery); err != nil {
			return HTTPBinding{}, &PatternError{URI: uri, Reason: err.Error()}
		}
	}
	return b, nil
}

func parsePath(raw string) ([]Segment, error) {
	if !strings.HasPrefix(raw, "/") {
		return nil, errors.New("it does not start with /")
	}
	if raw == "/" {
		return nil, nil
	}
	var path []Segment
	for s := range strings.SplitSeq(raw[1:], "/") {
		seg, err := parseSegment(s)
		if err != nil {
			return nil, err
		}
		path = append(path, seg)
	}
	return path, validatePath(path)
}

func parseQuery(raw string) ([]QueryMatch, error) {
	if raw == "" {
		return nil, errors.New("it ends with ?")
	}
	var conds []QueryMatch
	for piece := range strings.SplitSeq(raw, "&") {
		key, value, hasValue := strings.Cut(piece, "=")
		if hasValue {
			conds = append(conds, QueryEquals{Key: key, Value: value})
		} else {
			conds = append(conds, QueryPresent{Key: key})
		}
	}
	return conds, validateQuery(conds)
}

func parseSegment(s string) (Segment, error) {
	if s == "" {
		return nil, errors.New("it has an empty segment")
	}
	if inner, ok := strings.CutPrefix(s, "{"); ok {
		name, ok := strings.CutSuffix(inner, "}")
		if !ok || name == "" || strings.ContainsAny(name, "{}") {
			return nil, fmt.Errorf("segment %q is not a whole label", s)
		}
		if name, greedy := strings.CutSuffix(name, "+"); greedy {
			return Greedy{Name: name}, nil
		}
		return Label{Name: name}, nil
	}
	if strings.ContainsAny(s, "{}") {
		return nil, fmt.Errorf("segment %q mixes a label with literal text", s)
	}
	// Match compares a literal with the decoded request segment, so the
	// literal is stored decoded too.
	v, err := url.PathUnescape(s)
	if err != nil {
		return nil, fmt.Errorf("segment %q has an invalid percent-escape", s)
	}
	return Literal{Value: v}, nil
}

// validatePath enforces the http trait's rules that Match and the REST
// binders rely on (wire-rest.md 1.1): no nil segments, no empty or
// dot-segment literals, no literal holding a slash (Match splits the path
// before decoding, so no segment can equal it), no empty or repeated label
// names, at most one greedy label, followed by literals only. Generated
// descriptors are struct literals, so registry.New runs it too.
func validatePath(path []Segment) error {
	greedy := false
	labels := map[string]bool{}
	bind := func(name string) error {
		switch {
		case name == "":
			return errors.New("a label has an empty name")
		case labels[name]:
			return fmt.Errorf("label %q appears more than once", name)
		}
		labels[name] = true
		return nil
	}
	for i, s := range path {
		switch s := s.(type) {
		case nil:
			return fmt.Errorf("segment %d is nil", i)
		case Greedy:
			if greedy {
				return errors.New("it has more than one greedy label")
			}
			greedy = true
			if err := bind(s.Name); err != nil {
				return err
			}
		case Label:
			if greedy {
				return errors.New("a label follows the greedy label")
			}
			if err := bind(s.Name); err != nil {
				return err
			}
		case Literal:
			if err := validateLiteral(s.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateLiteral(v string) error {
	switch {
	case v == "":
		return errors.New("it has an empty segment")
	case v == "." || v == "..":
		return fmt.Errorf("it has the dot segment %q", v)
	case strings.Contains(v, "/"):
		return fmt.Errorf("literal segment %q holds a slash, which no request segment can match", v)
	}
	return nil
}

// validateQuery rejects query conditions with an empty key and keys that
// appear more than once: compareSpecificity counts conditions, so a repeated
// key would rank a row above one with more distinct conditions.
func validateQuery(conds []QueryMatch) error {
	keys := make(map[string]bool, len(conds))
	for _, c := range conds {
		var key string
		switch c := c.(type) {
		case QueryPresent:
			key = c.Key
		case QueryEquals:
			key = c.Key
		}
		switch {
		case key == "":
			return errors.New("a query literal has an empty key")
		case keys[key]:
			return fmt.Errorf("query key %q appears more than once", key)
		}
		keys[key] = true
	}
	return nil
}

// Route is one row of a REST dispatch table.
type Route struct {
	HTTPBinding
	// Headers are header names the request must carry (S3 CopyObject's
	// x-amz-copy-source).
	Headers   []string
	Operation *Operation
}

// validateHeaders rejects an empty required header name and a name required
// twice, ignoring case: compareSpecificity counts required headers, so a
// repeat would rank a row above one with more distinct headers.
func validateHeaders(names []string) error {
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		c := http.CanonicalHeaderKey(n)
		switch {
		case n == "":
			return errors.New("a required header has an empty name")
		case seen[c]:
			return fmt.Errorf("header %q is required twice; header names are case-insensitive", c)
		}
		seen[c] = true
	}
	return nil
}

// signature is equal for two routes exactly when Match cannot tell them
// apart: same method, a literal of the same value or a label of the same kind
// at each position (label names aside), the same set of query conditions and
// the same set of canonical header names. Every piece is quoted, so the
// separators never collide with the content.
func (r *Route) signature() string {
	var b strings.Builder
	b.WriteString(strconv.Quote(r.Method))
	b.WriteString(" path")
	for _, s := range r.Path {
		switch s := s.(type) {
		case Literal:
			b.WriteString(" " + strconv.Quote(s.Value))
		case Label:
			b.WriteString(" {}")
		case Greedy:
			b.WriteString(" {+}")
		}
	}
	query := make([]string, 0, len(r.Query))
	for _, c := range r.Query {
		switch c := c.(type) {
		case QueryPresent:
			query = append(query, strconv.Quote(c.Key))
		case QueryEquals:
			query = append(query, strconv.Quote(c.Key)+"="+strconv.Quote(c.Value))
		}
	}
	headers := make([]string, 0, len(r.Headers))
	for _, h := range r.Headers {
		headers = append(headers, strconv.Quote(http.CanonicalHeaderKey(h)))
	}
	// registry.New rejects repeated query keys and header names first, so
	// sorting alone turns each list into a set.
	slices.Sort(query)
	slices.Sort(headers)
	b.WriteString(" query " + strings.Join(query, " "))
	b.WriteString(" headers " + strings.Join(headers, " "))
	return b.String()
}

// RouteTable is a REST service's dispatch table: the model's http traits plus
// the codegen/services route overrides. registry.New validates it and
// accepts modeled operations only: an internal operation is never claimed by
// the edge, and a raw one is claimed by the edge's prefix and host rules,
// which live outside the table (ADR 0003).
type RouteTable struct {
	Routes []*Route
}

// Match returns the most specific route that accepts the request, by Smithy
// specificity routing (smithy-models.md 9 item 7, wire-rest.md 1.1): at the
// first differing segment a literal beats a label and a label beats a greedy
// label; then more segments win; then more query conditions; then more
// required headers. Equally specific matches resolve to the earlier row.
//
// path is the escaped request path (URL.EscapedPath): it is split before
// segments are decoded, so %2F stays inside its segment. A trailing slash is
// ignored except by a greedy label that ends the pattern, where it is part of
// the value (the S3 key "dir/").
func (t *RouteTable) Match(method, path string, query url.Values, header http.Header) (*Route, bool) {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var best *Route
	for _, r := range t.Routes {
		if r.Method != method || !matchPath(r.Path, segs) || !matchQuery(r.Query, query) || !matchHeaders(r.Headers, header) {
			continue
		}
		if best == nil || compareSpecificity(r, best) > 0 {
			best = r
		}
	}
	return best, best != nil
}

func matchPath(pattern []Segment, segs []string) bool {
	greedyAt := -1
	for i, s := range pattern {
		if _, ok := s.(Greedy); ok {
			greedyAt = i
		}
	}
	if greedyAt < 0 {
		if segs[len(segs)-1] == "" {
			segs = segs[:len(segs)-1]
		}
		if len(segs) != len(pattern) {
			return false
		}
		return matchEach(pattern, segs)
	}
	after := len(pattern) - greedyAt - 1
	// The greedy label owns a trailing slash only when it ends the pattern;
	// before trailing literals the slash belongs to no label.
	if after > 0 && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	if len(segs) < greedyAt+1+after {
		return false
	}
	return matchEach(pattern[:greedyAt], segs[:greedyAt]) &&
		matchEach(pattern[greedyAt+1:], segs[len(segs)-after:]) &&
		strings.Join(segs[greedyAt:len(segs)-after], "/") != ""
}

// matchEach matches literal and label segments pairwise. A greedy or nil
// segment matches nothing here.
func matchEach(pattern []Segment, segs []string) bool {
	for i, p := range pattern {
		switch p := p.(type) {
		case Literal:
			v, err := url.PathUnescape(segs[i])
			if err != nil || v != p.Value {
				return false
			}
		case Label:
			if segs[i] == "" {
				return false
			}
		case Greedy, nil:
			return false
		}
	}
	return true
}

func matchQuery(conds []QueryMatch, query url.Values) bool {
	for _, c := range conds {
		switch c := c.(type) {
		case QueryPresent:
			if !query.Has(c.Key) {
				return false
			}
		case QueryEquals:
			if !slices.Contains(query[c.Key], c.Value) {
				return false
			}
		}
	}
	return true
}

func matchHeaders(names []string, header http.Header) bool {
	for _, n := range names {
		if len(header.Values(n)) == 0 {
			return false
		}
	}
	return true
}

// compareSpecificity returns a positive number when a is more specific than
// b, negative when less, and 0 when equal.
func compareSpecificity(a, b *Route) int {
	for i := range min(len(a.Path), len(b.Path)) {
		if c := cmp.Compare(segmentRank(a.Path[i]), segmentRank(b.Path[i])); c != 0 {
			return c
		}
	}
	if c := cmp.Compare(len(a.Path), len(b.Path)); c != 0 {
		return c
	}
	if c := cmp.Compare(len(a.Query), len(b.Query)); c != 0 {
		return c
	}
	return cmp.Compare(len(a.Headers), len(b.Headers))
}

func segmentRank(s Segment) int {
	switch s.(type) {
	case Literal:
		return 2
	case Label:
		return 1
	case Greedy:
		return 0
	default:
		return 0
	}
}
