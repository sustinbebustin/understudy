package form

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxKeySegments is the most '.'-separated segments Parse accepts in one
// key. Modeled awsQuery keys stay far below it: CloudWatch's
// MetricDataQueries.member.N.MetricStat.Metric.Dimensions.member.M.Value,
// among the deepest, has 9.
const MaxKeySegments = 32

// MaxNodes is the most distinct keys and key prefixes Parse accepts in one
// form. Every node costs a few hundred bytes, so the cap, not the body
// size, bounds what a parse allocates: a body of short distinct keys
// would otherwise cost over a hundred times its size. It is understudy's
// choice, set well above what a form inside AWS's own request limits
// carries.
const MaxNodes = 1 << 18

// Values is a parsed form, held as a tree: each key is split on '.' into
// segments, so Attribute.1.Name is the child Name of the child 1 of the
// child Attribute of the root. The zero Values is an empty form.
type Values struct {
	root  Node
	nodes int
}

// Node is one key prefix of a form: it may carry a value (the key itself
// was sent) and children (longer keys were sent).
type Node struct {
	key      string
	segment  string
	value    string
	hasValue bool
	children map[string]*Node
}

// Pair is one key and value of a form, in the order Encode writes them.
type Pair struct {
	Key   string
	Value string
}

// Problem is why Parse refused a form.
type Problem uint8

// Problem values.
const (
	// BadEscape is a % not followed by two hexadecimal digits.
	BadEscape Problem = iota + 1
	// KeyTooDeep is a key of more than MaxKeySegments segments.
	KeyTooDeep
	// TooManyNodes is a form of more than MaxNodes keys and key prefixes.
	TooManyNodes
)

// ParseError is form text Parse refuses, and the pair it stopped at.
type ParseError struct {
	Problem Problem
	// Offset is the byte offset of the pair in the form text.
	Offset int
	// RawKey is the pair's key as sent, before percent-decoding, passed
	// through Excerpt. It never holds bytes of the value, which may be a
	// password or a token.
	RawKey string
}

// Message describes the refusal to the client that sent the form, naming
// the parameter by its key and never echoing its value.
func (e *ParseError) Message() string {
	at := "The parameter " + e.RawKey + " at byte " + strconv.Itoa(e.Offset)
	if e.RawKey == "" {
		at = "The parameter with an empty name at byte " + strconv.Itoa(e.Offset)
	}
	switch e.Problem {
	case BadEscape:
		return at + " has a % not followed by two hexadecimal digits; percent-encode a literal % as %25."
	case KeyTooDeep:
		return at + " has more than " + strconv.Itoa(MaxKeySegments) + " '.'-separated segments."
	case TooManyNodes:
		return at + " takes the form past " + strconv.Itoa(MaxNodes) + " distinct keys and key prefixes."
	}
	return at + " is refused."
}

func (e *ParseError) Error() string { return "form: " + e.Message() }

func newParseError(p Problem, offset int, pair string) *ParseError {
	rawKey, _, _ := strings.Cut(pair, "=")
	return &ParseError{Problem: p, Offset: offset, RawKey: Excerpt(rawKey)}
}

// ExcerptBytes bounds the client text an error message echoes, so an error
// for a 16 MiB key or value stays small.
const ExcerptBytes = 64

