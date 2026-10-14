package ruler

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Pattern is a compiled pattern: an OR of branches, one per combination of
// "$or" members, each an AND of conditions on flattened key paths. Nested
// keys are joined with ".", so {"a":{"b":[...]}} and {"a.b":[...]} name the
// same path (matching-engines.md 4.2). A Pattern is immutable and safe for
// concurrent use.
type Pattern struct {
	profile Profile
	// branches hold their conditions sorted by path; a condition is shared by
	// every branch that holds it.
	branches [][]*condition
}

// condition is one rule array as written in the source, at its path: the
// path matches when any matcher accepts a value there, or, when the rule
// array holds {"exists": false}, when no value is there. depth is the
// nesting level of the key in the pattern.
type condition struct {
	path     string
	matchers []matcher
	depth    int
	absent   bool
}

// Compile parses and validates a pattern for profile p. It follows Ruler's
// strict compiler (JsonRuleCompiler) token by token, so the first problem in
// source order is the one reported, with Ruler's text; then the profile's
// limits apply.
func Compile(pattern []byte, p Profile) (*Pattern, *CompileError) {
	if e := p.admit(pattern); e != nil {
		return nil, e.forProfile(p)
	}
	c, e := compileWith(pattern, p, false)
	if e != nil && p.options().MalformedOr == MalformedOrOrdinaryKey && e.Cause == (InvalidOperand{Operator: OperatorOr}) {
		c, e = compileWith(pattern, p, true)
	}
	if e != nil {
		return nil, e.forProfile(p)
	}
	if e := p.check(c); e != nil {
		return nil, e.forProfile(p)
	}
	return c.pattern(p), nil
}

// UsesWildcard reports whether any condition of any branch holds a
// wildcard, plain or inside anything-but. EventBridge counts such rules
// against a quota of their own (matching-engines.md 5.2, 30 per bus).
func (p *Pattern) UsesWildcard() bool {
	for _, branch := range p.branches {
		for _, cd := range branch {
			if len(cd.globs()) > 0 {
				return true
			}
		}
	}
	return false
}

func (e *CompileError) forProfile(p Profile) *CompileError {
	e.message = p.message(e)
	return e
}

// globs returns the wildcard patterns of the condition, plain and
// anything-but.
func (cd *condition) globs() []glob {
	gs := make([]glob, 0, len(cd.matchers))
	for _, m := range cd.matchers {
		gs = append(gs, wildcardCost(m).globs...)
	}
	return gs
}

// snsWildcardPoints is the SNS field complexity, (sum of pattern points) x
// (number of patterns), for a condition with a wildcard; conditions without
// one score nothing. A wildcard pattern scores 1 for one "*" and 3 per "*"
// for more (sns.md 6.2; per wildcard is the literal reading of "3 points
// each", H11); every other anything-but pattern scores 1.
func (cd *condition) snsWildcardPoints() int {
	if len(cd.globs()) == 0 {
		return 0
	}
	sum := 0
	for _, m := range cd.matchers {
		cost := wildcardCost(m)
		for _, g := range cost.globs {
			sum += globPoints(g)
		}
		if cost.anythingBut && len(cost.globs) == 0 {
			sum++
		}
	}
	return sum * len(cd.matchers)
}

func globPoints(g glob) int {
	if n := g.stars(); n > 1 {
		return 3 * n
	}
	return g.stars()
}

// matcherCost is what the wildcard limits see of a matcher.
type matcherCost struct {
	globs       []glob
	anythingBut bool
}

func wildcardCost(m matcher) matcherCost {
	switch m := m.(type) {
	case matchWildcard:
		return matcherCost{globs: []glob{m.pattern}}
	case matchAnythingButWildcard:
		return matcherCost{globs: m.patterns, anythingBut: true}
	case matchAnythingBut, matchAnythingButNumbers, matchAnythingButPrefix, matchAnythingButSuffix, matchAnythingButEqualsIgnoreCase:
		return matcherCost{anythingBut: true}
	case matchEquals, matchEqualsIP, matchEqualsNumber, matchEqualsNumberText, matchLiteral, matchPrefix, matchSuffix,
		matchPrefixIgnoreCase, matchSuffixIgnoreCase, matchEqualsIgnoreCase, matchNumeric, matchExists, matchNotExists, matchCIDR:
	}
	return matcherCost{}
}

