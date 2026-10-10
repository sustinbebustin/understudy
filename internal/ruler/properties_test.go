package ruler

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// node is a JSON value for building patterns and events in properties.
// Objects keep member order so properties can permute it.
type node struct {
	scalar  string // raw JSON text of a scalar, when object and array are nil
	members []member
	elems   []node
	isObj   bool
	isArr   bool
}

type member struct {
	key   string
	value node
}

func scalar(raw string) node     { return node{scalar: raw} }
func object(ms ...member) node   { return node{isObj: true, members: ms} }
func array(es ...node) node      { return node{isArr: true, elems: es} }
func kv(k string, v node) member { return member{key: k, value: v} }
func (n node) bytes() []byte     { return []byte(n.encode()) }

func (n node) encode() string {
	var b strings.Builder
	n.write(&b)
	return b.String()
}

func (n node) write(b *strings.Builder) {
	switch {
	case n.isObj:
		b.WriteByte('{')
		for i, m := range n.members {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(m.key))
			b.WriteByte(':')
			m.value.write(b)
		}
		b.WriteByte('}')
	case n.isArr:
		b.WriteByte('[')
		for i, e := range n.elems {
			if i > 0 {
				b.WriteByte(',')
			}
			e.write(b)
		}
		b.WriteByte(']')
	default:
		b.WriteString(n.scalar)
	}
}

// eventScalars mixes types that Ruler keeps distinct: "300" and 300, "true"
// and true, null and "".
var eventScalars = []string{
	`"x"`, `"y"`, `"X"`, `""`, `"null"`, `"300"`, `"true"`, `"10.0.0.7"`, `"xyz"`,
	`1`, `2.5`, `300`, `3.0e2`, `-0`, `true`, `false`, `null`,
}

// eventKeys covers patternKeys and the "$or" member keys, so "$or" branches
// can hold on values, not only through {"exists": false}.
var eventKeys = []string{"a", "b", "c", "d", "e"}

// patternKeys are the keys of pattern objects outside "$or".
var patternKeys = []string{"a", "b", "c"}

func eventValue(depth int) *rapid.Generator[node] {
	return rapid.Custom(func(t *rapid.T) node {
		kind := rapid.IntRange(0, 2).Draw(t, "kind")
		if depth == 0 {
			kind = 0
		}
		switch kind {
		case 1:
			return array(rapid.SliceOfN(eventValue(depth-1), 0, 3).Draw(t, "elems")...)
		case 2:
			return eventObject(depth-1).Draw(t, "object")
		}
		return scalar(rapid.SampledFrom(eventScalars).Draw(t, "scalar"))
	})
}

func eventObject(depth int) *rapid.Generator[node] {
	return rapid.Custom(func(t *rapid.T) node {
		keys := rapid.SliceOfNDistinct(rapid.SampledFrom(eventKeys), 0, 3, func(k string) string { return k }).Draw(t, "keys")
		ms := make([]member, len(keys))
		for i, k := range keys {
			ms[i] = kv(k, eventValue(depth).Draw(t, k))
		}
		return object(ms...)
	})
}

var ruleValues = []string{
	`"x"`, `"y"`, `""`, `300`, `1`, `true`, `null`, `"10.0.0.7"`,
	`{"prefix":"x"}`, `{"suffix":"z"}`, `{"equals-ignore-case":"X"}`, `{"wildcard":"*y*"}`,
	`{"anything-but":"x"}`, `{"anything-but":[1,300]}`, `{"anything-but":{"prefix":"x"}}`,
	`{"numeric":[">",1]}`, `{"numeric":[">=",0,"<",300]}`, `{"exists":true}`, `{"exists":false}`,
	`{"cidr":"10.0.0.0/24"}`,
}

func ruleLeaf() *rapid.Generator[node] {
	return rapid.Custom(func(t *rapid.T) node {
		vs := rapid.SliceOfN(rapid.SampledFrom(ruleValues), 1, 3).Draw(t, "rules")
		es := make([]node, len(vs))
		for i, v := range vs {
			es[i] = scalar(v)
		}
		return array(es...)
	})
}

