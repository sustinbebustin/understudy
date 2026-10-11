package filterpolicy_test

import (
	"encoding/json"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/service/sns/filterpolicy"
)

// kind is a value's JSON type. Equality never crosses kinds: the string
// "true" is not the literal true (sns.md 6.2).
type kind uint8

const (
	kindString kind = iota
	kindNumber
	// kindLiteral is null, true or false, which only String.Array elements
	// and body members can carry (sns.md 4.2).
	kindLiteral
)

// value is one value a message offers a policy key: a string, a number
// with the text it was written in, or a literal by its JSON text.
type value struct {
	kind kind
	text string
	n    float64
}

// rule is one element of a policy key's rule array, with the reference
// semantics of sns.md 6.2 and 6.3 (and Ruler's, matching-engines.md 4):
// operators other than exists hold when any value satisfies them, and only
// exists:false holds for a key without values.
type rule struct {
	json    string
	exists  *bool
	accepts func(v value) bool
}

func (r rule) holds(vs []value) bool {
	if r.exists != nil {
		return *r.exists == (len(vs) > 0)
	}
	return slices.ContainsFunc(vs, r.accepts)
}

// policyKey is a key and its rules, which hold when any rule does.
type policyKey struct {
	name  string
	rules []rule
}

func (k policyKey) holds(attrs map[string][]value) bool {
	return slices.ContainsFunc(k.rules, func(r rule) bool { return r.holds(attrs[k.name]) })
}

// policy is a flat policy: every key must hold, and, with "$or", the keys
// of at least one member.
type policy struct {
	keys []policyKey
	or   [][]policyKey
}

func (p policy) matches(attrs map[string][]value) bool {
	all := func(ks []policyKey) bool {
		return !slices.ContainsFunc(ks, func(k policyKey) bool { return !k.holds(attrs) })
	}
	if !all(p.keys) {
		return false
	}
	return len(p.or) == 0 || slices.ContainsFunc(p.or, all)
}

func (p policy) json() string {
	object := func(ks []policyKey) []string {
		out := make([]string, 0, len(ks))
		for _, k := range ks {
			rules := make([]string, len(k.rules))
			for i, r := range k.rules {
				rules[i] = r.json
			}
			out = append(out, strconv.Quote(k.name)+":["+strings.Join(rules, ",")+"]")
		}
		return out
	}
	members := object(p.keys)
	if len(p.or) > 0 {
		branches := make([]string, len(p.or))
		for i, b := range p.or {
			branches[i] = "{" + strings.Join(object(b), ",") + "}"
		}
		members = append(members, `"$or":[`+strings.Join(branches, ",")+"]")
	}
	return "{" + strings.Join(members, ",") + "}"
}

var (
	// words include IP addresses for the cidr rules, and strings whose text
	// equals a number or a literal, so a rule of one kind meets a value of
	// another with the same text.
	words = []string{
		"x", "xy", "Xy", "yx", "ab", "rugby", "10.0.0.7", "10.0.1.7", "192.168.4.2", "2001:db8::1",
		"true", "null", "100", "1", "1e2",
	}
	affixes = []string{"x", "y", "X", "ab", "r"}
	// globs are wildcard operands with one "*" each, which keeps a policy
	// inside the SNS wildcard budget of 100 points (sns.md 6.5), and no
	// other character path.Match treats specially.
	globs = []string{"*", "x*", "*y", "r*y", "X*", "10.0.*"}
	cidrs = []string{"10.0.0.0/24", "10.0.0.0/16", "192.168.0.0/16", "2001:db8::/32"}
	// numberTexts are JSON numbers, some of them equal in other forms.
	numberTexts = []string{"1", "2.5", "100", "-3", "1e2", "1.0e2", "0"}
	literals    = []string{"null", "true", "false"}
)

func number(text string) value {
	n, err := strconv.ParseFloat(text, 64)
	if err != nil {
		panic(err)
	}
	return value{kind: kindNumber, text: text, n: n}
}

func isString(v value) bool { return v.kind == kindString }

func isNumber(v value) bool { return v.kind == kindNumber }