// compiler holds the state of one compilation. It builds Ruler's rule list,
// one branch per "$or" combination, without copying a rule array into every
// branch: each branch in heads continues with the conditions in pending, and
// expand writes the branches out once the whole pattern is read.
type compiler struct {
	lx      *lexer
	profile Profile
	orAsKey bool
	// heads holds the last segment of each branch being built; nil is a
	// branch with no segment yet.
	heads   []*segment
	pending []*condition
	// branches is the expansion, set by expand.
	branches [][]*condition
	// orProduct saturates at maxExpansion.
	orProduct int
	maxDepth  int
	// work counts the conditions and segments written, up to maxExpansion.
	work int
	// overrun is the first limit the pattern is certain to exceed. Once it is
	// set, the branches stop being built and hold nothing meaningful; the
	// compiler only reads on, so a grammar error later in the source still
	// comes first, and the profile's check reports overrun in its place.
	overrun *CompileError
}

// segment is a run of conditions, in source order, that every branch
// through it holds. A branch is the chain from its last segment up through
// the parents, so branches that share a prefix share its segments, and a rule
// array is stored once however many "$or" branches hold it.
type segment struct {
	parent *segment
	conds  []*condition
	// paths holds the distinct paths of the branch up to this segment,
	// sorted, at most keysTracked of them.
	paths []string
}

// keysTracked bounds the key counting while a pattern is read: one past the
// SNS key limit is enough to know the limit is exceeded.
const keysTracked = snsMaxKeys + 1

func (s *segment) branchPaths() []string {
	if s == nil {
		return nil
	}
	return s.paths
}

// addPath returns the sorted set into with path added, unless into holds it
// or holds keysTracked paths. It copies into rather than change it, so
// segments can share a set.
func addPath(into []string, path string) []string {
	if len(into) == keysTracked {
		return into
	}
	if i, found := slices.BinarySearch(into, path); !found {
		into = slices.Insert(slices.Clip(into), i, path)
	}
	return into
}

// spend charges n units of expansion work. It reports false once the
// pattern has overrun a limit, when branches are no longer built.
func (c *compiler) spend(n int) bool {
	if c.overrun != nil {
		return false
	}
	c.work += n
	if c.work > maxExpansion {
		c.overrun = limitError(LimitCombinations, expansionDetail())
		return false
	}
	return true
}

func expansionDetail() string {
	return "The pattern is too complex: expanding its $or relationships takes more than " +
		strconv.Itoa(maxExpansion) + " steps"
}

// bound records the first limit that branches built so far, one of which
// holds keys keys, make certain.
func (c *compiler) bound(branches, keys int) {
	if c.overrun == nil {
		c.overrun = c.profile.overrun(expansion{branches: branches, keys: keys, orProduct: c.orProduct})
	}
}

func compileWith(src []byte, p Profile, orAsKey bool) (*compiler, *CompileError) {
	c := &compiler{lx: newLexer(src), profile: p, orAsKey: orAsKey, heads: []*segment{nil}, orProduct: 1}
	t, e := c.next()
	if e != nil {
		return nil, e
	}
	if t.kind != tokStartObject {
		return nil, structureError("Filter is not an object")
	}
	if e := c.object(nil, false); e != nil {
		return nil, e
	}
	t, e = c.next()
	if e != nil {
		return nil, e
	}
	if t.kind != tokEOF {
		return nil, structureError("Filter must be a single JSON object; found content after it")
	}
	c.expand()
	return c, nil
}

func (c *compiler) next() (token, *CompileError) {
	t, err := c.lx.next()
	if err != nil {
		return token{}, &CompileError{Cause: InvalidJSON{}, Detail: err.message}
	}
	return t, nil
}