// patternObject draws a pattern object over patternKeys. At most one "$or"
// per object, and its members use keys ("d", "e") no sibling uses, so no
// branch overrides a sibling and member order cannot matter.
func patternObject(depth int, withOr bool) *rapid.Generator[node] {
	return rapid.Custom(func(t *rapid.T) node {
		keys := rapid.SliceOfNDistinct(rapid.SampledFrom(patternKeys), 1, 3, func(k string) string { return k }).Draw(t, "keys")
		ms := make([]member, 0, len(keys)+1)
		for _, k := range keys {
			if depth > 0 && rapid.Bool().Draw(t, "nest") {
				ms = append(ms, kv(k, patternObject(depth-1, false).Draw(t, k)))
				continue
			}
			ms = append(ms, kv(k, ruleLeaf().Draw(t, k)))
		}
		if withOr && rapid.Bool().Draw(t, "or") {
			n := rapid.IntRange(2, 3).Draw(t, "branches")
			branches := make([]node, n)
			for i := range branches {
				bk := rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"d", "e"}), 1, 2, func(k string) string { return k }).Draw(t, "branch keys")
				bms := make([]member, len(bk))
				for j, k := range bk {
					bms[j] = kv(k, ruleLeaf().Draw(t, k))
				}
				branches[i] = object(bms...)
			}
			ms = append(ms, kv("$or", array(branches...)))
		}
		return object(ms...)
	})
}

// permuteArrays reorders the elements of every array in n.
func permuteArrays(t *rapid.T, n node) node {
	switch {
	case n.isObj:
		ms := make([]member, len(n.members))
		for i, m := range n.members {
			ms[i] = kv(m.key, permuteArrays(t, m.value))
		}
		return object(ms...)
	case n.isArr:
		es := make([]node, len(n.elems))
		for i, e := range n.elems {
			es[i] = permuteArrays(t, e)
		}
		return array(rapid.Permutation(es).Draw(t, "permutation")...)
	}
	return n
}

// permuteKeys reorders the members of every object in a pattern, leaving
// rule arrays alone.
func permuteKeys(t *rapid.T, n node) node {
	if !n.isObj {
		if n.isArr {
			es := make([]node, len(n.elems))
			for i, e := range n.elems {
				es[i] = permuteKeys(t, e)
			}
			return array(es...)
		}
		return n
	}
	ms := make([]member, len(n.members))
	for i, m := range n.members {
		ms[i] = kv(m.key, permuteKeys(t, m.value))
	}
	return object(rapid.Permutation(ms).Draw(t, "order")...)
}

func compileRapid(t *rapid.T, n node, p Profile) *Pattern {
	c, err := Compile(n.bytes(), p)
	if err != nil {
		t.Fatalf("Compile(%s): %s", n.encode(), err.Detail)
	}
	return c
}

func matchRapid(t *rapid.T, p *Pattern, event node) bool {
	ok, err := Match(p, event.bytes())
	if err != nil {
		t.Fatalf("Match(%s): %v", event.encode(), err)
	}
	return ok
}

// A pattern matches an event iff it matches the event with every array
// reordered: array elements are an unordered set of candidates, and array
// consistency only asks that two values come from the same element.
func TestPropertyArrayOrderIrrelevant(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		p := compileRapid(t, patternObject(2, true).Draw(t, "pattern"), EventBridge{})
		event := eventObject(3).Draw(t, "event")
		permuted := permuteArrays(t, event)
		if matchRapid(t, p, event) != matchRapid(t, p, permuted) {
			t.Fatalf("Match differs between %s and %s", event.encode(), permuted.encode())
		}
	})
}

