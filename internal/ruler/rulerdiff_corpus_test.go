package ruler

import (
	"encoding/json"
	"testing"
)

// diffCase is one pattern and one event the differential harness
// (rulerdiff_test.go) gives both understudy and Java Ruler.
type diffCase struct {
	pattern, event string
}

// diffCases is the differential corpus: the seed vectors, the IP range
// vectors, the Ruler defects and every pattern of each diffGroups entry
// crossed with its events.
func diffCases() []diffCase {
	cases := make([]diffCase, 0, len(seedVectors)+len(ipRangeVectors)+len(rulerDefects))
	for _, v := range seedVectors {
		cases = append(cases, diffCase{v.pattern, v.event})
	}
	for _, v := range ipRangeVectors {
		cases = append(cases, diffCase{v.pattern, v.event})
	}
	for _, v := range rulerDefects {
		cases = append(cases, diffCase{v.pattern, v.event})
	}
	for _, g := range diffGroups {
		for _, p := range g.patterns {
			for _, e := range g.events {
				cases = append(cases, diffCase{p, e})
			}
		}
	}
	return cases
}

// diffGroups are patterns around one operator or feature, each crossed
// with events chosen to sit on either side of its edges. Invalid patterns
// are in the groups too: the harness compares the rejection text.
var diffGroups = []struct {
	name             string
	patterns, events []string
}{
	{
		"bare values",
		[]string{
			`{"a":["x"]}`, `{"a":[""]}`, `{"a":[null]}`, `{"a":[true]}`, `{"a":[false]}`,
			`{"a":[1]}`, `{"a":[-0]}`, `{"a":[1.5e1]}`, `{"a":["x",1,null,true]}`,
			`{"a":["10.0.0.1"]}`, `{"a":["::ffff:10.0.0.1"]}`, `{"a":{"b":["x"]}}`, `{"a.b":["x"]}`,
			`{"a":[]}`, `{"a":{}}`, `{}`, `[]`, `{"a":"x"}`, `{"a":[["x"]]}`, `{"a":[{}]}`,
		},
		[]string{
			`{"a":"x"}`, `{"a":""}`, `{"a":null}`, `{"a":true}`, `{"a":false}`, `{"a":1}`, `{"a":0}`,
			`{"a":15}`, `{"a":"15"}`, `{"a":"10.0.0.1"}`, `{"a":{"b":"x"}}`, `{"a":["y","x"]}`, `{"b":"x"}`, `{}`,
		},
	},
	{
		"prefix and suffix",
		[]string{
			`{"a":[{"prefix":"ab"}]}`, `{"a":[{"suffix":"yz"}]}`, `{"a":[{"prefix":""}]}`, `{"a":[{"suffix":""}]}`,
			`{"a":[{"prefix":1}]}`, `{"a":[{"prefix":["ab"]}]}`, `{"a":[{"prefix":"é"}]}`, `{"a":[{"suffix":"😀"}]}`,
			`{"a":[{"prefix":"ab","suffix":"yz"}]}`, `{"a":[{"prefix":"ab"},{"suffix":"yz"}]}`,
		},
		[]string{
			`{"a":"abyz"}`, `{"a":"ab"}`, `{"a":"a"}`, `{"a":"ABYZ"}`, `{"a":"xyz"}`, `{"a":"é"}`, `{"a":"x😀"}`,
			`{"a":12}`, `{"a":["q","abc"]}`, `{"a":null}`, `{}`,
		},
	},
	{
		"equals-ignore-case",
		[]string{
			`{"a":[{"equals-ignore-case":"Hello"}]}`, `{"a":[{"equals-ignore-case":""}]}`,
			`{"a":[{"equals-ignore-case":"ß"}]}`, `{"a":[{"equals-ignore-case":"ǅ"}]}`, `{"a":[{"equals-ignore-case":"İ"}]}`,
			`{"a":[{"equals-ignore-case":"σ"}]}`, `{"a":[{"equals-ignore-case":1}]}`, `{"a":[{"equals-ignore-case":["x"]}]}`,
			`{"a":[{"prefix":{"equals-ignore-case":"AB"}}]}`, `{"a":[{"suffix":{"equals-ignore-case":"YZ"}}]}`,
			`{"a":[{"prefix":{"equals-ignore-case":""}}]}`, `{"a":[{"suffix":{"equals-ignore-case":1}}]}`,
			`{"a":[{"prefix":{"equals-ignore-case":"ss"}}]}`, `{"a":[{"prefix":{"nope":"x"}}]}`,
		},
		[]string{
			`{"a":"hello"}`, `{"a":"HELLO"}`, `{"a":""}`, `{"a":"SS"}`, `{"a":"ss"}`, `{"a":"ß"}`, `{"a":"ǅ"}`, `{"a":"ǆ"}`,
			`{"a":"i̇"}`, `{"a":"İ"}`, `{"a":"Σ"}`, `{"a":"ς"}`, `{"a":"abYz"}`, `{"a":"SSx"}`, `{"a":"ẞ"}`, `{"a":1}`,
		},
	},
	{
		"wildcard",
		[]string{
			`{"a":[{"wildcard":"a*z"}]}`, `{"a":[{"wildcard":"*"}]}`, `{"a":[{"wildcard":""}]}`, `{"a":[{"wildcard":"*b*"}]}`,
			`{"a":[{"wildcard":"a\\*z"}]}`, `{"a":[{"wildcard":"a\\\\z"}]}`, `{"a":[{"wildcard":"a**z"}]}`,
			`{"a":[{"wildcard":"a\\"}]}`, `{"a":[{"wildcard":"a\\b"}]}`, `{"a":[{"wildcard":1}]}`, `{"a":[{"wildcard":["a*"]}]}`,
			`{"a":[{"wildcard":"*.*.*"}]}`,
		},
		[]string{
			`{"a":"az"}`, `{"a":"abz"}`, `{"a":"a*z"}`, `{"a":"a\\z"}`, `{"a":""}`, `{"a":"b"}`, `{"a":"x.y.z"}`,
			`{"a":"Az"}`, `{"a":5}`, `{"a":["q","abz"]}`, `{}`,
		},
	},
	{
		"anything-but",
		[]string{
			`{"a":[{"anything-but":"x"}]}`, `{"a":[{"anything-but":1}]}`, `{"a":[{"anything-but":["x","y"]}]}`,
			`{"a":[{"anything-but":[1,2.0]}]}`, `{"a":[{"anything-but":[]}]}`, `{"a":[{"anything-but":null}]}`,
			`{"a":[{"anything-but":true}]}`, `{"a":[{"anything-but":["x",null]}]}`, `{"a":[{"anything-but":{}}]}`,
			`{"a":[{"anything-but":{"prefix":"ab"}}]}`, `{"a":[{"anything-but":{"prefix":["ab","cd"]}}]}`,
			`{"a":[{"anything-but":{"suffix":"yz"}}]}`, `{"a":[{"anything-but":{"suffix":""}}]}`,
			`{"a":[{"anything-but":{"equals-ignore-case":"X"}}]}`, `{"a":[{"anything-but":{"equals-ignore-case":["X","ß"]}}]}`,
			`{"a":[{"anything-but":{"wildcard":"a*"}}]}`, `{"a":[{"anything-but":{"wildcard":["a*","*z"]}}]}`,
			`{"a":[{"anything-but":{"wildcard":"a**"}}]}`, `{"a":[{"anything-but":{"numeric":[">",1]}}]}`,
			`{"a":[{"anything-but":{"prefix":"a","suffix":"z"}}]}`, `{"a":[{"anything-but":{"prefix":1}}]}`,
			`{"a":[{"anything-but":"x"},{"exists":false}]}`,
		},
		[]string{
			`{"a":"x"}`, `{"a":"X"}`, `{"a":"y"}`, `{"a":"abc"}`, `{"a":"xyz"}`, `{"a":"SS"}`, `{"a":1}`, `{"a":2}`,
			`{"a":"1"}`, `{"a":null}`, `{"a":true}`, `{"a":["x","q"]}`, `{"a":["x"]}`, `{"a":[]}`, `{}`,
		},
	},
	{
		"numeric",
		[]string{
			`{"a":[{"numeric":["=",5]}]}`, `{"a":[{"numeric":["<",5]}]}`, `{"a":[{"numeric":["<=",5]}]}`,
			`{"a":[{"numeric":[">",-5.5]}]}`, `{"a":[{"numeric":[">=",0,"<",1e3]}]}`, `{"a":[{"numeric":[">",0,"<=",0.1]}]}`,
			`{"a":[{"numeric":[">",5,">",6]}]}`, `{"a":[{"numeric":["<",5,">",1]}]}`, `{"a":[{"numeric":[">",5,"<",5]}]}`,
			`{"a":[{"numeric":["=","5"]}]}`, `{"a":[{"numeric":["==",5]}]}`, `{"a":[{"numeric":[]}]}`,
			`{"a":[{"numeric":[">"]}]}`, `{"a":[{"numeric":5}]}`, `{"a":[{"numeric":["=",5,"<",9]}]}`,
			`{"a":[{"numeric":["<",-1e-3]}]}`, `{"a":[{"numeric":["=",5000000000]}]}`, `{"a":[{"numeric":["=",1e-325]}]}`,
		},
		[]string{
			`{"a":5}`, `{"a":5.0}`, `{"a":4.999}`, `{"a":-5.5}`, `{"a":0}`, `{"a":-0.0}`, `{"a":0.1}`, `{"a":1e3}`,
			`{"a":999.9999999}`, `{"a":-0.002}`, `{"a":5e9}`, `{"a":"5"}`, `{"a":[3,7]}`, `{"a":1e400}`, `{}`,
		},
	},
	{
		"cidr",
		[]string{
			`{"a":[{"cidr":"10.0.0.0/24"}]}`, `{"a":[{"cidr":"10.0.0.0/32"}]}`, `{"a":[{"cidr":"10.0.0.1/8"}]}`,
			`{"a":[{"cidr":"2001:db8::/32"}]}`, `{"a":[{"cidr":"2001:db8::1/128"}]}`, `{"a":[{"cidr":"::/0"}]}`,
			`{"a":[{"cidr":"10.0.0.0/33"}]}`, `{"a":[{"cidr":"10.0.0.0"}]}`, `{"a":[{"cidr":"10.0.0.0/x"}]}`,
			`{"a":[{"cidr":"bad/8"}]}`, `{"a":[{"cidr":1}]}`, `{"a":[{"cidr":"::ffff:10.0.0.0/120"}]}`,
			`{"a":[{"cidr":"0.0.0.0/0"}]}`, `{"a":[{"cidr":"0.0.0.0/1"}]}`, `{"a":[{"cidr":"32.0.0.0/8"}]}`,
			`{"a":[{"cidr":"::/1"}]}`, `{"a":[{"cidr":"255.255.255.0/24"}]}`, `{"a":[{"cidr":"::1/127"}]}`,
			`{"a":["16.0.0.1"]}`, `{"a":["255.255.255.255"]}`,
			`{"a":[{"cidr":"0.0.0.0/1"},{"numeric":[">",100]}]}`, `{"a":[{"cidr":"0.0.0.0/1"},7]}`,
			`{"a":[{"cidr":"0.0.0.0/1"},12345678901234567890]}`, `{"a":[{"cidr":"0.0.0.0/1"},{"anything-but":[7]}]}`,
			`{"a":[{"cidr":"0.0.0.0/1"},{"anything-but":"7"}]}`,
		},
		[]string{
			`{"a":"10.0.0.1"}`, `{"a":"10.0.0.0"}`, `{"a":"10.0.1.0"}`, `{"a":"10.255.255.255"}`, `{"a":"2001:db8::1"}`,
			`{"a":"2001:DB8:0:0:0:0:0:1"}`, `{"a":"2001:db9::"}`, `{"a":"::ffff:10.0.0.1"}`, `{"a":"x"}`, `{"a":10}`, `{}`,
			`{"a":"::1"}`, `{"a":"::"}`, `{"a":"255.255.255.255"}`, `{"a":"fe80::1"}`, `{"a":5}`, `{"a":2050}`,
			`{"a":10000001}`, `{"a":1E5}`, `{"a":"0A5"}`, `{"a":-5}`, `{"a":7.5}`, `{"a":1e400}`, `{"a":true}`, `{"a":null}`,
		},
	},
	{
		"cidr beside other rules at its path",
		[]string{
			`{"$or":[{"a":[{"cidr":"0.0.0.0/1"}]},{"a":[7]}]}`, `{"$or":[{"a":[{"cidr":"0.0.0.0/1"}]},{"b":["x"],"a":[7]}]}`,
			`{"$or":[{"a":[{"cidr":"0.0.0.0/1"}]},{"a":[{"numeric":["<",0]}]}]}`,
		},
		[]string{`{"a":5}`, `{"a":5,"b":"x"}`, `{"a":"10.0.0.1"}`},
	},
	{
		"exists",
		[]string{
			`{"a":[{"exists":true}]}`, `{"a":[{"exists":false}]}`, `{"a":{"b":[{"exists":true}]}}`,
			`{"a":{"b":[{"exists":false}]}}`, `{"a":[{"exists":"true"}]}`, `{"a":[{"exists":true},"x"]}`,
			`{"a":[{"exists":false}],"c":["x"]}`, `{"a":[{"exists":false},{"exists":true}]}`,
		},
		[]string{
			`{"a":"x"}`, `{"a":null}`, `{"a":[]}`, `{"a":[null]}`, `{"a":{}}`, `{"a":{"b":1}}`, `{"a":{"c":1}}`,
			`{"c":"x"}`, `{"a":"y","c":"x"}`, `{}`,
		},
	},
	{
		"$or",
		[]string{
			`{"$or":[{"a":["x"]},{"b":["y"]}]}`, `{"c":["z"],"$or":[{"a":["x"]},{"b":["y"]}]}`,
			`{"$or":[{"a":["x"]},{"$or":[{"b":["y"]},{"c":["z"]}]}]}`, `{"n":{"$or":[{"a":["x"]},{"b":["y"]}]}}`,
			`{"$or":[{"a":[{"numeric":[">",1]}]},{"a":[{"prefix":"x"}]}]}`, `{"$or":[{"a":["x"]}]}`,
			`{"$or":{"a":["x"]}}`, `{"$or":["x","y"]}`, `{"$or":[{},{"a":["x"]}]}`, `{"$or":[{"a":["x"]},"x"]}`,
			`{"$or":[{"numeric":[">",1]},{"a":["x"]}]}`, `{"$or":[{"a":["x"]},{"a":[{"exists":false}]}]}`,
			`{"a":["q"],"$or":[{"a":["x"]},{"b":["y"]}]}`,
		},
		[]string{
			`{"a":"x"}`, `{"b":"y"}`, `{"c":"z"}`, `{"a":"x","c":"z"}`, `{"n":{"b":"y"}}`, `{"a":5}`, `{"a":"xy"}`,
			`{"a":"q","b":"y"}`, `{"$or":"x"}`, `{}`,
		},
	},
	{
		"arrays and array consistency",
		[]string{
			`{"r":{"a":["1"],"b":["2"]}}`, `{"r":{"a":["1"]},"s":{"b":["2"]}}`, `{"r":{"a":["1"],"b":[{"exists":false}]}}`,
			`{"r":{"a":[{"numeric":[">",0]}],"b":[{"prefix":"2"}]}}`, `{"a":["1"]}`, `{"r":{"q":{"a":["1"],"b":["2"]}}}`,
		},
		[]string{
			`{"r":[{"a":"1","b":"2"}]}`, `{"r":[{"a":"1"},{"b":"2"}]}`, `{"r":[{"a":"1"},{"a":"3","b":"2"}]}`,
			`{"r":[[{"a":"1","b":"2"}]]}`, `{"r":[[{"a":"1"}],[{"b":"2"}]]}`, `{"a":[["1"],["2"]]}`, `{"a":[[[1]]]}`,
			`{"r":[{"q":[{"a":"1"},{"b":"2"}]}]}`, `{"r":[{"q":[{"a":"1","b":"2"}]}]}`, `{"r":{"a":"1"},"s":[{"b":"2"},{"b":"3"}]}`,
			`{"r":[{"a":1,"b":"20"}]}`,
		},
	},
	{
		"malformed JSON",
		[]string{
			`{"a":["x"]`, `{"a":["x"]}}`, `{"a":["x"],}`, `{'a':["x"]}`, `{"a":[x]}`, `{"a":["x"]} {}`, ``, `{"a":[01]}`,
			`{"a":["\x"]}`, `{"a":[1e]}`, `{"a":[NaN]}`, `{"a" ["x"]}`, `{"a":["x"]}garbage`,
		},
		[]string{`{"a":"x"}`},
	},
	{
		"malformed events",
		[]string{`{"a":["x"]}`},
		[]string{`{"a":"x"`, `[{"a":"x"}]`, `"x"`, `{"a":"x"} {}`, `{"a":x}`, ``, `{"a":"x","a":"y"}`, `{"a":"y","a":"x"}`},
	},
	{
		"unknown and misspelled operators",
		[]string{
			`{"a":[{"EXISTS":true}]}`, `{"a":[{"nope":1}]}`, `{"a":[{"prefix":"a","nope":1}]}`,
			`{"a":[{"exists":true,"prefix":"a"}]}`,
		},
		[]string{`{"a":"abc"}`},
	},
	{
		// Ruler's own operator, which the EventBridge profile refuses.
		"exactly",
		[]string{`{"a":[{"exactly":"x"}]}`},
		[]string{`{"a":"x"}`},
	},
}