// object reads the members of an object whose '{' was consumed.
func (c *compiler) object(path []string, inOr bool) *CompileError {
	present := false
	for {
		t, e := c.next()
		if e != nil {
			return e
		}
		if t.kind == tokEndObject {
			break
		}
		present = true
		if t.text == "$or" && !c.orAsKey {
			if e := c.or(path); e != nil {
				return e
			}
			continue
		}
		if inOr && reservedInOr(t.text) {
			return orError(t.text + " is Ruler reserved fieldName which cannot be used inside $or.")
		}
		if e := c.member(path, t.text, inOr); e != nil {
			return e
		}
	}
	if !present {
		return structureError("Empty objects are not allowed")
	}
	return nil
}

func (c *compiler) member(path []string, name string, inOr bool) *CompileError {
	v, e := c.next()
	if e != nil {
		return e
	}
	switch v.kind {
	case tokStartObject:
		return c.object(append(slices.Clip(path), name), inOr)
	case tokStartArray:
		return c.leaf(strings.Join(append(slices.Clip(path), name), "."), len(path)+1)
	case tokEOF, tokEndObject, tokEndArray, tokFieldName, tokString, tokNumber, tokTrue, tokFalse, tokNull:
	}
	return structureError(`"` + name + `" must be an object or an array`)
}

// or reads a "$or" array: each member object continues every branch so far
// with its own copy of the pending conditions, and the members' branches
// replace them.
func (c *compiler) or(path []string) *CompileError {
	heads, pending := c.heads, c.pending
	var done []*segment
	t, e := c.next()
	if e != nil {
		return e
	}
	if t.kind != tokStartArray {
		return orError("It must be an Array followed with $or.")
	}
	members := 0
	for {
		t, e := c.next()
		if e != nil {
			return e
		}
		if t.kind == tokEndArray {
			break
		}
		members++
		if t.kind != tokStartObject {
			return orError("Only JSON object is allowed in array of $or relationship.")
		}
		c.heads, c.pending = heads, slices.Clip(pending)
		c.spend(len(pending))
		if e := c.object(path, true); e != nil {
			return e
		}
		done = append(done, c.close()...)
		c.bound(len(done), 0)
	}
	if members < 2 {
		return orError("There must have at least 2 Objects in $or relationship.")
	}
	c.heads, c.pending = done, nil
	c.orProduct = min(c.orProduct*members, maxExpansion)
	c.bound(len(done), 0)
	return nil
}

// close ends the pending run: every branch gains a segment holding the
// pending conditions. It returns no branches once the pattern has overrun a
// limit.
func (c *compiler) close() []*segment {
	if c.overrun != nil {
		// leaf stops appending once a limit is overrun, so pending is empty
		// here; returning the heads would let every later "$or" multiply
		// them unbounded.
		return nil
	}
	if len(c.pending) == 0 {
		return c.heads
	}
	if !c.spend(len(c.heads) + len(c.pending)) {
		return nil
	}
	// Capped at keysTracked, the pending paths still decide every capped
	// union: a capped set is either complete or already full.
	var added []string
	for _, cd := range c.pending {
		added = addPath(added, cd.path)
	}
	out := make([]*segment, len(c.heads))
	keys := 0
	for i, h := range c.heads {
		paths := h.branchPaths()
		for _, p := range added {
			paths = addPath(paths, p)
		}
		out[i] = &segment{parent: h, conds: c.pending, paths: paths}
		keys = max(keys, len(paths))
	}
	c.bound(len(out), keys)
	return out
}

// expand writes out the branches: each holds, per path, the last condition
// written to it (a later key with the same path replaces an earlier one), in
// path order. Its work counts toward the budget, and each branch's key count
// toward the profile's limits.
func (c *compiler) expand() {
	heads := c.close()
	if c.overrun != nil {
		return
	}
	byPath := map[string]*condition{}
	var chain []*segment
	c.branches = make([][]*condition, 0, len(heads))
	for _, h := range heads {
		chain = chain[:0]
		for s := h; s != nil; s = s.parent {
			chain = append(chain, s)
		}
		clear(byPath)
		for _, seg := range slices.Backward(chain) {
			if !c.spend(1 + len(seg.conds)) {
				return
			}
			for _, cd := range seg.conds {
				byPath[cd.path] = cd
			}
			c.bound(len(heads), len(byPath))
			if c.overrun != nil {
				return
			}
		}
		// Ruler sorts a rule's keys (GenericMachine.addPatternRule) with Java's
		// String.compareTo, by UTF-16 code unit. The order decides which
		// conditions an {"exists": false} is checked against.
		conds := make([]*condition, 0, len(byPath))
		for _, cd := range byPath {
			conds = append(conds, cd)
		}
		slices.SortFunc(conds, func(a, b *condition) int { return compareUTF16(a.path, b.path) })
		c.branches = append(c.branches, conds)
	}
}