// Matching does not depend on the order of keys in the pattern.
func TestPropertyPatternKeyOrderIrrelevant(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		pattern := patternObject(2, true).Draw(t, "pattern")
		reordered := permuteKeys(t, pattern)
		a, b := compileRapid(t, pattern, EventBridge{}), compileRapid(t, reordered, EventBridge{})
		event := eventObject(3).Draw(t, "event")
		if matchRapid(t, a, event) != matchRapid(t, b, event) {
			t.Fatalf("%s and %s disagree on %s", pattern.encode(), reordered.encode(), event.encode())
		}
	})
}

// naiveMatches is the plain backtracking search over every field, without
// the failure memo, the element lookup or the repeat filter, on explicit
// membership lists: exponential, but plainly the array-consistency rule of
// matching-engines.md 4.3 with Ruler's order for absences, each checked
// against the fields chosen for the paths sorted before it.
func naiveMatches(p *Pattern, fs *fields) bool {
	o := p.profile.options()
	if fs.empty && o.EmptyInput == EmptyInputNeverMatches {
		return false
	}
	for _, b := range p.branches {
		if naiveAssign(b, fs, o, nil) {
			return true
		}
	}
	return false
}

// position is one array a field sits in and the element it sits in there.
type position struct{ array, element int }

// membership lists the positions of a field in element e.
func membership(e *element) []position {
	var ps []position
	for ; e != nil; e = e.parent {
		ps = append(ps, position{e.array, e.id})
	}
	return ps
}

// naiveConsistent reports whether positions agree with the chosen ones: the
// same array at a different element conflicts.
func naiveConsistent(ps, chosen []position) bool {
	for _, p := range ps {
		for _, q := range chosen {
			if p.array == q.array && p.element != q.element {
				return false
			}
		}
	}
	return true
}

func naiveAssign(conds []*condition, fs *fields, o Options, chosen []position) bool {
	if len(conds) == 0 {
		return true
	}
	c := conds[0]
	for _, f := range fs.byPath[c.path] {
		ps := membership(f.elem)
		if naiveConsistent(ps, chosen) && accepted(c.matchers, f.value, o) &&
			naiveAssign(conds[1:], fs, o, append(slices.Clip(chosen), ps...)) {
			return true
		}
	}
	if !c.absent {
		return false
	}
	for _, f := range fs.byPath[c.path] {
		if naiveConsistent(membership(f.elem), chosen) {
			return false
		}
	}
	return naiveAssign(conds[1:], fs, o, chosen)
}

// denseEvent draws objects whose values are mostly arrays of objects over
// two keys and two strings, so array consistency decides most matches.
func denseEvent(depth int) *rapid.Generator[node] {
	return rapid.Custom(func(t *rapid.T) node {
		keys := rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"a", "b"}), 1, 2, func(k string) string { return k }).Draw(t, "keys")
		ms := make([]member, len(keys))
		for i, k := range keys {
			if depth == 0 || rapid.Bool().Draw(t, "scalar") {
				ms[i] = kv(k, scalar(rapid.SampledFrom([]string{`"x"`, `"y"`}).Draw(t, "value")))
				continue
			}
			ms[i] = kv(k, array(rapid.SliceOfN(denseEvent(depth-1), 1, 3).Draw(t, "elems")...))
		}
		return object(ms...)
	})
}

// collidingEvent draws events like denseEvent's whose keys may repeat or
// contain a dot, so one path can have fields in several arrays: "a.b" names
// the path of "b" inside "a", and a repeated key opens a second array at the
// same path. Patterns over "a" and "b" then alternate between independent
// arrays, or join them through one path.
func collidingEvent(depth int) *rapid.Generator[node] {
	return rapid.Custom(func(t *rapid.T) node {
		keys := rapid.SliceOfN(rapid.SampledFrom([]string{"a", "b", "a.b", "b.a"}), 1, 3).Draw(t, "keys")
		ms := make([]member, len(keys))
		for i, k := range keys {
			if depth == 0 || rapid.Bool().Draw(t, "scalar") {
				ms[i] = kv(k, scalar(rapid.SampledFrom([]string{`"x"`, `"y"`}).Draw(t, "value")))
				continue
			}
			ms[i] = kv(k, array(rapid.SliceOfN(collidingEvent(depth-1), 1, 3).Draw(t, "elems")...))
		}
		return object(ms...)
	})
}