// globMatches is the wildcard reference: "*" is any run of characters,
// and every other character matches itself.
func globMatches(pattern string, v value) bool {
	matched, err := path.Match(pattern, v.text)
	if err != nil {
		panic(err)
	}
	return isString(v) && matched
}

// inCIDR is the cidr reference: a string that is an IP address inside the
// range.
func inCIDR(cidr string, v value) bool {
	addr, err := netip.ParseAddr(v.text)
	return isString(v) && err == nil && netip.MustParsePrefix(cidr).Contains(addr)
}

// ruleGen draws one rule from the operators sns.md 6.2 lists for the
// MessageAttributes scope.
func ruleGen() *rapid.Generator[rule] {
	return rapid.Custom(func(t *rapid.T) rule {
		switch rapid.IntRange(0, 15).Draw(t, "op") {
		case 0:
			w := rapid.SampledFrom(words).Draw(t, "exact")
			return rule{json: strconv.Quote(w), accepts: func(v value) bool { return isString(v) && v.text == w }}
		case 1:
			n := number(rapid.SampledFrom(numberTexts).Draw(t, "bare"))
			return rule{json: n.text, accepts: func(v value) bool { return isNumber(v) && v.n == n.n }}
		case 2:
			a := rapid.SampledFrom(affixes).Draw(t, "prefix")
			return rule{json: `{"prefix":` + strconv.Quote(a) + `}`, accepts: func(v value) bool { return isString(v) && strings.HasPrefix(v.text, a) }}
		case 3:
			a := rapid.SampledFrom(affixes).Draw(t, "suffix")
			return rule{json: `{"suffix":` + strconv.Quote(a) + `}`, accepts: func(v value) bool { return isString(v) && strings.HasSuffix(v.text, a) }}
		case 4:
			w := rapid.SampledFrom(words).Draw(t, "ignore-case")
			return rule{json: `{"equals-ignore-case":` + strconv.Quote(w) + `}`, accepts: func(v value) bool { return isString(v) && strings.EqualFold(v.text, w) }}
		case 5:
			// anything-but excludes only values of its own type (V39, V40).
			ws := rapid.SliceOfNDistinct(rapid.SampledFrom(words), 1, 3, rapid.ID).Draw(t, "anything-but")
			quoted := make([]string, len(ws))
			for i, w := range ws {
				quoted[i] = strconv.Quote(w)
			}
			return rule{json: `{"anything-but":[` + strings.Join(quoted, ",") + `]}`, accepts: func(v value) bool { return !isString(v) || !slices.Contains(ws, v.text) }}
		case 6:
			a := rapid.SampledFrom(affixes).Draw(t, "anything-but prefix")
			return rule{json: `{"anything-but":{"prefix":` + strconv.Quote(a) + `}}`, accepts: func(v value) bool { return !isString(v) || !strings.HasPrefix(v.text, a) }}
		case 7:
			lo := rapid.IntRange(-5, 50).Draw(t, "lo")
			hi := rapid.IntRange(lo+1, 200).Draw(t, "hi")
			return rule{
				json:    `{"numeric":[">=",` + strconv.Itoa(lo) + `,"<",` + strconv.Itoa(hi) + `]}`,
				accepts: func(v value) bool { return isNumber(v) && v.n >= float64(lo) && v.n < float64(hi) },
			}
		case 8:
			n := number(rapid.SampledFrom(numberTexts).Draw(t, "equals"))
			return rule{json: `{"numeric":["=",` + n.text + `]}`, accepts: func(v value) bool { return isNumber(v) && v.n == n.n }}
		case 9:
			lit := rapid.SampledFrom(literals).Draw(t, "literal")
			return rule{json: lit, accepts: func(v value) bool { return v.kind == kindLiteral && v.text == lit }}
		case 10:
			// A numeric anything-but excludes numbers equal to a member,
			// whatever their text, and only numbers.
			texts := rapid.SliceOfNDistinct(rapid.SampledFrom(numberTexts), 1, 3, rapid.ID).Draw(t, "anything-but numbers")
			return rule{json: `{"anything-but":[` + strings.Join(texts, ",") + `]}`, accepts: func(v value) bool {
				return !isNumber(v) || !slices.ContainsFunc(texts, func(text string) bool { return number(text).n == v.n })
			}}
		case 11:
			g := rapid.SampledFrom(globs).Draw(t, "wildcard")
			return rule{json: `{"wildcard":` + strconv.Quote(g) + `}`, accepts: func(v value) bool { return globMatches(g, v) }}
		case 12:
			// The anything-but forms exclude only strings, as case 6 does.
			g := rapid.SampledFrom(globs).Draw(t, "anything-but wildcard")
			return rule{json: `{"anything-but":{"wildcard":` + strconv.Quote(g) + `}}`, accepts: func(v value) bool { return !isString(v) || !globMatches(g, v) }}
		case 13:
			a := rapid.SampledFrom(affixes).Draw(t, "anything-but suffix")
			return rule{json: `{"anything-but":{"suffix":` + strconv.Quote(a) + `}}`, accepts: func(v value) bool { return !isString(v) || !strings.HasSuffix(v.text, a) }}
		case 14:
			c := rapid.SampledFrom(cidrs).Draw(t, "cidr")
			return rule{json: `{"cidr":` + strconv.Quote(c) + `}`, accepts: func(v value) bool { return inCIDR(c, v) }}
		}
		exists := rapid.Bool().Draw(t, "exists")
		return rule{json: `{"exists":` + strconv.FormatBool(exists) + `}`, exists: &exists}
	})
}