// liveConditions returns each condition that some branch holds, once. A
// condition replaced by a later duplicate key in every branch is not in the
// rule, so the service limits do not see it.
func (c *compiler) liveConditions() []*condition {
	seen := map[*condition]struct{}{}
	var out []*condition
	for _, b := range c.branches {
		for _, cd := range b {
			if _, dup := seen[cd]; !dup {
				seen[cd] = struct{}{}
				out = append(out, cd)
			}
		}
	}
	return out
}

// machineGlobs groups the globs of the live conditions the way Ruler places
// them on ByteMachines, one group per machine. Ruler walks a branch's keys
// in sorted order, so branches that hold the same conditions before a path
// reach that path from the same name state and share its ByteMachine; its
// MachineComplexityEvaluator then scores the machine's patterns together.
// Conditions are compared by identity: the expansion shares a condition
// between every branch that holds its rule array.
func (c *compiler) machineGlobs() [][]glob {
	type machine struct {
		prefix int
		path   string
	}
	type member struct {
		group int
		cd    *condition
	}
	wild := map[*condition][]glob{}
	for _, cd := range c.liveConditions() {
		if gs := cd.globs(); len(gs) > 0 {
			wild[cd] = gs
		}
	}
	if len(wild) == 0 {
		return nil
	}
	condIDs := interner[*condition]{}
	// prefixes interns a branch prefix as (prefix before it, last condition).
	prefixes := interner[[2]int]{}
	groups := interner[machine]{}
	added := map[member]bool{}
	var out [][]glob
	for _, b := range c.branches {
		prefix := 0
		// Conditions after the branch's last wildcard condition decide no
		// machine anyone scores.
		for _, cd := range b[:lastWild(b, wild)] {
			if gs, ok := wild[cd]; ok {
				g := groups.id(machine{prefix, cd.path}) - 1
				if g == len(out) {
					out = append(out, nil)
				}
				if !added[member{g, cd}] {
					added[member{g, cd}] = true
					out[g] = append(out[g], gs...)
				}
			}
			prefix = prefixes.id([2]int{prefix, condIDs.id(cd)})
		}
	}
	return out
}

// lastWild returns one past the index of b's last condition in wild.
func lastWild(b []*condition, wild map[*condition][]glob) int {
	for i, cd := range slices.Backward(b) {
		if _, ok := wild[cd]; ok {
			return i + 1
		}
	}
	return 0
}

// interner numbers distinct keys from 1 in the order they are first seen.
type interner[K comparable] map[K]int

func (in interner[K]) id(k K) int {
	if v, ok := in[k]; ok {
		return v
	}
	v := len(in) + 1
	in[k] = v
	return v
}

// leaf reads a rule array whose '[' was consumed and adds it to every branch
// being built.
func (c *compiler) leaf(path string, depth int) *CompileError {
	var ms []matcher
	for {
		t, e := c.next()
		if e != nil {
			return e
		}
		if t.kind == tokEndArray {
			break
		}
		m, e := c.rule(t)
		if e != nil {
			return e
		}
		ms = append(ms, m)
	}
	if len(ms) == 0 {
		return structureError("Empty arrays are not allowed")
	}
	c.maxDepth = max(c.maxDepth, depth)
	if c.spend(1) {
		c.pending = append(c.pending, &condition{
			path: path, matchers: ms, depth: depth, absent: slices.ContainsFunc(ms, isNotExists),
		})
	}
	return nil
}