// densePattern draws patterns over denseEvent's keys and strings, with
// exists rules so absent conditions take part in the search.
func densePattern(depth int) *rapid.Generator[node] {
	return rapid.Custom(func(t *rapid.T) node {
		keys := rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"a", "b"}), 1, 2, func(k string) string { return k }).Draw(t, "keys")
		ms := make([]member, len(keys))
		for i, k := range keys {
			if depth > 0 && rapid.Bool().Draw(t, "nest") {
				ms[i] = kv(k, densePattern(depth-1).Draw(t, k))
				continue
			}
			rules := rapid.SliceOfN(rapid.SampledFrom([]string{`"x"`, `"y"`, `{"exists":false}`, `{"exists":true}`}), 1, 2).Draw(t, "rules")
			es := make([]node, len(rules))
			for j, r := range rules {
				es[j] = scalar(r)
			}
			ms[i] = kv(k, array(es...))
		}
		return object(ms...)
	})
}

// The memoized search agrees with the naive one: searching each component of
// a branch on its own, and dropping choices in arrays no remaining condition
// can see, never changes the outcome.
func TestPropertySearchMatchesNaiveSearch(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		var p *Pattern
		var event node
		switch rapid.SampledFrom([]string{"dense", "colliding", "general"}).Draw(t, "events") {
		case "dense":
			p = compileRapid(t, densePattern(2).Draw(t, "pattern"), EventBridge{})
			event = denseEvent(2).Draw(t, "event")
		case "colliding":
			p = compileRapid(t, densePattern(2).Draw(t, "pattern"), EventBridge{})
			event = collidingEvent(2).Draw(t, "event")
		default:
			p = compileRapid(t, patternObject(2, true).Draw(t, "pattern"), EventBridge{})
			event = eventObject(3).Draw(t, "event")
		}
		fs, err := flattenEvent(event.bytes())
		if err != nil {
			t.Fatalf("flattenEvent(%s): %v", event.encode(), err)
		}
		got, matchErr := p.matches(fs, maxSearchSteps)
		if matchErr != nil {
			t.Fatalf("Match(%s): %v", event.encode(), matchErr)
		}
		if want := naiveMatches(p, fs); got != want {
			t.Fatalf("Match = %t, naive search = %t, for %s", got, want, event.encode())
		}
	})
}

// A numeric rule never matches a string, however numeric the string looks.
func TestPropertyNumericNeverMatchesStrings(t *testing.T) {
	t.Parallel()
	number := rapid.Map(rapid.Float64Range(-1e6, 1e6), func(f float64) string {
		return strconv.FormatFloat(f, 'g', -1, 64)
	})
	rapid.Check(t, func(t *rapid.T) {
		low := number.Draw(t, "low")
		ops := rapid.SampledFrom([]string{
			`["=",` + low + `]`, `[">",` + low + `]`, `[">=",` + low + `]`, `["<",` + low + `]`, `["<=",` + low + `]`,
			`[">",` + low + `,"<",1e7]`, `[">=",-1e7,"<=",` + low + `]`,
		}).Draw(t, "operand")
		pattern := object(kv("n", array(scalar(`{"numeric":`+ops+`}`))))
		text := rapid.OneOf(number, rapid.String(), rapid.SampledFrom([]string{"0", "-0", "1e2", "NaN", "Infinity"})).Draw(t, "text")
		encoded, err := json.Marshal(text)
		if err != nil {
			t.Fatalf("encoding %q: %v", text, err)
		}
		values := rapid.SliceOfN(rapid.Just(scalar(string(encoded))), 1, 3).Draw(t, "values")
		event := object(kv("n", array(values...)))
		for _, prof := range []Profile{EventBridge{}, SNS{Scope: MessageBody}, LambdaESM{}} {
			if matchRapid(t, compileRapid(t, pattern, prof), event) {
				t.Fatalf("%T: %s matched string event %s", prof, pattern.encode(), event.encode())
			}
		}
	})
}