func keysGen(names []string, maxKeys int) *rapid.Generator[[]policyKey] {
	return rapid.Custom(func(t *rapid.T) []policyKey {
		chosen := rapid.SliceOfNDistinct(rapid.SampledFrom(names), 1, maxKeys, rapid.ID).Draw(t, "keys")
		out := make([]policyKey, len(chosen))
		for i, name := range chosen {
			out[i] = policyKey{name: name, rules: rapid.SliceOfN(ruleGen(), 1, 3).Draw(t, name)}
		}
		return out
	})
}

func policyGen() *rapid.Generator[policy] {
	return rapid.Custom(func(t *rapid.T) policy {
		p := policy{keys: keysGen([]string{"a", "b"}, 2).Draw(t, "top")}
		if rapid.Bool().Draw(t, "with $or") {
			p.or = rapid.SliceOfN(keysGen([]string{"c", "d"}, 1), 2, 3).Draw(t, "$or")
		}
		return p
	})
}

// message is a set of attributes and the reference view of them.
type message struct {
	attrs  []delivery.MessageAttribute
	values map[string][]value
}

func messageGen() *rapid.Generator[message] {
	return rapid.Custom(func(t *rapid.T) message {
		m := message{values: map[string][]value{}}
		for _, name := range rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"a", "b", "c", "d", "e"}), 0, 5, rapid.ID).Draw(t, "names") {
			var av delivery.AttributeValue
			switch rapid.IntRange(0, 3).Draw(t, name) {
			case 0:
				w := rapid.SampledFrom(words).Draw(t, name+" string")
				av, m.values[name] = delivery.StringAttribute{Value: w}, []value{{text: w}}
			case 1:
				n := rapid.SampledFrom(numberTexts).Draw(t, name+" number")
				av, m.values[name] = delivery.NumberAttribute{Value: n}, []value{number(n)}
			case 2:
				var elems []string
				for i := range rapid.IntRange(0, 3).Draw(t, name+" length") {
					switch rapid.SampledFrom([]kind{kindString, kindNumber, kindLiteral}).Draw(t, name+" element kind "+strconv.Itoa(i)) {
					case kindNumber:
						n := rapid.SampledFrom(numberTexts).Draw(t, name+" element")
						elems, m.values[name] = append(elems, n), append(m.values[name], number(n))
					case kindLiteral:
						lit := rapid.SampledFrom(literals).Draw(t, name+" element")
						elems, m.values[name] = append(elems, lit), append(m.values[name], value{kind: kindLiteral, text: lit})
					case kindString:
						w := rapid.SampledFrom(words).Draw(t, name+" element")
						elems, m.values[name] = append(elems, strconv.Quote(w)), append(m.values[name], value{text: w})
					}
				}
				av = delivery.StringAttribute{Label: "Array", Value: "[" + strings.Join(elems, ",") + "]"}
			default:
				av = delivery.BinaryAttribute{Value: []byte{1}}
			}
			m.attrs = append(m.attrs, delivery.MessageAttribute{Name: name, Value: av})
		}
		return m
	})
}