// rule compiles one element of a rule array.
func (c *compiler) rule(t token) (matcher, *CompileError) {
	switch t.kind {
	case tokStartObject:
		return c.expression()
	case tokString:
		s, e := stringText(t)
		if e != nil {
			return nil, e
		}
		if a, ok := parseIP(s); ok {
			return matchEqualsIP{addr: addressRange(a)}, nil
		}
		return matchEquals{text: s}, nil
	case tokNumber:
		if f, ok := comparableNumber(t.text); ok {
			return matchEqualsNumber{text: t.text, value: f}, nil
		}
		return matchEqualsNumberText{text: t.text}, nil
	case tokTrue:
		return matchLiteral{literal: literalTrue}, nil
	case tokFalse:
		return matchLiteral{literal: literalFalse}, nil
	case tokNull:
		return matchLiteral{literal: literalNull}, nil
	case tokEOF, tokEndObject, tokStartArray, tokEndArray, tokFieldName:
	}
	return nil, structureError("Match value must be String, number, true, false, or null")
}

// expression compiles an operator object whose '{' was consumed.
func (c *compiler) expression() (matcher, *CompileError) {
	t, e := c.next()
	if e != nil {
		return nil, e
	}
	if t.kind != tokFieldName {
		return nil, structureError("Match expression name not found")
	}
	op, known := operatorNamed(t.text)
	if !known {
		return nil, &CompileError{Cause: UnknownOperator{Name: t.text}, Detail: "Unrecognized match type " + t.text}
	}
	if !c.profile.allows(op) {
		return nil, unsupported(op, "Unrecognized match type "+t.text)
	}
	switch op {
	case OperatorPrefix, OperatorSuffix:
		return c.affix(op)
	case OperatorNumeric:
		return c.numeric()
	case OperatorAnythingBut:
		return c.anythingBut()
	case OperatorExists:
		return c.exists()
	case OperatorCIDR:
		return c.cidr()
	case OperatorEqualsIgnoreCase:
		return c.equalsIgnoreCase()
	case OperatorWildcard:
		return c.wildcard()
	case OperatorPrefixEqualsIgnoreCase, OperatorSuffixEqualsIgnoreCase, OperatorAnythingButPrefix,
		OperatorAnythingButSuffix, OperatorAnythingButEqualsIgnoreCase, OperatorAnythingButWildcard, OperatorOr:
	}
	return nil, &CompileError{Cause: UnknownOperator{Name: t.text}, Detail: "Unrecognized match type " + t.text}
}

// operatorNamed maps a match type name to its base operator. "exactly" is a
// Ruler operator no service documents, so it is unknown here.
func operatorNamed(name string) (Operator, bool) {
	switch name {
	case "prefix":
		return OperatorPrefix, true
	case "suffix":
		return OperatorSuffix, true
	case "numeric":
		return OperatorNumeric, true
	case "anything-but":
		return OperatorAnythingBut, true
	case "exists":
		return OperatorExists, true
	case "cidr":
		return OperatorCIDR, true
	case "equals-ignore-case":
		return OperatorEqualsIgnoreCase, true
	case "wildcard":
		return OperatorWildcard, true
	}
	return 0, false
}

// end consumes the '}' closing an operator object.
func (c *compiler) end(op Operator, detail string) *CompileError {
	t, e := c.next()
	if e != nil {
		return e
	}
	if t.kind != tokEndObject {
		return operandError(op, detail)
	}
	return nil
}

const onlyOneKey = "Only one key allowed in match expression"

func (c *compiler) affix(op Operator) (matcher, *CompileError) {
	name, icOp := "prefix", OperatorPrefixEqualsIgnoreCase
	if op == OperatorSuffix {
		name, icOp = "suffix", OperatorSuffixEqualsIgnoreCase
	}
	v, e := c.next()
	if e != nil {
		return nil, e
	}
	if v.kind == tokStartObject {
		if !c.profile.allows(icOp) {
			return nil, unsupported(icOp, name+" match pattern must be a string")
		}
		return c.affixIgnoreCase(name, icOp)
	}
	if v.kind != tokString {
		return nil, operandError(op, name+" match pattern must be a string")
	}
	s, e := stringText(v)
	if e != nil {
		return nil, e
	}
	if e := c.end(op, onlyOneKey); e != nil {
		return nil, e
	}
	if op == OperatorSuffix {
		return matchSuffix{text: s}, nil
	}
	return matchPrefix{text: s}, nil
}