// operandFaults are malformed operands for each operator.
var operandFaults = map[Operator][]string{
	OperatorPrefix:                      {`{"prefix":1}`, `{"prefix":null}`, `{"prefix":"a","x":1}`},
	OperatorPrefixEqualsIgnoreCase:      {`{"prefix":{"equals-ignore-case":1}}`, `{"prefix":{"other":"a"}}`, `{"prefix":{}}`},
	OperatorSuffix:                      {`{"suffix":[]}`, `{"suffix":true}`},
	OperatorSuffixEqualsIgnoreCase:      {`{"suffix":{"equals-ignore-case":"a","x":1}}`},
	OperatorEqualsIgnoreCase:            {`{"equals-ignore-case":2}`, `{"equals-ignore-case":"a","b":"c"}`},
	OperatorWildcard:                    {`{"wildcard":"a**"}`, `{"wildcard":"a\\b"}`, `{"wildcard":["a"]}`},
	OperatorAnythingBut:                 {`{"anything-but":[]}`, `{"anything-but":["a",1]}`, `{"anything-but":true}`, `{"anything-but":[null]}`, `{"anything-but":1e400}`, `{"anything-but":{"nope":"a"}}`},
	OperatorAnythingButPrefix:           {`{"anything-but":{"prefix":""}}`, `{"anything-but":{"prefix":[1]}}`},
	OperatorAnythingButSuffix:           {`{"anything-but":{"suffix":[""]}}`},
	OperatorAnythingButEqualsIgnoreCase: {`{"anything-but":{"equals-ignore-case":[1]}}`},
	OperatorAnythingButWildcard:         {`{"anything-but":{"wildcard":"**"}}`, `{"anything-but":{"wildcard":[true]}}`},
	OperatorNumeric:                     {`{"numeric":[]}`, `{"numeric":["=",9007199254740993]}`, `{"numeric":[">",5,"<",1]}`, `{"numeric":"5"}`, `{"numeric":["!",1]}`},
	OperatorExists:                      {`{"exists":1}`, `{"exists":true,"x":1}`},
	OperatorCIDR:                        {`{"cidr":"10.0.0.0"}`, `{"cidr":"10.0.0.0/32"}`, `{"cidr":"::/128"}`, `{"cidr":"1.2.3.4/x"}`, `{"cidr":"999.0.0.1/8"}`, `{"cidr":"10.0.0.0/4294967296"}`},
	// The strict compiler's "$or" shapes: not an array, one member, a member
	// that is not an object, and a reserved name inside a member.
	OperatorOr: {`{"m":["x"]}`, `[{"m":["x"]}]`, `[{"m":["x"]},"x"]`, `[{"prefix":["x"]},{"m":["y"]}]`},
}

// Every compile error names what failed: a malformed operand names its
// operator, an operator outside the allow-list names that operator, and a
// pattern over a limit names the limit.
func TestPropertyCompileErrorsNameTheFailure(t *testing.T) {
	t.Parallel()
	ops := slices.Sorted(maps.Keys(operandFaults))
	rapid.Check(t, func(t *rapid.T) {
		op := rapid.SampledFrom(ops).Draw(t, "operator")
		fault := rapid.SampledFrom(operandFaults[op]).Draw(t, "fault")
		valid := patternObject(1, false).Draw(t, "context")
		at := rapid.IntRange(0, len(valid.members)).Draw(t, "position")
		// A "$or" fault is the value of a "$or" member, at the top level or
		// nested; every other fault is an element of a rule array.
		bad := kv("z", object(kv("q", array(scalar(`"ok"`), scalar(fault)))))
		if op == OperatorOr {
			bad = kv("$or", scalar(fault))
			if rapid.Bool().Draw(t, "nested") {
				bad = kv("z", object(kv("q", array(scalar(`"ok"`))), bad))
			}
		}
		ms := append(append(append([]member{}, valid.members[:at]...), bad), valid.members[at:]...)
		pattern := object(ms...)
		_, err := Compile(pattern.bytes(), EventBridge{})
		if err == nil || err.Cause != (InvalidOperand{Operator: op}) {
			t.Fatalf("Compile(%s) = %#v, want an error naming %s", pattern.encode(), err, op)
		}
	})
}