// TestDiffCorpusCoversEveryOperator keeps the differential corpus honest in
// the default test run: every operator form the grammar has appears in at
// least one corpus pattern, and some event nests an array in an array.
func TestDiffCorpusCoversEveryOperator(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	nested := false
	for _, c := range diffCases() {
		for _, op := range operatorsIn(t, c.pattern) {
			seen[op] = true
		}
		nested = nested || hasNestedArray(t, c.event)
	}
	for op := OperatorPrefix; op <= OperatorOr; op++ {
		if !seen[op.String()] {
			t.Errorf("no corpus pattern uses %s", op)
		}
	}
	if !nested {
		t.Error("no corpus event nests an array in an array")
	}
}

// operatorsIn names the operator forms of a pattern as Operator.String
// writes them. A pattern that is not JSON has none.
func operatorsIn(t *testing.T, pattern string) []string {
	t.Helper()
	var root any
	if json.Unmarshal([]byte(pattern), &root) != nil {
		return nil
	}
	var ops []string
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				if k == "$or" {
					ops = append(ops, OperatorOr.String())
				}
				if len(v) == 1 {
					ops = append(ops, k)
					if inner, ok := child.(map[string]any); ok && len(inner) == 1 {
						for ik := range inner {
							ops = append(ops, k+"/"+ik)
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(root)
	return ops
}

func hasNestedArray(t *testing.T, event string) bool {
	t.Helper()
	var root any
	if json.Unmarshal([]byte(event), &root) != nil {
		return false
	}
	var walk func(v any, inArray bool) bool
	walk = func(v any, inArray bool) bool {
		switch v := v.(type) {
		case map[string]any:
			for _, child := range v {
				if walk(child, false) {
					return true
				}
			}
		case []any:
			if inArray {
				return true
			}
			for _, child := range v {
				if walk(child, true) {
					return true
				}
			}
		}
		return false
	}
	return walk(root, false)
}