func (c *compiler) affixIgnoreCase(name string, op Operator) (matcher, *CompileError) {
	t, e := c.next()
	if e != nil {
		return nil, e
	}
	if t.kind != tokFieldName {
		return nil, operandError(op, strings.ToUpper(name[:1])+name[1:]+" expression name not found")
	}
	if t.text != "equals-ignore-case" {
		return nil, operandError(op, "Unsupported "+name+" pattern: "+t.text)
	}
	v, e := c.next()
	if e != nil {
		return nil, e
	}
	if v.kind != tokString {
		return nil, operandError(op, "equals-ignore-case match pattern must be a string")
	}
	s, e := stringText(v)
	if e != nil {
		return nil, e
	}
	if e := c.end(op, onlyOneKey); e != nil {
		return nil, e
	}
	if e := c.end(op, onlyOneKey); e != nil {
		return nil, e
	}
	if op == OperatorSuffixEqualsIgnoreCase {
		return matchSuffixIgnoreCase{fold: newCaseless(s)}, nil
	}
	return matchPrefixIgnoreCase{fold: newCaseless(s)}, nil
}

func (c *compiler) exists() (matcher, *CompileError) {
	v, e := c.next()
	if e != nil {
		return nil, e
	}
	var m matcher
	switch v.kind {
	case tokTrue:
		m = matchExists{}
	case tokFalse:
		m = matchNotExists{}
	case tokEOF, tokStartObject, tokEndObject, tokStartArray, tokEndArray, tokFieldName, tokString, tokNumber, tokNull:
		return nil, operandError(OperatorExists, "exists match pattern must be either true or false.")
	}
	if e := c.end(OperatorExists, onlyOneKey); e != nil {
		return nil, e
	}
	return m, nil
}

func (c *compiler) cidr() (matcher, *CompileError) {
	v, e := c.next()
	if e != nil {
		return nil, e
	}
	if v.kind != tokString {
		// Ruler's text, sic.
		return nil, operandError(OperatorCIDR, "prefix match pattern must be a string")
	}
	s, e := stringText(v)
	if e != nil {
		return nil, e
	}
	prefix, detail := parseCIDR(s)
	if detail != "" {
		return nil, operandError(OperatorCIDR, detail)
	}
	if e := c.end(OperatorCIDR, onlyOneKey); e != nil {
		return nil, e
	}
	return matchCIDR{prefix: cidrRange(prefix)}, nil
}

func (c *compiler) equalsIgnoreCase() (matcher, *CompileError) {
	v, e := c.next()
	if e != nil {
		return nil, e
	}
	if v.kind != tokString {
		return nil, operandError(OperatorEqualsIgnoreCase, "equals-ignore-case match pattern must be a string")
	}
	s, e := stringText(v)
	if e != nil {
		return nil, e
	}
	if e := c.end(OperatorEqualsIgnoreCase, onlyOneKey); e != nil {
		return nil, e
	}
	return matchEqualsIgnoreCase{fold: newCaseless(s)}, nil
}

func (c *compiler) wildcard() (matcher, *CompileError) {
	v, e := c.next()
	if e != nil {
		return nil, e
	}
	if v.kind != tokString {
		return nil, operandError(OperatorWildcard, "wildcard match pattern must be a string")
	}
	s, e := stringText(v)
	if e != nil {
		return nil, e
	}
	g, detail := parseGlob(s)
	if detail != "" {
		return nil, operandError(OperatorWildcard, detail)
	}
	if e := c.end(OperatorWildcard, onlyOneKey); e != nil {
		return nil, e
	}
	return matchWildcard{pattern: g}, nil
}

func (c *compiler) pattern(p Profile) *Pattern {
	markNumericBeside(c.branches, p.options())
	return &Pattern{profile: p, branches: c.branches}
}