func TestPropertyUnsupportedOperatorsAreNamed(t *testing.T) {
	t.Parallel()
	forms := map[Operator]string{
		OperatorPrefixEqualsIgnoreCase:      `{"prefix":{"equals-ignore-case":"a"}}`,
		OperatorSuffixEqualsIgnoreCase:      `{"suffix":{"equals-ignore-case":"a"}}`,
		OperatorWildcard:                    `{"wildcard":"a*"}`,
		OperatorAnythingButPrefix:           `{"anything-but":{"prefix":"a"}}`,
		OperatorAnythingButSuffix:           `{"anything-but":{"suffix":"a"}}`,
		OperatorAnythingButEqualsIgnoreCase: `{"anything-but":{"equals-ignore-case":"a"}}`,
		OperatorAnythingButWildcard:         `{"anything-but":{"wildcard":"a*"}}`,
		OperatorCIDR:                        `{"cidr":"10.0.0.0/8"}`,
	}
	ops := slices.Sorted(maps.Keys(forms))
	rapid.Check(t, func(t *rapid.T) {
		op := rapid.SampledFrom(ops).Draw(t, "operator")
		prof := rapid.SampledFrom([]Profile{SNS{Scope: MessageBody}, LambdaESM{}}).Draw(t, "profile")
		pattern := object(kv("k", array(scalar(forms[op]))))
		_, err := Compile(pattern.bytes(), prof)
		if prof.allows(op) {
			if err != nil {
				t.Fatalf("%T rejected allowed %s: %s", prof, op, err.Detail)
			}
			return
		}
		if err == nil || err.Cause != (UnsupportedOperator{Operator: op}) {
			t.Fatalf("%T: Compile(%s) = %#v, want an error naming %s", prof, pattern.encode(), err, op)
		}
	})
}

func TestPropertyLimitErrorsNameTheLimit(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		keys := rapid.IntRange(1, 9).Draw(t, "keys")
		counts := rapid.SliceOfN(rapid.IntRange(1, 12), keys, keys).Draw(t, "values per key")
		ms := make([]member, keys)
		product := 1
		for i, n := range counts {
			es := make([]node, n)
			for j := range es {
				es[j] = scalar(strconv.Itoa(j))
			}
			ms[i] = kv("k"+strconv.Itoa(i), array(es...))
			product = min(product*n, snsMaxCombinations+1)
		}
		_, err := Compile(object(ms...).bytes(), SNS{Scope: MessageAttributes})
		switch {
		case keys > snsMaxKeys:
			if err == nil || err.Cause != (LimitExceeded{Limit: LimitKeys}) {
				t.Fatalf("%d keys: %#v, want the key limit", keys, err)
			}
		case product > snsMaxCombinations:
			if err == nil || err.Cause != (LimitExceeded{Limit: LimitCombinations}) {
				t.Fatalf("%v values: %#v, want the combinations limit", counts, err)
			}
		case err != nil:
			t.Fatalf("%v values rejected: %s", counts, err.Detail)
		}
	})
}