// body is the message as a JSON body with the same values: a Number is a
// JSON number, a String.Array a JSON array, and a Binary is left out.
func (m message) body() string {
	members := make([]string, 0, len(m.attrs))
	for _, a := range m.attrs {
		switch v := a.Value.(type) {
		case delivery.StringAttribute:
			if v.Label == "Array" {
				members = append(members, strconv.Quote(a.Name)+":"+v.Value)
				continue
			}
			members = append(members, strconv.Quote(a.Name)+":"+strconv.Quote(v.Value))
		case delivery.NumberAttribute:
			members = append(members, strconv.Quote(a.Name)+":"+v.Value)
		case delivery.BinaryAttribute:
		}
	}
	return "{" + strings.Join(members, ",") + "}"
}

// TestPropertyMatchesReference: a flat policy matches a message exactly when
// the reference evaluator says it does, in the MessageAttributes scope over
// typed attributes and in the MessageBody scope over the same values as a
// JSON body (sns.md Implications 3).
func TestPropertyMatchesReference(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		p := policyGen().Draw(t, "policy")
		m := messageGen().Draw(t, "message")
		want := p.matches(m.values)
		for _, scope := range []filterpolicy.Scope{filterpolicy.MessageAttributes, filterpolicy.MessageBody} {
			compiled, err := filterpolicy.Compile(p.json(), scope)
			if err != nil {
				t.Fatalf("Compile(%s, %s): %q", p.json(), scope, err.Reason)
			}
			got, merr := compiled.Match(m.attrs, m.body())
			if merr != nil || got != want {
				t.Fatalf("%s scope %s against %+v (body %s): Match = %v, %v; reference %v", p.json(), scope, m.attrs, m.body(), got, merr, want)
			}
		}
	})
}

// mutate edits a policy text once: it drops, duplicates or inserts a byte
// from the JSON alphabet, so the result is often almost valid.
func mutate(t *rapid.T, s string) string {
	if s == "" {
		return s
	}
	i := rapid.IntRange(0, len(s)-1).Draw(t, "at")
	switch rapid.IntRange(0, 2).Draw(t, "edit") {
	case 0:
		return s[:i] + s[i+1:]
	case 1:
		return s[:i] + s[i:i+1] + s[i:]
	}
	b := rapid.SampledFrom([]string{"{", "}", "[", "]", ",", ":", `"`, "1", "-", "e", "$", "\\", " ", "null"}).Draw(t, "byte")
	return s[:i] + b + s[i:]
}

// TestPropertyCompileNeverPanics: any text, valid or a near miss, compiles
// or is refused with an SNS reason (sns.md 6.5).
func TestPropertyCompileNeverPanics(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		var text string
		switch rapid.IntRange(0, 2).Draw(t, "source") {
		case 0:
			text = rapid.String().Draw(t, "text")
		case 1:
			text = policyGen().Draw(t, "policy").json()
		default:
			text = policyGen().Draw(t, "policy").json()
			for range rapid.IntRange(1, 4).Draw(t, "edits") {
				text = mutate(t, text)
			}
		}
		scope := rapid.SampledFrom([]filterpolicy.Scope{filterpolicy.MessageAttributes, filterpolicy.MessageBody}).Draw(t, "scope")
		p, err := filterpolicy.Compile(text, scope)
		switch {
		case err != nil && p != nil:
			t.Fatalf("Compile(%q) returned a policy and %q", text, err.Reason)
		case err != nil:
			if !strings.HasPrefix(err.Reason, "FilterPolicy: ") && err.Reason != "Filter policy scope MessageAttributes does not support nested filter policy" {
				t.Fatalf("Compile(%q) reason %q is not an SNS FilterPolicy reason", text, err.Reason)
			}
		case p == nil:
			t.Fatalf("Compile(%q) returned neither a policy nor an error", text)
		case !json.Valid([]byte(text)):
			t.Fatalf("Compile accepted %q, which is not JSON", text)
		}
	})
}