// markNumericBeside tells the IP range matchers at each path where a
// numeric rule also sits: Ruler's byte machine for the field then reads a
// comparable number as a number and never walks its text through the
// ranges. Ruler keeps one byte machine per field and per state of its walk
// through the sorted keys, so in a "$or" a numeric rule reached through
// other earlier keys has a machine of its own; here every rule at the path
// counts, in every branch.
func markNumericBeside(branches [][]*condition, o Options) {
	numeric := map[string]bool{}
	for _, branch := range branches {
		for _, cond := range branch {
			numeric[cond.path] = numeric[cond.path] || slices.ContainsFunc(cond.matchers, func(m matcher) bool {
				return isNumericRule(m, o)
			})
		}
	}
	for _, branch := range branches {
		for _, cond := range branch {
			if !numeric[cond.path] {
				continue
			}
			for i, m := range cond.matchers {
				switch m := m.(type) {
				case matchCIDR:
					m.numericBeside = true
					cond.matchers[i] = m
				case matchEqualsIP:
					m.numericBeside = true
					cond.matchers[i] = m
				case matchEquals, matchEqualsNumber, matchEqualsNumberText, matchLiteral, matchPrefix, matchSuffix,
					matchPrefixIgnoreCase, matchSuffixIgnoreCase, matchEqualsIgnoreCase, matchWildcard, matchAnythingBut,
					matchAnythingButNumbers, matchAnythingButPrefix, matchAnythingButSuffix, matchAnythingButEqualsIgnoreCase,
					matchAnythingButWildcard, matchNumeric, matchExists, matchNotExists:
				}
			}
		}
	}
}

// isNumericRule reports whether Ruler counts m as a numeric pattern of its
// field (ByteMachine.hasNumeric): a numeric range, a numeric anything-but,
// or a bare number it compares by value.
func isNumericRule(m matcher, o Options) bool {
	switch m.(type) {
	case matchNumeric, matchAnythingButNumbers:
		return true
	case matchEqualsNumber:
		return o.BareNumber == BareNumberNumericOrText
	case matchEquals, matchEqualsIP, matchEqualsNumberText, matchLiteral, matchPrefix, matchSuffix,
		matchPrefixIgnoreCase, matchSuffixIgnoreCase, matchEqualsIgnoreCase, matchWildcard, matchAnythingBut,
		matchAnythingButPrefix, matchAnythingButSuffix, matchAnythingButEqualsIgnoreCase, matchAnythingButWildcard,
		matchExists, matchNotExists, matchCIDR:
	}
	return false
}

// compareUTF16 orders strings by their UTF-16 code units, as Java does. It
// differs from byte order only between a character above U+FFFF, whose
// first unit is a surrogate (U+D800 to U+DBFF), and one from U+E000 to
// U+FFFF.
func compareUTF16(a, b string) int {
	for a != "" && b != "" {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if ra != rb {
			if ua, ub := firstUTF16(ra), firstUTF16(rb); ua != ub {
				return cmp.Compare(ua, ub)
			}
			return cmp.Compare(ra, rb)
		}
		a, b = a[na:], b[nb:]
	}
	return cmp.Compare(len(a), len(b))
}

// firstUTF16 is the first UTF-16 code unit of r.
func firstUTF16(r rune) rune {
	if hi, _ := utf16.EncodeRune(r); hi != utf8.RuneError {
		return hi
	}
	return r
}

func isNotExists(m matcher) bool {
	_, ok := m.(matchNotExists)
	return ok
}

// reservedInOr is Ruler's list of names that cannot be keys inside "$or"
// (Constants.RESERVED_FIELD_NAMES_IN_OR_RELATIONSHIP).
func reservedInOr(name string) bool {
	switch name {
	case "exactly", "equals-ignore-case", "prefix", "suffix", "anything-but", "exists", "wildcard", "numeric", "cidr",
		"=", "<", "<=", ">", ">=", "regex", "not-wildcard", "not-equals-ignore-case",
		"date-after", "date-on-or-after", "date-before", "date-on-or-before", "in-date-range",
		"ip-address-in-range", "ip-address-not-in-range":
		return true
	}
	return false
}

func structureError(detail string) *CompileError {
	return &CompileError{Cause: InvalidStructure{}, Detail: detail}
}

func operandError(op Operator, detail string) *CompileError {
	return &CompileError{Cause: InvalidOperand{Operator: op}, Detail: detail}
}

func unsupported(op Operator, detail string) *CompileError {
	return &CompileError{Cause: UnsupportedOperator{Operator: op}, Detail: detail}
}

func orError(detail string) *CompileError {
	return operandError(OperatorOr, detail)
}