// Excerpt returns s cut to at most ExcerptBytes at a UTF-8 boundary, marked
// with "..." when cut.
func Excerpt(s string) string {
	if len(s) <= ExcerptBytes {
		return s
	}
	cut := ExcerptBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// Parse decodes form text, a body or a query string: pairs separated by
// '&', key from value by the first '=', '+' as a space and %XX as the byte
// it names, in keys and values alike. A pair without '=' is its key with
// the empty value, an empty pair is skipped, and a repeated key keeps its
// first value. Decoded bytes that are not UTF-8 are kept for the
// validators. A bad escape, a key of more than MaxKeySegments segments and
// a form of more than MaxNodes nodes are refused.
func Parse(s string) (*Values, *ParseError) {
	v := &Values{}
	offset := 0
	for pair := range strings.SplitSeq(s, "&") {
		at := offset
		offset += len(pair) + 1
		if pair == "" {
			continue
		}
		rawKey, rawValue, _ := strings.Cut(pair, "=")
		key, kerr := url.QueryUnescape(rawKey)
		value, verr := url.QueryUnescape(rawValue)
		if kerr != nil || verr != nil {
			return nil, newParseError(BadEscape, at, pair)
		}
		// The decoded key is counted: an escaped '.' (%2E) splits too.
		if strings.Count(key, ".") >= MaxKeySegments {
			return nil, newParseError(KeyTooDeep, at, pair)
		}
		v.Add(key, value)
		if v.nodes > MaxNodes {
			return nil, newParseError(TooManyNodes, at, pair)
		}
	}
	return v, nil
}

// Lookup returns the value of the first pair of form text s whose decoded
// key is key, without building the tree: it finds a request's Action
// cheaply. Pairs before it whose key does not decode cannot be key and are
// passed over; only the value it returns is decoded and checked.
func Lookup(s, key string) (value string, ok bool, err *ParseError) {
	offset := 0
	for pair := range strings.SplitSeq(s, "&") {
		at := offset
		offset += len(pair) + 1
		rawKey, rawValue, _ := strings.Cut(pair, "=")
		if k, err := url.QueryUnescape(rawKey); err != nil || k != key {
			continue
		}
		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			return "", false, newParseError(BadEscape, at, pair)
		}
		return value, true, nil
	}
	return "", false, nil
}

// Add sets key to value unless key already has a value: the first value of
// a key wins. Unlike Parse, it sets no limit: it is for keys the caller
// writes, not keys a client sent.
func (v *Values) Add(key, value string) {
	n := &v.root
	start := 0
	for {
		end := strings.IndexByte(key[start:], '.')
		if end < 0 {
			n = v.child(n, key[start:], key)
			break
		}
		n = v.child(n, key[start:start+end], key[:start+end])
		start += end + 1
	}
	if !n.hasValue {
		n.value, n.hasValue = value, true
	}
}

// Root returns the node above every key.
func (v *Values) Root() *Node { return &v.root }

// child returns the child of n named segment, creating it with the full
// key when absent.
func (v *Values) child(n *Node, segment, key string) *Node {
	if c, ok := n.children[segment]; ok {
		return c
	}
	if n.children == nil {
		n.children = map[string]*Node{}
	}
	c := &Node{key: key, segment: segment}
	n.children[segment] = c
	v.nodes++
	return c
}

// Key returns the full key of n, its segments joined by '.'; the root's is
// empty.
func (n *Node) Key() string { return n.key }

// Value returns the value sent for n's key, if the key itself was sent.
func (n *Node) Value() (string, bool) { return n.value, n.hasValue }

// Child returns the child of n named segment.
func (n *Node) Child(segment string) (*Node, bool) {
	c, ok := n.children[segment]
	return c, ok
}

// Indexed returns the children of n whose segment is a decimal index, in
// numeric order: member.2 before member.10, whatever order the client sent
// them in. Indexes of any length compare by value; two spellings of one
// value (2 and 02) keep their text order. Other children are left out.
func (n *Node) Indexed() []*Node {
	var out []*Node
	for seg, c := range n.children {
		if isIndex(seg) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b *Node) int {
		x, y := strings.TrimLeft(a.segment, "0"), strings.TrimLeft(b.segment, "0")
		return cmp.Or(cmp.Compare(len(x), len(y)), strings.Compare(x, y), strings.Compare(a.segment, b.segment))
	})
	return out
}

// Escape percent-encodes s as RFC 3986 asks: every byte but the unreserved
// letters, digits, '-', '.', '_' and '~' becomes %XX, so a space is %20
// and '+' is %2B.
func Escape(s string) string {
	// QueryEscape encodes every byte Escape does and a literal '+' as %2B;
	// the one difference is a space, which it writes as '+'.
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// Encode writes pairs as form text in their order, each key and value
// through Escape. It builds request bodies for tests.
func Encode(pairs []Pair) string {
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(Escape(p.Key))
		b.WriteByte('=')
		b.WriteString(Escape(p.Value))
	}
	return b.String()
}

func isIndex(seg string) bool {
	if seg == "" {
		return false
	}
	for i := range len(seg) {
		if seg[i] < '0' || seg[i] > '9' {
			return false
		}
	}
	return true
}