// EventBridge rejects a pattern whose "$or" arrays multiply out to more than
// 1000 combinations, each array in its own nested object, and names the
// limit.
func TestPropertyEventBridgeOrCombinations(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		sizes := rapid.SliceOfN(rapid.IntRange(2, 12), 1, 3).Draw(t, "$or sizes")
		ms := make([]member, len(sizes))
		product := 1
		for i, n := range sizes {
			branches := make([]node, n)
			for j := range branches {
				branches[j] = object(kv("k"+strconv.Itoa(j), array(scalar(`"1"`))))
			}
			ms[i] = kv("o"+strconv.Itoa(i), object(kv("$or", array(branches...))))
			product *= n
		}
		pattern := object(ms...)
		_, err := Compile(pattern.bytes(), EventBridge{})
		switch {
		case product > ebMaxOrCombinations:
			if err == nil || err.Cause != (LimitExceeded{Limit: LimitCombinations}) {
				t.Fatalf("%v ($or product %d): %#v, want the combinations limit", sizes, product, err)
			}
		case err != nil:
			t.Fatalf("%v ($or product %d) rejected: %s", sizes, product, err.Detail)
		}
	})
}

// SNS rejects a wildcard pattern with more than 3 "*" and a field over 100
// wildcard points (sns.md 6.2: a pattern scores 1 for one "*" and 3 per "*"
// for more, times the field's pattern count), and names the limit.
func TestPropertySNSWildcardLimits(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		stars := rapid.SliceOfN(rapid.IntRange(1, 4), 1, 12).Draw(t, "stars per pattern")
		rules := make([]node, len(stars))
		points, tooMany := 0, false
		for i, n := range stars {
			rules[i] = scalar(`{"wildcard":"p` + strconv.Itoa(i) + strings.Repeat("*x", n) + `"}`)
			tooMany = tooMany || n > snsMaxWildcardsPerGlob
			if n > 1 {
				points += 3 * n
			} else {
				points++
			}
		}
		points *= len(stars)
		pattern := object(kv("f", array(rules...)))
		_, err := Compile(pattern.bytes(), SNS{Scope: MessageAttributes})
		switch {
		case tooMany:
			if err == nil || err.Cause != (LimitExceeded{Limit: LimitWildcardsPerPattern}) {
				t.Fatalf("stars %v: %#v, want the wildcards-per-pattern limit", stars, err)
			}
		case points > snsMaxWildcardPoints:
			if err == nil || err.Cause != (LimitExceeded{Limit: LimitWildcardComplexity}) {
				t.Fatalf("stars %v (%d points): %#v, want the wildcard complexity limit", stars, points, err)
			}
		case err != nil:
			t.Fatalf("stars %v (%d points) rejected: %s", stars, points, err.Detail)
		}
	})
}

// Compile and Match never panic on arbitrary input, every rejection has a
// cause and a detail, and every Match failure is an *EventError.
func TestPropertyArbitraryInput(t *testing.T) {
	t.Parallel()
	alphabet := rapid.SampledFrom([]string{
		`{`, `}`, `[`, `]`, `,`, `:`, `"a"`, `"$or"`, `"numeric"`, `"anything-but"`, `"prefix"`, `"wildcard"`,
		`"*\\"`, `1`, `-0.5e3`, `true`, `null`, ` `, `"`, `\u`, `x`,
	})
	exists := mustCompile(t, `{"a":[{"exists":true}]}`, EventBridge{})
	rapid.Check(t, func(t *rapid.T) {
		src := []byte(strings.Join(rapid.SliceOfN(alphabet, 0, 40).Draw(t, "tokens"), ""))
		patterns := []*Pattern{exists}
		for _, p := range []Profile{EventBridge{}, SNS{}, SNS{Scope: MessageBody}, LambdaESM{}} {
			c, err := Compile(src, p)
			if err != nil {
				if err.Cause == nil || err.Detail == "" || err.Message() == "" {
					t.Fatalf("%T: Compile(%q) = %#v, an error without a cause or text", p, src, err)
				}
				continue
			}
			if p.input() == InputEvent {
				patterns = append(patterns, c)
			}
		}
		for _, c := range patterns {
			if _, err := Match(c, src); err != nil {
				if ee, ok := errors.AsType[*EventError](err); !ok || ee.Detail == "" {
					t.Fatalf("Match(%q) error %T, want *EventError", src, err)
				}
			}
		}
	})
}
